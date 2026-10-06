package main

import (
	"bytes"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/aiyor/lxm/internal/config"
	"github.com/aiyor/lxm/internal/provider"
	"github.com/aiyor/lxm/internal/provider/fake"
	"github.com/aiyor/lxm/internal/provider/remote"
)

func TestRemoteCLI_Lifecycle(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("LXM_CONFIG_DIR", tmpDir)

	ctx := t.Context()
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	mockGetter := func() (provider.Driver, error) {
		return fake.New(), nil
	}

	// 1. List initial default remotes
	var stdout, stderr bytes.Buffer
	rootCmd, _ := newRootCmd(ctx, &stdout, &stderr, mockGetter, logger)
	rootCmd.SetArgs([]string{"remote", "list"})
	if err := rootCmd.Execute(); err != nil {
		t.Fatalf("remote list failed: %v", err)
	}
	out := stdout.String()
	if !strings.Contains(out, "local") {
		t.Errorf("expected 'local' remote in list output, got: %s", out)
	}

	// 2. Add new remote (unix socket)
	stdout.Reset()
	stderr.Reset()
	rootCmd, _ = newRootCmd(ctx, &stdout, &stderr, mockGetter, logger)
	rootCmd.SetArgs([]string{"remote", "add", "lab-socket", "unix:///run/incus/unix.socket", "--provider", "incus", "--project", "dev"})
	if err := rootCmd.Execute(); err != nil {
		t.Fatalf("remote add failed: %v", err)
	}
	if !strings.Contains(stdout.String(), "Remote \"lab-socket\" added successfully") {
		t.Errorf("expected success message, got: %s", stdout.String())
	}

	// 3. Set default remote
	stdout.Reset()
	stderr.Reset()
	rootCmd, _ = newRootCmd(ctx, &stdout, &stderr, mockGetter, logger)
	rootCmd.SetArgs([]string{"remote", "set-default", "lab-socket"})
	if err := rootCmd.Execute(); err != nil {
		t.Fatalf("remote set-default failed: %v", err)
	}

	// 4. Set project for remote
	stdout.Reset()
	stderr.Reset()
	rootCmd, _ = newRootCmd(ctx, &stdout, &stderr, mockGetter, logger)
	rootCmd.SetArgs([]string{"remote", "set-project", "lab-socket", "production"})
	if err := rootCmd.Execute(); err != nil {
		t.Fatalf("remote set-project failed: %v", err)
	}

	// 5. Verify list in JSON format
	stdout.Reset()
	stderr.Reset()
	rootCmd, _ = newRootCmd(ctx, &stdout, &stderr, mockGetter, logger)
	rootCmd.SetArgs([]string{"--format", "json", "remote", "list"})
	if err := rootCmd.Execute(); err != nil {
		t.Fatalf("remote list json failed: %v", err)
	}
	jsonOut := stdout.String()
	if !strings.Contains(jsonOut, `"default_remote": "lab-socket"`) || !strings.Contains(jsonOut, `"project": "production"`) {
		t.Errorf("expected json with updated default and project, got: %s", jsonOut)
	}

	// 6. Remove remote
	stdout.Reset()
	stderr.Reset()
	rootCmd, _ = newRootCmd(ctx, &stdout, &stderr, mockGetter, logger)
	rootCmd.SetArgs([]string{"remote", "remove", "lab-socket"})
	if err := rootCmd.Execute(); err != nil {
		t.Fatalf("remote remove failed: %v", err)
	}
	if !strings.Contains(stdout.String(), "Remote \"lab-socket\" removed") {
		t.Errorf("expected removal message, got: %s", stdout.String())
	}
}

func TestResolveFleetService(t *testing.T) {
	baseSvc := fake.New()
	baseGetter := func() (provider.Driver, error) {
		return baseSvc, nil
	}

	// 1. Conf without provider/remote/target/project returns baseSvc
	conf := &config.Config{Name: "web"}
	opts := &cmdOptions{}
	svc, err := resolveFleetService(baseGetter, []*config.Config{conf}, opts, unexpectedResolve(t))
	if err != nil || svc != baseSvc {
		t.Fatalf("expected baseSvc, got err: %v", err)
	}

	// 2. Conf with CLI override returns resolved driver (fake resolver, no daemon needed)
	optsCLI := &cmdOptions{provider: "lxd"}
	confWithProv := &config.Config{Name: "web", Provider: "incus"}
	override := fake.New()
	svc, err = resolveFleetService(baseGetter, []*config.Config{confWithProv}, optsCLI, func(opts remote.ResolveOptions) (provider.Driver, error) {
		if opts.Provider != provider.ProviderTypeLXD {
			t.Errorf("expected CLI --provider override lxd, got %q", opts.Provider)
		}
		return override, nil
	})
	if err != nil || svc == nil {
		t.Fatalf("expected resolved svc under CLI override, got err: %v", err)
	}
	if svc != override {
		t.Errorf("expected CLI override to take precedence over baseGetter, got %T", svc)
	}

	// 3. Conflicting fleet targets returns error
	confA := &config.Config{Name: "web", Remote: "remote-a"}
	confB := &config.Config{Name: "db", Remote: "remote-b"}
	_, err = resolveFleetService(baseGetter, []*config.Config{confA, confB}, opts, unexpectedResolve(t))
	if err == nil || !strings.Contains(err.Error(), "conflicting remote targets") {
		t.Fatalf("expected conflicting remote targets error, got: %v", err)
	}

	// 4. Conflicting cluster target nodes returns error
	confNode1 := &config.Config{Name: "web", Target: "node1"}
	confNode2 := &config.Config{Name: "db", Target: "node2"}
	_, err = resolveFleetService(baseGetter, []*config.Config{confNode1, confNode2}, opts, unexpectedResolve(t))
	if err == nil || !strings.Contains(err.Error(), "conflicting cluster target nodes") {
		t.Fatalf("expected conflicting cluster target nodes error, got: %v", err)
	}

	// 5. Conf with manifest remotes passes ManifestRemotes to resolver
	confManifestRemote := &config.Config{
		Name:   "web",
		Remote: "custom-remote",
		Remotes: map[string]config.RemoteConfig{
			"custom-remote": {
				Address:           "https://10.0.0.50:8443",
				Provider:          "incus",
				ServerCertificate: "-----BEGIN CERTIFICATE-----\nMIIC...\n-----END CERTIFICATE-----",
				ServerFingerprint: "abcdef0123456789",
			},
		},
	}
	svc, err = resolveFleetService(baseGetter, []*config.Config{confManifestRemote}, opts, func(opts remote.ResolveOptions) (provider.Driver, error) {
		if opts.RemoteName != "custom-remote" {
			t.Errorf("expected remote name custom-remote, got %q", opts.RemoteName)
		}
		entry, ok := opts.ManifestRemotes["custom-remote"]
		if !ok || entry.Address != "https://10.0.0.50:8443" || entry.Provider != provider.ProviderTypeIncus {
			t.Errorf("manifest remotes not populated properly: %+v", opts.ManifestRemotes)
		}
		if entry.ServerCertificate != "-----BEGIN CERTIFICATE-----\nMIIC...\n-----END CERTIFICATE-----" {
			t.Errorf("server certificate mismatch: got %q", entry.ServerCertificate)
		}
		if entry.ServerFingerprint != "abcdef0123456789" {
			t.Errorf("server fingerprint mismatch: got %q", entry.ServerFingerprint)
		}
		return override, nil
	})
	if err != nil || svc != override {
		t.Fatalf("expected resolved svc with manifest remotes, got %v", err)
	}
}

