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
	Kind      string               `json:"kind"` // create_acl | update_acl | create_vswitch | update_vswitch
	Name      string               `json:"name"`
	Changed   bool                 `json:"changed"`
	Diff      []FieldDiff          `json:"diff,omitempty"`
	Tightened bool                 `json:"tightened,omitempty"` // update_acl narrows/removes allows (conntrack warning)
	ACLPost   *api.NetworkACLsPost `json:"acl_post,omitempty"`
	ACLPut    *api.NetworkACLPut   `json:"acl_put,omitempty"`
	NetPost   *api.NetworksPost    `json:"network_post,omitempty"`
	NetPut    *api.NetworkPut      `json:"network_put,omitempty"`
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
			step := NetworkStep{
				Kind:    "update_acl",
				Name:    acl.Name,
				Changed: true,
				ACLPut:  desired,
				Diff: []FieldDiff{
					{Field: "rules", Old: fmt.Sprintf("%d live rules", len(liveACL.Egress)+len(liveACL.Ingress)), New: fmt.Sprintf("%d desired rules", len(egress)+len(ingress))},
				},
			}
			// D3: the conntrack warning applies only when the change removes or
			// narrows allows (§7.6), not on a pure widening.
			step.Tightened = allowsRemoved(liveACL, desired)
			// D6: surface a hand-created ACL that lxm is about to overwrite.
			if liveACL.Config == nil || liveACL.Config["user.lxm.managed"] != "true" {
				np.Warnings = append(np.Warnings, fmt.Sprintf("ACL %q exists without the lxm managed marker and will be overwritten (the lxm- prefix is reserved)", acl.Name))
			}
			np.Steps = append(np.Steps, step)
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

		// D4a: when a group is removed, annotate the now-orphaned lxm ACL so
		// its description records that it is unattached (§7.3 un-grouping row).
		if vs.Group == "" {
			aclName := network.ACLName(vs.Name)
			if liveACL, ok := live.ACLs[aclName]; ok && liveACL.Config["user.lxm.managed"] == "true" {
				annotated := liveACL.Description
				if !strings.Contains(annotated, "unattached") {
					if annotated != "" {
						annotated += "; "
					}
					annotated += "lxm managed ACL; unattached (group removed)"
				}
				if annotated != liveACL.Description {
					np.Steps = append(np.Steps, NetworkStep{
						Kind:    "update_acl",
						Name:    aclName,
						Changed: true,
						ACLPut: &api.NetworkACLPut{
							Description: annotated,
							Ingress:     liveACL.Ingress,
							Egress:      liveACL.Egress,
							Config:      liveACL.Config,
						},
					})
				}
			}
		}
	}

	// 3. Unmanage warning: lxm-managed networks/ACLs no longer declared.
	for name, liveNet := range live.Networks {
		if liveNet.Config["user.lxm.managed"] != "true" {
			continue
		}
		if _, desired := f.ByName[name]; !desired {
			np.Warnings = append(np.Warnings, fmt.Sprintf("vswitch %q no longer declared; left unmanaged (lxm never deletes networks)", name))
		}
	}
	for aclName, liveACL := range live.ACLs {
		if liveACL.Config["user.lxm.managed"] != "true" {
			continue
		}
		if _, desired := aclByName[aclName]; !desired {
			np.Warnings = append(np.Warnings, fmt.Sprintf("network ACL %q no longer declared; left unmanaged (lxm never deletes ACLs)", aclName))
		}
	}

	return np, nil
}

// allowsRemoved reports whether an ACL update removes or narrows an allow rule
// relative to the live ACL (D3 — the §7.6 tightening signal).
func allowsRemoved(live *api.NetworkACL, desired *api.NetworkACLPut) bool {
	liveKeys := make(map[string]bool)
	for _, r := range live.Ingress {
		if r.Action == "allow" {
			liveKeys[allowRuleKey("ingress", r)] = true
		}
	}
	for _, r := range live.Egress {
		if r.Action == "allow" {
			liveKeys[allowRuleKey("egress", r)] = true
		}
	}
	for _, r := range desired.Ingress {
		if r.Action == "allow" {
			delete(liveKeys, allowRuleKey("ingress", r))
		}
	}
	for _, r := range desired.Egress {
		if r.Action == "allow" {
			delete(liveKeys, allowRuleKey("egress", r))
		}
	}
	return len(liveKeys) > 0
}

func allowRuleKey(dir string, r api.NetworkACLRule) string {
	return dir + "\x00" + r.Source + "\x00" + r.Destination
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

	// Reconcile the description too, so adding/removing a group leaves a
	// truthful one (minor finding: update_vswitch previously pinned the live
	// value forever). A foreign bridge's own description is preserved on
	// adoption rather than clobbered.
	desiredDesc := vswitchDescription(vs)
	if live.Description != "" && live.Config["user.lxm.managed"] != "true" {
		desiredDesc = live.Description
	}
	if live.Description != desiredDesc {
		diff = append(diff, FieldDiff{Field: "description", Old: live.Description, New: desiredDesc})
	}

	if len(diff) == 0 {
		return nil, nil
	}
	put := &api.NetworkPut{
		Config:      desired,
		Description: desiredDesc,
	}
	return put, diff
}

// vswitchDescription is the deterministic description for a managed vswitch.
func vswitchDescription(vs *network.VSwitch) string {
	if vs.Group == "" {
		return ""
	}
	return fmt.Sprintf("lxm managed vswitch (group %s)", vs.Group)
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
		equal := live[k] == desired[k]
		if k == "security.acls" {
			// ACL references are a set: order must not cause perpetual
			// update churn (lxm writes them sorted, but LXD/operators may
			// reorder, and foreign ACLs can be appended in any order).
			equal = sameACLSet(live[k], desired[k])
		}
		if !equal {
			diff = append(diff, FieldDiff{Field: k, Old: live[k], New: desired[k]})
		}
	}
	return diff
}

// sameACLSet reports whether two comma-separated ACL reference lists name the
// same set of ACLs (order-insensitive).
func sameACLSet(a, b string) bool {
	sa, sb := splitACLs(a), splitACLs(b)
	if len(sa) != len(sb) {
		return false
	}
	mb := make(map[string]bool, len(sb))
	for _, n := range sb {
		mb[n] = true
	}
	for _, n := range sa {
		if !mb[n] {
			return false
		}
	}
	return true
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
