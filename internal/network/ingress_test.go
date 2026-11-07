package network

import (
	"reflect"
	"strings"
	"testing"

	"github.com/aiyor/lxm/internal/config"
)

func ingressFleet(t *testing.T, configs ...*config.Config) *Fleet {
	t.Helper()
	f, err := Union(configs)
	if err != nil {
		t.Fatalf("Union: %v", err)
	}
	return f
}

func findCompiledACL(t *testing.T, compiled []*CompiledACL, name string) *CompiledACL {
	t.Helper()
	for _, acl := range compiled {
		if acl.Name == name {
			return acl
		}
	}
	t.Fatalf("compiled ACL %q not found", name)
	return nil
}

func hasRule(acl *CompiledACL, want Rule) bool {
	for _, r := range acl.Rules {
		if r == want {
			return true
		}
	}
	return false
}

func TestCompile_HostAccessAndIngressAllow(t *testing.T) {
	conf := &config.Config{
		Schema: "lxm/config/v2",
		VSwitches: []config.VSwitchConfig{
			{Name: "labbr0", IPv4: "10.208.0.1/24", Group: "lab1", HostAccess: true},
			{Name: "otherbr0", IPv4: "10.209.0.1/24", Group: "lab2"},
		},
		NetworkPolicy: &config.NetworkPolicy{
			Allow: []config.NetworkPolicyRule{{From: "lab1", To: "lab2", Direction: "both"}},
			IngressAllow: []config.IngressAllowRule{
				{To: "lab1", Source: "100.99.45.41/32", Protocol: "tcp", Ports: []string{"22", "3000-3999"}},
				{To: "lab1", Source: "100.99.45.42/32", Protocol: "udp"},
			},
		},
	}
	f := ingressFleet(t, conf)
	compiled := Compile(f)

	lab := findCompiledACL(t, compiled, "lxm-labbr0")
	wantHost := Rule{
		Direction:   "ingress",
		Action:      "allow",
		Source:      "10.208.0.1/32",
		Destination: "10.208.0.0/24",
	}
	if !hasRule(lab, wantHost) {
		t.Fatalf("missing host_access rule %+v in %+v", wantHost, lab.Rules)
	}
	wantTCP := Rule{
		Direction:       "ingress",
		Action:          "allow",
		Source:          "100.99.45.41/32",
		Destination:     "10.208.0.0/24",
		Protocol:        "tcp",
		DestinationPort: "22,3000-3999",
	}
	if !hasRule(lab, wantTCP) {
		t.Fatalf("missing ingress_allow tcp rule %+v in %+v", wantTCP, lab.Rules)
	}
	wantUDP := Rule{Direction: "ingress", Action: "allow", Source: "100.99.45.42/32", Destination: "10.208.0.0/24", Protocol: "udp"}
	if !hasRule(lab, wantUDP) {
		t.Fatalf("missing ingress_allow udp all-ports rule %+v in %+v", wantUDP, lab.Rules)
	}

	other := findCompiledACL(t, compiled, "lxm-otherbr0")
	for _, r := range other.Rules {
		if strings.HasPrefix(r.Source, "100.99.45.") {
			t.Fatalf("ingress grant for lab1 must not be compiled into lab2 ACL: %+v", r)
		}
	}
}

func TestCompile_NoNewRulesWhenFieldsOmitted(t *testing.T) {
	conf := &config.Config{
		Schema: "lxm/config/v2",
		VSwitches: []config.VSwitchConfig{
			{Name: "br0", IPv4: "10.30.0.1/24", Group: "g1"},
		},
	}
	f := ingressFleet(t, conf)
	acl := findCompiledACL(t, Compile(f), "lxm-br0")
	for _, r := range acl.Rules {
		if r.Direction == "ingress" {
			t.Fatalf("bridge ACL must have no ingress rules when host_access/ingress_allow are omitted, got %+v", acl.Rules)
		}
	}
}

