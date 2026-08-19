package network

import (
	"fmt"
	"net"
	"sort"
	"strings"

	"github.com/aiyor/lxm/internal/provider"
)

// Rule is a single compiled ACL rule.
type Rule struct {
	Direction       string `json:"direction"`   // "egress" | "ingress"
	Action          string `json:"action"`      // "allow" | "reject"
	Source          string `json:"source"`      // CIDR
	Destination     string `json:"destination"` // CIDR
	Protocol        string `json:"protocol,omitempty"`
	DestinationPort string `json:"destination_port,omitempty"`
	ICMPType        string `json:"icmp_type,omitempty"`
}

// CompiledACL is the deterministic rule set for one grouped vswitch.
type CompiledACL struct {
	Name        string
	Description string
	Rules       []Rule
}

// APIRule is the network-ACL rule payload shape the reconciler submits.
type APIRule = provider.NetworkACLRule

// aclName returns the lxm-owned ACL name for a vswitch.
func aclName(vswitchName string) string {
	return "lxm-" + vswitchName
}

// ACLName returns the lxm-owned ACL name for a vswitch (exported for the
// reconciler's adoption/ownership checks).
func ACLName(vswitchName string) string {
	return aclName(vswitchName)
}

// Compile generates the ACL rule set for every grouped vswitch. It is a pure
// function of (vswitches, network_policy): identical input yields identical
// output, and rules within each ACL are deterministically ordered.
func Compile(f *Fleet) []*CompiledACL {
	aclIndex := make(map[string]*CompiledACL)
	for _, vs := range f.VSwitches {
		if vs.Status == "absent" || vs.Group == "" {
			continue
		}
		aclIndex[vs.Name] = &CompiledACL{
			Name:        aclName(vs.Name),
			Description: fmt.Sprintf("lxm managed policy for vswitch %s (group %s)", vs.Name, vs.Group),
			Rules:       []Rule{},
		}
	}

	for _, vs := range f.VSwitches {
		if vs.Status == "absent" || vs.Group == "" {
			continue
		}
		acl := aclIndex[vs.Name]
		src := vs.Subnet.String()

		// G0 — intra-vswitch (R1) for OVN: because OVN evaluates ACLs per port,
		// default reject would block intra-switch traffic unless explicitly allowed.
		if vs.EffectiveType() == "ovn" {
			acl.Rules = append(acl.Rules,
				Rule{Direction: "egress", Action: "allow", Source: src, Destination: src},
				Rule{Direction: "ingress", Action: "allow", Source: src, Destination: src},
			)
		}

		// G1/G2 — intra-group (R2): all peers share the group.
		for _, peer := range f.ByGroup[vs.Group] {
			if peer.Name == vs.Name {
				continue
			}
			peerNet := peer.Subnet.String()
			acl.Rules = append(acl.Rules,
				Rule{Direction: "egress", Action: "allow", Source: src, Destination: peerNet},
				Rule{Direction: "ingress", Action: "allow", Source: peerNet, Destination: src},
			)
		}

		// G3/G5 — policies from this group (R4/R5).
		for _, rule := range f.Allow {
			if rule.From != vs.Group {
				continue
			}
			for _, peer := range f.ByGroup[rule.To] {
				if peer.Name == vs.Name {
					continue
				}
				peerNet := peer.Subnet.String()
				if rule.Direction == "both" || rule.Direction == "egress" {
					acl.Rules = append(acl.Rules, Rule{Direction: "egress", Action: "allow", Source: src, Destination: peerNet})
				}
				if rule.Direction == "both" {
					acl.Rules = append(acl.Rules, Rule{Direction: "ingress", Action: "allow", Source: peerNet, Destination: src})
				}
			}
		}

		// G4/G6 — policies toward this group (R4/R5).
		for _, rule := range f.Allow {
			if rule.To != vs.Group {
				continue
			}
			for _, peer := range f.ByGroup[rule.From] {
				if peer.Name == vs.Name {
					continue
				}
				peerNet := peer.Subnet.String()
				if rule.Direction == "both" {
					acl.Rules = append(acl.Rules, Rule{Direction: "egress", Action: "allow", Source: src, Destination: peerNet})
				}
				if rule.Direction == "both" || rule.Direction == "egress" {
					acl.Rules = append(acl.Rules, Rule{Direction: "ingress", Action: "allow", Source: peerNet, Destination: src})
				}
			}
		}

		// G7 — internet egress (R6). G8 reject rules are generated only when
		// G7 exists; otherwise the reject default action already covers (R6).
		if boolVal(vs.Internet) {
			acl.Rules = append(acl.Rules, Rule{Direction: "egress", Action: "allow", Source: src, Destination: "0.0.0.0/0"})
			acl.Rules = append(acl.Rules, compileRejectRules(f, vs)...)
		} else if vs.EffectiveType() == "ovn" {
			// On internet: false OVN networks, the provider daemon baseline rule installs a priority 200 allow
			// for DNS (port 53) to the upstream router/resolver, which would sit above default reject (0).
			// To strictly seal this DNS exfiltration leak (EC-18 / R8), emit priority 400 reject rules on port 53.
			for _, res := range vs.DNSResolvers {
				if isIPv4CIDR(res) {
					acl.Rules = append(acl.Rules,
						Rule{Direction: "egress", Action: "reject", Source: src, Destination: res, Protocol: "udp", DestinationPort: "53"},
						Rule{Direction: "egress", Action: "reject", Source: src, Destination: res, Protocol: "tcp", DestinationPort: "53"},
					)
				}
			}
		}

		acl.Rules = dedupRules(acl.Rules)
		sort.SliceStable(acl.Rules, func(i, j int) bool {
			return ruleLess(acl.Rules[i], acl.Rules[j])
		})
	}

	out := make([]*CompiledACL, 0, len(aclIndex))
	for _, acl := range aclIndex {
		out = append(out, acl)
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].Name < out[j].Name
	})
	return out
}

