package lxd_test

import (
	"context"
	"errors"
	"testing"

	"github.com/aiyor/lxm/internal/lxd"
	"github.com/canonical/lxd/shared/api"
)

func TestExecResult_Combined(t *testing.T) {
	res := lxd.ExecResult{
		ExitCode: 0,
		Stdout:   "hello",
		Stderr:   "world",
	}
	combined := res.Combined()
	expected := "hello\nworld"
	if combined != expected {
		t.Errorf("expected %q, got %q", expected, combined)
	}
}

func TestDeviceName(t *testing.T) {
	name := lxd.DeviceName("/mnt/data")
	if name != "mount--mnt-data" {
		t.Errorf("expected mount--mnt-data, got %q", name)
	}
}

func TestIsHex(t *testing.T) {
	if !lxd.IsHex("1a2b3c") {
		t.Error("expected 1a2b3c to be hex")
	}
	if lxd.IsHex("1a2b3z") {
		t.Error("expected 1a2b3z not to be hex")
	}
	if lxd.IsHex("") {
		t.Error("expected empty string not to be hex")
	}
}

func TestFakeInstanceServer_Full(t *testing.T) {
	fake := lxd.NewFakeInstanceServer()

	// 1. CreateInstance
	err := fake.CreateInstance(api.InstancesPost{
		Name: "test-box",
		InstancePut: api.InstancePut{
			Config: map[string]string{
				"user.lxm.managed": "true",
			},
		},
	})
	if err != nil {
		t.Fatalf("CreateInstance error: %v", err)
	}

	// 2. GetInstance
	inst, etag, err := fake.GetInstance("test-box")
	if err != nil || inst == nil {
		t.Fatalf("GetInstance error: %v", err)
	}
	if etag == "" {
		t.Errorf("expected non-empty etag")
	}

	// 3. UpdateInstance
	err = fake.UpdateInstance("test-box", api.InstancePut{
		Config: map[string]string{
			"user.lxm.managed": "true",
			"user.lxm.user":    "ubuntu",
		},
	}, etag)
	if err != nil {
		t.Fatalf("UpdateInstance error: %v", err)
	}

	// 4. UpdateInstanceState
	err = fake.UpdateInstanceState("test-box", "start", false)
	if err != nil {
		t.Fatalf("UpdateInstanceState error: %v", err)
	}
	if fake.Instances["test-box"].Status != "Running" {
		t.Errorf("expected Running status, got %s", fake.Instances["test-box"].Status)
	}

	// 5. RebuildInstance
	err = fake.RebuildInstance("test-box", api.InstanceRebuildPost{
		Source: api.InstanceSource{
			Properties: map[string]string{"os": "debian", "release": "12"},
		},
	})
	if err != nil {
		t.Fatalf("RebuildInstance error: %v", err)
	}
	if fake.Instances["test-box"].Config["image.os"] != "debian" {
		t.Errorf("expected debian image.os after rebuild")
	}

	// 6. HasExtension
	if !fake.HasExtension("instances_rebuild") {
		t.Errorf("expected HasExtension('instances_rebuild') == true")
	}

	// 7. ListInstances
	list, err := fake.ListInstances()
	if err != nil || len(list) != 1 {
		t.Fatalf("ListInstances error: %v, count: %d", err, len(list))
	}

	// 8. ClassifyLXDError
	code, retryable := fake.ClassifyLXDError(nil, "lookup")
	if code != 0 || retryable {
		t.Errorf("expected code 0, retryable false for nil error")
	}

	code, _ = fake.ClassifyLXDError(errors.New("not found"), "lookup")
	if code != 5 {
		t.Errorf("expected code 5 (TARGET_NOT_FOUND) for lookup error, got %d", code)
	}

	code, retryable = fake.ClassifyLXDError(errors.New("etag mismatch"), "update")
	if code != 4 || !retryable {
		t.Errorf("expected code 4 retryable for etag mismatch")
	}

	// 9. Snapshot Operations
	err = fake.CreateInstanceSnapshot("test-box", api.InstanceSnapshotsPost{Name: "snap-1"})
	if err != nil {
		t.Fatalf("CreateInstanceSnapshot error: %v", err)
	}

	snaps, err := fake.GetInstanceSnapshots("test-box")
	if err != nil || len(snaps) != 1 {
		t.Fatalf("GetInstanceSnapshots error: %v, count: %d", err, len(snaps))
	}

	err = fake.RestoreInstanceSnapshot("test-box", "snap-1")
	if err != nil {
		t.Fatalf("RestoreInstanceSnapshot error: %v", err)
	}

	// 11. DeleteInstance (running instances must be stopped first, matching
	// real LXD semantics for a non-forced delete).
	err = fake.UpdateInstanceState("test-box", "stop", false)
	if err != nil {
		t.Fatalf("UpdateInstanceState stop error: %v", err)
	}
	err = fake.DeleteInstance("test-box")
	if err != nil {
		t.Fatalf("DeleteInstance error: %v", err)
	}
}