func TestCompile_IngressAllowOnOVN(t *testing.T) {
	conf := &config.Config{
		Schema: "lxm/config/v2",
		VSwitches: []config.VSwitchConfig{
			{Name: "ovnbr0", Type: "ovn", Parent: "lxdbr0", IPv4: "10.60.0.1/24", Group: "lab1"},
		},
		NetworkPolicy: &config.NetworkPolicy{
			IngressAllow: []config.IngressAllowRule{
				{To: "lab1", Source: "100.99.45.41/32", Protocol: "tcp", Ports: []string{"443"}},
			},
		},
	}
	f := ingressFleet(t, conf)
	acl := findCompiledACL(t, Compile(f), "lxm-ovnbr0")
	want := Rule{Direction: "ingress", Action: "allow", Source: "100.99.45.41/32", Destination: "10.60.0.0/24", Protocol: "tcp", DestinationPort: "443"}
	if !hasRule(acl, want) {
		t.Fatalf("missing OVN ingress_allow rule %+v in %+v", want, acl.Rules)
	}
}

func TestUnion_IngressAllowValidation(t *testing.T) {
	base := func(policy *config.NetworkPolicy) *config.Config {
		return &config.Config{
			Schema: "lxm/config/v2",
			VSwitches: []config.VSwitchConfig{
				{Name: "labbr0", IPv4: "10.208.0.1/24", Group: "lab1"},
			},
			NetworkPolicy: policy,
		}
	}

	t.Run("unknown target group", func(t *testing.T) {
		_, err := Union([]*config.Config{base(&config.NetworkPolicy{
			IngressAllow: []config.IngressAllowRule{{To: "nope", Source: "100.99.45.41/32", Protocol: "tcp"}},
		})})
		if err == nil || !strings.Contains(err.Error(), "has no vswitches assigned") {
			t.Fatalf("unexpected error: %v", err)
		}
	})

	t.Run("source overlaps target subnet", func(t *testing.T) {
		_, err := Union([]*config.Config{base(&config.NetworkPolicy{
			IngressAllow: []config.IngressAllowRule{{To: "lab1", Source: "10.208.0.5/32", Protocol: "tcp"}},
		})})
		if err == nil || !strings.Contains(err.Error(), "overlaps vswitch") {
			t.Fatalf("unexpected error: %v", err)
		}
	})

	t.Run("wildcard warns but applies", func(t *testing.T) {
		f, err := Union([]*config.Config{base(&config.NetworkPolicy{
			IngressAllow: []config.IngressAllowRule{{To: "lab1", Source: "0.0.0.0/0", Protocol: "tcp"}},
		})})
		if err != nil {
			t.Fatalf("wildcard source must warn, not fail: %v", err)
		}
		found := false
		for _, w := range f.Warnings {
			if strings.Contains(w, "wildcard source") {
				found = true
			}
		}
		if !found {
			t.Fatalf("expected wildcard warning, got %v", f.Warnings)
		}
	})

	t.Run("invalid source", func(t *testing.T) {
		_, err := Union([]*config.Config{base(&config.NetworkPolicy{
			IngressAllow: []config.IngressAllowRule{{To: "lab1", Source: "not-a-cidr", Protocol: "tcp"}},
		})})
		if err == nil || !strings.Contains(err.Error(), "not a valid CIDR") {
			t.Fatalf("unexpected error: %v", err)
		}
	})

	t.Run("identical rules across files dedupe", func(t *testing.T) {
		rule := config.IngressAllowRule{To: "lab1", Source: "100.99.45.41/32", Protocol: "tcp", Ports: []string{"22"}}
		f, err := Union([]*config.Config{
			base(&config.NetworkPolicy{IngressAllow: []config.IngressAllowRule{rule}}),
			base(&config.NetworkPolicy{IngressAllow: []config.IngressAllowRule{rule}}),
		})
		if err != nil {
			t.Fatalf("Union: %v", err)
		}
		if len(f.IngressAllow) != 1 {
			t.Fatalf("expected 1 deduped ingress rule, got %+v", f.IngressAllow)
		}
	})
}

