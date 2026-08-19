package network

import (
	"net"
	"reflect"
	"strings"
	"testing"

	"github.com/aiyor/lxm/internal/config"
)

// simulationFleet mirrors the simulation environment's inner-fleet fixtures
// (§3.3 worked example): vms/containers/services/quarantine groups with
// vms→services mutual and containers→services one-way.
func simulationFleet(t *testing.T, tweaks ...func(*config.Config)) *Fleet {
	t.Helper()
	boolPtr := func(b bool) *bool { return &b }
	base := &config.Config{
		Schema: "lxm/config/v2",
		Base:   true,
		VSwitches: []config.VSwitchConfig{
			{Name: "vmbr0", IPv4: "10.30.0.1/24", Group: "vms"},
			{Name: "vmbr1", IPv4: "10.31.0.1/24", Group: "vms"},
			{Name: "cbr0", IPv4: "10.40.0.1/24", Group: "containers"},
			{Name: "svcbr0", IPv4: "10.50.0.1/24", Group: "services"},
			{Name: "labbr0", IPv4: "10.60.0.1/24", Group: "quarantine"},
		},
		NetworkPolicy: &config.NetworkPolicy{
			Allow: []config.NetworkPolicyRule{
				{From: "vms", To: "services", Direction: "both"},
				{From: "containers", To: "services", Direction: "egress"},
			},
		},
	}
	_ = boolPtr
	f, err := Union([]*config.Config{base})
	if err != nil {
		t.Fatalf("Union: %v", err)
	}
	return f
}

func aclRules(t *testing.T, f *Fleet, vswitch string) []Rule {
	t.Helper()
	for _, acl := range Compile(f) {
		if acl.Name == "lxm-"+vswitch {
			return acl.Rules
		}
	}
	t.Fatalf("no ACL for vswitch %q", vswitch)
	return nil
}

func ruleDestinations(rules []Rule, direction, action string) []string {
	var out []string
	for _, r := range rules {
		if r.Direction == direction && r.Action == action {
			out = append(out, r.Destination)
		}
	}
	return out
}

func TestCompile_Quarantine_Golden(t *testing.T) {
	f := simulationFleet(t)
	rules := aclRules(t, f, "labbr0")

	// Exactly the §3.3 quarantine rule set.
	want := []string{
		"egress allow 10.60.0.0/24 -> 0.0.0.0/0",
		"egress reject 10.60.0.0/24 -> 10.0.0.0/8",
		"egress reject 10.60.0.0/24 -> 100.64.0.0/10",
		"egress reject 10.60.0.0/24 -> 127.0.0.0/8",
		"egress reject 10.60.0.0/24 -> 169.254.0.0/16",
		"egress reject 10.60.0.0/24 -> 172.16.0.0/12",
		"egress reject 10.60.0.0/24 -> 192.168.0.0/16",
	}
	var got []string
	for _, r := range rules {
		got = append(got, r.Direction+" "+r.Action+" "+r.Source+" -> "+r.Destination)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("quarantine ACL mismatch:\ngot  %v\nwant %v", got, want)
	}
}

func TestCompile_RejectSetDoesNotOverlapPermittedEgress(t *testing.T) {
	// Compliance requirement (§3.2): the emitted reject set contains no CIDR
	// overlapping any G1/G3/G4 destination.
	f := simulationFleet(t)
	for _, acl := range Compile(f) {
		vs := f.ByName[strings.TrimPrefix(acl.Name, "lxm-")]
		permitted := PermittedEgress(f, vs)
		permNets := make([]*net.IPNet, 0, len(permitted))
		for _, p := range permitted {
			permNets = append(permNets, mustParse(t, p))
		}
		for _, r := range acl.Rules {
			if r.Action != "reject" {
				continue
			}
			dst := mustParse(t, r.Destination)
			for _, p := range permNets {
				if overlaps(dst, p) {
					t.Fatalf("ACL %s: reject %s overlaps permitted egress %s", acl.Name, r.Destination, p.String())
				}
			}
		}
	}
}

