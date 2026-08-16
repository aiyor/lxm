package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSplitImageRef(t *testing.T) {
	tests := []struct {
		image    string
		remote   string
		alias    string
		isRemote bool
	}{
		{"ubuntu:22.04", "ubuntu", "22.04", true},
		{"images:ubuntu/22.04", "images", "ubuntu/22.04", true},
		{"ubuntu-daily:24.04", "ubuntu-daily", "24.04", true},
		{"corp-images:alpine", "corp-images", "alpine", true},
		{"jammy", "", "jammy", false},               // bare alias
		{"ubuntu-22.04", "", "ubuntu-22.04", false}, // bare alias with dash
		{"abc123def456", "", "abc123def456", false}, // fingerprint
		{"ubuntu/22.04", "", "ubuntu/22.04", false}, // slash form (rejected by CUE)
		{"a:b:c", "", "a:b:c", false},               // multi-colon (rejected by CUE)
		{"", "", "", false},
	}
	for _, tt := range tests {
		remote, alias, isRemote := SplitImageRef(tt.image)
		if remote != tt.remote || alias != tt.alias || isRemote != tt.isRemote {
			t.Errorf("SplitImageRef(%q) = (%q, %q, %v), want (%q, %q, %v)",
				tt.image, remote, alias, isRemote, tt.remote, tt.alias, tt.isRemote)
		}
	}
}

func TestImageLocalRef(t *testing.T) {
	tests := []struct {
		image        string
		instanceType string
		want         string
	}{
		{"ubuntu:22.04", "container", "ubuntu/22.04"},
		{"images:ubuntu/22.04", "container", "images/ubuntu/22.04"},
		{"ubuntu-daily:24.04", "container", "ubuntu-daily/24.04"},
		{"ubuntu:22.04", "virtual-machine", "ubuntu/22.04/vm"},
		{"images:debian/12", "virtual-machine", "images/debian/12/vm"},
		{"jammy", "container", "jammy"},
		{"jammy", "virtual-machine", "jammy"},
		{"abc123def456", "virtual-machine", "abc123def456"},
	}
	for _, tt := range tests {
		if got := ImageLocalRef(tt.image, tt.instanceType); got != tt.want {
			t.Errorf("ImageLocalRef(%q, %q) = %q, want %q", tt.image, tt.instanceType, got, tt.want)
		}
	}
}

func TestCanonicalizeImageRemoteURL(t *testing.T) {
	tests := []struct {
		name    string
		raw     string
		want    string
		wantErr string
	}{
		{"https://cloud-images.ubuntu.com/releases", "https://cloud-images.ubuntu.com/releases", "https://cloud-images.ubuntu.com/releases", ""},
		// trailing slash trimmed
		{"https://cloud-images.ubuntu.com/releases/", "https://cloud-images.ubuntu.com/releases/", "https://cloud-images.ubuntu.com/releases", ""},
		// scheme+host case normalized
		{"HTTPS://CLOUD-IMAGES.UBUNTU.COM/releases", "HTTPS://CLOUD-IMAGES.UBUNTU.COM/releases", "https://cloud-images.ubuntu.com/releases", ""},
		// port preserved
		{"https://images.example.com:8443/path", "https://images.example.com:8443/path", "https://images.example.com:8443/path", ""},
		// empty path dropped
		{"https://images.example.com", "https://images.example.com", "https://images.example.com", ""},
		{"https://images.example.com/", "https://images.example.com/", "https://images.example.com", ""},
		// query preserved
		{"https://images.example.com/p?token=1", "https://images.example.com/p?token=1", "https://images.example.com/p?token=1", ""},
		// loopback http accepted
		{"http://localhost:5000/images", "http://localhost:5000/images", "http://localhost:5000/images", ""},
		{"http://127.0.0.1/images", "http://127.0.0.1/images", "http://127.0.0.1/images", ""},
		{"http://[::1]/images", "http://[::1]/images", "http://[::1]/images", ""},
		// http on non-loopback rejected
		{"http://images.example.com/path", "http://images.example.com/path", "", "http is only allowed for loopback hosts"},
		// bad scheme rejected
		{"ftp://images.example.com/path", "ftp://images.example.com/path", "", "scheme must be https"},
		// missing host rejected
		{"https:///path", "https:///path", "", "missing host"},
		// missing scheme rejected
		{"cloud-images.ubuntu.com/releases", "cloud-images.ubuntu.com/releases", "", "missing scheme"},
		// unparsable rejected
		{"not a url", "not a url", "", "invalid image remote URL"},
	}
	for _, tt := range tests {
		got, err := CanonicalizeImageRemoteURL(tt.name, tt.raw)
		if tt.wantErr != "" {
			if err == nil {
				t.Errorf("CanonicalizeImageRemoteURL(%q, %q): expected error containing %q, got nil", tt.name, tt.raw, tt.wantErr)
				continue
			}
			if !strings.Contains(err.Error(), tt.wantErr) {
				t.Errorf("CanonicalizeImageRemoteURL(%q, %q): error %q does not contain %q", tt.name, tt.raw, err, tt.wantErr)
			}
			continue
		}
		if err != nil {
			t.Errorf("CanonicalizeImageRemoteURL(%q, %q): unexpected error: %v", tt.name, tt.raw, err)
			continue
		}
		if got != tt.want {
			t.Errorf("CanonicalizeImageRemoteURL(%q, %q) = %q, want %q", tt.name, tt.raw, got, tt.want)
		}
	}
}

