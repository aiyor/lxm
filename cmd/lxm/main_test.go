package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/aiyor/lxm/internal/config"
	"github.com/aiyor/lxm/internal/lxd"
	"github.com/aiyor/lxm/internal/output"
	"github.com/aiyor/lxm/internal/plan"
	"github.com/canonical/lxd/shared/api"
)

func TestMain(m *testing.M) {
	tmpDir, err := os.MkdirTemp("", "lxm_cmd_test_*")
	if err == nil {
		_ = os.Setenv("LXM_KNOWN_HOSTS_FILE", filepath.Join(tmpDir, "known_hosts"))
	}
	code := m.Run()
	if tmpDir != "" {
		_ = os.RemoveAll(tmpDir)
	}
	os.Exit(code)
}

func TestRun_Help(t *testing.T) {
	fake := lxd.NewFakeInstanceServer()
	var stdout, stderr bytes.Buffer

	code := run([]string{"--help"}, &stdout, &stderr, fake)
	if code != 0 {
		t.Errorf("run(--help) returned %d, want 0", code)
	}
	if !strings.Contains(stdout.String(), "declarative reconciliation") {
		t.Errorf("stdout missing usage header: %q", stdout.String())
	}
	if stderr.Len() > 0 {
		t.Errorf("expected empty stderr on --help, got: %q", stderr.String())
	}
}

func TestRun_OfflineLXD_FileAndMetaCommandsSucceed(t *testing.T) {
	// Passing nil service simulates offline LXD
	offlineCmds := [][]string{
		{"--help"},
		{"completion", "bash"},
		{"include", ".", "_base.yaml", "--dry-run"},
		{"compile", "."},
		{"doctor", "--skip-remote"},
	}

	for _, args := range offlineCmds {
		t.Run(strings.Join(args, "_"), func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			code := run(args, &stdout, &stderr, nil)
			if code != 0 {
				t.Errorf("run(%v) with offline LXD returned %d, want 0. Stderr: %s", args, code, stderr.String())
			}
		})
	}
}

func TestRun_OfflineLXD_LXDCommandsFailExit4(t *testing.T) {
	t.Setenv("LXD_SOCKET", "/nonexistent/path/to/lxd.sock")
	tmpDir := t.TempDir()
	cfgFile := filepath.Join(tmpDir, "dev.yaml")
	_ = os.WriteFile(cfgFile, []byte("name: test\nimage: ubuntu:22.04\nstatus: present\n"), 0644)

	// LXD-dependent commands should return exit 4 when LXD is offline
	lxdCmds := [][]string{
		{"apply", cfgFile},
		{"list"},
		{"run", "box1", "script.sh"},
		{"script", "box1", "script.sh"},
		{"shell", "box1"},
		{"ssh", "box1"},
		{"status", "box1"},
		{"doctor"},
	}

	for _, args := range lxdCmds {
		t.Run(strings.Join(args, "_"), func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			code := run(args, &stdout, &stderr, nil)
			if code != 4 {
				t.Errorf("run(%v) with offline LXD returned %d, want 4. Stderr: %s", args, code, stderr.String())
			}
			if !strings.Contains(stderr.String(), "LXD") && !strings.Contains(stderr.String(), "Failed") {
				t.Errorf("stderr missing connection error for %v: %q", args, stderr.String())
			}
		})
	}
}

func TestRun_ApplyPreflightConfigError_Exit3(t *testing.T) {
	fake := lxd.NewFakeInstanceServer()
	tmpDir := t.TempDir()
	cfgFile := filepath.Join(tmpDir, "invalid.yaml")
	// Invalid status value violates config validation
	_ = os.WriteFile(cfgFile, []byte("name: test\nimage: ubuntu:22.04\nstatus: invalid-status\n"), 0644)

	var stdout, stderr bytes.Buffer
	code := run([]string{"apply", cfgFile}, &stdout, &stderr, fake)
	if code != 3 {
		t.Errorf("run(apply invalid config) returned %d, want 3 (CONFIG_ERROR)", code)
	}
	if !strings.Contains(stderr.String(), "config validation") {
		t.Errorf("stderr missing config validation error: %q", stderr.String())
	}
}

func TestRun_RunPreflightConfigError_Exit3(t *testing.T) {
	fake := lxd.NewFakeInstanceServer()
	tmpDir := t.TempDir()
	cfgFile := filepath.Join(tmpDir, "invalid.yaml")
	_ = os.WriteFile(cfgFile, []byte("name: test\nimage: ubuntu:22.04\nstatus: invalid-status\n"), 0644)

	scriptFile := filepath.Join(tmpDir, "test.sh")
	_ = os.WriteFile(scriptFile, []byte("#!/bin/bash\necho ok\n"), 0755)

	var stdout, stderr bytes.Buffer
	code := run([]string{"run", tmpDir, scriptFile}, &stdout, &stderr, fake)
	if code != 3 {
		t.Errorf("run(run dir with invalid config) returned %d, want 3 (CONFIG_ERROR). Stderr: %s", code, stderr.String())
	}
}

func TestRun_ApplyNoStartFlag(t *testing.T) {
	fake := lxd.NewFakeInstanceServer()
	tmpDir := t.TempDir()
	cfgFile := filepath.Join(tmpDir, "dev.yaml")
	_ = os.WriteFile(cfgFile, []byte("name: dev-box\nimage: ubuntu:22.04\nstatus: present\n"), 0644)

	var stdout, stderr bytes.Buffer
	code := run([]string{"apply", cfgFile, "--no-start"}, &stdout, &stderr, fake)
	if code != 0 {
		t.Fatalf("apply --no-start returned %d, want 0. Stderr: %s", code, stderr.String())
	}
	inst, _, err := fake.GetInstance("dev-box")
	if err != nil {
		t.Fatalf("dev-box container should exist in fake server, got: %v", err)
	}
	if inst.StatusCode != api.Stopped {
		t.Errorf("container status under --no-start = %v, want %v (Stopped)", inst.StatusCode, api.Stopped)
	}
}

func TestRun_ApplyETagDrift_JSONRetryable(t *testing.T) {
	// UG5 B1: an apply that fails with LXD's real 412 message must surface
	// retryable: true in the JSON envelope so re-plan/re-apply pipelines can
	// detect drift. Regression for the envelope path, which previously
	// rebuilt error entries from the single exit error with retryable
	// hardcoded to false.
	fake := lxd.NewFakeInstanceServer()
	_ = fake.CreateInstance(api.InstancesPost{Name: "dev-box"})
	fake.UpdateInstanceFunc = func(name string, put api.InstancePut, etag string) error {
		return fmt.Errorf("ETag does not match: stale vs fresh. The configuration has been modified since this change began. Please retrieve the updated configuration before proceeding.")
	}

	tmpDir := t.TempDir()
	cfgFile := filepath.Join(tmpDir, "dev.yaml")
	_ = os.WriteFile(cfgFile, []byte("name: dev-box\nimage: ubuntu:22.04\nstatus: present\nuser: ubuntu\n"), 0644)

	var stdout, stderr bytes.Buffer
	code := run([]string{"apply", cfgFile, "--format", "json"}, &stdout, &stderr, fake)
	if code != 4 {
		t.Fatalf("apply returned %d, want 4. Stderr: %s", code, stderr.String())
	}

	var env struct {
		OK       bool `json:"ok"`
		ExitCode int  `json:"exit_code"`
		Errors   []struct {
			Code      string `json:"code"`
			Container string `json:"container"`
			Message   string `json:"message"`
			Retryable bool   `json:"retryable"`
		} `json:"errors"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &env); err != nil {
		t.Fatalf("failed to parse apply JSON envelope: %v. Output: %s", err, stdout.String())
	}
	if len(env.Errors) != 1 {
		t.Fatalf("expected 1 error in envelope, got %d: %s", len(env.Errors), stdout.String())
	}
	if !env.Errors[0].Retryable {
		t.Errorf("expected retryable=true on ETag drift error, got %+v", env.Errors[0])
	}
	if env.Errors[0].Code != "LXD_ERROR" {
		t.Errorf("expected error code LXD_ERROR, got %q", env.Errors[0].Code)
	}
	if env.Errors[0].Container != "dev-box" {
		t.Errorf("expected container dev-box on error, got %q", env.Errors[0].Container)
	}
	if env.OK || env.ExitCode != 4 {
		t.Errorf("expected ok=false exit_code=4, got ok=%v exit_code=%d", env.OK, env.ExitCode)
	}
}

func TestRun_ApplyInterrupt_EnvelopeKeepsInternalError(t *testing.T) {
	// The envelope's report-error propagation is gated on ctx.Err() == nil:
	// an interrupted apply forces exit 1 and must pair it with a single
	// INTERNAL_ERROR entry, never with the report's per-container
	// LXD_ERROR/retryable entries (SPEC_RESULT code-to-exit mapping).
	fake := lxd.NewFakeInstanceServer()
	_ = fake.CreateInstance(api.InstancesPost{Name: "dev-box"})
	fake.UpdateInstanceFunc = func(name string, put api.InstancePut, etag string) error {
		return fmt.Errorf("ETag does not match: stale vs fresh. The configuration has been modified since this change began. Please retrieve the updated configuration before proceeding.")
	}

	tmpDir := t.TempDir()
	cfgFile := filepath.Join(tmpDir, "dev.yaml")
	_ = os.WriteFile(cfgFile, []byte("name: dev-box\nimage: ubuntu:22.04\nstatus: present\nuser: ubuntu\n"), 0644)

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // pre-canceled: simulates an interrupt

	var stdout, stderr bytes.Buffer
	code := runWithContext(ctx, []string{"apply", cfgFile, "--format", "json"}, &stdout, &stderr, fake)
	if code != 1 {
		t.Fatalf("interrupted apply returned %d, want 1. Stderr: %s", code, stderr.String())
	}

	var env struct {
		OK       bool `json:"ok"`
		ExitCode int  `json:"exit_code"`
		Errors   []struct {
			Code      string `json:"code"`
			Retryable bool   `json:"retryable"`
		} `json:"errors"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &env); err != nil {
		t.Fatalf("failed to parse envelope: %v. Output: %s", err, stdout.String())
	}
	if len(env.Errors) != 1 {
		t.Fatalf("expected exactly one error on interrupt, got %d: %s", len(env.Errors), stdout.String())
	}
	if env.Errors[0].Code != "INTERNAL_ERROR" || env.Errors[0].Retryable {
		t.Errorf("expected a single non-retryable INTERNAL_ERROR on interrupt, got %+v", env.Errors[0])
	}
	// Discriminates the ctx.Err() guard: on the interrupt path the envelope
	// entry is built by SetExitCode (no container); report propagation would
	// carry the container name. Without the guard, a pre-canceled context
	// still yields an INTERNAL_ERROR-shaped report, so the code/retryable
	// assertions alone pass either way.
	var env2 struct {
		Errors []struct {
			Container string `json:"container"`
		} `json:"errors"`
	}
	_ = json.Unmarshal(stdout.Bytes(), &env2)
	if len(env2.Errors) == 0 || env2.Errors[0].Container != "" {
		t.Errorf("expected envelope error with empty container on interrupt, got %+v", env2.Errors)
	}
	if env.OK || env.ExitCode != 1 {
		t.Errorf("expected ok=false exit_code=1, got ok=%v exit_code=%d", env.OK, env.ExitCode)
	}
}

