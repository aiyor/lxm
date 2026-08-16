package config

import (
	"fmt"
	"net"
	"net/url"
	"sort"
	"strings"
)

// builtinImageRemotes is the locked default remote map. Entries are overridden
// by a same-named image_remotes declaration (IMAGE-SPEC §4.1).
var builtinImageRemotes = map[string]string{
	"ubuntu":       "https://cloud-images.ubuntu.com/releases",
	"ubuntu-daily": "https://cloud-images.ubuntu.com/daily",
	"images":       "https://images.lxd.canonical.com",
}

// SplitImageRef parses an image reference. It returns (remote, alias, true)
// for the remote:alias form and ("", image, false) otherwise (a fingerprint
// or a bare local alias). A reference with more than one ':' is never a valid
// #RemoteAlias, so it is treated as local (the CUE schema rejects it).
func SplitImageRef(image string) (remote, alias string, isRemote bool) {
	idx := strings.IndexByte(image, ':')
	if idx < 0 {
		return "", image, false
	}
	if strings.IndexByte(image[idx+1:], ':') >= 0 {
		return "", image, false
	}
	return image[:idx], image[idx+1:], true
}

// ImageLocalRef returns the local LXD image identity that must exist in the
// local store before create/recreate, for the given resolved instance type
// ("container" | "virtual-machine"). For remote:alias it is the canonical,
// TYPE-QUALIFIED local alias (IMAGE-SPEC §4.2): "<remote>/<alias>" for
// containers and "<remote>/<alias>/vm" for virtual machines, so a container
// image and a VM image for the same reference never collide. For a
// fingerprint or bare alias it is the reference itself (type does not
// participate).
func ImageLocalRef(image, instanceType string) string {
	remote, alias, isRemote := SplitImageRef(image)
	if !isRemote {
		return image
	}
	ref := remote + "/" + alias
	if instanceType == "virtual-machine" {
		ref += "/vm"
	}
	return ref
}

// isLoopbackHost reports whether a URL host is a loopback address. It accepts
// the literal "localhost" and any IP in 127.0.0.0/8 or ::1 (net.IP.IsLoopback).
func isLoopbackHost(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// validateImageRemoteNames checks every image_remotes key against the locked
// remote-name charset (IMAGE-SPEC §2.2). CUE enforces the rule as the
// resolved-form contract (#LXM_RESOLVED pairs #ImageRemoteName with
// #ImageRemoteNameInvalid), but its _|_ error does not name the offending key,
// so this Go check is the precise-diagnostic layer. It is invoked from the
// load path (ValidatePostMerge) and the compile path (MigrateManifest) so both
// produce an actionable message; it is also the sole enforcement for no-schema
// (v1-compat) manifests, which skip CUE entirely.
func validateImageRemoteNames(remotes map[string]string) error {
	for name := range remotes {
		if !imageRemoteNameRe.MatchString(name) {
			return fmt.Errorf(`image_remotes: invalid remote name %q (allowed characters: [a-zA-Z0-9_.-])`, name)
		}
	}
	return nil
}

// CanonicalizeImageRemoteURL validates an image_remotes URL and returns its
// canonical form (IMAGE-SPEC §2.3): the scheme and host lowercased, the path
// trimmed of a trailing slash (dropped entirely when empty), the port
// preserved. The scheme must be https, or http only when the host is loopback.
// Violations return an error surfaced as CONFIG_ERROR (exit 3).
func CanonicalizeImageRemoteURL(name, raw string) (string, error) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return "", fmt.Errorf(`image_remotes[%q]: invalid image remote URL %q`, name, raw)
	}
	if u.Scheme == "" {
		return "", fmt.Errorf(`image_remotes[%q]: invalid image remote URL %q (missing scheme)`, name, raw)
	}
	if u.Host == "" {
		return "", fmt.Errorf(`image_remotes[%q]: invalid image remote URL %q (missing host)`, name, raw)
	}
	switch strings.ToLower(u.Scheme) {
	case "https":
	case "http":
		if !isLoopbackHost(u.Hostname()) {
			return "", fmt.Errorf(`image_remotes[%q]: invalid image remote URL %q (http is only allowed for loopback hosts)`, name, raw)
		}
	default:
		return "", fmt.Errorf(`image_remotes[%q]: invalid image remote URL %q (scheme must be https)`, name, raw)
	}

	path := strings.TrimRight(u.Path, "/")
	if u.RawQuery != "" {
		path += "?" + u.RawQuery
	}
	return strings.ToLower(u.Scheme) + "://" + strings.ToLower(u.Host) + path, nil
}

// BuiltinImageRemotes returns the built-in remote registry. It is the
// effective registry for a fleet with no image_remotes declarations and is
// used by the reconciler and by tests; it never errors.
func BuiltinImageRemotes() map[string]string {
	out, _ := EffectiveImageRemotes(nil)
	return out
}

// EffectiveImageRemotes compiles the effective remote registry for a fleet:
// the built-in remotes as the base layer, overlaid key-wise by every loaded
// manifest's image_remotes declaration (IMAGE-SPEC §4.1). A declaration
// overrides a same-named built-in freely; identical (name, canonical URL)
// duplicates across manifests are deduplicated silently; the same name
// declared with a different canonical URL across two manifests is a conflict
// (exit 3) citing both files.
func EffectiveImageRemotes(configs []*Config) (map[string]string, error) {
	// Deterministic conflict attribution: process manifests in file order.
	sorted := append([]*Config(nil), configs...)
	sort.SliceStable(sorted, func(i, j int) bool { return sorted[i].ConfigFile < sorted[j].ConfigFile })

	declared := make(map[string]string) // remote name -> canonical URL
	attrib := make(map[string]string)   // remote name -> first declaring file
	for _, conf := range sorted {
		if conf == nil || len(conf.ImageRemotes) == 0 {
			continue
		}
		file := conf.ConfigFile
		if file == "" {
			file = conf.ConfigBaseDir
		}
		for name, raw := range conf.ImageRemotes {
			canon, err := CanonicalizeImageRemoteURL(name, raw)
			if err != nil {
				return nil, err
			}
			if prev, ok := declared[name]; ok {
				if prev != canon {
					return nil, fmt.Errorf("image remote %q declared with conflicting URLs (%s vs %s) in %q and %q",
						name, prev, canon, attrib[name], file)
				}
				continue // identical duplicate: silent dedup
			}
			declared[name] = canon
			attrib[name] = file
		}
	}

	out := make(map[string]string, len(builtinImageRemotes)+len(declared))
	for name, raw := range builtinImageRemotes {
		out[name] = raw
	}
	for name, canon := range declared {
		out[name] = canon
	}
	return out, nil
}
