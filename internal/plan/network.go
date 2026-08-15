package plan

import (
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/aiyor/lxm/internal/network"
	"github.com/canonical/lxd/shared/api"
)

// NetworkStep is a reconciliation step for a LXD network or network ACL.
// Executed before instance steps, in order: ACL steps, then vswitch steps
// (§7.4, driven by C8).
type NetworkStep struct {
	Kind    string               `json:"kind"` // create_acl | update_acl | create_vswitch | update_vswitch
	Name    string               `json:"name"`
	Changed bool                 `json:"changed"`
	Diff    []FieldDiff          `json:"diff,omitempty"`
	ACLPost *api.NetworkACLsPost `json:"acl_post,omitempty"`
	ACLPut  *api.NetworkACLPut   `json:"acl_put,omitempty"`
	NetPost *api.NetworksPost    `json:"network_post,omitempty"`
	NetPut  *api.NetworkPut      `json:"network_put,omitempty"`
}

// NetworkPlan is the fleet-scoped reconciliation plan for vswitches and ACLs.
type NetworkPlan struct {
	Steps    []NetworkStep
	Warnings []string
}

// NetworkLiveState snapshots the live LXD networks and ACLs at plan time.
type NetworkLiveState struct {
	Networks map[string]*api.Network
	ACLs     map[string]*api.NetworkACL
}

// NetworkReconciler computes the fleet-scoped network reconciliation plan.
type NetworkReconciler interface {
	ComputeNetworks(fleet *network.Fleet, live *NetworkLiveState) (*NetworkPlan, error)
}

// NewNetworkReconciler returns a reconciler for the fleet-scoped network plan.
func NewNetworkReconciler() NetworkReconciler {
	return &defaultReconciler{}
}

// ComputeNetworks implements NetworkReconciler for defaultReconciler.
func (r *defaultReconciler) ComputeNetworks(f *network.Fleet, live *NetworkLiveState) (*NetworkPlan, error) {
	if f == nil {
		return nil, fmt.Errorf("fleet cannot be nil")
	}
	if live == nil {
		live = &NetworkLiveState{Networks: map[string]*api.Network{}, ACLs: map[string]*api.NetworkACL{}}
	}
	if live.Networks == nil {
		live.Networks = map[string]*api.Network{}
	}
	if live.ACLs == nil {
		live.ACLs = map[string]*api.NetworkACL{}
	}

	np := &NetworkPlan{Steps: []NetworkStep{}, Warnings: []string{}}

	compiled := network.Compile(f)
	aclByName := make(map[string]*network.CompiledACL)
	for _, acl := range compiled {
		aclByName[acl.Name] = acl
	}

	// 1. ACL reconciliation (must precede vswitch steps: C8).
	for _, acl := range compiled {
		ingress, egress := network.ACLToAPIRules(acl)
		desired := &api.NetworkACLPut{
			Description: acl.Description,
			Ingress:     ingress,
			Egress:      egress,
			Config:      map[string]string{"user.lxm.managed": "true"},
		}

		liveACL, ok := live.ACLs[acl.Name]
		if !ok {
			np.Steps = append(np.Steps, NetworkStep{
				Kind:    "create_acl",
				Name:    acl.Name,
				Changed: true,
				ACLPost: &api.NetworkACLsPost{
					NetworkACLPost: api.NetworkACLPost{Name: acl.Name},
					NetworkACLPut:  *desired,
				},
			})
			continue
		}

		if !aclRulesEqual(liveACL, desired) || liveACL.Description != desired.Description {
			np.Steps = append(np.Steps, NetworkStep{
				Kind:    "update_acl",
				Name:    acl.Name,
				Changed: true,
				ACLPut:  desired,
				Diff: []FieldDiff{
					{Field: "rules", Old: fmt.Sprintf("%d live rules", len(liveACL.Egress)+len(liveACL.Ingress)), New: fmt.Sprintf("%d desired rules", len(egress)+len(ingress))},
				},
			})
		}
	}

	// 2. Vswitch reconciliation.
	for _, vs := range f.VSwitches {
		liveNet, ok := live.Networks[vs.Name]
		if !ok {
			// missing -> create
			np.Steps = append(np.Steps, NetworkStep{
				Kind:    "create_vswitch",
				Name:    vs.Name,
				Changed: true,
				NetPost: buildNetworksPost(vs),
			})
			continue
		}

		// exists -> immutable drift refusal
		if err := checkImmutableDrift(vs, liveNet); err != nil {
			return nil, err
		}

		// adoption: refuse foreign ACL
		if liveNet.Config["user.lxm.managed"] != "true" && !aclReferenced(liveNet, network.ACLName(vs.Name)) {
			if foreign := foreignACLs(liveNet, network.ACLName(vs.Name)); len(foreign) > 0 {
				return nil, fmt.Errorf("vswitch %q exists unmanaged by lxm with foreign security.acls %s; refusing to clobber hand-written policy (adopt manually or rename)", vs.Name, strings.Join(foreign, ","))
			}
		}

		// mutable drift -> update
		if put, diff := buildNetworkUpdate(vs, liveNet); put != nil {
			np.Steps = append(np.Steps, NetworkStep{
				Kind:    "update_vswitch",
				Name:    vs.Name,
				Changed: true,
				NetPut:  put,
				Diff:    diff,
			})
		}
	}

	// 3. Unmanage warning: lxm-managed networks no longer declared.
	for name, liveNet := range live.Networks {
		if liveNet.Config["user.lxm.managed"] != "true" {
			continue
		}
		if _, desired := f.ByName[name]; !desired {
			np.Warnings = append(np.Warnings, fmt.Sprintf("vswitch %q no longer declared; left unmanaged (lxm never deletes networks)", name))
		}
	}

	return np, nil
}

