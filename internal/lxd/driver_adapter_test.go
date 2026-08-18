package lxd

import (
	"context"
	"testing"

	"github.com/canonical/lxd/shared/api"

	"github.com/aiyor/lxm/internal/provider/fake"
)

func TestDriverAdapter_FullLifecycle(t *testing.T) {
	fakeDriver := fake.New()
	svc := NewServiceFromDriver(fakeDriver)

	// Create Instance
	createReq := api.InstancesPost{
		Name: "test-box",
		Type: api.InstanceType("container"),
		InstancePut: api.InstancePut{
			Config: map[string]string{
				"image.os": "ubuntu",
			},
			Devices: map[string]map[string]string{
				"root": {"type": "disk", "path": "/"},
			},
		},
	}
	if err := svc.CreateInstance(createReq); err != nil {
		t.Fatalf("CreateInstance failed: %v", err)
	}

	// Get Instance
	inst, etag, err := svc.GetInstance("test-box")
	if err != nil {
		t.Fatalf("GetInstance failed: %v", err)
	}
	if inst == nil || inst.Name != "test-box" {
		t.Fatalf("unexpected instance: %+v", inst)
	}
	if etag == "" {
		t.Fatalf("expected non-empty etag")
	}

	// Update Instance
	updateReq := api.InstancePut{
		Config: map[string]string{
			"image.os": "ubuntu",
			"user.lxm": "true",
		},
		Devices: inst.Devices,
	}
	if err := svc.UpdateInstance("test-box", updateReq, etag); err != nil {
		t.Fatalf("UpdateInstance failed: %v", err)
	}

	// List Instances
	list, err := svc.ListInstances()
	if err != nil {
		t.Fatalf("ListInstances failed: %v", err)
	}
	if len(list) != 1 || list[0].Name != "test-box" {
		t.Fatalf("unexpected list output: %+v", list)
	}

	// Snapshot Operations
	snapReq := api.InstanceSnapshotsPost{Name: "snap0"}
	if err := svc.CreateInstanceSnapshot("test-box", snapReq); err != nil {
		t.Fatalf("CreateInstanceSnapshot failed: %v", err)
	}
	snaps, err := svc.GetInstanceSnapshots("test-box")
	if err != nil || len(snaps) != 1 || snaps[0].Name != "snap0" {
		t.Fatalf("unexpected snapshots: %+v (err=%v)", snaps, err)
	}
	if err := svc.RestoreInstanceSnapshot("test-box", "snap0"); err != nil {
		t.Fatalf("RestoreInstanceSnapshot failed: %v", err)
	}
	if err := svc.DeleteInstanceSnapshot("test-box", "snap0"); err != nil {
		t.Fatalf("DeleteInstanceSnapshot failed: %v", err)
	}

	// Networks & ACLs
	netSvc, ok := svc.(NetworkService)
	if !ok {
		t.Fatalf("expected svc to implement NetworkService")
	}
	if err := netSvc.CreateNetwork(api.NetworksPost{Name: "br-test", Type: "bridge"}); err != nil {
		t.Fatalf("CreateNetwork failed: %v", err)
	}
	net, _, err := netSvc.GetNetwork("br-test")
	if err != nil || net == nil || net.Name != "br-test" {
		t.Fatalf("GetNetwork failed: %+v (err=%v)", net, err)
	}
	if err := netSvc.CreateNetworkACL(api.NetworkACLsPost{
		NetworkACLPost: api.NetworkACLPost{Name: "acl-test"},
		NetworkACLPut: api.NetworkACLPut{
			Description: "test acl",
			Ingress: []api.NetworkACLRule{
				{Action: "allow", Protocol: "tcp", DestinationPort: "80"},
			},
		},
	}); err != nil {
		t.Fatalf("CreateNetworkACL failed: %v", err)
	}
	acls, err := netSvc.GetNetworkACLs()
	if err != nil || len(acls) != 1 || acls[0].Name != "acl-test" {
		t.Fatalf("GetNetworkACLs failed: %+v (err=%v)", acls, err)
	}

	// Storage Volumes
	storageSvc, ok := svc.(StorageService)
	if !ok {
		t.Fatalf("expected svc to implement StorageService")
	}
	if err := storageSvc.CreateStoragePoolVolume("default", api.StorageVolumesPost{
		Name:        "vol-test",
		Type:        "custom",
		ContentType: "filesystem",
	}); err != nil {
		t.Fatalf("CreateStoragePoolVolume failed: %v", err)
	}
	vol, _, err := storageSvc.GetStoragePoolVolume("default", "custom", "vol-test")
	if err != nil || vol == nil || vol.Name != "vol-test" {
		t.Fatalf("GetStoragePoolVolume failed: %+v (err=%v)", vol, err)
	}

	// Exec & UserEnv
	env, err := svc.ResolveUserEnv("test-box", "root")
	if err != nil || env == nil || env.User != "root" {
		t.Fatalf("ResolveUserEnv failed: %+v (err=%v)", env, err)
	}
	res, err := svc.ExecInstanceContext(context.Background(), "test-box", []string{"echo", "hello"}, 0, nil)
	if err != nil || res.ExitCode != 0 {
		t.Fatalf("ExecInstance failed: %+v (err=%v)", res, err)
	}

	// Delete Instance
	if err := svc.DeleteInstance("test-box"); err != nil {
		t.Fatalf("DeleteInstance failed: %v", err)
	}
	deletedInst, _, _ := svc.GetInstance("test-box")
	if deletedInst != nil {
		t.Fatalf("expected instance to be deleted")
	}
}
