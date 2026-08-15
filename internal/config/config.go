package config

import (
	"fmt"
	"net"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"strconv"
	"strings"

	"github.com/aiyor/lxm/internal/recipe"
	"gopkg.in/yaml.v3"
)

var cpuRegex = regexp.MustCompile(`^[0-9]+(-[0-9]+)?(,[0-9]+(-[0-9]+)?)*$`)

// CPUCount handles custom unmarshaling and validation for int or string CPU allocations.
type CPUCount string

func (c *CPUCount) UnmarshalYAML(value *yaml.Node) error {
	if value.Kind == yaml.ScalarNode {
		if value.Tag == "!!int" {
			val, err := strconv.Atoi(value.Value)
			if err != nil || val <= 0 {
				return fmt.Errorf("cpu count must be a positive integer, got %q", value.Value)
			}
			*c = CPUCount(value.Value)
			return nil
		}
		if value.Value == "0" || !cpuRegex.MatchString(value.Value) {
			return fmt.Errorf("invalid cpu count or cpuset format %q", value.Value)
		}
		*c = CPUCount(value.Value)
		return nil
	}
	return fmt.Errorf("invalid cpu count format")
}

// LimitsConfig models CPU, Memory, and Root Disk constraints.
type LimitsConfig struct {
	CPU      CPUCount        `yaml:"cpu,omitempty"`
	Memory   string          `yaml:"memory,omitempty"`
	Disk     string          `yaml:"disk,omitempty"`
	Presence map[string]bool `yaml:"-"`
}

func (l *LimitsConfig) UnmarshalYAML(value *yaml.Node) error {
	type rawLimits LimitsConfig
	var rl rawLimits
	if err := value.Decode(&rl); err != nil {
		return err
	}
	*l = LimitsConfig(rl)
	l.Presence = extractPresenceMap(value)
	return nil
}

// VMConfig models hypervisor-specific firmware and boot flags.
type VMConfig struct {
	SecureBoot *bool           `yaml:"secureboot,omitempty"`
	BootMode   string          `yaml:"boot_mode,omitempty"`
	Hugepages  bool            `yaml:"hugepages,omitempty"`
	RawQEMU    string          `yaml:"raw_qemu,omitempty"`
	Presence   map[string]bool `yaml:"-"`
}

func (v *VMConfig) UnmarshalYAML(value *yaml.Node) error {
	type rawVM VMConfig
	var rv rawVM
	if err := value.Decode(&rv); err != nil {
		return err
	}
	*v = VMConfig(rv)
	v.Presence = extractPresenceMap(value)
	return nil
}

// WaitConfig specifies timeout and polling deadlines for readiness gates.
type WaitConfig struct {
	Agent     string          `yaml:"agent,omitempty"`
	CloudInit string          `yaml:"cloud_init,omitempty"`
	Network   string          `yaml:"network,omitempty"`
	Poll      string          `yaml:"poll,omitempty"`
	Required  bool            `yaml:"required,omitempty"`
	Presence  map[string]bool `yaml:"-"`
}

// DefaultWaitConfig returns standard default wait policy deadlines.
func DefaultWaitConfig() WaitConfig {
	return WaitConfig{
		Agent:     "",
		CloudInit: "10m",
		Network:   "60s",
		Poll:      "5s",
		Required:  true,
		Presence:  make(map[string]bool),
	}
}

// UnmarshalYAML implements custom unmarshaling for WaitConfig to support bool shorthand or map.
func (w *WaitConfig) UnmarshalYAML(value *yaml.Node) error {
	*w = DefaultWaitConfig()
	w.Presence = make(map[string]bool)

	if value.Kind == yaml.ScalarNode {
		var b bool
		if err := value.Decode(&b); err != nil {
			return fmt.Errorf("invalid wait scalar: %w", err)
		}
		w.Required = b
		w.Presence["required"] = true
		return nil
	}

	if value.Kind == yaml.MappingNode {
		type rawWait WaitConfig
		var rw rawWait
		if err := value.Decode(&rw); err != nil {
			return err
		}

		w.Presence = extractPresenceMap(value)

		if w.Presence["agent"] {
			w.Agent = rw.Agent
		}
		if w.Presence["cloud_init"] {
			w.CloudInit = rw.CloudInit
		}
		if w.Presence["network"] {
			w.Network = rw.Network
		}
		if w.Presence["poll"] {
			w.Poll = rw.Poll
		}
		if w.Presence["required"] {
			w.Required = rw.Required
		}
		return nil
	}

	return fmt.Errorf("invalid wait policy (expected bool or wait config map)")
}

// RemoveDirective defines items to drop from inherited lists.
type RemoveDirective struct {
	Mounts   []string `yaml:"mounts,omitempty"`
	Networks []string `yaml:"networks,omitempty"`
	Recipes  []string `yaml:"recipes,omitempty"`
	Disks    []string `yaml:"disks,omitempty"`
}

// ReplaceDirective defines wholesale replacements for inherited lists.
type ReplaceDirective struct {
	Mounts   []Mount         `yaml:"mounts,omitempty"`
	Networks []NetworkConfig `yaml:"networks,omitempty"`
	Recipes  []RecipeGroup   `yaml:"recipes,omitempty"`
	Disks    []DiskConfig    `yaml:"disks,omitempty"`
}

// Config defines the desired state for an instance.
type Config struct {
	Schema           string            `yaml:"schema,omitempty"`
	Name             string            `yaml:"name,omitempty"`
	Type             string            `yaml:"type,omitempty"` // container | virtual-machine
	Status           string            `yaml:"status,omitempty"`
	State            string            `yaml:"state,omitempty"` // running | stopped (F2)
	Limits           *LimitsConfig     `yaml:"limits,omitempty"`
	VM               *VMConfig         `yaml:"vm,omitempty"`
	WaitPolicy       WaitConfig        `yaml:"wait"`
	LegacyWaitPolicy *WaitConfig       `yaml:"wait_config,omitempty"` // v1-compat legacy tag
	Image            string            `yaml:"image,omitempty"`
	User             string            `yaml:"user,omitempty"`
	Mounts           Mounts            `yaml:"mounts,omitempty"`
	Networks         []NetworkConfig   `yaml:"networks,omitempty"`
	Disks            []DiskConfig      `yaml:"disks,omitempty"`
	VSwitches        []VSwitchConfig   `yaml:"vswitches,omitempty"`
	NetworkPolicy    *NetworkPolicy    `yaml:"network_policy,omitempty"`
	CloudInitInclude []string          `yaml:"cloud-init-include,omitempty"`
	CloudInit        string            `yaml:"cloud-init,omitempty"`
	CloudInitFile    string            `yaml:"cloud-init-file,omitempty"`
	NetworkConfig    string            `yaml:"network-config,omitempty"`
	Recipes          Recipes           `yaml:"recipes,omitempty"`
	Include          []string          `yaml:"include,omitempty"` // consumed during resolution
	Base             bool              `yaml:"base,omitempty"`    // file-level metadata
	Groups           []string          `yaml:"groups,omitempty"`
	Sudo             bool              `yaml:"sudo,omitempty"`            // opt-in passwordless sudo (D9)
	InjectSSHKeys    bool              `yaml:"inject_ssh_keys,omitempty"` // opt-in auto host-key injection (D9)
	SSHKeys          []string          `yaml:"ssh_keys,omitempty"`        // explicit identity public keys (D9)
	Vars             map[string]string `yaml:"vars,omitempty"`
	Remove           *RemoveDirective  `yaml:"remove,omitempty"`
	Replace          *ReplaceDirective `yaml:"replace,omitempty"`

	ConfigBaseDir string          `yaml:"-"` // directory of root manifest for relative file resolution
	ConfigFile    string          `yaml:"-"` // root manifest file path (fleet-union conflict attribution)
	presence      map[string]bool `yaml:"-"`
}

