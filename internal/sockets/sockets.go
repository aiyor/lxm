// Package sockets owns the manifest-to-provider-device encoding for Unix-domain
// socket proxies (issue #17): endpoint parsing/canonicalization, device
// encoding/parsing, namespace ownership predicates and the shared JSON
// projection used by inventory and command output.
//
// Each `sockets:` manifest entry compiles to a native LXD/Incus `proxy` device
// named socket-<name>, carrying the shared lxm ownership marker and the
// sockets namespace marker (see internal/proxydevice). The daemons use the same
// device type for TCP/UDP publishing (`ports:`), so namespace classification is
// marker-based rather than name-based; a marker-carrying socket device stays
// owned by this namespace even when it is malformed or renamed.
//
// The package is dependency-free (stdlib only) so internal/config,
// internal/plan, internal/fleet and internal/output can all import it without
// cycles. It knows nothing about the provider SDKs or about manifest
// inheritance.
package sockets

import (
	"fmt"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"unicode"

	"github.com/aiyor/lxm/internal/proxydevice"
)

const (
	// DevicePrefix is the device-name namespace for lxm-managed socket proxies.
	DevicePrefix = "socket-"
	// ProxyType is the LXD/Incus device type used for socket proxying.
	ProxyType = proxydevice.Type
	// ManagedKey is the shared per-device ownership marker key.
	ManagedKey = proxydevice.ManagedKey
	// ManagedValue is the ownership marker value written by lxm.
	ManagedValue = proxydevice.ManagedValue
	// KindKey is the namespace marker key stamped on socket devices.
	KindKey = proxydevice.KindKey
	// Kind marks the sockets namespace.
	Kind = proxydevice.KindSocket

	// BindHost listens on the endpoint named by the address itself (the daemon
	// default): `listen` is created on the host and connections are forwarded to
	// the instance-side `connect` socket.
	BindHost = "host"
	// BindInstance listens inside the instance: `listen` is created in the
	// instance and connections are forwarded to the host-side `connect` socket.
	BindInstance = "instance"

	// MaxID is the largest accepted listening-socket uid/gid. Both daemons
	// parse the value as a uint32, but lxm limits ids to the portable signed
	// 32-bit range: larger unsigned values are unusual for Unix identities and
	// do not fit in a Go int on 32-bit builds.
	MaxID = 2147483647

	// maxSunPath is the usable Linux sockaddr_un.sun_path size: 108 bytes
	// including the trailing NUL.
	maxSunPath = 107
)

// modeRe accepts a quoted octal file mode with an optional leading zero; the
// canonical form is "0" plus three octal digits (e.g. "0660").
var modeRe = regexp.MustCompile(`^0?[0-7]{3}$`)

// Endpoint is a canonical, validated Unix-domain socket endpoint.
type Endpoint struct {
	Path string // clean absolute path (opaque bytes, no trailing slash)
}

// ParseEndpoint validates and canonicalizes one `unix:<absolute path>`
// endpoint from a manifest. The form is `unix:` followed by an absolute,
// clean path; the daemon-level device encoding uses exactly the same string.
//
// Rejected: empty endpoints, absolute-less or unclean paths (`.`, `..`, `//`,
// trailing slash), abstract `@` sockets, control characters (including NUL),
// non-`unix` schemes (with a targeted ports pointer for tcp/udp), paths longer
// than the Linux sockaddr_un.sun_path limit, and unknown schemes.
func ParseEndpoint(raw string) (Endpoint, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return Endpoint{}, fmt.Errorf("endpoint is required")
	}

	scheme, path, found := proxydevice.Scheme(raw)
	if !found {
		return Endpoint{}, fmt.Errorf("must be a unix:<absolute path> endpoint (for example unix:/run/app.sock)")
	}
	switch scheme {
	case "unix":
		// Supported form.
	case "tcp", "udp":
		return Endpoint{}, fmt.Errorf("protocol %q is not supported for sockets; use ports: instead", scheme)
	default:
		return Endpoint{}, fmt.Errorf("unsupported endpoint scheme %q (must be unix:)", scheme)
	}

	switch {
	case path == "":
		return Endpoint{}, fmt.Errorf("socket path is required after the unix: scheme")
	case strings.HasPrefix(path, "@"):
		return Endpoint{}, fmt.Errorf("abstract unix sockets (unix:@name) are not supported")
	case !strings.HasPrefix(path, "/"):
		return Endpoint{}, fmt.Errorf("socket path %q must be absolute", path)
	case strings.ContainsFunc(path, unicode.IsControl):
		return Endpoint{}, fmt.Errorf("socket path must not contain control characters")
	case filepath.Clean(path) != path:
		return Endpoint{}, fmt.Errorf("socket path %q must be clean (no trailing slash, '.' or '..' segments, or duplicate separators)", path)
	case len(path) > maxSunPath:
		return Endpoint{}, fmt.Errorf("socket path %q is %d bytes; the Linux sun_path limit is %d bytes", path, len(path), maxSunPath)
	}

	return Endpoint{Path: path}, nil
}