func TestCompile_OwnSubnetAlwaysRejected(t *testing.T) {
	// A vswitch's own subnet is always a reject subject (§3.2 host protection),
	// even when it is not RFC1918.
	b := &config.Config{
		Schema: "lxm/config/v2",
		VSwitches: []config.VSwitchConfig{
			{Name: "pub0", IPv4: "198.51.100.1/24", Group: "pub"},
		},
	}
	f, err := Union([]*config.Config{b})
	if err != nil {
		t.Fatalf("Union: %v", err)
	}
	rules := aclRules(t, f, "pub0")
	found := false
	for _, r := range rules {
		if r.Action == "reject" && r.Destination == "198.51.100.0/24" {
			found = true
		}
	}
	if !found {
		t.Fatalf("own subnet not in reject set: %v", ruleDestinations(rules, "egress", "reject"))
	}
}

func TestCompile_InternetFalse_NoWildcardNoRejects(t *testing.T) {
	boolPtr := func(b bool) *bool { return &b }
	b := &config.Config{
		Schema: "lxm/config/v2",
		VSwitches: []config.VSwitchConfig{
			{Name: "int0", IPv4: "10.20.0.1/24", Group: "internal", Internet: boolPtr(false)},
		},
	}
	f, err := Union([]*config.Config{b})
	if err != nil {
		t.Fatalf("Union: %v", err)
	}
	rules := aclRules(t, f, "int0")
	for _, r := range rules {
		if r.Action == "allow" && r.Destination == "0.0.0.0/0" {
			t.Fatalf("internet:false must not emit wildcard egress allow")
		}
		if r.Action == "reject" {
			t.Fatalf("internet:false must not emit explicit rejects (default reject covers): %+v", r)
		}
	}
}

func TestCompile_Ungrouped_NoACL(t *testing.T) {
	b := &config.Config{
		Schema: "lxm/config/v2",
		VSwitches: []config.VSwitchConfig{
			{Name: "open0", IPv4: "10.20.0.1/24"},
		},
	}
	f, err := Union([]*config.Config{b})
	if err != nil {
		t.Fatalf("Union: %v", err)
	}
	acls := Compile(f)
	if len(acls) != 0 {
		t.Fatalf("ungrouped vswitch must produce no ACL, got %d", len(acls))
	}
}

func TestCompile_Deterministic(t *testing.T) {
	f1 := simulationFleet(t)
	f2 := simulationFleet(t)
	a1 := Compile(f1)
	a2 := Compile(f2)
	if len(a1) != len(a2) {
		t.Fatalf("compiled ACL count differs across runs")
	}
	for i := range a1 {
		if a1[i].Name != a2[i].Name {
			t.Fatalf("ACL order differs")
		}
		if !reflect.DeepEqual(a1[i].Rules, a2[i].Rules) {
			t.Fatalf("ACL %s rules differ across runs", a1[i].Name)
		}
	}
}

func TestCompile_OneWayPolicy_NoReciprocalEgress(t *testing.T) {
	// R5: containers→services egress; services must NOT get egress-allow back
	// toward containers.
	f := simulationFleet(t)
	cbr0 := aclRules(t, f, "cbr0")
	svcbr0 := aclRules(t, f, "svcbr0")

	if !containsRule(cbr0, "egress", "allow", "10.40.0.0/24", "10.50.0.0/24") {
		t.Fatalf("cbr0 missing egress allow to services: %v", cbr0)
	}
	if !containsRule(svcbr0, "ingress", "allow", "10.40.0.0/24", "10.50.0.0/24") {
		t.Fatalf("svcbr0 missing ingress allow from containers: %v", svcbr0)
	}
	if containsRule(svcbr0, "egress", "allow", "10.50.0.0/24", "10.40.0.0/24") {
		t.Fatalf("svcbr0 must NOT have reciprocal egress toward containers: %v", svcbr0)
	}
}

func TestCompile_CIDROnlySubjects(t *testing.T) {
	f := simulationFleet(t)
	for _, acl := range Compile(f) {
		for _, r := range acl.Rules {
			if strings.HasPrefix(r.Source, "@") || strings.HasPrefix(r.Destination, "@") {
				t.Fatalf("ACL %s: selector subject emitted (OVN-only): %+v", acl.Name, r)
			}
		}
	}
}