func TestRun_RunEnvVars(t *testing.T) {
	fake := lxd.NewFakeInstanceServer()
	_ = fake.CreateInstance(api.InstancesPost{Name: "dev-box"})

	tmpDir := t.TempDir()
	scriptFile := filepath.Join(tmpDir, "test.sh")
	_ = os.WriteFile(scriptFile, []byte("#!/bin/bash\necho $FOO\n"), 0755)

	var stdout, stderr bytes.Buffer
	code := run([]string{"run", "dev-box", scriptFile, "--env", "FOO=bar"}, &stdout, &stderr, fake)
	if code != 0 {
		t.Fatalf("run with --env returned %d, want 0. Stderr: %s", code, stderr.String())
	}
}

func TestRun_ApplyNameSelectorAndRenameTo(t *testing.T) {
	fake := lxd.NewFakeInstanceServer()
	tmpDir := t.TempDir()
	cfgFile := filepath.Join(tmpDir, "dev.yaml")
	_ = os.WriteFile(cfgFile, []byte("name: dev-box\nimage: ubuntu:22.04\nstatus: present\n"), 0644)

	t.Run("single file rename-to on matching name", func(t *testing.T) {
		var stdout, stderr bytes.Buffer
		code := run([]string{"apply", cfgFile, "--name", "dev-box", "--rename-to", "renamed-box"}, &stdout, &stderr, fake)
		if code != 0 {
			t.Fatalf("apply with --name and --rename-to returned %d, want 0. Stderr: %s", code, stderr.String())
		}
		if _, _, err := fake.GetInstance("renamed-box"); err != nil {
			t.Errorf("container renamed-box should have been created")
		}
	})

	t.Run("single file name mismatch exits 5", func(t *testing.T) {
		var stdout, stderr bytes.Buffer
		code := run([]string{"apply", cfgFile, "--name", "other-box"}, &stdout, &stderr, fake)
		if code != 5 {
			t.Errorf("apply with mismatching --name returned %d, want 5", code)
		}
	})

	t.Run("directory target with rename-to exits 2", func(t *testing.T) {
		var stdout, stderr bytes.Buffer
		code := run([]string{"apply", tmpDir, "--rename-to", "renamed-box"}, &stdout, &stderr, fake)
		if code != 2 {
			t.Errorf("apply directory with --rename-to returned %d, want 2", code)
		}
	})
}

func TestRun_SSHInterspersedFlags(t *testing.T) {
	fake := lxd.NewFakeInstanceServer()
	_ = fake.CreateInstance(api.InstancesPost{Name: "box1"})

	var stdout, stderr bytes.Buffer
	// Test passing SSH flag -o after container name box1 with --dry-run
	code := run([]string{"--dry-run", "ssh", "box1", "-o", "StrictHostKeyChecking=no"}, &stdout, &stderr, fake)
	if code != 0 {
		t.Errorf("ssh with -o flag returned %d, want 0. Stderr: %s", code, stderr.String())
	}
}

