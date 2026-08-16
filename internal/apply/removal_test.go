package apply_test

import (
	"context"
	"testing"

	"github.com/aiyor/lxm/internal/apply"
	"github.com/aiyor/lxm/internal/config"
	"github.com/aiyor/lxm/internal/lxd"
	"github.com/aiyor/lxm/internal/plan"
	"github.com/canonical/lxd/shared/api"
)

func TestApply_VolumeDelete_Phase3_Success(t *testing.T) {
	fake := lxd.NewFakeInstanceServer()
	fake.Instances["vm1"] = &api.Instance{
		Name:   "vm1",
		Status: "Stopped",
		Devices: map[string]map[string]string{
			"root":         {"type": "disk", "pool": "default", "path": "/"},
			"disk-scratch": {"type": "disk", "pool": "default", "path": "/mnt/scratch", "source": "vm1-scratch"},
		},
		ExpandedDevices: map[string]map[string]string{
			"root":         {"type": "disk", "pool": "default", "path": "/"},
			"disk-scratch": {"type": "disk", "pool": "default", "path": "/mnt/scratch", "source": "vm1-scratch"},
		},
	}
	fake.ETags["vm1"] = "etag-1"

	// Add volume to storage store
	fake.Vols.Volumes["default/vm1-scratch"] = &api.StorageVolume{
		Name:        "vm1-scratch",
		Pool:        "default",
		Type:        "custom",
		ContentType: "filesystem",
		Config:      map[string]string{"user.lxm.managed": "true"},
	}

	engine := apply.NewExecutor(fake)

	p := &plan.Plan{
		Steps: []plan.Step{
			{
				Container: "vm1",
				Action:    "update",
				Changed:   true,
				ETag:      "etag-1",
				InstancePut: &api.InstancePut{
					Devices: map[string]map[string]string{
						"root": {"type": "disk", "pool": "default", "path": "/"},
					},
				},
				VolumeOps: []plan.VolumeOp{
					{
						Op:          "delete",
						Pool:        "default",
						Name:        "vm1-scratch",
						ContentType: "filesystem",
					},
				},
			},
		},
	}

	report, err := engine.Apply(context.Background(), p, apply.ApplyOpts{})
	if err != nil {
		t.Fatalf("Apply error: %v", err)
	}
	if report.ExitCode != 0 {
		t.Fatalf("expected exit code 0, got %d (errors: %+v)", report.ExitCode, report.Errors)
	}

	// Verify volume was deleted in Phase 3
	if _, exists := fake.Vols.Volumes["default/vm1-scratch"]; exists {
		t.Errorf("expected storage volume default/vm1-scratch to be deleted, but it still exists")
	}

	// Verify device was detached in Phase 2
	if _, exists := fake.Instances["vm1"].Devices["disk-scratch"]; exists {
		t.Errorf("expected device disk-scratch detached from vm1, but it still exists")
	}
}

func TestApply_VolumeDelete_IdempotentNotFound(t *testing.T) {
	fake := lxd.NewFakeInstanceServer()
	fake.Instances["vm1"] = &api.Instance{
		Name:   "vm1",
		Status: "Stopped",
	}
	fake.ETags["vm1"] = "etag-1"

	engine := apply.NewExecutor(fake)

	p := &plan.Plan{
		Steps: []plan.Step{
			{
				Container: "vm1",
				Action:    "update",
				Changed:   true,
				ETag:      "etag-1",
				InstancePut: &api.InstancePut{
					Devices: map[string]map[string]string{
						"root": {"type": "disk", "pool": "default", "path": "/"},
					},
				},
				VolumeOps: []plan.VolumeOp{
					{
						Op:          "delete",
						Pool:        "default",
						Name:        "non-existent-vol",
						ContentType: "filesystem",
					},
				},
			},
		},
	}

	report, err := engine.Apply(context.Background(), p, apply.ApplyOpts{})
	if err != nil {
		t.Fatalf("Apply error: %v", err)
	}
	if report.ExitCode != 0 {
		t.Fatalf("expected exit code 0 on missing volume deletion (idempotent), got %d (errors: %+v)", report.ExitCode, report.Errors)
	}
}

