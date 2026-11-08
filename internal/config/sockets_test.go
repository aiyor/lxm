package config_test

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/aiyor/lxm/internal/config"
)

func writeSocketsManifest(t *testing.T, name, body string) string {
	t.Helper()
	return writeSocketsManifestInDir(t, t.TempDir(), name, body)
}

func writeSocketsManifestInDir(t *testing.T, dir, name, body string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(body), 0644); err != nil {
		t.Fatalf("writing %s: %v", name, err)
	}
	return path
}

func loadSocketsConfig(t *testing.T, path string) (*config.Config, error) {
	t.Helper()
	conf, err := config.LoadConfig(path)
	if err != nil {
		return nil, err
	}
	if err := conf.Validate(filepath.Dir(path)); err != nil {
		return nil, err
	}
	return conf, nil
}

func TestLoadConfig_Sockets_DefaultsAndCanonicalization(t *testing.T) {
	manifest := `schema: lxm/config/v2
name: lab1
image: ubuntu:24.04
sockets:
  - name: broker
    listen: unix:/run/sshigit/broker.sock
    connect: unix:/run/sshigit-broker/lab1-dsh.sock
    bind: instance
    uid: 1000
    gid: 1000
    mode: "0660"
  - name: agent
    listen: unix:/run/sshigit/agent.sock
    connect: unix:/run/sshigit-agent/lab1-dsh.sock
    mode: 660
`
	conf, err := loadSocketsConfig(t, writeSocketsManifest(t, "lab1.yaml", manifest))
	if err != nil {
		t.Fatalf("LoadConfig/Validate error: %v", err)
	}
	if len(conf.Sockets) != 2 {
		t.Fatalf("expected 2 sockets, got %d", len(conf.Sockets))
	}

	broker := conf.Sockets[0]
	if broker.Bind != "instance" {
		t.Errorf("bind = %q, want instance", broker.Bind)
	}
	if broker.Listen != "unix:/run/sshigit/broker.sock" || broker.Connect != "unix:/run/sshigit-broker/lab1-dsh.sock" {
		t.Errorf("unexpected canonical endpoints: %+v", broker)
	}
	if broker.UID == nil || *broker.UID != 1000 || broker.GID == nil || *broker.GID != 1000 {
		t.Errorf("unexpected uid/gid: %+v", broker)
	}
	if broker.Mode != "0660" {
		t.Errorf("mode = %q, want canonical 0660", broker.Mode)
	}

	agent := conf.Sockets[1]
	if agent.Bind != "host" {
		t.Errorf("default bind = %q, want host", agent.Bind)
	}
	if agent.Mode != "0660" {
		t.Errorf("unquoted mode canonical = %q, want 0660", agent.Mode)
	}
	if agent.UID != nil || agent.GID != nil {
		t.Errorf("undeclared uid/gid must stay nil: %+v", agent)
	}
	if got := agent.String(); !strings.Contains(got, "bind=host") || !strings.Contains(got, "mode=0660") {
		t.Errorf("String() = %q", got)
	}
}

func TestLoadConfig_Sockets_V1Canonicalization(t *testing.T) {
	// Schema-less (v1-compat) manifests bypass CUE but must still be
	// canonicalized and validated by the Go layers.
	manifest := `name: lab1
image: ubuntu:24.04
sockets:
  - name: broker
    listen: unix:/run/broker.sock
    connect: unix:/run/host/broker.sock
`
	conf, err := loadSocketsConfig(t, writeSocketsManifest(t, "lab1.yaml", manifest))
	if err != nil {
		t.Fatalf("LoadConfig/Validate error: %v", err)
	}
	if len(conf.Sockets) != 1 || conf.Sockets[0].Bind != "host" {
		t.Fatalf("v1 socket not normalized: %+v", conf.Sockets)
	}
}