// nodeKindName returns a readable name for a yaml.Node kind, used in
// unmarshaling error messages.
func nodeKindName(k yaml.Kind) string {
	switch k {
	case yaml.DocumentNode:
		return "document"
	case yaml.SequenceNode:
		return "list"
	case yaml.MappingNode:
		return "map"
	case yaml.ScalarNode:
		return "scalar"
	case yaml.AliasNode:
		return "alias"
	}
	return fmt.Sprintf("kind %d", k)
}

// Mounts is the authoring surface for mounts. It normalizes the list and map
// forms into a canonical list of Mount objects.
//
//	mounts:                       # list form: string shorthand or objects
//	  - "/tmp/a:/var/a:rw"
//	  - source: /tmp/b
//	    path: /var/b
//	mounts:                       # map form (Style 2): container path -> host source or object
//	  /var/log: /tmp/host-logs
type Mounts []Mount

// UnmarshalYAML implements custom unmarshaling for Mounts so both the list and
// the Style 2 map form decode into a canonical list of Mount objects.
func (ms *Mounts) UnmarshalYAML(value *yaml.Node) error {
	switch value.Kind {
	case yaml.SequenceNode:
		var out []Mount
		for _, elem := range value.Content {
			var m Mount
			if err := elem.Decode(&m); err != nil { // reuses Mount.UnmarshalYAML
				return err
			}
			out = append(out, m)
		}
		*ms = out
	case yaml.MappingNode:
		var out []Mount
		for i := 0; i+1 < len(value.Content); i += 2 {
			pathNode, valNode := value.Content[i], value.Content[i+1]
			var m Mount
			if valNode.Kind == yaml.ScalarNode {
				m = Mount{Source: valNode.Value, Path: pathNode.Value}
			} else {
				if err := valNode.Decode(&m); err != nil {
					return err
				}
				if m.Path == "" {
					m.Path = pathNode.Value
				}
			}
			out = append(out, m)
		}
		*ms = out
	default:
		return fmt.Errorf("mounts must be a list or a map, got %s", nodeKindName(value.Kind))
	}
	return nil
}

// Mount defines a host-to-container directory mapping.
type Mount struct {
	Source    string `yaml:"source"`
	Path      string `yaml:"path"`
	Recursive bool   `yaml:"recursive,omitempty"`
	Readonly  bool   `yaml:"readonly,omitempty"`
	Shift     *bool  `yaml:"shift,omitempty"`
}

// UnmarshalYAML implements custom unmarshaling for Mount to support compact string shorthand.
func (m *Mount) UnmarshalYAML(value *yaml.Node) error {
	if value.Kind == yaml.ScalarNode {
		parts := strings.Split(value.Value, ":")
		if len(parts) < 2 {
			return fmt.Errorf("invalid mount shorthand %q", value.Value)
		}
		m.Source = parts[0]
		m.Path = parts[1]
		if len(parts) > 2 {
			for _, opt := range parts[2:] {
				switch opt {
				case "ro", "readonly":
					m.Readonly = true
				case "recursive":
					m.Recursive = true
				}
			}
		}
		return nil
	}

	type rawMount Mount
	var rm rawMount
	if err := value.Decode(&rm); err != nil {
		return err
	}
	*m = Mount(rm)
	return nil
}

// NetworkConfig defines a network interface configuration.
type NetworkConfig struct {
	Name   string `yaml:"name"`             // Defaults to eth0
	IPv4   string `yaml:"ipv4,omitempty"`   // e.g., 10.0.0.10
	Parent string `yaml:"parent,omitempty"` // Defaults to lxdbr0
}

// DiskConfig models an additional managed storage disk (VMs only). The two
// orthogonal axes — mode (filesystem vs block, by Path) and ownership (managed
// vs external, by Source) — are documented in STORAGE-SPEC.md §3.
type DiskConfig struct {
	Name     string `yaml:"name"`
	Size     string `yaml:"size,omitempty"`
	Pool     string `yaml:"pool,omitempty"`
	Path     string `yaml:"path,omitempty"`
	Source   string `yaml:"source,omitempty"`
	Readonly bool   `yaml:"readonly,omitempty"`
	Bus      string `yaml:"bus,omitempty"`
}

// VSwitchConfig models a managed LXD virtual switch (fleet-scoped).
type VSwitchConfig struct {
	Name     string `yaml:"name"`
	Type     string `yaml:"type,omitempty"`   // "" = "bridge" (v1: only "bridge")
	Driver   string `yaml:"driver,omitempty"` // "" = "native" (bridge.driver)
	IPv4     string `yaml:"ipv4"`
	IPv6     string `yaml:"ipv6,omitempty"` // "" = "none"
	NAT      *bool  `yaml:"nat,omitempty"`  // nil = true
	Group    string `yaml:"group,omitempty"`
	Internet *bool  `yaml:"internet,omitempty"` // nil = true
}

// NetworkPolicyRule models one inter-group allowance.
type NetworkPolicyRule struct {
	From      string `yaml:"from"`
	To        string `yaml:"to"`
	Direction string `yaml:"direction,omitempty"` // "" = "both"
}

// NetworkPolicy is a fleet-scoped, group-based traffic policy.
type NetworkPolicy struct {
	InternalCIDRs []string            `yaml:"internal_cidrs,omitempty"`
	Allow         []NetworkPolicyRule `yaml:"allow"`
}

// Recipes is the authoring surface for recipes. It normalizes string, root:,
// run_as, and legacy scripts-only forms into canonical RecipeGroups.
//
//	recipes:
//	  - recipes/bootstrap.sh          # string shorthand  -> run_as root
//	  - root: [recipes/setup.sh]      # root shorthand     -> run_as root
//	  - run_as: dev                   # object form
//	    scripts: [recipes/user.sh]
//	  - scripts: [recipes/legacy.sh]  # v1-compat scripts-only -> run_as root
type Recipes []RecipeGroup

// runnableScript reports whether a script entry is a real script rather than
// an empty or comment-only line. It mirrors the migrator's Transform 8
// hasScripts check so the loader and compiler agree on what counts as an
// empty recipe group.
func runnableScript(script string) bool {
	clean := strings.TrimSpace(script)
	return clean != "" && !strings.HasPrefix(clean, "#")
}

