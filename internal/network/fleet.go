package network

import (
	"fmt"
	"net"
	"sort"

	"github.com/aiyor/lxm/internal/config"
)

// VSwitch is a resolved, validated managed virtual switch.
type VSwitch struct {
	config.VSwitchConfig
	Subnet *net.IPNet // canonical network CIDR (e.g. 10.50.0.0/24)
	File   string     // manifest file that declared it (conflict attribution)
}

// EffectiveDriver returns the bridge.driver value (default "native").
func (v *VSwitch) EffectiveDriver() string {
	if v.Driver == "" {
		return "native"
	}
	return v.Driver
}

// EffectiveNAT returns the effective nat value (default true).
func (v *VSwitch) EffectiveNAT() bool {
	return boolVal(v.NAT)
}

// EffectiveInternet returns the effective internet value (default true).
func (v *VSwitch) EffectiveInternet() bool {
	return boolVal(v.Internet)
}

// PolicyRule is a resolved inter-group allowance with attribution.
type PolicyRule struct {
	From      string
	To        string
	Direction string
	File      string
}

// Fleet is the fleet-scoped union of vswitches and network_policy across all
// loaded manifests for an invocation (§7.2).
type Fleet struct {
	VSwitches     []*VSwitch
	ByName        map[string]*VSwitch
	ByGroup       map[string][]*VSwitch
	Allow         []PolicyRule
	InternalCIDRs []string // canonicalized internal set (defaults ∪ operator ∪ managed subnets)
	Warnings      []string
}

// defaultInternalCIDRs is the locked default internal set (§3.2). The IPv6
// entries are retained for the future IPv6 phase; the v1 IPv4-only compiler
// does not emit them as rules.
var defaultInternalCIDRs = []string{
	"10.0.0.0/8",
	"172.16.0.0/12",
	"192.168.0.0/16",
	"100.64.0.0/10",
	"127.0.0.0/8",
	"169.254.0.0/16",
	"::1/128",
	"fe80::/10",
	"fc00::/7",
}

func vswitchEqual(a, b config.VSwitchConfig) bool {
	return a.Name == b.Name &&
		a.Type == b.Type &&
		a.Driver == b.Driver &&
		a.IPv4 == b.IPv4 &&
		a.IPv6 == b.IPv6 &&
		boolVal(a.NAT) == boolVal(b.NAT) &&
		a.Group == b.Group &&
		boolVal(a.Internet) == boolVal(b.Internet)
}

// boolVal reports the effective value of an optional bool, treating a nil
// pointer as its documented default of true.
func boolVal(p *bool) bool {
	return p == nil || *p
}

