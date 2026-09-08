package main

import (
	"bytes"
	"encoding/json"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/aiyor/lxm/internal/output"
	"github.com/aiyor/lxm/internal/provider"
	"github.com/aiyor/lxm/internal/provider/fake"
)

func TestDiskGC_DryRun_PrintsPreview_DoesNotDelete(t *testing.T) {
	driver := fake.New()
	driver.AddVolume("default", "db-vm-orphan", "filesystem", map[string]string{
		"user.lxm.managed":  "true",
		"user.lxm.instance": "db-vm",
		"user.lxm.disk":     "orphan",
		"size":              "50GiB",
	})

	manifestDir := t.TempDir()
	manifestFile := filepath.Join(manifestDir, "db.yaml")
	content := `schema: lxm/config/v2
name: db-vm
type: vm
user: ubuntu
disks:
  - name: active
    size: 20GiB
    path: /var/lib/active
`
	if err := os.WriteFile(manifestFile, []byte(content), 0644); err != nil {
		t.Fatalf("writing manifest: %v", err)
	}

	var stdout, stderr bytes.Buffer
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	getSvc := func() (provider.Driver, error) { return driver, nil }

	rootCmd, _ := newRootCmd(t.Context(), &stdout, &stderr, getSvc, logger)
	rootCmd.SetArgs([]string{"disk", "gc", "--dry-run", manifestDir})

	if err := rootCmd.Execute(); err != nil {
		t.Fatalf("Execute error: %v (stderr: %s)", err, stderr.String())
	}

	out := stdout.String()
	if !strings.Contains(out, "ORPHANED MANAGED STORAGE VOLUMES") || !strings.Contains(out, "db-vm-orphan") {
		t.Errorf("expected preview table containing db-vm-orphan, got:\n%s", out)
	}
	if !strings.Contains(out, "[dry-run] No volumes were deleted.") {
		t.Errorf("expected dry-run note, got:\n%s", out)
	}

	// Verify volume still exists
	if _, exists := driver.Volumes["default"]["db-vm-orphan"]; !exists {
		t.Errorf("dry-run must not delete volume")
	}
}

func TestDiskGC_Force_DeletesOrphan_PreservesForeignAndReferenced(t *testing.T) {
	driver := fake.New()

	// 1. Referenced volume
	driver.AddVolume("default", "db-vm-active", "filesystem", map[string]string{"user.lxm.managed": "true"})
	// 2. Orphaned managed volume
	driver.AddVolume("default", "db-vm-orphan", "filesystem", map[string]string{"user.lxm.managed": "true"})
	// 3. Foreign unmanaged volume
	driver.AddVolume("default", "foreign-vol", "filesystem", map[string]string{}) // no user.lxm.managed marker

	manifestDir := t.TempDir()
	manifestFile := filepath.Join(manifestDir, "db.yaml")
	content := `schema: lxm/config/v2
name: db-vm
type: vm
user: ubuntu
disks:
  - name: active
    size: 20GiB
    path: /var/lib/active
`
	if err := os.WriteFile(manifestFile, []byte(content), 0644); err != nil {
		t.Fatalf("writing manifest: %v", err)
	}

	var stdout, stderr bytes.Buffer
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	getSvc := func() (provider.Driver, error) { return driver, nil }

	rootCmd, _ := newRootCmd(t.Context(), &stdout, &stderr, getSvc, logger)
	rootCmd.SetArgs([]string{"disk", "gc", "--force", manifestDir})

	if err := rootCmd.Execute(); err != nil {
		t.Fatalf("Execute error: %v (stderr: %s)", err, stderr.String())
	}

	// Verify orphan was deleted
	if _, exists := driver.Volumes["default"]["db-vm-orphan"]; exists {
		t.Errorf("expected db-vm-orphan deleted, but it still exists")
	}

	// Verify referenced was preserved
	if _, exists := driver.Volumes["default"]["db-vm-active"]; !exists {
		t.Errorf("expected db-vm-active preserved, but it was deleted")
	}

	// Verify foreign was preserved
	if _, exists := driver.Volumes["default"]["foreign-vol"]; !exists {
		t.Errorf("expected foreign-vol preserved, but it was deleted")
	}
}