// TestRun_SSHStrictInvocation_HostKeyAlias covers UG5 B2: the strict
// verification invocation must pin the known-hosts lookup to the container
// name with `-o HostKeyAlias=<name>`. Without it, OpenSSH consults the
// HostName (the IP) for non-DNS-resolvable names, so the name-keyed entry
// registered by lxm never matches and every first connect fails with
// "No ... host key is known". Bypass paths (--insecure or user-supplied
// -o StrictHostKeyChecking=no) must not gain the alias.
func TestRun_SSHStrictInvocation_HostKeyAlias(t *testing.T) {
	fake := lxd.NewFakeInstanceServer()
	_ = fake.CreateInstance(api.InstancesPost{Name: "box1"})

	// Strict verification path.
	var stdout, stderr bytes.Buffer
	code := run([]string{"--dry-run", "ssh", "box1", "hostname"}, &stdout, &stderr, fake)
	if code != 0 {
		t.Fatalf("ssh --dry-run returned %d, want 0. Stderr: %s", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "HostKeyAlias=box1") {
		t.Errorf("strict ssh invocation missing HostKeyAlias=box1: %q", stdout.String())
	}

	// Bypass paths must NOT gain HostKeyAlias: user -o StrictHostKeyChecking=no,
	// user -o UserKnownHostsFile=/dev/null, and --insecure.
	for _, bypass := range [][]string{
		{"-o", "StrictHostKeyChecking=no"},
		{"-o", "UserKnownHostsFile=/dev/null"},
	} {
		var out, errB bytes.Buffer
		args := []string{"--dry-run", "ssh", "box1"}
		args = append(args, bypass...)
		args = append(args, "hostname")
		code = run(args, &out, &errB, fake)
		if code != 0 {
			t.Fatalf("ssh bypass %v returned %d, want 0. Stderr: %s", bypass, code, errB.String())
		}
		if strings.Contains(out.String(), "HostKeyAlias=") {
			t.Errorf("bypass ssh invocation %v should not contain HostKeyAlias: %q", bypass, out.String())
		}
	}
	var out3, err3 bytes.Buffer
	code = run([]string{"--dry-run", "ssh", "box1", "--insecure", "hostname"}, &out3, &err3, fake)
	if code != 0 {
		t.Fatalf("ssh --insecure --dry-run returned %d, want 0. Stderr: %s", code, err3.String())
	}
	if strings.Contains(out3.String(), "HostKeyAlias=") {
		t.Errorf("--insecure ssh invocation should not contain HostKeyAlias: %q", out3.String())
	}
}

// TestRun_SSHUserOverridesEffective covers UG5 B4: OpenSSH honors the FIRST
// occurrence of an option, so lxm must not emit its own defaults for options
// the user supplied. Regression: lxm's `-o Port=22` / `-o
// StrictHostKeyChecking=yes` preceded the user's passthrough args, silently
// defeating documented overrides like `-p 2222` and
// `-o StrictHostKeyChecking=no` (which also skipped key registration and then
// failed strict verification).
func TestRun_SSHUserOverridesEffective(t *testing.T) {
	fake := lxd.NewFakeInstanceServer()
	_ = fake.CreateInstance(api.InstancesPost{Name: "box1"})

	t.Run("user -o StrictHostKeyChecking=no replaces lxm default", func(t *testing.T) {
		var stdout, stderr bytes.Buffer
		code := run([]string{"--dry-run", "ssh", "box1", "-o", "StrictHostKeyChecking=no", "hostname"}, &stdout, &stderr, fake)
		if code != 0 {
			t.Fatalf("ssh returned %d, want 0. Stderr: %s", code, stderr.String())
		}
		out := stdout.String()
		if strings.Contains(out, "StrictHostKeyChecking=yes") {
			t.Errorf("lxm default StrictHostKeyChecking=yes must be dropped when the user sets the option: %q", out)
		}
		if !strings.Contains(out, "-o StrictHostKeyChecking=no") {
			t.Errorf("user option missing from invocation: %q", out)
		}
	})

	t.Run("user -p overrides lxm Port default", func(t *testing.T) {
		var stdout, stderr bytes.Buffer
		code := run([]string{"--dry-run", "ssh", "box1", "-p", "2222", "hostname"}, &stdout, &stderr, fake)
		if code != 0 {
			t.Fatalf("ssh returned %d, want 0. Stderr: %s", code, stderr.String())
		}
		out := stdout.String()
		if strings.Contains(out, "-o Port=22") {
			t.Errorf("lxm default Port must be dropped when the user passes -p: %q", out)
		}
		if !strings.Contains(out, "-p 2222") {
			t.Errorf("user -p missing from invocation: %q", out)
		}
	})

	t.Run("defaults present without user options", func(t *testing.T) {
		var stdout, stderr bytes.Buffer
		code := run([]string{"--dry-run", "ssh", "box1", "hostname"}, &stdout, &stderr, fake)
		if code != 0 {
			t.Fatalf("ssh returned %d, want 0. Stderr: %s", code, stderr.String())
		}
		out := stdout.String()
		if !strings.Contains(out, "-o Port=22") || !strings.Contains(out, "StrictHostKeyChecking=yes") {
			t.Errorf("expected lxm defaults in plain invocation: %q", out)
		}
	})

	t.Run("option keys are case-insensitive", func(t *testing.T) {
		// OpenSSH config keywords are case-insensitive; the dedup must match
		// isHostKeyBypassArg's lowercasing, otherwise a lowercase spelling is
		// not deduped and lxm's strict default (emitted first) wins.
		var stdout, stderr bytes.Buffer
		code := run([]string{"--dry-run", "ssh", "box1", "-o", "stricthostkeychecking=no", "hostname"}, &stdout, &stderr, fake)
		if code != 0 {
			t.Fatalf("ssh returned %d, want 0. Stderr: %s", code, stderr.String())
		}
		out := stdout.String()
		if strings.Contains(out, "StrictHostKeyChecking=yes") {
			t.Errorf("lowercase user option not deduped; lxm strict default present: %q", out)
		}
	})

	t.Run("attached -p2222 overrides Port", func(t *testing.T) {
		var stdout, stderr bytes.Buffer
		code := run([]string{"--dry-run", "ssh", "box1", "-p2222", "hostname"}, &stdout, &stderr, fake)
		if code != 0 {
			t.Fatalf("ssh returned %d, want 0. Stderr: %s", code, stderr.String())
		}
		out := stdout.String()
		if strings.Contains(out, "-o Port=22") {
			t.Errorf("lxm Port default must be dropped for -p2222: %q", out)
		}
	})

	t.Run("config-line space form deduped", func(t *testing.T) {
		var stdout, stderr bytes.Buffer
		code := run([]string{"--dry-run", "ssh", "box1", "-o", "Port 2222", "hostname"}, &stdout, &stderr, fake)
		if code != 0 {
			t.Fatalf("ssh returned %d, want 0. Stderr: %s", code, stderr.String())
		}
		out := stdout.String()
		if strings.Contains(out, "-o Port=22") {
			t.Errorf("lxm Port default must be dropped for '-o Port 2222': %q", out)
		}
	})

	t.Run("UserKnownHostsFile=/dev/null bypass completes", func(t *testing.T) {
		// A user nulling the key store without setting StrictHostKeyChecking
		// asked for verification to be disabled; lxm must not leave strict
		// checking against an empty store (unconnectable). It completes the
		// bypass and never emits the strict default.
		var stdout, stderr bytes.Buffer
		code := run([]string{"--dry-run", "ssh", "box1", "-o", "UserKnownHostsFile=/dev/null", "hostname"}, &stdout, &stderr, fake)
		if code != 0 {
			t.Fatalf("ssh returned %d, want 0. Stderr: %s", code, stderr.String())
		}
		out := stdout.String()
		if strings.Contains(out, "StrictHostKeyChecking=yes") {
			t.Errorf("strict default must not be emitted on the /dev/null bypass: %q", out)
		}
		if !strings.Contains(out, "StrictHostKeyChecking=no") {
			t.Errorf("bypass completion missing StrictHostKeyChecking=no: %q", out)
		}
	})

	t.Run("space-form UserKnownHostsFile /dev/null bypass completes", func(t *testing.T) {
		// OpenSSH's config-file space grammar ('-o "UserKnownHostsFile
		// /dev/null"') must trigger the same bypass completion as the '='
		// spelling; otherwise the dedup drops lxm's store default while the
		// strict default stays, yielding an unconnectable strict-vs-empty
		// combination.
		var stdout, stderr bytes.Buffer
		code := run([]string{"--dry-run", "ssh", "box1", "-o", "UserKnownHostsFile /dev/null", "hostname"}, &stdout, &stderr, fake)
		if code != 0 {
			t.Fatalf("ssh returned %d, want 0. Stderr: %s", code, stderr.String())
		}
		out := stdout.String()
		if strings.Contains(out, "StrictHostKeyChecking=yes") {
			t.Errorf("strict default must not be emitted on the space-form /dev/null bypass: %q", out)
		}
		if !strings.Contains(out, "StrictHostKeyChecking=no") {
			t.Errorf("space-form bypass completion missing StrictHostKeyChecking=no: %q", out)
		}
	})
}

func TestRun_ListJSONFormat(t *testing.T) {
	fake := lxd.NewFakeInstanceServer()
	_ = fake.CreateInstance(api.InstancesPost{Name: "box1"})

	var stdout, stderr bytes.Buffer
	code := run([]string{"list", "--format", "json"}, &stdout, &stderr, fake)
	if code != 0 {
		t.Fatalf("list --format json returned %d, want 0. Stderr: %s", code, stderr.String())
	}

	var env output.Envelope
	if err := json.Unmarshal(stdout.Bytes(), &env); err != nil {
		t.Fatalf("failed to parse list JSON output envelope: %v. Output: %s", err, stdout.String())
	}
	if env.Schema != "lxm/result/v1" {
		t.Errorf("expected schema lxm/result/v1, got %s", env.Schema)
	}
	if !env.OK || env.ExitCode != 0 {
		t.Errorf("expected OK true and exit_code 0, got OK=%v exit_code=%d", env.OK, env.ExitCode)
	}
}

func TestRun_RunMissingTarget_Exit5(t *testing.T) {
	fake := lxd.NewFakeInstanceServer()
	tmpDir := t.TempDir()
	scriptFile := filepath.Join(tmpDir, "script.sh")
	_ = os.WriteFile(scriptFile, []byte("#!/bin/bash\necho ok\n"), 0755)

	var stdout, stderr bytes.Buffer
	code := run([]string{"run", "nonexistent-box", scriptFile}, &stdout, &stderr, fake)
	if code != 5 {
		t.Errorf("run on nonexistent container returned %d, want 5 (TARGET_NOT_FOUND). Stderr: %s", code, stderr.String())
	}
}

func TestRun_SubcommandHelp(t *testing.T) {
	fake := lxd.NewFakeInstanceServer()
	var stdout, stderr bytes.Buffer

	code := run([]string{"apply", "--help"}, &stdout, &stderr, fake)
	if code != 0 {
		t.Errorf("run(apply --help) returned %d, want 0", code)
	}
	if !strings.Contains(stdout.String(), "Usage:") || !strings.Contains(stdout.String(), "apply") {
		t.Errorf("stdout missing apply help: %q", stdout.String())
	}
}

func TestRun_UnknownCommand(t *testing.T) {
	fake := lxd.NewFakeInstanceServer()
	var stdout, stderr bytes.Buffer

	code := run([]string{"invalidcmd"}, &stdout, &stderr, fake)
	if code != 2 {
		t.Errorf("run(invalidcmd) returned %d, want 2", code)
	}
	if !strings.Contains(stderr.String(), "unknown command") {
		t.Errorf("stderr missing unknown command error: %q", stderr.String())
	}
	if !strings.Contains(stderr.String(), "Run 'lxm --help' for usage") {
		t.Errorf("stderr missing usage hint: %q", stderr.String())
	}
}

func TestRun_RemovedCommands(t *testing.T) {
	removedCmds := []string{"launch", "bootstrap", "attach"}
	fake := lxd.NewFakeInstanceServer()

	for _, cmdName := range removedCmds {
		t.Run(cmdName, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			code := run([]string{cmdName}, &stdout, &stderr, fake)
			if code != 2 {
				t.Errorf("run(%s) returned %d, want 2 (Usage error for removed command)", cmdName, code)
			}
			if !strings.Contains(stderr.String(), "unknown command") {
				t.Errorf("stderr missing unknown command error for %s: %q", cmdName, stderr.String())
			}
		})
	}
}

func TestRun_UnknownFlag(t *testing.T) {
	fake := lxd.NewFakeInstanceServer()
	var stdout, stderr bytes.Buffer

	code := run([]string{"--nonexistent-flag"}, &stdout, &stderr, fake)
	if code != 2 {
		t.Errorf("run(--nonexistent-flag) returned %d, want 2", code)
	}
	if !strings.Contains(stderr.String(), "unknown flag") {
		t.Errorf("stderr missing unknown flag error: %q", stderr.String())
	}
}

func TestRun_ApplyPruneSingleFile_Exit2(t *testing.T) {
	fake := lxd.NewFakeInstanceServer()
	tmpDir := t.TempDir()
	cfgFile := filepath.Join(tmpDir, "test.yaml")
	_ = os.WriteFile(cfgFile, []byte("name: test\nimage: ubuntu:22.04\nstatus: present\n"), 0644)

	var stdout, stderr bytes.Buffer
	code := run([]string{"apply", cfgFile, "--prune"}, &stdout, &stderr, fake)
	if code != 2 {
		t.Errorf("run(apply file --prune) returned %d, want 2", code)
	}
	if !strings.Contains(stderr.String(), "--prune is only allowed on directory targets") {
		t.Errorf("stderr missing prune single-file error: %q", stderr.String())
	}
}

