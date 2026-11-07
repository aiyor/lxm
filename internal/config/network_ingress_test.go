package config_test

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/aiyor/lxm/internal/config"
)

func ingressManifest(policy string) string {
	return `schema: lxm/config/v2
name: lab-net
image: ubuntu:24.04
vswitches:
  - name: labbr0
    ipv4: 10.208.0.1/24
    group: lab1
network_policy:
  allow: []
` + policy
}

func TestLoadConfig_IngressAllow_Normalization(t *testing.T) {
	path := writeConfigFile(t, "lab.yaml", ingressManifest(`  ingress_allow:
    - to: lab1
      source: 100.99.45.41/32
      ports: ["3000-3999", "22", "22", "022", "80, 80"]
    - to: lab1
      source: 10.20.5.9/24
      protocol: udp
      ports: ["53"]
`))
	conf, err := config.LoadConfig(path)
	if err != nil {
		t.Fatalf("LoadConfig error: %v", err)
	}
	rules := conf.NetworkPolicy.IngressAllow
	if len(rules) != 2 {
		t.Fatalf("expected 2 ingress rules, got %+v", rules)
	}
	if rules[0].Source != "100.99.45.41/32" || rules[0].Protocol != "tcp" {
		t.Fatalf("unexpected first rule: %+v", rules[0])
	}
	if got := strings.Join(rules[0].Ports, ","); got != "22,80,3000-3999" {
		t.Fatalf("ports = %q, want canonical sorted/deduped", got)
	}
	if rules[1].Source != "10.20.5.0/24" || rules[1].Protocol != "udp" {
		t.Fatalf("unexpected second rule: %+v", rules[1])
	}
}

func ingressManifestV1(policy string) string {
	// The v1 surface skips the CUE authoring gate, exercising the Go-side
	// canonicalizer directly.
	return strings.Replace(ingressManifest(policy), "lxm/config/v2", "lxm/config/v1", 1)
}

func TestLoadConfig_IngressAllow_Errors(t *testing.T) {
	tests := []struct {
		name   string
		policy string
		want   string
	}{
		{
			name:   "bare address",
			policy: "  ingress_allow: [{to: lab1, source: 100.99.45.41}]\n",
			want:   "must be a CIDR prefix",
		},
		{
			name:   "hostname",
			policy: "  ingress_allow: [{to: lab1, source: client.example.com/32}]\n",
			want:   "is not a valid CIDR",
		},
		{
			name:   "ipv6",
			policy: "  ingress_allow: [{to: lab1, source: \"2001:db8::/32\"}]\n",
			want:   "only IPv4 sources are supported",
		},
		{
			name:   "bare ipv6",
			policy: "  ingress_allow: [{to: lab1, source: \"2001:db8::1\"}]\n",
			want:   "only IPv4 sources are supported",
		},
		{
			name:   "bad protocol",
			policy: "  ingress_allow: [{to: lab1, source: 100.99.45.41/32, protocol: sctp}]\n",
			want:   "protocol must be tcp or udp",
		},
		{
			name:   "port zero",
			policy: "  ingress_allow: [{to: lab1, source: 100.99.45.41/32, ports: [\"0\"]}]\n",
			want:   "invalid port",
		},
		{
			name:   "port too high",
			policy: "  ingress_allow: [{to: lab1, source: 100.99.45.41/32, ports: [\"65536\"]}]\n",
			want:   "invalid port",
		},
		{
			name:   "reversed range",
			policy: "  ingress_allow: [{to: lab1, source: 100.99.45.41/32, ports: [\"3000-2000\"]}]\n",
			want:   "must have start < end",
		},
		{
			name:   "single-port range",
			policy: "  ingress_allow: [{to: lab1, source: 100.99.45.41/32, ports: [\"22-22\"]}]\n",
			want:   "must have start < end",
		},
		{
			name:   "empty token",
			policy: "  ingress_allow: [{to: lab1, source: 100.99.45.41/32, ports: [\"22,\"]}]\n",
			want:   "empty port token",
		},
		{
			name:   "missing to",
			policy: "  ingress_allow: [{to: \"\", source: 100.99.45.41/32}]\n",
			want:   "to is required",
		},
		{
			name:   "missing source",
			policy: "  ingress_allow: [{to: lab1, source: \"\"}]\n",
			want:   "source is required",
		},
		{
			name: "duplicate rule",
			policy: `  ingress_allow:
    - {to: lab1, source: 100.99.45.41/32, ports: ["22"]}
    - {to: lab1, source: 100.99.45.41/32, ports: ["022"]}
`,
			want: "duplicate rule",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path := writeConfigFile(t, "lab.yaml", ingressManifest(tt.policy))
			_, err := config.LoadConfig(path)
			if err == nil {
				t.Fatalf("expected error containing %q", tt.want)
			}
			if tt.want != "" && !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("error = %q, want containing %q", err, tt.want)
			}
		})
	}
}

func TestConfigValidate_HostAccessPolicies(t *testing.T) {
	tests := []struct {
		name string
		vs   string
		want string
	}{
		{
			name: "valid bridge group",
			vs:   "{name: labbr0, ipv4: 10.208.0.1/24, group: lab1, host_access: true}",
		},
		{
			name: "ungrouped",
			vs:   "{name: labbr0, ipv4: 10.208.0.1/24, host_access: true}",
			want: "requires a group",
		},
		{
			name: "ovn",
			vs:   "{name: labbr0, type: ovn, parent: lxdbr0, ipv4: 10.208.0.1/24, group: lab1, host_access: true}",
			want: "not supported on type: ovn",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			manifest := `schema: lxm/config/v2
name: lab-net
image: ubuntu:24.04
vswitches:
  - ` + tt.vs + `
`
			path := writeConfigFile(t, "lab.yaml", manifest)
			conf, err := config.LoadConfig(path)
			if err == nil {
				err = conf.Validate(filepath.Dir(path))
			}
			if tt.want == "" {
				if err != nil {
					t.Fatalf("expected valid host_access, got: %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("error = %v, want containing %q", err, tt.want)
			}
		})
	}
}

func TestLoadConfig_IngressAllow_V1Normalization(t *testing.T) {
	path := writeConfigFile(t, "lab-v1.yaml", ingressManifestV1(`  ingress_allow:
    - to: lab1
      source: 10.20.5.9/24
      protocol: " UDP "
      ports: ["053", "22", "22"]
`))
	conf, err := config.LoadConfig(path)
	if err != nil {
		t.Fatalf("LoadConfig error: %v", err)
	}
	rule := conf.NetworkPolicy.IngressAllow[0]
	if rule.Source != "10.20.5.0/24" || rule.Protocol != "udp" {
		t.Fatalf("unexpected v1-normalized rule: %+v", rule)
	}
	if got := strings.Join(rule.Ports, ","); got != "22,53" {
		t.Fatalf("ports = %q, want 22,53", got)
	}
}