func TestConfigValidate_Sockets_ErrorMatrix(t *testing.T) {
	// Schema-less manifests exercise the authoritative Go diagnostics (v2
	// manifests surface the CUE coarser gate first; see the CUE matrix below).
	base := `name: c1
image: ubuntu:24.04
`
	tests := []struct {
		name string
		body string
		want string
	}{
		{
			name: "relative path",
			body: base + `sockets: [{name: broker, listen: "unix:broker.sock", connect: "unix:/run/host.sock"}]`,
			want: "must be absolute",
		},
		{
			name: "tcp listen",
			body: base + `sockets: [{name: broker, listen: "tcp:127.0.0.1:8080", connect: "unix:/run/host.sock"}]`,
			want: "use ports: instead",
		},
		{
			name: "abstract socket",
			body: base + `sockets: [{name: broker, listen: "unix:@broker", connect: "unix:/run/host.sock"}]`,
			want: "abstract unix sockets",
		},
		{
			name: "dotdot segment",
			body: base + `sockets: [{name: broker, listen: "unix:/run/../broker.sock", connect: "unix:/run/host.sock"}]`,
			want: "must be clean",
		},
		{
			name: "overlong path",
			body: base + `sockets: [{name: broker, listen: "unix:/` + strings.Repeat("a", 108) + `", connect: "unix:/run/host.sock"}]`,
			want: "sun_path limit",
		},
		{
			name: "NUL byte",
			body: base + "sockets: [{name: broker, listen: \"unix:/run/a\\u0000b.sock\", connect: \"unix:/run/host.sock\"}]",
			want: "control characters",
		},
		{
			name: "duplicate name in one layer",
			body: base + `sockets:
  - {name: broker, listen: "unix:/run/a.sock", connect: "unix:/run/h.sock"}
  - {name: broker, listen: "unix:/run/b.sock", connect: "unix:/run/h2.sock"}`,
			want: "duplicate socket name",
		},
		{
			name: "missing name",
			body: base + `sockets: [{listen: "unix:/run/a.sock", connect: "unix:/run/h.sock"}]`,
			want: "name is required",
		},
		{
			name: "invalid name",
			body: base + `sockets: [{name: Broker, listen: "unix:/run/a.sock", connect: "unix:/run/h.sock"}]`,
			want: "name must match",
		},
		{
			name: "invalid bind",
			body: base + `sockets: [{name: broker, listen: "unix:/run/a.sock", connect: "unix:/run/h.sock", bind: both}]`,
			want: "invalid bind",
		},
		{
			name: "uid out of range",
			body: base + `sockets: [{name: broker, listen: "unix:/run/a.sock", connect: "unix:/run/h.sock", uid: -1}]`,
			want: "uid -1 is out of range",
		},
		{
			name: "gid out of range",
			body: base + `sockets: [{name: broker, listen: "unix:/run/a.sock", connect: "unix:/run/h.sock", gid: 2147483648}]`,
			want: "gid 2147483648 is out of range",
		},
		{
			name: "invalid mode",
			body: base + `sockets: [{name: broker, listen: "unix:/run/a.sock", connect: "unix:/run/h.sock", mode: "0999"}]`,
			want: "octal file mode",
		},
		{
			name: "NIC device collision",
			body: base + `networks: [{name: socket-broker, parent: lxdbr0}]
sockets: [{name: broker, listen: "unix:/run/a.sock", connect: "unix:/run/h.sock"}]`,
			want: "would collide with device",
		},
		{
			name: "duplicate host-side path bind host",
			body: base + `sockets:
  - {name: a, listen: "unix:/run/shared.sock", connect: "unix:/run/i1.sock"}
  - {name: b, listen: "unix:/run/shared.sock", connect: "unix:/run/i2.sock"}`,
			want: "host-side unix socket path",
		},
		{
			name: "duplicate host-side path across bind modes",
			body: base + `sockets:
  - {name: a, listen: "unix:/run/shared.sock", connect: "unix:/run/i1.sock"}
  - {name: b, listen: "unix:/run/i2.sock", connect: "unix:/run/shared.sock", bind: instance}`,
			want: "host-side unix socket path",
		},
		{
			name: "duplicate instance-side listen path",
			body: base + `sockets:
  - {name: a, listen: "unix:/run/shared.sock", connect: "unix:/run/h1.sock", bind: instance}
  - {name: b, listen: "unix:/run/shared.sock", connect: "unix:/run/h2.sock", bind: instance}`,
			want: "instance-side listen socket",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path := writeSocketsManifest(t, "c1.yaml", tt.body)
			_, err := loadSocketsConfig(t, path)
			if err == nil {
				t.Fatalf("expected error containing %q, got nil", tt.want)
			}
			if !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("error = %q, want containing %q", err, tt.want)
			}
		})
	}
}

