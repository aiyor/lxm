package config

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/aiyor/lxm/internal/sockets"
)

// cloneYAMLNode shallow-copies a yaml.Node, dropping anchor/alias metadata so
// nodes reparented by Transform 9 never leak YAML anchor annotations or
// shared-pointer duplication into the compiled output.
func cloneYAMLNode(n *yaml.Node) *yaml.Node {
	if n == nil {
		return nil
	}
	c := *n
	c.Anchor = ""
	c.Alias = nil
	return &c
}

// filterRunnableScripts removes empty/comment-only entries from a scripts
// sequence node in place, so compiled output contains only runnable scripts
// (mirroring the loader's normalization).
func filterRunnableScripts(vNode *yaml.Node) {
	if vNode == nil || vNode.Kind != yaml.SequenceNode {
		return
	}
	var kept []*yaml.Node
	for _, scriptNode := range vNode.Content {
		if runnableScript(scriptNode.Value) {
			kept = append(kept, scriptNode)
		}
	}
	vNode.Content = kept
}

// collectAliasRefs returns the set of anchor names referenced by alias nodes
// anywhere in the document. Transform 9 uses it to decide whether an anchor
// annotation can be stripped from a re-emitted node without dangling an
// external reference (e.g. `replace: {recipes: [*g]}`).
func collectAliasRefs(root *yaml.Node) map[string]bool {
	refs := make(map[string]bool)
	var walk func(n *yaml.Node)
	walk = func(n *yaml.Node) {
		if n == nil {
			return
		}
		if n.Kind == yaml.AliasNode {
			refs[n.Value] = true
		}
		for _, c := range n.Content {
			walk(c)
		}
	}
	walk(root)
	return refs
}

var knownTopLevelKeys = map[string]bool{
	"schema":             true,
	"name":               true,
	"type":               true,
	"status":             true,
	"state":              true,
	"provider":           true,
	"remote":             true,
	"remotes":            true,
	"target":             true,
	"project":            true,
	"limits":             true,
	"vm":                 true,
	"disks":              true,
	"ports":              true,
	"sockets":            true,
	"vswitches":          true,
	"network_policy":     true,
	"wait":               true,
	"wait_config":        true,
	"image":              true,
	"user":               true,
	"groups":             true,
	"sudo":               true,
	"inject_ssh_keys":    true,
	"ssh_keys":           true,
	"cloud-init":         true,
	"cloud_init":         true,
	"cloud-init-file":    true,
	"cloud_init_file":    true,
	"cloud-init-include": true,
	"cloud_init_include": true,
	"network-config":     true,
	"network_config":     true,
	"mounts":             true,
	"networks":           true,
	"devices":            true,
	"profiles":           true,
	"recipes":            true,
	"vars":               true,
	"base":               true,
	"include":            true,
	"remove":             true,
	"replace":            true,
}

