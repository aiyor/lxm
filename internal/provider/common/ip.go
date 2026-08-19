package common

import (
	"fmt"
	"strings"

	"github.com/aiyor/lxm/internal/provider"
)

// ExtractIPv4 extracts the first non-loopback IPv4 address from instance network state.
func ExtractIPv4(networks map[string]provider.InstanceStateNetwork) (string, error) {
	for name, net := range networks {
		if name == "lo" || strings.HasPrefix(name, "lo:") {
			continue
		}
		for _, addr := range net.Addresses {
			if addr.Family == "inet" && addr.Scope == "global" && addr.Address != "" {
				return addr.Address, nil
			}
		}
	}
	return "", fmt.Errorf("no global IPv4 address found")
}
