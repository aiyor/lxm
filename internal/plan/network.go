package plan

import (
	"fmt"
	"net"
	"sort"
	"strconv"
	"strings"

	"github.com/aiyor/lxm/internal/network"
	"github.com/aiyor/lxm/internal/provider"
)

// NetworkStep is a reconciliation step for a LXD/Incus network or network ACL.
// Executed before instance steps, in order: ACL steps, then vswitch steps
// (§7.4, driven by C8).
type NetworkStep struct {
	Kind      string                            `json:"kind"` // create_acl | update_acl | create_vswitch | update_vswitch | delete_vswitch
	Name      string                            `json:"name"`
	Changed   bool                              `json:"changed"`
	Diff      []FieldDiff                       `json:"diff,omitempty"`
	Tightened bool                              `json:"tightened,omitempty"` // update_acl narrows/removes allows (conntrack warning)
	ACLPost   *provider.NetworkACLCreateRequest `json:"acl_post,omitempty"`
	ACLPut    *provider.NetworkACLUpdateRequest `json:"acl_put,omitempty"`
	NetPost   *provider.NetworkCreateRequest    `json:"network_post,omitempty"`
	NetPut    *provider.NetworkUpdateRequest    `json:"network_put,omitempty"`
}

// NetworkPlan is the fleet-scoped reconciliation plan for vswitches and ACLs.
type NetworkPlan struct {
	Steps    []NetworkStep
	Warnings []string
}

