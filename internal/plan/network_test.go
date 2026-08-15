package plan_test

import (
	"strings"
	"testing"

	"github.com/aiyor/lxm/internal/config"
	"github.com/aiyor/lxm/internal/network"
	"github.com/aiyor/lxm/internal/plan"
	"github.com/canonical/lxd/shared/api"
)

func testFleet(t *testing.T, configs ...*config.Config) *network.Fleet {
	t.Helper()
	f, err := network.Union(configs)
	if err != nil {
		t.Fatalf("Union: %v", err)
	}
	return f
}

func fleetConfigs() []*config.Config {
	base := &config.Config{
		Schema: "lxm/config/v2",
		Base:   true,
		VSwitches: []config.VSwitchConfig{
			{Name: "vmbr0", IPv4: "10.30.0.1/24", Group: "vms"},
			{Name: "svcbr0", IPv4: "10.50.0.1/24", Group: "services"},
		},
		NetworkPolicy: &config.NetworkPolicy{
			Allow: []config.NetworkPolicyRule{{From: "vms", To: "services", Direction: "both"}},
		},
	}
	return []*config.Config{base}
}

func TestComputeNetworks_CreateFromScratch(t *testing.T) {
	f := testFleet(t, fleetConfigs()...)
	rec := plan.NewNetworkReconciler()
	np, err := rec.ComputeNetworks(f, &plan.NetworkLiveState{Networks: map[string]*api.Network{}, ACLs: map[string]*api.NetworkACL{}})
	if err != nil {
		t.Fatalf("ComputeNetworks: %v", err)
	}

	// ACL steps must precede vswitch steps (C8 / §7.4).
	var kinds []string
	for _, s := range np.Steps {
		kinds = append(kinds, s.Kind)
	}
	want := []string{"create_acl", "create_acl", "create_vswitch", "create_vswitch"}
	if strings.Join(kinds, ",") != strings.Join(want, ",") {
		t.Fatalf("step ordering mismatch: got %v want %v", kinds, want)
	}

	// vswitch create payload carries the ACL reference + reject defaults.
	var found bool
	for _, s := range np.Steps {
		if s.Kind == "create_vswitch" && s.Name == "vmbr0" {
			found = true
			if s.NetPost.Config["security.acls"] != "lxm-vmbr0" {
				t.Errorf("vmbr0 missing security.acls: %v", s.NetPost.Config)
			}
			if s.NetPost.Config["security.acls.default.egress.action"] != "reject" {
				t.Errorf("vmbr0 missing egress reject default")
			}
			if s.NetPost.Config["user.lxm.managed"] != "true" {
				t.Errorf("vmbr0 missing lxm managed marker")
			}
			if s.NetPost.Config["ipv4.address"] != "10.30.0.1/24" {
				t.Errorf("vmbr0 ipv4.address wrong: %v", s.NetPost.Config["ipv4.address"])
			}
		}
	}
	if !found {
		t.Fatalf("no create_vswitch for vmbr0: %v", kinds)
	}
}

func TestComputeNetworks_ImmutableDrift_Error(t *testing.T) {
	f := testFleet(t, fleetConfigs()...)
	live := &plan.NetworkLiveState{
		Networks: map[string]*api.Network{
			"vmbr0": {Name: "vmbr0", Config: map[string]string{"user.lxm.managed": "true", "ipv4.address": "10.99.0.1/24", "bridge.driver": "native"}},
		},
		ACLs: map[string]*api.NetworkACL{},
	}
	rec := plan.NewNetworkReconciler()
	_, err := rec.ComputeNetworks(f, live)
	if err == nil || !strings.Contains(err.Error(), "subnet change") {
		t.Fatalf("expected immutable subnet drift error, got: %v", err)
	}
}

func TestComputeNetworks_Adoption_RefusesForeignACL(t *testing.T) {
	f := testFleet(t, fleetConfigs()...)
	live := &plan.NetworkLiveState{
		Networks: map[string]*api.Network{
			// No user.lxm.managed marker -> adoption path; foreign ACL present.
			"vmbr0": {Name: "vmbr0", Config: map[string]string{"ipv4.address": "10.30.0.1/24", "bridge.driver": "native", "security.acls": "handwritten"}},
		},
		ACLs: map[string]*api.NetworkACL{},
	}
	rec := plan.NewNetworkReconciler()
	_, err := rec.ComputeNetworks(f, live)
	if err == nil || !strings.Contains(err.Error(), "foreign security.acls") {
		t.Fatalf("expected foreign-ACL adoption refusal, got: %v", err)
	}
}