// UnmarshalYAML implements custom unmarshaling for Recipes so string and root:
// shorthands and legacy scripts-only groups decode into canonical RecipeGroups.
func (rs *Recipes) UnmarshalYAML(value *yaml.Node) error {
	if value.Kind != yaml.SequenceNode {
		return fmt.Errorf("recipes must be a list, got %s", nodeKindName(value.Kind))
	}
	var out []RecipeGroup
	for _, elem := range value.Content {
		if elem.Kind == yaml.AliasNode {
			if elem.Alias == nil {
				return fmt.Errorf("recipe group references a missing YAML anchor")
			}
			elem = elem.Alias
		}
		var rg RecipeGroup
		switch elem.Kind {
		case yaml.ScalarNode:
			if !runnableScript(elem.Value) {
				continue // empty/comment-only shorthand: pruned like Transform 8
			}
			rg = RecipeGroup{RunAs: "root", Scripts: []string{elem.Value}}
		case yaml.MappingNode:
			runAs := "root"
			var scripts, rootScripts []string
			for i := 0; i+1 < len(elem.Content); i += 2 {
				switch elem.Content[i].Value {
				case "run_as", "run-as":
					if err := elem.Content[i+1].Decode(&runAs); err != nil {
						return err
					}
				case "scripts":
					if err := elem.Content[i+1].Decode(&scripts); err != nil {
						return err
					}
				case "root":
					if err := elem.Content[i+1].Decode(&rootScripts); err != nil {
						return err
					}
				}
			}
			rg = RecipeGroup{RunAs: runAs, Scripts: filterRunnableScriptList(append(scripts, rootScripts...))}
			if !hasRunnableScript(rg.Scripts) {
				continue // empty/comment-only group: pruned like Transform 8
			}
		default:
			return fmt.Errorf("recipe group must be a script path or an object, got %s", nodeKindName(elem.Kind))
		}
		out = append(out, rg)
	}
	*rs = out
	return nil
}

func hasRunnableScript(scripts []string) bool {
	for _, s := range scripts {
		if runnableScript(s) {
			return true
		}
	}
	return false
}

// filterRunnableScriptList drops empty/comment-only entries from a script
// list, mirroring the migrator's compiled-output filtering so load and
// compile agree on what is kept.
func filterRunnableScriptList(scripts []string) []string {
	var kept []string
	for _, s := range scripts {
		if runnableScript(s) {
			kept = append(kept, s)
		}
	}
	return kept
}

// RecipeGroup defines a set of scripts to execute as a specific user.
type RecipeGroup struct {
	RunAs   string   `yaml:"run_as"`
	Scripts []string `yaml:"scripts"`
}

// RecipeHashKey returns a path-qualified key for recipe idempotency tracking.
func RecipeHashKey(scriptPath, metadataName string) string {
	return recipe.PathQualifiedHashKey(scriptPath, metadataName)
}

// Validate checks that the config is valid before acting on it.
func (conf *Config) Validate(configBaseDir string) error {
	if configBaseDir == "" {
		configBaseDir = conf.ConfigBaseDir
	}

	if conf.Base {
		if conf.Name != "" {
			return fmt.Errorf("base config must not have a name (got %q)", conf.Name)
		}
		if conf.Image != "" {
			return fmt.Errorf("base config must not have an image (got %q)", conf.Image)
		}
		return conf.validateCommon(configBaseDir)
	}

	if conf.Name == "" {
		return fmt.Errorf("name must be specified in config or via --name flag")
	}

	if conf.Status == "" {
		conf.Status = "present"
	}

	if conf.Status != "present" && conf.Status != "absent" {
		return fmt.Errorf("status must be 'present' or 'absent', got %q", conf.Status)
	}

	if conf.Status == "present" && conf.Image == "" {
		return fmt.Errorf("image must be specified when status is 'present'")
	}

	if conf.Status == "absent" && conf.State != "" {
		return fmt.Errorf("state cannot be specified when status is 'absent'")
	}

	return conf.validateCommon(configBaseDir)
}

func (conf *Config) validateCommon(configBaseDir string) error {
	if conf.User == "" {
		conf.User = "ubuntu"
	}

	for i := range conf.Mounts {
		m := &conf.Mounts[i]
		if m.Source == "" || m.Path == "" {
			return fmt.Errorf("mount %d: source and path are required", i)
		}
		if strings.HasPrefix(m.Source, "~/") || m.Source == "~" {
			home, err := os.UserHomeDir()
			if err == nil {
				if m.Source == "~" {
					m.Source = home
				} else {
					m.Source = filepath.Join(home, m.Source[2:])
				}
			}
		}

		cleanPath := filepath.Clean(m.Path)
		if cleanPath == "/" || cleanPath == "/proc" || cleanPath == "/sys" || cleanPath == "/dev" {
			return fmt.Errorf("mount %d: invalid container destination path %q", i, m.Path)
		}

		if _, err := os.Stat(m.Source); err != nil {
			return fmt.Errorf("mount %d: source path %q does not exist on host: %w", i, m.Source, err)
		}
		if !filepath.IsAbs(m.Source) {
			return fmt.Errorf("mount %d: source path %q must be absolute", i, m.Source)
		}
		if !filepath.IsAbs(m.Path) {
			return fmt.Errorf("mount %d: container path %q must be absolute", i, m.Path)
		}
	}

	if conf.CloudInitFile != "" {
		p := conf.CloudInitFile
		if !filepath.IsAbs(p) && configBaseDir != "" {
			p = filepath.Join(configBaseDir, p)
		}
		if _, err := os.Stat(p); err != nil {
			return fmt.Errorf("cloud-init file %q does not exist: %w", p, err)
		}
	}

	for i, n := range conf.Networks {
		if n.IPv4 != "" && net.ParseIP(n.IPv4) == nil {
			return fmt.Errorf("network %d: invalid IPv4 address %q", i, n.IPv4)
		}
	}

	if err := conf.validateVSwitches(); err != nil {
		return err
	}

	return nil
}

// ValidatePostMerge checks constraints that only make sense after config resolution.
func ValidatePostMerge(conf *Config) error {
	// Union of mount and filesystem-disk destination paths must be unique
	// (STORAGE-SPEC §7.2): LXD rejects duplicate device paths, and silently
	// shadowing one with the other would be a correctness hazard.
	seenPaths := make(map[string]string) // cleaned path -> origin
	for _, m := range conf.Mounts {
		cleanP := filepath.Clean(m.Path)
		if origin, exists := seenPaths[cleanP]; exists {
			return fmt.Errorf("duplicate mount path %q (defined in %s and mounts)", m.Path, origin)
		}
		seenPaths[cleanP] = "mounts"
	}
	for _, d := range conf.Disks {
		if d.Path == "" {
			continue
		}
		cleanP := filepath.Clean(d.Path)
		if origin, exists := seenPaths[cleanP]; exists {
			return fmt.Errorf("duplicate mount path %q (defined in %s and disk %q)", d.Path, origin, d.Name)
		}
		seenPaths[cleanP] = fmt.Sprintf("disk %q", d.Name)
	}

	seenNetworks := make(map[string]bool)
	for i, n := range conf.Networks {
		name := n.Name
		if name == "" {
			name = "eth0"
		}
		if seenNetworks[name] {
			return fmt.Errorf("duplicate network name %q (network %d)", name, i)
		}
		seenNetworks[name] = true
	}

	seenDisks := make(map[string]bool)
	for i, d := range conf.Disks {
		if d.Name == "" {
			return fmt.Errorf("disk %d: name is required", i)
		}
		if d.Name == "root" {
			return fmt.Errorf("disk %d: name %q is reserved for the root volume", i, d.Name)
		}
		if seenDisks[d.Name] {
			return fmt.Errorf("duplicate disk name %q (disk %d)", d.Name, i)
		}
		seenDisks[d.Name] = true
	}

	return nil
}