// unexpectedResolve returns a resolver that fails the test if it is ever invoked.
func unexpectedResolve(t *testing.T) resolveDriverFunc {
	t.Helper()
	return func(remote.ResolveOptions) (provider.Driver, error) {
		t.Fatal("resolveFleetService resolver invoked unexpectedly")
		return nil, nil
	}
}

func TestRemoteCLI_AddInsecure_RoundTripResolve(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("LXM_CONFIG_DIR", tmpDir)

	// Spin up mock HTTPS server simulating an Incus API endpoint
	ts := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"type":"sync","status":"Success","status_code":200,"metadata":{"api_version":"1.0"}}`))
	}))
	defer ts.Close()

	ctx := t.Context()
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	mockGetter := func() (provider.Driver, error) {
		return fake.New(), nil
	}

	var stdout, stderr bytes.Buffer
	rootCmd, _ := newRootCmd(ctx, &stdout, &stderr, mockGetter, logger)
	rootCmd.SetArgs([]string{"remote", "add", "lab-insecure", ts.URL, "--insecure"})
	if err := rootCmd.Execute(); err != nil {
		t.Fatalf("remote add --insecure failed: %v", err)
	}

	cfg, err := remote.LoadConfig()
	if err != nil {
		t.Fatalf("loading saved remote config: %v", err)
	}
	entry, ok := cfg.Remotes["lab-insecure"]
	if !ok {
		t.Fatalf("expected remote 'lab-insecure' in config, got %+v", cfg.Remotes)
	}
	if !entry.Insecure {
		t.Errorf("expected Insecure: true, got false")
	}
	if entry.ServerCertificate != "" {
		t.Errorf("expected empty ServerCertificate when --insecure is set, got %q", entry.ServerCertificate)
	}
	if entry.ServerFingerprint != "" {
		t.Errorf("expected empty ServerFingerprint when --insecure is set, got %q", entry.ServerFingerprint)
	}

	// Resolve the remote driver; must succeed without contradictory configuration errors
	d, err := remote.ResolveDriver(remote.ResolveOptions{
		RemoteName: "lab-insecure",
	})
	if err != nil {
		t.Fatalf("ResolveDriver failed on remote added with --insecure: %v", err)
	}
	if d == nil {
		t.Fatalf("expected resolved driver, got nil")
	}
}

func TestRemoteCLI_AddInsecure_UnreachableServerSucceeds(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("LXM_CONFIG_DIR", tmpDir)

	ctx := t.Context()
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	mockGetter := func() (provider.Driver, error) {
		return fake.New(), nil
	}

	var stdout, stderr bytes.Buffer
	rootCmd, _ := newRootCmd(ctx, &stdout, &stderr, mockGetter, logger)
	// Unreachable endpoint (closed port) must succeed when --insecure is passed because probe is skipped
	rootCmd.SetArgs([]string{"remote", "add", "unreachable-host", "https://127.0.0.1:65534", "--insecure"})
	if err := rootCmd.Execute(); err != nil {
		t.Fatalf("remote add --insecure on unreachable host should succeed without probing, but got: %v", err)
	}

	cfg, err := remote.LoadConfig()
	if err != nil {
		t.Fatalf("loading saved remote config: %v", err)
	}
	entry, ok := cfg.Remotes["unreachable-host"]
	if !ok {
		t.Fatalf("expected remote 'unreachable-host' in config, got %+v", cfg.Remotes)
	}
	if !entry.Insecure {
		t.Errorf("expected Insecure: true, got false")
	}
	if entry.ServerCertificate != "" {
		t.Errorf("expected empty ServerCertificate when --insecure is set, got %q", entry.ServerCertificate)
	}
	if entry.ServerFingerprint != "" {
		t.Errorf("expected empty ServerFingerprint when --insecure is set, got %q", entry.ServerFingerprint)
	}
}