// buildNetworksPost constructs the create payload for a vswitch.
func buildNetworksPost(vs *network.VSwitch) *api.NetworksPost {
	cfg := map[string]string{
		"bridge.driver":    vs.EffectiveDriver(),
		"ipv4.address":     vs.IPv4,
		"ipv4.nat":         strconv.FormatBool(vs.EffectiveNAT()),
		"ipv4.dhcp":        "true",
		"ipv6.address":     "none",
		"dns.domain":       "lxd",
		"user.lxm.managed": "true",
	}
	description := ""
	if vs.Group != "" {
		cfg["security.acls"] = network.ACLName(vs.Name)
		cfg["security.acls.default.ingress.action"] = "reject"
		cfg["security.acls.default.egress.action"] = "reject"
		description = fmt.Sprintf("lxm managed vswitch (group %s)", vs.Group)
	}
	return &api.NetworksPost{
		Name: vs.Name,
		Type: "bridge",
		NetworkPut: api.NetworkPut{
			Config:      cfg,
			Description: description,
		},
	}
}

// checkImmutableDrift returns a plan error when a live vswitch's immutable
// keys (ipv4.address, driver) drift from the desired spec (§7.3).
func checkImmutableDrift(vs *network.VSwitch, live *api.Network) error {
	liveAddr := live.Config["ipv4.address"]
	if liveAddr != "" && liveAddr != vs.IPv4 {
		return fmt.Errorf("vswitch %q: subnet change %q -> %q requires migrating instances to a new vswitch name (in-place renumbering is out of scope)", vs.Name, liveAddr, vs.IPv4)
	}
	liveDriver := live.Config["bridge.driver"]
	if liveDriver == "" {
		liveDriver = "native"
	}
	desiredDriver := vs.EffectiveDriver()
	if liveDriver != desiredDriver {
		return fmt.Errorf("vswitch %q: bridge.driver change %q -> %q is immutable after create (migrate to a new vswitch name)", vs.Name, liveDriver, desiredDriver)
	}
	return nil
}

// aclReferenced reports whether the network's security.acls references the
// given ACL name.
func aclReferenced(live *api.Network, name string) bool {
	for _, n := range splitACLs(live.Config["security.acls"]) {
		if n == name {
			return true
		}
	}
	return false
}