func TestVSwitchGC_Force_DeletesOrphanACL_PreservesForeignAndReferenced(t *testing.T) {
	driver := fake.New()

	// 1. Referenced ACL
	driver.NetworkACLs["lxm-dmzbr0"] = &provider.NetworkACL{
		Name:   "lxm-dmzbr0",
		Config: map[string]string{"user.lxm.managed": "true"},
	}
	// 2. Orphaned managed ACL
	driver.NetworkACLs["lxm-orphanbr0"] = &provider.NetworkACL{
		Name:   "lxm-orphanbr0",
		Config: map[string]string{"user.lxm.managed": "true"},
	}
	// 3. Foreign unmanaged ACL
	driver.NetworkACLs["foreign-acl"] = &provider.NetworkACL{
		Name:   "foreign-acl",
		Config: map[string]string{}, // no user.lxm.managed
	}

	manifestDir := t.TempDir()
	manifestFile := filepath.Join(manifestDir, "net.yaml")
	content := `schema: lxm/config/v2
name: vm1
type: vm
user: ubuntu
vswitches:
  - name: dmzbr0
    ipv4: 10.20.0.1/24
`
	if err := os.WriteFile(manifestFile, []byte(content), 0644); err != nil {
		t.Fatalf("writing manifest: %v", err)
	}

	var stdout, stderr bytes.Buffer
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	getSvc := func() (provider.Driver, error) { return driver, nil }

	rootCmd, _ := newRootCmd(t.Context(), &stdout, &stderr, getSvc, logger)
	rootCmd.SetArgs([]string{"vswitch", "gc", "--force", manifestDir})

	if err := rootCmd.Execute(); err != nil {
		t.Fatalf("Execute error: %v (stderr: %s)", err, stderr.String())
	}

	// Verify orphan was deleted
	if _, exists := driver.NetworkACLs["lxm-orphanbr0"]; exists {
		t.Errorf("expected lxm-orphanbr0 deleted, but it still exists")
	}

	// Verify referenced was preserved
	if _, exists := driver.NetworkACLs["lxm-dmzbr0"]; !exists {
		t.Errorf("expected lxm-dmzbr0 preserved, but it was deleted")
	}

	// Verify foreign was preserved
	if _, exists := driver.NetworkACLs["foreign-acl"]; !exists {
		t.Errorf("expected foreign-acl preserved, but it was deleted")
	}
}

func TestDiskGC_LoadError_FailsClosed(t *testing.T) {
	driver := fake.New()
	driver.AddVolume("default", "db-vm-data", "filesystem", map[string]string{"user.lxm.managed": "true"})

	manifestDir := t.TempDir()
	brokenFile := filepath.Join(manifestDir, "broken.yaml")
	// Invalid YAML syntax
	if err := os.WriteFile(brokenFile, []byte("invalid: yaml: syntax: {"), 0644); err != nil {
		t.Fatalf("writing broken manifest: %v", err)
	}

	var stdout, stderr bytes.Buffer
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	getSvc := func() (provider.Driver, error) { return driver, nil }

	rootCmd, _ := newRootCmd(t.Context(), &stdout, &stderr, getSvc, logger)
	rootCmd.SetArgs([]string{"disk", "gc", "--force", manifestDir})

	err := rootCmd.Execute()
	if err == nil {
		t.Fatalf("expected error on broken manifest, got nil")
	}

	// Verify volume was NOT deleted (fail closed)
	if _, exists := driver.Volumes["default"]["db-vm-data"]; !exists {
		t.Errorf("expected volume preserved when manifest fails to load, but it was deleted")
	}
}

