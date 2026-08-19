package apply_test

import (
	"context"
	"strings"
	"testing"

	"github.com/aiyor/lxm/internal/apply"
	"github.com/aiyor/lxm/internal/plan"
	"github.com/aiyor/lxm/internal/provider"
	"github.com/aiyor/lxm/internal/provider/fake"
)

func volumeOpPlan(ops []plan.VolumeOp) *plan.Plan {
	return &plan.Plan{
		Schema: "lxm/plan/v1",
		Steps: []plan.Step{
			{
				Container: "db-vm",
				Action:    "create",
				Changed:   true,
				VolumeOps: ops,
				InstancesPost: &provider.InstanceCreateRequest{
					Name:    "db-vm",
					Type:    "virtual-machine",
					Config:  map[string]string{"user.lxm.managed": "true"},
					Devices: map[string]map[string]string{},
				},
			},
		},
	}
}

func TestApply_DryRun_NoVolumeMutation(t *testing.T) {
	ctx := context.Background()
	driver := fake.New()
	exec := apply.NewExecutor(driver)

	p := volumeOpPlan([]plan.VolumeOp{
		{Op: "create", Pool: "default", Name: "db-vm-data", ContentType: "filesystem", Size: "100GiB"},
		{Op: "grow", Pool: "default", Name: "db-vm-wal", ContentType: "block", Size: "40GiB"},
	})
	report, err := exec.Apply(ctx, p, apply.ApplyOpts{Jobs: 1, DryRun: true})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if report.ExitCode != 0 {
		t.Fatalf("expected exit 0 on dry-run, got %d (%+v)", report.ExitCode, report.Errors)
	}
	// No volume must be created or grown; no instance must be created.
	if _, _, err := driver.GetStoragePoolVolume(ctx, "default", "custom", "db-vm-data"); err == nil {
		t.Errorf("dry-run created a volume; storage volume ops must be skipped")
	}
	if _, ok := driver.Instances["db-vm"]; ok {
		t.Errorf("dry-run created the instance; instance steps must be skipped")
	}
}

func TestApply_VolumeOps_CreatedBeforeInstance(t *testing.T) {
	ctx := context.Background()
	driver := fake.New()
	exec := apply.NewExecutor(driver)

	p := volumeOpPlan([]plan.VolumeOp{
		{Op: "create", Pool: "default", Name: "db-vm-data", ContentType: "filesystem", Size: "100GiB"},
	})
	report, err := exec.Apply(ctx, p, apply.ApplyOpts{Jobs: 1})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if report.ExitCode != 0 {
		t.Fatalf("expected exit 0, got %d (%+v)", report.ExitCode, report.Errors)
	}
	// Volume provisioned before/during instance create.
	vol, _, err := driver.GetStoragePoolVolume(ctx, "default", "custom", "db-vm-data")
	if err != nil {
		t.Fatalf("volume not created: %v", err)
	}
	if vol.ContentType != "filesystem" || vol.Config["size"] != "100GiB" {
		t.Errorf("unexpected volume: %+v", vol)
	}
	if _, ok := driver.Instances["db-vm"]; !ok {
		t.Errorf("instance not created")
	}
}

func TestApply_VolumeOps_CreateIdempotent_NoGrow(t *testing.T) {
	ctx := context.Background()
	driver := fake.New()
	if err := driver.CreateStoragePoolVolume(ctx, "default", provider.StorageVolumeCreateRequest{
		Name: "db-vm-data", Type: "custom", ContentType: "filesystem",
		Config: map[string]string{"size": "100GiB"},
	}); err != nil {
		t.Fatalf("seeding volume: %v", err)
	}
	exec := apply.NewExecutor(driver)

	p := volumeOpPlan([]plan.VolumeOp{
		{Op: "create", Pool: "default", Name: "db-vm-data", ContentType: "filesystem", Size: "100GiB"},
	})
	report, err := exec.Apply(ctx, p, apply.ApplyOpts{Jobs: 1})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if report.ExitCode != 0 {
		t.Fatalf("expected exit 0, got %d (%+v)", report.ExitCode, report.Errors)
	}
	vol, _, _ := driver.GetStoragePoolVolume(ctx, "default", "custom", "db-vm-data")
	if vol.Config["size"] != "100GiB" {
		t.Errorf("volume size must be unchanged, got %q", vol.Config["size"])
	}
}