// PermittedEgress returns the destination subnets this vswitch's own egress
// allows reach (G1/G3/G4 destinations), excluding its own subnet. Used as the
// carve-out set for G8 decomposition.
func PermittedEgress(f *Fleet, vs *VSwitch) []string {
	set := make(map[string]bool)
	for _, peer := range f.ByGroup[vs.Group] {
		if peer.Name != vs.Name {
			set[peer.Subnet.String()] = true
		}
	}
	for _, rule := range f.Allow {
		if rule.From == vs.Group {
			for _, peer := range f.ByGroup[rule.To] {
				if peer.Name != vs.Name {
					set[peer.Subnet.String()] = true
				}
			}
		}
		if rule.To == vs.Group && rule.Direction == "both" {
			for _, peer := range f.ByGroup[rule.From] {
				if peer.Name != vs.Name {
					set[peer.Subnet.String()] = true
				}
			}
		}
	}
	out := make([]string, 0, len(set))
	for s := range set {
		out = append(out, s)
	}
	sort.Strings(out)
	return out
}

// compileRejectRules implements G8: egress rejects covering every internal
// CIDR not decomposed out by a permitted allowance, plus the vswitch's own
// subnet for bridges (host-gateway protection, NETWORK-SPEC §5.2). Only IPv4
// subjects are emitted in v1 (C2/C6).
func compileRejectRules(f *Fleet, vs *VSwitch) []Rule {
	src := vs.Subnet.String()
	carveNets := make([]*net.IPNet, 0)
	for _, c := range PermittedEgress(f, vs) {
		n, err := ParseCIDR(c)
		if err == nil {
			carveNets = append(carveNets, n)
		}
	}

	// For OVN vswitches, vs.Subnet and vs.DNSResolvers are carved out of the internal supernets
	// so that decomposed reject rules never shadow G0 (intra-switch allow) or uplink DNS queries.
	// For bridge vswitches, vs.Subnet is intentionally left in the reject set
	// to protect the host gateway IP alias (.1).
	if vs.EffectiveType() == "ovn" {
		carveNets = append(carveNets, vs.Subnet)
		for _, res := range vs.DNSResolvers {
			if rNet, err := ParseCIDR(res); err == nil && isIPv4CIDR(res) {
				carveNets = append(carveNets, rNet)
			}
		}
	}

	rejectSet := make(map[string]bool)
	for _, cidr := range f.InternalCIDRs {
		if !isIPv4CIDR(cidr) {
			continue // IPv6 defaults deferred to the future IPv6 phase
		}
		super, err := ParseCIDR(cidr)
		if err != nil {
			continue
		}
		for _, piece := range SubtractCIDRs(super, carveNets) {
			rejectSet[piece.String()] = true
		}
	}

	if vs.EffectiveType() == "bridge" {
		// The bridge vswitch's own subnet is always a reject subject (NETWORK-SPEC §5.2 host
		// protection) — added explicitly only when it is not already covered by a
		// decomposed internal supernet (e.g. an RFC1918 subnet under 10.0.0.0/8).
		if !cidrCoveredByAny(rejectSet, vs.Subnet.String()) {
			rejectSet[vs.Subnet.String()] = true
		}
	}

	rejects := make([]string, 0, len(rejectSet))
	for r := range rejectSet {
		rejects = append(rejects, r)
	}
	sort.Strings(rejects)

	var rules []Rule
	// For OVN vswitches with carved DNS resolvers, emit port-guard reject rules
	// so that ONLY DNS (port 53 UDP/TCP) can reach the resolver IP, while all other
	// host gateway services (SSH, API, HTTP, ICMP) are strictly rejected.
	if vs.EffectiveType() == "ovn" {
		for _, res := range vs.DNSResolvers {
			if isIPv4CIDR(res) {
				rules = append(rules,
					Rule{Direction: "egress", Action: "reject", Source: src, Destination: res, Protocol: "tcp", DestinationPort: "1-52,54-65535"},
					Rule{Direction: "egress", Action: "reject", Source: src, Destination: res, Protocol: "udp", DestinationPort: "1-52,54-65535"},
					Rule{Direction: "egress", Action: "reject", Source: src, Destination: res, Protocol: "icmp4"},
				)
			}
		}
	}

	for _, r := range rejects {
		rules = append(rules, Rule{Direction: "egress", Action: "reject", Source: src, Destination: r})
	}
	return rules
}

