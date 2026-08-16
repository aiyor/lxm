package plan_test

import (
	"strings"
	"testing"

	"github.com/aiyor/lxm/internal/config"
	"github.com/aiyor/lxm/internal/network"
	"github.com/aiyor/lxm/internal/plan"
	"github.com/canonical/lxd/shared/api"
)

func TestPlan_DiskStatusAbsent_EmitsDeleteVolumeOp(t *testing.T) {
	manifest := &config.Config{
		Name: "vm1",
		Type: "virtual-machine",
		User: "ubuntu",
		Disks: []config.DiskConfig{
			{
				Name:   "scratch",
				Status: "absent",
				Pool:   "default",
				Source: "vm1-scratch",
			},
		},
	}

	live := &plan.InstanceSnapshot{
		Name:   "vm1",
		Type:   "virtual-machine",
		Status: "Stopped",
		Devices: map[string]map[string]string{
			"root":         {"type": "disk", "pool": "default", "path": "/"},
			"disk-scratch": {"type": "disk", "pool": "default", "path": "/mnt/scratch", "source": "vm1-scratch"},
		},
	}

	volumes := map[string]map[string]*api.StorageVolume{
		"default": {
			"vm1-scratch": {
				Name:        "vm1-scratch",
				Pool:        "default",
				Type:        "custom",
				ContentType: "filesystem",
				Config:      map[string]string{"user.lxm.managed": "true"},
			},
		},
	}

	rec := plan.NewReconciler()
	p, err := rec.Compute(manifest, map[string]*plan.InstanceSnapshot{"vm1": live}, volumes, nil, nil, true)
	if err != nil {
		t.Fatalf("Compute error: %v", err)
	}

	if len(p.Steps) != 1 {
		t.Fatalf("expected 1 step, got %d", len(p.Steps))
	}
	step := p.Steps[0]

	// Must have VolumeOp with Op: "delete"
	var foundDelete bool
	for _, op := range step.VolumeOps {
		if op.Op == "delete" && op.Name == "vm1-scratch" && op.Pool == "default" {
			foundDelete = true
		}
	}
	if !foundDelete {
		t.Errorf("expected VolumeOp{Op: delete, Name: vm1-scratch}, got: %+v", step.VolumeOps)
	}

	// Device must be removed from InstancePut
	if step.InstancePut != nil && step.InstancePut.Devices != nil {
		if _, exists := step.InstancePut.Devices["disk-scratch"]; exists {
			t.Errorf("expected disk-scratch removed from InstancePut.Devices, but it exists: %+v", step.InstancePut.Devices)
		}
	}
}

func TestPlan_DiskStatusAbsent_ExternalDisk_DetachesOnly(t *testing.T) {
	manifest := &config.Config{
		Name: "vm1",
		Type: "virtual-machine",
		User: "ubuntu",
		Disks: []config.DiskConfig{
			{
				Name:   "extdata",
				Status: "absent",
				Pool:   "default",
				Source: "external-vol-1",
			},
		},
	}

	live := &plan.InstanceSnapshot{
		Name:   "vm1",
		Type:   "virtual-machine",
		Status: "Stopped",
		Devices: map[string]map[string]string{
			"root":         {"type": "disk", "pool": "default", "path": "/"},
			"disk-extdata": {"type": "disk", "pool": "default", "path": "/mnt/ext", "source": "external-vol-1"},
		},
	}

	volumes := map[string]map[string]*api.StorageVolume{
		"default": {
			"external-vol-1": {Name: "external-vol-1", Pool: "default", Type: "custom", ContentType: "filesystem"},
		},
	}

	rec := plan.NewReconciler()
	p, err := rec.Compute(manifest, map[string]*plan.InstanceSnapshot{"vm1": live}, volumes, nil, nil, true)
	if err != nil {
		t.Fatalf("Compute error: %v", err)
	}

	step := p.Steps[0]
	for _, op := range step.VolumeOps {
		if op.Op == "delete" {
			t.Errorf("external disk must never generate delete volume op, got: %+v", op)
		}
	}
}

