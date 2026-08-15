// Package network implements the vswitches:/network_policy: fleet model and
// its deterministic compilation into LXD network ACLs.
package network

import (
	"fmt"
	"net"
	"sort"
)

// ParseSubnet parses a vswitch-style "gateway/prefix" string and returns the
// canonical network CIDR (e.g. "10.50.0.1/24" -> "10.50.0.0/24") and its
// *net.IPNet. Only IPv4 is supported in v1.
func ParseSubnet(s string) (string, *net.IPNet, error) {
	ip, ipnet, err := net.ParseCIDR(s)
	if err != nil {
		return "", nil, err
	}
	if ip.To4() == nil {
		return "", nil, fmt.Errorf("%q is not an IPv4 CIDR", s)
	}
	return ipnet.String(), ipnet, nil
}

// ParseCIDR parses a bare CIDR string (network form required for correctness,
// but any address in the block is normalized to the network address). Only
// IPv4 is supported for policy subjects in v1.
func ParseCIDR(s string) (*net.IPNet, error) {
	_, ipnet, err := net.ParseCIDR(s)
	if err != nil {
		return nil, err
	}
	if ipnet.IP.To4() == nil {
		return nil, fmt.Errorf("%q is not an IPv4 CIDR", s)
	}
	return ipnet, nil
}

// parseAnyCIDR parses a CIDR without the IPv4 restriction; used for the
// internal-set canonicalization that must preserve the locked IPv6 defaults.
func parseAnyCIDR(s string) (*net.IPNet, error) {
	_, ipnet, err := net.ParseCIDR(s)
	if err != nil {
		return nil, err
	}
	return ipnet, nil
}

// covers reports whether outer fully contains inner (inner ⊆ outer).
func covers(outer, inner *net.IPNet) bool {
	oOnes, oBits := outer.Mask.Size()
	iOnes, iBits := inner.Mask.Size()
	if oBits != iBits {
		return false
	}
	return oOnes <= iOnes && outer.Contains(inner.IP)
}

// overlaps reports whether two networks share any address.
func overlaps(a, b *net.IPNet) bool {
	return a.Contains(b.IP) || b.Contains(a.IP)
}

// CanonicalizeCIDRs removes duplicate and subsumed CIDRs from a set. A CIDR
// subsumed by another member is dropped regardless of provenance, keeping the
// emitted reject set minimal and deterministic.
func CanonicalizeCIDRs(cidrs []string) ([]string, error) {
	parsed := make([]*net.IPNet, 0, len(cidrs))
	for _, c := range cidrs {
		n, err := parseAnyCIDR(c)
		if err != nil {
			return nil, err
		}
		parsed = append(parsed, n)
	}

	seen := make(map[string]*net.IPNet)
	for _, n := range parsed {
		seen[n.String()] = n
	}
	list := make([]*net.IPNet, 0, len(seen))
	for _, n := range seen {
		list = append(list, n)
	}

	var kept []*net.IPNet
	for _, a := range list {
		subsumed := false
		for _, b := range list {
			if a.String() == b.String() {
				continue
			}
			if covers(b, a) {
				subsumed = true
				break
			}
		}
		if !subsumed {
			kept = append(kept, a)
		}
	}

	out := make([]string, 0, len(kept))
	for _, n := range kept {
		out = append(out, n.String())
	}
	sort.Strings(out)
	return out, nil
}

// SubtractCIDRs performs true CIDR carve-out by prefix decomposition:
// SubtractCIDRs(supernet, carveOuts) replaces the supernet with the maximal
// set of non-overlapping sub-prefixes excluding the carved-out ranges. For
// example 10.0.0.0/8 \ 10.50.0.0/24 produces exactly one sibling per level
// from /9 to /24.
func SubtractCIDRs(supernet *net.IPNet, carveOuts []*net.IPNet) []*net.IPNet {
	ones, bits := supernet.Mask.Size()
	if bits != 32 {
		return []*net.IPNet{supernet}
	}

	relevant := make([]*net.IPNet, 0, len(carveOuts))
	for _, c := range carveOuts {
		if overlaps(supernet, c) {
			relevant = append(relevant, c)
		}
	}
	if len(relevant) == 0 {
		return []*net.IPNet{supernet}
	}

	var out []*net.IPNet
	var walk func(ip net.IP, curOnes int)
	walk = func(ip net.IP, curOnes int) {
		p := &net.IPNet{IP: ip, Mask: net.CIDRMask(curOnes, 32)}
		// p ⊆ some carve-out: entirely excluded.
		for _, c := range relevant {
			if covers(c, p) {
				return
			}
		}
		// p ∩ carve-outs = ∅: emit.
		disjoint := true
		for _, c := range relevant {
			if overlaps(c, p) {
				disjoint = false
				break
			}
		}
		if disjoint {
			// Copy ip: the caller's child slice is reused by setBit for the
			// sibling walk, so the appended prefix must own its address bytes.
			ipCopy := make(net.IP, len(ip))
			copy(ipCopy, ip)
			out = append(out, &net.IPNet{IP: ipCopy, Mask: net.CIDRMask(curOnes, 32)})
			return
		}
		if curOnes >= 32 {
			return
		}
		child := make(net.IP, 4)
		copy(child, ip)
		walk(child, curOnes+1)
		setBit(child, curOnes)
		walk(child, curOnes+1)
	}

	walk(supernet.IP.To4(), ones)
	sort.Slice(out, func(i, j int) bool {
		return out[i].String() < out[j].String()
	})
	return out
}

// setBit sets the bit at index bit (0 = MSB of a 32-bit IPv4) in ip.
func setBit(ip net.IP, bit int) {
	byteIdx := bit / 8
	bitIdx := uint(7 - (bit % 8))
	ip[byteIdx] |= 1 << bitIdx
}
