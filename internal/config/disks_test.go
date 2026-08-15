package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeDiskManifest(t *testing.T, body string) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "disk.yaml")
	if err := os.WriteFile(path, []byte(body), 0644); err != nil {
		t.Fatalf("writing manifest: %v", err)
	}
	return path
}

func TestLoadConfig_Disks_Normalization(t *testing.T) {
	path := writeDiskManifest(t, `
schema: lxm/config/v2
name: db-vm
type: vm
image: ubuntu:24.04
disks:
  - name: data
    size: 100GiB
    path: /var/lib/postgresql
  - name: wal
    size: 20GiB
    bus: nvme
  - name: shared-fs
    source: web-root-vol
    pool: fast-pool
    path: /srv/www
    readonly: true
`)
	conf, err := LoadConfig(path)
	if err != nil {
		t.Fatalf("loading manifest: %v", err)
	}
	if len(conf.Disks) != 3 {
		t.Fatalf("expected 3 disks, got %d", len(conf.Disks))
	}

	// filesystem managed: derived source, default pool, no bus
	d := conf.Disks[0]
	if d.Name != "data" || d.Size != "100GiB" || d.Pool != "default" || d.Path != "/var/lib/postgresql" {
		t.Errorf("unexpected data disk: %+v", d)
	}
	if d.Source != "db-vm-data" {
		t.Errorf("expected derived source %q, got %q", "db-vm-data", d.Source)
	}
	if d.Bus != "" {
		t.Errorf("filesystem disk must not carry a bus, got %q", d.Bus)
	}

	// block managed: derived source, default bus
	d = conf.Disks[1]
	if d.Name != "wal" || d.Bus != "nvme" || d.Path != "" || d.Source != "db-vm-wal" {
		t.Errorf("unexpected wal disk: %+v", d)
	}

	// filesystem external: explicit source, no size, default bus cleared
	d = conf.Disks[2]
	if d.Source != "web-root-vol" || d.Size != "" || d.Pool != "fast-pool" || d.Path != "/srv/www" || !d.Readonly || d.Bus != "" {
		t.Errorf("unexpected shared-fs disk: %+v", d)
	}
}

