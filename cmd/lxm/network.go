package main

import (
	"fmt"

	"github.com/aiyor/lxm/internal/config"
	"github.com/aiyor/lxm/internal/lxd"
	"github.com/aiyor/lxm/internal/network"
	"github.com/aiyor/lxm/internal/plan"
	"github.com/canonical/lxd/shared/api"
)

// computeNetworkPlan performs the fleet-scoped network reconciliation for an
// invocation: union of vswitches/network_policy across ALL loaded manifests
// (selector-scope invariant §7.2), extension gating (§7.5), live-state diff,
// and instance-NIC integrity checks (§4).
//
// Config/policy/union violations return exit code 3; a missing network_acl
// extension (with grouped vswitches declared) returns exit code 4.
func computeNetworkPlan(svc lxd.InstanceService, loaded []*config.Config, netReconciler plan.NetworkReconciler) (*plan.NetworkPlan, []string, error) {
	fleet, err := network.Union(loaded)
	if err != nil {
		return nil, nil, &exitError{code: 3, err: err}
	}

	warnings := append([]string{}, fleet.Warnings...)

	hasGrouped := false
	for _, vs := range fleet.VSwitches {
		if vs.Group != "" {
			hasGrouped = true
			break
		}
	}

	// Rule-count guard (§3.2): >256 reject rules per vswitch warns, proceeds.
	for _, acl := range network.Compile(fleet) {
		if network.RuleCount(acl) > 256 {
			warnings = append(warnings, fmt.Sprintf("ACL %q has %d rules (>256); consider fewer inter-group carve-outs", acl.Name, network.RuleCount(acl)))
		}
	}

	live := &plan.NetworkLiveState{
		Networks: map[string]*api.Network{},
		ACLs:     map[string]*api.NetworkACL{},
	}

	if svc != nil {
		netSvc, ok := svc.(lxd.NetworkService)
		if !ok {
			return nil, nil, &exitError{code: 4, err: fmt.Errorf("LXD service does not support network operations (network_policy unavailable)")}
		}
		if hasGrouped && !svc.HasExtension("network_acl") {
			return nil, nil, &exitError{code: 4, err: fmt.Errorf("LXD server lacks the network_acl extension; grouped vswitches cannot be policy-managed (needs LXD with bridge network ACL support)")}
		}
		if nets, err := netSvc.GetNetworks(); err == nil {
			for i := range nets {
				live.Networks[nets[i].Name] = &nets[i]
			}
		}
		if acls, err := netSvc.GetNetworkACLs(); err == nil {
			for i := range acls {
				live.ACLs[acls[i].Name] = &acls[i]
			}
		}
	}

	np, err := netReconciler.ComputeNetworks(fleet, live)
	if err != nil {
		return nil, nil, &exitError{code: 3, err: err}
	}
	warnings = append(warnings, np.Warnings...)

	// Instance-NIC integrity (§4) against the FULL loaded fleet.
	liveNetNames := make(map[string]bool)
	for n := range live.Networks {
		liveNetNames[n] = true
	}
	nicWarnings, err := network.CheckInstances(loaded, fleet, liveNetNames)
	if err != nil {
		return nil, nil, &exitError{code: 3, err: err}
	}
	warnings = append(warnings, nicWarnings...)

	return np, warnings, nil
}
