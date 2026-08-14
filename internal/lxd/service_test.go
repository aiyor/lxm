package lxd

import (
	"context"
	"fmt"
	"strings"
	"testing"

	lxd_client "github.com/canonical/lxd/client"
	"github.com/canonical/lxd/shared/api"
)

func TestFakeInstanceServer(t *testing.T) {
	fake := NewFakeInstanceServer()
	if fake == nil {
		t.Fatal("expected non-nil fake instance server")
	}

	err := fake.CreateInstance(api.InstancesPost{
		Name: "test-inst",
	})
	if err != nil {
		t.Fatalf("CreateInstance returned error: %v", err)
	}

	insts, err := fake.ListInstances()
	if err != nil {
		t.Fatalf("ListInstances returned error: %v", err)
	}
	if len(insts) != 1 {
		t.Fatalf("ListInstances returned %d instances, want 1", len(insts))
	}
	if insts[0].Name != "test-inst" {
		t.Errorf("instance name = %q, want %q", insts[0].Name, "test-inst")
	}

	// Test snapshots
	err = fake.CreateInstanceSnapshot("test-inst", api.InstanceSnapshotsPost{Name: "snap1"})
	if err != nil {
		t.Fatalf("CreateInstanceSnapshot returned error: %v", err)
	}

	snaps, err := fake.GetInstanceSnapshots("test-inst")
	if err != nil || len(snaps) != 1 {
		t.Fatalf("GetInstanceSnapshots failed: %v", err)
	}

	if err := fake.RestoreInstanceSnapshot("test-inst", "snap1"); err != nil {
		t.Fatalf("RestoreInstanceSnapshot failed: %v", err)
	}

	if err := fake.DeleteInstanceSnapshot("test-inst", "snap1"); err != nil {
		t.Fatalf("DeleteInstanceSnapshot failed: %v", err)
	}

	// Test user UID/Env resolution
	uidRoot, _ := fake.ResolveUID("test-inst", "root")
	if uidRoot != 0 {
		t.Errorf("expected root UID 0, got %d", uidRoot)
	}
	uidUbuntu, _ := fake.ResolveUID("test-inst", "ubuntu")
	if uidUbuntu != 1000 {
		t.Errorf("expected ubuntu UID 1000, got %d", uidUbuntu)
	}

	envRoot, _ := fake.ResolveUserEnv("test-inst", "root")
	if envRoot.Home != "/root" {
		t.Errorf("expected root home /root, got %s", envRoot.Home)
	}

	// Test error classification
	code, _ := fake.ClassifyLXDError(fmt.Errorf("not found"), "lookup")
	if code != 5 {
		t.Errorf("expected exit code 5 for lookup not found, got %d", code)
	}
	codeEtag, isEtag := fake.ClassifyLXDError(fmt.Errorf("etag mismatch"), "update")
	if !isEtag || codeEtag != 4 {
		t.Errorf("expected etag mismatch flag true, got %v", isEtag)
	}

	// Test context wrappers
	ctx := context.Background()
	_ = fake.UpdateInstanceContext(ctx, "test-inst", api.InstancePut{}, "etag")
	_ = fake.UpdateInstanceStateContext(ctx, "test-inst", "start", false)
	_ = fake.RebuildInstanceContext(ctx, "test-inst", api.InstanceRebuildPost{})
	_, _ = fake.ExecInstanceContext(ctx, "test-inst", []string{"ls"}, 0, nil)
	_ = fake.CreateInstanceFile("test-inst", "/tmp/file", lxd_client.InstanceFileArgs{Content: strings.NewReader("hello")})
	_ = fake.DeleteInstanceFile("test-inst", "/tmp/file")
	_, _ = fake.GetInstanceSnapshots("test-inst")
	_ = fake.CreateInstanceSnapshotContext(ctx, "test-inst", api.InstanceSnapshotsPost{Name: "snap2"})
	// Test UserEnv.DefaultEnv and ExecResult.Combined
	env := (&UserEnv{Home: "/home/ubuntu", User: "ubuntu", Shell: "/bin/bash"}).DefaultEnv()
	if env["HOME"] != "/home/ubuntu" || env["USER"] != "ubuntu" || env["SHELL"] != "/bin/bash" {
		t.Errorf("unexpected DefaultEnv: %v", env)
	}

	execRes := ExecResult{ExitCode: 0, Stdout: "hello", Stderr: "world"}
	if execRes.Combined() != "hello\nworld" {
		t.Errorf("expected combined output 'hello\\nworld', got %q", execRes.Combined())
	}
	execResStdoutOnly := ExecResult{ExitCode: 0, Stdout: "hello"}
	if execResStdoutOnly.Combined() != "hello" {
		t.Errorf("expected combined output 'hello', got %q", execResStdoutOnly.Combined())
	}
}

func TestNewService_InvalidSocket(t *testing.T) {
	t.Setenv("LXD_SOCKET", "/nonexistent/path/to/lxd/socket.sock")
	_, err := NewService()
	if err == nil {
		t.Errorf("expected error when connecting to non-existent LXD socket")
	}
}