func TestCompile_MutualPolicy_Mirrored(t *testing.T) {
	// R4: vms⇄services both — vmbr0 egress+ingress to services; svcbr0
	// egress+ingress to vms.
	f := simulationFleet(t)
	vmbr0 := aclRules(t, f, "vmbr0")
	svcbr0 := aclRules(t, f, "svcbr0")

	if !containsRule(vmbr0, "egress", "allow", "10.30.0.0/24", "10.50.0.0/24") {
		t.Fatalf("vmbr0 missing egress to services")
	}
	if !containsRule(vmbr0, "ingress", "allow", "10.50.0.0/24", "10.30.0.0/24") {
		t.Fatalf("vmbr0 missing ingress from services")
	}
	if !containsRule(svcbr0, "egress", "allow", "10.50.0.0/24", "10.30.0.0/24") {
		t.Fatalf("svcbr0 missing egress to vms")
	}
	if !containsRule(svcbr0, "ingress", "allow", "10.30.0.0/24", "10.50.0.0/24") {
		t.Fatalf("svcbr0 missing ingress from vms")
	}
}

func containsRule(rules []Rule, dir, action, src, dst string) bool {
	for _, r := range rules {
		if r.Direction == dir && r.Action == action && r.Source == src && r.Destination == dst {
			return true
		}
	}
	return false
}

func TestCompile_OVN_SingleSwitch_EmitsG0AndNoOwnSubnetReject(t *testing.T) {
	b := &config.Config{
		Schema: "lxm/config/v2",
		VSwitches: []config.VSwitchConfig{
			{
				Name:   "ovnbr0",
				Type:   "ovn",
				Parent: "lxdbr0",
				IPv4:   "10.60.0.1/24",
				Group:  "ovngroup",
			},
		},
	}
	f, err := Union([]*config.Config{b})
	if err != nil {
		t.Fatalf("Union: %v", err)
	}
	rules := aclRules(t, f, "ovnbr0")

	// G0 intra-switch must be present
	if !containsRule(rules, "egress", "allow", "10.60.0.0/24", "10.60.0.0/24") {
		t.Fatalf("OVN switch missing G0 egress allow for own subnet: %v", rules)
	}
	if !containsRule(rules, "ingress", "allow", "10.60.0.0/24", "10.60.0.0/24") {
		t.Fatalf("OVN switch missing G0 ingress allow for own subnet: %v", rules)
	}

	// G7 internet egress must be present
	if !containsRule(rules, "egress", "allow", "10.60.0.0/24", "0.0.0.0/0") {
		t.Fatalf("OVN switch missing G7 wildcard allow: %v", rules)
	}

	// Own subnet 10.60.0.0/24 MUST NOT be in any reject rule (or overlapped by any reject rule)
	ownNet := mustParse(t, "10.60.0.0/24")
	for _, r := range rules {
		if r.Action == "reject" {
			dst := mustParse(t, r.Destination)
			if dst.String() == "10.60.0.0/24" || overlaps(dst, ownNet) {
				t.Fatalf("OVN switch must NOT reject own subnet (got reject %s)", r.Destination)
			}
		}
	}
}

func TestCompile_OVN_IntraSwitchAllowedInIsolated(t *testing.T) {
	boolFalse := false
	b := &config.Config{
		Schema: "lxm/config/v2",
		VSwitches: []config.VSwitchConfig{
			{
				Name:     "ovnbr0",
				Type:     "ovn",
				Parent:   "lxdbr0",
				IPv4:     "10.60.0.1/24",
				Group:    "isolated",
				Internet: &boolFalse,
			},
		},
	}
	f, err := Union([]*config.Config{b})
	if err != nil {
		t.Fatalf("Union: %v", err)
	}
	rules := aclRules(t, f, "ovnbr0")

	// G0 intra-switch MUST still be present
	if !containsRule(rules, "egress", "allow", "10.60.0.0/24", "10.60.0.0/24") {
		t.Fatalf("isolated OVN switch missing G0 egress allow: %v", rules)
	}
	if !containsRule(rules, "ingress", "allow", "10.60.0.0/24", "10.60.0.0/24") {
		t.Fatalf("isolated OVN switch missing G0 ingress allow: %v", rules)
	}

	// Zero G7 or G8 rules
	for _, r := range rules {
		if r.Destination == "0.0.0.0/0" || r.Action == "reject" {
			t.Fatalf("isolated switch must not have G7/G8 rules: %+v", r)
		}
	}
}

