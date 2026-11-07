package config_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/aiyor/lxm/internal/config"
)

func writePortsManifest(t *testing.T, name, body string) string {
	t.Helper()
	dir := t.TempDir()
	return writePortsManifestInDir(t, dir, name, body)
}

func writePortsManifestInDir(t *testing.T, dir, name, body string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(body), 0644); err != nil {
		t.Fatalf("writing %s: %v", name, err)
	}
	return path
}

func TestLoadConfig_Ports_DefaultsAndCanonicalization(t *testing.T) {
	manifest := `schema: lxm/config/v2
name: web-a
image: ubuntu:24.04
ports:
  - name: web
    listen: 127.0.0.1:8080
    connect: 127.0.0.1:80
  - name: dns
    listen: "[2001:db8::1]:5353"
    connect: "[2001:db8::2]:53"
    proto: udp
`
	path := writePortsManifest(t, "web.yaml", manifest)
	conf, err := config.LoadConfig(path)
	if err != nil {
		t.Fatalf("LoadConfig error: %v", err)
	}
	if len(conf.Ports) != 2 {
		t.Fatalf("expected 2 ports, got %d", len(conf.Ports))
	}
	web := conf.Ports[0]
	if web.Proto != "tcp" {
		t.Errorf("default proto = %q, want tcp", web.Proto)
	}
	if web.NAT {
		t.Error("default nat = true, want false")
	}
	if web.Listen != "127.0.0.1:8080" || web.Connect != "127.0.0.1:80" {
		t.Errorf("unexpected canonical endpoints: %+v", web)
	}
	dns := conf.Ports[1]
	if dns.Proto != "udp" || dns.Listen != "[2001:db8::1]:5353" || dns.Connect != "[2001:db8::2]:53" {
		t.Errorf("unexpected ipv6 canonical form: %+v", dns)
	}
}

func TestConfigValidate_Ports_ErrorMatrix(t *testing.T) {
	base := `schema: lxm/config/v2
name: c1
image: ubuntu:24.04
`
	tests := []struct {
		name string
		body string
		want string
	}{
		{
			name: "protocol prefix in listen",
			body: base + `ports: [{name: web, listen: "tcp:127.0.0.1:8080", connect: "127.0.0.1:80"}]`,
			want: "do not prefix the protocol",
		},
		{
			name: "missing explicit host",
			body: base + `ports: [{name: web, listen: ":8080", connect: "127.0.0.1:80"}]`,
			want: "invalid value", // rejected by the CUE authoring endpoint regex
		},
		{
			name: "zero port",
			body: base + `ports: [{name: web, listen: "127.0.0.1:0", connect: "127.0.0.1:80"}]`,
			want: "port must be in 1-65535",
		},
		{
			name: "hostname",
			body: base + `ports: [{name: web, listen: "localhost:8080", connect: "127.0.0.1:80"}]`,
			want: "explicit IP:port",
		},
		{
			name: "four-in-six",
			body: base + `ports: [{name: web, listen: "[::ffff:1.2.3.4]:8080", connect: "127.0.0.1:80"}]`,
			want: "4-in-6",
		},
		{
			name: "wildcard connect",
			body: base + `ports: [{name: web, listen: "127.0.0.1:8080", connect: "0.0.0.0:80"}]`,
			want: "wildcard connect is not supported",
		},
		{
			name: "nat loopback listen",
			body: base + `networks: [{name: eth0, parent: lxdbr0, ipv4: 10.0.0.5}]
ports: [{name: web, listen: "127.0.0.1:8080", connect: "10.0.0.5:80", nat: true}]`,
			want: "cannot listen on a loopback address",
		},
		{
			name: "nat wildcard listen",
			body: base + `networks: [{name: eth0, parent: lxdbr0, ipv4: 10.0.0.5}]
ports: [{name: web, listen: "0.0.0.0:8080", connect: "10.0.0.5:80", nat: true}]`,
			want: "explicit, non-wildcard listen",
		},
		{
			name: "nat no static ipv4",
			body: base + `networks: [{name: eth0, parent: lxdbr0}]
ports: [{name: web, listen: "10.0.0.1:8080", connect: "10.0.0.5:80", nat: true}]`,
			want: "requires a static ipv4",
		},
		{
			name: "nat connect mismatch",
			body: base + `networks: [{name: eth0, parent: lxdbr0, ipv4: 10.0.0.5}]
ports: [{name: web, listen: "10.0.0.1:8080", connect: "10.0.0.9:80", nat: true}]`,
			want: "connect must be an explicit declared static ipv4",
		},
		{
			name: "nat ipv6",
			body: base + `networks: [{name: eth0, parent: lxdbr0, ipv4: 10.0.0.5}]
ports: [{name: web, listen: "10.0.0.1:8080", connect: "[2001:db8::1]:80", nat: true}]`,
			want: "supports IPv4 only",
		},
		{
			name: "vm without nat",
			body: `schema: lxm/config/v2
name: vm1
type: virtual-machine
image: ubuntu:24.04
ports: [{name: web, listen: "127.0.0.1:8080", connect: "127.0.0.1:80"}]`,
			want: "virtual machines require nat: true",
		},
		{
			name: "nic device-name collision",
			body: base + `networks: [{name: proxy-web, parent: lxdbr0}]
ports: [{name: web, listen: "127.0.0.1:8080", connect: "127.0.0.1:80"}]`,
			want: "would collide with device",
		},
		{
			name: "same-instance duplicate listener",
			body: base + `ports:
  - {name: web, listen: "127.0.0.1:8080", connect: "127.0.0.1:80"}
  - {name: web2, listen: "127.0.0.1:8080", connect: "127.0.0.1:81"}`,
			want: `duplicates port "web"`,
		},
		{
			name: "same-instance dual-stack wildcard",
			body: base + `ports:
  - {name: web, listen: "127.0.0.1:8080", connect: "127.0.0.1:80"}
  - {name: web2, listen: "0.0.0.0:8080", connect: "127.0.0.1:81"}`,
			want: `duplicates port "web"`,
		},
		{
			name: "trailing hyphen name",
			body: base + `ports: [{name: "web-", listen: "127.0.0.1:8080", connect: "127.0.0.1:80"}]`,
			want: "invalid value",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path := writePortsManifest(t, "ports.yaml", tt.body)
			conf, err := config.LoadConfig(path)
			if err == nil {
				err = conf.Validate(filepath.Dir(path))
			}
			if err == nil {
				t.Fatalf("expected error containing %q, got nil", tt.want)
			}
			if !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("error = %q, want containing %q", err, tt.want)
			}
		})
	}
}