func TestApply_VolumeOps_CreateGrowIfSmaller(t *testing.T) {
	ctx := context.Background()
	driver := fake.New()
	if err := driver.CreateStoragePoolVolume(ctx, "default", provider.StorageVolumeCreateRequest{
		Name: "db-vm-data", Type: "custom", ContentType: "filesystem",
		Config: map[string]string{"size": "50GiB"},
	}); err != nil {
		t.Fatalf("seeding volume: %v", err)
	}
	exec := apply.NewExecutor(driver)

	p := volumeOpPlan([]plan.VolumeOp{
		{Op: "create", Pool: "default", Name: "db-vm-data", ContentType: "filesystem", Size: "100GiB"},
	})
	report, err := exec.Apply(ctx, p, apply.ApplyOpts{Jobs: 1})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if report.ExitCode != 0 {
		t.Fatalf("expected exit 0, got %d (%+v)", report.ExitCode, report.Errors)
	}
	vol, _, _ := driver.GetStoragePoolVolume(ctx, "default", "custom", "db-vm-data")
	if vol.Config["size"] != "100GiB" {
		t.Errorf("expected grow to 100GiB, got %q", vol.Config["size"])
	}
}

func TestApply_VolumeOps_Grow(t *testing.T) {
	ctx := context.Background()
	driver := fake.New()
	if err := driver.CreateStoragePoolVolume(ctx, "default", provider.StorageVolumeCreateRequest{
		Name: "db-vm-wal", Type: "custom", ContentType: "block",
		Config: map[string]string{"size": "20GiB"},
	}); err != nil {
		t.Fatalf("seeding volume: %v", err)
	}
	exec := apply.NewExecutor(driver)

	p := volumeOpPlan([]plan.VolumeOp{
		{Op: "grow", Pool: "default", Name: "db-vm-wal", ContentType: "block", Size: "40GiB"},
	})
	report, err := exec.Apply(ctx, p, apply.ApplyOpts{Jobs: 1})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if report.ExitCode != 0 {
		t.Fatalf("expected exit 0, got %d (%+v)", report.ExitCode, report.Errors)
	}
	vol, _, _ := driver.GetStoragePoolVolume(ctx, "default", "custom", "db-vm-wal")
	if vol.Config["size"] != "40GiB" {
		t.Errorf("expected grow to 40GiB, got %q", vol.Config["size"])
	}
}

func TestApply_VolumeOps_ContentTypeConflict(t *testing.T) {
	ctx := context.Background()
	driver := fake.New()
	if err := driver.CreateStoragePoolVolume(ctx, "default", provider.StorageVolumeCreateRequest{
		Name: "db-vm-data", Type: "custom", ContentType: "block",
		Config: map[string]string{"size": "100GiB"},
	}); err != nil {
		t.Fatalf("seeding volume: %v", err)
	}
	exec := apply.NewExecutor(driver)

	p := volumeOpPlan([]plan.VolumeOp{
		{Op: "create", Pool: "default", Name: "db-vm-data", ContentType: "filesystem", Size: "100GiB"},
	})
	report, err := exec.Apply(ctx, p, apply.ApplyOpts{Jobs: 1})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if report.ExitCode != 4 {
		t.Fatalf("expected exit 4 on content-type conflict, got %d (%+v)", report.ExitCode, report.Errors)
	}
	if len(report.Errors) == 0 || !strings.Contains(report.Errors[0].Message, "content type") {
		t.Errorf("expected content-type error, got %+v", report.Errors)
	}
}

func TestApply_VolumeOps_GrowMissingVolume_Fails(t *testing.T) {
	ctx := context.Background()
	driver := fake.New()
	exec := apply.NewExecutor(driver)

	p := volumeOpPlan([]plan.VolumeOp{
		{Op: "grow", Pool: "default", Name: "never-created", ContentType: "filesystem", Size: "40GiB"},
	})
	report, err := exec.Apply(ctx, p, apply.ApplyOpts{Jobs: 1})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if report.ExitCode != 4 {
		t.Fatalf("expected exit 4 on grow of missing volume, got %d", report.ExitCode)
	}
	if _, ok := driver.Instances["db-vm"]; ok {
		t.Errorf("instance create must not run when a volume op fails")
	}
}