// MigrateManifest transforms a legacy v1 (or draft v2) YAML manifest into a canonical v2 manifest.
// Returns the migrated YAML bytes, any warnings, or a compile error (exit 3).
func MigrateManifest(rawYAML []byte) ([]byte, []string, error) {
	// UG5 B3: expand templates BEFORE migration and validation, so compile
	// accepts exactly what plan/apply accept. The loader expands
	// {{ .Env.* }}, {{ .Vars.* }}, {{ .Name }} (config.go loadConfig); without
	// the same step here, an env-templated `image` fails the #ImageRef CUE
	// pattern even when the variable is bound, and the compiled output is not
	// the "resolved" manifest the CLI reference promises. An unbound variable
	// fails with the loader's message (exit 3).
	var probe struct {
		Name string            `yaml:"name"`
		Vars map[string]string `yaml:"vars"`
	}
	if err := yaml.Unmarshal(rawYAML, &probe); err != nil {
		return nil, nil, fmt.Errorf("parsing YAML: %w", err)
	}
	if probe.Vars == nil {
		probe.Vars = make(map[string]string)
	}
	expanded, err := expandTemplatesPreserveEscapes(string(rawYAML), probe.Vars, probe.Name, "")
	if err != nil {
		return nil, nil, fmt.Errorf("template expansion: %w", err)
	}
	rawYAML = []byte(expanded)

	var root yaml.Node
	if err := yaml.Unmarshal(rawYAML, &root); err != nil {
		return nil, nil, fmt.Errorf("parsing YAML: %w", err)
	}

	if len(root.Content) == 0 {
		return nil, nil, fmt.Errorf("empty YAML document")
	}

	docNode := root.Content[0]
	if docNode.Kind != yaml.MappingNode {
		return nil, nil, fmt.Errorf("YAML document root must be a mapping")
	}

	// Anchors referenced from outside the recipes/mounts lists must survive
	// Transform 9's anchor stripping (e.g. `replace: {recipes: [*g]}`).
	aliasRefs := collectAliasRefs(&root)

	var warnings []string

	// 1. Scan top-level keys for unknown keys and existing schema
	var valNodes []*yaml.Node
	schemaIdx := -1
	hasCloudInit := false
	hasCloudInitFile := false
	hasSudo := false
	hasInjectSSH := false
	recipesIdx := -1
	mountsIdx := -1
	hasRelativeMountSource := false

	for i := 0; i < len(docNode.Content); i += 2 {
		keyNode := docNode.Content[i]
		valNode := docNode.Content[i+1]
		valNodes = append(valNodes, valNode)
		keyName := keyNode.Value

		if !knownTopLevelKeys[keyName] {
			warnings = append(warnings, fmt.Sprintf("CONFIG_WARN_UNKNOWN_KEY: unknown top-level key %q", keyName))
		}

		switch keyName {
		case "schema":
			schemaIdx = i / 2
			if valNode.Value != "" && valNode.Value != "lxm/config/v1" && valNode.Value != "lxm/config/v2" {
				return nil, nil, fmt.Errorf("CONFIG_ERROR: unknown or unsupported schema version %q", valNode.Value)
			}
		case "cloud-init", "cloud_init":
			hasCloudInit = true
		case "cloud-init-file", "cloud_init_file":
			hasCloudInitFile = true
		case "sudo":
			hasSudo = true
		case "inject_ssh_keys":
			hasInjectSSH = true
		case "recipes":
			recipesIdx = i / 2
		case "mounts":
			mountsIdx = i / 2
		}
	}

	// 4. Conflict check: cloud-init + cloud-init-file
	if hasCloudInit && hasCloudInitFile {
		return nil, nil, fmt.Errorf("CONFIG_ERROR_CONFLICT: both cloud-init and cloud-init-file specified in manifest")
	}

	// 5. Mount destination security check & sensitive path warnings
	if mountsIdx != -1 {
		mountsNode := valNodes[mountsIdx]
		if mountsNode.Kind == yaml.AliasNode && mountsNode.Alias != nil {
			mountsNode = mountsNode.Alias
		}

		// Collect (source, destination) pairs from either the list form or the
		// Style 2 map form (container path -> host source or object).
		var mountPairs [][2]string
		switch mountsNode.Kind {
		case yaml.SequenceNode:
			for _, elem := range mountsNode.Content {
				if elem.Kind == yaml.AliasNode && elem.Alias != nil {
					elem = elem.Alias
				}
				var destPath string
				var srcPath string
				switch elem.Kind {
				case yaml.ScalarNode:
					parts := strings.Split(elem.Value, ":")
					if len(parts) >= 2 {
						srcPath = parts[0]
						destPath = parts[1]
					}
				case yaml.MappingNode:
					for m := 0; m < len(elem.Content); m += 2 {
						k := elem.Content[m].Value
						v := elem.Content[m+1].Value
						switch k {
						case "path", "destination":
							destPath = v
						case "source":
							srcPath = v
						}
					}
				}
				mountPairs = append(mountPairs, [2]string{srcPath, destPath})
			}
		case yaml.MappingNode:
			for m := 0; m+1 < len(mountsNode.Content); m += 2 {
				destPath := mountsNode.Content[m].Value
				srcNode := mountsNode.Content[m+1]
				if srcNode.Kind == yaml.AliasNode && srcNode.Alias != nil {
					srcNode = srcNode.Alias
				}
				srcPath := ""
				switch srcNode.Kind {
				case yaml.ScalarNode:
					srcPath = srcNode.Value
				case yaml.MappingNode:
					for s := 0; s+1 < len(srcNode.Content); s += 2 {
						if srcNode.Content[s].Value == "source" {
							srcPath = srcNode.Content[s+1].Value
						}
					}
				}
				mountPairs = append(mountPairs, [2]string{srcPath, destPath})
			}
		}

		for _, pair := range mountPairs {
			srcPath, destPath := pair[0], pair[1]
			if srcPath != "" && (srcPath == "~" || strings.HasPrefix(srcPath, "~/") ||
				strings.HasPrefix(srcPath, "./") || strings.HasPrefix(srcPath, "../")) {
				hasRelativeMountSource = true
			}

			cleanDest := filepath.Clean(destPath)
			if cleanDest == "/" || cleanDest == "/proc" || cleanDest == "/sys" || cleanDest == "/dev" {
				return nil, nil, fmt.Errorf("CONFIG_ERROR_SECURITY: mount destination %q is restricted", destPath)
			}

			cleanSrc := filepath.Clean(srcPath)
			if strings.HasPrefix(cleanSrc, "/etc") || strings.HasPrefix(cleanSrc, "/proc") ||
				strings.HasPrefix(cleanSrc, "/sys") || strings.HasPrefix(cleanSrc, "/dev") ||
				strings.HasPrefix(cleanSrc, "/var/run/docker.sock") {
				warnings = append(warnings, fmt.Sprintf("SECURITY_WARNING: sensitive host mount source %q", srcPath))
			}
		}
	}

	// 6. Semantic default flip warnings
	if !hasSudo {
		warnings = append(warnings, "CONFIG_WARN_DEFAULT_FLIP: v2 default for sudo is false (opt-in); set 'sudo: true' to preserve legacy passwordless sudo")
	}
	if !hasInjectSSH {
		warnings = append(warnings, "CONFIG_WARN_DEFAULT_FLIP: v2 default for inject_ssh_keys is false (opt-in); set 'inject_ssh_keys: true' to enable auto host-key injection")
	}

	// 7. Transform 8 (F2): Recipe group script inspection & pruning
	if recipesIdx != -1 {
		recipesNode := valNodes[recipesIdx]
		if recipesNode.Kind == yaml.AliasNode && recipesNode.Alias != nil {
			recipesNode = recipesNode.Alias
		}
		if recipesNode.Kind == yaml.SequenceNode {
			var validGroups []*yaml.Node
			for _, grpNode := range recipesNode.Content {
				inspectNode := grpNode
				if inspectNode.Kind == yaml.AliasNode && inspectNode.Alias != nil {
					inspectNode = inspectNode.Alias
				}
				hasScripts := false
				if inspectNode.Kind == yaml.MappingNode {
					for r := 0; r < len(inspectNode.Content); r += 2 {
						k := inspectNode.Content[r].Value
						vNode := inspectNode.Content[r+1]
						if (k == "scripts" || k == "root") && vNode.Kind == yaml.SequenceNode {
							for _, scriptNode := range vNode.Content {
								cleanScript := strings.TrimSpace(scriptNode.Value)
								if cleanScript != "" && !strings.HasPrefix(cleanScript, "#") {
									hasScripts = true
									break
								}
							}
						}
					}
				} else if inspectNode.Kind == yaml.ScalarNode {
					if runnableScript(inspectNode.Value) {
						hasScripts = true
					}
				}

				if hasScripts {
					validGroups = append(validGroups, grpNode)
				} else {
					warnings = append(warnings, "CONFIG_WARN_EMPTY_RECIPE: pruned recipe group with empty or comment-only scripts")
				}
			}
			recipesNode.Content = validGroups
		}
	}

	// Transform 9 (D14): Normalize authoring shorthands into canonical object
	// form so compiled output matches #LXM_RESOLVED literally:
	//   - map-form mounts -> list of {source, path} objects
	//   - scalar / root: / scripts-only recipe groups -> run_as objects
	if mountsIdx != -1 {
		mountsNode := valNodes[mountsIdx]
		if mountsNode.Kind == yaml.AliasNode {
			if mountsNode.Alias == nil {
				mountsNode = nil
			} else {
				mountsNode = cloneYAMLNode(mountsNode.Alias)
			}
		}
		if mountsNode != nil && mountsNode.Kind == yaml.MappingNode {
			var normMounts []*yaml.Node
			for m := 0; m+1 < len(mountsNode.Content); m += 2 {
				pathNode := mountsNode.Content[m]
				valNode := mountsNode.Content[m+1]
				if valNode.Kind == yaml.AliasNode && valNode.Alias != nil {
					valNode = valNode.Alias
				}
				if valNode.Kind == yaml.ScalarNode {
					normMounts = append(normMounts, &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map", Content: []*yaml.Node{
						{Kind: yaml.ScalarNode, Tag: "!!str", Value: "source"},
						{Kind: yaml.ScalarNode, Tag: "!!str", Value: valNode.Value},
						{Kind: yaml.ScalarNode, Tag: "!!str", Value: "path"},
						{Kind: yaml.ScalarNode, Tag: "!!str", Value: pathNode.Value},
					}})
				} else if valNode.Kind == yaml.MappingNode {
					// Clone so shared/anchor-bearing nodes never leak YAML
					// anchor annotations into the compiled output.
					valNode = cloneYAMLNode(valNode)
					// Object value: ensure a path key matching the map key
					hasPath := false
					for s := 0; s+1 < len(valNode.Content); s += 2 {
						if valNode.Content[s].Value == "path" {
							hasPath = true
							break
						}
					}
					if !hasPath {
						valNode.Content = append([]*yaml.Node{
							{Kind: yaml.ScalarNode, Tag: "!!str", Value: "path"},
							{Kind: yaml.ScalarNode, Tag: "!!str", Value: pathNode.Value},
						}, valNode.Content...)
					}
					normMounts = append(normMounts, valNode)
				}
			}
			mountsNode.Kind = yaml.SequenceNode
			mountsNode.Tag = "!!seq"
			mountsNode.Value = ""
			mountsNode.Content = normMounts
		}
	}

	if recipesIdx != -1 {
		recipesNode := valNodes[recipesIdx]
		if recipesNode.Kind == yaml.SequenceNode {
			var normGroups []*yaml.Node
			for _, grpNode := range recipesNode.Content {
				if grpNode.Kind == yaml.AliasNode {
					if grpNode.Alias == nil {
						continue
					}
					// Clone so shared/anchor-bearing nodes never leak YAML
					// anchor annotations or duplicate definitions.
					grpNode = cloneYAMLNode(grpNode.Alias)
				}
				switch grpNode.Kind {
				case yaml.ScalarNode:
					// String shorthand -> {run_as: root, scripts: [path]}
					norm := &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map", Content: []*yaml.Node{
						{Kind: yaml.ScalarNode, Tag: "!!str", Value: "run_as"},
						{Kind: yaml.ScalarNode, Tag: "!!str", Value: "root"},
						{Kind: yaml.ScalarNode, Tag: "!!str", Value: "scripts"},
						{Kind: yaml.SequenceNode, Tag: "!!seq", Content: []*yaml.Node{
							{Kind: yaml.ScalarNode, Tag: "!!str", Value: grpNode.Value},
						}},
					}}
					if aliasRefs[grpNode.Anchor] {
						norm.Anchor = grpNode.Anchor
					}
					normGroups = append(normGroups, norm)
				case yaml.MappingNode:
					// Drop empty/comment-only script entries so compiled output
					// stays loadable by the strict authoring gate.
					for r := 0; r+1 < len(grpNode.Content); r += 2 {
						k := grpNode.Content[r].Value
						if k == "scripts" || k == "root" {
							filterRunnableScripts(grpNode.Content[r+1])
						}
					}
					hasRunAs := false
					hasScripts := false
					rootIdx := -1
					for r := 0; r+1 < len(grpNode.Content); r += 2 {
						switch grpNode.Content[r].Value {
						case "run_as", "run-as":
							hasRunAs = true
						case "scripts":
							hasScripts = true
						case "root":
							rootIdx = r
						}
					}
					switch {
					case rootIdx != -1 && !hasScripts && !hasRunAs:
						// root: shorthand -> {run_as: root, scripts: [...]}
						norm := &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map", Content: []*yaml.Node{
							{Kind: yaml.ScalarNode, Tag: "!!str", Value: "run_as"},
							{Kind: yaml.ScalarNode, Tag: "!!str", Value: "root"},
							{Kind: yaml.ScalarNode, Tag: "!!str", Value: "scripts"},
							grpNode.Content[rootIdx+1],
						}}
						if aliasRefs[grpNode.Anchor] {
							norm.Anchor = grpNode.Anchor
						}
						normGroups = append(normGroups, norm)
					case !hasRunAs && hasScripts:
						// Legacy scripts-only group -> insert run_as: root
						grpNode.Content = append([]*yaml.Node{
							{Kind: yaml.ScalarNode, Tag: "!!str", Value: "run_as"},
							{Kind: yaml.ScalarNode, Tag: "!!str", Value: "root"},
						}, grpNode.Content...)
						if !aliasRefs[grpNode.Anchor] {
							grpNode.Anchor = ""
						}
						normGroups = append(normGroups, grpNode)
					default:
						if !aliasRefs[grpNode.Anchor] {
							grpNode.Anchor = ""
						}
						normGroups = append(normGroups, grpNode)
					}
				default:
					normGroups = append(normGroups, grpNode)
				}
			}
			recipesNode.Content = normGroups
		}
	}

	// Transform 1 & 2: Ensure schema: lxm/config/v2 and canonicalize wait
	if schemaIdx != -1 {
		valNodes[schemaIdx].Value = "lxm/config/v2"
	} else {
		// Prepend schema: lxm/config/v2 to top of mapping
		newContent := []*yaml.Node{
			{Kind: yaml.ScalarNode, Value: "schema"},
			{Kind: yaml.ScalarNode, Value: "lxm/config/v2"},
		}
		docNode.Content = append(newContent, docNode.Content...)
	}

	// Re-evaluate wait node location after potential insert
	for i := 0; i < len(docNode.Content); i += 2 {
		switch docNode.Content[i].Value {
		case "wait":
			vNode := docNode.Content[i+1]
			if vNode.Kind == yaml.ScalarNode && (strings.EqualFold(vNode.Value, "true") || strings.EqualFold(vNode.Value, "false")) {
				// Convert wait: bool -> wait: {required: bool}. Case-insensitive
				// so YAML bool variants (TRUE/True/False) canonicalize too, and
				// quoted strings ("true") still convert — the loader decodes a
				// scalar wait into a bool, so leaving a quoted string would break
				// load.
				isReq := strings.EqualFold(vNode.Value, "true")
				vNode.Kind = yaml.MappingNode
				vNode.Tag = "!!map" // drop the stale !!bool tag from the original scalar
				vNode.Style = 0
				vNode.Value = ""
				vNode.Content = []*yaml.Node{
					{Kind: yaml.ScalarNode, Value: "required"},
					{Kind: yaml.ScalarNode, Value: fmt.Sprintf("%t", isReq)},
				}
			}
		case "wait_config":
			// Migrate legacy wait_config key to wait
			docNode.Content[i].Value = "wait"
		}
	}

	migratedBytes, err := encodeYAMLNode(&root)
	if err != nil {
		return nil, nil, fmt.Errorf("encoding migrated YAML: %w", err)
	}

	// Precise remote-name diagnostic on the compile path: the resolved-schema
	// _|_ rejection does not name the offending key, and the authoring fallback
	// below would otherwise accept it (the authoring schema declares the
	// charset but does not enforce map keys under close()). Check the emitted
	// manifest directly so lxm compile fails with an actionable message.
	var migratedCfg Config
	if err := yaml.Unmarshal(migratedBytes, &migratedCfg); err != nil {
		return nil, warnings, fmt.Errorf("decoding migrated manifest: %w", err)
	}
	if err := validateImageRemoteNames(migratedCfg.ImageRemotes); err != nil {
		return nil, warnings, err
	}

	// Validate migrated output against resolved schema to ensure CUE compliance
	validator, validatorErr := NewValidator()
	if validatorErr == nil {
		if valErr := validator.ValidateResolved(migratedBytes); valErr != nil {
			if valAuthoringErr := validator.ValidateAuthoring(migratedBytes); valAuthoringErr != nil {
				return nil, warnings, fmt.Errorf("migrated manifest failed CUE validation: %w", valAuthoringErr)
			}
		}
	}

	// Go-side cross-field validation/normalization that is intentionally not
	// expressible in CUE. The compile path must match plan/apply (LoadConfig):
	// reject invalid host_access and ingress_allow values, and emit canonical
	// ingress sources/protocol/ports rather than the authoring fallback's raw
	// strings.
	changed, err := normalizeCompiledManifest(&migratedCfg, &root)
	if err != nil {
		return nil, warnings, err
	}
	if changed {
		migratedBytes, err = encodeYAMLNode(&root)
		if err != nil {
			return nil, warnings, fmt.Errorf("encoding normalized manifest: %w", err)
		}
		if validator != nil {
			if valErr := validator.ValidateResolved(migratedBytes); valErr != nil {
				if valAuthoringErr := validator.ValidateAuthoring(migratedBytes); valAuthoringErr != nil {
					return nil, warnings, fmt.Errorf("normalized manifest failed CUE validation: %w", valAuthoringErr)
				}
			}
		}
	}

	if hasRelativeMountSource {
		warnings = append(warnings, "CONFIG_WARN_RELATIVE_MOUNT: manifest contains relative or tilde mount sources requiring resolution prior to apply")
	}

	return migratedBytes, warnings, nil
}