func TestConfigValidate_Sockets_VMRejected(t *testing.T) {
	body := `name: vm1
type: virtual-machine
image: ubuntu:24.04
vm: {boot_mode: uefi-nosecureboot}
sockets:
  - {name: broker, listen: "unix:/run/a.sock", connect: "unix:/run/h.sock"}
`
	_, err := loadSocketsConfig(t, writeSocketsManifest(t, "vm1.yaml", body))
	if err == nil || !strings.Contains(err.Error(), "container-only") {
		t.Fatalf("expected container-only error, got %v", err)
	}
}

func TestConfigValidate_Sockets_BaseAndAbsentSkipCrossField(t *testing.T) {
	// A base fragment and a status: absent manifest skip the cross-field rules
	// (host-path uniqueness, VM rejection), but grammar and names still apply.
	// Schema-less (v1-compat) base fragment: base files are normally only
	// loaded through an include, so no resolved-CUE round-trip applies here.
	baseFragment := `base: true
sockets:
  - {name: a, listen: "unix:/run/a.sock", connect: "unix:/run/h.sock"}
  - {name: b, listen: "unix:/run/a.sock", connect: "unix:/run/h.sock"}
`
	if _, err := config.LoadConfig(writeSocketsManifest(t, "_base.yaml", baseFragment)); err != nil {
		t.Fatalf("base fragment should skip cross-field validation: %v", err)
	}

	absent := `schema: lxm/config/v2
name: c1
status: absent
type: virtual-machine
sockets:
  - {name: a, listen: "unix:/run/a.sock", connect: "unix:/run/h.sock"}
  - {name: b, listen: "unix:/run/a.sock", connect: "unix:/run/h.sock"}
`
	if _, err := loadSocketsConfig(t, writeSocketsManifest(t, "c1.yaml", absent)); err != nil {
		t.Fatalf("status: absent should skip cross-field validation: %v", err)
	}

	// Grammar errors are still reported for base fragments.
	badGrammar := `base: true
sockets:
  - {name: a, listen: "unix:relative.sock", connect: "unix:/run/h.sock"}
`
	_, err := config.LoadConfig(writeSocketsManifest(t, "_bad.yaml", badGrammar))
	if err == nil || !strings.Contains(err.Error(), "must be absolute") {
		t.Fatalf("expected grammar error for base fragment, got %v", err)
	}
}

func TestLoadConfig_Sockets_CUEAuthoringMatrix(t *testing.T) {
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
			name: "unknown field nat",
			body: base + `sockets: [{name: broker, listen: "unix:/run/a.sock", connect: "unix:/run/h.sock", nat: true}]`,
			want: "schema validation",
		},
		{
			name: "unknown field proto",
			body: base + `sockets: [{name: broker, listen: "unix:/run/a.sock", connect: "unix:/run/h.sock", proto: tcp}]`,
			want: "schema validation",
		},
		{
			name: "unknown field proxy_protocol",
			body: base + `sockets: [{name: broker, listen: "unix:/run/a.sock", connect: "unix:/run/h.sock", proxy_protocol: true}]`,
			want: "schema validation",
		},
		{
			name: "missing unix prefix",
			body: base + `sockets: [{name: broker, listen: "/run/a.sock", connect: "unix:/run/h.sock"}]`,
			want: "schema validation",
		},
		{
			name: "invalid bind enum",
			body: base + `sockets: [{name: broker, listen: "unix:/run/a.sock", connect: "unix:/run/h.sock", bind: guest}]`,
			want: "schema validation",
		},
		{
			name: "negative uid",
			body: base + `sockets: [{name: broker, listen: "unix:/run/a.sock", connect: "unix:/run/h.sock", uid: -1}]`,
			want: "schema validation",
		},
		{
			name: "invalid socket name",
			body: base + `sockets: [{name: "Broker!", listen: "unix:/run/a.sock", connect: "unix:/run/h.sock"}]`,
			want: "schema validation",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := config.LoadConfig(writeSocketsManifest(t, "c1.yaml", tt.body))
			if err == nil {
				t.Fatalf("expected error containing %q, got nil", tt.want)
			}
			if !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("error = %q, want containing %q", err, tt.want)
			}
		})
	}
}