func TestPlan_DiskAttachFalse_DetachesWithoutDeleting(t *testing.T) {
	attachFalse := false
	manifest := &config.Config{
		Name: "vm1",
		Type: "virtual-machine",
		User: "ubuntu",
		Disks: []config.DiskConfig{
			{
				Name:   "data",
				Status: "present",
				Attach: &attachFalse,
				Size:   "50GiB",
				Pool:   "default",
				Path:   "/var/lib/data",
				Source: "vm1-data",
			},
		},
	}

	live := &plan.InstanceSnapshot{
		Name:   "vm1",
		Type:   "virtual-machine",
		Status: "Stopped",
		Devices: map[string]map[string]string{
			"root":      {"type": "disk", "pool": "default", "path": "/"},
			"disk-data": {"type": "disk", "pool": "default", "path": "/var/lib/data", "source": "vm1-data"},
		},
	}

	volumes := map[string]map[string]*api.StorageVolume{
		"default": {
			"vm1-data": {
				Name:        "vm1-data",
				Pool:        "default",
				Type:        "custom",
				ContentType: "filesystem",
				Config:      map[string]string{"size": "53687091200"},
			},
		},
	}

	rec := plan.NewReconciler()
	p, err := rec.Compute(manifest, map[string]*plan.InstanceSnapshot{"vm1": live}, volumes, nil, nil, true)
	if err != nil {
		t.Fatalf("Compute error: %v", err)
	}

	step := p.Steps[0]
	if !step.Changed {
		t.Fatalf("expected step.Changed = true for attach: false")
	}

	for _, op := range step.VolumeOps {
		if op.Op == "delete" {
			t.Errorf("attach: false must not generate delete volume op, got: %+v", op)
		}
	}

	// Device must not be in InstancePut
	if _, exists := step.InstancePut.Devices["disk-data"]; exists {
		t.Errorf("expected disk-data detached in InstancePut, but found: %+v", step.InstancePut.Devices)
	}
}

func TestPlan_VSwitchStatusAbsent_EmitsDeleteStep(t *testing.T) {
	fleet := &network.Fleet{
		VSwitches: []*network.VSwitch{
			{
				VSwitchConfig: config.VSwitchConfig{
					Name:   "legacybr0",
					Status: "absent",
				},
			},
		},
		ByName: map[string]*network.VSwitch{
			"legacybr0": {
				VSwitchConfig: config.VSwitchConfig{
					Name:   "legacybr0",
					Status: "absent",
				},
			},
		},
	}

	live := &plan.NetworkLiveState{
		Networks: map[string]*api.Network{
			"legacybr0": {
				Name:    "legacybr0",
				Managed: true,
				Config:  map[string]string{"user.lxm.managed": "true"},
			},
		},
		ACLs: map[string]*api.NetworkACL{
			"lxm-legacybr0": {
				Name:   "lxm-legacybr0",
				Config: map[string]string{"user.lxm.managed": "true"},
			},
		},
	}

	rec := plan.NewNetworkReconciler()
	np, err := rec.ComputeNetworks(fleet, live)
	if err != nil {
		t.Fatalf("ComputeNetworks error: %v", err)
	}

	if len(np.Steps) != 1 {
		t.Fatalf("expected 1 delete step, got %d", len(np.Steps))
	}
	if np.Steps[0].Kind != "delete_vswitch" || np.Steps[0].Name != "legacybr0" {
		t.Errorf("expected Kind delete_vswitch for legacybr0, got: %+v", np.Steps[0])
	}
}