func TestUnion_HostAccessValidation(t *testing.T) {
	t.Run("ungrouped", func(t *testing.T) {
		_, err := Union([]*config.Config{{
			Schema:    "lxm/config/v2",
			VSwitches: []config.VSwitchConfig{{Name: "br0", IPv4: "10.30.0.1/24", HostAccess: true}},
		}})
		if err == nil || !strings.Contains(err.Error(), "requires a group") {
			t.Fatalf("unexpected error: %v", err)
		}
	})
	t.Run("ovn", func(t *testing.T) {
		_, err := Union([]*config.Config{{
			Schema: "lxm/config/v2",
			VSwitches: []config.VSwitchConfig{{
				Name: "ovn0", Type: "ovn", Parent: "lxdbr0", IPv4: "10.60.0.1/24", Group: "lab1", HostAccess: true,
			}},
		}})
		if err == nil || !strings.Contains(err.Error(), "not supported on type: ovn") {
			t.Fatalf("unexpected error: %v", err)
		}
	})
}

func TestUnion_DirectConfigIngressValidated(t *testing.T) {
	base := func(rules []config.IngressAllowRule) *config.Config {
		return &config.Config{
			Schema: "lxm/config/v2",
			VSwitches: []config.VSwitchConfig{
				{Name: "labbr0", IPv4: "10.208.0.1/24", Group: "lab1"},
			},
			NetworkPolicy: &config.NetworkPolicy{IngressAllow: rules},
		}
	}
	tests := []struct {
		name  string
		rules []config.IngressAllowRule
		want  string
	}{
		{
			name:  "invalid source",
			rules: []config.IngressAllowRule{{To: "lab1", Source: "not-a-cidr"}},
			want:  "not a valid CIDR",
		},
		{
			name:  "invalid protocol",
			rules: []config.IngressAllowRule{{To: "lab1", Source: "100.99.45.41/32", Protocol: "SCTP"}},
			want:  "protocol must be tcp or udp",
		},
		{
			name:  "invalid ports",
			rules: []config.IngressAllowRule{{To: "lab1", Source: "100.99.45.41/32", Ports: []string{"abc"}}},
			want:  "invalid port",
		},
		{
			name: "duplicate normalized rules",
			rules: []config.IngressAllowRule{
				{To: "lab1", Source: "100.99.45.41/32", Protocol: "tcp", Ports: []string{"22"}},
				{To: "lab1", Source: "100.99.45.41/32", Protocol: "tcp", Ports: []string{"022"}},
			},
			want: "duplicate rule",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := Union([]*config.Config{base(tt.rules)})
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("error = %v, want containing %q", err, tt.want)
			}
		})
	}
}

