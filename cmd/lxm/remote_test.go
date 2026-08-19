package main

import (
	"bytes"
	"context"
	"log/slog"
	"os"
	"strings"
	"testing"

	"github.com/aiyor/lxm/internal/config"
	"github.com/aiyor/lxm/internal/provider"
	"github.com/aiyor/lxm/internal/provider/fake"
)

func TestRemoteCLI_Lifecycle(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("LXM_CONFIG_DIR", tmpDir)

	ctx := context.Background()
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
	svc, err := resolveFleetService(baseGetter, []*config.Config{conf}, opts)
	if err != nil || svc != baseSvc {
		t.Fatalf("expected baseSvc, got err: %v", err)
	}

	// 2. Conf with CLI override returns resolved driver
	optsCLI := &cmdOptions{provider: "lxd"}
	confWithProv := &config.Config{Name: "web", Provider: "incus"}
	svc, err = resolveFleetService(baseGetter, []*config.Config{confWithProv}, optsCLI)
	if err != nil || svc == nil {
		t.Fatalf("expected resolved svc under CLI override, got err: %v", err)
	}

	// 3. Conflicting fleet targets returns error
	confA := &config.Config{Name: "web", Remote: "remote-a"}
	confB := &config.Config{Name: "db", Remote: "remote-b"}
	_, err = resolveFleetService(baseGetter, []*config.Config{confA, confB}, opts)
	if err == nil || !strings.Contains(err.Error(), "conflicting remote targets") {
		t.Fatalf("expected conflicting remote targets error, got: %v", err)
	}

	// 4. Conflicting cluster target nodes returns error
	confNode1 := &config.Config{Name: "web", Target: "node1"}
	confNode2 := &config.Config{Name: "db", Target: "node2"}
	_, err = resolveFleetService(baseGetter, []*config.Config{confNode1, confNode2}, opts)
	if err == nil || !strings.Contains(err.Error(), "conflicting cluster target nodes") {
		t.Fatalf("expected conflicting cluster target nodes error, got: %v", err)
	}
}