func TestApply_VSwitchDelete_Phase4_OrderBridgeBeforeACL(t *testing.T) {
	fake := lxd.NewFakeInstanceServer()
	fake.Nets.Networks["legacybr0"] = &api.Network{
		Name:    "legacybr0",
		Type:    "bridge",
		Managed: true,
		Config:  map[string]string{"user.lxm.managed": "true"},
	}
	fake.Nets.ACLs["lxm-legacybr0"] = &api.NetworkACL{
		Name:   "lxm-legacybr0",
		Config: map[string]string{"user.lxm.managed": "true"},
	}

	var callOrder []string
	fake.DeleteNetworkFunc = func(name string) error {
		callOrder = append(callOrder, "DeleteNetwork:"+name)
		delete(fake.Nets.Networks, name)
		return nil
	}
	fake.DeleteNetworkACLFunc = func(name string) error {
		callOrder = append(callOrder, "DeleteNetworkACL:"+name)
		delete(fake.Nets.ACLs, name)
		return nil
	}

	engine := apply.NewExecutor(fake)

	p := &plan.Plan{
		NetworkSteps: []plan.NetworkStep{
			{
				Kind:    "delete_vswitch",
				Name:    "legacybr0",
				Changed: true,
			},
		},
	}

	report, err := engine.Apply(context.Background(), p, apply.ApplyOpts{})
	if err != nil {
		t.Fatalf("Apply error: %v", err)
	}
	if report.ExitCode != 0 {
		t.Fatalf("expected exit code 0, got %d (errors: %+v)", report.ExitCode, report.Errors)
	}

	if len(callOrder) != 2 {
		t.Fatalf("expected 2 calls, got %d: %v", len(callOrder), callOrder)
	}
	if callOrder[0] != "DeleteNetwork:legacybr0" {
		t.Errorf("expected DeleteNetwork called first, got %q", callOrder[0])
	}
	if callOrder[1] != "DeleteNetworkACL:lxm-legacybr0" {
		t.Errorf("expected DeleteNetworkACL called second, got %q", callOrder[1])
	}
}

func TestApply_DryRun_SkipsDeletions(t *testing.T) {
	fake := lxd.NewFakeInstanceServer()
	fake.Vols.Volumes["default/vm1-scratch"] = &api.StorageVolume{
		Name: "vm1-scratch",
		Pool: "default",
	}
	fake.Nets.Networks["legacybr0"] = &api.Network{Name: "legacybr0"}
	fake.Nets.ACLs["lxm-legacybr0"] = &api.NetworkACL{Name: "lxm-legacybr0"}

	engine := apply.NewExecutor(fake)

	p := &plan.Plan{
		NetworkSteps: []plan.NetworkStep{
			{
				Kind:    "delete_vswitch",
				Name:    "legacybr0",
				Changed: true,
			},
		},
		Steps: []plan.Step{
			{
				Container: "vm1",
				Action:    "update",
				Changed:   true,
				VolumeOps: []plan.VolumeOp{
					{
						Op:   "delete",
						Pool: "default",
						Name: "vm1-scratch",
					},
				},
			},
		},
	}

	report, err := engine.Apply(context.Background(), p, apply.ApplyOpts{DryRun: true})
	if err != nil {
		t.Fatalf("Apply error: %v", err)
	}
	if report.ExitCode != 0 {
		t.Fatalf("expected exit code 0, got %d", report.ExitCode)
	}

	// Verify nothing was deleted
	if _, exists := fake.Vols.Volumes["default/vm1-scratch"]; !exists {
		t.Errorf("dry-run must not delete storage volume")
	}
	if _, exists := fake.Nets.Networks["legacybr0"]; !exists {
		t.Errorf("dry-run must not delete network bridge")
	}
	if _, exists := fake.Nets.ACLs["lxm-legacybr0"]; !exists {
		t.Errorf("dry-run must not delete network ACL")
	}
}

