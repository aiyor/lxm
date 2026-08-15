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