func dedupRules(rules []Rule) []Rule {
	seen := make(map[string]bool)
	var out []Rule
	for _, r := range rules {
		key := r.Direction + "\x00" + r.Action + "\x00" + r.Source + "\x00" + r.Destination + "\x00" + r.Protocol + "\x00" + r.DestinationPort + "\x00" + r.ICMPType
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, r)
	}
	return out
}

// cidrCoveredByAny reports whether any CIDR string in set covers (contains)
// the given network.
func cidrCoveredByAny(set map[string]bool, cidr string) bool {
	inner, err := ParseCIDR(cidr)
	if err != nil {
		return false
	}
	for s := range set {
		outer, err := ParseCIDR(s)
		if err != nil {
			continue
		}
		if covers(outer, inner) {
			return true
		}
	}
	return false
}

// ruleLess orders rules by (direction, action, source, destination, protocol, destination_port, icmp_type).
func ruleLess(a, b Rule) bool {
	if a.Direction != b.Direction {
		return a.Direction < b.Direction
	}
	if a.Action != b.Action {
		return a.Action < b.Action
	}
	if a.Source != b.Source {
		return a.Source < b.Source
	}
	if a.Destination != b.Destination {
		return a.Destination < b.Destination
	}
	if a.Protocol != b.Protocol {
		return a.Protocol < b.Protocol
	}
	if a.DestinationPort != b.DestinationPort {
		return a.DestinationPort < b.DestinationPort
	}
	return a.ICMPType < b.ICMPType
}

// ACLToAPIRules converts compiled rules into network ACL rule payloads,
// partitioned into ingress and egress lists.
func ACLToAPIRules(acl *CompiledACL) (ingress, egress []provider.NetworkACLRule) {
	for _, r := range acl.Rules {
		rule := provider.NetworkACLRule{
			Action:          r.Action,
			Source:          r.Source,
			Destination:     r.Destination,
			Protocol:        r.Protocol,
			DestinationPort: r.DestinationPort,
			ICMPType:        r.ICMPType,
			State:           "enabled",
		}
		if r.Direction == "ingress" {
			ingress = append(ingress, rule)
		} else {
			egress = append(egress, rule)
		}
	}
	return ingress, egress
}

// RulesEqual reports whether two ACL rule lists are semantically identical
// (order-independent).
func RulesEqual(a, b []provider.NetworkACLRule) bool {
	if len(a) != len(b) {
		return false
	}
	key := func(r provider.NetworkACLRule) string {
		return r.Action + "\x00" + r.Source + "\x00" + r.Destination + "\x00" + r.Protocol + "\x00" + r.DestinationPort + "\x00" + r.ICMPType + "\x00" + r.State
	}
	sa := make([]string, len(a))
	sb := make([]string, len(b))
	for i := range a {
		sa[i] = key(a[i])
		sb[i] = key(b[i])
	}
	sort.Strings(sa)
	sort.Strings(sb)
	for i := range sa {
		if sa[i] != sb[i] {
			return false
		}
	}
	return true
}

func isIPv4CIDR(cidr string) bool {
	return strings.Count(cidr, ":") == 0
}

// RuleCount returns the number of rules in a compiled ACL (for the >256 guard).
func RuleCount(acl *CompiledACL) int {
	return len(acl.Rules)
}

// RejectRuleCount returns the number of reject rules in a compiled ACL (the
// §3.2 guard is defined over the compiled reject set).
func RejectRuleCount(acl *CompiledACL) int {
	n := 0
	for _, r := range acl.Rules {
		if r.Action == "reject" {
			n++
		}
	}
	return n
}
