package config_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/aiyor/lxm/internal/config"
)

func writeConfigFile(t *testing.T, filename, content string) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, filename)
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatalf("writing %s: %v", filename, err)
	}
	return path
}

func TestLoadConfig_DiskStatusPresentDefault(t *testing.T) {
	manifest := `schema: lxm/config/v2
name: vm1
type: vm
user: ubuntu
disks:
  - name: data
    size: 20GiB
    path: /var/lib/data
`
	path := writeConfigFile(t, "vm.yaml", manifest)
	conf, err := config.LoadConfig(path)
	if err != nil {
		t.Fatalf("LoadConfig error: %v", err)
	}
	if len(conf.Disks) != 1 {
		t.Fatalf("expected 1 disk, got %d", len(conf.Disks))
	}
	d := conf.Disks[0]
	if d.Status != "present" {
		t.Errorf("expected default status 'present', got %q", d.Status)
	}
	if d.Attach == nil || !*d.Attach {
		t.Errorf("expected default attach true, got %v", d.Attach)
	}
}

func TestLoadConfig_DiskStatusAbsent_Valid(t *testing.T) {
	manifest := `schema: lxm/config/v2
name: vm1
type: vm
user: ubuntu
disks:
  - name: scratch
    status: absent
`
	path := writeConfigFile(t, "vm.yaml", manifest)
	conf, err := config.LoadConfig(path)
	if err != nil {
		t.Fatalf("LoadConfig error: %v", err)
	}
	if len(conf.Disks) != 1 {
		t.Fatalf("expected 1 disk, got %d", len(conf.Disks))
	}
	d := conf.Disks[0]
	if d.Status != "absent" {
		t.Errorf("expected status 'absent', got %q", d.Status)
	}
	if d.Source != "vm1-scratch" {
		t.Errorf("expected derived source 'vm1-scratch', got %q", d.Source)
	}
}

func TestLoadConfig_DiskStatusAbsent_AttachForbidden(t *testing.T) {
	manifest := `schema: lxm/config/v2
name: vm1
type: vm
user: ubuntu
disks:
  - name: scratch
    status: absent
    attach: false
`
	path := writeConfigFile(t, "vm.yaml", manifest)
	_, err := config.LoadConfig(path)
	if err == nil {
		t.Fatal("expected error when attach is specified with status: absent, got nil")
	}
	if !strings.Contains(err.Error(), "attach is not allowed when status is absent") {
		t.Errorf("unexpected error message: %v", err)
	}
}

func TestLoadConfig_DiskAttachFalse_Valid(t *testing.T) {
	manifest := `schema: lxm/config/v2
name: vm1
type: vm
user: ubuntu
disks:
  - name: data
    size: 20GiB
    path: /var/lib/data
    attach: false
`
	path := writeConfigFile(t, "vm.yaml", manifest)
	conf, err := config.LoadConfig(path)
	if err != nil {
		t.Fatalf("LoadConfig error: %v", err)
	}
	if len(conf.Disks) != 1 {
		t.Fatalf("expected 1 disk, got %d", len(conf.Disks))
	}
	d := conf.Disks[0]
	if d.Status != "present" {
		t.Errorf("expected status 'present', got %q", d.Status)
	}
	if d.Attach == nil || *d.Attach {
		t.Errorf("expected attach false, got %v", d.Attach)
	}
}

func TestLoadConfig_VSwitchStatusPresentDefault(t *testing.T) {
	manifest := `schema: lxm/config/v2
name: vm1
type: vm
user: ubuntu
vswitches:
  - name: dmzbr0
    ipv4: 10.20.0.1/24
`
	path := writeConfigFile(t, "vm.yaml", manifest)
	conf, err := config.LoadConfig(path)
	if err != nil {
		t.Fatalf("LoadConfig error: %v", err)
	}
	if len(conf.VSwitches) != 1 {
		t.Fatalf("expected 1 vswitch, got %d", len(conf.VSwitches))
	}
	vs := conf.VSwitches[0]
	if vs.Status != "present" {
		t.Errorf("expected default status 'present', got %q", vs.Status)
	}
}