func TestRun_ApplyTargetNotFound_Exit5(t *testing.T) {
	fake := lxd.NewFakeInstanceServer()
	var stdout, stderr bytes.Buffer

	code := run([]string{"apply", "/nonexistent/path/target.yaml"}, &stdout, &stderr, fake)
	if code != 5 {
		t.Errorf("run(apply nonexistent) returned %d, want 5", code)
	}
	if !strings.Contains(stderr.String(), "target") {
		t.Errorf("stderr missing target not found message: %q", stderr.String())
	}
}

func TestRun_ApplySuccess(t *testing.T) {
	fake := lxd.NewFakeInstanceServer()
	tmpDir := t.TempDir()
	cfgFile := filepath.Join(tmpDir, "dev.yaml")
	content := `
name: dev-box
image: ubuntu:22.04
status: present
`
	if err := os.WriteFile(cfgFile, []byte(content), 0644); err != nil {
		t.Fatalf("failed to write test config: %v", err)
	}

	var stdout, stderr bytes.Buffer
	code := run([]string{"apply", cfgFile, "--debug"}, &stdout, &stderr, fake)
	if code != 0 {
		t.Fatalf("run(apply) returned %d, want 0. Stderr: %s", code, stderr.String())
	}

	inst, _, err := fake.GetInstance("dev-box")
	if err != nil {
		t.Fatalf("expected instance dev-box created in fake server, got err: %v", err)
	}
	if inst.Name != "dev-box" {
		t.Errorf("expected instance name dev-box, got %s", inst.Name)
	}
}

func TestRun_GroupFiltersSpaceAndEquals(t *testing.T) {
	fake := lxd.NewFakeInstanceServer()
	tmpDir := t.TempDir()

	cfg1 := filepath.Join(tmpDir, "dev.yaml")
	_ = os.WriteFile(cfg1, []byte("name: dev-box\nimage: ubuntu:22.04\nstatus: present\ngroups: [dev]\n"), 0644)

	cfg2 := filepath.Join(tmpDir, "prod.yaml")
	_ = os.WriteFile(cfg2, []byte("name: prod-box\nimage: ubuntu:22.04\nstatus: present\ngroups: [prod]\n"), 0644)

	// Test --group dev (space syntax)
	var stdout, stderr bytes.Buffer
	code := run([]string{"apply", tmpDir, "--group", "dev"}, &stdout, &stderr, fake)
	if code != 0 {
		t.Fatalf("run(apply --group dev) returned %d, want 0. Stderr: %s", code, stderr.String())
	}
	if _, _, err := fake.GetInstance("dev-box"); err != nil {
		t.Errorf("dev-box should be created")
	}
	if _, _, err := fake.GetInstance("prod-box"); err == nil {
		t.Errorf("prod-box should not be created under --group dev")
	}

	// Test --group=prod (equals syntax)
	stdout.Reset()
	stderr.Reset()
	code = run([]string{"apply", tmpDir, "--group=prod"}, &stdout, &stderr, fake)
	if code != 0 {
		t.Fatalf("run(apply --group=prod) returned %d, want 0. Stderr: %s", code, stderr.String())
	}
	if _, _, err := fake.GetInstance("prod-box"); err != nil {
		t.Errorf("prod-box should be created under --group=prod")
	}
}

func TestRun_Plan(t *testing.T) {
	fake := lxd.NewFakeInstanceServer()
	tmpDir := t.TempDir()
	cfgFile := filepath.Join(tmpDir, "test.yaml")
	_ = os.WriteFile(cfgFile, []byte("name: test\nimage: ubuntu:22.04\nstatus: present\n"), 0644)

	t.Run("text format", func(t *testing.T) {
		var stdout, stderr bytes.Buffer
		code := run([]string{"plan", tmpDir}, &stdout, &stderr, fake)
		if code != 0 {
			t.Errorf("plan returned %d, want 0", code)
		}
		if !strings.Contains(stdout.String(), "Plan:") {
			t.Errorf("stdout missing Plan header: %q", stdout.String())
		}
	})

	t.Run("json format", func(t *testing.T) {
		var stdout, stderr bytes.Buffer
		code := run([]string{"plan", tmpDir, "--format", "json"}, &stdout, &stderr, fake)
		if code != 0 {
			t.Errorf("plan --format json returned %d, want 0", code)
		}
		var env output.Envelope
		if err := json.Unmarshal(stdout.Bytes(), &env); err != nil {
			t.Fatalf("failed to unmarshal JSON result envelope: %v. Output: %s", err, stdout.String())
		}
		if env.Schema != "lxm/result/v1" {
			t.Errorf("schema = %q, want lxm/result/v1", env.Schema)
		}
	})

	t.Run("invalid format", func(t *testing.T) {
		var stdout, stderr bytes.Buffer
		code := run([]string{"plan", tmpDir, "--format", "yaml"}, &stdout, &stderr, fake)
		if code != 2 {
			t.Errorf("plan --format yaml returned %d, want 2", code)
		}
	})
}

func TestRun_ScriptAndRunAs(t *testing.T) {
	fake := lxd.NewFakeInstanceServer()
	_ = fake.CreateInstance(api.InstancesPost{Name: "box1"})

	tmpDir := t.TempDir()
	scriptFile := filepath.Join(tmpDir, "setup.sh")
	if err := os.WriteFile(scriptFile, []byte("#!/bin/bash\necho ok\n"), 0755); err != nil {
		t.Fatalf("failed to write script file: %v", err)
	}

	t.Run("script with --run-as flag", func(t *testing.T) {
		var stdout, stderr bytes.Buffer
		code := run([]string{"script", "box1", scriptFile, "--run-as", "ubuntu"}, &stdout, &stderr, fake)
		if code != 0 {
			t.Errorf("script with --run-as returned %d, want 0. Stderr: %s", code, stderr.String())
		}
	})

	t.Run("script with positional user", func(t *testing.T) {
		var stdout, stderr bytes.Buffer
		code := run([]string{"script", "box1", scriptFile, "ubuntu"}, &stdout, &stderr, fake)
		if code != 0 {
			t.Errorf("script with positional user returned %d, want 0. Stderr: %s", code, stderr.String())
		}
	})
}

func TestRun_SnapshotAndRollback(t *testing.T) {
	fake := lxd.NewFakeInstanceServer()
	_ = fake.CreateInstance(api.InstancesPost{Name: "box1"})

	t.Run("create_snapshot", func(t *testing.T) {
		var stdout, stderr bytes.Buffer
		code := run([]string{"snapshot", "box1", "user.lxm.snap.test1"}, &stdout, &stderr, fake)
		if code != 0 {
			t.Errorf("snapshot create returned %d, want 0. Stderr: %s", code, stderr.String())
		}
	})

	t.Run("list_snapshot", func(t *testing.T) {
		var stdout, stderr bytes.Buffer
		code := run([]string{"snapshot", "box1"}, &stdout, &stderr, fake)
		if code != 0 {
			t.Errorf("snapshot list returned %d, want 0", code)
		}
		if !strings.Contains(stdout.String(), "user.lxm.snap.test1") {
			t.Errorf("snapshot list output missing snapshot: %s", stdout.String())
		}
	})

	t.Run("snapshot_gc_dryrun", func(t *testing.T) {
		var stdout, stderr bytes.Buffer
		code := run([]string{"snapshot", "box1", "--gc", "--dry-run", "--older-than", "0s"}, &stdout, &stderr, fake)
		if code != 0 {
			t.Errorf("snapshot --gc --dry-run returned %d, want 0", code)
		}
		if !strings.Contains(stdout.String(), "[DRY-RUN]") {
			t.Errorf("expected dry-run banner in output: %s", stdout.String())
		}
	})

	t.Run("snapshot_gc_prune", func(t *testing.T) {
		var stdout, stderr bytes.Buffer
		code := run([]string{"snapshot", "box1", "--gc", "--older-than", "0s"}, &stdout, &stderr, fake)
		if code != 0 {
			t.Errorf("snapshot --gc returned %d, want 0", code)
		}
		if !strings.Contains(stdout.String(), "Pruned 1 snapshot") {
			t.Errorf("expected 1 snapshot pruned: %s", stdout.String())
		}
	})

	t.Run("rollback", func(t *testing.T) {
		var stdout, stderr bytes.Buffer
		code := run([]string{"rollback", "box1", "user.lxm.snap.test1"}, &stdout, &stderr, fake)
		if code != 0 {
			t.Errorf("rollback returned %d, want 0", code)
		}
	})
}

func TestRun_CompileAndDoctor(t *testing.T) {
	fake := lxd.NewFakeInstanceServer()
	tmpDir := t.TempDir()
	cfgFile := filepath.Join(tmpDir, "dev.yaml")
	_ = os.WriteFile(cfgFile, []byte("name: dev-box\nimage: ubuntu:22.04\nstatus: present\n"), 0644)

	t.Run("compile", func(t *testing.T) {
		var stdout, stderr bytes.Buffer
		code := run([]string{"compile", tmpDir}, &stdout, &stderr, fake)
		if code != 0 {
			t.Errorf("compile returned %d, want 0. Stderr: %s", code, stderr.String())
		}
	})

	t.Run("doctor", func(t *testing.T) {
		var stdout, stderr bytes.Buffer
		code := run([]string{"doctor"}, &stdout, &stderr, fake)
		if code != 0 {
			t.Errorf("doctor returned %d, want 0", code)
		}
		if !strings.Contains(stdout.String(), "[OK]") {
			t.Errorf("doctor stdout missing OK checks: %q", stdout.String())
		}
	})
}

