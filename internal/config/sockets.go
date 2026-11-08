package config

import (
	"fmt"
	"sort"

	"gopkg.in/yaml.v3"

	"github.com/aiyor/lxm/internal/sockets"
)

// validateSockets enforces per-instance Unix-socket proxy rules (issue #17). It
// is invoked from validateCommon (root-only, after type normalization).
// Cross-field rules are skipped for base fragments: they are validated on the
// resolved leaf manifest, where the instance type and NICs are known.
func (conf *Config) validateSockets() error {
	if len(conf.Sockets) == 0 {
		return nil
	}
	if conf.Base {
		return nil
	}
	// A deletion does not proxy anything; keep cross-field socket validation out
	// of the way of removing an instance whose inherited (or stale) socket list
	// would not be usable on a live instance. Endpoint grammar and name rules
	// are still validated during load/ValidatePostMerge.
	if conf.Status == "absent" {
		return nil
	}

	instance := conf.Name
	if instance == "" {
		instance = "?"
	}

	// Both daemons support proxy devices on VMs in NAT mode only, and NAT cannot
	// proxy Unix endpoints, so a Unix-socket proxy can never be attached to a
	// virtual machine.
	if conf.Type == "virtual-machine" || conf.Type == "vm" {
		return fmt.Errorf("sockets of instance %q: Unix-socket proxies are container-only (virtual machines require nat: true, which cannot proxy unix endpoints)", instance)
	}

	nicNames := make(map[string]bool, len(conf.Networks))
	for i := range conf.Networks {
		name := conf.Networks[i].Name
		if name == "" {
			name = "eth0"
		}
		nicNames[name] = true
	}

	// Host-side socket paths are host-global resources; instance-side listening
	// paths are per-instance (and only created for bind=instance). Detect both
	// within this manifest; the fleet-wide host-side check runs separately.
	hostSide := make(map[string]string, len(conf.Sockets))
	instanceListen := make(map[string]string, len(conf.Sockets))

	for i := range conf.Sockets {
		sock := &conf.Sockets[i]

		// Defensive re-parse so Config literals built by callers/tests behave
		// like loader output.
		listen, err := sockets.ParseEndpoint(sock.Listen)
		if err != nil {
			return fmt.Errorf("socket %q of instance %q: invalid listen %q: %w", sock.Name, instance, sock.Listen, err)
		}
		connect, err := sockets.ParseEndpoint(sock.Connect)
		if err != nil {
			return fmt.Errorf("socket %q of instance %q: invalid connect %q: %w", sock.Name, instance, sock.Connect, err)
		}
		bind, ok := sockets.ParseBind(sock.Bind)
		if !ok {
			return fmt.Errorf("socket %q of instance %q: invalid bind %q (must be host or instance)", sock.Name, instance, sock.Bind)
		}
		if sock.UID != nil {
			if err := sockets.ValidateID("uid", *sock.UID); err != nil {
				return fmt.Errorf("socket %q of instance %q: %w", sock.Name, instance, err)
			}
		}
		if sock.GID != nil {
			if err := sockets.ValidateID("gid", *sock.GID); err != nil {
				return fmt.Errorf("socket %q of instance %q: %w", sock.Name, instance, err)
			}
		}
		if sock.Mode != "" {
			if _, err := sockets.ParseMode(string(sock.Mode)); err != nil {
				return fmt.Errorf("socket %q of instance %q: %w", sock.Name, instance, err)
			}
		}

		// A NIC named like the socket device would be silently overwritten by
		// the socket in the instance PUT device map.
		devName := sockets.DeviceName(sock.Name)
		if nicNames[devName] {
			return fmt.Errorf("socket %q of instance %q: NIC %q would collide with device %q", sock.Name, instance, devName, devName)
		}

		hostPath := listen.Path
		if bind == sockets.BindInstance {
			hostPath = connect.Path
		}
		if prev, dup := hostSide[hostPath]; dup {
			return fmt.Errorf("socket %q of instance %q: host-side unix socket path %q is already used by socket %q", sock.Name, instance, hostPath, prev)
		}
		hostSide[hostPath] = sock.Name

		// bind=instance creates its listening socket inside the instance; two
		// devices cannot create the same instance-side socket.
		if bind == sockets.BindInstance {
			if prev, dup := instanceListen[listen.Path]; dup {
				return fmt.Errorf("socket %q of instance %q: instance-side listen socket %q is already created by socket %q", sock.Name, instance, listen.Path, prev)
			}
			instanceListen[listen.Path] = sock.Name
		}
	}

	return nil
}

// legacySocketFields is the closed field set of a `sockets:` entry. The v2 CUE
// authoring schema closes the object for schema: lxm/config/v2 manifests; legacy
// (schema-less and lxm/config/v1) manifests have no authoring gate, so without
// this check the YAML decoder would silently drop unknown keys such as `nat`,
// `proto`, `proxy_protocol` or `security.uid`.
var legacySocketFields = map[string]bool{
	"name":    true,
	"listen":  true,
	"connect": true,
	"bind":    true,
	"uid":     true,
	"gid":     true,
	"mode":    true,
}

