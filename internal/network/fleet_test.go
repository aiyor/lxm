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

func TestUnion_AbsentVSwitch_NoPanicOrInvalidCIDR(t *testing.T) {
	b := &config.Config{
		Schema: "lxm/config/v2",
		VSwitches: []config.VSwitchConfig{
			{Name: "br0", IPv4: "10.30.0.1/24", Group: "a"},
			{Name: "br-old", Status: "absent"},
		},
	}
	f, err := Union([]*config.Config{b})
	if err != nil {
		t.Fatalf("expected Union to succeed with absent vswitch, got error: %v", err)
	}
	if len(f.VSwitches) != 2 {
		t.Fatalf("expected 2 vswitches in fleet, got %d", len(f.VSwitches))
	}
	if vs, ok := f.ByName["br-old"]; !ok || vs.Status != "absent" {
		t.Fatalf("expected br-old to be present in fleet with status=absent")
	}
}

func TestCheckInstancesWithProvider_IncusDefaultBridge(t *testing.T) {
	conf := &config.Config{
		Name: "web",
		Networks: []config.NetworkConfig{
			{Name: "eth0"}, // no parent specified, no provider on config
		},
	}
	f := &Fleet{ByName: map[string]*VSwitch{}}
	liveNets := map[string]bool{"incusbr0": true}

	// When provider is incus, default parent is incusbr0 -> present in liveNets -> 0 warnings
	warns, err := CheckInstancesWithProvider([]*config.Config{conf}, f, liveNets, "incus")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(warns) != 0 {
		t.Errorf("expected 0 warnings on incus, got: %v", warns)
	}

	// When provider is not incus, default parent is lxdbr0 -> missing from liveNets -> warning
	warnsLXD, err := CheckInstancesWithProvider([]*config.Config{conf}, f, liveNets, "lxd")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(warnsLXD) == 0 || !strings.Contains(warnsLXD[0], "lxdbr0") {
		t.Errorf("expected warning mentioning lxdbr0 on lxd, got: %v", warnsLXD)
	}
}

func TestCheckInstancesWithProvider_NATOnOVNRejected(t *testing.T) {
	conf := &config.Config{
		Name: "vm1",
		Type: "virtual-machine",
		Networks: []config.NetworkConfig{
			{Name: "eth0", Parent: "ovn0", IPv4: "10.10.10.31"},
		},
		Ports: []config.PortConfig{{
			Name:    "https",
			Listen:  "10.10.10.1:8443",
			Connect: "10.10.10.31:443",
			Proto:   "tcp",
			NAT:     true,
		}},
	}
	f := &Fleet{ByName: map[string]*VSwitch{
		"ovn0": {VSwitchConfig: config.VSwitchConfig{Name: "ovn0", Type: "ovn"}},
	}}
	if _, err := CheckInstancesWithProvider([]*config.Config{conf}, f, nil, "lxd"); err == nil || !strings.Contains(err.Error(), "OVN-backed network") {
		t.Fatalf("expected NAT+OVN error, got: %v", err)
	}

	// A bridge-backed vswitch with the same NAT port is allowed by this layer
	// (config.validatePorts validates the static-IPv4 pairing separately).
	f.ByName["ovn0"] = &VSwitch{VSwitchConfig: config.VSwitchConfig{Name: "ovn0", Type: "bridge"}}
	if _, err := CheckInstancesWithProvider([]*config.Config{conf}, f, nil, "lxd"); err != nil {
		t.Fatalf("bridge-backed NAT must not be rejected by the network layer: %v", err)
	}
}

func TestCheckInstancesWithProvider_NATFollowsConnectNIC(t *testing.T) {
	// A multi-NIC instance may have an OVN NIC while its NAT connect address
	// lives on a bridged NIC; the daemon selects the NIC matching connect, so
	// the bridged case must be accepted and the OVN case rejected.
	mkConf := func(connect string) *config.Config {
		return &config.Config{
			Name: "vm1",
			Type: "virtual-machine",
			Networks: []config.NetworkConfig{
				{Name: "eth0", Parent: "br0", IPv4: "10.10.10.31"},
				{Name: "eth1", Parent: "ovn0", IPv4: "10.60.0.50"},
			},
			Ports: []config.PortConfig{{
				Name:    "https",
				Listen:  "10.10.10.1:8443",
				Connect: connect,
				Proto:   "tcp",
				NAT:     true,
			}},
		}
	}
	f := &Fleet{ByName: map[string]*VSwitch{
		"br0":  {VSwitchConfig: config.VSwitchConfig{Name: "br0", Type: "bridge"}},
		"ovn0": {VSwitchConfig: config.VSwitchConfig{Name: "ovn0", Type: "ovn"}},
	}}
	if _, err := CheckInstancesWithProvider([]*config.Config{mkConf("10.10.10.31:443")}, f, nil, "lxd"); err != nil {
		t.Fatalf("NAT on the bridged NIC must be accepted despite an OVN NIC: %v", err)
	}
	if _, err := CheckInstancesWithProvider([]*config.Config{mkConf("10.60.0.50:443")}, f, nil, "lxd"); err == nil {
		t.Fatal("NAT on the OVN-backed NIC must be rejected")
	}
}

func TestCheckInstancesWithProviderAndNetworkTypes_NATOnLiveOVN(t *testing.T) {
	conf := &config.Config{
		Name: "vm1",
		Type: "virtual-machine",
		Networks: []config.NetworkConfig{
			{Name: "eth0", Parent: "live-ovn", IPv4: "10.70.0.5"},
		},
		Ports: []config.PortConfig{{
			Name:    "https",
			Listen:  "10.70.0.1:8443",
			Connect: "10.70.0.5:443",
			Proto:   "tcp",
			NAT:     true,
		}},
	}
	f := &Fleet{ByName: map[string]*VSwitch{}}

	// The OVN parent is not declared in vswitches:, so only the live network
	// type can reveal the incompatibility.
	if _, err := CheckInstancesWithProvider([]*config.Config{conf}, f, map[string]bool{"live-ovn": true}, "lxd"); err != nil {
		t.Fatalf("without live types the declared-only layer cannot detect it: %v", err)
	}
	if _, err := CheckInstancesWithProviderAndNetworkTypes(
		[]*config.Config{conf}, f, map[string]bool{"live-ovn": true}, map[string]string{"live-ovn": "ovn"}, "lxd",
	); err == nil || !strings.Contains(err.Error(), "OVN-backed network") {
		t.Fatalf("expected live-OVN NAT rejection, got: %v", err)
	}
	if _, err := CheckInstancesWithProviderAndNetworkTypes(
		[]*config.Config{conf}, f, map[string]bool{"live-ovn": true}, map[string]string{"live-ovn": "bridge"}, "lxd",
	); err != nil {
		t.Fatalf("bridged live network must be accepted: %v", err)
	}
}
