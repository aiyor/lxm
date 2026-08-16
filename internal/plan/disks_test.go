package plan_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/aiyor/lxm/internal/config"
	"github.com/aiyor/lxm/internal/plan"
	"github.com/canonical/lxd/shared/api"
)

// normalizedDisksVM returns a VM manifest with normalized DiskConfig values
// (as produced by config.LoadConfig) exercising all four modes.
func normalizedDisksVM() *config.Config {
	return &config.Config{
		Name:  "db-vm",
		Type:  "virtual-machine",
		Image: "ubuntu:24.04",
		User:  "ubuntu",
		Disks: []config.DiskConfig{
			{Name: "data", Size: "100GiB", Pool: "default", Path: "/var/lib/postgresql", Source: "db-vm-data"},
			{Name: "wal", Size: "20GiB", Pool: "default", Bus: "virtio-scsi", Source: "db-vm-wal"},
			{Name: "shared-fs", Pool: "fast-pool", Path: "/srv/www", Source: "web-root-vol", Readonly: true},
		},
	}
}

// TestReconciler_Create_DeviceShape verifies the four-mode device shape and
// managed-disk create ops. The volume map is passed to Reconciler.Compute as a
// dedicated parameter (it is not carried on instance snapshots, so it survives
// an empty instance list).
func TestReconciler_Create_DeviceShape(t *testing.T) {
	rec := plan.NewReconciler()
	conf := normalizedDisksVM()
	volumes := map[string]map[string]*api.StorageVolume{
		"fast-pool": {"web-root-vol": {Name: "web-root-vol", Type: "custom", ContentType: "filesystem"}},
	}

	p, err := rec.Compute(conf, nil, volumes, nil, config.BuiltinImageRemotes(), false)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	step := p.Steps[0]
	if step.Action != "create" {
		t.Fatalf("expected create, got %q", step.Action)
	}
	post := step.InstancesPost
	if post == nil {
		t.Fatal("expected InstancesPost")
	}

	// filesystem managed: pool+source+path, no size, no bus
	d := post.Devices["disk-data"]
	if d == nil || d["type"] != "disk" || d["pool"] != "default" || d["source"] != "db-vm-data" || d["path"] != "/var/lib/postgresql" {
		t.Fatalf("unexpected disk-data device: %v", d)
	}
	if d["size"] != "" {
		t.Errorf("device map must not carry size, got %q", d["size"])
	}
	if d["io.bus"] != "" {
		t.Errorf("filesystem device must not carry io.bus, got %q", d["io.bus"])
	}

	// block managed: pool+source, no path, no size, no io.bus (the default
	// virtio-scsi bus is LXD's own default and is omitted from the device map).
	d = post.Devices["disk-wal"]
	if d == nil || d["type"] != "disk" || d["pool"] != "default" || d["source"] != "db-vm-wal" {
		t.Fatalf("unexpected disk-wal device: %v", d)
	}
	if d["path"] != "" || d["size"] != "" {
		t.Errorf("block device must not carry path/size, got path=%q size=%q", d["path"], d["size"])
	}
	if d["io.bus"] != "" {
		t.Errorf("default-bus block device must not carry io.bus, got %q", d["io.bus"])
	}

	// filesystem external: source+path+readonly, no size
	d = post.Devices["disk-shared-fs"]
	if d == nil || d["pool"] != "fast-pool" || d["source"] != "web-root-vol" || d["path"] != "/srv/www" || d["readonly"] != "true" {
		t.Fatalf("unexpected disk-shared-fs device: %v", d)
	}

	// VolumeOps: managed disks only (create), external disks none
	ops := step.VolumeOps
	if len(ops) != 2 {
		t.Fatalf("expected 2 create ops (managed disks), got %d: %+v", len(ops), ops)
	}
	found := map[string]plan.VolumeOp{}
	for _, op := range ops {
		found[op.Name] = op
	}
	if op := found["db-vm-data"]; op.Op != "create" || op.ContentType != "filesystem" || op.Size != "100GiB" {
		t.Errorf("unexpected db-vm-data op: %+v", op)
	}
	if op := found["db-vm-wal"]; op.Op != "create" || op.ContentType != "block" || op.Size != "20GiB" {
		t.Errorf("unexpected db-vm-wal op: %+v", op)
	}
}