// encodeYAMLNode renders a YAML document with the project's 2-space indentation.
func encodeYAMLNode(root *yaml.Node) ([]byte, error) {
	var buf bytes.Buffer
	encoder := yaml.NewEncoder(&buf)
	encoder.SetIndent(2)
	if err := encoder.Encode(root); err != nil {
		return nil, err
	}
	_ = encoder.Close()
	return buf.Bytes(), nil
}

// normalizeCompiledManifest runs the Go-side validation/normalization that
// LoadConfig applies at plan/apply time. It mutates the YAML document for
// normalized sockets and ingress rules so `lxm compile` emits the resolved
// surface (and never emits a manifest that plan/apply would reject).
func normalizeCompiledManifest(cfg *Config, root *yaml.Node) (bool, error) {
	for i := range cfg.VSwitches {
		if err := cfg.VSwitches[i].ValidateHostAccess(); err != nil {
			return false, err
		}
	}

	changed, err := normalizeCompiledSockets(cfg, root)
	if err != nil {
		return false, err
	}

	if cfg.NetworkPolicy != nil && len(cfg.NetworkPolicy.IngressAllow) > 0 {
		rules, err := NormalizeIngressAllow(cfg.NetworkPolicy.IngressAllow)
		if err != nil {
			return false, err
		}
		cfg.NetworkPolicy.IngressAllow = rules
		if err := applyIngressNormalization(root, rules); err != nil {
			return false, err
		}
		changed = true
	}

	return changed, nil
}

