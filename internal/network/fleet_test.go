package network

import (
	"strings"
	"testing"

	"github.com/aiyor/lxm/internal/config"
)

func boolPtr(b bool) *bool { return &b }

func TestUnion_BaseInheritance_Dedup(t *testing.T) {
	// Two leaves include one base; the fleet union must yield exactly one
	// vswitch and one allow entry (§7.2 worked example).
	base := &config.Config{
		Schema:     "lxm/config/v2",
		Base:       true,
		ConfigFile: "/tmp/fleet/_base.yaml",
		VSwitches: []config.VSwitchConfig{
			{Name: "vmbr0", IPv4: "10.30.0.1/24", Group: "vms"},
			{Name: "svcbr0", IPv4: "10.50.0.1/24", Group: "services"},
		},
		NetworkPolicy: &config.NetworkPolicy{
			Allow: []config.NetworkPolicyRule{{From: "vms", To: "services"}},
		},
	}
	leafA := &config.Config{Name: "a", ConfigFile: "/tmp/fleet/a.yaml"}
	leafB := &config.Config{Name: "b", ConfigFile: "/tmp/fleet/b.yaml"}

	f, err := Union([]*config.Config{leafA, leafB, base})
	if err != nil {
		t.Fatalf("Union: %v", err)
	}
	if len(f.VSwitches) != 2 {
		t.Fatalf("expected 2 vswitches after dedup, got %d", len(f.VSwitches))
	}
	if len(f.Allow) != 1 {
		t.Fatalf("expected 1 allow after dedup, got %d", len(f.Allow))
	}
}

func TestUnion_BaseLeafConflict(t *testing.T) {
	base := &config.Config{
		Schema:     "lxm/config/v2",
		ConfigFile: "/tmp/fleet/_base.yaml",
		VSwitches:  []config.VSwitchConfig{{Name: "vmbr0", IPv4: "10.30.0.1/24", Group: "vms"}},
	}
	leaf := &config.Config{
		Schema:     "lxm/config/v2",
		ConfigFile: "/tmp/fleet/a.yaml",
		VSwitches:  []config.VSwitchConfig{{Name: "vmbr0", IPv4: "10.99.0.1/24", Group: "vms"}},
	}
	_, err := Union([]*config.Config{base, leaf})
	if err == nil || !strings.Contains(err.Error(), "_base.yaml") || !strings.Contains(err.Error(), "a.yaml") {
		t.Fatalf("expected base-vs-leaf conflict citing both files, got: %v", err)
	}
}

func TestUnion_ConflictingPolicyDirection(t *testing.T) {
	a := &config.Config{
		Schema:     "lxm/config/v2",
		ConfigFile: "/tmp/fleet/a.yaml",
		VSwitches:  []config.VSwitchConfig{{Name: "vmbr0", IPv4: "10.30.0.1/24", Group: "vms"}, {Name: "svcbr0", IPv4: "10.50.0.1/24", Group: "services"}},
		NetworkPolicy: &config.NetworkPolicy{
			Allow: []config.NetworkPolicyRule{{From: "vms", To: "services", Direction: "both"}},
		},
	}
	b := &config.Config{
		Schema:     "lxm/config/v2",
		ConfigFile: "/tmp/fleet/b.yaml",
		VSwitches:  []config.VSwitchConfig{{Name: "vmbr0", IPv4: "10.30.0.1/24", Group: "vms"}, {Name: "svcbr0", IPv4: "10.50.0.1/24", Group: "services"}},
		NetworkPolicy: &config.NetworkPolicy{
			Allow: []config.NetworkPolicyRule{{From: "vms", To: "services", Direction: "egress"}},
		},
	}
	_, err := Union([]*config.Config{a, b})
	if err == nil || !strings.Contains(err.Error(), "conflicting declarations") {
		t.Fatalf("expected conflicting-policy error, got: %v", err)
	}
}

func TestUnion_UnknownGroup(t *testing.T) {
	b := &config.Config{
		Schema: "lxm/config/v2",
		VSwitches: []config.VSwitchConfig{
			{Name: "vmbr0", IPv4: "10.30.0.1/24", Group: "vms"},
		},
		NetworkPolicy: &config.NetworkPolicy{
			Allow: []config.NetworkPolicyRule{{From: "vms", To: "ghosts"}},
		},
	}
	_, err := Union([]*config.Config{b})
	if err == nil || !strings.Contains(err.Error(), "group \"ghosts\" has no vswitches assigned") {
		t.Fatalf("expected unknown-group error, got: %v", err)
	}
}

func TestUnion_SubnetOverlap(t *testing.T) {
	b := &config.Config{
		Schema: "lxm/config/v2",
		VSwitches: []config.VSwitchConfig{
			{Name: "br0", IPv4: "10.30.0.1/24", Group: "a"},
			{Name: "br1", IPv4: "10.30.0.1/23", Group: "b"},
		},
	}
	_, err := Union([]*config.Config{b})
	if err == nil || !strings.Contains(err.Error(), "overlaps") {
		t.Fatalf("expected subnet overlap error, got: %v", err)
	}
}

func TestUnion_OperatorInternalCIDRsMerged(t *testing.T) {
	b := &config.Config{
		Schema: "lxm/config/v2",
		VSwitches: []config.VSwitchConfig{
			{Name: "labbr0", IPv4: "10.60.0.1/24", Group: "quarantine"},
		},
		NetworkPolicy: &config.NetworkPolicy{
			InternalCIDRs: []string{"192.168.77.0/24"},
		},
	}
	f, err := Union([]*config.Config{b})
	if err != nil {
		t.Fatalf("Union: %v", err)
	}
	// 192.168.77.0/24 is subsumed by the default 192.168.0.0/16 (canonicalization).
	found := false
	for _, c := range f.InternalCIDRs {
		if c == "192.168.0.0/16" {
			found = true
		}
	}
	if !found {
		t.Fatalf("default 192.168.0.0/16 missing from internal set: %v", f.InternalCIDRs)
	}
}

func TestUnion_FromToSame_Warning(t *testing.T) {
	b := &config.Config{
		Schema: "lxm/config/v2",
		VSwitches: []config.VSwitchConfig{
			{Name: "br0", IPv4: "10.30.0.1/24", Group: "a"},
			{Name: "br1", IPv4: "10.31.0.1/24", Group: "a"},
		},
		NetworkPolicy: &config.NetworkPolicy{
			Allow: []config.NetworkPolicyRule{{From: "a", To: "a"}},
		},
	}
	f, err := Union([]*config.Config{b})
	if err != nil {
		t.Fatalf("Union: %v", err)
	}
	if len(f.Warnings) == 0 || !strings.Contains(f.Warnings[0], "no-op") {
		t.Fatalf("expected no-op warning, got warnings: %v", f.Warnings)
	}
}

func TestUnion_NatFalseInternetTrue_Warning(t *testing.T) {
	b := &config.Config{
		Schema: "lxm/config/v2",
		VSwitches: []config.VSwitchConfig{
			{Name: "br0", IPv4: "10.30.0.1/24", Group: "a", NAT: boolPtr(false)},
		},
	}
	f, err := Union([]*config.Config{b})
	if err != nil {
		t.Fatalf("Union: %v", err)
	}
	if len(f.Warnings) == 0 || !strings.Contains(f.Warnings[0], "nat: false") {
		t.Fatalf("expected nat/internet warning, got warnings: %v", f.Warnings)
	}
}