func TestValidatePostMerge_ImageRemotesCanonicalizes(t *testing.T) {
	conf := &Config{
		ImageRemotes: map[string]string{
			"ubuntu": "HTTPS://CLOUD-IMAGES.UBUNTU.COM/releases/",
		},
	}
	if err := ValidatePostMerge(conf); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got := conf.ImageRemotes["ubuntu"]; got != "https://cloud-images.ubuntu.com/releases" {
		t.Errorf("expected canonicalized URL, got %q", got)
	}
}

func TestValidatePostMerge_ImageRemotesBadURL(t *testing.T) {
	conf := &Config{
		ImageRemotes: map[string]string{
			"corp": "http://images.corp.example.com",
		},
	}
	if err := ValidatePostMerge(conf); err == nil {
		t.Fatal("expected error for http non-loopback image remote")
	}
}

func TestMergeConfigs_ImageRemotesKeyWise(t *testing.T) {
	base := &Config{
		ImageRemotes: map[string]string{
			"ubuntu": "https://mirror.internal/ubuntu",
			"corp":   "https://images.corp.example.com",
		},
	}
	overlay := &Config{
		ImageRemotes: map[string]string{
			"ubuntu": "https://override.internal/ubuntu",
		},
	}
	res, err := MergeConfigs(base, overlay)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got := res.ImageRemotes["ubuntu"]; got != "https://override.internal/ubuntu" {
		t.Errorf("expected overlay to win for ubuntu, got %q", got)
	}
	if got := res.ImageRemotes["corp"]; got != "https://images.corp.example.com" {
		t.Errorf("expected base corp preserved (key-wise merge), got %q", got)
	}
	if len(res.ImageRemotes) != 2 {
		t.Errorf("expected 2 remotes after key-wise merge, got %d", len(res.ImageRemotes))
	}
}