func TestApply_SteadyStateVolume_BackfillsMarker(t *testing.T) {
	fake := lxd.NewFakeInstanceServer()
	fake.Instances["vm1"] = &api.Instance{
		Name:   "vm1",
		Status: "Running",
		Devices: map[string]map[string]string{
			"root":      {"type": "disk", "pool": "default", "path": "/"},
			"disk-data": {"type": "disk", "pool": "default", "path": "/var/lib/data", "source": "vm1-data"},
		},
	}
	fake.ETags["vm1"] = "etag-1"

	// Pre-existing legacy volume without user.lxm.managed marker
	fake.Vols.Volumes["default/vm1-data"] = &api.StorageVolume{
		Name:        "vm1-data",
		Pool:        "default",
		Type:        "custom",
		ContentType: "filesystem",
		Config:      map[string]string{"size": "50GiB"}, // missing user.lxm.managed
	}

	engine := apply.NewExecutor(fake)

	// Steady-state step: real noop with no InstancePut or InstancesPost payload
	p := &plan.Plan{
		Steps: []plan.Step{
			{
				Container: "vm1",
				Action:    "noop",
				Changed:   false,
				ManagedDisks: []config.DiskConfig{
					{
						Name:   "data",
						Pool:   "default",
						Source: "vm1-data",
						Size:   "50GiB",
					},
				},
			},
		},
	}

	report, err := engine.Apply(context.Background(), p, apply.ApplyOpts{})
	if err != nil {
		t.Fatalf("Apply error: %v", err)
	}
	if report.ExitCode != 0 {
		t.Fatalf("expected exit code 0, got %d", report.ExitCode)
	}

	// Verify volume received user.lxm.managed: "true" and tracking metadata
	vol := fake.Vols.Volumes["default/vm1-data"]
	if vol == nil {
		t.Fatal("expected volume to exist")
	}
	if vol.Config["user.lxm.managed"] != "true" {
		t.Errorf("expected user.lxm.managed: true backfilled, got: %v", vol.Config["user.lxm.managed"])
	}
	if vol.Config["user.lxm.instance"] != "vm1" || vol.Config["user.lxm.disk"] != "data" {
		t.Errorf("expected instance/disk metadata backfilled, got: %+v", vol.Config)
	}
}

func TestApply_ExternalVolume_NotBackfilledAsManaged(t *testing.T) {
	fake := lxd.NewFakeInstanceServer()
	fake.Instances["vm1"] = &api.Instance{
		Name:   "vm1",
		Status: "Running",
		Devices: map[string]map[string]string{
			"root":         {"type": "disk", "pool": "default", "path": "/"},
			"disk-scratch": {"type": "disk", "pool": "default", "path": "/mnt/scratch", "source": "vm1-scratch"},
		},
	}
	fake.ETags["vm1"] = "etag-1"

	// Pre-existing external volume without user.lxm.managed marker
	fake.Vols.Volumes["default/vm1-scratch"] = &api.StorageVolume{
		Name:        "vm1-scratch",
		Pool:        "default",
		Type:        "custom",
		ContentType: "filesystem",
		Config:      map[string]string{}, // unmanaged external
	}

	engine := apply.NewExecutor(fake)

	// An instance with no ManagedDisks (e.g. disk declared as external source)
	p := &plan.Plan{
		Steps: []plan.Step{
			{
				Container:    "vm1",
				Action:       "noop",
				Changed:      false,
				ManagedDisks: nil, // external disk is NOT in ManagedDisks
			},
		},
	}

	report, err := engine.Apply(context.Background(), p, apply.ApplyOpts{})
	if err != nil {
		t.Fatalf("Apply error: %v", err)
	}
	if report.ExitCode != 0 {
		t.Fatalf("expected exit code 0, got %d", report.ExitCode)
	}

	// Verify external volume was NOT tagged as managed
	vol := fake.Vols.Volumes["default/vm1-scratch"]
	if vol == nil {
		t.Fatal("expected volume to exist")
	}
	if vol.Config["user.lxm.managed"] == "true" {
		t.Errorf("external volume must not be backfilled as user.lxm.managed")
	}
}