func TestRun_Include(t *testing.T) {
	fake := lxd.NewFakeInstanceServer()
	tmpDir := t.TempDir()
	cfgFile := filepath.Join(tmpDir, "dev.yaml")
	_ = os.WriteFile(cfgFile, []byte("name: dev-box\nimage: ubuntu:22.04\nstatus: present\n"), 0644)

	var stdout, stderr bytes.Buffer
	code := run([]string{"include", tmpDir, "_base.yaml", "--dry-run"}, &stdout, &stderr, fake)
	if code != 0 {
		t.Errorf("include returned %d, want 0", code)
	}
}

func TestHasAnyGroup(t *testing.T) {
	tests := []struct {
		name    string
		groups  []string
		targets []string
		want    bool
	}{
		{"single match", []string{"dev", "gpu"}, []string{"dev"}, true},
		{"no match", []string{"dev", "gpu"}, []string{"staging"}, false},
		{"multi-target OR - first matches", []string{"dev"}, []string{"dev", "staging"}, true},
		{"multi-target OR - second matches", []string{"staging"}, []string{"dev", "staging"}, true},
		{"multi-target OR - none match", []string{"prod"}, []string{"dev", "staging"}, false},
		{"empty groups", []string{}, []string{"dev"}, false},
		{"empty targets", []string{"dev"}, []string{}, false},
		{"both empty", []string{}, []string{}, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := hasAnyGroup(tt.groups, tt.targets)
			if got != tt.want {
				t.Errorf("hasAnyGroup(%v, %v) = %v, want %v", tt.groups, tt.targets, got, tt.want)
			}
		})
	}
}

func TestShouldSkipByGroup(t *testing.T) {
	tests := []struct {
		name           string
		groupFilters   []string
		excludeFilters []string
		groups         []string
		want           bool
	}{
		{"no filters", nil, nil, []string{"dev"}, false},
		{"include match", []string{"dev"}, nil, []string{"dev"}, false},
		{"include no match", []string{"staging"}, nil, []string{"dev"}, true},
		{"exclude match", nil, []string{"dev"}, []string{"dev"}, true},
		{"exclude no match", nil, []string{"staging"}, []string{"dev"}, false},
		{"include match, exclude no match", []string{"dev"}, []string{"staging"}, []string{"dev"}, false},
		{"include match, exclude match", []string{"dev"}, []string{"dev"}, []string{"dev"}, true},
		{"include no match", []string{"staging"}, []string{"prod"}, []string{"dev"}, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := shouldSkipByGroup(tt.groups, tt.groupFilters, tt.excludeFilters)
			if got != tt.want {
				t.Errorf("shouldSkipByGroup(%v) with groupFilters=%v excludeFilters=%v = %v, want %v",
					tt.groups, tt.groupFilters, tt.excludeFilters, got, tt.want)
			}
		})
	}
}

func TestRun_FormatJSON_Success(t *testing.T) {
	tmpDir := t.TempDir()
	var stdout, stderr bytes.Buffer
	code := run([]string{"compile", tmpDir, "--format", "json"}, &stdout, &stderr, nil)
	if code != 0 {
		t.Fatalf("compile --format json returned %d, want 0. Stderr: %s", code, stderr.String())
	}

	var env output.Envelope
	if err := json.Unmarshal(stdout.Bytes(), &env); err != nil {
		t.Fatalf("failed to parse JSON envelope from stdout: %v. Output: %s", err, stdout.String())
	}

	if env.Schema != "lxm/result/v1" {
		t.Errorf("schema = %q, want lxm/result/v1", env.Schema)
	}
	if env.Command != "compile" {
		t.Errorf("command = %q, want compile", env.Command)
	}
	if !env.OK {
		t.Errorf("ok = %v, want true", env.OK)
	}
	if env.ExitCode != 0 {
		t.Errorf("exit_code = %d, want 0", env.ExitCode)
	}
}

func TestRun_FormatJSON_Errors(t *testing.T) {
	t.Run("Usage Error Exit 2", func(t *testing.T) {
		var stdout, stderr bytes.Buffer
		code := run([]string{"apply", "--invalid-flag", "--format", "json"}, &stdout, &stderr, nil)
		if code != 2 {
			t.Fatalf("returned exit code %d, want 2", code)
		}
		var env output.Envelope
		if err := json.Unmarshal(stdout.Bytes(), &env); err != nil {
			t.Fatalf("failed to parse JSON envelope: %v. Output: %s", err, stdout.String())
		}
		if env.OK || env.ExitCode != 2 || len(env.Errors) == 0 || env.Errors[0].Code != "USAGE_ERROR" {
			t.Errorf("unexpected envelope for exit 2: %+v", env)
		}
	})

	t.Run("Config Error Exit 3", func(t *testing.T) {
		tmpDir := t.TempDir()
		cfgFile := filepath.Join(tmpDir, "invalid.yaml")
		_ = os.WriteFile(cfgFile, []byte("name: test\nimage: ubuntu:22.04\nstatus: invalid-status\n"), 0644)

		var stdout, stderr bytes.Buffer
		code := run([]string{"apply", cfgFile, "--format", "json"}, &stdout, &stderr, nil)
		if code != 3 {
			t.Fatalf("returned exit code %d, want 3", code)
		}
		var env output.Envelope
		if err := json.Unmarshal(stdout.Bytes(), &env); err != nil {
			t.Fatalf("failed to parse JSON envelope: %v. Output: %s", err, stdout.String())
		}
		if env.OK || env.ExitCode != 3 || len(env.Errors) == 0 || env.Errors[0].Code != "CONFIG_ERROR" {
			t.Errorf("unexpected envelope for exit 3: %+v", env)
		}
	})

	t.Run("LXD Offline Error Exit 4", func(t *testing.T) {
		t.Setenv("LXD_SOCKET", "/nonexistent.sock")
		tmpDir := t.TempDir()
		cfgFile := filepath.Join(tmpDir, "dev.yaml")
		_ = os.WriteFile(cfgFile, []byte("name: dev-box\nimage: ubuntu:22.04\nstatus: present\n"), 0644)

		var stdout, stderr bytes.Buffer
		code := run([]string{"apply", cfgFile, "--format", "json"}, &stdout, &stderr, nil)
		if code != 4 {
			t.Fatalf("returned exit code %d, want 4", code)
		}
		var env output.Envelope
		if err := json.Unmarshal(stdout.Bytes(), &env); err != nil {
			t.Fatalf("failed to parse JSON envelope: %v. Output: %s", err, stdout.String())
		}
		if env.OK || env.ExitCode != 4 || len(env.Errors) == 0 || env.Errors[0].Code != "LXD_ERROR" {
			t.Errorf("unexpected envelope for exit 4: %+v", env)
		}
	})

	t.Run("Target Not Found Error Exit 5", func(t *testing.T) {
		fake := lxd.NewFakeInstanceServer()
		tmpDir := t.TempDir()
		scriptFile := filepath.Join(tmpDir, "test.sh")
		_ = os.WriteFile(scriptFile, []byte("#!/bin/bash\necho ok\n"), 0755)

		var stdout, stderr bytes.Buffer
		code := run([]string{"run", "non-existent-box", scriptFile, "--format", "json"}, &stdout, &stderr, fake)
		if code != 5 {
			t.Fatalf("returned exit code %d, want 5", code)
		}
		var env output.Envelope
		if err := json.Unmarshal(stdout.Bytes(), &env); err != nil {
			t.Fatalf("failed to parse JSON envelope: %v. Output: %s", err, stdout.String())
		}
		if env.OK || env.ExitCode != 5 || len(env.Errors) == 0 || env.Errors[0].Code != "TARGET_NOT_FOUND" {
			t.Errorf("unexpected envelope for exit 5: %+v", env)
		}
	})
}

func TestRun_FormatJSON_InteractiveCarveOut(t *testing.T) {
	fake := lxd.NewFakeInstanceServer()
	_ = fake.CreateInstance(api.InstancesPost{Name: "box1"})

	t.Run("shell rejects --format json", func(t *testing.T) {
		var stdout, stderr bytes.Buffer
		code := run([]string{"shell", "box1", "--format", "json"}, &stdout, &stderr, fake)
		if code != 2 {
			t.Fatalf("shell --format json returned exit code %d, want 2", code)
		}
		var env output.Envelope
		if err := json.Unmarshal(stdout.Bytes(), &env); err != nil {
			t.Fatalf("failed to parse JSON envelope: %v. Output: %s", err, stdout.String())
		}
		if env.ExitCode != 2 || len(env.Errors) == 0 || env.Errors[0].Code != "USAGE_ERROR" {
			t.Errorf("unexpected envelope for interactive carve-out: %+v", env)
		}
	})

	t.Run("ssh rejects --format json", func(t *testing.T) {
		var stdout, stderr bytes.Buffer
		code := run([]string{"--dry-run", "ssh", "box1", "--format", "json"}, &stdout, &stderr, fake)
		if code != 2 {
			t.Fatalf("ssh --format json returned exit code %d, want 2", code)
		}
		var env output.Envelope
		if err := json.Unmarshal(stdout.Bytes(), &env); err != nil {
			t.Fatalf("failed to parse JSON envelope: %v. Output: %s", err, stdout.String())
		}
		if env.ExitCode != 2 || len(env.Errors) == 0 || env.Errors[0].Code != "USAGE_ERROR" {
			t.Errorf("unexpected envelope for interactive carve-out: %+v", env)
		}
	})
}