func TestPlan_VSwitchStatusAbsent_InUseCheckFails(t *testing.T) {
	manifests := []*config.Config{
		{
			Name:   "web1",
			Status: "present",
			Networks: []config.NetworkConfig{
				{Name: "eth0", Parent: "dmzbr0"},
			},
		},
	}

	fleet := &network.Fleet{
		VSwitches: []*network.VSwitch{
			{
				VSwitchConfig: config.VSwitchConfig{
					Name:   "dmzbr0",
					Status: "absent",
				},
			},
		},
		ByName: map[string]*network.VSwitch{
			"dmzbr0": {
				VSwitchConfig: config.VSwitchConfig{
					Name:   "dmzbr0",
					Status: "absent",
				},
			},
		},
	}

	_, err := network.CheckInstances(manifests, fleet, map[string]bool{"dmzbr0": true})
	if err == nil {
		t.Fatal("expected error when vswitch marked status: absent is in use by instance, got nil")
	}
	if !strings.Contains(err.Error(), "cannot delete a vswitch currently attached to active instances") {
		t.Errorf("unexpected error message: %v", err)
	}
}

func TestPlan_DiskStatusAbsent_ExternalVolumeWithDerivedName_NotDeleted(t *testing.T) {
	// An external volume whose name matches <instance>-<disk> exactly,
	// but lacks user.lxm.managed: "true" marker, must NEVER be deleted.
	manifest := &config.Config{
		Name: "vm1",
		Type: "virtual-machine",
		User: "ubuntu",
		Disks: []config.DiskConfig{
			{
				Name:   "scratch",
				Status: "absent",
				Pool:   "default",
				Source: "vm1-scratch",
			},
		},
	}

	live := &plan.InstanceSnapshot{
		Name:   "vm1",
		Type:   "virtual-machine",
		Status: "Stopped",
		Devices: map[string]map[string]string{
			"root":         {"type": "disk", "pool": "default", "path": "/"},
			"disk-scratch": {"type": "disk", "pool": "default", "path": "/mnt/scratch", "source": "vm1-scratch"},
		},
	}

	volumes := map[string]map[string]*api.StorageVolume{
		"default": {
			"vm1-scratch": {
				Name:        "vm1-scratch",
				Pool:        "default",
				Type:        "custom",
				ContentType: "filesystem",
				Config:      map[string]string{}, // no user.lxm.managed: "true"
			},
		},
	}

	rec := plan.NewReconciler()
	p, err := rec.Compute(manifest, map[string]*plan.InstanceSnapshot{"vm1": live}, volumes, nil, nil, true)
	if err != nil {
		t.Fatalf("Compute error: %v", err)
	}

	step := p.Steps[0]
	for _, op := range step.VolumeOps {
		if op.Op == "delete" {
			t.Errorf("external volume matching derived name must not be deleted, got: %+v", op)
		}
	}

	// For external disks (Managed: false), no warning should be emitted (intended behavior)
	for _, w := range p.Warnings {
		if strings.Contains(w, "lacks user.lxm.managed marker") {
			t.Errorf("external disk should not emit unmarked volume warning, got: %s", w)
		}
	}
}

func TestPlan_DiskStatusAbsent_UnmarkedManagedVolume_EmitsWarning(t *testing.T) {
	// A managed disk (Managed: true) whose volume exists live without the marker
	// should emit a warning informing the operator that the volume was not deleted.
	manifest := &config.Config{
		Name: "vm1",
		Type: "virtual-machine",
		User: "ubuntu",
		Disks: []config.DiskConfig{
			{
				Name:    "scratch",
				Status:  "absent",
				Pool:    "default",
				Source:  "vm1-scratch",
				Managed: true,
			},
		},
	}

	live := &plan.InstanceSnapshot{
		Name:   "vm1",
		Type:   "virtual-machine",
		Status: "Stopped",
		Devices: map[string]map[string]string{
			"root":         {"type": "disk", "pool": "default", "path": "/"},
			"disk-scratch": {"type": "disk", "pool": "default", "path": "/mnt/scratch", "source": "vm1-scratch"},
		},
	}

	volumes := map[string]map[string]*api.StorageVolume{
		"default": {
			"vm1-scratch": {
				Name:        "vm1-scratch",
				Pool:        "default",
				Type:        "custom",
				ContentType: "filesystem",
				Config:      map[string]string{}, // lacks user.lxm.managed
			},
		},
	}

	rec := plan.NewReconciler()
	p, err := rec.Compute(manifest, map[string]*plan.InstanceSnapshot{"vm1": live}, volumes, nil, nil, true)
	if err != nil {
		t.Fatalf("Compute error: %v", err)
	}

	step := p.Steps[0]
	for _, op := range step.VolumeOps {
		if op.Op == "delete" {
			t.Errorf("unmarked volume must not be deleted, got: %+v", op)
		}
	}

	// Verify plan warning emitted for unmarked managed volume
	var foundWarning bool
	for _, w := range p.Warnings {
		if strings.Contains(w, "lacks user.lxm.managed marker; detaching from instance without deleting storage volume") {
			foundWarning = true
		}
	}
	if !foundWarning {
		t.Errorf("expected plan warning for unmarked managed volume on status: absent, got warnings: %+v", p.Warnings)
	}
}