func TestReconciler_Create_ExplicitBus_EmitsIOBus(t *testing.T) {
	rec := plan.NewReconciler()
	conf := &config.Config{
		Name:  "db-vm",
		Type:  "virtual-machine",
		Image: "ubuntu:24.04",
		User:  "ubuntu",
		Disks: []config.DiskConfig{
			{Name: "wal", Size: "20GiB", Pool: "default", Bus: "nvme", Source: "db-vm-wal"},
			{Name: "scsi", Size: "20GiB", Pool: "default", Bus: "virtio-scsi", Source: "db-vm-scsi"},
		},
	}
	p, err := rec.Compute(conf, nil, nil, nil, config.BuiltinImageRemotes(), false)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	devs := p.Steps[0].InstancesPost.Devices
	// Explicit non-default bus is emitted; explicit default bus is omitted.
	if devs["disk-wal"]["io.bus"] != "nvme" {
		t.Errorf("expected io.bus nvme, got %q", devs["disk-wal"]["io.bus"])
	}
	if devs["disk-scsi"]["io.bus"] != "" {
		t.Errorf("explicit virtio-scsi is LXD's default and must be omitted, got %q", devs["disk-scsi"]["io.bus"])
	}
}

func TestReconciler_Update_DefaultBusReconstructed(t *testing.T) {
	// A live block device without io.bus (LXD default) must reconstruct as
	// virtio-scsi so it converges with the manifest's materialized default.
	rec := plan.NewReconciler()
	conf := normalizedDisksVM()
	live, volumes := liveVMWithDisk(map[string]map[string]string{
		"disk-data":      diskDev("", "default", "db-vm-data", "/var/lib/postgresql", "", ""),
		"disk-wal":       diskDev("", "default", "db-vm-wal", "", "", ""), // no io.bus
		"disk-shared-fs": diskDev("", "fast-pool", "web-root-vol", "/srv/www", "", "true"),
	}, map[string]map[string]*api.StorageVolume{
		"default": {
			"db-vm-data": {Name: "db-vm-data", Config: map[string]string{"size": "100GiB"}},
			"db-vm-wal":  {Name: "db-vm-wal", Config: map[string]string{"size": "20GiB"}},
		},
		"fast-pool": {"web-root-vol": {Name: "web-root-vol", Config: map[string]string{"size": "10GiB"}}},
	})

	p, err := rec.Compute(conf, live, volumes, nil, config.BuiltinImageRemotes(), false)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if p.Steps[0].Action != "noop" {
		t.Errorf("expected noop when default bus is absent on the live device, got %q (diff: %+v)", p.Steps[0].Action, p.Steps[0].Diff)
	}
}

func TestReconciler_Create_ExternalVolumeMissing(t *testing.T) {
	rec := plan.NewReconciler()
	conf := normalizedDisksVM()
	// No volumes at all: external web-root-vol is missing.
	_, err := rec.Compute(conf, map[string]*plan.InstanceSnapshot{}, nil, nil, config.BuiltinImageRemotes(), false)
	if err == nil {
		t.Fatal("expected MissingVolumeError for absent external volume")
	}
	var mve *plan.MissingVolumeError
	if !errors.As(err, &mve) {
		t.Fatalf("expected *plan.MissingVolumeError, got %T: %v", err, err)
	}
	if mve.Volume != "web-root-vol" || mve.Disk != "shared-fs" || mve.Instance != "db-vm" {
		t.Errorf("unexpected MissingVolumeError: %+v", mve)
	}
}