// normalizeCompiledSockets validates and canonicalizes migrated `sockets:`
// entries exactly like LoadConfig does (endpoint grammar, bind, mode, uid/gid)
// and rewrites the YAML nodes with the canonical values: `unix:` endpoints, an
// explicit host|instance bind, a quoted octal mode and decimal uid/gid. This
// keeps `lxm compile` from emitting a manifest that plan/apply would reject
// (for example `mode: "0o660"`, `uid: 3000000000`, `unix://run/x.sock`, or an
// unquoted integer mode). Both the top-level list and `replace.sockets` are
// normalized: a non-empty replace list wins at plan/apply time, but the ignored
// local list is still an authoring surface that must not be silently invalid.
func normalizeCompiledSockets(cfg *Config, root *yaml.Node) (bool, error) {
	if len(cfg.Sockets) == 0 && (cfg.Replace == nil || len(cfg.Replace.Sockets) == 0) {
		return false, nil
	}
	if root == nil || len(root.Content) == 0 || root.Content[0].Kind != yaml.MappingNode {
		return false, fmt.Errorf("internal: migrated manifest root is not a mapping")
	}

	if len(cfg.Sockets) > 0 {
		if err := normalizeCompiledSocketList(root.Content[0], "sockets", cfg.Sockets); err != nil {
			return false, err
		}
	}
	if cfg.Replace != nil && len(cfg.Replace.Sockets) > 0 {
		replaceNode := mappingValue(root.Content[0], "replace")
		if replaceNode == nil || replaceNode.Kind != yaml.MappingNode {
			return false, fmt.Errorf("internal: replace node missing during socket normalization")
		}
		if err := normalizeCompiledSocketList(replaceNode, "sockets", cfg.Replace.Sockets); err != nil {
			return false, err
		}
	}
	return true, nil
}