func TestFakeInstanceServer_ContextMethods(t *testing.T) {
	fake := lxd.NewFakeInstanceServer()
	ctx := context.Background()

	if err := fake.CreateInstanceContext(ctx, api.InstancesPost{Name: "c1"}); err != nil {
		t.Fatalf("CreateInstanceContext failed: %v", err)
	}
	if err := fake.UpdateInstanceContext(ctx, "c1", api.InstancePut{}, ""); err != nil {
		t.Fatalf("UpdateInstanceContext failed: %v", err)
	}
	if err := fake.UpdateInstanceStateContext(ctx, "c1", "start", false); err != nil {
		t.Fatalf("UpdateInstanceStateContext failed: %v", err)
	}
	if _, err := fake.ExecInstanceContext(ctx, "c1", []string{"echo"}, 0, nil); err != nil {
		t.Fatalf("ExecInstanceContext failed: %v", err)
	}
	if err := fake.CreateInstanceSnapshotContext(ctx, "c1", api.InstanceSnapshotsPost{Name: "s1"}); err != nil {
		t.Fatalf("CreateInstanceSnapshotContext failed: %v", err)
	}
	if err := fake.RestoreInstanceSnapshotContext(ctx, "c1", "s1"); err != nil {
		t.Fatalf("RestoreInstanceSnapshotContext failed: %v", err)
	}
	if err := fake.DeleteInstanceSnapshotContext(ctx, "c1", "s1"); err != nil {
		t.Fatalf("DeleteInstanceSnapshotContext failed: %v", err)
	}
	// Running instances must be stopped before delete, matching real LXD.
	if err := fake.UpdateInstanceStateContext(ctx, "c1", "stop", false); err != nil {
		t.Fatalf("UpdateInstanceStateContext stop failed: %v", err)
	}
	if err := fake.DeleteInstanceContext(ctx, "c1"); err != nil {
		t.Fatalf("DeleteInstanceContext failed: %v", err)
	}
}

func TestClassifyLXDError_StatusError(t *testing.T) {
	fake := lxd.NewFakeInstanceServer()

	// 1. Error string "not found" + lookup -> exit code 5 (TARGET_NOT_FOUND)
	code, retryable := fake.ClassifyLXDError(errors.New("container not found"), "lookup")
	if code != 5 || retryable {
		t.Errorf("expected code 5, retryable false for 404 lookup, got code %d, retryable %v", code, retryable)
	}

	// 2. Error string "not found" + check -> exit code 0 (create signal)
	code, retryable = fake.ClassifyLXDError(errors.New("container not found"), "check")
	if code != 0 || retryable {
		t.Errorf("expected code 0, retryable false for 404 check, got code %d, retryable %v", code, retryable)
	}

	// 3. Error string "etag mismatch" -> exit code 4, retryable true
	code, retryable = fake.ClassifyLXDError(errors.New("etag mismatch"), "update")
	if code != 4 || !retryable {
		t.Errorf("expected code 4, retryable true for 412, got code %d, retryable %v", code, retryable)
	}

	// 4. General error -> exit code 4, retryable false
	code, retryable = fake.ClassifyLXDError(errors.New("internal server error 500"), "update")
	if code != 4 || retryable {
		t.Errorf("expected code 4, retryable false for 500, got code %d, retryable %v", code, retryable)
	}
}

func TestFakeInstanceServer_ResolveUID(t *testing.T) {
	fake := lxd.NewFakeInstanceServer()
	uid, err := fake.ResolveUID("c1", "root")
	if err != nil || uid != 0 {
		t.Errorf("expected root UID 0, got %d (err: %v)", uid, err)
	}

	uid, err = fake.ResolveUID("c1", "ubuntu")
	if err != nil || uid != 1000 {
		t.Errorf("expected ubuntu UID 1000, got %d (err: %v)", uid, err)
	}
}