func TestComputeNetworks_Update_NATDrift(t *testing.T) {
	f := testFleet(t, fleetConfigs()...)
	live := &plan.NetworkLiveState{
		Networks: map[string]*api.Network{
			"vmbr0": {Name: "vmbr0", Config: map[string]string{"user.lxm.managed": "true", "ipv4.address": "10.30.0.1/24", "bridge.driver": "native", "ipv4.nat": "false"}},
		},
		ACLs: map[string]*api.NetworkACL{},
	}
	rec := plan.NewNetworkReconciler()
	np, err := rec.ComputeNetworks(f, live)
	if err != nil {
		t.Fatalf("ComputeNetworks: %v", err)
	}
	found := false
	for _, s := range np.Steps {
		if s.Kind == "update_vswitch" && s.Name == "vmbr0" {
			found = true
			if s.NetPut.Config["ipv4.nat"] != "true" {
				t.Errorf("expected ipv4.nat reconciled to true, got %v", s.NetPut.Config["ipv4.nat"])
			}
		}
	}
	if !found {
		t.Fatalf("expected update_vswitch for vmbr0")
	}
}

func TestComputeNetworks_GroupRemoval_DetachesACL(t *testing.T) {
	base := &config.Config{
		Schema:    "lxm/config/v2",
		Base:      true,
		VSwitches: []config.VSwitchConfig{{Name: "br0", IPv4: "10.30.0.1/24"}}, // no group
	}
	f := testFleet(t, base)
	live := &plan.NetworkLiveState{
		Networks: map[string]*api.Network{
			"br0": {Name: "br0", Config: map[string]string{
				"user.lxm.managed":                    "true",
				"ipv4.address":                        "10.30.0.1/24",
				"bridge.driver":                       "native",
				"security.acls":                       "lxm-br0",
				"security.acls.default.egress.action": "reject",
			}},
		},
		ACLs: map[string]*api.NetworkACL{},
	}
	rec := plan.NewNetworkReconciler()
	np, err := rec.ComputeNetworks(f, live)
	if err != nil {
		t.Fatalf("ComputeNetworks: %v", err)
	}
	found := false
	for _, s := range np.Steps {
		if s.Kind == "update_vswitch" && s.Name == "br0" {
			found = true
			if _, still := s.NetPut.Config["security.acls"]; still {
				t.Errorf("group removal must clear security.acls, got %v", s.NetPut.Config["security.acls"])
			}
			if _, still := s.NetPut.Config["security.acls.default.egress.action"]; still {
				t.Errorf("group removal must clear default egress action")
			}
		}
	}
	if !found {
		t.Fatalf("expected update_vswitch detach for br0")
	}
}

func TestComputeNetworks_UnmanageWarning(t *testing.T) {
	f := testFleet(t, fleetConfigs()...)
	live := &plan.NetworkLiveState{
		Networks: map[string]*api.Network{
			"ghostbr0": {Name: "ghostbr0", Config: map[string]string{"user.lxm.managed": "true"}},
		},
		ACLs: map[string]*api.NetworkACL{},
	}
	rec := plan.NewNetworkReconciler()
	np, err := rec.ComputeNetworks(f, live)
	if err != nil {
		t.Fatalf("ComputeNetworks: %v", err)
	}
	if len(np.Warnings) == 0 || !strings.Contains(np.Warnings[0], "left unmanaged") {
		t.Fatalf("expected unmanage warning, got: %v", np.Warnings)
	}
}