// ResolveCloudInit reads and deep-merges cloud-init data from the Config.
func (conf *Config) ResolveCloudInit(configBaseDir string) (string, error) {
	if configBaseDir == "" {
		configBaseDir = conf.ConfigBaseDir
	}

	var merged map[string]interface{}

	for _, inc := range conf.CloudInitInclude {
		incPath := inc
		if !filepath.IsAbs(incPath) && configBaseDir != "" {
			incPath = filepath.Join(configBaseDir, incPath)
		}
		data, err := os.ReadFile(incPath)
		if err != nil {
			return "", fmt.Errorf("reading cloud-init-include %q: %w", incPath, err)
		}
		if err := mergeYAMLData(&merged, data); err != nil {
			return "", fmt.Errorf("merging cloud-init-include %q: %w", incPath, err)
		}
	}

	var localData []byte
	if conf.CloudInitFile != "" {
		p := conf.CloudInitFile
		if !filepath.IsAbs(p) && configBaseDir != "" {
			p = filepath.Join(configBaseDir, p)
		}
		var err error
		localData, err = os.ReadFile(p)
		if err != nil {
			return "", fmt.Errorf("reading cloud-init-file %q: %w", p, err)
		}
	} else if conf.CloudInit != "" {
		localData = []byte(conf.CloudInit)
	}

	if len(localData) > 0 {
		if err := mergeYAMLData(&merged, localData); err != nil {
			return "", fmt.Errorf("merging local cloud-init: %w", err)
		}
	}

	if merged == nil {
		if conf.User == "" {
			return "", nil
		}
		merged = make(map[string]interface{})
	}

	if conf.User != "" {
		injectUserConfig(conf, merged)
	}

	out, err := yaml.Marshal(merged)
	if err != nil {
		return "", fmt.Errorf("marshaling merged cloud-init: %w", err)
	}

	result := "#cloud-config\n" + string(out)
	return result, nil
}

func injectUserConfig(conf *Config, merged map[string]interface{}) {
	user := conf.User
	userEntry := map[string]interface{}{
		"name":   user,
		"groups": "sudo",
		"shell":  "/bin/bash",
	}

	if conf.Sudo {
		userEntry["sudo"] = []interface{}{"ALL=(ALL) NOPASSWD:ALL"}
	}

	if len(conf.SSHKeys) > 0 {
		userEntry["ssh_authorized_keys"] = conf.SSHKeys
	} else if conf.InjectSSHKeys {
		if keys := DiscoverHostPublicKeys(); len(keys) > 0 {
			userEntry["ssh_authorized_keys"] = keys
		}
	}

	if existing, ok := merged["users"]; ok {
		if existingList, ok := existing.([]interface{}); ok {
			merged["users"] = append(existingList, userEntry)
		}
	} else {
		merged["users"] = []interface{}{"default", userEntry}
	}

	envFile := map[string]interface{}{
		"path":        "/etc/profile.d/lxm-env.sh",
		"permissions": "0644",
		"content":     fmt.Sprintf("export LXM_USER=%s\n", user),
	}

	if existing, ok := merged["write_files"]; ok {
		if existingList, ok := existing.([]interface{}); ok {
			merged["write_files"] = append(existingList, envFile)
		}
	} else {
		merged["write_files"] = []interface{}{envFile}
	}
}

func DiscoverHostPublicKeys() []string {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil
	}

	sshDir := filepath.Join(home, ".ssh")
	files, err := os.ReadDir(sshDir)
	if err != nil {
		return nil
	}

	var keys []string
	for _, f := range files {
		if strings.HasSuffix(f.Name(), ".pub") {
			data, err := os.ReadFile(filepath.Join(sshDir, f.Name()))
			if err == nil {
				keys = append(keys, strings.TrimSpace(string(data)))
			}
		}
	}
	return keys
}

func DiscoverHostPrivateKeys() []string {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil
	}

	sshDir := filepath.Join(home, ".ssh")
	files, err := os.ReadDir(sshDir)
	if err != nil {
		return nil
	}

	var keys []string
	for _, f := range files {
		if strings.HasSuffix(f.Name(), ".pub") {
			privKey := strings.TrimSuffix(f.Name(), ".pub")
			privPath := filepath.Join(sshDir, privKey)
			if _, err := os.Stat(privPath); err == nil {
				keys = append(keys, privPath)
			}
		}
	}
	return keys
}

func mergeYAMLData(dst *map[string]interface{}, srcData []byte) error {
	strData := strings.TrimPrefix(string(srcData), "#cloud-config")

	var src map[string]interface{}
	if err := yaml.Unmarshal([]byte(strData), &src); err != nil {
		return err
	}

	if *dst == nil {
		*dst = src
		return nil
	}

	mergedVal := deepMerge(*dst, src)
	if merged, ok := mergedVal.(map[string]interface{}); ok {
		*dst = merged
		return nil
	}
	return fmt.Errorf("unexpected merged config type %T", mergedVal)
}

func deepMerge(dst, src interface{}) interface{} {
	switch dstTyped := dst.(type) {
	case map[string]interface{}:
		srcTyped, ok := src.(map[string]interface{})
		if !ok {
			return src
		}
		out := make(map[string]interface{})
		for k, v := range dstTyped {
			out[k] = v
		}
		for k, v := range srcTyped {
			if existing, found := out[k]; found {
				out[k] = deepMerge(existing, v)
			} else {
				out[k] = v
			}
		}
		return out
	case []interface{}:
		srcTyped, ok := src.([]interface{})
		if !ok {
			return src
		}
		return append(dstTyped, srcTyped...)
	default:
		if isZeroValue(src) {
			return dst
		}
		return src
	}
}

func isZeroValue(v interface{}) bool {
	if v == nil {
		return true
	}
	rv := reflect.ValueOf(v)
	if !rv.IsValid() {
		return true
	}
	return rv.IsZero()
}

func isPresent(c *Config, fieldName string, val interface{}) bool {
	if c == nil {
		return false
	}
	if c.presence != nil {
		return c.presence[fieldName]
	}
	return !isZeroValue(val)
}

