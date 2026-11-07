package plan_test

import (
	"strings"
	"testing"

	"github.com/aiyor/lxm/internal/config"
	"github.com/aiyor/lxm/internal/network"
	"github.com/aiyor/lxm/internal/plan"
	"github.com/aiyor/lxm/internal/provider"
)

func ingressConfig(withNewFields bool) *config.Config {
	vs := config.VSwitchConfig{
		Name: "labbr0", IPv4: "10.208.0.1/24", Group: "lab1",
	}
	policy := &config.NetworkPolicy{Allow: []config.NetworkPolicyRule{}}
	if withNewFields {
		vs.HostAccess = true
		policy.IngressAllow = []config.IngressAllowRule{
			{To: "lab1", Source: "100.99.45.41/32", Protocol: "tcp", Ports: []string{"22", "3000-3999"}},
		}
	}
	return &config.Config{Schema: "lxm/config/v2", VSwitches: []config.VSwitchConfig{vs}, NetworkPolicy: policy}
}

func seedNetworkState(t *testing.T, conf *config.Config) *plan.NetworkLiveState {
	t.Helper()
	f, err := network.Union([]*config.Config{conf})
	if err != nil {
		t.Fatalf("Union: %v", err)
	}
	np, err := plan.NewNetworkReconciler().ComputeNetworks(f, &plan.NetworkLiveState{
		Networks: map[string]*provider.Network{},
		ACLs:     map[string]*provider.NetworkACL{},
	})
	if err != nil {
		t.Fatalf("ComputeNetworks seed: %v", err)
	}
	live := &plan.NetworkLiveState{
		Networks: map[string]*provider.Network{},
		ACLs:     map[string]*provider.NetworkACL{},
	}
	for _, step := range np.Steps {
		switch step.Kind {
		case "create_vswitch":
			live.Networks[step.Name] = &provider.Network{
				Name:        step.NetPost.Name,
				Type:        step.NetPost.Type,
				Description: step.NetPost.Description,
				Config:      step.NetPost.Config,
			}
		case "create_acl":
			live.ACLs[step.Name] = &provider.NetworkACL{
				Name:        step.ACLPost.Name,
				Description: step.ACLPost.Description,
				Ingress:     step.ACLPost.Ingress,
				Egress:      step.ACLPost.Egress,
				Config:      step.ACLPost.Config,
			}
		}
	}
	return live
}

func computeNetworks(t *testing.T, conf *config.Config, live *plan.NetworkLiveState) *plan.NetworkPlan {
	t.Helper()
	f, err := network.Union([]*config.Config{conf})
	if err != nil {
		t.Fatalf("Union: %v", err)
	}
	np, err := plan.NewNetworkReconciler().ComputeNetworks(f, live)
	if err != nil {
		t.Fatalf("ComputeNetworks: %v", err)
	}
	return np
}

func findACLStep(np *plan.NetworkPlan, name, kind string) *plan.NetworkStep {
	for i := range np.Steps {
		if np.Steps[i].Name == name && np.Steps[i].Kind == kind {
			return &np.Steps[i]
		}
	}
	return nil
}

func hasAPIRule(rules []provider.NetworkACLRule, want provider.NetworkACLRule) bool {
	for _, r := range rules {
		if r.Action == want.Action && r.Source == want.Source && r.Destination == want.Destination &&
			r.Protocol == want.Protocol && r.DestinationPort == want.DestinationPort {
			return true
		}
	}
	return false
}

func TestComputeNetworks_HostAccessCreate(t *testing.T) {
	np := computeNetworks(t, ingressConfig(true), &plan.NetworkLiveState{})
	step := findACLStep(np, "lxm-labbr0", "create_acl")
	if step == nil || step.ACLPost == nil {
		t.Fatalf("expected create_acl with payload, got %+v", np.Steps)
	}
	wantHost := provider.NetworkACLRule{Action: "allow", Source: "10.208.0.1/32", Destination: "10.208.0.0/24"}
	if !hasAPIRule(step.ACLPost.Ingress, wantHost) {
		t.Fatalf("missing host_access rule in create payload: %+v", step.ACLPost.Ingress)
	}
	wantGrant := provider.NetworkACLRule{Action: "allow", Source: "100.99.45.41/32", Destination: "10.208.0.0/24", Protocol: "tcp", DestinationPort: "22,3000-3999"}
	if !hasAPIRule(step.ACLPost.Ingress, wantGrant) {
		t.Fatalf("missing ingress_allow rule in create payload: %+v", step.ACLPost.Ingress)
	}
}