func TestCompile_HybridFleet_BridgeAndOVN(t *testing.T) {
	b := &config.Config{
		Schema: "lxm/config/v2",
		VSwitches: []config.VSwitchConfig{
			{
				Name:  "vmbr0",
				Type:  "bridge",
				IPv4:  "10.30.0.1/24",
				Group: "vms",
			},
			{
				Name:   "ovnbr0",
				Type:   "ovn",
				Parent: "lxdbr0",
				IPv4:   "10.60.0.1/24",
				Group:  "services",
			},
		},
		NetworkPolicy: &config.NetworkPolicy{
			Allow: []config.NetworkPolicyRule{
				{From: "vms", To: "services", Direction: "both"},
			},
		},
	}
	f, err := Union([]*config.Config{b})
	if err != nil {
		t.Fatalf("Union: %v", err)
	}

	vmRules := aclRules(t, f, "vmbr0")
	ovnRules := aclRules(t, f, "ovnbr0")

	// Bridge (vmbr0) has NO G0
	if containsRule(vmRules, "egress", "allow", "10.30.0.0/24", "10.30.0.0/24") {
		t.Fatalf("bridge switch vmbr0 must not have G0 allow")
	}

	// OVN (ovnbr0) HAS G0
	if !containsRule(ovnRules, "egress", "allow", "10.60.0.0/24", "10.60.0.0/24") {
		t.Fatalf("OVN switch ovnbr0 must have G0 allow")
	}

	// Inter-group mutual allowances exist
	if !containsRule(vmRules, "egress", "allow", "10.30.0.0/24", "10.60.0.0/24") {
		t.Fatalf("vmbr0 missing egress to ovnbr0")
	}
	if !containsRule(ovnRules, "ingress", "allow", "10.30.0.0/24", "10.60.0.0/24") {
		t.Fatalf("ovnbr0 missing ingress from vmbr0")
	}
}

func TestCompile_OVN_RejectSetDoesNotOverlapOwnSubnetOrPermittedEgress(t *testing.T) {
	b := &config.Config{
		Schema: "lxm/config/v2",
		VSwitches: []config.VSwitchConfig{
			{Name: "ovn1", Type: "ovn", Parent: "lxdbr0", IPv4: "10.10.0.1/24", Group: "g1"},
			{Name: "ovn2", Type: "ovn", Parent: "lxdbr0", IPv4: "10.20.0.1/24", Group: "g2"},
			{Name: "ovn3", Type: "ovn", Parent: "lxdbr0", IPv4: "172.16.50.1/24", Group: "g3"},
		},
		NetworkPolicy: &config.NetworkPolicy{
			Allow: []config.NetworkPolicyRule{
				{From: "g1", To: "g2", Direction: "both"},
			},
		},
	}
	f, err := Union([]*config.Config{b})
	if err != nil {
		t.Fatalf("Union: %v", err)
	}

	for _, acl := range Compile(f) {
		vs := f.ByName[strings.TrimPrefix(acl.Name, "lxm-")]
		permitted := PermittedEgress(f, vs)
		carveList := append([]string{}, permitted...)
		if vs.EffectiveType() == "ovn" {
			carveList = append(carveList, vs.Subnet.String())
		}

		carveNets := make([]*net.IPNet, 0, len(carveList))
		for _, c := range carveList {
			carveNets = append(carveNets, mustParse(t, c))
		}

		for _, r := range acl.Rules {
			if r.Action != "reject" {
				continue
			}
			dst := mustParse(t, r.Destination)
			for _, p := range carveNets {
				if overlaps(dst, p) {
					t.Fatalf("ACL %s: reject %s overlaps carved network %s", acl.Name, r.Destination, p.String())
				}
			}
		}
	}
}

