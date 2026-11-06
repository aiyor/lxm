package plan

import (
	"fmt"
	"net/netip"
	"slices"
	"sort"
	"strings"

	"github.com/aiyor/lxm/internal/config"
	"github.com/aiyor/lxm/internal/ports"
)

// buildPortDevices compiles manifest ports into managed proxy device payloads.
func buildPortDevices(manifest *config.Config) (map[string]map[string]string, error) {
	out := make(map[string]map[string]string, len(manifest.Ports))
	for i := range manifest.Ports {
		p := &manifest.Ports[i]
		listen, err := ports.ParseEndpoint(p.Proto, p.Listen)
		if err != nil {
			return nil, fmt.Errorf("port %q: invalid listen %q: %w", p.Name, p.Listen, err)
		}
		connect, err := ports.ParseEndpoint(p.Proto, p.Connect)
		if err != nil {
			return nil, fmt.Errorf("port %q: invalid connect %q: %w", p.Name, p.Connect, err)
		}
		out[ports.DeviceName(p.Name)] = ports.BuildDevice(listen, connect, p.NAT)
	}
	return out, nil
}

// findPortOwnershipConflicts rejects a manifest port whose device name is
// already taken by an existing device that lxm does not manage. Silent adoption
// is unsafe: Incus treats a marker-only device change as an update and fails
// with ErrCannotUpdate, while LXD would remove+add with a listener blip.
func findPortOwnershipConflicts(manifest *config.Config, live *InstanceSnapshot) error {
	if live == nil {
		return nil
	}
	for i := range manifest.Ports {
		devName := ports.DeviceName(manifest.Ports[i].Name)
		if props, ok := live.Devices[devName]; ok && !ports.IsManagedProxy(devName, props) {
			return fmt.Errorf("device %q for instance %q exists but is not lxm-managed; remove it or rename the port", devName, manifest.Name)
		}
	}
	return nil
}

// livePort is the plan-layer view of a marker-carrying proxy device. Malformed
// managed devices are retained so the diff can repair or remove them rather
// than failing the plan. Device names are the canonical identity used for
// matching (ownership is marker-based, so a managed device can legally exist
// under a name that is not proxy-<port-name>).
type livePort struct {
	Device    string
	Config    config.PortConfig
	Malformed bool
	Raw       map[string]string
}

func getLivePorts(live *InstanceSnapshot) []livePort {
	if live == nil {
		return nil
	}
	out := make([]livePort, 0, len(live.Devices))
	for devName, props := range live.Devices {
		if !ports.IsManagedProxy(devName, props) {
			continue // foreign devices are preserved, never diffed
		}
		if dev, ok := ports.ParseProxy(devName, props); ok {
			out = append(out, livePort{
				Device: devName,
				Config: config.PortConfig{
					Name:    dev.Name,
					Listen:  dev.Listen.HostPort(),
					Connect: dev.Connect.HostPort(),
					Proto:   dev.Listen.Proto,
					NAT:     dev.NAT,
				},
				Raw: props,
			})
			continue
		}
		out = append(out, livePort{Device: devName, Malformed: true, Raw: props})
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Device < out[j].Device })
	return out
}