func TestLoadConfig_Disks_ManagedWithoutSize_Rejected(t *testing.T) {
	path := writeDiskManifest(t, `
schema: lxm/config/v2
name: db-vm
type: vm
image: ubuntu:24.04
disks:
  - name: data
    path: /var/lib/postgresql
`)
	_, err := LoadConfig(path)
	if err == nil {
		t.Fatal("expected error for managed disk without size")
	}
	if !strings.Contains(err.Error(), "size is required") {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestLoadConfig_Disks_SizeWithSource_Rejected(t *testing.T) {
	path := writeDiskManifest(t, `
schema: lxm/config/v2
name: db-vm
type: vm
image: ubuntu:24.04
disks:
  - name: shared
    source: web-root-vol
    size: 10GiB
`)
	_, err := LoadConfig(path)
	if err == nil {
		t.Fatal("expected error for external disk with size")
	}
	if !strings.Contains(err.Error(), "schema validation") {
		t.Errorf("expected CUE schema rejection, got: %v", err)
	}
}

func TestLoadConfig_Disks_BusWithPath_Rejected(t *testing.T) {
	path := writeDiskManifest(t, `
schema: lxm/config/v2
name: db-vm
type: vm
image: ubuntu:24.04
disks:
  - name: data
    size: 100GiB
    path: /var/lib/postgresql
    bus: nvme
`)
	_, err := LoadConfig(path)
	if err == nil {
		t.Fatal("expected error for bus with path")
	}
	if !strings.Contains(err.Error(), "schema validation") {
		t.Errorf("expected CUE schema rejection, got: %v", err)
	}
}

func TestLoadConfig_Disks_ContainerGuard(t *testing.T) {
	path := writeDiskManifest(t, `
schema: lxm/config/v2
name: web
type: container
image: ubuntu:24.04
disks:
  - name: data
    size: 100GiB
    path: /var/lib/data
`)
	_, err := LoadConfig(path)
	if err == nil {
		t.Fatal("expected error for disks on a container")
	}
	if !strings.Contains(err.Error(), `field "disks" is only supported for type: virtual-machine`) {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestLoadConfig_Disks_DuplicateName_Rejected(t *testing.T) {
	path := writeDiskManifest(t, `
schema: lxm/config/v2
name: db-vm
type: vm
image: ubuntu:24.04
disks:
  - name: data
    size: 100GiB
    path: /var/lib/a
  - name: data
    size: 50GiB
    path: /var/lib/b
`)
	_, err := LoadConfig(path)
	if err == nil {
		t.Fatal("expected error for duplicate disk name")
	}
	if !strings.Contains(err.Error(), "duplicate disk name") {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestLoadConfig_Disks_ReservedRootName_Rejected(t *testing.T) {
	path := writeDiskManifest(t, `
schema: lxm/config/v2
name: db-vm
type: vm
image: ubuntu:24.04
disks:
  - name: root
    size: 100GiB
    path: /var/lib/a
`)
	_, err := LoadConfig(path)
	if err == nil {
		t.Fatal("expected error for reserved root disk name")
	}
	// Enforced at CUE authoring time ("out of bound !=root"); the Go
	// ValidatePostMerge "reserved" check is defense-in-depth for direct callers.
	if !strings.Contains(err.Error(), "reserved") && !strings.Contains(err.Error(), "schema validation") {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestLoadConfig_Disks_MountPathCollision_Rejected(t *testing.T) {
	path := writeDiskManifest(t, `
schema: lxm/config/v2
name: db-vm
type: vm
image: ubuntu:24.04
mounts:
  - source: /tmp/host
    path: /var/lib/data
disks:
  - name: data
    size: 100GiB
    path: /var/lib/data
`)
	_, err := LoadConfig(path)
	if err == nil {
		t.Fatal("expected error for mount path collision")
	}
	if !strings.Contains(err.Error(), "duplicate mount path") {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestLoadConfig_Disks_RemoveReplaceDirectives(t *testing.T) {
	dir := t.TempDir()
	base := filepath.Join(dir, "_base.yaml")
	if err := os.WriteFile(base, []byte(`schema: lxm/config/v2
base: true
disks:
  - name: data
    size: 100GiB
    path: /var/lib/data
  - name: wal
    size: 20GiB
`), 0644); err != nil {
		t.Fatalf("writing base: %v", err)
	}
	leaf := filepath.Join(dir, "leaf.yaml")
	if err := os.WriteFile(leaf, []byte(`schema: lxm/config/v2
name: db-vm
type: vm
image: ubuntu:24.04
include: ["_base.yaml"]
replace:
  disks:
    - name: fresh
      size: 5GiB
`), 0644); err != nil {
		t.Fatalf("writing leaf: %v", err)
	}
	conf, err := LoadConfig(leaf)
	if err != nil {
		t.Fatalf("loading manifest: %v", err)
	}
	if len(conf.Disks) != 1 {
		t.Fatalf("expected 1 disk after replace, got %d: %+v", len(conf.Disks), conf.Disks)
	}
	if conf.Disks[0].Name != "fresh" {
		t.Errorf("expected replace to apply, got %+v", conf.Disks[0])
	}

	// remove.disks alone (no replace) drops the named inherited disk.
	leafRemove := filepath.Join(dir, "leaf2.yaml")
	if err := os.WriteFile(leafRemove, []byte(`schema: lxm/config/v2
name: db-vm
type: vm
image: ubuntu:24.04
include: ["_base.yaml"]
remove:
  disks: ["wal"]
`), 0644); err != nil {
		t.Fatalf("writing leaf2: %v", err)
	}
	conf, err = LoadConfig(leafRemove)
	if err != nil {
		t.Fatalf("loading manifest: %v", err)
	}
	if len(conf.Disks) != 1 || conf.Disks[0].Name != "data" {
		t.Fatalf("expected remove.disks to drop 'wal', got %+v", conf.Disks)
	}
}

func TestLoadConfig_Disks_RemoveMissing_Rejected(t *testing.T) {
	path := writeDiskManifest(t, `
schema: lxm/config/v2
name: db-vm
type: vm
image: ubuntu:24.04
remove:
  disks: ["nope"]
`)
	_, err := LoadConfig(path)
	if err == nil {
		t.Fatal("expected error for remove.disks matching nothing")
	}
	if !strings.Contains(err.Error(), "remove.disks") {
		t.Errorf("unexpected error: %v", err)
	}
}