// MergeConfigs merges two Config structs using presence-wins scalar & recursive struct rules (D5).
func MergeConfigs(base, overlay *Config) (*Config, error) {
	if base == nil {
		return overlay, nil
	}
	if overlay == nil {
		return base, nil
	}

	res := &Config{
		WaitPolicy:    base.WaitPolicy,
		ConfigBaseDir: base.ConfigBaseDir,
		ConfigFile:    base.ConfigFile,
		presence:      make(map[string]bool),
	}
	if overlay.ConfigBaseDir != "" {
		res.ConfigBaseDir = overlay.ConfigBaseDir
	}
	if overlay.ConfigFile != "" {
		res.ConfigFile = overlay.ConfigFile
	}

	res.WaitPolicy.Presence = make(map[string]bool)
	for k, v := range base.WaitPolicy.Presence {
		res.WaitPolicy.Presence[k] = v
	}

	if base.presence != nil {
		for k, v := range base.presence {
			res.presence[k] = v
		}
	}
	if overlay.presence != nil {
		for k, v := range overlay.presence {
			res.presence[k] = v
		}
	}

	// Support v1-compat legacy wait_config tag
	effectiveOverlayWait := overlay.WaitPolicy
	if overlay.LegacyWaitPolicy != nil {
		effectiveOverlayWait = *overlay.LegacyWaitPolicy
	}

	// Recursive struct merge for WaitPolicy
	if isPresent(overlay, "wait", effectiveOverlayWait) || isPresent(overlay, "wait_config", effectiveOverlayWait) || len(effectiveOverlayWait.Presence) > 0 {
		if effectiveOverlayWait.Presence != nil {
			if effectiveOverlayWait.Presence["agent"] {
				res.WaitPolicy.Agent = effectiveOverlayWait.Agent
				res.WaitPolicy.Presence["agent"] = true
			}
			if effectiveOverlayWait.Presence["cloud_init"] {
				res.WaitPolicy.CloudInit = effectiveOverlayWait.CloudInit
				res.WaitPolicy.Presence["cloud_init"] = true
			}
			if effectiveOverlayWait.Presence["network"] {
				res.WaitPolicy.Network = effectiveOverlayWait.Network
				res.WaitPolicy.Presence["network"] = true
			}
			if effectiveOverlayWait.Presence["poll"] {
				res.WaitPolicy.Poll = effectiveOverlayWait.Poll
				res.WaitPolicy.Presence["poll"] = true
			}
			if effectiveOverlayWait.Presence["required"] {
				res.WaitPolicy.Required = effectiveOverlayWait.Required
				res.WaitPolicy.Presence["required"] = true
			}
		}
	}

	// Presence-wins scalar merge
	if isPresent(overlay, "type", overlay.Type) {
		res.Type = overlay.Type
	} else {
		res.Type = base.Type
	}

	// Recursive struct merge for Limits
	if base.Limits != nil || overlay.Limits != nil {
		res.Limits = &LimitsConfig{Presence: make(map[string]bool)}
		if base.Limits != nil {
			res.Limits.CPU = base.Limits.CPU
			res.Limits.Memory = base.Limits.Memory
			res.Limits.Disk = base.Limits.Disk
			for k, v := range base.Limits.Presence {
				res.Limits.Presence[k] = v
			}
		}
		if overlay.Limits != nil {
			if overlay.Limits.Presence["cpu"] {
				res.Limits.CPU = overlay.Limits.CPU
				res.Limits.Presence["cpu"] = true
			}
			if overlay.Limits.Presence["memory"] {
				res.Limits.Memory = overlay.Limits.Memory
				res.Limits.Presence["memory"] = true
			}
			if overlay.Limits.Presence["disk"] {
				res.Limits.Disk = overlay.Limits.Disk
				res.Limits.Presence["disk"] = true
			}
		}
	}

	// Recursive struct merge for VM
	if base.VM != nil || overlay.VM != nil {
		res.VM = &VMConfig{Presence: make(map[string]bool)}
		if base.VM != nil {
			res.VM.SecureBoot = base.VM.SecureBoot
			res.VM.BootMode = base.VM.BootMode
			res.VM.Hugepages = base.VM.Hugepages
			res.VM.RawQEMU = base.VM.RawQEMU
			for k, v := range base.VM.Presence {
				res.VM.Presence[k] = v
			}
		}
		if overlay.VM != nil {
			if overlay.VM.Presence["secureboot"] {
				res.VM.SecureBoot = overlay.VM.SecureBoot
				res.VM.Presence["secureboot"] = true
			}
			if overlay.VM.Presence["boot_mode"] {
				res.VM.BootMode = overlay.VM.BootMode
				res.VM.Presence["boot_mode"] = true
			}
			if overlay.VM.Presence["hugepages"] {
				res.VM.Hugepages = overlay.VM.Hugepages
				res.VM.Presence["hugepages"] = true
			}
			if overlay.VM.Presence["raw_qemu"] {
				res.VM.RawQEMU = overlay.VM.RawQEMU
				res.VM.Presence["raw_qemu"] = true
			}
		}
	}

	// Presence-wins scalar merge
	if isPresent(overlay, "name", overlay.Name) {
		res.Name = overlay.Name
	} else {
		res.Name = base.Name
	}

	if isPresent(overlay, "schema", overlay.Schema) {
		res.Schema = overlay.Schema
	} else {
		res.Schema = base.Schema
	}

	if isPresent(overlay, "status", overlay.Status) {
		res.Status = overlay.Status
	} else {
		res.Status = base.Status
	}

	if isPresent(overlay, "state", overlay.State) {
		res.State = overlay.State
	} else {
		res.State = base.State
	}

	if isPresent(overlay, "image", overlay.Image) {
		res.Image = overlay.Image
	} else {
		res.Image = base.Image
	}

	if isPresent(overlay, "user", overlay.User) {
		res.User = overlay.User
	} else {
		res.User = base.User
	}

	if isPresent(overlay, "cloud-init", overlay.CloudInit) {
		res.CloudInit = overlay.CloudInit
	} else {
		res.CloudInit = base.CloudInit
	}

	if isPresent(overlay, "cloud-init-file", overlay.CloudInitFile) {
		res.CloudInitFile = overlay.CloudInitFile
	} else {
		res.CloudInitFile = base.CloudInitFile
	}

	if isPresent(overlay, "network-config", overlay.NetworkConfig) {
		res.NetworkConfig = overlay.NetworkConfig
	} else {
		res.NetworkConfig = base.NetworkConfig
	}

	if isPresent(overlay, "include", overlay.Include) {
		res.Include = overlay.Include
	} else {
		res.Include = base.Include
	}

	if isPresent(overlay, "sudo", overlay.Sudo) {
		res.Sudo = overlay.Sudo
	} else {
		res.Sudo = base.Sudo
	}

	if isPresent(overlay, "inject_ssh_keys", overlay.InjectSSHKeys) {
		res.InjectSSHKeys = overlay.InjectSSHKeys
	} else {
		res.InjectSSHKeys = base.InjectSSHKeys
	}

	if isPresent(overlay, "ssh_keys", overlay.SSHKeys) {
		res.SSHKeys = overlay.SSHKeys
	} else {
		res.SSHKeys = base.SSHKeys
	}

	// Base metadata is file-level: merged config of base + leaf is NOT a base config (Base=false)
	if isPresent(overlay, "base", overlay.Base) {
		res.Base = overlay.Base
	} else {
		res.Base = false
	}

	// List merge: replace vs concat
	if overlay.Replace != nil && len(overlay.Replace.Mounts) > 0 {
		res.Mounts = overlay.Replace.Mounts
	} else {
		res.Mounts = append(append(Mounts(nil), base.Mounts...), overlay.Mounts...)
	}

	if overlay.Replace != nil && len(overlay.Replace.Networks) > 0 {
		res.Networks = overlay.Replace.Networks
	} else {
		res.Networks = append(append([]NetworkConfig(nil), base.Networks...), overlay.Networks...)
	}

	if overlay.Replace != nil && len(overlay.Replace.Disks) > 0 {
		res.Disks = overlay.Replace.Disks
	} else {
		res.Disks = append(append([]DiskConfig(nil), base.Disks...), overlay.Disks...)
	}

	if overlay.Replace != nil && len(overlay.Replace.Recipes) > 0 {
		res.Recipes = overlay.Replace.Recipes
	} else {
		res.Recipes = append(append(Recipes(nil), base.Recipes...), overlay.Recipes...)
	}

	// vswitches: list-concat within an include chain (like mounts/networks).
	// Dedup + conflict resolution happen at the fleet union (§7.2).
	res.VSwitches = append(append([]VSwitchConfig(nil), base.VSwitches...), overlay.VSwitches...)

	// network_policy: whole-value presence-wins replacement within a tree
	// (§2.2). Across sibling manifests the allow/internal_cidrs lists are
	// unioned at the fleet union.
	if isPresent(overlay, "network_policy", overlay.NetworkPolicy) {
		res.NetworkPolicy = copyNetworkPolicy(overlay.NetworkPolicy)
	} else {
		res.NetworkPolicy = copyNetworkPolicy(base.NetworkPolicy)
	}

	res.CloudInitInclude = append(append([]string(nil), base.CloudInitInclude...), overlay.CloudInitInclude...)
	res.Groups = append(append([]string(nil), base.Groups...), overlay.Groups...)

	// Apply Remove Directives (D5, C3)
	if overlay.Remove != nil {
		if err := applyRemoveDirectives(res, overlay.Remove); err != nil {
			return nil, err
		}
	}

	return res, nil
}

