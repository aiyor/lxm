package config_test

import (
	"os"
	"strings"
	"testing"

	"github.com/aiyor/lxm/internal/config"
)

func boolPtr(b bool) *bool { return &b }

func TestValidateVSwitch_Valid(t *testing.T) {
	c := &config.Config{
		Name:  "box1",
		Image: "ubuntu:24.04",
		VSwitches: []config.VSwitchConfig{
			{Name: "vmbr0", IPv4: "10.30.0.1/24", Group: "vms"},
		},
	}
	if err := c.Validate(""); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestValidateVSwitch_NotFirstUsableHost(t *testing.T) {
	// 10.10.50.1/16 matches the old ".1 suffix" regex but is not the first
	// usable host of 10.10.0.0/16 (§2.1 review finding).
	c := &config.Config{
		Name:  "box1",
		Image: "ubuntu:24.04",
		VSwitches: []config.VSwitchConfig{
			{Name: "vmbr0", IPv4: "10.10.50.1/16", Group: "vms"},
		},
	}
	err := c.Validate("")
	if err == nil || !strings.Contains(err.Error(), "first usable host") {
		t.Fatalf("expected first-usable-host error, got: %v", err)
	}
}

func TestValidateVSwitch_MaskBounds(t *testing.T) {
	for _, bad := range []string{"10.0.0.1/7", "10.0.0.1/30", "10.0.0.1/32"} {
		c := &config.Config{Name: "box1", VSwitches: []config.VSwitchConfig{{Name: "br", IPv4: bad, Group: "g"}}}
		if err := c.Validate(""); err == nil {
			t.Fatalf("expected mask-bound error for %s", bad)
		}
	}
	// /8 and /29 are accepted.
	for _, ok := range []string{"10.0.0.1/8", "10.0.0.1/29"} {
		c := &config.Config{Name: "box1", Image: "ubuntu:24.04", VSwitches: []config.VSwitchConfig{{Name: "br", IPv4: ok, Group: "g"}}}
		if err := c.Validate(""); err != nil {
			t.Fatalf("unexpected error for %s: %v", ok, err)
		}
	}
}

func TestValidateVSwitch_DuplicateName(t *testing.T) {
	c := &config.Config{
		Name:  "box1",
		Image: "ubuntu:24.04",
		VSwitches: []config.VSwitchConfig{
			{Name: "vmbr0", IPv4: "10.30.0.1/24"},
			{Name: "vmbr0", IPv4: "10.31.0.1/24"},
		},
	}
	if err := c.Validate(""); err == nil || !strings.Contains(err.Error(), "duplicate name") {
		t.Fatalf("expected duplicate-name error, got: %v", err)
	}
}

func TestValidateVSwitch_SubnetOverlap(t *testing.T) {
	c := &config.Config{
		Name:  "box1",
		Image: "ubuntu:24.04",
		VSwitches: []config.VSwitchConfig{
			{Name: "br0", IPv4: "10.30.0.1/24"},
			{Name: "br1", IPv4: "10.30.0.1/23"},
		},
	}
	if err := c.Validate(""); err == nil || !strings.Contains(err.Error(), "overlaps") {
		t.Fatalf("expected overlap error, got: %v", err)
	}
}

func TestValidateVSwitch_InternetWithoutGroup(t *testing.T) {
	c := &config.Config{
		Name:  "box1",
		Image: "ubuntu:24.04",
		VSwitches: []config.VSwitchConfig{
			{Name: "br0", IPv4: "10.30.0.1/24", Internet: boolPtr(false)},
		},
	}
	if err := c.Validate(""); err == nil || !strings.Contains(err.Error(), "requires a group") {
		t.Fatalf("expected internet-without-group error, got: %v", err)
	}
}

func TestValidateVSwitch_NATFalse_Allowed(t *testing.T) {
	// Regression for the code review finding: nat: false is a documented,
	// CUE-valid configuration (§2.1) and must not be rejected.
	c := &config.Config{
		Name:  "box1",
		Image: "ubuntu:24.04",
		VSwitches: []config.VSwitchConfig{
			{Name: "vmbr0", IPv4: "10.30.0.1/24", Group: "vms", NAT: boolPtr(false)},
		},
	}
	if err := c.Validate(""); err != nil {
		t.Fatalf("unexpected error for nat: false: %v", err)
	}
}

func TestValidateVSwitch_NATTrueAndNil_Allowed(t *testing.T) {
	for _, nat := range []*bool{boolPtr(true), nil} {
		c := &config.Config{
			Name:  "box1",
			Image: "ubuntu:24.04",
			VSwitches: []config.VSwitchConfig{
				{Name: "vmbr0", IPv4: "10.30.0.1/24", Group: "vms", NAT: nat},
			},
		}
		if err := c.Validate(""); err != nil {
			t.Fatalf("unexpected error for nat=%v: %v", nat, err)
		}
	}
}

func TestValidateNetworkPolicy_InvalidInternalCIDR(t *testing.T) {
	c := &config.Config{
		Name:  "box1",
		Image: "ubuntu:24.04",
		NetworkPolicy: &config.NetworkPolicy{
			InternalCIDRs: []string{"not-a-cidr"},
		},
	}
	if err := c.Validate(""); err == nil || !strings.Contains(err.Error(), "invalid CIDR") {
		t.Fatalf("expected invalid-CIDR error, got: %v", err)
	}
}

func TestValidateVSwitch_IPv6Rejected(t *testing.T) {
	c := &config.Config{
		Name:  "box1",
		Image: "ubuntu:24.04",
		VSwitches: []config.VSwitchConfig{
			{Name: "br0", IPv4: "2001:db8::1/64", Group: "g"},
		},
	}
	if err := c.Validate(""); err == nil {
		t.Fatalf("expected IPv6 rejection (v1 lock)")
	}
}

func TestLoadConfig_VSwitchDefaultsNormalized(t *testing.T) {
	dir := t.TempDir()
	base := `schema: lxm/config/v2
base: true
vswitches:
  - name: vmbr0
    ipv4: 10.30.0.1/24
    group: vms
`
	leaf := `schema: lxm/config/v2
include: [_base.yaml]
name: box1
`
	if err := writeFile(dir+"/_base.yaml", base); err != nil {
		t.Fatal(err)
	}
	if err := writeFile(dir+"/leaf.yaml", leaf); err != nil {
		t.Fatal(err)
	}

	conf, err := config.LoadConfig(dir + "/leaf.yaml")
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	if len(conf.VSwitches) != 1 {
		t.Fatalf("expected 1 vswitch, got %d", len(conf.VSwitches))
	}
	vs := conf.VSwitches[0]
	if vs.Type != "bridge" {
		t.Errorf("expected default type bridge, got %q", vs.Type)
	}
	if vs.Driver != "native" {
		t.Errorf("expected default driver native, got %q", vs.Driver)
	}
	if vs.IPv6 != "none" {
		t.Errorf("expected default ipv6 none, got %q", vs.IPv6)
	}
	if vs.NAT == nil || !*vs.NAT {
		t.Errorf("expected default nat true")
	}
	if vs.Internet == nil || !*vs.Internet {
		t.Errorf("expected default internet true")
	}
	if vs.IPv4 != "10.30.0.1/24" {
		t.Errorf("expected ipv4 preserved as gateway form, got %q", vs.IPv4)
	}
}

func writeFile(path, content string) error {
	return os.WriteFile(path, []byte(content), 0644)
}