// NetworkLiveState snapshots the live networks and ACLs at plan time.
type NetworkLiveState struct {
	Networks map[string]*provider.Network
	ACLs     map[string]*provider.NetworkACL
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
		live = &NetworkLiveState{Networks: map[string]*provider.Network{}, ACLs: map[string]*provider.NetworkACL{}}
	}
	if live.Networks == nil {
		live.Networks = map[string]*provider.Network{}
	}
	if live.ACLs == nil {
		live.ACLs = map[string]*provider.NetworkACL{}
	}

	np := &NetworkPlan{Steps: []NetworkStep{}, Warnings: []string{}}

	// For OVN vswitches, auto-resolve parent DNS resolver /32 if not already populated.
	for _, vs := range f.VSwitches {
		if vs.EffectiveType() == "ovn" {
			resolvers, warn := deriveDNSResolvers(vs, live)
			if len(vs.DNSResolvers) == 0 && len(resolvers) > 0 {
				vs.DNSResolvers = resolvers
			}
			if warn != "" {
				np.Warnings = append(np.Warnings, warn)
			}
		}
	}

	compiled := network.Compile(f)
	for _, acl := range compiled {
		if n := network.RejectRuleCount(acl); n > 256 {
			np.Warnings = append(np.Warnings, fmt.Sprintf("ACL %q has %d reject rules (>256); consider fewer inter-group carve-outs", acl.Name, n))
		}
	}
	aclByName := make(map[string]*network.CompiledACL)
	for _, acl := range compiled {
		aclByName[acl.Name] = acl
	}

	// 1. ACL reconciliation (must precede vswitch steps: C8).
	for _, acl := range compiled {
		ingress, egress := network.ACLToAPIRules(acl)
		desired := &provider.NetworkACLUpdateRequest{
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
				ACLPost: &provider.NetworkACLCreateRequest{
					Name:        acl.Name,
					Description: acl.Description,
					Ingress:     ingress,
					Egress:      egress,
					Config:      map[string]string{"user.lxm.managed": "true"},
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
		aclName := network.ACLName(vs.Name)
		_, aclExists := live.ACLs[aclName]

		if vs.Status == "absent" {
			if ok && len(liveNet.UsedBy) > 0 {
				return nil, fmt.Errorf("vswitch %q cannot be deleted (status: absent); %d instance(s) still attached: %s", vs.Name, len(liveNet.UsedBy), strings.Join(liveNet.UsedBy, ", "))
			}
			if ok || aclExists {
				np.Steps = append(np.Steps, NetworkStep{
					Kind:    "delete_vswitch",
					Name:    vs.Name,
					Changed: true,
					Diff: []FieldDiff{
						{Field: "status", Old: "present", New: "absent"},
					},
				})
			}
			continue
		}

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
						ACLPut: &provider.NetworkACLUpdateRequest{
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
func allowsRemoved(live *provider.NetworkACL, desired *provider.NetworkACLUpdateRequest) bool {
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

func allowRuleKey(dir string, r provider.NetworkACLRule) string {
	return dir + "\x00" + r.Source + "\x00" + r.Destination
}

// buildNetworksPost constructs the create payload for a vswitch.
func buildNetworksPost(vs *network.VSwitch) *provider.NetworkCreateRequest {
	netType := vs.EffectiveType()
	cfg := make(map[string]string)
	for k, v := range vs.Config {
		cfg[k] = v
	}

	description := ""
	if vs.Group != "" {
		description = fmt.Sprintf("lxm managed vswitch (group %s)", vs.Group)
	}

	if netType == "ovn" {
		cfg["network"] = vs.EffectiveParent()
		cfg["ipv4.address"] = vs.IPv4
		cfg["ipv6.address"] = "none"
		cfg["ipv4.nat"] = strconv.FormatBool(vs.EffectiveNAT())
		if vs.MTU > 0 {
			cfg["bridge.mtu"] = strconv.Itoa(vs.MTU)
		}
		cfg["dns.domain"] = "lxd"
		cfg["user.lxm.managed"] = "true"
		if vs.Group != "" {
			cfg["security.acls"] = network.ACLName(vs.Name)
			cfg["security.acls.default.ingress.action"] = "reject"
			cfg["security.acls.default.egress.action"] = "reject"
		}
		return &provider.NetworkCreateRequest{
			Name:        vs.Name,
			Type:        "ovn",
			Description: description,
			Config:      cfg,
		}
	}

	cfg["bridge.driver"] = vs.EffectiveDriver()
	cfg["ipv4.address"] = vs.IPv4
	cfg["ipv4.nat"] = strconv.FormatBool(vs.EffectiveNAT())
	cfg["ipv4.dhcp"] = "true"
	cfg["ipv6.address"] = "none"
	cfg["dns.domain"] = "lxd"
	cfg["user.lxm.managed"] = "true"
	if vs.MTU > 0 {
		cfg["bridge.mtu"] = strconv.Itoa(vs.MTU)
	}

	if vs.Group != "" {
		cfg["security.acls"] = network.ACLName(vs.Name)
		cfg["security.acls.default.ingress.action"] = "reject"
		cfg["security.acls.default.egress.action"] = "reject"
	}
	return &provider.NetworkCreateRequest{
		Name:        vs.Name,
		Type:        "bridge",
		Description: description,
		Config:      cfg,
	}
}

// checkImmutableDrift returns a plan error when a live vswitch's immutable
// keys (ipv4.address, driver, network parent) drift from the desired spec (§7.3).
func checkImmutableDrift(vs *network.VSwitch, live *provider.Network) error {
	if live.Type != "" && vs.EffectiveType() != "" && live.Type != vs.EffectiveType() {
		return fmt.Errorf("vswitch %q: type change %q -> %q is immutable after create", vs.Name, live.Type, vs.EffectiveType())
	}

	liveAddr := live.Config["ipv4.address"]
	if liveAddr != "" && liveAddr != vs.IPv4 {
		return fmt.Errorf("vswitch %q: subnet change %q -> %q requires migrating instances to a new vswitch name (in-place renumbering is out of scope)", vs.Name, liveAddr, vs.IPv4)
	}

	if vs.EffectiveType() == "bridge" || vs.EffectiveType() == "" {
		liveDriver := live.Config["bridge.driver"]
		if liveDriver == "" {
			liveDriver = "native"
		}
		desiredDriver := vs.EffectiveDriver()
		if liveDriver != desiredDriver {
			return fmt.Errorf("vswitch %q: bridge.driver change %q -> %q is immutable after create (migrate to a new vswitch name)", vs.Name, liveDriver, desiredDriver)
		}
	}

	if vs.EffectiveType() == "ovn" {
		liveParent := live.Config["network"]
		desiredParent := vs.EffectiveParent()
		if liveParent != "" && desiredParent != "" && liveParent != desiredParent {
			return fmt.Errorf("vswitch %q: uplink parent change %q -> %q requires migrating instances to a new vswitch name (uplink parent is immutable in lxm policy)", vs.Name, liveParent, desiredParent)
		}
	}
	return nil
}

// aclReferenced reports whether the network's security.acls references the
// given ACL name.
func aclReferenced(live *provider.Network, name string) bool {
	for _, n := range splitACLs(live.Config["security.acls"]) {
		if n == name {
			return true
		}
	}
	return false
}

// foreignACLs returns the network's security.acls entries other than name.
func foreignACLs(live *provider.Network, name string) []string {
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
func buildNetworkUpdate(vs *network.VSwitch, live *provider.Network) (*provider.NetworkUpdateRequest, []FieldDiff) {
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
	put := &provider.NetworkUpdateRequest{
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
func desiredNetworkConfig(vs *network.VSwitch, live *provider.Network) map[string]string {
	out := make(map[string]string)
	for k, v := range live.Config {
		out[k] = v
	}
	if vs.EffectiveType() == "bridge" || vs.EffectiveType() == "" {
		out["bridge.driver"] = vs.EffectiveDriver()
		out["ipv4.dhcp"] = "true"
	}
	if vs.MTU > 0 {
		out["bridge.mtu"] = strconv.Itoa(vs.MTU)
	} else if live.Config["user.lxm.managed"] == "true" {
		delete(out, "bridge.mtu")
	}
	out["ipv4.address"] = vs.IPv4
	out["ipv4.nat"] = strconv.FormatBool(vs.EffectiveNAT())
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
func aclRulesEqual(live *provider.NetworkACL, desired *provider.NetworkACLUpdateRequest) bool {
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

// deriveDNSResolvers extracts effective DNS resolver IPs for an OVN vswitch.
// Resolves in order: explicit vs.DNSResolvers -> vs.Config["dns.nameservers"] ->
// parentNet.Config["dns.nameservers"] -> parentNet.Config["ipv4.address"] (or volatile address if "auto").
func deriveDNSResolvers(vs *network.VSwitch, live *NetworkLiveState) ([]string, string) {
	if len(vs.DNSResolvers) > 0 {
		return vs.DNSResolvers, ""
	}
	var resolvers []string
	addIP := func(raw string) {
		raw = strings.TrimSpace(raw)
		if raw == "" || raw == "none" || raw == "auto" {
			return
		}
		if ip, _, err := net.ParseCIDR(raw); err == nil && ip.To4() != nil {
			resolvers = append(resolvers, ip.String()+"/32")
			return
		}
		if ip := net.ParseIP(raw); ip != nil && ip.To4() != nil {
			resolvers = append(resolvers, ip.String()+"/32")
			return
		}
	}

	if vs.Config != nil {
		if ns, ok := vs.Config["dns.nameservers"]; ok && ns != "" {
			for _, item := range strings.FieldsFunc(ns, func(r rune) bool { return r == ',' || r == ' ' || r == '\t' }) {
				addIP(item)
			}
		}
	}

	if live != nil && live.Networks != nil {
		parentName := vs.EffectiveParent()
		if parentNet, ok := live.Networks[parentName]; ok && parentNet.Config != nil {
			if ns, ok := parentNet.Config["dns.nameservers"]; ok && ns != "" {
				for _, item := range strings.FieldsFunc(ns, func(r rune) bool { return r == ',' || r == ' ' || r == '\t' }) {
					addIP(item)
				}
			}
			if len(resolvers) == 0 {
				if ipStr, ok := parentNet.Config["ipv4.address"]; ok && ipStr != "" && ipStr != "none" {
					if ipStr == "auto" {
						if volIP, ok := parentNet.Config["volatile.network.ipv4.address"]; ok && volIP != "" {
							addIP(volIP)
						}
					} else {
						addIP(ipStr)
					}
				}
			}
		}
	}

	var warn string
	if len(resolvers) == 0 && vs.EffectiveInternet() {
		warn = fmt.Sprintf("vswitch %q: unable to derive uplink DNS resolver; private DNS queries may be rejected by G8 policy", vs.Name)
	}

	seen := make(map[string]bool)
	var deduped []string
	for _, r := range resolvers {
		if !seen[r] {
			seen[r] = true
			deduped = append(deduped, r)
		}
	}
	return deduped, warn
}