func TestMergeConfigs_ImageRemotesNone(t *testing.T) {
	res, err := MergeConfigs(&Config{Image: "ubuntu:22.04"}, &Config{User: "x"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.ImageRemotes != nil {
		t.Errorf("expected nil ImageRemotes when neither side declares any, got %v", res.ImageRemotes)
	}
}

func TestEffectiveImageRemotes_BuiltinsAndOverrides(t *testing.T) {
	conf := &Config{
		ConfigFile: "/fleet/a.yaml",
		ImageRemotes: map[string]string{
			"ubuntu": "https://mirror.internal/ubuntu",
			"corp":   "https://images.corp.example.com",
		},
	}
	out, err := EffectiveImageRemotes([]*Config{conf})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if out["ubuntu"] != "https://mirror.internal/ubuntu" {
		t.Errorf("expected declared ubuntu to override built-in, got %q", out["ubuntu"])
	}
	if out["ubuntu-daily"] != "https://cloud-images.ubuntu.com/daily" {
		t.Errorf("expected built-in ubuntu-daily preserved, got %q", out["ubuntu-daily"])
	}
	if out["images"] != "https://images.lxd.canonical.com" {
		t.Errorf("expected built-in images preserved, got %q", out["images"])
	}
	if out["corp"] != "https://images.corp.example.com" {
		t.Errorf("expected custom corp remote, got %q", out["corp"])
	}
}

func TestEffectiveImageRemotes_IdenticalDedup(t *testing.T) {
	a := &Config{ConfigFile: "/fleet/a.yaml", ImageRemotes: map[string]string{"ubuntu": "https://cloud-images.ubuntu.com/releases"}}
	b := &Config{ConfigFile: "/fleet/b.yaml", ImageRemotes: map[string]string{"ubuntu": "https://cloud-images.ubuntu.com/releases/"}}
	out, err := EffectiveImageRemotes([]*Config{a, b})
	if err != nil {
		t.Fatalf("expected identical canonical URLs to dedup without conflict, got %v", err)
	}
	if out["ubuntu"] != "https://cloud-images.ubuntu.com/releases" {
		t.Errorf("expected canonical deduped URL, got %q", out["ubuntu"])
	}
}

func TestEffectiveImageRemotes_Conflict(t *testing.T) {
	a := &Config{ConfigFile: "/fleet/a.yaml", ImageRemotes: map[string]string{"mirror": "https://a.example.com"}}
	b := &Config{ConfigFile: "/fleet/b.yaml", ImageRemotes: map[string]string{"mirror": "https://b.example.com"}}
	_, err := EffectiveImageRemotes([]*Config{a, b})
	if err == nil {
		t.Fatal("expected conflict error for same name with different URLs")
	}
	if !strings.Contains(err.Error(), "mirror") || !strings.Contains(err.Error(), "a.yaml") || !strings.Contains(err.Error(), "b.yaml") {
		t.Errorf("conflict error should cite name and both files, got %v", err)
	}
}

func TestLoadConfig_ImageRemotesRoundTrip(t *testing.T) {
	dir := t.TempDir()
	basePath := filepath.Join(dir, "_base.yaml")
	manifestPath := filepath.Join(dir, "dev.yaml")

	baseContent := `schema: lxm/config/v2
base: true
image_remotes:
  corp-images: https://images.corp.example.com
`
	if err := os.WriteFile(basePath, []byte(baseContent), 0644); err != nil {
		t.Fatalf("writing base: %v", err)
	}
	manifestContent := `schema: lxm/config/v2
include:
  - _base.yaml
name: dev
status: present
image: corp-images:alpine
`
	if err := os.WriteFile(manifestPath, []byte(manifestContent), 0644); err != nil {
		t.Fatalf("writing manifest: %v", err)
	}

	conf, err := LoadConfig(manifestPath)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// Base-declared remote inherited through the include chain.
	if got := conf.ImageRemotes["corp-images"]; got != "https://images.corp.example.com" {
		t.Errorf("expected inherited image_remotes, got %q", got)
	}
}

func TestLoadConfig_ImageRemotesInvalidName(t *testing.T) {
	dir := t.TempDir()
	manifestPath := filepath.Join(dir, "dev.yaml")
	content := `schema: lxm/config/v2
name: dev
status: absent
image_remotes:
  "bad name!": https://images.example.com
`
	if err := os.WriteFile(manifestPath, []byte(content), 0644); err != nil {
		t.Fatalf("writing manifest: %v", err)
	}
	_, err := LoadConfig(manifestPath)
	if err == nil {
		t.Fatal("expected error for invalid image remote name")
	}
	// The precise-diagnostic layer must name the offending key (the CUE _|_
	// error does not).
	if !strings.Contains(err.Error(), `invalid remote name "bad name!"`) {
		t.Errorf("expected precise message naming the key, got: %v", err)
	}
}

// TestImageRemoteName_CompilePath locks the remote-name charset on the lxm
// compile path. MigrateManifest validates the resolved schema first and falls
// back to the authoring schema; a bad name fails resolved but passes authoring
// (the authoring schema declares the charset but does not enforce map keys
// under close()), so the compile path runs validateImageRemoteNames directly
// to fail with a precise, actionable message.
func TestImageRemoteName_CompilePath(t *testing.T) {
	bad := []byte("schema: lxm/config/v2\nname: dev\nstatus: present\nimage: ubuntu:22.04\nimage_remotes:\n  \"bad name!\": https://images.example.com\n")
	if _, _, err := MigrateManifest(bad); err == nil {
		t.Fatal("expected compile to reject invalid remote name")
	} else if !strings.Contains(err.Error(), `invalid remote name "bad name!"`) {
		t.Errorf("expected precise compile message, got: %v", err)
	}
	good := []byte("schema: lxm/config/v2\nname: dev\nstatus: present\nimage: ubuntu:22.04\nimage_remotes:\n  corp-images: https://images.example.com\n")
	if _, _, err := MigrateManifest(good); err != nil {
		t.Errorf("compile should accept valid remote name, got: %v", err)
	}
}

// TestImageRemoteName_CUEEnforced locks the remote-name charset rule. CUE
// enforces it as the resolved-form contract (#LXM_RESOLVED pairs the positive
// #ImageRemoteName pattern with #ImageRemoteNameInvalid: _|_), and Go enforces
// it with a precise message in ValidatePostMerge (CUE's _|_ error does not
// name the key, so the Go check is the diagnostic layer). The authoring schema
// declares the charset for tooling; it is intentionally not the enforcement
// point (a positive-only key-pattern is a no-op inside close()).
func TestImageRemoteName_CUEEnforced(t *testing.T) {
	v, err := NewValidator()
	if err != nil {
		t.Fatalf("NewValidator: %v", err)
	}
	resolved := func(name string) error {
		doc := "schema: lxm/config/v2\nname: dev\nuser: ubuntu\nstatus: present\nimage: ubuntu:22.04\nimage_remotes:\n  \"" + name + "\": https://images.example.com\n"
		return v.ValidateResolved([]byte(doc))
	}
	goCheck := func(name string) error {
		return ValidatePostMerge(&Config{ImageRemotes: map[string]string{name: "https://images.example.com"}})
	}

	for _, name := range []string{"ubuntu", "ubuntu-daily", "corp-images", "a.b_c-1"} {
		if err := resolved(name); err != nil {
			t.Errorf("resolved: valid name %q rejected: %v", name, err)
		}
		if err := goCheck(name); err != nil {
			t.Errorf("Go: valid name %q rejected: %v", name, err)
		}
	}
	for _, name := range []string{"bad name!", "a/b", "a:b", "snowman☃"} {
		if err := resolved(name); err == nil {
			t.Errorf("resolved: invalid name %q accepted", name)
		}
		if err := goCheck(name); err == nil {
			t.Errorf("Go: invalid name %q accepted", name)
		} else if !strings.Contains(err.Error(), `invalid remote name "`+name+`"`) {
			t.Errorf("Go: precise message missing for %q: %v", name, err)
		}
	}
}

// TestVarsKey_CUEEnforced covers the same close() map-key quirk for the
// pre-existing vars/#EnvKey field, which is fixed by the same paired-rejection
// idiom so the schema honestly enforces what it declares.
func TestVarsKey_CUEEnforced(t *testing.T) {
	v, err := NewValidator()
	if err != nil {
		t.Fatalf("NewValidator: %v", err)
	}
	doc := func(key string) []byte {
		return []byte("schema: lxm/config/v2\nname: dev\nstatus: absent\nvars:\n  \"" + key + "\": x\n")
	}
	if err := v.ValidateAuthoring(doc("PROJECT_ROOT")); err != nil {
		t.Errorf("valid env key rejected: %v", err)
	}
	if err := v.ValidateAuthoring(doc("bad key!")); err == nil {
		t.Error("invalid env key accepted by authoring schema")
	}
}
