// Package proxydevice owns the shared LXD/Incus proxy-device primitives used by
// the two declarative proxy features: `ports:` (TCP/UDP host <-> instance
// publishing, PORTS-SPEC) and `sockets:` (Unix-domain socket proxying,
// SOCKETS-SPEC).
//
// Both features compile to the same daemon device type (`proxy`), so ownership
// and namespace classification must be shared: every managed proxy device
// belongs to exactly one manifest namespace, and the plan engine must partition
// live devices without either feature claiming the other's devices. The
// namespace is carried by a device-level marker (user.lxm.kind) because the
// daemons accept arbitrary user.* device keys and store them verbatim; the
// name prefix alone is not authoritative for ownership.
//
// The package is dependency-free (stdlib only) so internal/config,
// internal/plan, internal/fleet and internal/output can import it without
// cycles. It knows nothing about the provider SDKs or about manifest
// inheritance.
package proxydevice

import "strings"

const (
	// Type is the LXD/Incus device type used by every lxm proxy feature.
	Type = "proxy"
	// ManagedKey is the per-device ownership marker key. Both daemons accept
	// arbitrary user.* keys on devices and store them verbatim.
	ManagedKey = "user.lxm.managed"
	// ManagedValue is the ownership marker value written by lxm.
	ManagedValue = "true"
	// KindKey is the namespace marker key stamped on devices that are not part
	// of the original (TCP/UDP) ports namespace. Devices created before the
	// key existed carry no kind and remain owned by the ports namespace.
	KindKey = "user.lxm.kind"
	// KindSocket marks Unix-domain socket proxy devices (`sockets:`).
	KindSocket = "socket"
)

// IsManaged reports whether props describe a proxy device owned by lxm,
// regardless of feature namespace. The plan PUT preserve/skip path uses this so
// devices from every managed proxy namespace are rebuilt from the manifest
// (omission is what deletes retired devices).
func IsManaged(props map[string]string) bool {
	return props != nil && props["type"] == Type && props[ManagedKey] == ManagedValue
}

// Kind returns the namespace marker of a device ("" when absent).
func Kind(props map[string]string) string {
	if props == nil {
		return ""
	}
	return props[KindKey]
}

// IsSocket reports whether a managed proxy device belongs to the sockets
// namespace.
func IsSocket(props map[string]string) bool {
	return IsManaged(props) && Kind(props) == KindSocket
}

// Scheme splits a daemon-level endpoint ("tcp:127.0.0.1:80", "unix:/run/x.sock")
// into its lower-cased connection scheme and the remainder.
func Scheme(endpoint string) (scheme, rest string, ok bool) {
	scheme, rest, ok = strings.Cut(endpoint, ":")
	if !ok {
		return "", "", false
	}
	return strings.ToLower(scheme), rest, true
}

// ParseBool mirrors the daemons' truthiness for hand-edited device values
// (LXD shared.IsTrue / Incus util.IsTrue and their IsFalse counterparts).
// Unknown values return ok=false so callers can treat the device as malformed
// rather than silently reading it as false.
func ParseBool(raw string) (value bool, ok bool) {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "", "0", "false", "no", "off":
		return false, true
	case "1", "true", "yes", "on":
		return true, true
	default:
		return false, false
	}
}