func TestReconciler_Create_ExternalVolume_PresentOnEmptyLive(t *testing.T) {
	// Zero live instances (fresh LXD) attaching an existing external volume:
	// the volume map is a dedicated Compute parameter, so it survives an empty
	// instance list (regression: previously dropped when no snapshot carried it).
	rec := plan.NewReconciler()
	conf := normalizedDisksVM()
	volumes := map[string]map[string]*api.StorageVolume{
		"fast-pool": {"web-root-vol": {Name: "web-root-vol", Type: "custom", ContentType: "filesystem"}},
	}
	p, err := rec.Compute(conf, map[string]*plan.InstanceSnapshot{}, volumes, nil, config.BuiltinImageRemotes(), false)
	if err != nil {
		t.Fatalf("expected create to succeed with the external volume present, got: %v", err)
	}
	if p.Steps[0].Action != "create" {
		t.Fatalf("expected create, got %q", p.Steps[0].Action)
	}
}

func TestReconciler_Update_RewordedEqualSize_NoDiff(t *testing.T) {
	// A semantically-equal size reworded differently (10GiB vs 10737418240)
	// must not produce a perpetual size diff (M3).
	rec := plan.NewReconciler()
	conf := normalizedDisksVM()
	conf.Disks[0].Size = "10737418240" // == 10GiB
	live, volumes := liveVMWithDisk(map[string]map[string]string{
		"disk-data":      diskDev("", "default", "db-vm-data", "/var/lib/postgresql", "", ""),
		"disk-wal":       diskDev("", "default", "db-vm-wal", "", "virtio-scsi", ""),
		"disk-shared-fs": diskDev("", "fast-pool", "web-root-vol", "/srv/www", "", "true"),
	}, map[string]map[string]*api.StorageVolume{
		"default": {
			"db-vm-data": {Name: "db-vm-data", Config: map[string]string{"size": "10GiB"}},
			"db-vm-wal":  {Name: "db-vm-wal", Config: map[string]string{"size": "20GiB"}},
		},
		"fast-pool": {"web-root-vol": {Name: "web-root-vol", Config: map[string]string{"size": "10GiB"}}},
	})

	p, err := rec.Compute(conf, live, volumes, nil, config.BuiltinImageRemotes(), false)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if p.Steps[0].Action != "noop" {
		t.Errorf("expected noop for reworded-equal size, got %q (diff: %+v)", p.Steps[0].Action, p.Steps[0].Diff)
	}
}

func TestReconciler_Update_ManagedPoolChange_Restart(t *testing.T) {
	// A managed pool change re-points the device to a fresh volume in another
	// pool; on a running VM this detaches/attaches a device, so a restart is
	// required (M4).
	rec := plan.NewReconciler()
	conf := normalizedDisksVM()
	conf.Disks[0].Pool = "fast-pool"
	live, volumes := liveVMWithDisk(map[string]map[string]string{
		"disk-data":      diskDev("", "default", "db-vm-data", "/var/lib/postgresql", "", ""),
		"disk-wal":       diskDev("", "default", "db-vm-wal", "", "virtio-scsi", ""),
		"disk-shared-fs": diskDev("", "fast-pool", "web-root-vol", "/srv/www", "", "true"),
	}, map[string]map[string]*api.StorageVolume{
		"default": {
			"db-vm-data": {Name: "db-vm-data", Config: map[string]string{"size": "100GiB"}},
			"db-vm-wal":  {Name: "db-vm-wal", Config: map[string]string{"size": "20GiB"}},
		},
		"fast-pool": {"web-root-vol": {Name: "web-root-vol", Config: map[string]string{"size": "10GiB"}}},
	})

	p, err := rec.Compute(conf, live, volumes, nil, config.BuiltinImageRemotes(), false)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	step := p.Steps[0]
	if step.Action != "update" {
		t.Fatalf("expected update, got %q", step.Action)
	}
	if step.PowerTransition != "restart" {
		t.Errorf("managed pool change on a running VM must restart, got %q", step.PowerTransition)
	}
}