func TestMergeConfigs_Sockets(t *testing.T) {
	dir := t.TempDir()
	writeSocketsManifestInDir(t, dir, "_base.yaml", `schema: lxm/config/v2
base: true
sockets:
  - {name: broker, listen: "unix:/run/broker.sock", connect: "unix:/run/host/broker.sock"}
  - {name: agent, listen: "unix:/run/agent.sock", connect: "unix:/run/host/agent.sock", bind: instance}
`)
	writeSocketsManifestInDir(t, dir, "leaf.yaml", `schema: lxm/config/v2
include: [_base.yaml]
name: lab1
image: ubuntu:24.04
sockets:
  - name: broker
    listen: unix:/run/broker2.sock
    connect: unix:/run/host/broker2.sock
    bind: instance
    mode: "0600"
remove:
  sockets: [agent]
`)
	conf, err := loadSocketsConfig(t, filepath.Join(dir, "leaf.yaml"))
	if err != nil {
		t.Fatalf("LoadConfig/Validate error: %v", err)
	}
	if len(conf.Sockets) != 1 {
		t.Fatalf("expected 1 merged socket, got %d: %+v", len(conf.Sockets), conf.Sockets)
	}
	broker := conf.Sockets[0]
	if broker.Listen != "unix:/run/broker2.sock" || broker.Bind != "instance" || broker.Mode != "0600" {
		t.Fatalf("overlay did not replace the whole entry: %+v", broker)
	}
}

func TestMergeConfigs_Sockets_RemoveUnmatchedFails(t *testing.T) {
	dir := t.TempDir()
	writeSocketsManifestInDir(t, dir, "leaf.yaml", `schema: lxm/config/v2
name: lab1
image: ubuntu:24.04
sockets:
  - {name: broker, listen: "unix:/run/a.sock", connect: "unix:/run/h.sock"}
remove:
  sockets: [missing]
`)
	_, err := config.LoadConfig(filepath.Join(dir, "leaf.yaml"))
	if err == nil || !strings.Contains(err.Error(), `remove.sockets: name "missing" matched no socket`) {
		t.Fatalf("expected unmatched remove error, got %v", err)
	}
}

func TestMergeConfigs_Sockets_Replace(t *testing.T) {
	dir := t.TempDir()
	writeSocketsManifestInDir(t, dir, "_base.yaml", `schema: lxm/config/v2
base: true
sockets:
  - {name: broker, listen: "unix:/run/broker.sock", connect: "unix:/run/host/broker.sock"}
`)
	writeSocketsManifestInDir(t, dir, "leaf.yaml", `schema: lxm/config/v2
include: [_base.yaml]
name: lab1
image: ubuntu:24.04
sockets:
  - {name: ignored, listen: "unix:/run/ignored.sock", connect: "unix:/run/h.sock"}
replace:
  sockets:
    - {name: agent, listen: "unix:/run/agent.sock", connect: "unix:/run/host/agent.sock", bind: instance}
`)
	conf, err := loadSocketsConfig(t, filepath.Join(dir, "leaf.yaml"))
	if err != nil {
		t.Fatalf("LoadConfig/Validate error: %v", err)
	}
	if len(conf.Sockets) != 1 || conf.Sockets[0].Name != "agent" {
		t.Fatalf("replace.sockets did not win: %+v", conf.Sockets)
	}
}

func TestMergeConfigs_Sockets_EmptyReplaceIsNoop(t *testing.T) {
	dir := t.TempDir()
	writeSocketsManifestInDir(t, dir, "_base.yaml", `schema: lxm/config/v2
base: true
sockets:
  - {name: broker, listen: "unix:/run/broker.sock", connect: "unix:/run/host/broker.sock"}
`)
	writeSocketsManifestInDir(t, dir, "leaf.yaml", `schema: lxm/config/v2
include: [_base.yaml]
name: lab1
image: ubuntu:24.04
replace:
  sockets: []
`)
	conf, err := loadSocketsConfig(t, filepath.Join(dir, "leaf.yaml"))
	if err != nil {
		t.Fatalf("LoadConfig/Validate error: %v", err)
	}
	if len(conf.Sockets) != 1 || conf.Sockets[0].Name != "broker" {
		t.Fatalf("empty replace.sockets must be a no-op: %+v", conf.Sockets)
	}
}