func applyRemoveDirectives(res *Config, remove *RemoveDirective) error {
	for _, dropPath := range remove.Mounts {
		cleanDrop := filepath.Clean(dropPath)
		matched := false
		var filtered []Mount
		for _, m := range res.Mounts {
			if filepath.Clean(m.Path) == cleanDrop {
				matched = true
			} else {
				filtered = append(filtered, m)
			}
		}
		if !matched {
			return fmt.Errorf("remove.mounts: path %q matched no mount", dropPath)
		}
		res.Mounts = filtered
	}

	for _, dropName := range remove.Networks {
		matched := false
		var filtered []NetworkConfig
		for _, n := range res.Networks {
			name := n.Name
			if name == "" {
				name = "eth0"
			}
			if name == dropName {
				matched = true
			} else {
				filtered = append(filtered, n)
			}
		}
		if !matched {
			return fmt.Errorf("remove.networks: name %q matched no network", dropName)
		}
		res.Networks = filtered
	}

	for _, dropRecipe := range remove.Recipes {
		cleanDrop := filepath.Clean(dropRecipe)
		matched := false
		var filtered []RecipeGroup
		for _, rg := range res.Recipes {
			var keptScripts []string
			for _, script := range rg.Scripts {
				if filepath.Clean(script) == cleanDrop || script == dropRecipe {
					matched = true
				} else {
					keptScripts = append(keptScripts, script)
				}
			}
			if len(keptScripts) > 0 {
				rg.Scripts = keptScripts
				filtered = append(filtered, rg)
			}
		}
		if !matched {
			return fmt.Errorf("remove.recipes: %q matched no recipe", dropRecipe)
		}
		res.Recipes = filtered
	}

	for _, dropName := range remove.Disks {
		matched := false
		var filtered []DiskConfig
		for _, d := range res.Disks {
			if d.Name == dropName {
				matched = true
			} else {
				filtered = append(filtered, d)
			}
		}
		if !matched {
			return fmt.Errorf("remove.disks: name %q matched no disk", dropName)
		}
		res.Disks = filtered
	}

	return nil
}

// copyNetworkPolicy deep-copies a NetworkPolicy so merged configs never share
// mutable slices with their inheritance parents.
func copyNetworkPolicy(p *NetworkPolicy) *NetworkPolicy {
	if p == nil {
		return nil
	}
	cp := &NetworkPolicy{
		InternalCIDRs: append([]string(nil), p.InternalCIDRs...),
		Allow:         append([]NetworkPolicyRule(nil), p.Allow...),
	}
	return cp
}

// LoadConfig reads a YAML config file and resolves includes/templates.
func LoadConfig(configFile string) (*Config, error) {
	conf, err := loadConfigRecursive(configFile, nil)
	if err != nil {
		return nil, err
	}

	if conf.Status == "" && !isPresent(conf, "status", conf.Status) {
		conf.Status = "present"
	}
	if conf.User == "" && !isPresent(conf, "user", conf.User) {
		conf.User = "ubuntu"
	}
	conf.Include = nil

	// Normalization Pipeline (VM, Limits, Mounts, WaitPolicy)
	switch conf.Type {
	case "vm", "virtual-machine":
		conf.Type = "virtual-machine"
		if conf.VM == nil {
			conf.VM = &VMConfig{BootMode: "uefi-secureboot"}
		} else {
			if conf.VM.BootMode == "" && conf.VM.SecureBoot != nil {
				if *conf.VM.SecureBoot {
					conf.VM.BootMode = "uefi-secureboot"
				} else {
					conf.VM.BootMode = "uefi-nosecureboot"
				}
			}
			if conf.VM.BootMode == "" && conf.VM.SecureBoot == nil {
				conf.VM.BootMode = "uefi-secureboot"
			}
			conf.VM.SecureBoot = nil
		}
		if !conf.WaitPolicy.Presence["agent"] || conf.WaitPolicy.Agent == "" {
			conf.WaitPolicy.Agent = "2m"
		}
	default:
		conf.Type = "container"
		conf.VM = nil
		if !conf.WaitPolicy.Presence["agent"] {
			conf.WaitPolicy.Agent = ""
		}
	}

	// disks is VM-only in v1 (STORAGE-SPEC §8). Go-side rather than CUE-side
	// because type normalization (vm → virtual-machine) happens before CUE
	// resolved validation and a CUE cross-field guard on sibling top-level
	// keys with defaults is brittle here.
	if len(conf.Disks) > 0 && conf.Type == "container" {
		return nil, fmt.Errorf(`field "disks" is only supported for type: virtual-machine (instance %q)`, conf.Name)
	}

	for i := range conf.Mounts {
		if conf.Mounts[i].Shift == nil {
			t := true
			conf.Mounts[i].Shift = &t
		}
	}

	// disks default normalization (mirrors #LXM_RESOLVED defaults so the
	// strict resolved schema round-trips deterministically, STORAGE-SPEC §4).
	for i := range conf.Disks {
		d := &conf.Disks[i]
		if d.Pool == "" {
			d.Pool = "default"
		}
		if d.Source == "" {
			// Managed disk: derive the volume name from the instance and
			// materialize the size it provisions.
			if conf.Name != "" && d.Name != "" {
				d.Source = conf.Name + "-" + d.Name
			}
			if d.Size == "" {
				return nil, fmt.Errorf("disk %q of instance %q: size is required when source is unset (managed disk)", d.Name, conf.Name)
			}
		} else {
			// External disk: the volume size is managed outside lxm.
			d.Size = ""
		}
		if d.Path == "" {
			// Block mode: default io.bus.
			if d.Bus == "" {
				d.Bus = "virtio-scsi"
			}
		} else {
			// Filesystem mode: io.bus is forbidden (CUE-enforced).
			d.Bus = ""
		}
	}

	// vswitch default normalization (mirrors #LXM_RESOLVED defaults so the
	// strict resolved schema round-trips deterministically).
	for i := range conf.VSwitches {
		vs := &conf.VSwitches[i]
		if vs.Type == "" {
			vs.Type = "bridge"
		}
		if vs.Driver == "" {
			vs.Driver = "native"
		}
		if vs.IPv6 == "" {
			vs.IPv6 = "none"
		}
		if vs.NAT == nil {
			t := true
			vs.NAT = &t
		}
		if vs.Internet == nil {
			t := true
			vs.Internet = &t
		}
	}

	// network_policy rule direction default ("both").
	if conf.NetworkPolicy != nil {
		for i := range conf.NetworkPolicy.Allow {
			if conf.NetworkPolicy.Allow[i].Direction == "" {
				conf.NetworkPolicy.Allow[i].Direction = "both"
			}
		}
	}

	// Validate resolved manifest against CUE resolved schema (#LXM_RESOLVED) when schema is lxm/config/v2
	if conf.Schema == "lxm/config/v2" {
		v, err := NewValidator()
		if err == nil {
			resolvedBytes, err := yaml.Marshal(conf)
			if err == nil {
				if err := v.ValidateResolved(resolvedBytes); err != nil {
					return nil, fmt.Errorf("resolved schema validation failed: %w", err)
				}
			}
		}
	}

	return conf, nil
}