func TestConfigValidate_Ports_ValidNAT(t *testing.T) {
	manifest := `schema: lxm/config/v2
name: vm1
type: virtual-machine
image: ubuntu:24.04
networks:
  - {name: eth0, parent: lxdbr0, ipv4: 10.10.10.31}
ports:
  - {name: https, listen: "10.10.10.1:8443", connect: "10.10.10.31:443", nat: true}
`
	path := writePortsManifest(t, "vm.yaml", manifest)
	conf, err := config.LoadConfig(path)
	if err != nil {
		t.Fatalf("LoadConfig error: %v", err)
	}
	if err := conf.Validate(filepath.Dir(path)); err != nil {
		t.Fatalf("Validate error: %v", err)
	}
}

func TestConfigValidate_BaseFragmentSkipsCrossFieldPortRules(t *testing.T) {
	conf := &config.Config{
		Base: true,
		Ports: []config.PortConfig{{
			Name:    "web",
			Listen:  "127.0.0.1:8080",
			Connect: "127.0.0.1:80",
			Proto:   "tcp",
			NAT:     true, // normally invalid without a static IPv4 and on loopback; base fragments skip cross-field rules
		}},
	}
	if err := conf.Validate(""); err != nil {
		t.Fatalf("base fragment validation should skip cross-field port rules, got: %v", err)
	}
}

func TestLoadConfig_Ports_MergeByNameAndRemove(t *testing.T) {
	dir := t.TempDir()
	writePortsManifestInDir(t, dir, "_base.yaml", `schema: lxm/config/v2
base: true
image: ubuntu:24.04
ports:
  - {name: api, listen: "127.0.0.1:8080", connect: "127.0.0.1:80"}
  - {name: legacy, listen: "127.0.0.1:9090", connect: "127.0.0.1:90"}
`)
	leaf := writePortsManifestInDir(t, dir, "leaf.yaml", `schema: lxm/config/v2
name: leaf
image: ubuntu:24.04
include: [_base.yaml]
ports:
  - {name: api, listen: "127.0.0.1:8090", connect: "127.0.0.1:90"}
remove:
  ports: [legacy]
`)
	conf, err := config.LoadConfig(leaf)
	if err != nil {
		t.Fatalf("LoadConfig error: %v", err)
	}
	if len(conf.Ports) != 1 {
		t.Fatalf("expected 1 merged port, got %d: %+v", len(conf.Ports), conf.Ports)
	}
	if conf.Ports[0].Name != "api" || conf.Ports[0].Listen != "127.0.0.1:8090" {
		t.Fatalf("expected overridden api port, got %+v", conf.Ports[0])
	}
}