func TestReconciler_Create_NoExternalVolumesProbed(t *testing.T) {
	rec := plan.NewReconciler()
	conf := &config.Config{
		Name:  "db-vm",
		Type:  "virtual-machine",
		Image: "ubuntu:24.04",
		User:  "ubuntu",
		Disks: []config.DiskConfig{
			{Name: "data", Size: "100GiB", Pool: "default", Path: "/var/lib/postgresql", Source: "db-vm-data"},
			{Name: "wal", Size: "20GiB", Pool: "default", Bus: "virtio-scsi", Source: "db-vm-wal"},
		},
	}
	// Only managed disks: no volumes needed, nil live map is fine.
	p, err := rec.Compute(conf, nil, nil, nil, config.BuiltinImageRemotes(), false)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(p.Steps[0].VolumeOps) != 2 {
		t.Errorf("expected 2 create ops, got %+v", p.Steps[0].VolumeOps)
	}
}

func liveVMWithDisk(devices map[string]map[string]string, volumes map[string]map[string]*api.StorageVolume) (map[string]*plan.InstanceSnapshot, map[string]map[string]*api.StorageVolume) {
	return map[string]*plan.InstanceSnapshot{
		"db-vm": {
			Name:            "db-vm",
			Type:            "virtual-machine",
			Status:          "Running",
			StatusCode:      103,
			Config:          map[string]string{"user.lxm.managed": "true", "user.lxm.user": "ubuntu"},
			Devices:         devices,
			ExpandedDevices: devices,
			ETag:            "etag-1",
		},
	}, volumes
}

func diskDev(name, pool, source, path, bus, readonly string) map[string]string {
	d := map[string]string{"type": "disk", "pool": pool, "source": source}
	if path != "" {
		d["path"] = path
	}
	if bus != "" {
		d["io.bus"] = bus
	}
	if readonly == "true" {
		d["readonly"] = "true"
	}
	return d
}

func TestReconciler_Update_NoDiskDiff(t *testing.T) {
	rec := plan.NewReconciler()
	conf := normalizedDisksVM()
	live, volumes := liveVMWithDisk(map[string]map[string]string{
		"disk-data":      diskDev("", "default", "db-vm-data", "/var/lib/postgresql", "", ""),
		"disk-wal":       diskDev("", "default", "db-vm-wal", "", "virtio-scsi", ""),
		"disk-shared-fs": diskDev("", "fast-pool", "web-root-vol", "/srv/www", "", "true"),
	}, map[string]map[string]*api.StorageVolume{
		"default": {
			"db-vm-data": {Name: "db-vm-data", Config: map[string]string{"size": "100GiB"}},
			"db-vm-wal":  {Name: "db-vm-wal", Config: map[string]string{"size": "20GiB"}},
		},
		"fast-pool": {"web-root-vol": {Name: "web-root-vol", Config: map[string]string{"size": "10GiB"}}},
	})

	p, err := rec.Compute(conf, live, volumes, nil, config.BuiltinImageRemotes(), false)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	step := p.Steps[0]
	if step.Action != "noop" {
		t.Errorf("expected noop when disks match, got %q (diff: %+v)", step.Action, step.Diff)
	}
	if len(step.VolumeOps) != 0 {
		t.Errorf("expected no volume ops, got %+v", step.VolumeOps)
	}
}