// validateLegacySocketFields rejects unknown fields inside `sockets:` (and
// `replace.sockets:`) entries for manifests that skip CUE authoring validation.
func validateLegacySocketFields(doc *yaml.Node) error {
	root := doc
	if root != nil && root.Kind == yaml.DocumentNode {
		if len(root.Content) == 0 {
			return nil
		}
		root = root.Content[0]
	}
	if root == nil || root.Kind != yaml.MappingNode {
		return nil
	}

	if err := validateSocketSequence(mappingValue(root, "sockets"), "sockets"); err != nil {
		return err
	}
	if replace := mappingValue(root, "replace"); replace != nil && replace.Kind == yaml.MappingNode {
		if err := validateSocketSequence(mappingValue(replace, "sockets"), "replace.sockets"); err != nil {
			return err
		}
	}
	return nil
}

func validateSocketSequence(seq *yaml.Node, context string) error {
	if seq == nil || seq.Kind != yaml.SequenceNode {
		return nil // wrong shapes are reported by the decoder/CUE layers
	}
	for i, entry := range seq.Content {
		entry = derefAliasNode(entry)
		if entry == nil || entry.Kind != yaml.MappingNode {
			continue
		}
		for j := 0; j+1 < len(entry.Content); j += 2 {
			key := entry.Content[j].Value
			// YAML merge keys pull fields from an anchored mapping; those fields
			// are resolved by the decoder and are not statically visible here, so
			// the merged source cannot be checked for unknown keys by this pass.
			if key == "<<" || legacySocketFields[key] {
				continue
			}
			return fmt.Errorf("%s[%d]: unknown field %q (supported fields: name, listen, connect, bind, uid, gid, mode)", context, i, key)
		}
	}
	return nil
}

// ValidateFleetSockets validates host-side Unix-socket path uniqueness across
// the full loaded manifest set. The host-side endpoint is the `connect` path for
// bind=instance and the `listen` path for bind=host; two managed sockets
// pointing at the same host path are almost certainly a mistake (and two
// bind=host listeners on one path are a hard bind conflict), so duplicates are
// exit-3 configuration errors citing both manifests. Same-instance duplicates
// are already rejected by validateSockets; they are skipped here so the
// single-manifest error stays the precise one.
//
// The warnings return is reserved for future cluster-scope notices (mirroring
// ValidateFleetPorts); it is always nil today.
func ValidateFleetSockets(configs []*Config) (warnings []string, err error) {
	type entry struct {
		instance string
		file     string
		socket   string
		path     string
	}

	sorted := make([]*Config, 0, len(configs))
	for _, conf := range configs {
		if conf == nil || conf.Base || conf.Name == "" || conf.Status == "absent" {
			continue
		}
		sorted = append(sorted, conf)
	}
	sort.SliceStable(sorted, func(i, j int) bool {
		return sorted[i].ConfigFile < sorted[j].ConfigFile
	})

	entries := make([]entry, 0)
	for _, conf := range sorted {
		file := conf.ConfigFile
		if file == "" {
			file = conf.ConfigBaseDir
		}
		for i := range conf.Sockets {
			sock := &conf.Sockets[i]
			listen, parseErr := sockets.ParseEndpoint(sock.Listen)
			if parseErr != nil {
				return nil, fmt.Errorf("socket %q of instance %q: invalid listen %q: %w", sock.Name, conf.Name, sock.Listen, parseErr)
			}
			connect, parseErr := sockets.ParseEndpoint(sock.Connect)
			if parseErr != nil {
				return nil, fmt.Errorf("socket %q of instance %q: invalid connect %q: %w", sock.Name, conf.Name, sock.Connect, parseErr)
			}
			bind, ok := sockets.ParseBind(sock.Bind)
			if !ok {
				return nil, fmt.Errorf("socket %q of instance %q: invalid bind %q (must be host or instance)", sock.Name, conf.Name, sock.Bind)
			}

			hostPath := listen.Path
			if bind == sockets.BindInstance {
				hostPath = connect.Path
			}

			for _, prev := range entries {
				if prev.instance == conf.Name && prev.file == file {
					continue // same-instance duplicates within one file are a validateSockets error
				}
				if prev.path != hostPath {
					continue
				}
				return nil, fmt.Errorf("host-side unix socket path %q is declared by socket %q of instance %q (%s) and socket %q of instance %q (%s)",
					hostPath, prev.socket, prev.instance, prev.file, sock.Name, conf.Name, file)
			}
			entries = append(entries, entry{instance: conf.Name, file: file, socket: sock.Name, path: hostPath})
		}
	}

	return nil, nil
}