// normalizeCompiledSocketList validates and canonicalizes one socket list
// (top-level `sockets:` or `replace.sockets`) in place.
func normalizeCompiledSocketList(parent *yaml.Node, key string, socks []SocketConfig) error {
	seq := mappingValue(parent, key)
	if seq == nil || seq.Kind != yaml.SequenceNode {
		return fmt.Errorf("internal: %s node missing during socket normalization", key)
	}
	if len(seq.Content) != len(socks) {
		return fmt.Errorf("internal: %s node count %d != decoded socket count %d", key, len(seq.Content), len(socks))
	}
	for i := range socks {
		node := derefAliasNode(seq.Content[i])
		if node == nil || node.Kind != yaml.MappingNode {
			return fmt.Errorf("socket %q: entry is not a mapping", socks[i].Name)
		}
		if err := normalizeCompiledSocketEntry(&socks[i], node); err != nil {
			return err
		}
	}
	return nil
}

// normalizeCompiledSocketEntry validates one socket and rewrites its mapping
// node with canonical values. Alias entries are dereferenced by the caller, so
// the anchor target is canonicalized once for every alias that shares it.
func normalizeCompiledSocketEntry(sock *SocketConfig, node *yaml.Node) error {
	bind, ok := sockets.ParseBind(sock.Bind)
	if !ok {
		return fmt.Errorf("socket %q: invalid bind %q (must be host or instance)", sock.Name, sock.Bind)
	}
	listen, err := sockets.ParseEndpoint(sock.Listen)
	if err != nil {
		return fmt.Errorf("socket %q: invalid listen %q: %w", sock.Name, sock.Listen, err)
	}
	connect, err := sockets.ParseEndpoint(sock.Connect)
	if err != nil {
		return fmt.Errorf("socket %q: invalid connect %q: %w", sock.Name, sock.Connect, err)
	}
	mode := ""
	if sock.Mode != "" {
		mode, err = sockets.ParseMode(string(sock.Mode))
		if err != nil {
			return fmt.Errorf("socket %q: %w", sock.Name, err)
		}
	}
	if sock.UID != nil {
		if err := sockets.ValidateID("uid", *sock.UID); err != nil {
			return fmt.Errorf("socket %q: %w", sock.Name, err)
		}
	}
	if sock.GID != nil {
		if err := sockets.ValidateID("gid", *sock.GID); err != nil {
			return fmt.Errorf("socket %q: %w", sock.Name, err)
		}
	}

	setMappingString(node, "bind", bind)
	setMappingString(node, "listen", listen.DeviceString())
	setMappingString(node, "connect", connect.DeviceString())
	if mode == "" {
		removeMappingKey(node, "mode")
	} else {
		setMappingString(node, "mode", mode)
	}
	if sock.UID != nil {
		setMappingInt(node, "uid", *sock.UID)
	}
	if sock.GID != nil {
		setMappingInt(node, "gid", *sock.GID)
	}
	return nil
}