func TestReconciler_Update_DiskAdded(t *testing.T) {
	rec := plan.NewReconciler()
	conf := normalizedDisksVM()
	// live has only the wal disk; data (managed) and shared-fs (external) added.
	live, volumes := liveVMWithDisk(map[string]map[string]string{
		"disk-wal": diskDev("", "default", "db-vm-wal", "", "virtio-scsi", ""),
	}, map[string]map[string]*api.StorageVolume{
		"default":   {"db-vm-wal": {Name: "db-vm-wal", Config: map[string]string{"size": "20GiB"}}},
		"fast-pool": {"web-root-vol": {Name: "web-root-vol", Config: map[string]string{"size": "10GiB"}}},
	})

	p, err := rec.Compute(conf, live, volumes, nil, config.BuiltinImageRemotes(), false)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	step := p.Steps[0]
	if step.Action != "update" {
		t.Fatalf("expected update, got %q", step.Action)
	}
	if step.PowerTransition != "" {
		t.Errorf("adding disks must not restart a running VM, got %q", step.PowerTransition)
	}
	// Managed data disk needs a create op.
	foundCreate := false
	for _, op := range step.VolumeOps {
		if op.Name == "db-vm-data" && op.Op == "create" {
			foundCreate = true
		}
	}
	if !foundCreate {
		t.Errorf("expected VolumeOps{create} for added managed disk, got %+v", step.VolumeOps)
	}
}

func TestReconciler_Update_DiskRemoved(t *testing.T) {
	rec := plan.NewReconciler()
	conf := normalizedDisksVM()
	// live has an extra foreign-to-manifest disk disk-gone.
	live, volumes := liveVMWithDisk(map[string]map[string]string{
		"disk-data":      diskDev("", "default", "db-vm-data", "/var/lib/postgresql", "", ""),
		"disk-wal":       diskDev("", "default", "db-vm-wal", "", "virtio-scsi", ""),
		"disk-shared-fs": diskDev("", "fast-pool", "web-root-vol", "/srv/www", "", "true"),
		"disk-gone":      diskDev("", "default", "db-vm-gone", "/var/lib/gone", "", ""),
	}, map[string]map[string]*api.StorageVolume{
		"default": {
			"db-vm-data": {Name: "db-vm-data", Config: map[string]string{"size": "100GiB"}},
			"db-vm-wal":  {Name: "db-vm-wal", Config: map[string]string{"size": "20GiB"}},
		},
		"fast-pool": {"web-root-vol": {Name: "web-root-vol", Config: map[string]string{"size": "10GiB"}}},
	})

	p, err := rec.Compute(conf, live, volumes, nil, config.BuiltinImageRemotes(), false)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	step := p.Steps[0]
	if step.Action != "update" {
		t.Fatalf("expected update, got %q", step.Action)
	}
	removed := false
	for _, d := range step.Diff {
		if d.Field == "disks[gone]" {
			removed = true
		}
	}
	if !removed {
		t.Errorf("expected disks[gone] removal diff, got %+v", step.Diff)
	}
	// Detach only: no delete ops.
	for _, op := range step.VolumeOps {
		if op.Name == "db-vm-gone" {
			t.Errorf("removed disk must not generate a volume op (never delete), got %+v", step.VolumeOps)
		}
	}
}

func TestReconciler_Update_Grow(t *testing.T) {
	rec := plan.NewReconciler()
	conf := normalizedDisksVM()
	conf.Disks[0].Size = "150GiB" // grow data 100GiB → 150GiB
	live, volumes := liveVMWithDisk(map[string]map[string]string{
		"disk-data":      diskDev("", "default", "db-vm-data", "/var/lib/postgresql", "", ""),
		"disk-wal":       diskDev("", "default", "db-vm-wal", "", "virtio-scsi", ""),
		"disk-shared-fs": diskDev("", "fast-pool", "web-root-vol", "/srv/www", "", "true"),
	}, map[string]map[string]*api.StorageVolume{
		"default": {
			"db-vm-data": {Name: "db-vm-data", Config: map[string]string{"size": "100GiB"}},
			"db-vm-wal":  {Name: "db-vm-wal", Config: map[string]string{"size": "20GiB"}},
		},
		"fast-pool": {"web-root-vol": {Name: "web-root-vol", Config: map[string]string{"size": "10GiB"}}},
	})

	p, err := rec.Compute(conf, live, volumes, nil, config.BuiltinImageRemotes(), false)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	step := p.Steps[0]
	if step.Action != "update" {
		t.Fatalf("expected update, got %q", step.Action)
	}
	if step.PowerTransition != "" {
		t.Errorf("grow must not restart, got %q", step.PowerTransition)
	}
	foundGrow := false
	for _, op := range step.VolumeOps {
		if op.Name == "db-vm-data" && op.Op == "grow" && op.Size == "150GiB" {
			foundGrow = true
		}
	}
	if !foundGrow {
		t.Errorf("expected VolumeOps{grow} for data, got %+v", step.VolumeOps)
	}
}