func TestLoadConfig_VSwitchStatusAbsent_Valid(t *testing.T) {
	manifest := `schema: lxm/config/v2
name: vm1
type: vm
user: ubuntu
vswitches:
  - name: legacybr0
    status: absent
`
	path := writeConfigFile(t, "vm.yaml", manifest)
	conf, err := config.LoadConfig(path)
	if err != nil {
		t.Fatalf("LoadConfig error: %v", err)
	}
	if len(conf.VSwitches) != 1 {
		t.Fatalf("expected 1 vswitch, got %d", len(conf.VSwitches))
	}
	vs := conf.VSwitches[0]
	if vs.Status != "absent" {
		t.Errorf("expected status 'absent', got %q", vs.Status)
	}
}

func TestLoadConfig_VSwitchStatusPresent_IPv4Required(t *testing.T) {
	manifest := `schema: lxm/config/v2
name: vm1
type: vm
user: ubuntu
vswitches:
  - name: dmzbr0
    status: present
`
	path := writeConfigFile(t, "vm.yaml", manifest)
	_, err := config.LoadConfig(path)
	if err == nil {
		t.Fatal("expected error when ipv4 is missing for status: present, got nil")
	}
	if !strings.Contains(err.Error(), "ipv4 is required") {
		t.Errorf("unexpected error message: %v", err)
	}
}

func TestMergeConfigs_DisksNameIdentityOverride(t *testing.T) {
	baseDir := t.TempDir()
	baseFile := filepath.Join(baseDir, "_base.yaml")
	baseContent := `schema: lxm/config/v2
base: true
type: vm
user: ubuntu
disks:
  - name: data
    size: 50GiB
    path: /var/lib/data
  - name: logs
    size: 10GiB
    path: /var/log
`
	if err := os.WriteFile(baseFile, []byte(baseContent), 0644); err != nil {
		t.Fatalf("writing base: %v", err)
	}

	leafFile := filepath.Join(baseDir, "leaf.yaml")
	leafContent := `schema: lxm/config/v2
name: db-vm
include:
  - _base.yaml
disks:
  - name: data
    status: absent
`
	if err := os.WriteFile(leafFile, []byte(leafContent), 0644); err != nil {
		t.Fatalf("writing leaf: %v", err)
	}

	conf, err := config.LoadConfig(leafFile)
	if err != nil {
		t.Fatalf("LoadConfig error: %v", err)
	}

	if len(conf.Disks) != 2 {
		t.Fatalf("expected 2 disks after name-identity override, got %d: %+v", len(conf.Disks), conf.Disks)
	}

	// First disk 'data' should be overridden to status: absent
	if conf.Disks[0].Name != "data" || conf.Disks[0].Status != "absent" {
		t.Errorf("expected data disk overridden to status: absent, got %+v", conf.Disks[0])
	}
	// Second disk 'logs' should remain preserved
	if conf.Disks[1].Name != "logs" || conf.Disks[1].Status != "present" || conf.Disks[1].Size != "10GiB" {
		t.Errorf("expected logs disk preserved, got %+v", conf.Disks[1])
	}
}

func TestMergeConfigs_DisksNameIdentityAttachFalse(t *testing.T) {
	baseDir := t.TempDir()
	baseFile := filepath.Join(baseDir, "_base.yaml")
	baseContent := `schema: lxm/config/v2
base: true
type: vm
user: ubuntu
disks:
  - name: data
    size: 50GiB
    path: /var/lib/data
`
	if err := os.WriteFile(baseFile, []byte(baseContent), 0644); err != nil {
		t.Fatalf("writing base: %v", err)
	}

	leafFile := filepath.Join(baseDir, "leaf.yaml")
	leafContent := `schema: lxm/config/v2
name: db-vm
include:
  - _base.yaml
disks:
  - name: data
    size: 50GiB
    path: /var/lib/data
    attach: false
`
	if err := os.WriteFile(leafFile, []byte(leafContent), 0644); err != nil {
		t.Fatalf("writing leaf: %v", err)
	}

	conf, err := config.LoadConfig(leafFile)
	if err != nil {
		t.Fatalf("LoadConfig error: %v", err)
	}

	if len(conf.Disks) != 1 {
		t.Fatalf("expected 1 disk, got %d", len(conf.Disks))
	}
	if conf.Disks[0].Attach == nil || *conf.Disks[0].Attach {
		t.Errorf("expected attach: false after override, got %v", conf.Disks[0].Attach)
	}
}