// setMappingInt writes a canonical decimal integer scalar.
func setMappingInt(node *yaml.Node, key string, value int) {
	setMappingNode(node, key, &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!int", Value: strconv.Itoa(value)})
}

// applyIngressNormalization rewrites network_policy.ingress_allow nodes with
// canonical source/protocol/ports values.
func applyIngressNormalization(root *yaml.Node, rules []IngressAllowRule) error {
	if root == nil || len(root.Content) == 0 || root.Content[0].Kind != yaml.MappingNode {
		return fmt.Errorf("internal: migrated manifest root is not a mapping")
	}
	doc := root.Content[0]
	policy := mappingValue(doc, "network_policy")
	if policy == nil || policy.Kind != yaml.MappingNode {
		return fmt.Errorf("internal: network_policy node missing during ingress normalization")
	}
	seq := mappingValue(policy, "ingress_allow")
	if seq == nil || seq.Kind != yaml.SequenceNode {
		return fmt.Errorf("internal: ingress_allow node missing during normalization")
	}
	if len(seq.Content) != len(rules) {
		return fmt.Errorf("internal: ingress_allow node count %d != normalized rule count %d", len(seq.Content), len(rules))
	}
	for i, rule := range rules {
		node := seq.Content[i]
		if node.Kind != yaml.MappingNode {
			return fmt.Errorf("internal: ingress_allow[%d] is not a mapping", i)
		}
		setMappingString(node, "to", rule.To)
		setMappingString(node, "source", rule.Source)
		setMappingString(node, "protocol", rule.Protocol)
		if len(rule.Ports) == 0 {
			removeMappingKey(node, "ports")
			continue
		}
		ports := make([]*yaml.Node, len(rule.Ports))
		for j, port := range rule.Ports {
			ports[j] = &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: port}
		}
		setMappingNode(node, "ports", &yaml.Node{Kind: yaml.SequenceNode, Tag: "!!seq", Content: ports})
	}
	return nil
}