func TestRun_FormatJSON_SingleDocument(t *testing.T) {
	tmpDir := t.TempDir()
	cfgFile := filepath.Join(tmpDir, "dev.yaml")
	_ = os.WriteFile(cfgFile, []byte("name: dev-box\nimage: ubuntu:22.04\nstatus: present\n"), 0644)

	t.Run("doctor --format json single document and empty target", func(t *testing.T) {
		var stdout, stderr bytes.Buffer
		code := run([]string{"doctor", "--skip-remote", "--format", "json"}, &stdout, &stderr, nil)
		if code != 0 {
			t.Fatalf("doctor returned %d, want 0", code)
		}
		var env output.Envelope
		if err := json.Unmarshal(stdout.Bytes(), &env); err != nil {
			t.Fatalf("failed to unmarshal single JSON document: %v. Output: %s", err, stdout.String())
		}
		if env.Command != "doctor" {
			t.Errorf("command = %q, want doctor", env.Command)
		}
		if env.Target != "" {
			t.Errorf("target = %q, want empty string (target leak check)", env.Target)
		}
	})

	t.Run("status --format json single document", func(t *testing.T) {
		fake := lxd.NewFakeInstanceServer()
		_ = fake.CreateInstance(api.InstancesPost{Name: "box1"})

		var stdout, stderr bytes.Buffer
		code := run([]string{"status", "box1", "--format", "json"}, &stdout, &stderr, fake)
		if code != 0 {
			t.Fatalf("status returned %d, want 0", code)
		}
		var env output.Envelope
		if err := json.Unmarshal(stdout.Bytes(), &env); err != nil {
			t.Fatalf("failed to unmarshal single JSON document for status: %v. Output: %s", err, stdout.String())
		}
		if env.Command != "status" {
			t.Errorf("command = %q, want status", env.Command)
		}
		if env.Target != "box1" {
			t.Errorf("target = %q, want box1", env.Target)
		}
		if len(env.Results) == 0 || env.Results[0].Container != "box1" {
			t.Errorf("expected non-empty results containing box1, got: %+v", env.Results)
		}
	})

	t.Run("completion bash --format json does not emit envelope", func(t *testing.T) {
		var stdout, stderr bytes.Buffer
		code := run([]string{"completion", "bash", "--format", "json"}, &stdout, &stderr, nil)
		if code != 0 {
			t.Fatalf("completion bash returned %d, want 0", code)
		}
		if strings.Contains(stdout.String(), `"schema": "lxm/result/v1"`) {
			t.Errorf("completion bash should not emit JSON result envelope: %s", stdout.String())
		}
	})

	t.Run("root-only --format json does not emit envelope", func(t *testing.T) {
		var stdout, stderr bytes.Buffer
		code := run([]string{"--format", "json"}, &stdout, &stderr, nil)
		if code != 0 {
			t.Fatalf("root-only --format json returned %d, want 0", code)
		}
		if strings.Contains(stdout.String(), `"schema": "lxm/result/v1"`) {
			t.Errorf("root-only --format json should not emit JSON result envelope: %s", stdout.String())
		}
	})

	t.Run("plan --format json single document and command resolution", func(t *testing.T) {
		var stdout, stderr bytes.Buffer
		code := run([]string{"--dry-run", "plan", cfgFile, "--format", "json"}, &stdout, &stderr, nil)
		if code != 0 {
			t.Fatalf("plan returned %d, want 0", code)
		}
		var env output.Envelope
		if err := json.Unmarshal(stdout.Bytes(), &env); err != nil {
			t.Fatalf("failed to unmarshal single JSON document: %v. Output: %s", err, stdout.String())
		}
		if env.Command != "plan" {
			t.Errorf("command = %q, want plan", env.Command)
		}
		if env.Target != cfgFile {
			t.Errorf("target = %q, want %q", env.Target, cfgFile)
		}
	})
}

func TestRun_SSH_RemoteCommandJSON_NotCarvedOut(t *testing.T) {
	fake := lxd.NewFakeInstanceServer()
	_ = fake.CreateInstance(api.InstancesPost{Name: "box1"})

	var stdout, stderr bytes.Buffer
	code := run([]string{"--dry-run", "ssh", "box1", "json"}, &stdout, &stderr, fake)
	if code != 0 {
		t.Fatalf("ssh box1 json returned %d, want 0 (should not be carved out). Stderr: %s", code, stderr.String())
	}
}

func TestRun_InvalidFormatFlag(t *testing.T) {
	tmpDir := t.TempDir()
	cfgFile := filepath.Join(tmpDir, "dev.yaml")
	_ = os.WriteFile(cfgFile, []byte("name: dev-box\nimage: ubuntu:22.04\nstatus: present\n"), 0644)

	var stdout, stderr bytes.Buffer
	code := run([]string{"apply", cfgFile, "--format", "yaml"}, &stdout, &stderr, nil)
	if code != 2 {
		t.Fatalf("apply --format yaml returned %d, want 2 (USAGE_ERROR)", code)
	}
}

func TestRun_SignalInterrupt(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // Pre-cancelled context

	var stdout, stderr bytes.Buffer
	code := runWithContext(ctx, []string{"compile", "."}, &stdout, &stderr, nil)
	if code != 1 {
		t.Fatalf("cancelled context run returned %d, want 1", code)
	}
}

func TestRun_List_SuccessAndFilter(t *testing.T) {
	fake := lxd.NewFakeInstanceServer()
	fake.Instances["c1"] = &api.Instance{
		Name:       "c1",
		Status:     "Running",
		StatusCode: api.Running,
		Config: map[string]string{
			"user.lxm.managed": "true",
			"user.lxm.groups":  "dev",
		},
	}
	fake.Instances["c2"] = &api.Instance{
		Name:       "c2",
		Status:     "Stopped",
		StatusCode: api.Stopped,
		Config: map[string]string{
			"user.lxm.managed": "true",
			"user.lxm.groups":  "prod",
		},
	}

	t.Run("list all text output", func(t *testing.T) {
		var stdout, stderr bytes.Buffer
		code := run([]string{"list"}, &stdout, &stderr, fake)
		if code != 0 {
			t.Fatalf("list returned %d, want 0. Stderr: %s", code, stderr.String())
		}
		out := stdout.String()
		if !strings.Contains(out, "c1") || !strings.Contains(out, "c2") {
			t.Errorf("expected c1 and c2 in list output, got:\n%s", out)
		}
	})

	t.Run("list filter group dev", func(t *testing.T) {
		var stdout, stderr bytes.Buffer
		code := run([]string{"list", "--group", "dev"}, &stdout, &stderr, fake)
		if code != 0 {
			t.Fatalf("list --group dev returned %d, want 0", code)
		}
		out := stdout.String()
		if !strings.Contains(out, "c1") || strings.Contains(out, "c2") {
			t.Errorf("expected only c1 in output, got:\n%s", out)
		}
	})

	t.Run("list filter no match exits 5", func(t *testing.T) {
		var stdout, stderr bytes.Buffer
		code := run([]string{"list", "--group", "nonexistent"}, &stdout, &stderr, fake)
		if code != 5 {
			t.Fatalf("list --group nonexistent returned %d, want 5 (TARGET_ERROR)", code)
		}
	})
}

func TestRun_Status_SuccessAndNotFound(t *testing.T) {
	fake := lxd.NewFakeInstanceServer()
	fake.Instances["dev1"] = &api.Instance{
		Name:         "dev1",
		Status:       "Running",
		StatusCode:   api.Running,
		Architecture: "x86_64",
		Config: map[string]string{
			"user.lxm.managed":           "true",
			"user.lxm.groups":            "dev",
			"user.lxm.recipe.setup.hash": "hash123",
		},
	}

	t.Run("status dev1 text", func(t *testing.T) {
		var stdout, stderr bytes.Buffer
		code := run([]string{"status", "dev1"}, &stdout, &stderr, fake)
		if code != 0 {
			t.Fatalf("status dev1 returned %d, want 0", code)
		}
		out := stdout.String()
		if !strings.Contains(out, "dev1") || !strings.Contains(out, "hash123") {
			t.Errorf("expected dev1 and hash123 in status output, got:\n%s", out)
		}
	})

	t.Run("status nonexistent exits 5", func(t *testing.T) {
		var stdout, stderr bytes.Buffer
		code := run([]string{"status", "ghost"}, &stdout, &stderr, fake)
		if code != 5 {
			t.Fatalf("status ghost returned %d, want 5 (TARGET_ERROR)", code)
		}
	})
}

func TestRun_Init_OverwriteGuardAndForce(t *testing.T) {
	tmpDir := t.TempDir()

	t.Run("first init succeeds", func(t *testing.T) {
		var stdout, stderr bytes.Buffer
		code := run([]string{"init", tmpDir}, &stdout, &stderr, nil)
		if code != 0 {
			t.Fatalf("init returned %d, want 0. Stderr: %s", code, stderr.String())
		}
		if _, err := os.Stat(filepath.Join(tmpDir, "_base.yaml")); err != nil {
			t.Errorf("_base.yaml missing after init")
		}
		// Assert scaffolded config can be loaded and validated cleanly
		devPath := filepath.Join(tmpDir, "config", "dev.yaml")
		conf, err := config.LoadConfig(devPath)
		if err != nil {
			t.Fatalf("scaffolded config failed to load: %v", err)
		}
		if err := conf.Validate(filepath.Join(tmpDir, "config")); err != nil {
			t.Fatalf("scaffolded config failed validation: %v", err)
		}
	})

	t.Run("second init without force fails exit 2", func(t *testing.T) {
		var stdout, stderr bytes.Buffer
		code := run([]string{"init", tmpDir}, &stdout, &stderr, nil)
		if code != 2 {
			t.Fatalf("second init without --force returned %d, want 2 (USAGE_ERROR)", code)
		}
	})

	t.Run("second init with force succeeds", func(t *testing.T) {
		var stdout, stderr bytes.Buffer
		code := run([]string{"--force", "init", tmpDir}, &stdout, &stderr, nil)
		if code != 0 {
			t.Fatalf("init --force returned %d, want 0. Stderr: %s", code, stderr.String())
		}
	})
}

