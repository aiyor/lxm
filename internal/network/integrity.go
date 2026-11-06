package network

import (
	"fmt"
	"maps"
	"net"
	"net/netip"
	"slices"
	"strings"

	"github.com/aiyor/lxm/internal/config"
	"github.com/aiyor/lxm/internal/ports"
)

// CheckInstances enforces the instance-NIC integration rules (§4):
//
//  1. NIC-IP-in-subnet membership (exit 3 on violation) — a static IPv4 on a
//     NIC whose parent is a declared vswitch must fall inside that vswitch's
//     subnet.
//  2. Unknown-parent warning — a NIC parent that is neither a live LXD network
//     nor a declared vswitch.
//  3. Multi-NIC group-span warning (R10) — NICs on vswitches from ≥2 distinct
//     groups may bypass network_policy via guest routing.
//  4. NAT+OVN rejection (issue #11) — a published port with nat: true cannot
//     use a connect address whose NIC is attached to an OVN network: the
//     daemon's NAT setup only considers bridged/routed NICs, so the device
//     fails at instance start. The check follows the connect address to its
//     specific NIC, so a bridged NIC on a multi-NIC instance does not trip it.
func CheckInstances(configs []*config.Config, f *Fleet, liveNetworks map[string]bool) ([]string, error) {
	return CheckInstancesWithProvider(configs, f, liveNetworks, "")
}

// CheckInstancesWithProvider is like CheckInstances but defaults NIC parent
// to incusbr0 when the resolved provider is incus. Live network types are not
// available through this entry point (see CheckInstancesWithProviderAndNetworkTypes).
func CheckInstancesWithProvider(configs []*config.Config, f *Fleet, liveNetworks map[string]bool, prov string) ([]string, error) {
	return CheckInstancesWithProviderAndNetworkTypes(configs, f, liveNetworks, nil, prov)
}

// CheckInstancesWithProviderAndNetworkTypes is CheckInstancesWithProvider with
// the live network type catalogue (name -> provider network type, e.g. "ovn",
// "bridge"). It lets NAT validation catch an OVN parent that is not declared in
// vswitches: rather than only trusting the loaded manifest set.
func CheckInstancesWithProviderAndNetworkTypes(configs []*config.Config, f *Fleet, liveNetworks map[string]bool, liveNetworkTypes map[string]string, prov string) ([]string, error) {
	var warnings []string

	for _, conf := range configs {
		if conf == nil || conf.Status == "absent" {
			continue
		}
		groupsSeen := make(map[string]bool)
		// Static IPv4 -> every parent network that NIC is attached to, with the
		// OVN classification needed by the NAT follow-up below.
		type nicParent struct {
			parent string
			ovn    bool
		}
		ipv4Parents := make(map[netip.Addr][]nicParent)

		for _, nic := range conf.Networks {
			name := nic.Name
			if name == "" {
				name = "eth0"
			}
			parent := nic.Parent
			if parent == "" {
				switch {
				case conf.Provider == "incus":
					parent = "incusbr0"
				case conf.Provider == "lxd":
					parent = "lxdbr0"
				case prov == "incus":
					parent = "incusbr0"
				default:
					parent = "lxdbr0"
				}
			}

			vs, declared := f.ByName[parent]

			if declared && vs != nil && vs.Status == "absent" {
				return nil, fmt.Errorf("instance %q: NIC %q parent vswitch %q is declared with status: absent; cannot delete a vswitch currently attached to active instances",
					conf.Name, name, parent)
			}

			isOVN := false
			switch {
			case declared && vs != nil:
				isOVN = vs.EffectiveType() == "ovn"
			case liveNetworkTypes != nil:
				isOVN = liveNetworkTypes[parent] == "ovn"
			}

			if addr, err := netip.ParseAddr(strings.TrimSpace(nic.IPv4)); err == nil && addr.Is4() {
				ipv4Parents[addr] = append(ipv4Parents[addr], nicParent{parent: parent, ovn: isOVN})
			}

			if nic.IPv4 != "" && declared && vs != nil && vs.Subnet != nil {
				ip := net.ParseIP(nic.IPv4)
				if ip == nil || !vs.Subnet.Contains(ip) {
					return nil, fmt.Errorf("instance %q: NIC %q static IP %s is outside parent vswitch %q subnet %s",
						conf.Name, name, nic.IPv4, parent, vs.Subnet.String())
				}
			}

			if !declared && !liveNetworks[parent] {
				warnings = append(warnings, fmt.Sprintf("instance %q: NIC parent %q is not a known LXD network or declared vswitch", conf.Name, parent))
			}

			if declared && vs != nil && vs.Group != "" {
				groupsSeen[vs.Group] = true
			}
		}

		// NAT requires a bridged/routed NIC reachable at the connect address.
		for i := range conf.Ports {
			port := &conf.Ports[i]
			if !port.NAT {
				continue
			}
			connect, err := ports.ParseEndpoint(port.Proto, port.Connect)
			if err != nil {
				continue // config validation reports the endpoint error
			}
			for _, parent := range ipv4Parents[connect.Addr] {
				if !parent.ovn {
					continue
				}
				return nil, fmt.Errorf("port %q of instance %q: nat: true cannot use connect address %s on OVN-backed network %q (NAT requires a bridged/routed NIC)",
					port.Name, conf.Name, connect.HostPort(), parent.parent)
			}
		}

		if len(groupsSeen) > 1 {
			groups := slices.Sorted(maps.Keys(groupsSeen))
			warnings = append(warnings, fmt.Sprintf("instance %q NICs span network groups [%s]; guest routing may bypass network_policy",
				conf.Name, strings.Join(groups, ", ")))
		}
	}

	return warnings, nil
}