func TestDiskGC_MultiPool_ScansAllPools(t *testing.T) {
	driver := fake.New()
	// Add volumes across default and nvme pools
	driver.AddVolume("default", "vm1-orphan", "filesystem", map[string]string{"user.lxm.managed": "true"})
	driver.AddVolume("nvme", "vm2-orphan", "filesystem", map[string]string{"user.lxm.managed": "true"})

	manifestDir := t.TempDir()
	emptyManifest := filepath.Join(manifestDir, "empty.yaml")
	content := `schema: lxm/config/v2
name: vm3
type: vm
user: ubuntu
`
	if err := os.WriteFile(emptyManifest, []byte(content), 0644); err != nil {
		t.Fatalf("writing manifest: %v", err)
	}

	var stdout, stderr bytes.Buffer
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	getSvc := func() (provider.Driver, error) { return driver, nil }

	rootCmd, _ := newRootCmd(t.Context(), &stdout, &stderr, getSvc, logger)
	rootCmd.SetArgs([]string{"disk", "gc", "--force", manifestDir})

	if err := rootCmd.Execute(); err != nil {
		t.Fatalf("Execute error: %v (stderr: %s)", err, stderr.String())
	}

	// Verify orphans in both pools were cleaned
	if _, exists := driver.Volumes["default"]["vm1-orphan"]; exists {
		t.Errorf("expected default/vm1-orphan deleted, but it still exists")
	}
	if _, exists := driver.Volumes["nvme"]["vm2-orphan"]; exists {
		t.Errorf("expected nvme/vm2-orphan deleted, but it still exists")
	}
}

func TestDiskGC_Interactive_InputInjected(t *testing.T) {
	driver := fake.New()
	driver.AddVolume("default", "vm1-orphan", "filesystem", map[string]string{"user.lxm.managed": "true"})

	manifestDir := t.TempDir()
	emptyManifest := filepath.Join(manifestDir, "empty.yaml")
	content := `schema: lxm/config/v2
name: vm3
type: vm
user: ubuntu
`
	if err := os.WriteFile(emptyManifest, []byte(content), 0644); err != nil {
		t.Fatalf("writing manifest: %v", err)
	}

	var stdout, stderr bytes.Buffer
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	getSvc := func() (provider.Driver, error) { return driver, nil }

	rootCmd, _ := newRootCmd(t.Context(), &stdout, &stderr, getSvc, logger)
	rootCmd.SetIn(strings.NewReader("yes\n"))
	rootCmd.SetArgs([]string{"disk", "gc", manifestDir})

	if err := rootCmd.Execute(); err != nil {
		t.Fatalf("Execute error: %v (stderr: %s)", err, stderr.String())
	}

	// Verify orphan was deleted after confirmed prompt
	if _, exists := driver.Volumes["default"]["vm1-orphan"]; exists {
		t.Errorf("expected default/vm1-orphan deleted after interactive confirmation")
	}
}

func TestDiskGC_JSON_Output(t *testing.T) {
	driver := fake.New()
	driver.AddVolume("default", "db-vm-orphan", "filesystem", map[string]string{
		"user.lxm.managed":  "true",
		"user.lxm.instance": "db-vm",
		"user.lxm.disk":     "orphan",
		"size":              "50GiB",
	})

	manifestDir := t.TempDir()
	manifestFile := filepath.Join(manifestDir, "db.yaml")
	content := `schema: lxm/config/v2
name: db-vm
type: vm
user: ubuntu
`
	if err := os.WriteFile(manifestFile, []byte(content), 0644); err != nil {
		t.Fatalf("writing manifest: %v", err)
	}

	t.Run("dry-run produces clean json without text table", func(t *testing.T) {
		var stdout, stderr bytes.Buffer
		code := run([]string{"disk", "gc", "--dry-run", "--format", "json", manifestDir}, &stdout, &stderr, driver)
		if code != 0 {
			t.Fatalf("run returned %d, want 0. Stderr: %s", code, stderr.String())
		}
		if strings.Contains(stdout.String(), "ORPHANED MANAGED STORAGE VOLUMES") {
			t.Errorf("stdout must not contain plain text table: %s", stdout.String())
		}
		var env output.Envelope
		if err := json.Unmarshal(stdout.Bytes(), &env); err != nil {
			t.Fatalf("unmarshaling json envelope: %v. Output: %s", err, stdout.String())
		}
		if !env.OK || len(env.Results) != 1 {
			t.Errorf("expected 1 result item in envelope, got: %+v", env)
		}
		if env.Results[0].Container != "db-vm-orphan" || env.Results[0].Action != "delete_volume" {
			t.Errorf("unexpected result item: %+v", env.Results[0])
		}
	})

	t.Run("without force returns exit code 2 in json mode", func(t *testing.T) {
		var stdout, stderr bytes.Buffer
		code := run([]string{"disk", "gc", "--format", "json", manifestDir}, &stdout, &stderr, driver)
		if code != 2 {
			t.Fatalf("run returned %d, want 2. Stderr: %s", code, stderr.String())
		}
	})

	t.Run("with force deletes volume and returns clean json", func(t *testing.T) {
		var stdout, stderr bytes.Buffer
		code := run([]string{"disk", "gc", "--force", "--format", "json", manifestDir}, &stdout, &stderr, driver)
		if code != 0 {
			t.Fatalf("run returned %d, want 0. Stderr: %s", code, stderr.String())
		}
		var env output.Envelope
		if err := json.Unmarshal(stdout.Bytes(), &env); err != nil {
			t.Fatalf("unmarshaling json envelope: %v. Output: %s", err, stdout.String())
		}
		if !env.OK || len(env.Results) != 1 || !env.Results[0].Changed {
			t.Errorf("expected changed result item, got: %+v", env)
		}
		if _, exists := driver.Volumes["default"]["db-vm-orphan"]; exists {
			t.Errorf("expected volume deleted")
		}
	})
}