func mappingValue(node *yaml.Node, key string) *yaml.Node {
	if node == nil || node.Kind != yaml.MappingNode {
		return nil
	}
	for i := 0; i+1 < len(node.Content); i += 2 {
		if node.Content[i].Value == key {
			return node.Content[i+1]
		}
	}
	return nil
}

func setMappingString(node *yaml.Node, key, value string) {
	setMappingNode(node, key, &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: value})
}

// derefAliasNode follows YAML alias nodes to their anchor target so
// normalization/validation can inspect the real mapping. It returns nil for a
// dangling alias.
func derefAliasNode(node *yaml.Node) *yaml.Node {
	for node != nil && node.Kind == yaml.AliasNode {
		node = node.Alias
	}
	return node
}

func setMappingNode(node *yaml.Node, key string, value *yaml.Node) {
	for i := 0; i+1 < len(node.Content); i += 2 {
		if node.Content[i].Value == key {
			// Preserve the author's inline comment on rewritten values so
			// `lxm compile` does not silently strip annotations.
			value.LineComment = node.Content[i+1].LineComment
			node.Content[i+1] = value
			return
		}
	}
	node.Content = append(node.Content,
		&yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: key},
		value,
	)
}

func removeMappingKey(node *yaml.Node, key string) {
	for i := 0; i+1 < len(node.Content); i += 2 {
		if node.Content[i].Value == key {
			node.Content = append(node.Content[:i], node.Content[i+2:]...)
			return
		}
	}
}