func TestLoadConfig_Ports_RemoveUnmatchedRejected(t *testing.T) {
	manifest := `schema: lxm/config/v2
name: leaf
image: ubuntu:24.04
remove:
  ports: [missing]
`
	path := writePortsManifest(t, "leaf.yaml", manifest)
	_, err := config.LoadConfig(path)
	if err == nil || !strings.Contains(err.Error(), `remove.ports: name "missing" matched no port`) {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestLoadConfig_Ports_DuplicateLayerNamesRejected(t *testing.T) {
	dir := t.TempDir()
	writePortsManifestInDir(t, dir, "_base.yaml", `schema: lxm/config/v2
base: true
image: ubuntu:24.04
ports:
  - {name: base, listen: "127.0.0.1:8080", connect: "127.0.0.1:80"}
`)
	leaf := writePortsManifestInDir(t, dir, "leaf.yaml", `schema: lxm/config/v2
name: leaf
image: ubuntu:24.04
include: [_base.yaml]
ports:
  - {name: dup, listen: "127.0.0.1:8081", connect: "127.0.0.1:81"}
  - {name: dup, listen: "127.0.0.1:8082", connect: "127.0.0.1:82"}
`)
	_, err := config.LoadConfig(leaf)
	if err == nil || !strings.Contains(err.Error(), `duplicate port name "dup"`) {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestLoadConfig_Ports_WholeEntryOverrideResetsNAT(t *testing.T) {
	dir := t.TempDir()
	writePortsManifestInDir(t, dir, "_base.yaml", `schema: lxm/config/v2
base: true
image: ubuntu:24.04
ports:
  - {name: api, listen: "10.0.0.1:8080", connect: "10.0.0.5:80", nat: true}
`)
	leaf := writePortsManifestInDir(t, dir, "leaf.yaml", `schema: lxm/config/v2
name: leaf
image: ubuntu:24.04
include: [_base.yaml]
ports:
  - {name: api, listen: "127.0.0.1:8080", connect: "127.0.0.1:80"}
`)
	conf, err := config.LoadConfig(leaf)
	if err != nil {
		t.Fatalf("LoadConfig error: %v", err)
	}
	if len(conf.Ports) != 1 || conf.Ports[0].NAT {
		t.Fatalf("whole-entry override must reset nat to false, got %+v", conf.Ports)
	}
}

func TestValidateFleetPorts(t *testing.T) {
	port := func(name, listen, connect string) config.PortConfig {
		return config.PortConfig{Name: name, Listen: listen, Connect: connect, Proto: "tcp"}
	}
	instance := func(name, file string, ports ...config.PortConfig) *config.Config {
		return &config.Config{Schema: "lxm/config/v2", Name: name, Status: "present", ConfigFile: file, Ports: ports}
	}

	tests := []struct {
		name    string
		configs []*config.Config
		wantErr string
	}{
		{
			name: "duplicate across instances",
			configs: []*config.Config{
				instance("a", "/m/a.yaml", port("web", "127.0.0.1:8080", "127.0.0.1:80")),
				instance("b", "/m/b.yaml", port("web", "127.0.0.1:8080", "127.0.0.1:81")),
			},
			wantErr: `published by both instance "a" (/m/a.yaml) and instance "b" (/m/b.yaml)`,
		},
		{
			name: "wildcard overlaps specific",
			configs: []*config.Config{
				instance("a", "/m/a.yaml", port("web", "0.0.0.0:8080", "127.0.0.1:80")),
				instance("b", "/m/b.yaml", port("web", "127.0.0.1:8080", "127.0.0.1:81")),
			},
			wantErr: "overlaps listener",
		},
		{
			name: "wildcard overlaps other family",
			configs: []*config.Config{
				instance("a", "/m/a.yaml", port("web", "0.0.0.0:8080", "127.0.0.1:80")),
				instance("b", "/m/b.yaml", port("web", "[::1]:8080", "[::1]:81")),
			},
			wantErr: "overlaps listener",
		},
		{
			name: "different protocols do not conflict",
			configs: []*config.Config{
				instance("a", "/m/a.yaml", port("dns", "127.0.0.1:5353", "127.0.0.1:53")),
				instance("b", "/m/b.yaml", config.PortConfig{Name: "dns", Listen: "127.0.0.1:5353", Connect: "127.0.0.1:53", Proto: "udp"}),
			},
		},
		{
			name: "v4 and v6 loopbacks do not conflict",
			configs: []*config.Config{
				instance("a", "/m/a.yaml", port("web", "127.0.0.1:8080", "127.0.0.1:80")),
				instance("b", "/m/b.yaml", port("web", "[::1]:8080", "[::1]:81")),
			},
		},
		{
			name: "absent instance skipped",
			configs: []*config.Config{
				instance("a", "/m/a.yaml", port("web", "127.0.0.1:8080", "127.0.0.1:80")),
				{Name: "b", Status: "absent", ConfigFile: "/m/b.yaml", Ports: []config.PortConfig{port("web", "127.0.0.1:8080", "127.0.0.1:81")}},
			},
		},
		{
			name: "base config skipped",
			configs: []*config.Config{
				instance("a", "/m/a.yaml", port("web", "127.0.0.1:8080", "127.0.0.1:80")),
				{Base: true, ConfigFile: "/m/_base.yaml", Ports: []config.PortConfig{port("web", "127.0.0.1:8080", "127.0.0.1:81")}},
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := config.ValidateFleetPorts(tt.configs)
			if tt.wantErr == "" {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("error = %v, want containing %q", err, tt.wantErr)
			}
		})
	}
}

func TestMigrateManifest_PortsKnownKey(t *testing.T) {
	manifest := `schema: lxm/config/v1
name: web
image: ubuntu:24.04
ports:
  - {name: web, listen: "127.0.0.1:8080", connect: "127.0.0.1:80"}
`
	_, warnings, err := config.MigrateManifest([]byte(manifest))
	if err != nil {
		t.Fatalf("MigrateManifest error: %v", err)
	}
	for _, w := range warnings {
		if strings.Contains(w, "CONFIG_WARN_UNKNOWN_KEY") && strings.Contains(w, "ports") {
			t.Fatalf("ports must be a known key, got warning: %s", w)
		}
	}
}

func TestLoadConfig_Ports_ReplaceDirective(t *testing.T) {
	dir := t.TempDir()
	writePortsManifestInDir(t, dir, "_base.yaml", `schema: lxm/config/v2
base: true
image: ubuntu:24.04
ports:
  - {name: api, listen: "127.0.0.1:8080", connect: "127.0.0.1:80"}
`)
	leaf := writePortsManifestInDir(t, dir, "leaf.yaml", `schema: lxm/config/v2
name: leaf
image: ubuntu:24.04
include: [_base.yaml]
replace:
  ports:
    - {name: web, listen: "127.0.0.1:9090", connect: "127.0.0.1:90"}
`)
	conf, err := config.LoadConfig(leaf)
	if err != nil {
		t.Fatalf("LoadConfig error: %v", err)
	}
	if len(conf.Ports) != 1 || conf.Ports[0].Name != "web" {
		t.Fatalf("replace.ports must replace the inherited list, got %+v", conf.Ports)
	}
}

func TestLoadConfig_Ports_UnknownFieldRejected(t *testing.T) {
	manifest := `schema: lxm/config/v2
name: leaf
image: ubuntu:24.04
ports:
  - {name: web, listen: "127.0.0.1:8080", connect: "127.0.0.1:80", bogus: true}
`
	path := writePortsManifest(t, "leaf.yaml", manifest)
	if _, err := config.LoadConfig(path); err == nil || !strings.Contains(err.Error(), "field not allowed") {
		t.Fatalf("expected unknown-field rejection, got: %v", err)
	}
}

func TestConfigValidate_Ports_StaticIPv4MustBeIPv4(t *testing.T) {
	conf := &config.Config{
		Schema: "lxm/config/v2",
		Name:   "c1",
		Type:   "container",
		Status: "present",
		Image:  "ubuntu:24.04",
		Networks: []config.NetworkConfig{
			{Name: "eth0", Parent: "lxdbr0", IPv4: "2001:db8::5"},
		},
		Ports: []config.PortConfig{{
			Name:    "web",
			Listen:  "10.0.0.1:8080",
			Connect: "10.0.0.5:80",
			Proto:   "tcp",
			NAT:     true,
		}},
	}
	// The pre-existing network validation accepts IPv6 through net.ParseIP;
	// NAT validation must not treat it as a declared static IPv4.
	err := conf.Validate("")
	if err == nil {
		t.Fatal("expected NAT static-IPv4 validation error")
	}
	if !strings.Contains(err.Error(), "requires a static ipv4") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestLoadConfig_Ports_V1ProtoCanonicalized(t *testing.T) {
	// v1/no-schema manifests bypass the CUE enum; the loader must still
	// canonicalize the protocol so plan/diff comparisons are deterministic.
	manifest := `schema: lxm/config/v1
name: legacy-web
image: ubuntu:22.04
ports:
  - {name: dns, listen: "127.0.0.1:5353", connect: "127.0.0.1:53", proto: " UDP "}
`
	path := writePortsManifest(t, "legacy.yaml", manifest)
	conf, err := config.LoadConfig(path)
	if err != nil {
		t.Fatalf("LoadConfig error: %v", err)
	}
	if len(conf.Ports) != 1 || conf.Ports[0].Proto != "udp" {
		t.Fatalf("expected canonical proto udp, got %+v", conf.Ports)
	}
}

func TestConfigValidate_Ports_ContainerNATAllowed(t *testing.T) {
	manifest := `schema: lxm/config/v2
name: c-nat
image: ubuntu:24.04
networks:
  - {name: eth0, parent: lxdbr0, ipv4: 10.10.10.50}
ports:
  - {name: api, listen: "10.10.10.1:9443", connect: "10.10.10.50:443", nat: true}
`
	path := writePortsManifest(t, "c-nat.yaml", manifest)
	conf, err := config.LoadConfig(path)
	if err != nil {
		t.Fatalf("LoadConfig error: %v", err)
	}
	if err := conf.Validate(filepath.Dir(path)); err != nil {
		t.Fatalf("container NAT must be accepted: %v", err)
	}
}

func TestPortConfig_String(t *testing.T) {
	got := config.PortConfig{Name: "web", Listen: "127.0.0.1:8080", Connect: "127.0.0.1:80", Proto: "tcp", NAT: true}.String()
	if got != "tcp 127.0.0.1:8080 -> 127.0.0.1:80 (nat)" {
		t.Fatalf("String() = %q", got)
	}
	plain := config.PortConfig{Name: "web", Listen: "127.0.0.1:8080", Connect: "127.0.0.1:80", Proto: "udp"}
	if got := plain.String(); got != "udp 127.0.0.1:8080 -> 127.0.0.1:80" {
		t.Fatalf("String() = %q", got)
	}
}

func TestLoadConfig_Ports_EmptyReplaceIsNoop(t *testing.T) {
	// List-directive convention (shared with mounts/networks/disks/recipes): an
	// empty replace list is a no-op; remove.ports is the way to clear inherited
	// ports. Pinned so the documented behaviour cannot silently diverge.
	dir := t.TempDir()
	writePortsManifestInDir(t, dir, "_base.yaml", `schema: lxm/config/v2
base: true
image: ubuntu:24.04
ports:
  - {name: api, listen: "127.0.0.1:8080", connect: "127.0.0.1:80"}
`)
	leaf := writePortsManifestInDir(t, dir, "leaf.yaml", `schema: lxm/config/v2
name: leaf
image: ubuntu:24.04
include: [_base.yaml]
replace:
  ports: []
`)
	conf, err := config.LoadConfig(leaf)
	if err != nil {
		t.Fatalf("LoadConfig error: %v", err)
	}
	if len(conf.Ports) != 1 || conf.Ports[0].Name != "api" {
		t.Fatalf("empty replace.ports must be a no-op, got %+v", conf.Ports)
	}
}

func TestValidateFleetPorts_DuplicateInstanceNamesAcrossFiles(t *testing.T) {
	port := config.PortConfig{Name: "web", Listen: "127.0.0.1:8080", Connect: "127.0.0.1:80", Proto: "tcp"}
	configs := []*config.Config{
		{Schema: "lxm/config/v2", Name: "a", Status: "present", ConfigFile: "/m/a1.yaml", Ports: []config.PortConfig{port}},
		{Schema: "lxm/config/v2", Name: "a", Status: "present", ConfigFile: "/m/a2.yaml", Ports: []config.PortConfig{port}},
	}
	_, err := config.ValidateFleetPorts(configs)
	if err == nil || !strings.Contains(err.Error(), "published by both") {
		t.Fatalf("duplicate instance names must not bypass fleet validation, got: %v", err)
	}
}

func TestValidateFleetPorts_NATVsNonNATSameAddress(t *testing.T) {
	nat := config.PortConfig{Name: "web", Listen: "10.0.0.1:8443", Connect: "10.0.0.31:443", Proto: "tcp", NAT: true}
	plain := config.PortConfig{Name: "web", Listen: "10.0.0.1:8443", Connect: "10.0.0.31:443", Proto: "tcp"}
	configs := []*config.Config{
		{Schema: "lxm/config/v2", Name: "a", Status: "present", ConfigFile: "/m/a.yaml", Ports: []config.PortConfig{nat}},
		{Schema: "lxm/config/v2", Name: "b", Status: "present", ConfigFile: "/m/b.yaml", Ports: []config.PortConfig{plain}},
	}
	if _, err := config.ValidateFleetPorts(configs); err == nil {
		t.Fatal("NAT and non-NAT explicit listeners on the same address must conflict")
	}
}

func TestLoadConfig_Ports_BaseFragmentNICCollision(t *testing.T) {
	dir := t.TempDir()
	writePortsManifestInDir(t, dir, "_base.yaml", `schema: lxm/config/v2
base: true
image: ubuntu:24.04
networks:
  - {name: proxy-web, parent: lxdbr0}
`)
	leaf := writePortsManifestInDir(t, dir, "leaf.yaml", `schema: lxm/config/v2
name: leaf
image: ubuntu:24.04
include: [_base.yaml]
ports:
  - {name: web, listen: "127.0.0.1:8080", connect: "127.0.0.1:80"}
`)
	conf, err := config.LoadConfig(leaf)
	if err != nil {
		t.Fatalf("LoadConfig error: %v", err)
	}
	err = conf.Validate(dir)
	if err == nil || !strings.Contains(err.Error(), "would collide with device") {
		t.Fatalf("expected cross-layer NIC/proxy collision, got: %v", err)
	}
}

func TestConfigValidate_Ports_AbsentInstanceSkipsCrossFieldRules(t *testing.T) {
	// Deleting an instance must not be blocked by a NAT port that no longer has
	// a usable network pairing.
	conf := &config.Config{
		Name:   "gone",
		Status: "absent",
		Ports: []config.PortConfig{{
			Name:    "https",
			Listen:  "10.0.0.1:8443",
			Connect: "10.0.0.5:443",
			Proto:   "tcp",
			NAT:     true,
		}},
	}
	if err := conf.Validate(""); err != nil {
		t.Fatalf("absent instance port validation must be skipped, got: %v", err)
	}
}

func TestLoadConfig_Ports_IgnoredLocalListStillValidated(t *testing.T) {
	dir := t.TempDir()
	writePortsManifestInDir(t, dir, "_base.yaml", `schema: lxm/config/v2
base: true
image: ubuntu:24.04
ports:
  - {name: base, listen: "127.0.0.1:8080", connect: "127.0.0.1:80"}
`)
	leaf := writePortsManifestInDir(t, dir, "leaf.yaml", `schema: lxm/config/v2
name: leaf
image: ubuntu:24.04
include: [_base.yaml]
ports:
  - {name: dup, listen: "127.0.0.1:8081", connect: "127.0.0.1:81"}
  - {name: dup, listen: "127.0.0.1:8082", connect: "127.0.0.1:82"}
replace:
  ports:
    - {name: web, listen: "127.0.0.1:9090", connect: "127.0.0.1:90"}
`)
	_, err := config.LoadConfig(leaf)
	if err == nil || !strings.Contains(err.Error(), `duplicate port name "dup"`) {
		t.Fatalf("ignored local ports list must still be validated, got: %v", err)
	}
}