func TestValidateFleetSockets(t *testing.T) {
	manifest := func(name, sockets string) *config.Config {
		return &config.Config{
			Name:       name,
			Status:     "present",
			ConfigFile: name + ".yaml",
			Sockets:    mustSocketConfigs(t, sockets),
		}
	}

	t.Run("duplicate host-side connect across instances", func(t *testing.T) {
		_, err := config.ValidateFleetSockets([]*config.Config{
			manifest("a", `- {name: broker, listen: "unix:/run/a.sock", connect: "unix:/run/shared.sock", bind: instance}`),
			manifest("b", `- {name: broker, listen: "unix:/run/b.sock", connect: "unix:/run/shared.sock", bind: instance}`),
		})
		if err == nil || !strings.Contains(err.Error(), "host-side unix socket path") {
			t.Fatalf("expected duplicate host path error, got %v", err)
		}
		if !strings.Contains(err.Error(), "a.yaml") || !strings.Contains(err.Error(), "b.yaml") {
			t.Fatalf("error must cite both manifests: %v", err)
		}
	})

	t.Run("duplicate host-side listen across bind modes", func(t *testing.T) {
		_, err := config.ValidateFleetSockets([]*config.Config{
			manifest("a", `- {name: listener, listen: "unix:/run/shared.sock", connect: "unix:/run/i.sock"}`),
			manifest("b", `- {name: connector, listen: "unix:/run/i2.sock", connect: "unix:/run/shared.sock", bind: instance}`),
		})
		if err == nil || !strings.Contains(err.Error(), "host-side unix socket path") {
			t.Fatalf("expected cross-mode duplicate error, got %v", err)
		}
	})

	t.Run("same instance-side path on different instances is allowed", func(t *testing.T) {
		// Instance-side paths live in separate namespaces, so identical
		// listen paths on different instances are not a host conflict.
		warnings, err := config.ValidateFleetSockets([]*config.Config{
			manifest("a", `- {name: broker, listen: "unix:/run/broker.sock", connect: "unix:/run/host/a.sock", bind: instance}`),
			manifest("b", `- {name: broker, listen: "unix:/run/broker.sock", connect: "unix:/run/host/b.sock", bind: instance}`),
		})
		if err != nil {
			t.Fatalf("unexpected fleet conflict: %v", err)
		}
		if len(warnings) != 0 {
			t.Fatalf("unexpected warnings: %v", warnings)
		}
	})

	t.Run("base and absent manifests are skipped", func(t *testing.T) {
		baseConf := manifest("base", `- {name: a, listen: "unix:/run/x.sock", connect: "unix:/run/y.sock"}`)
		baseConf.Base = true
		absent := manifest("absent", `- {name: a, listen: "unix:/run/x.sock", connect: "unix:/run/y.sock"}`)
		absent.Status = "absent"
		live := manifest("live", `- {name: a, listen: "unix:/run/x.sock", connect: "unix:/run/y.sock"}`)
		if _, err := config.ValidateFleetSockets([]*config.Config{baseConf, absent, live}); err != nil {
			t.Fatalf("unexpected fleet error for skipped manifests: %v", err)
		}
	})
}

func mustSocketConfigs(t *testing.T, socketsYAML string) []config.SocketConfig {
	t.Helper()
	// Build a single-layer manifest with the supplied sockets list and reuse
	// the loader's normalization by writing it to a temporary file.
	dir := t.TempDir()
	body := "name: tmp\nimage: ubuntu:24.04\nsockets:\n" + socketsYAML + "\n"
	path := writeSocketsManifestInDir(t, dir, "tmp.yaml", body)
	conf, err := config.LoadConfig(path)
	if err != nil {
		t.Fatalf("loading socket fixture: %v", err)
	}
	return conf.Sockets
}