func TestComputeNetworks_ACLRuleDrift_Updates(t *testing.T) {
	f := testFleet(t, fleetConfigs()...)
	live := &plan.NetworkLiveState{
		Networks: map[string]*api.Network{
			"vmbr0": {Name: "vmbr0", Config: map[string]string{"user.lxm.managed": "true", "ipv4.address": "10.30.0.1/24", "bridge.driver": "native"}},
		},
		ACLs: map[string]*api.NetworkACL{
			// Stale: a rule that no longer belongs.
			"lxm-vmbr0": {
				Name:   "lxm-vmbr0",
				Config: map[string]string{"user.lxm.managed": "true"},
				Egress: []api.NetworkACLRule{
					{Action: "reject", Source: "10.30.0.0/24", Destination: "10.9.9.0/24", State: "enabled"},
				},
			},
		},
	}
	rec := plan.NewNetworkReconciler()
	np, err := rec.ComputeNetworks(f, live)
	if err != nil {
		t.Fatalf("ComputeNetworks: %v", err)
	}
	found := false
	for _, s := range np.Steps {
		if s.Kind == "update_acl" && s.Name == "lxm-vmbr0" {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected update_acl for lxm-vmbr0")
	}
}

func TestComputeNetworks_Noop(t *testing.T) {
	f := testFleet(t, fleetConfigs()...)
	// Build the desired ACL from the compiler so live == desired.
	acls := map[string]*api.NetworkACL{}
	for _, acl := range network.Compile(f) {
		ingress, egress := network.ACLToAPIRules(acl)
		acls[acl.Name] = &api.NetworkACL{
			Name:        acl.Name,
			Description: acl.Description,
			Config:      map[string]string{"user.lxm.managed": "true"},
			Ingress:     ingress,
			Egress:      egress,
		}
	}
	live := &plan.NetworkLiveState{
		Networks: map[string]*api.Network{
			"vmbr0": {Name: "vmbr0", Description: "lxm managed vswitch (group vms)", Config: map[string]string{
				"user.lxm.managed":                     "true",
				"ipv4.address":                         "10.30.0.1/24",
				"bridge.driver":                        "native",
				"ipv4.nat":                             "true",
				"ipv4.dhcp":                            "true",
				"ipv6.address":                         "none",
				"dns.domain":                           "lxd",
				"security.acls":                        "lxm-vmbr0",
				"security.acls.default.ingress.action": "reject",
				"security.acls.default.egress.action":  "reject",
			}},
			"svcbr0": {Name: "svcbr0", Description: "lxm managed vswitch (group services)", Config: map[string]string{
				"user.lxm.managed":                     "true",
				"ipv4.address":                         "10.50.0.1/24",
				"bridge.driver":                        "native",
				"ipv4.nat":                             "true",
				"ipv4.dhcp":                            "true",
				"ipv6.address":                         "none",
				"dns.domain":                           "lxd",
				"security.acls":                        "lxm-svcbr0",
				"security.acls.default.ingress.action": "reject",
				"security.acls.default.egress.action":  "reject",
			}},
		},
		ACLs: acls,
	}
	rec := plan.NewNetworkReconciler()
	np, err := rec.ComputeNetworks(f, live)
	if err != nil {
		t.Fatalf("ComputeNetworks: %v", err)
	}
	if len(np.Steps) != 0 {
		t.Fatalf("expected noop plan, got %d steps: %+v", len(np.Steps), np.Steps)
	}
}

func TestComputeNetworks_NATFalse_FlowsToCreatePayload(t *testing.T) {
	// Regression for the code review finding: nat: false must be accepted and
	// rendered as ipv4.nat=false on the created vswitch.
	boolPtr := func(b bool) *bool { return &b }
	f := testFleet(t, &config.Config{
		Schema:    "lxm/config/v2",
		Base:      true,
		VSwitches: []config.VSwitchConfig{{Name: "routed0", IPv4: "10.30.0.1/24", Group: "routed", NAT: boolPtr(false)}},
	})
	rec := plan.NewNetworkReconciler()
	np, err := rec.ComputeNetworks(f, &plan.NetworkLiveState{Networks: map[string]*api.Network{}, ACLs: map[string]*api.NetworkACL{}})
	if err != nil {
		t.Fatalf("ComputeNetworks: %v", err)
	}
	for _, s := range np.Steps {
		if s.Kind == "create_vswitch" && s.Name == "routed0" {
			if s.NetPost.Config["ipv4.nat"] != "false" {
				t.Fatalf("expected ipv4.nat=false, got %q", s.NetPost.Config["ipv4.nat"])
			}
			return
		}
	}
	t.Fatalf("no create_vswitch for routed0")
}

func TestComputeNetworks_ExtensionNotRequiredForUngrouped(t *testing.T) {
	// computeNetworkPlan gates on grouped vswitches; the reconciler itself has
	// no extension dependency. Ungrouped vswitches produce only create_vswitch.
	f := testFleet(t, &config.Config{Schema: "lxm/config/v2", Base: true, VSwitches: []config.VSwitchConfig{{Name: "open0", IPv4: "10.20.0.1/24"}}})
	rec := plan.NewNetworkReconciler()
	np, err := rec.ComputeNetworks(f, &plan.NetworkLiveState{Networks: map[string]*api.Network{}, ACLs: map[string]*api.NetworkACL{}})
	if err != nil {
		t.Fatalf("ComputeNetworks: %v", err)
	}
	if len(np.Steps) != 1 || np.Steps[0].Kind != "create_vswitch" {
		t.Fatalf("expected single create_vswitch, got %v", np.Steps)
	}
}

func TestComputeNetworks_Tightened_OnlyWhenAllowsRemoved(t *testing.T) {
	f := testFleet(t, fleetConfigs()...)
	rec := plan.NewNetworkReconciler()

	// Live ACL has the full mutual-policy allow set; desired removes one allow
	// (simulate tightening by making the live ACL richer).
	liveACL := &api.NetworkACL{
		Name:   "lxm-vmbr0",
		Config: map[string]string{"user.lxm.managed": "true"},
		Egress: []api.NetworkACLRule{
			{Action: "allow", Source: "10.30.0.0/24", Destination: "10.31.0.0/24", State: "enabled"},
			{Action: "allow", Source: "10.30.0.0/24", Destination: "10.50.0.0/24", State: "enabled"},
			{Action: "reject", Source: "10.30.0.0/24", Destination: "10.9.9.0/24", State: "enabled"},
		},
	}
	np, err := rec.ComputeNetworks(f, &plan.NetworkLiveState{
		Networks: map[string]*api.Network{
			"vmbr0": {Name: "vmbr0", Description: "lxm managed vswitch (group vms)", Config: map[string]string{"user.lxm.managed": "true", "ipv4.address": "10.30.0.1/24", "bridge.driver": "native"}},
		},
		ACLs: map[string]*api.NetworkACL{"lxm-vmbr0": liveACL},
	})
	if err != nil {
		t.Fatalf("ComputeNetworks: %v", err)
	}
	for _, s := range np.Steps {
		if s.Kind == "update_acl" && s.Name == "lxm-vmbr0" {
			if !s.Tightened {
				t.Fatalf("expected Tightened=true when an allow was removed")
			}
			return
		}
	}
	t.Fatalf("expected update_acl step")
}

func TestComputeNetworks_NotTightened_OnWidening(t *testing.T) {
	f := testFleet(t, fleetConfigs()...)
	rec := plan.NewNetworkReconciler()

	// Live ACL is missing an allow that desired adds -> widening, not tightening.
	liveACL := &api.NetworkACL{
		Name:   "lxm-vmbr0",
		Config: map[string]string{"user.lxm.managed": "true"},
		Egress: []api.NetworkACLRule{
			{Action: "allow", Source: "10.30.0.0/24", Destination: "0.0.0.0/0", State: "enabled"},
		},
	}
	np, err := rec.ComputeNetworks(f, &plan.NetworkLiveState{
		Networks: map[string]*api.Network{
			"vmbr0": {Name: "vmbr0", Description: "lxm managed vswitch (group vms)", Config: map[string]string{"user.lxm.managed": "true", "ipv4.address": "10.30.0.1/24", "bridge.driver": "native"}},
		},
		ACLs: map[string]*api.NetworkACL{"lxm-vmbr0": liveACL},
	})
	if err != nil {
		t.Fatalf("ComputeNetworks: %v", err)
	}
	for _, s := range np.Steps {
		if s.Kind == "update_acl" && s.Name == "lxm-vmbr0" {
			if s.Tightened {
				t.Fatalf("expected Tightened=false for a widening update")
			}
			return
		}
	}
	t.Fatalf("expected update_acl step")
}

func TestComputeNetworks_UnmanagedACL_OverwriteWarning(t *testing.T) {
	f := testFleet(t, fleetConfigs()...)
	rec := plan.NewNetworkReconciler()
	// Hand-created ACL (no lxm marker) with stale rules.
	liveACL := &api.NetworkACL{
		Name:   "lxm-vmbr0",
		Config: map[string]string{},
		Egress: []api.NetworkACLRule{{Action: "reject", Source: "10.30.0.0/24", Destination: "10.9.9.0/24", State: "enabled"}},
	}
	np, err := rec.ComputeNetworks(f, &plan.NetworkLiveState{
		Networks: map[string]*api.Network{
			"vmbr0": {Name: "vmbr0", Description: "lxm managed vswitch (group vms)", Config: map[string]string{"user.lxm.managed": "true", "ipv4.address": "10.30.0.1/24", "bridge.driver": "native"}},
		},
		ACLs: map[string]*api.NetworkACL{"lxm-vmbr0": liveACL},
	})
	if err != nil {
		t.Fatalf("ComputeNetworks: %v", err)
	}
	found := false
	for _, w := range np.Warnings {
		if strings.Contains(w, "without the lxm managed marker") {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected overwrite warning for unmanaged ACL, got: %v", np.Warnings)
	}
}

func TestComputeNetworks_OrphanedACL_AnnotatedAndWarned(t *testing.T) {
	// Group removed: the orphaned lxm ACL's description must be annotated, and
	// an lxm ACL whose vswitch vanished entirely must be surfaced as a warning.
	ungrouped := testFleet(t, &config.Config{
		Schema:    "lxm/config/v2",
		Base:      true,
		VSwitches: []config.VSwitchConfig{{Name: "br0", IPv4: "10.30.0.1/24"}}, // group removed
	})
	rec := plan.NewNetworkReconciler()
	np, err := rec.ComputeNetworks(ungrouped, &plan.NetworkLiveState{
		Networks: map[string]*api.Network{
			"br0": {Name: "br0", Description: "lxm managed vswitch (group vms)", Config: map[string]string{"user.lxm.managed": "true", "ipv4.address": "10.30.0.1/24", "bridge.driver": "native", "security.acls": "lxm-br0"}},
		},
		ACLs: map[string]*api.NetworkACL{
			"lxm-br0":   {Name: "lxm-br0", Config: map[string]string{"user.lxm.managed": "true"}, Description: "lxm managed policy for vswitch br0 (group vms)"},
			"lxm-ghost": {Name: "lxm-ghost", Config: map[string]string{"user.lxm.managed": "true"}},
		},
	})
	if err != nil {
		t.Fatalf("ComputeNetworks: %v", err)
	}
	annotated := false
	for _, s := range np.Steps {
		if s.Kind == "update_acl" && s.Name == "lxm-br0" {
			if !strings.Contains(s.ACLPut.Description, "unattached") {
				t.Fatalf("orphaned ACL description not annotated: %q", s.ACLPut.Description)
			}
			annotated = true
		}
	}
	if !annotated {
		t.Fatalf("expected update_acl annotation for orphaned lxm-br0")
	}
	ghostWarned := false
	for _, w := range np.Warnings {
		if strings.Contains(w, "lxm-ghost") && strings.Contains(w, "left unmanaged") {
			ghostWarned = true
		}
	}
	if !ghostWarned {
		t.Fatalf("expected orphaned-ACL warning, got: %v", np.Warnings)
	}
}

func TestComputeNetworks_SecurityACLs_OrderInsensitive(t *testing.T) {
	// The live security.acls may be ordered differently from lxm's sorted
	// desired form (foreign ACL appended by an operator, or LXD reordering);
	// this must NOT cause a perpetual update_vswitch churn.
	f := testFleet(t, fleetConfigs()...)
	rec := plan.NewNetworkReconciler()
	np, err := rec.ComputeNetworks(f, &plan.NetworkLiveState{
		Networks: map[string]*api.Network{
			"vmbr0": {Name: "vmbr0", Description: "lxm managed vswitch (group vms)", Config: map[string]string{
				"user.lxm.managed":                     "true",
				"ipv4.address":                         "10.30.0.1/24",
				"bridge.driver":                        "native",
				"ipv4.nat":                             "true",
				"ipv4.dhcp":                            "true",
				"ipv6.address":                         "none",
				"dns.domain":                           "lxd",
				"security.acls":                        "handwritten,lxm-vmbr0", // foreign first
				"security.acls.default.ingress.action": "reject",
				"security.acls.default.egress.action":  "reject",
			}},
		},
		ACLs: map[string]*api.NetworkACL{},
	})
	if err != nil {
		t.Fatalf("ComputeNetworks: %v", err)
	}
	for _, s := range np.Steps {
		if s.Kind == "update_vswitch" && s.Name == "vmbr0" {
			t.Fatalf("security.acls order must not cause update churn")
		}
	}
}