func TestVSwitchGC_JSON_Output(t *testing.T) {
	driver := fake.New()
	driver.NetworkACLs["lxm-orphaned-acl"] = &provider.NetworkACL{
		Name:        "lxm-orphaned-acl",
		Description: "Test ACL",
		Config: map[string]string{
			"user.lxm.managed": "true",
		},
	}

	manifestDir := t.TempDir()
	manifestFile := filepath.Join(manifestDir, "net.yaml")
	content := `schema: lxm/config/v2
name: dummy
`
	if err := os.WriteFile(manifestFile, []byte(content), 0644); err != nil {
		t.Fatalf("writing manifest: %v", err)
	}

	t.Run("dry-run produces clean json without text table", func(t *testing.T) {
		var stdout, stderr bytes.Buffer
		code := run([]string{"vswitch", "gc", "--dry-run", "--format", "json", manifestDir}, &stdout, &stderr, driver)
		if code != 0 {
			t.Fatalf("run returned %d, want 0. Stderr: %s", code, stderr.String())
		}
		if strings.Contains(stdout.String(), "ORPHANED MANAGED NETWORK ACLS") {
			t.Errorf("stdout must not contain plain text table: %s", stdout.String())
		}
		var env output.Envelope
		if err := json.Unmarshal(stdout.Bytes(), &env); err != nil {
			t.Fatalf("unmarshaling json envelope: %v. Output: %s", err, stdout.String())
		}
		if !env.OK || len(env.Results) != 1 {
			t.Errorf("expected 1 result item in envelope, got: %+v", env)
		}
	})

	t.Run("without force returns exit code 2 in json mode", func(t *testing.T) {
		var stdout, stderr bytes.Buffer
		code := run([]string{"vswitch", "gc", "--format", "json", manifestDir}, &stdout, &stderr, driver)
		if code != 2 {
			t.Fatalf("run returned %d, want 2. Stderr: %s", code, stderr.String())
		}
	})

	t.Run("with force deletes ACL and returns clean json", func(t *testing.T) {
		var stdout, stderr bytes.Buffer
		code := run([]string{"vswitch", "gc", "--force", "--format", "json", manifestDir}, &stdout, &stderr, driver)
		if code != 0 {
			t.Fatalf("run returned %d, want 0. Stderr: %s", code, stderr.String())
		}
		var env output.Envelope
		if err := json.Unmarshal(stdout.Bytes(), &env); err != nil {
			t.Fatalf("unmarshaling json envelope: %v. Output: %s", err, stdout.String())
		}
		if !env.OK || len(env.Results) != 1 || !env.Results[0].Changed {
			t.Errorf("expected changed result item, got: %+v", env)
		}
	})
}