// foreignACLs returns the network's security.acls entries other than name.
func foreignACLs(live *api.Network, name string) []string {
	var out []string
	for _, n := range splitACLs(live.Config["security.acls"]) {
		if n != name {
			out = append(out, n)
		}
	}
	sort.Strings(out)
	return out
}

func splitACLs(s string) []string {
	var out []string
	for _, part := range strings.Split(s, ",") {
		part = strings.TrimSpace(part)
		if part != "" {
			out = append(out, part)
		}
	}
	return out
}

// buildNetworkUpdate diffs the mutable keys of a live vswitch against the
// desired spec. Returns nil when no drift exists.
func buildNetworkUpdate(vs *network.VSwitch, live *api.Network) (*api.NetworkPut, []FieldDiff) {
	desired := desiredNetworkConfig(vs, live)
	diff := diffNetworkConfig(live.Config, desired)
	if len(diff) == 0 {
		return nil, nil
	}
	put := &api.NetworkPut{
		Config:      desired,
		Description: live.Description,
	}
	return put, diff
}

// desiredNetworkConfig computes the full desired config for an existing
// vswitch: live mutable values preserved, lxm-managed keys reconciled,
// foreign ACLs preserved verbatim.
func desiredNetworkConfig(vs *network.VSwitch, live *api.Network) map[string]string {
	out := make(map[string]string)
	for k, v := range live.Config {
		out[k] = v
	}
	out["bridge.driver"] = vs.EffectiveDriver()
	out["ipv4.address"] = vs.IPv4
	out["ipv4.nat"] = strconv.FormatBool(vs.EffectiveNAT())
	out["ipv4.dhcp"] = "true"
	out["ipv6.address"] = "none"
	out["dns.domain"] = "lxd"
	out["user.lxm.managed"] = "true"

	own := network.ACLName(vs.Name)
	if vs.Group != "" {
		// Reconcile security.acls: own ACL + preserved foreign ACLs.
		names := []string{own}
		for _, n := range splitACLs(live.Config["security.acls"]) {
			if n != own {
				names = append(names, n)
			}
		}
		sort.Strings(names)
		out["security.acls"] = strings.Join(names, ",")
		out["security.acls.default.ingress.action"] = "reject"
		out["security.acls.default.egress.action"] = "reject"
	} else {
		// Ungrouped (or group removed): detach the lxm ACL, clear defaults.
		var names []string
		for _, n := range splitACLs(live.Config["security.acls"]) {
			if n != own {
				names = append(names, n)
			}
		}
		if len(names) > 0 {
			out["security.acls"] = strings.Join(names, ",")
		} else {
			delete(out, "security.acls")
		}
		delete(out, "security.acls.default.ingress.action")
		delete(out, "security.acls.default.egress.action")
	}
	return out
}

func diffNetworkConfig(live, desired map[string]string) []FieldDiff {
	var keys []string
	seen := make(map[string]bool)
	for k := range live {
		keys = append(keys, k)
		seen[k] = true
	}
	for k := range desired {
		if !seen[k] {
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)
	var diff []FieldDiff
	for _, k := range keys {
		if live[k] != desired[k] {
			diff = append(diff, FieldDiff{Field: k, Old: live[k], New: desired[k]})
		}
	}
	return diff
}

// aclRulesEqual reports whether a live ACL matches the desired put payload.
func aclRulesEqual(live *api.NetworkACL, desired *api.NetworkACLPut) bool {
	return network.RulesEqual(live.Ingress, desired.Ingress) &&
		network.RulesEqual(live.Egress, desired.Egress)
}

// NetworkStepKindOrder orders network steps for execution (ACLs first).
func NetworkStepKindOrder(kind string) int {
	switch kind {
	case "create_acl", "update_acl":
		return 0
	case "create_vswitch", "update_vswitch":
		return 1
	default:
		return 2
	}
}
