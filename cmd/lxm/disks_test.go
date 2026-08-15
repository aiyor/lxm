package main

import (
	"errors"
	"strings"
	"testing"

	"github.com/aiyor/lxm/internal/config"
	"github.com/aiyor/lxm/internal/lxd"
	"github.com/aiyor/lxm/internal/plan"
)

func TestPlanComputeError_ExitCodeMapping(t *testing.T) {
	// Missing external volume → exit 4.
	mve := &plan.MissingVolumeError{Instance: "db-vm", Disk: "data", Pool: "default", Volume: "missing-vol"}
	if err := planComputeError(mve); err == nil {
		t.Fatal("expected error")
	} else {
		var ee *exitError
		if !errors.As(err, &ee) || ee.code != 4 {
			t.Errorf("expected exit 4 for MissingVolumeError, got %+v", err)
		}
	}

	// Generic config error → exit 3.
	generic := errors.New("some config error")
	if err := planComputeError(generic); err == nil {
		t.Fatal("expected error")
	} else {
		var ee *exitError
		if !errors.As(err, &ee) || ee.code != 3 {
			t.Errorf("expected exit 3 for generic error, got %+v", err)
		}
	}
}

func TestCheckDiskExtensions_BlockModeGate(t *testing.T) {
	fake := lxd.NewFakeInstanceServer()

	blockDisk := &config.Config{
		Name: "db-vm",
		Type: "virtual-machine",
		Disks: []config.DiskConfig{
			{Name: "wal", Path: "", Size: "20GiB"}, // block mode: path unset
		},
	}
	fsDisk := &config.Config{
		Name: "db-vm",
		Type: "virtual-machine",
		Disks: []config.DiskConfig{
			{Name: "data", Path: "/var/lib/data", Size: "20GiB"}, // filesystem mode
		},
	}

	// Extension present → no error.
	fake.Extensions["custom_block_volumes"] = true
	if err := checkDiskExtensions(fake, []*config.Config{blockDisk}); err != nil {
		t.Errorf("expected no error with extension present, got %v", err)
	}

	// Extension absent with block disk → exit 4.
	delete(fake.Extensions, "custom_block_volumes")
	err := checkDiskExtensions(fake, []*config.Config{blockDisk})
	if err == nil {
		t.Fatal("expected error when extension missing for block disk")
	}
	var ee *exitError
	if !errors.As(err, &ee) || ee.code != 4 {
		t.Errorf("expected exit 4, got %+v", err)
	}

	// Extension absent with only filesystem disks → no error.
	if err := checkDiskExtensions(fake, []*config.Config{fsDisk}); err != nil {
		t.Errorf("expected no error for filesystem-only disks, got %v", err)
	}

	// Nil service → no error.
	if err := checkDiskExtensions(nil, []*config.Config{blockDisk}); err != nil {
		t.Errorf("expected no error for nil service, got %v", err)
	}
}

func TestCheckDiskExtensions_IOBusGate(t *testing.T) {
	fake := lxd.NewFakeInstanceServer()
	fake.Extensions["custom_block_volumes"] = true
	delete(fake.Extensions, "disk_io_bus")
	delete(fake.Extensions, "disk_io_bus_virtio_blk")

	nvmeDisk := &config.Config{Name: "db-vm", Type: "virtual-machine", Disks: []config.DiskConfig{{Name: "wal", Path: "", Size: "20GiB", Bus: "nvme"}}}
	virtioBlkDisk := &config.Config{Name: "db-vm", Type: "virtual-machine", Disks: []config.DiskConfig{{Name: "wal", Path: "", Size: "20GiB", Bus: "virtio-blk"}}}
	scsiDisk := &config.Config{Name: "db-vm", Type: "virtual-machine", Disks: []config.DiskConfig{{Name: "wal", Path: "", Size: "20GiB", Bus: "virtio-scsi"}}}

	// Non-default io.bus requires disk_io_bus.
	err := checkDiskExtensions(fake, []*config.Config{nvmeDisk})
	if err == nil {
		t.Fatal("expected disk_io_bus gate failure for nvme")
	}
	var ee *exitError
	if !errors.As(err, &ee) || ee.code != 4 {
		t.Errorf("expected exit 4, got %+v", err)
	}

	// virtio-blk additionally requires disk_io_bus_virtio_blk.
	fake.Extensions["disk_io_bus"] = true
	if err := checkDiskExtensions(fake, []*config.Config{virtioBlkDisk}); err == nil {
		t.Error("expected disk_io_bus_virtio_blk gate failure for virtio-blk")
	}
	fake.Extensions["disk_io_bus_virtio_blk"] = true
	if err := checkDiskExtensions(fake, []*config.Config{virtioBlkDisk}); err != nil {
		t.Errorf("expected no error for virtio-blk with both extensions, got %v", err)
	}

	// Default virtio-scsi needs no extra gate.
	delete(fake.Extensions, "disk_io_bus")
	delete(fake.Extensions, "disk_io_bus_virtio_blk")
	if err := checkDiskExtensions(fake, []*config.Config{scsiDisk}); err != nil {
		t.Errorf("expected no error for default bus, got %v", err)
	}
}

func TestCheckDiskExtensions_Message(t *testing.T) {
	fake := lxd.NewFakeInstanceServer()
	delete(fake.Extensions, "custom_block_volumes")
	err := checkDiskExtensions(fake, []*config.Config{{
		Name: "db-vm",
		Type: "virtual-machine",
		Disks: []config.DiskConfig{
			{Name: "wal", Size: "20GiB"},
		},
	}})
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "custom_block_volumes") {
		t.Errorf("unexpected message: %v", err)
	}
}