func TestComputeNetworks_IngressAllowUpdateAndNoop(t *testing.T) {
	live := seedNetworkState(t, ingressConfig(false))
	np := computeNetworks(t, ingressConfig(true), live)

	step := findACLStep(np, "lxm-labbr0", "update_acl")
	if step == nil || step.ACLPut == nil {
		t.Fatalf("expected update_acl with payload, got %+v", np.Steps)
	}
	wantHost := provider.NetworkACLRule{Action: "allow", Source: "10.208.0.1/32", Destination: "10.208.0.0/24"}
	if !hasAPIRule(step.ACLPut.Ingress, wantHost) {
		t.Fatalf("missing host_access rule in update payload: %+v", step.ACLPut.Ingress)
	}
	added := false
	for _, d := range step.Diff {
		if d.Field == "ingress.added" && d.New != nil && strings.Contains(d.New.(string), "10.208.0.1/32") {
			added = true
		}
	}
	if !added {
		t.Fatalf("expected ingress.added diff for host_access, got %+v", step.Diff)
	}

	// Seeding the live state with the desired rules makes the next plan a no-op.
	live = seedNetworkState(t, ingressConfig(true))
	np = computeNetworks(t, ingressConfig(true), live)
	if step := findACLStep(np, "lxm-labbr0", "update_acl"); step != nil {
		t.Fatalf("expected idempotent ACL plan, got update: %+v", step.Diff)
	}
}

func TestComputeNetworks_IngressAllowRemovalTightens(t *testing.T) {
	live := seedNetworkState(t, ingressConfig(true))
	np := computeNetworks(t, ingressConfig(false), live)

	step := findACLStep(np, "lxm-labbr0", "update_acl")
	if step == nil {
		t.Fatalf("expected update_acl removal step, got %+v", np.Steps)
	}
	if !step.Tightened {
		t.Fatalf("removing ingress allows must tighten the ACL: %+v", step)
	}
	removed := false
	for _, d := range step.Diff {
		if d.Field == "ingress.removed" && d.Old != nil && strings.Contains(d.Old.(string), "100.99.45.41/32") {
			removed = true
		}
	}
	if !removed {
		t.Fatalf("expected ingress.removed diff for grant removal, got %+v", step.Diff)
	}
}

func ingressConfigWithPorts(ports []string) *config.Config {
	conf := ingressConfig(true)
	conf.NetworkPolicy.IngressAllow[0].Ports = ports
	return conf
}

func TestComputeNetworks_IngressPortChangeTightening(t *testing.T) {
	// Narrowing: 22 -> 80 removes the port-22 allow, so the conntrack warning must fire.
	live := seedNetworkState(t, ingressConfigWithPorts([]string{"22"}))
	np := computeNetworks(t, ingressConfigWithPorts([]string{"80"}), live)
	step := findACLStep(np, "lxm-labbr0", "update_acl")
	if step == nil {
		t.Fatalf("expected update_acl, got %+v", np.Steps)
	}
	if !step.Tightened {
		t.Fatalf("port narrowing must tighten the ACL")
	}

	// Widening: 22 -> all ports is covered by the desired no-port rule and must
	// not produce a false tightening warning.
	live = seedNetworkState(t, ingressConfigWithPorts([]string{"22"}))
	np = computeNetworks(t, ingressConfigWithPorts(nil), live)
	step = findACLStep(np, "lxm-labbr0", "update_acl")
	if step == nil {
		t.Fatalf("expected update_acl, got %+v", np.Steps)
	}
	if step.Tightened {
		t.Fatalf("widening from one port to all ports must not be treated as tightening")
	}
}