func TestMigrateManifest_SocketsKnownKey(t *testing.T) {
	manifest := `schema: lxm/config/v1
name: lab1
image: ubuntu:24.04
sockets:
  - {name: broker, listen: "unix:/run/broker.sock", connect: "unix:/run/host.sock"}
`
	_, warnings, err := config.MigrateManifest([]byte(manifest))
	if err != nil {
		t.Fatalf("MigrateManifest error: %v", err)
	}
	for _, w := range warnings {
		if strings.Contains(w, "CONFIG_WARN_UNKNOWN_KEY") && strings.Contains(w, "sockets") {
			t.Fatalf("sockets must be a known key, got warning: %s", w)
		}
	}
}

func TestMigrateManifest_SocketsCanonicalized(t *testing.T) {
	// lxm compile must emit the same canonical socket surface the loader would
	// produce (and never a manifest plan/apply would reject).
	manifest := `name: lab1
image: ubuntu:24.04
sockets:
  - name: broker
    listen: unix:/run/broker.sock
    connect: unix:/run/host.sock
    mode: 660
    uid: 1000
`
	out, warnings, err := config.MigrateManifest([]byte(manifest))
	if err != nil {
		t.Fatalf("MigrateManifest error: %v", err)
	}
	for _, w := range warnings {
		if strings.Contains(w, "CONFIG_WARN_UNKNOWN_KEY") && strings.Contains(w, "sockets") {
			t.Fatalf("sockets must be a known key, got warning: %s", w)
		}
	}
	compiled := string(out)
	for _, want := range []string{
		"listen: unix:/run/broker.sock",
		"connect: unix:/run/host.sock",
		"bind: host",
		`mode: "0660"`,
		"uid: 1000",
	} {
		if !strings.Contains(compiled, want) {
			t.Fatalf("compiled manifest missing %q:\n%s", want, compiled)
		}
	}
	if strings.Contains(compiled, "mode: 660") {
		t.Fatalf("compiled manifest must canonicalize the mode to a quoted octal string:\n%s", compiled)
	}
}

func TestMigrateManifest_SocketsInvalidRejected(t *testing.T) {
	base := `name: lab1
image: ubuntu:24.04
sockets:
  - name: broker
    listen: %s
    connect: unix:/run/host.sock
%s
`
	tests := []struct {
		name   string
		listen string
		extra  string
		want   string
	}{
		{name: "invalid mode spelling", listen: "unix:/run/broker.sock", extra: `    mode: "0o660"`, want: "octal file mode"},
		{name: "uid out of range", listen: "unix:/run/broker.sock", extra: "    uid: 3000000000", want: "out of range"},
		{name: "double slash path", listen: "unix://run/broker.sock", extra: "", want: "must be clean"},
		// The v2 authoring schema is a coarse `unix:`/absolute gate, so these are
		// rejected by CUE before the precise Go endpoint diagnostics.
		{name: "abstract socket", listen: "unix:@broker", extra: "", want: "failed CUE validation"},
		{name: "relative path", listen: "unix:broker.sock", extra: "", want: "failed CUE validation"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, _, err := config.MigrateManifest([]byte(fmt.Sprintf(base, tt.listen, tt.extra)))
			if err == nil {
				t.Fatalf("expected error containing %q, got nil", tt.want)
			}
			if !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("error = %q, want containing %q", err, tt.want)
			}
		})
	}
}

func TestLoadConfig_Sockets_LegacyUnknownFieldsRejected(t *testing.T) {
	base := `name: c1
image: ubuntu:24.04
`
	tests := []struct {
		name string
		body string
		want string
	}{
		{
			name: "nat in sockets entry",
			body: base + `sockets: [{name: broker, listen: "unix:/run/a.sock", connect: "unix:/run/h.sock", nat: true}]`,
			want: `sockets[0]: unknown field "nat"`,
		},
		{
			name: "proto in sockets entry",
			body: base + `sockets: [{name: broker, listen: "unix:/run/a.sock", connect: "unix:/run/h.sock", proto: tcp}]`,
			want: `unknown field "proto"`,
		},
		{
			name: "proxy_protocol in replace.sockets",
			body: base + `replace:
  sockets:
    - {name: broker, listen: "unix:/run/a.sock", connect: "unix:/run/h.sock", proxy_protocol: true}
`,
			want: `replace.sockets[0]: unknown field "proxy_protocol"`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := config.LoadConfig(writeSocketsManifest(t, "c1.yaml", tt.body))
			if err == nil {
				t.Fatalf("expected error containing %q, got nil", tt.want)
			}
			if !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("error = %q, want containing %q", err, tt.want)
			}
		})
	}
}

