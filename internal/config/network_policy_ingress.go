package config

import (
	"fmt"
	"net/netip"
	"slices"
	"strconv"
	"strings"
)

// normalizeIngressAllow canonicalizes and validates network_policy.ingress_allow.
// The normalized form is what the resolved CUE schema and the fleet compiler
// consume: canonical IPv4 masked CIDR, materialized protocol, and sorted,
// deduplicated port tokens.
func normalizeIngressAllow(p *NetworkPolicy) error {
	seen := make(map[string]bool, len(p.IngressAllow))
	for i := range p.IngressAllow {
		rule := &p.IngressAllow[i]

		rule.To = strings.TrimSpace(rule.To)
		if rule.To == "" {
			return fmt.Errorf("network_policy.ingress_allow[%d]: to is required", i)
		}

		source, err := normalizeIngressSource(rule.Source)
		if err != nil {
			return fmt.Errorf("network_policy.ingress_allow[%d]: %w", i, err)
		}
		rule.Source = source

		protocol := strings.ToLower(strings.TrimSpace(rule.Protocol))
		if protocol == "" {
			protocol = "tcp"
		}
		if protocol != "tcp" && protocol != "udp" {
			return fmt.Errorf("network_policy.ingress_allow[%d]: protocol must be tcp or udp, got %q", i, rule.Protocol)
		}
		rule.Protocol = protocol

		ports, err := normalizeACLPorts(rule.Ports)
		if err != nil {
			return fmt.Errorf("network_policy.ingress_allow[%d]: %w", i, err)
		}
		rule.Ports = ports

		key := rule.To + "\x00" + rule.Source + "\x00" + rule.Protocol + "\x00" + strings.Join(rule.Ports, ",")
		if seen[key] {
			return fmt.Errorf("network_policy.ingress_allow[%d]: duplicate rule for group %q from %s (%s, ports %q)",
				i, rule.To, rule.Source, rule.Protocol, strings.Join(rule.Ports, ","))
		}
		seen[key] = true
	}
	return nil
}

// normalizeIngressSource accepts only an explicit IPv4 CIDR. Bare addresses,
// hostnames, zones, 4-in-6 literals, and IPv6 prefixes are rejected.
func normalizeIngressSource(raw string) (string, error) {
	source := strings.TrimSpace(raw)
	if source == "" {
		return "", fmt.Errorf("source is required")
	}

	prefix, err := netip.ParsePrefix(source)
	if err != nil {
		if addr, addrErr := netip.ParseAddr(source); addrErr == nil {
			return "", fmt.Errorf("source %q must be a CIDR prefix, not a bare address (use %s/32)", raw, addr)
		}
		return "", fmt.Errorf("source %q is not a valid CIDR: %w", raw, err)
	}

	addr := prefix.Addr()
	if addr.Is4In6() {
		return "", fmt.Errorf("source %q: 4-in-6 source addresses are not supported", raw)
	}
	if !addr.Is4() {
		return "", fmt.Errorf("source %q: only IPv4 sources are supported in v1", raw)
	}
	return prefix.Masked().String(), nil
}

type aclPortRange struct {
	start uint16
	end   uint16
	token string
}

// normalizeACLPorts flattens comma lists, canonicalizes single ports and
// ranges, rejects invalid/zero/reversed ranges, and returns a sorted,
// deduplicated token list (LXD ACL destination_port syntax).
func normalizeACLPorts(raw []string) ([]string, error) {
	if len(raw) == 0 {
		return nil, nil
	}

	seen := make(map[string]bool)
	ranges := make([]aclPortRange, 0, len(raw))
	for _, item := range raw {
		for token := range strings.SplitSeq(item, ",") {
			token = strings.TrimSpace(token)
			if token == "" {
				return nil, fmt.Errorf("ports: empty port token")
			}

			startStr, endStr, hasRange := strings.Cut(token, "-")
			start, err := strconv.ParseUint(strings.TrimSpace(startStr), 10, 16)
			if err != nil || start < 1 {
				return nil, fmt.Errorf("ports: invalid port %q (must be 1-65535)", token)
			}
			end := start
			if hasRange {
				end, err = strconv.ParseUint(strings.TrimSpace(endStr), 10, 16)
				if err != nil || end < 1 {
					return nil, fmt.Errorf("ports: invalid port range %q", token)
				}
				if start >= end {
					return nil, fmt.Errorf("ports: range %q must have start < end (use a single port for equal endpoints)", token)
				}
			}

			canonical := strconv.Itoa(int(start))
			if hasRange {
				canonical += "-" + strconv.Itoa(int(end))
			}
			if seen[canonical] {
				continue
			}
			seen[canonical] = true
			ranges = append(ranges, aclPortRange{start: uint16(start), end: uint16(end), token: canonical})
		}
	}

	slices.SortFunc(ranges, func(a, b aclPortRange) int {
		if a.start != b.start {
			return int(a.start) - int(b.start)
		}
		if a.end != b.end {
			return int(a.end) - int(b.end)
		}
		return strings.Compare(a.token, b.token)
	})

	out := make([]string, len(ranges))
	for i := range ranges {
		out[i] = ranges[i].token
	}
	return out, nil
}