func TestUnion_IngressAllowManagedPeerWarning(t *testing.T) {
	conf := &config.Config{
		Schema: "lxm/config/v2",
		VSwitches: []config.VSwitchConfig{
			{Name: "labbr0", IPv4: "10.208.0.1/24", Group: "lab1"},
			{Name: "peerbr0", IPv4: "10.209.0.1/24", Group: "lab2"},
		},
		NetworkPolicy: &config.NetworkPolicy{
			IngressAllow: []config.IngressAllowRule{
				{To: "lab1", Source: "10.209.0.0/24", Protocol: "tcp"},
			},
		},
	}
	f, err := Union([]*config.Config{conf})
	if err != nil {
		t.Fatalf("Union: %v", err)
	}
	found := false
	for _, w := range f.Warnings {
		if strings.Contains(w, "overlaps managed vswitch") && strings.Contains(w, "peerbr0") {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected managed-peer overlap warning, got %v", f.Warnings)
	}
}

func TestCompile_HostAccessAndIngressAllow_MultipleVSwitches(t *testing.T) {
	conf := &config.Config{
		Schema: "lxm/config/v2",
		VSwitches: []config.VSwitchConfig{
			{Name: "br-a", IPv4: "10.30.0.1/24", Group: "lab", HostAccess: true},
			{Name: "br-b", IPv4: "10.31.0.1/24", Group: "lab", HostAccess: true},
		},
		NetworkPolicy: &config.NetworkPolicy{
			IngressAllow: []config.IngressAllowRule{
				{To: "lab", Source: "100.99.45.41/32", Protocol: "tcp", Ports: []string{"22"}},
			},
		},
	}
	f := ingressFleet(t, conf)
	compiled := Compile(f)
	for _, tc := range []struct {
		name    string
		gateway string
		subnet  string
	}{
		{name: "lxm-br-a", gateway: "10.30.0.1/32", subnet: "10.30.0.0/24"},
		{name: "lxm-br-b", gateway: "10.31.0.1/32", subnet: "10.31.0.0/24"},
	} {
		acl := findCompiledACL(t, compiled, tc.name)
		wantHost := Rule{Direction: "ingress", Action: "allow", Source: tc.gateway, Destination: tc.subnet}
		if !hasRule(acl, wantHost) {
			t.Fatalf("%s: missing host_access rule %+v in %+v", tc.name, wantHost, acl.Rules)
		}
		wantGrant := Rule{
			Direction:       "ingress",
			Action:          "allow",
			Source:          "100.99.45.41/32",
			Destination:     tc.subnet,
			Protocol:        "tcp",
			DestinationPort: "22",
		}
		if !hasRule(acl, wantGrant) {
			t.Fatalf("%s: missing ingress_allow rule %+v in %+v", tc.name, wantGrant, acl.Rules)
		}
	}
}

func TestCompile_IngressAllowWildcardRule(t *testing.T) {
	conf := &config.Config{
		Schema: "lxm/config/v2",
		VSwitches: []config.VSwitchConfig{
			{Name: "labbr0", IPv4: "10.208.0.1/24", Group: "lab1"},
		},
		NetworkPolicy: &config.NetworkPolicy{
			IngressAllow: []config.IngressAllowRule{
				{To: "lab1", Source: "0.0.0.0/0", Protocol: "tcp", Ports: []string{"443"}},
			},
		},
	}
	f := ingressFleet(t, conf)
	acl := findCompiledACL(t, Compile(f), "lxm-labbr0")
	want := Rule{Direction: "ingress", Action: "allow", Source: "0.0.0.0/0", Destination: "10.208.0.0/24", Protocol: "tcp", DestinationPort: "443"}
	if !hasRule(acl, want) {
		t.Fatalf("missing wildcard ingress rule %+v in %+v", want, acl.Rules)
	}
}

func TestCompile_OmittedNewFieldsEquivalentToExplicitFalse(t *testing.T) {
	omitted := []*config.Config{{
		Schema: "lxm/config/v2",
		VSwitches: []config.VSwitchConfig{
			{Name: "br0", IPv4: "10.30.0.1/24", Group: "g1"},
		},
		NetworkPolicy: &config.NetworkPolicy{Allow: []config.NetworkPolicyRule{}},
	}}
	explicit := []*config.Config{{
		Schema: "lxm/config/v2",
		VSwitches: []config.VSwitchConfig{
			{Name: "br0", IPv4: "10.30.0.1/24", Group: "g1", HostAccess: false},
		},
		NetworkPolicy: &config.NetworkPolicy{
			Allow:        []config.NetworkPolicyRule{},
			IngressAllow: []config.IngressAllowRule{},
		},
	}}
	gotOmitted := Compile(ingressFleet(t, omitted...))
	gotExplicit := Compile(ingressFleet(t, explicit...))
	if !reflect.DeepEqual(gotOmitted, gotExplicit) {
		t.Fatalf("omitted new fields must compile identically to explicit false/empty:\nomitted=%+v\nexplicit=%+v", gotOmitted, gotExplicit)
	}
}

func TestUnion_DirectConfigIngressNormalized(t *testing.T) {
	conf := &config.Config{
		Schema: "lxm/config/v2",
		VSwitches: []config.VSwitchConfig{
			{Name: "labbr0", IPv4: "10.208.0.1/24", Group: "lab1"},
		},
		NetworkPolicy: &config.NetworkPolicy{
			IngressAllow: []config.IngressAllowRule{
				{To: " lab1 ", Source: "100.99.45.41/24", Ports: []string{"3000-3999", "22", "022"}},
			},
		},
	}
	f, err := Union([]*config.Config{conf})
	if err != nil {
		t.Fatalf("Union: %v", err)
	}
	if len(f.IngressAllow) != 1 {
		t.Fatalf("expected 1 ingress rule, got %+v", f.IngressAllow)
	}
	rule := f.IngressAllow[0]
	if rule.To != "lab1" || rule.Source != "100.99.45.0/24" || rule.Protocol != "tcp" {
		t.Fatalf("rule not normalized: %+v", rule)
	}
	if got := strings.Join(rule.Ports, ","); got != "22,3000-3999" {
		t.Fatalf("ports = %q, want normalized", got)
	}
}