func TestMigrateManifest_SocketsReplaceCanonicalized(t *testing.T) {
	// A non-empty replace.sockets wins at plan/apply time, but the ignored local
	// list and the replace list are both authoring surfaces: compile must
	// canonicalize both and reject invalid values in either.
	manifest := `name: lab1
image: ubuntu:24.04
sockets:
  - {name: ignored, listen: unix:/run/ignored.sock, connect: unix:/run/h.sock}
replace:
  sockets:
    - name: broker
      listen: unix:/run/broker.sock
      connect: unix:/run/host.sock
      bind: instance
      mode: 660
      uid: 1000
`
	out, _, err := config.MigrateManifest([]byte(manifest))
	if err != nil {
		t.Fatalf("MigrateManifest error: %v", err)
	}
	compiled := string(out)
	if !strings.Contains(compiled, `mode: "0660"`) {
		t.Fatalf("replace.sockets mode was not canonicalized:\n%s", compiled)
	}
	if strings.Contains(compiled, "mode: 660") {
		t.Fatalf("replace.sockets mode stayed non-canonical:\n%s", compiled)
	}

	invalid := strings.Replace(manifest, "mode: 660", `mode: "0o660"`, 1)
	if _, _, err := config.MigrateManifest([]byte(invalid)); err == nil || !strings.Contains(err.Error(), "octal file mode") {
		t.Fatalf("expected replace.sockets mode validation error, got %v", err)
	}
}

func TestLoadConfig_Sockets_LegacyAliasAndMergeKeys(t *testing.T) {
	// Valid YAML aliases and merge keys must not be rejected as unknown socket
	// fields by the legacy strictness pass; the decoder resolves them.
	tests := []struct {
		name string
		body string
	}{
		{
			name: "alias entry",
			body: `x-socket: &sock
  name: broker
  listen: unix:/run/a.sock
  connect: unix:/run/h.sock
sockets:
  - *sock
`,
		},
		{
			name: "merge key entry",
			body: `sockets:
  - <<: &defaults
      name: broker
      listen: unix:/run/a.sock
      connect: unix:/run/h.sock
`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			conf, err := config.LoadConfig(writeSocketsManifest(t, "c1.yaml", "name: c1\nimage: ubuntu:24.04\n"+tt.body))
			if err != nil {
				t.Fatalf("LoadConfig error: %v", err)
			}
			if len(conf.Sockets) != 1 || conf.Sockets[0].Name != "broker" || conf.Sockets[0].Listen != "unix:/run/a.sock" {
				t.Fatalf("alias/merge socket not decoded: %+v", conf.Sockets)
			}
		})
	}
}

func TestMigrateManifest_SocketsAliasEntry(t *testing.T) {
	// An alias entry is a mapping after dereferencing; compile must not fail
	// spuriously (the duplicate names are not checked on the compile path, which
	// does not run the plan-time uniqueness validation).
	manifest := `name: lab1
image: ubuntu:24.04
sockets:
  - &sock
    name: broker
    listen: unix:/run/a.sock
    connect: unix:/run/h.sock
  - *sock
`
	if _, _, err := config.MigrateManifest([]byte(manifest)); err != nil {
		t.Fatalf("MigrateManifest rejected a valid alias entry: %v", err)
	}
}

func TestMigrateManifest_SocketsPreservesInlineComments(t *testing.T) {
	manifest := `name: lab1
image: ubuntu:24.04
sockets:
  - name: broker
    listen: unix:/run/a.sock  # instance side
    connect: unix:/run/h.sock
    mode: "660"  # client access
`
	out, _, err := config.MigrateManifest([]byte(manifest))
	if err != nil {
		t.Fatalf("MigrateManifest error: %v", err)
	}
	compiled := string(out)
	for _, want := range []string{"# instance side", "# client access"} {
		if !strings.Contains(compiled, want) {
			t.Fatalf("compiled manifest lost inline comment %q:\n%s", want, compiled)
		}
	}
	if !strings.Contains(compiled, `mode: "0660"`) {
		t.Fatalf("mode was not canonicalized while preserving comments:\n%s", compiled)
	}
}
