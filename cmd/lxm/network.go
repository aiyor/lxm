package main

import (
	"context"
	"fmt"

	"github.com/aiyor/lxm/internal/config"
	"github.com/aiyor/lxm/internal/network"
	"github.com/aiyor/lxm/internal/plan"
	"github.com/aiyor/lxm/internal/provider"
)

// computeNetworkPlan performs the fleet-scoped network reconciliation for an
// invocation: union of vswitches/network_policy across ALL loaded manifests
// (selector-scope invariant §7.2), extension gating (§7.5), live-state diff,
// and instance-NIC integrity checks (§4).
//
// Config/policy/union violations return exit code 3; a missing network_acl
// extension (with grouped vswitches declared) returns exit code 4.
func computeNetworkPlan(ctx context.Context, svc provider.Driver, loaded []*config.Config, netReconciler plan.NetworkReconciler) (*plan.NetworkPlan, []string, error) {
	fleet, err := network.Union(loaded)
	if err != nil {
		return nil, nil, &exitError{code: 3, err: err}
	}

	warnings := append([]string{}, fleet.Warnings...)

	hasGrouped := false
	hasOVN := false
	for _, vs := range fleet.VSwitches {
		if vs.Group != "" {
			hasGrouped = true
		}
		if vs.EffectiveType() == "ovn" {
			hasOVN = true
		}
	}

	// Rule-count guard (§3.2): >256 reject rules per vswitch warns, proceeds.
	for _, acl := range network.Compile(fleet) {
		if n := network.RejectRuleCount(acl); n > 256 {
			warnings = append(warnings, fmt.Sprintf("ACL %q has %d reject rules (>256); consider fewer inter-group carve-outs", acl.Name, n))
		}
	}

	live := &plan.NetworkLiveState{
		Networks: map[string]*provider.Network{},
		ACLs:     map[string]*provider.NetworkACL{},
	}

	// Fetch live networks/ACLs whenever the service exposes the network
	// surface: even a vswitch-less fleet needs the live network set so the
	// NIC unknown-parent check (§4) doesn't misfire on the stock lxdbr0 / incusbr0.
	if svc != nil {
		if hasOVN && !svc.HasExtension("network_ovn") {
			return nil, nil, &exitError{code: 4, err: fmt.Errorf("server lacks the network_ovn extension; OVN virtual switches cannot be created")}
		}
		if hasGrouped && !svc.HasExtension("network_acl") {
			return nil, nil, &exitError{code: 4, err: fmt.Errorf("server lacks the network_acl extension; grouped vswitches cannot be policy-managed (needs provider with network ACL support)")}
		}
		// Live-state listing failures are fatal (exit 4): planning against
		// an empty live set would propose create steps for existing objects
		// and silently skip the adoption-refusal/foreign-ACL checks (§9).
		nets, err := svc.GetNetworks(ctx)
		if err != nil {
			return nil, nil, &exitError{code: 4, err: fmt.Errorf("listing provider networks: %w", err)}
		}
		for i := range nets {
			live.Networks[nets[i].Name] = &nets[i]
		}
		// The ACL listing is only meaningful (and only safe to call) when
		// the server actually has the network_acl extension; otherwise the
		// endpoint 404s and would turn every vswitch-less plan into exit 4.
		if svc.HasExtension("network_acl") {
			acls, err := svc.GetNetworkACLs(ctx)
			if err != nil {
				return nil, nil, &exitError{code: 4, err: fmt.Errorf("listing provider network ACLs: %w", err)}
			}
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
