// Package ports owns the manifest-to-provider-device encoding for published
// ports (issue #11): endpoint parsing/canonicalization, listener-conflict
// semantics, per-device ownership predicates and the shared JSON projection
// used by inventory and command output.
//
// The package is dependency-free (stdlib only) so internal/config,
// internal/plan and internal/fleet can all import it without cycles. It knows
// nothing about the provider SDKs or about manifest inheritance.
package ports

import (
	"fmt"
	"net/netip"
	"sort"
	"strings"

	"github.com/aiyor/lxm/internal/proxydevice"
)

const (
	// DevicePrefix is the device-name namespace for lxm-managed publishers.
	DevicePrefix = "proxy-"
	// ProxyType is the LXD/Incus device type used for port publishing.
	ProxyType = proxydevice.Type
	// ManagedKey is the per-device ownership marker key. Both daemons accept
	// arbitrary user.* keys on devices and store them verbatim.
	ManagedKey = proxydevice.ManagedKey
	// ManagedValue is the marker value written by lxm.
	ManagedValue = proxydevice.ManagedValue
)

// Endpoint is a canonical, validated published-port endpoint.
type Endpoint struct {
	Proto string     // "tcp" | "udp"
	Addr  netip.Addr // explicit, zone-free, never 4-in-6
	Port  uint16     // 1-65535
}

// ParseEndpoint validates and canonicalizes one endpoint from a manifest. The
// accepted form is an explicit IP literal and port ("127.0.0.1:8080",
// "[::1]:80"); protocol prefixes (the device-level "tcp:" form) are rejected
// with a targeted message. Hostnames, zones, 4-in-6 literals and port 0 are
// rejected.
func ParseEndpoint(proto, raw string) (Endpoint, error) {
	proto = strings.ToLower(strings.TrimSpace(proto))
	if proto == "" {
		proto = "tcp"
	}
	if proto != "tcp" && proto != "udp" {
		return Endpoint{}, fmt.Errorf("invalid protocol %q (must be tcp or udp)", proto)
	}

	raw = strings.TrimSpace(raw)
	if raw == "" {
		return Endpoint{}, fmt.Errorf("address is required")
	}
	lower := strings.ToLower(raw)
	if strings.HasPrefix(lower, "tcp:") || strings.HasPrefix(lower, "udp:") {
		return Endpoint{}, fmt.Errorf("do not prefix the protocol; use the proto field")
	}

	ap, err := netip.ParseAddrPort(raw)
	if err != nil {
		return Endpoint{}, fmt.Errorf("must be an explicit IP:port address (IPv6 must be bracketed): %w", err)
	}

	addr := ap.Addr()
	if !addr.IsValid() {
		return Endpoint{}, fmt.Errorf("invalid IP address %q", addr.String())
	}
	if addr.Zone() != "" {
		return Endpoint{}, fmt.Errorf("IPv6 zone identifiers are not supported")
	}
	if addr.Is4In6() {
		return Endpoint{}, fmt.Errorf("4-in-6 address literals are not supported")
	}
	if ap.Port() == 0 {
		return Endpoint{}, fmt.Errorf("port must be in 1-65535")
	}

	return Endpoint{Proto: proto, Addr: addr, Port: ap.Port()}, nil
}

// HostPort renders the endpoint as "<ip>:<port>" with IPv6 bracketed.
func (e Endpoint) HostPort() string {
	return netip.AddrPortFrom(e.Addr, e.Port).String()
}

// DeviceString renders the daemon-level endpoint form: "<proto>:<addr>:<port>".
func (e Endpoint) DeviceString() string {
	return e.Proto + ":" + e.HostPort()
}

// IsLoopback reports whether the endpoint address is a loopback address.
func (e Endpoint) IsLoopback() bool { return e.Addr.IsLoopback() }

// IsWildcard reports whether the endpoint address is unspecified (0.0.0.0 or ::).
func (e Endpoint) IsWildcard() bool { return e.Addr.IsUnspecified() }

// IsIPv6 reports whether the endpoint address is IPv6.
func (e Endpoint) IsIPv6() bool { return e.Addr.Is6() }

// Listener is a published listener plus its NAT mode. NAT mode matters for
// conflict analysis: NAT rules are family-specific, while non-NAT listeners
// bind sockets (a wildcard bind is dual-stack).
type Listener struct {
	Proto string
	Addr  netip.Addr
	Port  uint16
	NAT   bool
}

// NewListener builds a Listener from a canonical endpoint.
func NewListener(e Endpoint, nat bool) Listener {
	return Listener{Proto: e.Proto, Addr: e.Addr, Port: e.Port, NAT: nat}
}

