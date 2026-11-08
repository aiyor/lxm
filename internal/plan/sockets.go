package plan

import (
	"fmt"
	"maps"
	"slices"
	"sort"
	"strings"

	"github.com/aiyor/lxm/internal/config"
	"github.com/aiyor/lxm/internal/sockets"
)

// buildSocketDevices compiles manifest sockets into managed proxy device
// payloads.
func buildSocketDevices(manifest *config.Config) (map[string]map[string]string, error) {
	out := make(map[string]map[string]string, len(manifest.Sockets))
	for i := range manifest.Sockets {
		sock := &manifest.Sockets[i]
		listen, err := sockets.ParseEndpoint(sock.Listen)
		if err != nil {
			return nil, fmt.Errorf("socket %q: invalid listen %q: %w", sock.Name, sock.Listen, err)
		}
		connect, err := sockets.ParseEndpoint(sock.Connect)
		if err != nil {
			return nil, fmt.Errorf("socket %q: invalid connect %q: %w", sock.Name, sock.Connect, err)
		}
		bind, ok := sockets.ParseBind(sock.Bind)
		if !ok {
			return nil, fmt.Errorf("socket %q: invalid bind %q (must be host or instance)", sock.Name, sock.Bind)
		}
		mode := ""
		if sock.Mode != "" {
			mode, err = sockets.ParseMode(string(sock.Mode))
			if err != nil {
				return nil, fmt.Errorf("socket %q: %w", sock.Name, err)
			}
		}
		out[sockets.DeviceName(sock.Name)] = sockets.BuildDevice(listen, connect, bind, sock.UID, sock.GID, mode)
	}
	return out, nil
}

// findSocketOwnershipConflicts rejects a manifest socket whose device name is
// already taken by an existing device that the sockets namespace does not own.
// Silent adoption is unsafe: Incus treats a marker-only device change as an
// update and fails with ErrCannotUpdate, while LXD would remove+add with a
// listener blip.
func findSocketOwnershipConflicts(manifest *config.Config, live *InstanceSnapshot) error {
	if live == nil {
		return nil
	}
	for i := range manifest.Sockets {
		devName := sockets.DeviceName(manifest.Sockets[i].Name)
		if props, ok := live.Devices[devName]; ok && !sockets.IsManagedSocket(props) {
			return fmt.Errorf("device %q for instance %q exists but is not owned by the sockets namespace; remove it or rename the socket", devName, manifest.Name)
		}
	}
	return nil
}

// liveSocket is the plan-layer view of a marker-carrying socket device.
// Malformed managed devices are retained so the diff can repair or remove them
// rather than failing the plan. Device names are the canonical identity used for
// matching (ownership is marker-based, so a managed device can legally exist
// under a name that is not socket-<socket-name>).
type liveSocket struct {
	Device    string
	Config    config.SocketConfig
	Malformed bool
	Raw       map[string]string
}

// socketConfigFromDevice converts a parsed live device into the manifest-level
// representation used by the diff.
func socketConfigFromDevice(dev sockets.Device) config.SocketConfig {
	return config.SocketConfig{
		Name:    dev.Name,
		Listen:  dev.Listen.DeviceString(),
		Connect: dev.Connect.DeviceString(),
		Bind:    dev.Bind,
		UID:     dev.UID,
		GID:     dev.GID,
		Mode:    config.SocketMode(dev.Mode),
	}
}

func getLiveSockets(live *InstanceSnapshot) []liveSocket {
	if live == nil {
		return nil
	}
	out := make([]liveSocket, 0, len(live.Devices))
	for devName, props := range live.Devices {
		if !sockets.IsManagedSocket(props) {
			continue // ports-namespace and foreign devices are not diffed here
		}
		if dev, ok := sockets.ParseProxy(devName, props); ok {
			out = append(out, liveSocket{Device: devName, Config: socketConfigFromDevice(dev), Raw: props})
			continue
		}
		out = append(out, liveSocket{Device: devName, Malformed: true, Raw: props})
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Device < out[j].Device })
	return out
}

// equalIntPtr compares optional uid/gid values by value.
func equalIntPtr(a, b *int) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return *a == *b
}