func TestRun_Compile_MigrationAndInPlace(t *testing.T) {
	tmpDir := t.TempDir()
	v1File := filepath.Join(tmpDir, "dev.yaml")
	v1Content := `name: dev-box
image: ubuntu:22.04
wait: true
`
	_ = os.WriteFile(v1File, []byte(v1Content), 0644)

	t.Run("compile non-destructive", func(t *testing.T) {
		var stdout, stderr bytes.Buffer
		code := run([]string{"compile", tmpDir}, &stdout, &stderr, nil)
		if code != 0 {
			t.Fatalf("compile returned %d, want 0. Stderr: %s", code, stderr.String())
		}
		compiledFile := filepath.Join(tmpDir, ".lxm", "compiled", "dev.yaml")
		raw, err := os.ReadFile(compiledFile)
		if err != nil {
			t.Fatalf("failed to read compiled file: %v", err)
		}
		if !strings.Contains(string(raw), "schema: lxm/config/v2") {
			t.Errorf("compiled manifest missing schema: lxm/config/v2")
		}
	})

	t.Run("compile in-place", func(t *testing.T) {
		var stdout, stderr bytes.Buffer
		code := run([]string{"compile", tmpDir, "--in-place"}, &stdout, &stderr, nil)
		if code != 0 {
			t.Fatalf("compile --in-place returned %d, want 0", code)
		}
		raw, _ := os.ReadFile(v1File)
		if !strings.Contains(string(raw), "schema: lxm/config/v2") {
			t.Errorf("in-place compiled manifest missing schema: lxm/config/v2")
		}
	})
}

func TestRun_Doctor_Diagnostics(t *testing.T) {
	tmpDir := t.TempDir()
	fake := lxd.NewFakeInstanceServer()

	var stdout, stderr bytes.Buffer
	code := run([]string{"doctor", tmpDir}, &stdout, &stderr, fake)
	if code != 0 {
		t.Fatalf("doctor returned %d, want 0. Stderr: %s", code, stderr.String())
	}
	out := stdout.String()
	if !strings.Contains(out, "LXD socket reachable") || !strings.Contains(out, "Kernel idmapped mounts") {
		t.Errorf("doctor stdout missing expected checks: %q", out)
	}
}

func TestRun_Doctor_NoFalsePositives(t *testing.T) {
	// B6: doctor must not flag base manifests (schema: lxm/config/v2, not
	// standalone-loadable by design) or unrelated YAML as un-migrated.
	tmpDir := t.TempDir()
	os.WriteFile(filepath.Join(tmpDir, "_base.yaml"), []byte("schema: lxm/config/v2\nbase: true\nuser: ubuntu\n"), 0644)
	os.WriteFile(filepath.Join(tmpDir, "dev.yaml"), []byte("schema: lxm/config/v2\nname: dev\nimage: ubuntu:22.04\nuser: ubuntu\n"), 0644)
	os.WriteFile(filepath.Join(tmpDir, "mkdocs.yml"), []byte("site_name: demo\nnav:\n  - Home: index.md\n"), 0644)
	os.WriteFile(filepath.Join(tmpDir, "Taskfile.yml"), []byte("version: 3\ntasks:\n  build:\n    cmds: [go build]\n"), 0644)

	var stdout, stderr bytes.Buffer
	code := run([]string{"doctor", "--skip-remote", tmpDir}, &stdout, &stderr, nil)
	if code != 0 {
		t.Fatalf("doctor returned %d, want 0. Stderr: %s", code, stderr.String())
	}
	if strings.Contains(stderr.String(), "Un-migrated") {
		t.Errorf("doctor flagged migrated/base/unrelated YAML as un-migrated:\n%s", stderr.String())
	}
	if !strings.Contains(stdout.String(), "All discovered configs migrated") {
		t.Errorf("expected 'All discovered configs migrated' check, got:\n%s", stdout.String())
	}
}

func TestRun_Doctor_FlagsLegacyV1(t *testing.T) {
	// B6: a genuine legacy v1 manifest must still be flagged for migration.
	tmpDir := t.TempDir()
	os.WriteFile(filepath.Join(tmpDir, "legacy.yaml"), []byte("name: legacy\nimage: ubuntu:22.04\n"), 0644)

	var stdout, stderr bytes.Buffer
	code := run([]string{"doctor", "--skip-remote", tmpDir}, &stdout, &stderr, nil)
	if code != 0 {
		t.Fatalf("doctor returned %d, want 0. Stderr: %s", code, stderr.String())
	}
	if !strings.Contains(stderr.String(), "Un-migrated config (missing schema: lxm/config/v2):") {
		t.Errorf("expected legacy v1 manifest to be flagged as un-migrated, got stderr:\n%s", stderr.String())
	}
}

func TestRun_Doctor_ReportsLoadFailure(t *testing.T) {
	// B6: a v2 manifest that fails to load must report the real error, not the
	// misleading "missing schema" warning.
	tmpDir := t.TempDir()
	os.WriteFile(filepath.Join(tmpDir, "broken.yaml"), []byte("schema: lxm/config/v2\nname: broken\nimage: ubuntu:22.04\nuser: ubuntu\nstatus: bogus\n"), 0644)

	var stdout, stderr bytes.Buffer
	code := run([]string{"doctor", "--skip-remote", tmpDir}, &stdout, &stderr, nil)
	if code != 0 {
		t.Fatalf("doctor returned %d, want 0. Stderr: %s", code, stderr.String())
	}
	if !strings.Contains(stderr.String(), "fails to load:") {
		t.Errorf("expected a load-failure warning with the real error, got stderr:\n%s", stderr.String())
	}
	if strings.Contains(stderr.String(), "missing schema") {
		t.Errorf("v2 load failure misreported as missing schema:\n%s", stderr.String())
	}
}

func TestRun_Prune_OrphanGC(t *testing.T) {
	fake := lxd.NewFakeInstanceServer()

	// Pre-create orphan container with user.lxm.managed=true in fake LXD
	fake.CreateInstance(api.InstancesPost{
		Name: "orphan-box",
		InstancePut: api.InstancePut{
			Config: map[string]string{
				"user.lxm.managed": "true",
				"user.lxm.groups":  "dev",
				"user.lxm.user":    "ubuntu",
			},
		},
	})
	fake.UpdateInstanceState("orphan-box", "start", false)

	tmpDir := t.TempDir()
	keeperPath := filepath.Join(tmpDir, "keeper.yaml")
	_ = os.WriteFile(keeperPath, []byte(`schema: lxm/config/v2
name: keeper-box
image: ubuntu:22.04
status: present
groups: [dev]
`), 0644)

	t.Run("plan --prune previews delete step for orphan", func(t *testing.T) {
		var stdout, stderr bytes.Buffer
		code := run([]string{"plan", tmpDir, "--prune", "--format", "json"}, &stdout, &stderr, fake)
		if code != 0 {
			t.Fatalf("plan --prune returned %d, want 0. Stderr: %s", code, stderr.String())
		}
		var env output.Envelope
		if err := json.Unmarshal(stdout.Bytes(), &env); err != nil {
			t.Fatalf("unmarshaling json envelope: %v", err)
		}
		foundOrphanDelete := false
		var steps []plan.Step
		stepsBytes, _ := json.Marshal(env.Plan.Steps)
		_ = json.Unmarshal(stepsBytes, &steps)
		for _, step := range steps {
			if step.Container == "orphan-box" && step.Action == "delete" {
				foundOrphanDelete = true
			}
		}
		if !foundOrphanDelete {
			t.Errorf("expected delete step for orphan-box in plan --prune, got plan: %+v", env.Plan)
		}
	})

	t.Run("apply --prune deletes orphan container", func(t *testing.T) {
		var stdout, stderr bytes.Buffer
		code := run([]string{"apply", tmpDir, "--prune"}, &stdout, &stderr, fake)
		if code != 0 {
			t.Fatalf("apply --prune returned %d, want 0. Stderr: %s", code, stderr.String())
		}
		inst, _, err := fake.GetInstance("orphan-box")
		if err == nil && inst != nil {
			t.Errorf("expected orphan-box to be deleted by apply --prune, but it still exists")
		}
	})
}