func loadConfigRecursive(configFile string, visited map[string]bool) (*Config, error) {
	absPath, err := filepath.Abs(configFile)
	if err != nil {
		return nil, fmt.Errorf("resolving config path %q: %w", configFile, err)
	}

	if visited == nil {
		visited = make(map[string]bool)
	}
	if visited[absPath] {
		return nil, fmt.Errorf("circular include detected: %s", configFile)
	}
	visited[absPath] = true

	data, err := os.ReadFile(absPath)
	if err != nil {
		return nil, fmt.Errorf("reading config %q: %w", configFile, err)
	}

	// Step 1: YAML parse with presence tracking
	var docNode yaml.Node
	if err := yaml.Unmarshal(data, &docNode); err != nil {
		return nil, fmt.Errorf("parsing config %q: %w", configFile, err)
	}

	var raw Config
	if err := docNode.Decode(&raw); err != nil {
		return nil, fmt.Errorf("decoding config %q: %w", configFile, err)
	}
	raw.presence = extractPresenceMap(&docNode)
	baseDir := filepath.Dir(absPath)
	raw.ConfigBaseDir = baseDir

	if raw.LegacyWaitPolicy != nil && raw.WaitPolicy.Presence == nil {
		raw.WaitPolicy = *raw.LegacyWaitPolicy
	}

	// Step 2: Anchored Templating (vars, env, Protect-Sentinel-Restore)
	expandedData, err := expandTemplates(string(data), raw.Vars, raw.Name, "")
	if err != nil {
		return nil, fmt.Errorf("template expansion in %q: %w", configFile, err)
	}

	// If templates expanded, re-decode raw from expandedData so values are substituted!
	if expandedData != string(data) {
		var expDoc yaml.Node
		if err := yaml.Unmarshal([]byte(expandedData), &expDoc); err == nil {
			var expRaw Config
			if err := expDoc.Decode(&expRaw); err == nil {
				expRaw.presence = extractPresenceMap(&expDoc)
				expRaw.ConfigBaseDir = baseDir
				if expRaw.LegacyWaitPolicy != nil && expRaw.WaitPolicy.Presence == nil {
					expRaw.WaitPolicy = *expRaw.LegacyWaitPolicy
				}
				raw = expRaw
			}
		}
	}

	// Expand tilde '~' in mount sources immediately after decoding
	for i := range raw.Mounts {
		m := &raw.Mounts[i]
		if strings.HasPrefix(m.Source, "~/") || m.Source == "~" {
			if home, err := os.UserHomeDir(); err == nil {
				if m.Source == "~" {
					m.Source = home
				} else {
					m.Source = filepath.Join(home, m.Source[2:])
				}
			}
		}
	}

	fileName := filepath.Base(absPath)
	hasUnderscorePrefix := strings.HasPrefix(fileName, "_")
	if hasUnderscorePrefix && !raw.Base {
		return nil, fmt.Errorf("file %q has '_' prefix but base is not true; add 'base: true' to the file", fileName)
	}

	// Schema detection (D13) & CUE Authoring validation (on expandedData so templates are substituted!)
	switch raw.Schema {
	case "":
		fmt.Fprintf(os.Stderr, "notice: %s declares no schema (lxm/config/v1 compat mode) — run lxm compile to migrate to lxm/config/v2\n", fileName)
	case "lxm/config/v2":
		v, err := NewValidator()
		if err == nil {
			if err := v.ValidateAuthoring([]byte(expandedData)); err != nil {
				return nil, fmt.Errorf("schema validation %s: %w", fileName, err)
			}
		}
	case "lxm/config/v1":
		// Supported v1 schema
	default:
		return nil, fmt.Errorf("unknown schema version %q in %s", raw.Schema, fileName)
	}

	// Resolve includes depth-first
	accumulated := &Config{WaitPolicy: DefaultWaitConfig(), ConfigBaseDir: baseDir, presence: make(map[string]bool)}
	for _, includePath := range raw.Include {
		resolved := resolveConfigPath(includePath, baseDir)
		included, err := loadConfigRecursive(resolved, visited)
		if err != nil {
			return nil, fmt.Errorf("in include %q from %q: %w", includePath, configFile, err)
		}
		var mergeErr error
		accumulated, mergeErr = MergeConfigs(accumulated, included)
		if mergeErr != nil {
			return nil, fmt.Errorf("merging include %q: %w", includePath, mergeErr)
		}
	}

	var mergeErr error
	accumulated, mergeErr = MergeConfigs(accumulated, &raw)
	if mergeErr != nil {
		return nil, fmt.Errorf("merging %q: %w", configFile, mergeErr)
	}

	if err := ValidatePostMerge(accumulated); err != nil {
		return nil, err
	}

	accumulated.ConfigFile = absPath
	return accumulated, nil
}

