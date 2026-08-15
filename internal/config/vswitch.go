package config

import (
	"fmt"
	"net"
)

// validateVSwitches enforces the Go-side vswitch constraints that CUE cannot
// express: CIDR arithmetic (first usable host, /8–/29 mask bounds), duplicate
// names, overlapping subnets, internet-without-group, and valid internal CIDRs.
func (conf *Config) validateVSwitches() error {
	seenNames := make(map[string]bool)
	var subnets []*net.IPNet
	for i, vs := range conf.VSwitches {
		if vs.Name == "" {
			return fmt.Errorf("vswitches[%d]: name is required", i)
		}
		if seenNames[vs.Name] {
			// Redeclarations are resolved at the fleet union (§5/§7.2), not
			// per-tree: identical duplicates deduplicate silently and
			// conflicting ones fail with both file paths in the message.
			// Skipping here lets that resolution happen with attribution.
			continue
		}
		seenNames[vs.Name] = true

		if err := validateVSwitchIPv4(vs); err != nil {
			return err
		}

		if vs.Group == "" && vs.Internet != nil && !*vs.Internet {
			return fmt.Errorf("vswitch %q: internet: false requires a group (ungrouped vswitches are not policy-managed)", vs.Name)
		}

		_, ipnet, err := net.ParseCIDR(vs.IPv4)
		if err != nil {
			return fmt.Errorf("vswitch %q: invalid ipv4 %q: %w", vs.Name, vs.IPv4, err)
		}
		for _, other := range subnets {
			if cidrsOverlap(ipnet, other) {
				return fmt.Errorf("vswitch %q: subnet %s overlaps vswitch subnet %s", vs.Name, ipnet.String(), other.String())
			}
		}
		subnets = append(subnets, ipnet)
	}

	if conf.NetworkPolicy != nil {
		for i, cidr := range conf.NetworkPolicy.InternalCIDRs {
			// Duplicates are deduplicated silently (§2.2) at the fleet union.
			if _, _, err := net.ParseCIDR(cidr); err != nil {
				return fmt.Errorf("network_policy.internal_cidrs[%d]: invalid CIDR %q: %w", i, cidr, err)
			}
		}
	}

	return nil
}

func validateVSwitchIPv4(vs VSwitchConfig) error {
	ip, ipnet, err := net.ParseCIDR(vs.IPv4)
	if err != nil {
		return fmt.Errorf("vswitch %q: invalid ipv4 %q: %w", vs.Name, vs.IPv4, err)
	}
	if ip.To4() == nil {
		return fmt.Errorf("vswitch %q: ipv4 %q is not an IPv4 address", vs.Name, vs.IPv4)
	}
	ones, _ := ipnet.Mask.Size()
	if ones < 8 || ones > 29 {
		return fmt.Errorf("vswitch %q: prefix length /%d out of range [8,29]", vs.Name, ones)
	}

	networkIP := ipnet.IP.To4()
	expected := make(net.IP, 4)
	copy(expected, networkIP)
	incrementIPv4(expected)
	if !ip.Equal(expected) {
		return fmt.Errorf("vswitch %q: ipv4 %q is not the first usable host of %s (gateway must be network .1)", vs.Name, vs.IPv4, ipnet.String())
	}
	return nil
}

// incrementIPv4 increments a big-endian IPv4 address in place by one.
func incrementIPv4(ip net.IP) {
	for i := len(ip) - 1; i >= 0; i-- {
		ip[i]++
		if ip[i] != 0 {
			break
		}
	}
}

// cidrsOverlap reports whether two IPv4 networks share any address.
func cidrsOverlap(a, b *net.IPNet) bool {
	return a.Contains(b.IP) || b.Contains(a.IP)
}
