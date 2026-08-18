package fake_test

import (
	"context"
	"strings"
	"testing"

	"github.com/aiyor/lxm/internal/provider"
	"github.com/aiyor/lxm/internal/provider/fake"
)

func TestFakeDriver_Lifecycle(t *testing.T) {
	ctx := context.Background()
	d := fake.New()

	// 1. Create Instance
	req := provider.InstanceCreateRequest{
		Name: "test-c1",
		Type: provider.InstanceTypeContainer,
		Source: provider.InstanceSource{
			Type:  "image",
			Alias: "ubuntu/24.04",
		},
		Config: map[string]string{
			"user.lxm.managed": "true",
		},
		Devices: map[string]map[string]string{
			"root": {"type": "disk", "path": "/", "pool": "default"},
		},
	}

	if err := d.CreateInstanceContext(ctx, req); err != nil {
		t.Fatalf("CreateInstance failed: %v", err)
	}

	// 2. Get Instance
	inst, etag, err := d.GetInstance("test-c1")
	if err != nil {
		t.Fatalf("GetInstance failed: %v", err)
	}
	if inst.Name != "test-c1" {
		t.Errorf("expected name 'test-c1', got %q", inst.Name)
	}
	if etag == "" {
		t.Errorf("expected non-empty etag")
	}

	// 3. Update Instance with ETag
	updateReq := provider.InstanceUpdateRequest{
		Config: map[string]string{
			"user.lxm.managed": "true",
			"limits.cpu":       "2",
		},
	}
	if err := d.UpdateInstanceContext(ctx, "test-c1", updateReq, etag); err != nil {
		t.Fatalf("UpdateInstance failed: %v", err)
	}

	// 4. Update with stale ETag should fail (OCC drift simulation)
	if err := d.UpdateInstanceContext(ctx, "test-c1", updateReq, "stale-etag"); err == nil {
		t.Fatalf("expected error updating with stale ETag, got nil")
	}

	// 5. Snapshot operations
	if err := d.CreateInstanceSnapshotContext(ctx, "test-c1", "snap0", false); err != nil {
		t.Fatalf("CreateSnapshot failed: %v", err)
	}
	snaps, err := d.GetInstanceSnapshots("test-c1")
	if err != nil || len(snaps) != 1 || snaps[0].Name != "snap0" {
		t.Fatalf("GetInstanceSnapshots failed: %v, snaps: %+v", err, snaps)
	}

	// 6. State operations
	if err := d.UpdateInstanceStateContext(ctx, "test-c1", "start", false); err != nil {
		t.Fatalf("UpdateInstanceState start failed: %v", err)
	}
	inst, _, _ = d.GetInstance("test-c1")
	if inst.Status != "Running" {
		t.Errorf("expected status 'Running', got %q", inst.Status)
	}

	// 7. File operations
	content := strings.NewReader("hello world")
	if err := d.CreateInstanceFile("test-c1", "/tmp/test.txt", content, 0644, 0, 0); err != nil {
		t.Fatalf("CreateInstanceFile failed: %v", err)
	}
	if err := d.DeleteInstanceFile("test-c1", "/tmp/test.txt"); err != nil {
		t.Fatalf("DeleteInstanceFile failed: %v", err)
	}

	// 8. Delete Instance
	if err := d.DeleteInstanceContext(ctx, "test-c1"); err != nil {
		t.Fatalf("DeleteInstance failed: %v", err)
	}
	if _, _, err := d.GetInstance("test-c1"); err == nil {
		t.Fatalf("expected deleted instance to return error, got nil")
	}
}

func TestFakeDriver_ClusterAndProjects(t *testing.T) {
	d := fake.New()

	// Cluster
	if d.IsClustered() {
		t.Errorf("expected not clustered by default")
	}
	d.SetClustered(true)
	d.SetClusterMembers([]provider.ClusterMember{
		{ServerName: "incus-node1", Status: provider.ClusterMemberStatusOnline, Roles: []string{"database"}},
		{ServerName: "incus-node2", Status: provider.ClusterMemberStatusOnline, Roles: []string{}},
	})

	if !d.IsClustered() {
		t.Errorf("expected clustered true")
	}
	members, err := d.GetClusterMembers()
	if err != nil || len(members) != 2 {
		t.Fatalf("GetClusterMembers failed: %v, count: %d", err, len(members))
	}
	m, err := d.GetClusterMember("incus-node1")
	if err != nil || m.ServerName != "incus-node1" {
		t.Fatalf("GetClusterMember incus-node1 failed: %v", err)
	}

	// Target scoping
	scoped := d.UseTarget("incus-node2")
	if scoped == nil {
		t.Fatalf("UseTarget returned nil")
	}

	// Projects
	exists, err := d.ProjectExists("default")
	if err != nil || !exists {
		t.Errorf("expected project 'default' to exist")
	}
	if err := d.CreateProject("staging", "Staging environment"); err != nil {
		t.Fatalf("CreateProject failed: %v", err)
	}
	projScoped := d.UseProject("staging")
	if projScoped == nil {
		t.Fatalf("UseProject returned nil")
	}
}