func extractPresenceMap(doc *yaml.Node) map[string]bool {
	presence := make(map[string]bool)
	if doc == nil {
		return presence
	}
	root := doc
	if doc.Kind == yaml.DocumentNode && len(doc.Content) > 0 {
		root = doc.Content[0]
	}
	if root.Kind == yaml.MappingNode {
		for i := 0; i < len(root.Content)-1; i += 2 {
			keyNode := root.Content[i]
			presence[keyNode.Value] = true
		}
	}
	return presence
}

// 3-Pass Protect-Sentinel-Restore Anchored Template Expansion (D4, Step 4)
func expandTemplates(input string, vars map[string]string, containerName string, containerGroup string) (string, error) {
	return expandTemplatesOpt(input, vars, containerName, containerGroup, false)
}

// expandTemplatesPreserveEscapes expands templates but leaves \{{ \}} escape
// markers intact in the output. The loader consumes the un-escaped form
// (escapes become literal braces in the parsed value); artifacts that will be
// re-loaded by the loader — compiled manifests — must keep the escape markers
// so a later load un-escapes exactly once instead of substituting a live
// template a second time (UG5 B3 regression).
func expandTemplatesPreserveEscapes(input string, vars map[string]string, containerName string, containerGroup string) (string, error) {
	return expandTemplatesOpt(input, vars, containerName, containerGroup, true)
}

func expandTemplatesOpt(input string, vars map[string]string, containerName string, containerGroup string, preserveEscapes bool) (string, error) {
	// Protect escapes before substitution. Two forms are honored: the
	// documented `\{{` / `\}}` (3-char, reference/manifest.md +
	// reference/environment-variables.md) and the legacy 4-char `\{{` /
	// `\}}\` form the original implementation and its tests used. The
	// 4-char form is replaced first so its trailing backslash is consumed by
	// the sentinel instead of leaving a stray `\` before a live template.
	protected := strings.ReplaceAll(input, "\\{{\\", "\x00LXM_ESC_OPEN\x00")
	protected = strings.ReplaceAll(protected, "\\}}\\", "\x00LXM_ESC_CLOSE\x00")
	protected = strings.ReplaceAll(protected, "\\{{", "\x00LXM_ESC_OPEN\x00")
	protected = strings.ReplaceAll(protected, "\\}}", "\x00LXM_ESC_CLOSE\x00")

	var expandErr error

	reVars := regexp.MustCompile(`\{\{\s*\.Vars\.([A-Za-z0-9_]+)\s*\}\}`)
	protected = reVars.ReplaceAllStringFunc(protected, func(m string) string {
		match := reVars.FindStringSubmatch(m)
		if len(match) > 1 {
			k := match[1]
			if v, ok := vars[k]; ok {
				return v
			}
			expandErr = fmt.Errorf("unbound template variable .Vars.%s", k)
		}
		return m
	})
	if expandErr != nil {
		return "", expandErr
	}

	reEnv := regexp.MustCompile(`\{\{\s*\.Env\.([A-Za-z0-9_]+)\s*\}\}`)
	protected = reEnv.ReplaceAllStringFunc(protected, func(m string) string {
		match := reEnv.FindStringSubmatch(m)
		if len(match) > 1 {
			k := match[1]
			v := os.Getenv(k)
			if v == "" {
				expandErr = fmt.Errorf("unbound environment variable .Env.%s", k)
			}
			return v
		}
		return m
	})
	if expandErr != nil {
		return "", expandErr
	}

	reName := regexp.MustCompile(`\{\{\s*\.Name\s*\}\}`)
	protected = reName.ReplaceAllString(protected, containerName)

	reGroup := regexp.MustCompile(`\{\{\s*\.Group\s*\}\}`)
	protected = reGroup.ReplaceAllString(protected, containerGroup)

	openRepl, closeRepl := "{{", "}}"
	if preserveEscapes {
		openRepl, closeRepl = "\\{{", "\\}}"
	}
	restored := strings.ReplaceAll(protected, "\x00LXM_ESC_OPEN\x00", openRepl)
	restored = strings.ReplaceAll(restored, "\x00LXM_ESC_CLOSE\x00", closeRepl)

	return restored, nil
}

func resolveConfigPath(includePath, baseDir string) string {
	if filepath.IsAbs(includePath) {
		return includePath
	}
	return filepath.Join(baseDir, includePath)
}

// AddIncludeToYAMLFile adds an include path to a YAML config file.
func AddIncludeToYAMLFile(filePath string, includePath string) (bool, error) {
	data, err := os.ReadFile(filePath)
	if err != nil {
		return false, fmt.Errorf("reading file: %w", err)
	}

	var doc yaml.Node
	if err := yaml.Unmarshal(data, &doc); err != nil {
		return false, fmt.Errorf("parsing YAML: %w", err)
	}

	if doc.Kind != yaml.DocumentNode || len(doc.Content) == 0 {
		return false, fmt.Errorf("invalid YAML document")
	}

	root := doc.Content[0]
	if root.Kind != yaml.MappingNode {
		return false, fmt.Errorf("file does not appear to be an lxm config; refusing to modify")
	}

	for i := 0; i < len(root.Content)-1; i += 2 {
		key := root.Content[i]
		if key.Value == "include" {
			seq := root.Content[i+1]
			if seq.Kind != yaml.SequenceNode {
				return false, fmt.Errorf("include field is not a sequence")
			}
			for _, item := range seq.Content {
				if item.Value == includePath {
					return false, nil
				}
			}
			newItem := &yaml.Node{Kind: yaml.ScalarNode, Value: includePath}
			seq.Content = append(seq.Content, newItem)
			return true, writeYAMLNode(filePath, &doc)
		}
	}

	includeKey := &yaml.Node{Kind: yaml.ScalarNode, Value: "include"}
	includeSeq := &yaml.Node{Kind: yaml.SequenceNode}
	includeVal := &yaml.Node{Kind: yaml.ScalarNode, Value: includePath}
	includeSeq.Content = append(includeSeq.Content, includeVal)

	root.Content = append(root.Content, includeKey, includeSeq)
	return true, writeYAMLNode(filePath, &doc)
}

func writeYAMLNode(filePath string, doc *yaml.Node) error {
	out, err := yaml.Marshal(doc)
	if err != nil {
		return fmt.Errorf("marshaling YAML: %w", err)
	}

	//nolint:gosec // G306: YAML configuration file intended to be readable (0644)
	if err := os.WriteFile(filePath, out, 0644); err != nil {
		return fmt.Errorf("writing file: %w", err)
	}
	return nil
}

// HasIncludeInYAMLFile checks whether a YAML config file already has the given include path.
func HasIncludeInYAMLFile(filePath string, includePath string) (bool, error) {
	data, err := os.ReadFile(filePath)
	if err != nil {
		return false, fmt.Errorf("reading file: %w", err)
	}

	var raw Config
	if err := yaml.Unmarshal(data, &raw); err != nil {
		return false, fmt.Errorf("parsing YAML: %w", err)
	}

	for _, inc := range raw.Include {
		if inc == includePath {
			return true, nil
		}
	}
	return false, nil
}