func TestPlan_VSwitchStatusAbsent_LiveNetworkInUse_FailsPlan(t *testing.T) {
	fleet := &network.Fleet{
		VSwitches: []*network.VSwitch{
			{
				VSwitchConfig: config.VSwitchConfig{
					Name:   "legacybr0",
					Status: "absent",
				},
			},
		},
		ByName: map[string]*network.VSwitch{
			"legacybr0": {
				VSwitchConfig: config.VSwitchConfig{
					Name:   "legacybr0",
					Status: "absent",
				},
			},
		},
	}

	live := &plan.NetworkLiveState{
		Networks: map[string]*api.Network{
			"legacybr0": {
				Name:    "legacybr0",
				Managed: true,
				UsedBy:  []string{"/1.0/instances/running-vm"},
			},
		},
	}

	rec := plan.NewNetworkReconciler()
	_, err := rec.ComputeNetworks(fleet, live)
	if err == nil {
		t.Fatal("expected error when live network is used by instances, got nil")
	}
	if !strings.Contains(err.Error(), "cannot be deleted (status: absent); 1 instance(s) still attached") {
		t.Errorf("unexpected error message: %v", err)
	}
}

func TestPlan_DiskAttach_Reattach_VM_HotplugNoRestart(t *testing.T) {
	attachTrue := true
	manifest := &config.Config{
		Name: "vm1",
		Type: "virtual-machine",
		User: "ubuntu",
		Disks: []config.DiskConfig{
			{
				Name:   "data",
				Status: "present",
				Attach: &attachTrue,
				Size:   "50GiB",
				Pool:   "default",
				Path:   "/var/lib/data",
				Source: "vm1-data",
			},
		},
	}

	// Live instance is Running and disk is currently detached (not in Devices)
	live := &plan.InstanceSnapshot{
		Name:   "vm1",
		Type:   "virtual-machine",
		Status: "Running",
		Devices: map[string]map[string]string{
			"root": {"type": "disk", "pool": "default", "path": "/"},
		},
	}

	volumes := map[string]map[string]*api.StorageVolume{
		"default": {
			"vm1-data": {
				Name:        "vm1-data",
				Pool:        "default",
				Type:        "custom",
				ContentType: "filesystem",
				Config:      map[string]string{"user.lxm.managed": "true", "size": "53687091200"},
			},
		},
	}

	rec := plan.NewReconciler()
	p, err := rec.Compute(manifest, map[string]*plan.InstanceSnapshot{"vm1": live}, volumes, nil, nil, true)
	if err != nil {
		t.Fatalf("Compute error: %v", err)
	}

	step := p.Steps[0]
	if step.PowerTransition != "" {
		t.Errorf("attaching disk to running VM is hotplugged and must not restart, got: %q", step.PowerTransition)
	}
	if step.InstancePut == nil || step.InstancePut.Devices["disk-data"] == nil {
		t.Errorf("expected disk-data device in InstancePut.Devices, got: %+v", step.InstancePut)
	}
}