// intOrNil projects an optional id into a nil-able diff value (a nil *int inside
// an `any` would marshal as null instead of being omitted).
func intOrNil(value *int) any {
	if value == nil {
		return nil
	}
	return *value
}

// normalizeSockets defensively materializes defaults and canonical values so
// plan tests that build config.Config literals behave like loader output.
func normalizeSockets(in []config.SocketConfig) ([]config.SocketConfig, error) {
	out := make([]config.SocketConfig, 0, len(in))
	for i := range in {
		sock := in[i]
		bind, ok := sockets.ParseBind(sock.Bind)
		if !ok {
			return nil, fmt.Errorf("socket %q: invalid bind %q (must be host or instance)", sock.Name, sock.Bind)
		}
		sock.Bind = bind

		listen, err := sockets.ParseEndpoint(sock.Listen)
		if err != nil {
			return nil, fmt.Errorf("socket %q: invalid listen %q: %w", sock.Name, sock.Listen, err)
		}
		connect, err := sockets.ParseEndpoint(sock.Connect)
		if err != nil {
			return nil, fmt.Errorf("socket %q: invalid connect %q: %w", sock.Name, sock.Connect, err)
		}
		sock.Listen = listen.DeviceString()
		sock.Connect = connect.DeviceString()

		if sock.Mode != "" {
			mode, err := sockets.ParseMode(string(sock.Mode))
			if err != nil {
				return nil, fmt.Errorf("socket %q: %w", sock.Name, err)
			}
			sock.Mode = config.SocketMode(mode)
		}
		if sock.UID != nil {
			if err := sockets.ValidateID("uid", *sock.UID); err != nil {
				return nil, fmt.Errorf("socket %q: %w", sock.Name, err)
			}
		}
		if sock.GID != nil {
			if err := sockets.ValidateID("gid", *sock.GID); err != nil {
				return nil, fmt.Errorf("socket %q: %w", sock.Name, err)
			}
		}
		out = append(out, sock)
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

// socketRemovalField names a removed device. Canonical socket-<name> devices use
// the socket-diff namespace; anomalous marked devices under any other name are
// not declared sockets, so they use a device-qualified field to avoid colliding
// with a real socket's sockets[<name>] entry in the same plan.
func socketRemovalField(device string) string {
	if name, ok := strings.CutPrefix(device, sockets.DevicePrefix); ok && name != "" {
		return "sockets[" + name + "]"
	}
	return "devices[" + device + "]"
}

// canonicalSocketDevice renders the exact device payload lxm would write for a
// socket. Comparing it with the live device map catches extra properties and
// non-canonical representations (for example a legacy `guest` bind alias or an
// explicitly-omitted bind) that the scalar field diffs do not model; those
// would otherwise be silently dropped by a later rewrite.
func canonicalSocketDevice(sock config.SocketConfig) (map[string]string, bool) {
	listen, err := sockets.ParseEndpoint(sock.Listen)
	if err != nil {
		return nil, false
	}
	connect, err := sockets.ParseEndpoint(sock.Connect)
	if err != nil {
		return nil, false
	}
	bind, ok := sockets.ParseBind(sock.Bind)
	if !ok {
		return nil, false
	}
	mode := ""
	if sock.Mode != "" {
		mode, err = sockets.ParseMode(string(sock.Mode))
		if err != nil {
			return nil, false
		}
	}
	return sockets.BuildDevice(listen, connect, bind, sock.UID, sock.GID, mode), true
}

// liveSocketDrift reports whether the live device map differs from the
// canonical representation of the same live device. Semantic changes are
// ordinary field diffs, not drift; only alternate spellings, the legacy
// guest/container bind aliases, an omitted bind and extra properties count here.
func liveSocketDrift(device string, raw map[string]string) bool {
	dev, ok := sockets.ParseProxy(device, raw)
	if !ok {
		return false // malformed devices are repaired/removed via their own path
	}
	want, ok := canonicalSocketDevice(socketConfigFromDevice(dev))
	if !ok {
		return false
	}
	return !maps.Equal(want, raw)
}

// diffSockets compares desired sockets with managed live socket devices and
// emits per-socket FieldDiff entries (mirroring diffPorts naming). Both sides
// are keyed by canonical device name (socket-<name>); a marker-carrying device
// under any other name is removable junk and can never satisfy a manifest
// socket. It also rejects unmarked ownership collisions.
func diffSockets(manifest *config.Config, live *InstanceSnapshot) ([]FieldDiff, error) {
	if err := findSocketOwnershipConflicts(manifest, live); err != nil {
		return nil, err
	}

	desired, err := normalizeSockets(manifest.Sockets)
	if err != nil {
		return nil, err
	}
	desiredByDevice := make(map[string]config.SocketConfig, len(desired))
	for _, sock := range desired {
		desiredByDevice[sockets.DeviceName(sock.Name)] = sock
	}

	liveParsed := make(map[string]config.SocketConfig)
	liveRaw := make(map[string]map[string]string)
	liveMalformed := make(map[string]map[string]string)
	for _, ls := range getLiveSockets(live) {
		if ls.Malformed {
			liveMalformed[ls.Device] = ls.Raw
			continue
		}
		liveParsed[ls.Device] = ls.Config
		liveRaw[ls.Device] = ls.Raw
	}

	diffs := make([]FieldDiff, 0, len(desired)+len(liveParsed)+len(liveMalformed))
	for _, ds := range desired {
		devName := sockets.DeviceName(ds.Name)
		ls, exists := liveParsed[devName]
		if !exists {
			if raw, bad := liveMalformed[devName]; bad {
				// Repair a malformed managed device in place.
				diffs = append(diffs, FieldDiff{Field: "sockets[" + ds.Name + "]", Old: raw, New: ds})
				continue
			}
			diffs = append(diffs, FieldDiff{Field: "sockets[" + ds.Name + "]", Old: nil, New: ds})
			continue
		}

		// Non-canonical device representation (extra properties, legacy bind
		// aliases) would be silently dropped by the next rewrite; surface the
		// device repair so the plan is honest.
		if liveSocketDrift(devName, liveRaw[devName]) {
			diffs = append(diffs, FieldDiff{Field: "sockets[" + ds.Name + "]", Old: liveRaw[devName], New: ds})
		}

		if ls.Listen != ds.Listen {
			diffs = append(diffs, FieldDiff{Field: "sockets[" + ds.Name + "].listen", Old: ls.Listen, New: ds.Listen})
		}
		if ls.Connect != ds.Connect {
			diffs = append(diffs, FieldDiff{Field: "sockets[" + ds.Name + "].connect", Old: ls.Connect, New: ds.Connect})
		}
		if ls.Bind != ds.Bind {
			diffs = append(diffs, FieldDiff{Field: "sockets[" + ds.Name + "].bind", Old: ls.Bind, New: ds.Bind})
		}
		if !equalIntPtr(ls.UID, ds.UID) {
			diffs = append(diffs, FieldDiff{Field: "sockets[" + ds.Name + "].uid", Old: intOrNil(ls.UID), New: intOrNil(ds.UID)})
		}
		if !equalIntPtr(ls.GID, ds.GID) {
			diffs = append(diffs, FieldDiff{Field: "sockets[" + ds.Name + "].gid", Old: intOrNil(ls.GID), New: intOrNil(ds.GID)})
		}
		if ls.Mode != ds.Mode {
			diffs = append(diffs, FieldDiff{Field: "sockets[" + ds.Name + "].mode", Old: string(ls.Mode), New: string(ds.Mode)})
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
		fieldName := socketRemovalField(devName)
		if ls, ok := liveParsed[devName]; ok {
			diffs = append(diffs, FieldDiff{Field: fieldName, Old: ls, New: nil})
			continue
		}
		diffs = append(diffs, FieldDiff{Field: fieldName, Old: liveMalformed[devName], New: nil})
	}

	return diffs, nil
}

// appendSocketWarnings adds per-manifest socket plan warnings. Host-bound
// sockets are host-local listeners that bypass network_policy (like ports);
// non-canonical managed devices are rewritten on the next update; a manifest
// socket that shadows a profile-provided device is not managed by lxm.
func appendSocketWarnings(p *Plan, manifest *config.Config, live *InstanceSnapshot) {
	for i := range manifest.Sockets {
		sock := &manifest.Sockets[i]
		bind, ok := sockets.ParseBind(sock.Bind)
		if !ok {
			continue
		}
		if bind == sockets.BindHost {
			if listen, err := sockets.ParseEndpoint(sock.Listen); err == nil {
				p.Warnings = append(p.Warnings, fmt.Sprintf("socket %q of instance %q uses bind: host and creates a host-side unix socket at %s; host-local processes with access to the socket reach the instance service directly, bypassing network_policy",
					sock.Name, manifest.Name, listen.Path))
			}
		}

		if live == nil {
			continue
		}
		devName := sockets.DeviceName(sock.Name)
		if localProps, local := live.Devices[devName]; local {
			// Managed devices with a non-canonical representation are rewritten
			// canonically on the next update; warn so the change is not silent.
			if sockets.IsManagedSocket(localProps) && liveSocketDrift(devName, localProps) {
				p.Warnings = append(p.Warnings, fmt.Sprintf("socket %q of instance %q: proxy device %q has non-canonical device properties; the next update rewrites them to the managed form",
					sock.Name, manifest.Name, devName))
			}
			continue
		}
		if _, profileProvided := live.ExpandedDevices[devName]; profileProvided {
			p.Warnings = append(p.Warnings, fmt.Sprintf("socket %q of instance %q shadows a profile-provided device %q; profile devices are not managed by lxm", sock.Name, manifest.Name, devName))
		}
	}
}

// detectSocketHandoffs reports host-bound socket paths that move between
// instances within one plan. Instance steps run concurrently, so a host-side
// listener released by instance A and claimed by B can race; callers surface
// these as warnings (apply ordering is future work). Only bind=host creates a
// host-side listener; bind=instance host endpoints are connector targets.
func detectSocketHandoffs(configs []*config.Config, live map[string]*InstanceSnapshot, steps []Step) []string {
	desired := make(map[string][]string) // instance -> host-side listen paths
	for _, conf := range configs {
		if conf == nil || conf.Base || conf.Name == "" || conf.Status == "absent" {
			continue
		}
		for i := range conf.Sockets {
			sock := &conf.Sockets[i]
			bind, ok := sockets.ParseBind(sock.Bind)
			if !ok || bind != sockets.BindHost {
				continue
			}
			listen, err := sockets.ParseEndpoint(sock.Listen)
			if err != nil {
				continue
			}
			desired[conf.Name] = append(desired[conf.Name], listen.Path)
		}
	}

	livePaths := func(instance string) []string {
		snap := live[instance]
		if snap == nil {
			return nil
		}
		var out []string
		for devName, props := range snap.Devices {
			if !sockets.IsManagedSocket(props) {
				continue
			}
			dev, ok := sockets.ParseProxy(devName, props)
			if !ok || dev.Bind != sockets.BindHost {
				continue
			}
			out = append(out, dev.Listen.Path)
		}
		return out
	}

	actionByInstance := make(map[string]string, len(steps))
	for i := range steps {
		actionByInstance[steps[i].Container] = steps[i].Action
	}

	type namedPath struct {
		instance string
		path     string
	}

	// Abandoned: live host listeners of stepped instances no longer desired.
	var abandoned []namedPath
	for instance := range actionByInstance {
		desiredForInstance := desired[instance]
		for _, path := range livePaths(instance) {
			if !slices.Contains(desiredForInstance, path) {
				abandoned = append(abandoned, namedPath{instance: instance, path: path})
			}
		}
	}

	// Claimed: desired host listeners that are not already live on the same
	// instance.
	var claimed []namedPath
	for instance, wanted := range desired {
		if _, inPlan := actionByInstance[instance]; !inPlan {
			continue
		}
		existing := livePaths(instance)
		for _, path := range wanted {
			if !slices.Contains(existing, path) {
				claimed = append(claimed, namedPath{instance: instance, path: path})
			}
		}
	}

	var warnings []string
	seen := make(map[string]bool)
	for _, a := range abandoned {
		for _, c := range claimed {
			if a.instance == c.instance || a.path != c.path {
				continue
			}
			msg := fmt.Sprintf("unix socket path %q moves from instance %q to instance %q in this plan; instance steps run concurrently — re-run apply if the claim fails (or use -j 1)",
				a.path, a.instance, c.instance)
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
