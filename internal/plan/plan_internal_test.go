package plan

import (
	"testing"

	"github.com/aiyor/lxm/internal/config"
)

func TestGetLiveMounts_SkipsDataDisks(t *testing.T) {
	snap := &InstanceSnapshot{
		Devices: map[string]map[string]string{
			"root":       {"type": "disk", "path": "/", "pool": "default", "size": "30GiB"},
			"mount0":     {"type": "disk", "source": "/tmp/host", "path": "/var/a"},
			"mount-/opt": {"type": "disk", "source": "/tmp/legacy", "path": "/opt"},
			"disk-data":  {"type": "disk", "pool": "default", "source": "db-vm-data", "path": "/var/lib/postgresql"},
			"foreign":    {"type": "disk", "source": "/dev/sdb", "path": "/mnt/foreign"},
		},
	}
	mounts := getLiveMounts(snap)
	if len(mounts) != 2 {
		t.Fatalf("expected 2 mounts (mount0 + legacy mount-/opt), got %d: %+v", len(mounts), mounts)
	}
	for _, m := range mounts {
		if m.Path == "/var/lib/postgresql" || m.Path == "/mnt/foreign" || m.Source == "/dev/sdb" {
			t.Errorf("data/foreign disk leaked into mounts: %+v", m)
		}
	}
}

func TestBuildInstancePut_PreservesForeignDiskDevice(t *testing.T) {
	conf := &config.Config{
		Name:  "db-vm",
		Type:  "virtual-machine",
		Image: "ubuntu:24.04",
		User:  "ubuntu",
		Disks: []config.DiskConfig{
			{Name: "data", Size: "100GiB", Pool: "default", Path: "/var/lib/postgresql", Source: "db-vm-data"},
		},
	}
	live := &InstanceSnapshot{
		Architecture: "x86_64",
		Profiles:     []string{"default"},
		Config:       map[string]string{"user.lxm.managed": "true"},
		Devices: map[string]map[string]string{
			"root":      {"type": "disk", "path": "/", "pool": "default", "size": "30GiB"},
			"mount0":    {"type": "disk", "source": "/tmp/host", "path": "/var/a"},
			"disk-data": {"type": "disk", "pool": "default", "source": "db-vm-data", "path": "/var/lib/postgresql"},
			"foreign":   {"type": "disk", "source": "/dev/sdb", "path": "/opt"},
			"gpu":       {"type": "gpu"},
		},
	}
	put, err := buildInstancePut(conf, live)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// Foreign disk device and non-disk device preserved.
	if put.Devices["foreign"] == nil {
		t.Errorf("foreign disk device must be preserved, got %v", put.Devices)
	}
	if put.Devices["gpu"] == nil {
		t.Errorf("non-disk device must be preserved")
	}
	// Managed disk device rebuilt with source and no size.
	if d := put.Devices["disk-data"]; d == nil || d["source"] != "db-vm-data" || d["path"] != "/var/lib/postgresql" || d["size"] != "" {
		t.Errorf("unexpected disk-data device: %v", d)
	}
	// mount0 is rebuilt from the manifest (no mounts declared) and dropped,
	// matching mount rebuild semantics.
	if put.Devices["mount0"] != nil {
		t.Errorf("mount0 should be rebuilt from manifest (dropped here), got %v", put.Devices["mount0"])
	}
}