// DeviceString renders the daemon-level endpoint form: "unix:<path>".
func (e Endpoint) DeviceString() string { return "unix:" + e.Path }

// String renders the endpoint for human-readable output.
func (e Endpoint) String() string { return e.DeviceString() }

// DeviceName returns the managed device name for a socket name.
func DeviceName(name string) string { return DevicePrefix + name }

// ParseBind validates and canonicalizes the listen side. The daemons accept
// host|instance (plus the legacy guest|container aliases, which are equivalent
// to instance); an empty value is the daemon default host. The canonical form is
// host|instance.
func ParseBind(raw string) (string, bool) {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "", BindHost:
		return BindHost, true
	case BindInstance, "guest", "container":
		return BindInstance, true
	default:
		return "", false
	}
}

// ValidateBind returns a targeted error for an unsupported listen side.
func ValidateBind(raw string) error {
	if _, ok := ParseBind(raw); ok {
		return nil
	}
	return fmt.Errorf("invalid bind %q (must be host or instance)", raw)
}

// ParseMode validates and canonicalizes a listening-socket file mode. Accepted
// input is a quoted octal string of three octal digits with an optional leading
// zero ("660" or "0660"); the canonical form is "0660". Values outside
// 0000-0777 are rejected: the daemons parse any uint32 octal value but pass it
// through os.FileMode/os.Chmod, where only the permission bits behave
// predictably (setuid/setgid/sticky raw bits are silently dropped).
func ParseMode(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", fmt.Errorf("mode is empty")
	}
	if !modeRe.MatchString(raw) {
		return "", fmt.Errorf("mode %q must be a quoted octal file mode between \"0000\" and \"0777\" (for example \"0660\")", raw)
	}
	value, err := strconv.ParseUint(strings.TrimPrefix(raw, "0"), 8, 32)
	if err != nil || value > 0o777 {
		return "", fmt.Errorf("mode %q must be between \"0000\" and \"0777\"", raw)
	}
	return fmt.Sprintf("0%03o", value), nil
}

// ValidateID returns a targeted error for an out-of-range uid/gid.
func ValidateID(field string, value int) error {
	if value < 0 || value > MaxID {
		return fmt.Errorf("%s %d is out of range (0-%d)", field, value, MaxID)
	}
	return nil
}

func parseID(raw string) (int, bool) {
	value, err := strconv.ParseUint(strings.TrimSpace(raw), 10, 32)
	if err != nil || value > MaxID {
		return 0, false
	}
	return int(value), true
}

// BuildDevice renders the provider device payload for one socket proxy. The
// listen side is always emitted explicitly (host or instance): it is part of the
// declarative contract and, on LXD, an explicit bind=host is what makes the
// daemon remove the host-side socket when the device is retired. uid/gid/mode
// are emitted only when declared; the marker pair (ownership + namespace) is
// always emitted.
func BuildDevice(listen, connect Endpoint, bind string, uid, gid *int, mode string) map[string]string {
	if bind != BindInstance {
		bind = BindHost
	}
	props := map[string]string{
		"type":     ProxyType,
		"listen":   listen.DeviceString(),
		"connect":  connect.DeviceString(),
		"bind":     bind,
		ManagedKey: ManagedValue,
		KindKey:    Kind,
	}
	if uid != nil {
		props["uid"] = strconv.Itoa(*uid)
	}
	if gid != nil {
		props["gid"] = strconv.Itoa(*gid)
	}
	if mode != "" {
		props["mode"] = mode
	}
	return props
}

// Device is the manifest-level view of a socket proxy device.
type Device struct {
	Name    string // derived from the socket-<name> device name; empty when foreign-named
	Listen  Endpoint
	Connect Endpoint
	Bind    string
	UID     *int
	GID     *int
	Mode    string // canonical octal form, empty when undeclared
}