// Union deduplicates vswitches and network_policy across all loaded manifests,
// resolves group references, and canonicalizes the internal CIDR set.
func Union(configs []*config.Config) (*Fleet, error) {
	f := &Fleet{
		ByName:   make(map[string]*VSwitch),
		ByGroup:  make(map[string][]*VSwitch),
		Warnings: []string{},
	}

	// 1. Vswitch union (identical dedup; conflicting spec -> exit 3).
	for _, conf := range configs {
		file := conf.ConfigFile
		if file == "" {
			file = conf.ConfigBaseDir
		}
		for _, raw := range conf.VSwitches {
			_, ipnet, err := ParseSubnet(raw.IPv4)
			if err != nil {
				return nil, fmt.Errorf("vswitch %q: invalid ipv4 %q: %w", raw.Name, raw.IPv4, err)
			}
			vs := &VSwitch{
				VSwitchConfig: raw,
				Subnet:        ipnet,
				File:          file,
			}

			if existing, ok := f.ByName[raw.Name]; ok {
				if !vswitchEqual(existing.VSwitchConfig, raw) {
					return nil, fmt.Errorf("vswitch %q declared with conflicting specs in %q and %q", raw.Name, existing.File, file)
				}
				continue
			}
			// Cross-vswitch subnet overlap check (fleet level).
			for _, other := range f.VSwitches {
				if overlaps(other.Subnet, ipnet) {
					return nil, fmt.Errorf("vswitch %q: subnet %s overlaps vswitch %q subnet %s", raw.Name, ipnet.String(), other.Name, other.Subnet.String())
				}
			}
			f.ByName[raw.Name] = vs
			f.VSwitches = append(f.VSwitches, vs)
			if raw.Group != "" {
				f.ByGroup[raw.Group] = append(f.ByGroup[raw.Group], vs)
			}
		}
	}

	// 2. network_policy union (identical dedup; conflicting direction -> exit 3).
	policyIndex := make(map[string]PolicyRule) // from\x00to -> rule
	for _, conf := range configs {
		if conf.NetworkPolicy == nil {
			continue
		}
		file := conf.ConfigFile
		if file == "" {
			file = conf.ConfigBaseDir
		}
		for _, a := range conf.NetworkPolicy.Allow {
			dir := a.Direction
			if dir == "" {
				dir = "both"
			}
			key := a.From + "\x00" + a.To
			if prev, ok := policyIndex[key]; ok {
				if prev.Direction != dir {
					return nil, fmt.Errorf("network_policy: conflicting declarations for %q -> %q (%s vs %s) in %q and %q",
						a.From, a.To, prev.Direction, dir, prev.File, file)
				}
				continue
			}
			policyIndex[key] = PolicyRule{From: a.From, To: a.To, Direction: dir, File: file}
		}
	}
	for _, r := range policyIndex {
		f.Allow = append(f.Allow, r)
	}
	sort.Slice(f.Allow, func(i, j int) bool {
		if f.Allow[i].From != f.Allow[j].From {
			return f.Allow[i].From < f.Allow[j].From
		}
		if f.Allow[i].To != f.Allow[j].To {
			return f.Allow[i].To < f.Allow[j].To
		}
		return f.Allow[i].Direction < f.Allow[j].Direction
	})

	// 3. Policy group resolution (exit 3 on unknown group).
	for _, r := range f.Allow {
		if len(f.ByGroup[r.From]) == 0 {
			return nil, fmt.Errorf("network_policy: group %q has no vswitches assigned", r.From)
		}
		if len(f.ByGroup[r.To]) == 0 {
			return nil, fmt.Errorf("network_policy: group %q has no vswitches assigned", r.To)
		}
		if r.From == r.To {
			f.Warnings = append(f.Warnings, fmt.Sprintf("network_policy: rule %q -> %q is a no-op (intra-group is already allowed)", r.From, r.To))
		}
	}

	// 4. Operator internal_cidrs + managed subnets + defaults -> canonical set.
	internalSet := append([]string(nil), defaultInternalCIDRs...)
	internalSet = append(internalSet, f.managedSubnets()...)
	for _, conf := range configs {
		if conf.NetworkPolicy != nil {
			internalSet = append(internalSet, conf.NetworkPolicy.InternalCIDRs...)
		}
	}
	canonical, err := CanonicalizeCIDRs(internalSet)
	if err != nil {
		return nil, err
	}
	f.InternalCIDRs = canonical

	// 5. Cross-field warnings.
	for _, vs := range f.VSwitches {
		if vs.Group != "" && !boolVal(vs.NAT) && boolVal(vs.Internet) {
			f.Warnings = append(f.Warnings, fmt.Sprintf("vswitch %q: nat: false with internet: true emits a wildcard egress with no source NAT (RFC1918 sources dropped upstream)", vs.Name))
		}
	}

	return f, nil
}

func (f *Fleet) managedSubnets() []string {
	out := make([]string, 0, len(f.VSwitches))
	for _, vs := range f.VSwitches {
		out = append(out, vs.Subnet.String())
	}
	return out
}

// Groups returns the sorted set of group names with at least one vswitch.
func (f *Fleet) Groups() []string {
	out := make([]string, 0, len(f.ByGroup))
	for g := range f.ByGroup {
		out = append(out, g)
	}
	sort.Strings(out)
	return out
}