func TestRun_SSH_SecurityPosture(t *testing.T) {
	khFile := filepath.Join(t.TempDir(), "known_hosts")
	t.Setenv("LXM_KNOWN_HOSTS_FILE", khFile)

	fake := lxd.NewFakeInstanceServer()
	_ = fake.CreateInstance(api.InstancesPost{
		Name: "secure-box",
		InstancePut: api.InstancePut{
			Config: map[string]string{
				"user.lxm.user":    "ubuntu",
				"user.lxm.managed": "true",
			},
		},
	})
	fake.IPs["secure-box"] = "10.0.0.50"

	t.Run("B1: dry-run does not write to known_hosts", func(t *testing.T) {
		var stdout, stderr bytes.Buffer
		code := run([]string{"--dry-run", "ssh", "secure-box"}, &stdout, &stderr, fake)
		if code != 0 {
			t.Fatalf("ssh dry-run returned %d, want 0. Stderr: %s", code, stderr.String())
		}
		if _, err := os.Stat(khFile); !os.IsNotExist(err) {
			t.Errorf("expected known_hosts file NOT to be created by dry-run")
		}
	})

	t.Run("B2: stopped container with no IPv4 returns exit code 6", func(t *testing.T) {
		_ = fake.CreateInstance(api.InstancesPost{
			Name: "stopped-box",
			InstancePut: api.InstancePut{
				Config: map[string]string{"user.lxm.user": "ubuntu"},
			},
		})
		fake.Instances["stopped-box"].Status = "Stopped"
		fake.Instances["stopped-box"].StatusCode = api.Stopped
		delete(fake.IPs, "stopped-box")

		var stdout, stderr bytes.Buffer
		code := run([]string{"ssh", "stopped-box"}, &stdout, &stderr, fake)
		if code != 6 {
			t.Fatalf("ssh stopped-box returned %d, want 6. Stderr: %s", code, stderr.String())
		}
		if !strings.Contains(stderr.String(), "has no IPv4 address") {
			t.Errorf("expected error message for missing IPv4, got: %s", stderr.String())
		}
	})

	t.Run("B3: space-separated option passthrough preserved intact", func(t *testing.T) {
		var stdout, stderr bytes.Buffer
		code := run([]string{"--dry-run", "ssh", "secure-box", "-o", "ServerAliveInterval=5", "-p", "2222"}, &stdout, &stderr, fake)
		if code != 0 {
			t.Fatalf("ssh with options returned %d, want 0. Stderr: %s", code, stderr.String())
		}
		outStr := stdout.String()
		if !strings.Contains(outStr, "ubuntu@secure-box -o ServerAliveInterval=5 -p 2222") {
			t.Errorf("expected preserved option ordering in dry-run stdout, got: %s", outStr)
		}
	})

	t.Run("R1: -o StrictHostKeyChecking=off/OFF/No variants emit warning", func(t *testing.T) {
		bypassVariants := [][]string{
			{"--dry-run", "ssh", "secure-box", "-oStrictHostKeyChecking=off"},
			{"--dry-run", "ssh", "secure-box", "-o", "StrictHostKeyChecking=OFF"},
			{"--dry-run", "ssh", "secure-box", "-oStrictHostKeyChecking=No"},
			{"--dry-run", "ssh", "secure-box", "-o", "UserKnownHostsFile=/dev/null"},
		}
		for _, v := range bypassVariants {
			var stdout, stderr bytes.Buffer
			code := run(v, &stdout, &stderr, fake)
			if code != 0 {
				t.Fatalf("ssh with %v returned %d, want 0. Stderr: %s", v, code, stderr.String())
			}
			if !strings.Contains(stderr.String(), "WARNING: Host key verification disabled via -o flag") {
				t.Errorf("expected warning in stderr for %v, got: %s", v, stderr.String())
			}
		}

		benignVariants := [][]string{
			{"--dry-run", "ssh", "secure-box", "-o", "Port=2222"},
			{"--dry-run", "ssh", "secure-box", "-oStrictHostKeyChecking=yes"},
			{"--dry-run", "ssh", "secure-box", "-o", "UserKnownHostsFile=/tmp/my_kh"},
			{"--dry-run", "ssh", "secure-box", "echo", "stricthostkeychecking=off"},
		}
		for _, v := range benignVariants {
			var stdout, stderr bytes.Buffer
			code := run(v, &stdout, &stderr, fake)
			if code != 0 {
				t.Fatalf("ssh with benign %v returned %d, want 0. Stderr: %s", v, code, stderr.String())
			}
			if strings.Contains(stderr.String(), "WARNING: Host key verification disabled") {
				t.Errorf("expected NO warning for benign %v, but got: %s", v, stderr.String())
			}
			if !strings.Contains(stdout.String(), "StrictHostKeyChecking=yes") {
				t.Errorf("expected StrictHostKeyChecking=yes in dry-run stdout for benign %v, got: %s", v, stdout.String())
			}
		}
	})

	t.Run("R2: options before name parses container name correctly", func(t *testing.T) {
		var stdout, stderr bytes.Buffer
		code := run([]string{"--dry-run", "ssh", "-o", "ServerAliveInterval=5", "secure-box"}, &stdout, &stderr, fake)
		if code != 0 {
			t.Fatalf("ssh options-before-name returned %d, want 0. Stderr: %s", code, stderr.String())
		}
		if !strings.Contains(stdout.String(), "ubuntu@secure-box") {
			t.Errorf("expected container name 'secure-box' to be parsed correctly, got: %s", stdout.String())
		}
	})

	t.Run("R5: keyscan failure on unreachable IP returns exit code 6", func(t *testing.T) {
		_ = fake.CreateInstance(api.InstancesPost{
			Name: "unreachable-box",
			InstancePut: api.InstancePut{
				Config: map[string]string{"user.lxm.user": "ubuntu"},
			},
		})
		fake.Instances["unreachable-box"].Status = "Running"
		fake.Instances["unreachable-box"].StatusCode = api.Running
		fake.IPs["unreachable-box"] = "192.0.2.1" // Non-routable documentation IP -> keyscan fails

		var stdout, stderr bytes.Buffer
		code := run([]string{"ssh", "unreachable-box"}, &stdout, &stderr, fake)
		if code != 6 {
			t.Fatalf("ssh unreachable-box returned %d, want 6. Stderr: %s", code, stderr.String())
		}
		if !strings.Contains(stderr.String(), "host key registration failed") {
			t.Errorf("expected host key registration error message, got: %s", stderr.String())
		}
	})

	t.Run("R9: IP resolution from running instance State.Network", func(t *testing.T) {
		_ = fake.CreateInstance(api.InstancesPost{
			Name: "running-box",
			InstancePut: api.InstancePut{
				Config: map[string]string{"user.lxm.user": "ubuntu"},
			},
		})
		fake.Instances["running-box"].Status = "Running"
		fake.Instances["running-box"].StatusCode = api.Running
		fake.IPs["running-box"] = "10.0.0.99"

		var stdout, stderr bytes.Buffer
		code := run([]string{"--dry-run", "ssh", "running-box"}, &stdout, &stderr, fake)
		if code != 0 {
			t.Fatalf("ssh running-box returned %d, want 0. Stderr: %s", code, stderr.String())
		}
		if !strings.Contains(stdout.String(), "HostName=10.0.0.99") {
			t.Errorf("expected resolved IP HostName=10.0.0.99 in dry-run stdout, got: %s", stdout.String())
		}
	})

	t.Run("B5: --format JSON case variant returns exit code 2", func(t *testing.T) {
		var stdout, stderr bytes.Buffer
		code := run([]string{"--format", "JSON", "ssh", "secure-box"}, &stdout, &stderr, fake)
		if code != 2 {
			t.Fatalf("ssh --format JSON returned %d, want 2. Stderr: %s", code, stderr.String())
		}
	})

	t.Run("ssh with --insecure emits warning", func(t *testing.T) {
		var stdout, stderr bytes.Buffer
		code := run([]string{"--dry-run", "ssh", "secure-box", "--insecure"}, &stdout, &stderr, fake)
		if code != 0 {
			t.Fatalf("ssh --insecure returned %d, want 0. Stderr: %s", code, stderr.String())
		}
		if !strings.Contains(stderr.String(), "WARNING: Host key verification disabled by --insecure flag") {
			t.Errorf("expected warning in stderr, got: %s", stderr.String())
		}
		if !strings.Contains(stdout.String(), "StrictHostKeyChecking=no") {
			t.Errorf("expected StrictHostKeyChecking=no in dry-run stdout, got: %s", stdout.String())
		}
	})

	t.Run("ssh secure mode uses StrictHostKeyChecking=yes", func(t *testing.T) {
		var stdout, stderr bytes.Buffer
		code := run([]string{"--dry-run", "ssh", "secure-box"}, &stdout, &stderr, fake)
		if code != 0 {
			t.Fatalf("ssh returned %d, want 0. Stderr: %s", code, stderr.String())
		}
		if !strings.Contains(stdout.String(), "StrictHostKeyChecking=yes") {
			t.Errorf("expected StrictHostKeyChecking=yes in dry-run stdout, got: %s", stdout.String())
		}
		if !strings.Contains(stdout.String(), "known_hosts") {
			t.Errorf("expected UserKnownHostsFile to point to known_hosts, got: %s", stdout.String())
		}
	})

	t.Run("ssh nonexistent container returns exit code 5", func(t *testing.T) {
		var stdout, stderr bytes.Buffer
		code := run([]string{"ssh", "nonexistent-box"}, &stdout, &stderr, fake)
		if code != 5 {
			t.Fatalf("ssh nonexistent-box returned %d, want 5. Stderr: %s", code, stderr.String())
		}
	})

	t.Run("ssh --format json returns exit code 2", func(t *testing.T) {
		var stdout, stderr bytes.Buffer
		code := run([]string{"--format", "json", "ssh", "secure-box"}, &stdout, &stderr, fake)
		if code != 2 {
			t.Fatalf("ssh --format json returned %d, want 2. Stderr: %s", code, stderr.String())
		}
	})
}