func TestCompile_OVN_DNSResolver_CarvedAndPortGuarded(t *testing.T) {
	b := &config.Config{
		Schema: "lxm/config/v2",
		VSwitches: []config.VSwitchConfig{
			{Name: "ovn1", Type: "ovn", Parent: "lxdbr0", IPv4: "10.70.0.1/24", Group: "web"},
		},
	}
	f, err := Union([]*config.Config{b})
	if err != nil {
		t.Fatalf("Union: %v", err)
	}

	// Attach a DNS resolver
	f.VSwitches[0].DNSResolvers = []string{"10.171.13.1/32"}

	acls := Compile(f)
	if len(acls) != 1 {
		t.Fatalf("expected 1 ACL, got %d", len(acls))
	}
	rules := acls[0].Rules

	// 1. Verify port guards exist for 10.171.13.1/32
	hasTCPGuard := false
	hasUDPGuard := false
	hasICMPGuard := false
	for _, r := range rules {
		if r.Destination == "10.171.13.1/32" && r.Action == "reject" {
			if r.Protocol == "tcp" && r.DestinationPort == "1-52,54-65535" {
				hasTCPGuard = true
			}
			if r.Protocol == "udp" && r.DestinationPort == "1-52,54-65535" {
				hasUDPGuard = true
			}
			if r.Protocol == "icmp4" {
				hasICMPGuard = true
			}
		}
	}

	if !hasTCPGuard {
		t.Errorf("missing TCP non-DNS port guard for 10.171.13.1/32")
	}
	if !hasUDPGuard {
		t.Errorf("missing UDP non-DNS port guard for 10.171.13.1/32")
	}
	if !hasICMPGuard {
		t.Errorf("missing ICMP4 guard for 10.171.13.1/32")
	}

	// 2. Verify 10.171.13.1 is not covered by any decomposed supernet reject rule
	targetIP := net.ParseIP("10.171.13.1")
	siblingIP := net.ParseIP("10.171.13.2")
	siblingCovered := false

	for _, r := range rules {
		if r.Action != "reject" || r.Protocol != "" {
			continue // Skip port-specific rules, check general CIDR rejects
		}
		_, netDst, err := net.ParseCIDR(r.Destination)
		if err != nil {
			continue
		}
		if netDst.Contains(targetIP) {
			t.Errorf("general reject rule %s shadows DNS resolver IP 10.171.13.1", r.Destination)
		}
		if netDst.Contains(siblingIP) {
			siblingCovered = true
		}
	}

	if !siblingCovered {
		t.Errorf("sibling host IP 10.171.13.2 was unexpectedly carved out")
	}
}

func TestCompile_OVN_InternetFalse_BlocksDNS(t *testing.T) {
	tr := true
	fls := false
	m := &config.Config{
		Schema: "lxm/config/v2",
		Base:   true,
		VSwitches: []config.VSwitchConfig{
			{Name: "ovn-isolated", Type: "ovn", Parent: "lxdbr0", IPv4: "10.80.0.1/24", Group: "isolated", NAT: &tr, Internet: &fls},
		},
	}

	f, err := Union([]*config.Config{m})
	if err != nil {
		t.Fatalf("Union: %v", err)
	}

	f.VSwitches[0].DNSResolvers = []string{"10.171.13.1/32"}

	acls := Compile(f)
	if len(acls) != 1 {
		t.Fatalf("expected 1 ACL, got %d", len(acls))
	}
	rules := acls[0].Rules

	hasUDP53Reject := false
	hasTCP53Reject := false
	for _, r := range rules {
		if r.Destination == "10.171.13.1/32" && r.Action == "reject" {
			if r.Protocol == "udp" && r.DestinationPort == "53" {
				hasUDP53Reject = true
			}
			if r.Protocol == "tcp" && r.DestinationPort == "53" {
				hasTCP53Reject = true
			}
		}
	}

	if !hasUDP53Reject {
		t.Errorf("missing UDP port 53 reject rule for isolated OVN network")
	}
	if !hasTCP53Reject {
		t.Errorf("missing TCP port 53 reject rule for isolated OVN network")
	}
}