// HostPath is the host-side filesystem path of the device: the connect path for
// bind=instance, the listen path for bind=host. Host-side paths must be unique
// across the managed fleet.
func (d Device) HostPath() string {
	if d.Bind == BindInstance {
		return d.Connect.Path
	}
	return d.Listen.Path
}

// InstanceListenPath is the instance-side listening path, or "" for bind=host
// (where the instance side is a connect-only endpoint).
func (d Device) InstanceListenPath() string {
	if d.Bind != BindInstance {
		return ""
	}
	return d.Listen.Path
}

// IsManagedSocket reports whether a live device belongs to the sockets
// namespace.
func IsManagedSocket(props map[string]string) bool {
	return proxydevice.IsSocket(props)
}

// parseUnixProxy parses any proxy device with Unix endpoints into manifest form,
// without requiring lxm ownership. It returns false for non-proxy devices, for
// non-unix devices and for malformed devices; callers decide what a malformed
// managed device means (see internal/plan, which repairs or removes it rather
// than failing the plan).
func parseUnixProxy(deviceName string, props map[string]string) (Device, bool) {
	if props == nil || props["type"] != ProxyType {
		return Device{}, false
	}

	listen, err := ParseEndpoint(props["listen"])
	if err != nil {
		return Device{}, false
	}
	connect, err := ParseEndpoint(props["connect"])
	if err != nil {
		return Device{}, false
	}

	bind, ok := ParseBind(props["bind"])
	if !ok {
		return Device{}, false
	}
	// nat is meaningless for Unix endpoints and rejected by both daemons.
	if nat, ok := proxydevice.ParseBool(props["nat"]); !ok || nat {
		return Device{}, false
	}

	var uid, gid *int
	if raw, present := props["uid"]; present && strings.TrimSpace(raw) != "" {
		value, ok := parseID(raw)
		if !ok {
			return Device{}, false
		}
		uid = &value
	}
	if raw, present := props["gid"]; present && strings.TrimSpace(raw) != "" {
		value, ok := parseID(raw)
		if !ok {
			return Device{}, false
		}
		gid = &value
	}

	mode := ""
	if raw, present := props["mode"]; present && strings.TrimSpace(raw) != "" {
		canonical, err := ParseMode(raw)
		if err != nil {
			return Device{}, false
		}
		mode = canonical
	}

	name := ""
	if rest, ok := strings.CutPrefix(deviceName, DevicePrefix); ok {
		name = rest
	}

	return Device{Name: name, Listen: listen, Connect: connect, Bind: bind, UID: uid, GID: gid, Mode: mode}, true
}

// ParseProxy parses a marker-carrying socket device into manifest form. It
// returns false for devices outside the sockets namespace and for malformed
// socket devices (which callers repair or remove).
func ParseProxy(deviceName string, props map[string]string) (Device, bool) {
	if !IsManagedSocket(props) {
		return Device{}, false
	}
	return parseUnixProxy(deviceName, props)
}

// PublishedSocket is the single JSON shape shared by inventory and command
// output (lxm/result/v1 additive field).
type PublishedSocket struct {
	Device  string `json:"device"`
	Name    string `json:"name,omitempty"`
	Listen  string `json:"listen"`
	Connect string `json:"connect"`
	Bind    string `json:"bind"`
	UID     *int   `json:"uid,omitempty"`
	GID     *int   `json:"gid,omitempty"`
	Mode    string `json:"mode,omitempty"`
	Managed bool   `json:"managed"`
}

// Published projects a device map into deterministic PublishedSocket values.
// Foreign (unmarked) Unix proxy devices are included with Managed=false;
// non-proxy, TCP/UDP proxy and malformed devices are skipped.
func Published(devices map[string]map[string]string) []PublishedSocket {
	if len(devices) == 0 {
		return nil
	}

	names := make([]string, 0, len(devices))
	for name := range devices {
		names = append(names, name)
	}
	sort.Strings(names)

	out := make([]PublishedSocket, 0, len(names))
	for _, name := range names {
		props := devices[name]
		dev, ok := parseUnixProxy(name, props)
		if !ok {
			continue
		}
		out = append(out, PublishedSocket{
			Device:  name,
			Name:    dev.Name,
			Listen:  dev.Listen.DeviceString(),
			Connect: dev.Connect.DeviceString(),
			Bind:    dev.Bind,
			UID:     dev.UID,
			GID:     dev.GID,
			Mode:    dev.Mode,
			Managed: IsManagedSocket(props),
		})
	}
	return out
}