// normalizePorts defensively materializes defaults and canonical endpoints so
// plan tests that build config.Config literals behave like loader output.
func normalizePorts(in []config.PortConfig) ([]config.PortConfig, error) {
	out := make([]config.PortConfig, 0, len(in))
	for i := range in {
		p := in[i]
		if p.Proto == "" {
			p.Proto = "tcp"
		}
		listen, err := ports.ParseEndpoint(p.Proto, p.Listen)
		if err != nil {
			return nil, fmt.Errorf("port %q: invalid listen %q: %w", p.Name, p.Listen, err)
		}
		connect, err := ports.ParseEndpoint(p.Proto, p.Connect)
		if err != nil {
			return nil, fmt.Errorf("port %q: invalid connect %q: %w", p.Name, p.Connect, err)
		}
		p.Proto = listen.Proto
		p.Listen = listen.HostPort()
		p.Connect = connect.HostPort()
		out = append(out, p)
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

// portRemovalField names a removed device. Canonical proxy-<name> devices use
// the port-diff namespace; anomalous marked devices under any other name are
// not declared ports, so they use a device-qualified field to avoid colliding
// with a real port's ports[<name>] entry in the same plan.
func portRemovalField(device string) string {
	if name, ok := strings.CutPrefix(device, ports.DevicePrefix); ok && name != "" {
		return "ports[" + name + "]"
	}
	return "devices[" + device + "]"
}

// unmanagedProxyProps returns the device properties lxm does not own. Such
// properties are left behind by hand edits or future daemon options; the next
// rewrite of the device drops them, so a drift-aware plan repairs the device
// instead of reporting a false noop.
func unmanagedProxyProps(props map[string]string) []string {
	allowed := map[string]bool{
		"type":           true,
		"listen":         true,
		"connect":        true,
		"nat":            true,
		ports.ManagedKey: true,
	}
	extra := make([]string, 0)
	for k := range props {
		if !allowed[k] {
			extra = append(extra, k)
		}
	}
	sort.Strings(extra)
	return extra
}

// diffPorts compares desired ports with managed live proxy devices and emits
// per-port FieldDiff entries (mirroring diffDisks naming). Both sides are keyed
// by canonical device name (proxy-<name>); a marker-carrying device under any
// other name is removable junk and can never satisfy a manifest port. It also
// rejects unmarked ownership collisions.
func diffPorts(manifest *config.Config, live *InstanceSnapshot) ([]FieldDiff, error) {
	if err := findPortOwnershipConflicts(manifest, live); err != nil {
		return nil, err
	}

	desired, err := normalizePorts(manifest.Ports)
	if err != nil {
		return nil, err
	}
	desiredByDevice := make(map[string]config.PortConfig, len(desired))
	for _, p := range desired {
		desiredByDevice[ports.DeviceName(p.Name)] = p
	}

	liveParsed := make(map[string]config.PortConfig)
	liveRaw := make(map[string]map[string]string)
	liveMalformed := make(map[string]map[string]string)
	for _, lp := range getLivePorts(live) {
		if lp.Malformed {
			liveMalformed[lp.Device] = lp.Raw
			continue
		}
		liveParsed[lp.Device] = lp.Config
		liveRaw[lp.Device] = lp.Raw
	}

	diffs := make([]FieldDiff, 0, len(desired)+len(liveParsed)+len(liveMalformed))
	for _, dp := range desired {
		devName := ports.DeviceName(dp.Name)
		lp, exists := liveParsed[devName]
		if !exists {
			if raw, bad := liveMalformed[devName]; bad {
				// Repair a malformed managed device in place.
				diffs = append(diffs, FieldDiff{Field: "ports[" + dp.Name + "]", Old: raw, New: dp})
				continue
			}
			diffs = append(diffs, FieldDiff{Field: "ports[" + dp.Name + "]", Old: nil, New: dp})
			continue
		}

		// Out-of-band device properties would be silently dropped by the next
		// rewrite; surface the device repair so the plan is honest.
		if extra := unmanagedProxyProps(liveRaw[devName]); len(extra) > 0 {
			diffs = append(diffs, FieldDiff{Field: "ports[" + dp.Name + "]", Old: liveRaw[devName], New: dp})
		}

		if lp.Listen != dp.Listen {
			diffs = append(diffs, FieldDiff{Field: "ports[" + dp.Name + "].listen", Old: lp.Listen, New: dp.Listen})
		}
		if lp.Connect != dp.Connect {
			diffs = append(diffs, FieldDiff{Field: "ports[" + dp.Name + "].connect", Old: lp.Connect, New: dp.Connect})
		}
		if lp.Proto != dp.Proto {
			diffs = append(diffs, FieldDiff{Field: "ports[" + dp.Name + "].proto", Old: lp.Proto, New: dp.Proto})
		}
		if lp.NAT != dp.NAT {
			diffs = append(diffs, FieldDiff{Field: "ports[" + dp.Name + "].nat", Old: lp.NAT, New: dp.NAT})
		}
	}

	removed := make([]string, 0)
	for devName := range liveParsed {
		if _, keep := desiredByDevice[devName]; !keep {
			removed = append(removed, devName)
		}
	}
	for devName := range liveMalformed {
		if _, keep := desiredByDevice[devName]; !keep {
			removed = append(removed, devName)
		}
	}
	sort.Strings(removed)
	for _, devName := range removed {
		fieldName := portRemovalField(devName)
		if lp, ok := liveParsed[devName]; ok {
			diffs = append(diffs, FieldDiff{Field: fieldName, Old: lp, New: nil})
			continue
		}
		diffs = append(diffs, FieldDiff{Field: fieldName, Old: liveMalformed[devName], New: nil})
	}

	return diffs, nil
}

// appendManifestWarnings adds per-manifest plan warnings. It is called from all
// Compute return paths so warnings are never lost to a branch (the raw.qemu
// warning previously lived in three copies).
func appendManifestWarnings(p *Plan, manifest *config.Config, live *InstanceSnapshot) {
	if manifest == nil {
		return
	}
	if manifest.Type == "virtual-machine" && manifest.VM != nil && manifest.VM.RawQEMU != "" {
		p.Warnings = append(p.Warnings, fmt.Sprintf("instance %q specifies raw.qemu hypervisor arguments: %q", manifest.Name, manifest.VM.RawQEMU))
	}

	for i := range manifest.Ports {
		port := &manifest.Ports[i]
		listen, err := ports.ParseEndpoint(port.Proto, port.Listen)
		if err != nil {
			continue // invalid manifests are rejected during config validation
		}
		if !listen.IsLoopback() {
			warn := fmt.Sprintf("port %q of instance %q listens on non-loopback address %s; traffic bypasses network_policy and must be allowed by the host firewall",
				port.Name, manifest.Name, listen.HostPort())
			if listen.IsWildcard() {
				warn += "; the wildcard bind is dual-stack and also exposes IPv6"
			}
			p.Warnings = append(p.Warnings, warn)
		}

		if live == nil {
			continue
		}
		devName := ports.DeviceName(port.Name)
		if localProps, local := live.Devices[devName]; local {
			// Managed devices with extra properties are rewritten canonically on
			// the next update; warn so the drop is not silent.
			if ports.IsManagedProxy(devName, localProps) {
				if extra := unmanagedProxyProps(localProps); len(extra) > 0 {
					p.Warnings = append(p.Warnings, fmt.Sprintf("port %q of instance %q: proxy device %q has unmanaged properties %v; the next update rewrites the device without them",
						port.Name, manifest.Name, devName, extra))
				}
			}
			continue
		}
		if _, profileProvided := live.ExpandedDevices[devName]; profileProvided {
			p.Warnings = append(p.Warnings, fmt.Sprintf("port %q of instance %q shadows a profile-provided device %q; profile devices are not managed by lxm", port.Name, manifest.Name, devName))
		}
	}
}

// DetectListenerHandoffs reports listeners that move between instances (or are
// freed by a delete/--prune and claimed elsewhere) within one plan. Apply runs
// instance steps concurrently, so such a move can race; callers surface these
// as warnings (apply ordering is future work).
func DetectListenerHandoffs(configs []*config.Config, live map[string]*InstanceSnapshot, steps []Step) []string {
	desired := make(map[string][]ports.Listener)
	for _, conf := range configs {
		if conf == nil || conf.Base || conf.Name == "" || conf.Status == "absent" {
			continue
		}
		for i := range conf.Ports {
			p := &conf.Ports[i]
			ep, err := ports.ParseEndpoint(p.Proto, p.Listen)
			if err != nil {
				continue
			}
			desired[conf.Name] = append(desired[conf.Name], ports.NewListener(ep, p.NAT))
		}
	}

	liveListeners := func(instance string) []ports.Listener {
		snap := live[instance]
		if snap == nil {
			return nil
		}
		var out []ports.Listener
		for devName, props := range snap.Devices {
			if !ports.IsManagedProxy(devName, props) {
				continue
			}
			dev, ok := ports.ParseProxy(devName, props)
			if !ok {
				continue
			}
			out = append(out, ports.NewListener(dev.Listen, dev.NAT))
		}
		return out
	}

	actionByInstance := make(map[string]string, len(steps))
	for i := range steps {
		actionByInstance[steps[i].Container] = steps[i].Action
	}

	// Abandoned: live managed listeners no longer desired (or instance deleted).
	type namedListener struct {
		instance string
		listener ports.Listener
	}
	var abandoned []namedListener
	for instance := range actionByInstance {
		desiredForInstance := desired[instance]
		for _, ll := range liveListeners(instance) {
			stillDesired := false
			for _, dl := range desiredForInstance {
				if dl == ll || (dl.Proto == ll.Proto && dl.Addr == ll.Addr && dl.Port == ll.Port) {
					stillDesired = true
					break
				}
			}
			if !stillDesired {
				abandoned = append(abandoned, namedListener{instance: instance, listener: ll})
			}
		}
	}

	// Claimed: desired listeners that are not already live on the same instance.
	var claimed []namedListener
	for instance, wanted := range desired {
		if _, inPlan := actionByInstance[instance]; !inPlan {
			continue
		}
		existing := liveListeners(instance)
		for _, dl := range wanted {
			alreadyLive := false
			for _, ll := range existing {
				if dl == ll || (dl.Proto == ll.Proto && dl.Addr == ll.Addr && dl.Port == ll.Port) {
					alreadyLive = true
					break
				}
			}
			if !alreadyLive {
				claimed = append(claimed, namedListener{instance: instance, listener: dl})
			}
		}
	}

	var warnings []string
	seen := make(map[string]bool)
	for _, a := range abandoned {
		for _, c := range claimed {
			if a.instance == c.instance {
				continue
			}
			if !ports.ListenersConflict(a.listener, c.listener) {
				continue
			}
			msg := fmt.Sprintf("listener %s %s moves from instance %q to instance %q in this plan; instance steps run concurrently — re-run apply if the claim fails (or use -j 1)",
				a.listener.Proto, netip.AddrPortFrom(a.listener.Addr, a.listener.Port).String(), a.instance, c.instance)
			if seen[msg] {
				continue
			}
			seen[msg] = true
			warnings = append(warnings, msg)
		}
	}
	slices.Sort(warnings)
	return warnings
}