// ListenersConflict reports whether two listeners of the same protocol and
// port collide on one host.
//
// Rules (verified against Go's dual-stack wildcard behaviour):
//   - a wildcard listener of either family conflicts with every same-port
//     address of either family (non-NAT wildcard binds use net.Listen("tcp",
//     "0.0.0.0:p") / "[::]:p", which yields an AF_INET6 socket with
//     IPV6_V6ONLY=0);
//   - otherwise conflict is exact address equality (a specific 127.0.0.1 and a
//     specific [::1] are independent sockets). NAT rules are family-specific,
//     so the wildcard rule is conservative for NAT listeners too (v1 rejects
//     wildcard NAT listens at authoring time).
func ListenersConflict(a, b Listener) bool {
	if a.Proto != b.Proto || a.Port != b.Port {
		return false
	}
	if a.Addr.IsUnspecified() || b.Addr.IsUnspecified() {
		return true
	}
	return a.Addr == b.Addr
}

// DeviceName returns the managed device name for a port name.
func DeviceName(name string) string { return DevicePrefix + name }

// BuildDevice renders the provider device payload for one published port.
// The marker is always emitted; nat is emitted only when enabled (both daemons
// default it to false).
func BuildDevice(listen, connect Endpoint, nat bool) map[string]string {
	props := map[string]string{
		"type":     ProxyType,
		"listen":   listen.DeviceString(),
		"connect":  connect.DeviceString(),
		ManagedKey: ManagedValue,
	}
	if nat {
		props["nat"] = "true"
	}
	return props
}

// Device is the manifest-level view of a proxy device.
type Device struct {
	Name    string // derived from the proxy-<name> device name; empty when foreign-named
	Listen  Endpoint
	Connect Endpoint
	NAT     bool
}

// IsManagedProxy reports whether a device is owned by the ports namespace.
// Ownership is marker-based and independent of the device name: a
// marker-carrying proxy device is lxm-managed even if it is malformed or not
// named proxy-<name>. Proxy devices carrying the sockets namespace marker
// (user.lxm.kind: "socket") belong to internal/sockets instead and are
// explicitly excluded here, so the two features never claim each other's
// devices.
func IsManagedProxy(deviceName string, props map[string]string) bool {
	return proxydevice.IsManaged(props) && !proxydevice.IsSocket(props)
}

// ParseProxy parses any proxy device (managed or foreign) into manifest form.
// It returns false for non-proxy devices and for malformed proxy devices; the
// caller decides what a malformed managed device means (see internal/plan,
// which repairs or removes it rather than failing the plan).
func ParseProxy(deviceName string, props map[string]string) (Device, bool) {
	if props == nil || props["type"] != ProxyType {
		return Device{}, false
	}

	listenProto, listenRaw, ok := strings.Cut(props["listen"], ":")
	if !ok {
		return Device{}, false
	}
	connectProto, connectRaw, ok := strings.Cut(props["connect"], ":")
	if !ok {
		return Device{}, false
	}
	if !strings.EqualFold(listenProto, connectProto) {
		return Device{}, false
	}

	listen, err := ParseEndpoint(listenProto, listenRaw)
	if err != nil {
		return Device{}, false
	}
	connect, err := ParseEndpoint(connectProto, connectRaw)
	if err != nil {
		return Device{}, false
	}

	nat, ok := ParseBool(props["nat"])
	if !ok {
		return Device{}, false
	}

	name := ""
	if strings.HasPrefix(deviceName, DevicePrefix) {
		name = strings.TrimPrefix(deviceName, DevicePrefix)
	}

	return Device{Name: name, Listen: listen, Connect: connect, NAT: nat}, true
}

// ParseBool mirrors the daemons' truthiness for hand-edited device values.
// It is a thin alias for proxydevice.ParseBool, kept exported for callers that
// already depend on the ports package.
func ParseBool(raw string) (value bool, ok bool) {
	return proxydevice.ParseBool(raw)
}

// PublishedPort is the single JSON shape shared by inventory and command
// output (lxm/result/v1 additive field).
type PublishedPort struct {
	Device  string `json:"device"`
	Name    string `json:"name,omitempty"`
	Proto   string `json:"proto"`
	Listen  string `json:"listen"`
	Connect string `json:"connect"`
	NAT     bool   `json:"nat"`
	Managed bool   `json:"managed"`
}

// Published projects a device map into deterministic PublishedPort values.
// Unmanaged proxy devices are included with Managed=false; non-proxy and
// malformed devices are skipped.
func Published(devices map[string]map[string]string) []PublishedPort {
	if len(devices) == 0 {
		return nil
	}

	names := make([]string, 0, len(devices))
	for name := range devices {
		names = append(names, name)
	}
	sort.Strings(names)

	out := make([]PublishedPort, 0, len(names))
	for _, name := range names {
		props := devices[name]
		if props == nil || props["type"] != ProxyType || proxydevice.IsSocket(props) {
			continue // socket-namespace devices belong to published_sockets
		}
		dev, ok := ParseProxy(name, props)
		if !ok {
			continue
		}
		out = append(out, PublishedPort{
			Device:  name,
			Name:    dev.Name,
			Proto:   dev.Listen.Proto,
			Listen:  dev.Listen.HostPort(),
			Connect: dev.Connect.HostPort(),
			NAT:     dev.NAT,
			Managed: IsManagedProxy(name, props),
		})
	}
	return out
}
