package network

import (
	"fmt"
	"net"
	"sort"
	"strings"

	"github.com/aiyor/lxm/internal/config"
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
func CheckInstances(configs []*config.Config, f *Fleet, liveNetworks map[string]bool) ([]string, error) {
	var warnings []string

	for _, conf := range configs {
		if conf == nil || conf.Status == "absent" {
			continue
		}
		groupsSeen := make(map[string]bool)
		for _, nic := range conf.Networks {
			name := nic.Name
			if name == "" {
				name = "eth0"
			}
			parent := nic.Parent
			if parent == "" {
				parent = "lxdbr0"
			}

			vs, declared := f.ByName[parent]

			if nic.IPv4 != "" && declared && vs != nil {
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

		if len(groupsSeen) > 1 {
			groups := make([]string, 0, len(groupsSeen))
			for g := range groupsSeen {
				groups = append(groups, g)
			}
			sort.Strings(groups)
			warnings = append(warnings, fmt.Sprintf("instance %q NICs span network groups [%s]; guest routing may bypass network_policy",
				conf.Name, strings.Join(groups, ", ")))
		}
	}

	return warnings, nil
}
