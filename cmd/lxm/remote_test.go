package main

import (
	"bytes"
	"context"
	"log/slog"
	"os"
	"strings"
	"testing"

	"github.com/aiyor/lxm/internal/lxd"
)

func TestRemoteCLI_Lifecycle(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("LXM_CONFIG_DIR", tmpDir)

	ctx := context.Background()
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	mockGetter := func() (lxd.InstanceService, error) {
		return lxd.NewFakeInstanceServer(), nil
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