func TestReconciler_Update_Shrink_Error(t *testing.T) {
	rec := plan.NewReconciler()
	conf := normalizedDisksVM()
	conf.Disks[0].Size = "50GiB" // shrink data 100GiB → 50GiB
	live, volumes := liveVMWithDisk(map[string]map[string]string{
		"disk-data":      diskDev("", "default", "db-vm-data", "/var/lib/postgresql", "", ""),
		"disk-wal":       diskDev("", "default", "db-vm-wal", "", "virtio-scsi", ""),
		"disk-shared-fs": diskDev("", "fast-pool", "web-root-vol", "/srv/www", "", "true"),
	}, map[string]map[string]*api.StorageVolume{
		"default": {
			"db-vm-data": {Name: "db-vm-data", Config: map[string]string{"size": "100GiB"}},
			"db-vm-wal":  {Name: "db-vm-wal", Config: map[string]string{"size": "20GiB"}},
		},
		"fast-pool": {"web-root-vol": {Name: "web-root-vol", Config: map[string]string{"size": "10GiB"}}},
	})

	_, err := rec.Compute(conf, live, volumes, nil, config.BuiltinImageRemotes(), false)
	if err == nil {
		t.Fatal("expected shrink to be rejected")
	}
	if !strings.Contains(err.Error(), "cannot be shrunk") {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestReconciler_Update_ModeSwitch_Error(t *testing.T) {
	rec := plan.NewReconciler()
	conf := normalizedDisksVM()
	// Switch wal from block to filesystem (add path).
	conf.Disks[1].Path = "/var/lib/wal"
	conf.Disks[1].Bus = ""
	live, volumes := liveVMWithDisk(map[string]map[string]string{
		"disk-data":      diskDev("", "default", "db-vm-data", "/var/lib/postgresql", "", ""),
		"disk-wal":       diskDev("", "default", "db-vm-wal", "", "virtio-scsi", ""),
		"disk-shared-fs": diskDev("", "fast-pool", "web-root-vol", "/srv/www", "", "true"),
	}, map[string]map[string]*api.StorageVolume{
		"default": {
			"db-vm-data": {Name: "db-vm-data", Config: map[string]string{"size": "100GiB"}},
			"db-vm-wal":  {Name: "db-vm-wal", Config: map[string]string{"size": "20GiB"}},
		},
		"fast-pool": {"web-root-vol": {Name: "web-root-vol", Config: map[string]string{"size": "10GiB"}}},
	})

	_, err := rec.Compute(conf, live, volumes, nil, config.BuiltinImageRemotes(), false)
	if err == nil {
		t.Fatal("expected mode switch to be rejected")
	}
	if !strings.Contains(err.Error(), "mode switch") {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestReconciler_Update_PathChange_Restart(t *testing.T) {
	rec := plan.NewReconciler()
	conf := normalizedDisksVM()
	conf.Disks[0].Path = "/var/lib/new-path"
	live, volumes := liveVMWithDisk(map[string]map[string]string{
		"disk-data":      diskDev("", "default", "db-vm-data", "/var/lib/postgresql", "", ""),
		"disk-wal":       diskDev("", "default", "db-vm-wal", "", "virtio-scsi", ""),
		"disk-shared-fs": diskDev("", "fast-pool", "web-root-vol", "/srv/www", "", "true"),
	}, map[string]map[string]*api.StorageVolume{
		"default": {
			"db-vm-data": {Name: "db-vm-data", Config: map[string]string{"size": "100GiB"}},
			"db-vm-wal":  {Name: "db-vm-wal", Config: map[string]string{"size": "20GiB"}},
		},
		"fast-pool": {"web-root-vol": {Name: "web-root-vol", Config: map[string]string{"size": "10GiB"}}},
	})

	p, err := rec.Compute(conf, live, volumes, nil, config.BuiltinImageRemotes(), false)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	step := p.Steps[0]
	if step.Action != "update" {
		t.Fatalf("expected update, got %q", step.Action)
	}
	if step.PowerTransition != "restart" {
		t.Errorf("path change on running VM must restart, got %q", step.PowerTransition)
	}
}

func TestReconciler_Update_ManagedPoolChange_CreateOp(t *testing.T) {
	rec := plan.NewReconciler()
	conf := normalizedDisksVM()
	conf.Disks[0].Pool = "fast-pool" // move managed data disk to fast-pool
	conf.Disks[0].Source = "db-vm-data"
	live, volumes := liveVMWithDisk(map[string]map[string]string{
		"disk-data":      diskDev("", "default", "db-vm-data", "/var/lib/postgresql", "", ""),
		"disk-wal":       diskDev("", "default", "db-vm-wal", "", "virtio-scsi", ""),
		"disk-shared-fs": diskDev("", "fast-pool", "web-root-vol", "/srv/www", "", "true"),
	}, map[string]map[string]*api.StorageVolume{
		"default": {
			"db-vm-data": {Name: "db-vm-data", Config: map[string]string{"size": "100GiB"}},
			"db-vm-wal":  {Name: "db-vm-wal", Config: map[string]string{"size": "20GiB"}},
		},
		"fast-pool": {"web-root-vol": {Name: "web-root-vol", Config: map[string]string{"size": "10GiB"}}},
	})

	p, err := rec.Compute(conf, live, volumes, nil, config.BuiltinImageRemotes(), false)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	step := p.Steps[0]
	if step.Action != "update" {
		t.Fatalf("expected update, got %q", step.Action)
	}
	foundCreate := false
	for _, op := range step.VolumeOps {
		if op.Name == "db-vm-data" && op.Op == "create" && op.Pool == "fast-pool" {
			foundCreate = true
		}
	}
	if !foundCreate {
		t.Errorf("expected VolumeOps{create} in new pool, got %+v", step.VolumeOps)
	}
}

func TestReconciler_Update_ExternalMissing_Error(t *testing.T) {
	rec := plan.NewReconciler()
	conf := normalizedDisksVM()
	// shared-fs external volume disappears from the pool.
	live, volumes := liveVMWithDisk(map[string]map[string]string{
		"disk-data": diskDev("", "default", "db-vm-data", "/var/lib/postgresql", "", ""),
		"disk-wal":  diskDev("", "default", "db-vm-wal", "", "virtio-scsi", ""),
	}, map[string]map[string]*api.StorageVolume{
		"default": {
			"db-vm-data": {Name: "db-vm-data", Config: map[string]string{"size": "100GiB"}},
			"db-vm-wal":  {Name: "db-vm-wal", Config: map[string]string{"size": "20GiB"}},
		},
	})

	_, err := rec.Compute(conf, live, volumes, nil, config.BuiltinImageRemotes(), false)
	if err == nil {
		t.Fatal("expected error for missing external volume")
	}
	var mve *plan.MissingVolumeError
	if !errors.As(err, &mve) {
		t.Fatalf("expected *plan.MissingVolumeError, got %T: %v", err, err)
	}
}