// SaveMigratedFile writes the migrated manifest content either non-destructively to .lxm/compiled/
// preserving relative directory structure, or in-place via atomic tempfile + rename.
func SaveMigratedFile(srcPath string, migratedBytes []byte, inPlace bool) (string, error) {
	cleanSrc := filepath.Clean(srcPath)
	if inPlace {
		dir := filepath.Dir(cleanSrc)
		tmpFile, err := os.CreateTemp(dir, "lxm-compile-*.yaml")
		if err != nil {
			return "", fmt.Errorf("creating temp file: %w", err)
		}
		tmpName := tmpFile.Name()

		if _, err := tmpFile.Write(migratedBytes); err != nil {
			_ = tmpFile.Close()
			_ = os.Remove(tmpName)
			return "", fmt.Errorf("writing temp file: %w", err)
		}
		if err := tmpFile.Close(); err != nil {
			_ = os.Remove(tmpName)
			return "", fmt.Errorf("closing temp file: %w", err)
		}

		if err := os.Rename(tmpName, cleanSrc); err != nil {
			_ = os.Remove(tmpName)
			return "", fmt.Errorf("atomic rename failed: %w", err)
		}
		return cleanSrc, nil
	}

	// Non-destructive: preserve relative directory path under .lxm/compiled/ if within working directory,
	// or write to <dir>/.lxm/compiled/<basename> for external/temp targets.
	compiledPath := ""
	cwd, err := os.Getwd()
	if err == nil {
		rel, relErr := filepath.Rel(cwd, cleanSrc)
		if relErr == nil && !strings.HasPrefix(rel, "..") {
			compiledPath = filepath.Join(".lxm", "compiled", rel)
		}
	}
	if compiledPath == "" {
		compiledPath = filepath.Join(filepath.Dir(cleanSrc), ".lxm", "compiled", filepath.Base(cleanSrc))
	}

	compiledDir := filepath.Dir(compiledPath)
	if err := os.MkdirAll(compiledDir, 0755); err != nil {
		return "", fmt.Errorf("creating compiled directory %q: %w", compiledDir, err)
	}

	//nolint:gosec // G306: compiled manifest file is intended to be standard readable YAML (0644)
	if err := os.WriteFile(compiledPath, migratedBytes, 0644); err != nil {
		return "", fmt.Errorf("writing compiled manifest: %w", err)
	}

	return compiledPath, nil
}
