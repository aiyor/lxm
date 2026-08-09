package apply_test

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/aiyor/lxm/internal/apply"
	"github.com/aiyor/lxm/internal/config"
	"github.com/aiyor/lxm/internal/lxd"
	"github.com/aiyor/lxm/internal/plan"
	"github.com/canonical/lxd/shared/api"
)

func TestMain(m *testing.M) {
	tmpDir, err := os.MkdirTemp("", "lxm_apply_test_*")
	if err == nil {
		os.Setenv("LXM_KNOWN_HOSTS_FILE", filepath.Join(tmpDir, "known_hosts"))
		defer os.RemoveAll(tmpDir)
	}
	os.Exit(m.Run())
}

func TestExecutor_DryRun(t *testing.T) {
	fake := lxd.NewFakeInstanceServer()
	exec := apply.NewExecutor(fake)

	p := &plan.Plan{
		Schema: "lxm/plan/v1",
		Steps: []plan.Step{
			{Container: "box1", Action: "create", Changed: true},
		},
	}

	report, err := exec.Apply(context.Background(), p, apply.ApplyOpts{DryRun: true})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if report.ExitCode != 0 {
		t.Errorf("expected exit code 0, got %d", report.ExitCode)
	}
	if len(fake.Instances) != 0 {
		t.Errorf("expected 0 instances created in dry-run mode")
	}
}

func TestExecutor_SingleFilePrune_Fails(t *testing.T) {
	fake := lxd.NewFakeInstanceServer()
	exec := apply.NewExecutor(fake)

	p := &plan.Plan{Schema: "lxm/plan/v1"}
	report, err := exec.Apply(context.Background(), p, apply.ApplyOpts{
		IsSingleFile: true,
		Prune:        true,
	})
	if err == nil {
		t.Fatalf("expected error for single file prune")
	}
	if report.ExitCode != 2 {
		t.Errorf("expected exit code 2 (USAGE_ERROR), got %d", report.ExitCode)
	}
}

func TestExecutor_ETagMismatch_Fails(t *testing.T) {
	fake := lxd.NewFakeInstanceServer()
	_ = fake.CreateInstance(api.InstancesPost{
		Name: "box1",
	})
	fake.ETags["box1"] = "etag-new"

	exec := apply.NewExecutor(fake)

	p := &plan.Plan{
		Schema: "lxm/plan/v1",
		Steps: []plan.Step{
			{Container: "box1", Action: "update", Changed: true, ETag: "etag-old"},
		},
	}

	report, _ := exec.Apply(context.Background(), p, apply.ApplyOpts{})
	if report.ExitCode != 4 {
		t.Errorf("expected exit code 4 for ETag mismatch, got %d", report.ExitCode)
	}
	if len(report.Errors) == 0 || !report.Errors[0].Retryable {
		t.Errorf("expected retryable error for ETag mismatch")
	}
}

// TestExecutor_RealLXD412PUT_Retryable covers UG5 B1: when the LXD daemon
// answers an update PUT with its real 412 message ("ETag does not match:
// <old> vs <new>. The configuration has been modified since this change
// began. ..."), the executor must classify the error retryable so
// re-plan/re-apply pipelines can detect the drift. Regression for the
// classifier string heuristic that only matched the synthetic "etag
// mismatch" text.
func TestExecutor_RealLXD412PUT_Retryable(t *testing.T) {
	fake := lxd.NewFakeInstanceServer()
	_ = fake.CreateInstance(api.InstancesPost{
		Name: "box1",
	})
	fake.UpdateInstanceFunc = func(name string, put api.InstancePut, etag string) error {
		return fmt.Errorf("ETag does not match: stale-etag vs fresh-etag. The configuration has been modified since this change began. Please retrieve the updated configuration before proceeding.")
	}

	exec := apply.NewExecutor(fake)

	p := &plan.Plan{
		Schema: "lxm/plan/v1",
		Steps: []plan.Step{
			{Container: "box1", Action: "update", Changed: true, ETag: "fake-etag-created"},
		},
	}

	report, _ := exec.Apply(context.Background(), p, apply.ApplyOpts{})
	if report.ExitCode != 4 {
		t.Errorf("expected exit code 4 for LXD 412, got %d", report.ExitCode)
	}
	if len(report.Errors) == 0 {
		t.Fatalf("expected an error entry")
	}
	if !report.Errors[0].Retryable {
		t.Errorf("expected retryable error for real LXD 412 message, got %+v", report.Errors[0])
	}
}

func TestExecutor_RebuildSnapshotGate(t *testing.T) {
	fake := lxd.NewFakeInstanceServer()
	_ = fake.CreateInstance(api.InstancesPost{
		Name: "box1",
	})

	exec := apply.NewExecutor(fake)

	p := &plan.Plan{
		Schema: "lxm/plan/v1",
		Steps: []plan.Step{
			{Container: "box1", Action: "recreate", Changed: true, PurgeSnapshots: true},
		},
	}

	// Without --force gate -> fails
	report, _ := exec.Apply(context.Background(), p, apply.ApplyOpts{Force: false})
	if report.ExitCode != 3 {
		t.Errorf("expected exit code 3 (CONFIG_ERROR) when rebuilding snapshot-bearing instance without --force, got %d", report.ExitCode)
	}

	// With --force gate -> passes
	report, _ = exec.Apply(context.Background(), p, apply.ApplyOpts{Force: true})
	if report.ExitCode != 0 {
		t.Errorf("expected exit code 0 with --force gate, got %d", report.ExitCode)
	}
}

func TestExecutor_RecreateFallbackGate(t *testing.T) {
	fake := lxd.NewFakeInstanceServer()
	_ = fake.CreateInstance(api.InstancesPost{
		Name: "box1",
	})

	exec := apply.NewExecutor(fake)

	p := &plan.Plan{
		Schema: "lxm/plan/v1",
		Steps: []plan.Step{
			{Container: "box1", Action: "recreate", Changed: true, RebuildFallback: true},
		},
	}

	// Without --force -> fails
	report, _ := exec.Apply(context.Background(), p, apply.ApplyOpts{Force: false})
	if report.ExitCode != 3 {
		t.Errorf("expected exit code 3 (CONFIG_ERROR) for rebuild fallback without --force, got %d", report.ExitCode)
	}

	// With --force -> passes
	report, _ = exec.Apply(context.Background(), p, apply.ApplyOpts{Force: true})
	if report.ExitCode != 0 {
		t.Errorf("expected exit code 0 for rebuild fallback with --force, got %d", report.ExitCode)
	}
}

func TestExecutor_Actions_CreateUpdateDeleteStartStop(t *testing.T) {
	fake := lxd.NewFakeInstanceServer()
	exec := apply.NewExecutor(fake)

	// Create
	pCreate := &plan.Plan{
		Steps: []plan.Step{{Container: "box1", Action: "create", Changed: true}},
	}
	rep, _ := exec.Apply(context.Background(), pCreate, apply.ApplyOpts{})
	if rep.ExitCode != 0 || len(fake.Instances) != 1 {
		t.Fatalf("Create step failed")
	}

	// Update
	pUpdate := &plan.Plan{
		Steps: []plan.Step{{Container: "box1", Action: "update", Changed: true}},
	}
	rep, _ = exec.Apply(context.Background(), pUpdate, apply.ApplyOpts{})
	if rep.ExitCode != 0 {
		t.Fatalf("Update step failed")
	}

	// Start
	pStart := &plan.Plan{
		Steps: []plan.Step{{Container: "box1", Action: "start", Changed: true}},
	}
	rep, _ = exec.Apply(context.Background(), pStart, apply.ApplyOpts{})
	if rep.ExitCode != 0 || fake.Instances["box1"].Status != "Running" {
		t.Fatalf("Start step failed")
	}

	// Stop
	pStop := &plan.Plan{
		Steps: []plan.Step{{Container: "box1", Action: "stop", Changed: true}},
	}
	rep, _ = exec.Apply(context.Background(), pStop, apply.ApplyOpts{})
	if rep.ExitCode != 0 || fake.Instances["box1"].Status != "Stopped" {
		t.Fatalf("Stop step failed")
	}

	// Delete
	pDelete := &plan.Plan{
		Steps: []plan.Step{{Container: "box1", Action: "delete", Changed: true}},
	}
	rep, _ = exec.Apply(context.Background(), pDelete, apply.ApplyOpts{})
	if rep.ExitCode != 0 || len(fake.Instances) != 0 {
		t.Fatalf("Delete step failed")
	}
}

func TestExecutor_DeleteRunningContainer_StopsFirst(t *testing.T) {
	fake := lxd.NewFakeInstanceServer()
	_ = fake.CreateInstance(api.InstancesPost{Name: "box-running"})
	if err := fake.UpdateInstanceState("box-running", "start", false); err != nil {
		t.Fatalf("start failed: %v", err)
	}
	if fake.Instances["box-running"].Status != "Running" {
		t.Fatalf("expected box-running to be Running")
	}

	exec := apply.NewExecutor(fake)

	// Deleting a running container must stop it first — real LXD refuses a
	// non-forced delete of a running instance ("Instance is running").
	pDelete := &plan.Plan{
		Steps: []plan.Step{{Container: "box-running", Action: "delete", Changed: true}},
	}
	rep, _ := exec.Apply(context.Background(), pDelete, apply.ApplyOpts{})
	if rep.ExitCode != 0 {
		t.Fatalf("delete of running container failed (expected stop-then-delete), exit code %d", rep.ExitCode)
	}
	if len(fake.Instances) != 0 {
		t.Errorf("expected container to be deleted, got %d instance(s)", len(fake.Instances))
	}
}

func TestExecutor_DeleteStoppedContainer_SkipsStop(t *testing.T) {
	fake := lxd.NewFakeInstanceServer()
	_ = fake.CreateInstance(api.InstancesPost{Name: "box-stopped"})
	if fake.Instances["box-stopped"].StatusCode != api.Stopped {
		t.Fatalf("expected box-stopped to be Stopped by default")
	}

	// Spy: a stop attempt on an already-stopped instance must not happen.
	// Real LXD rejects it ("The instance is already stopped"), which would
	// break `status: absent` / `--prune` for stopped containers.
	stopCalled := false
	fake.UpdateInstanceStateFunc = func(name, action string, force bool) error {
		stopCalled = true
		return nil
	}

	exec := apply.NewExecutor(fake)
	pDelete := &plan.Plan{
		Steps: []plan.Step{{Container: "box-stopped", Action: "delete", Changed: true}},
	}
	rep, _ := exec.Apply(context.Background(), pDelete, apply.ApplyOpts{})
	if rep.ExitCode != 0 {
		t.Fatalf("delete of stopped container failed, exit code %d: %s", rep.ExitCode, rep.Results[0].Error)
	}
	if stopCalled {
		t.Errorf("expected no stop call for an already-stopped container")
	}
	if len(fake.Instances) != 0 {
		t.Errorf("expected container to be deleted, got %d instance(s)", len(fake.Instances))
	}
}

func TestExecutor_RecreateFallback_StoppedContainer(t *testing.T) {
	fake := lxd.NewFakeInstanceServer()
	_ = fake.CreateInstance(api.InstancesPost{Name: "box-recreate"})
	if fake.Instances["box-recreate"].StatusCode != api.Stopped {
		t.Fatalf("expected box-recreate to be Stopped by default")
	}

	exec := apply.NewExecutor(fake)
	p := &plan.Plan{
		Steps: []plan.Step{{
			Container:       "box-recreate",
			Action:          "recreate",
			Changed:         true,
			RebuildFallback: true,
		}},
	}
	// Recreate of a stopped container must not attempt a stop (LXD rejects
	// stopping an already-stopped instance).
	rep, _ := exec.Apply(context.Background(), p, apply.ApplyOpts{Force: true})
	if rep.ExitCode != 0 {
		t.Fatalf("recreate fallback of stopped container failed, exit code %d: %s", rep.ExitCode, rep.Results[0].Error)
	}
	if _, ok := fake.Instances["box-recreate"]; !ok {
		t.Errorf("expected recreated container to exist after fallback")
	}
}

func TestExecutor_RecreateFallback_RunningContainer(t *testing.T) {
	fake := lxd.NewFakeInstanceServer()
	_ = fake.CreateInstance(api.InstancesPost{Name: "box-recreate-running"})
	if err := fake.UpdateInstanceState("box-recreate-running", "start", false); err != nil {
		t.Fatalf("start failed: %v", err)
	}

	stopCalls := 0
	fake.UpdateInstanceStateFunc = func(name, action string, force bool) error {
		if action == "stop" {
			stopCalls++
			// Mirror the fake's default stop transition so the subsequent
			// delete sees a stopped instance.
			inst := fake.Instances[name]
			inst.Status = "Stopped"
			inst.StatusCode = api.Stopped
		}
		return nil
	}

	exec := apply.NewExecutor(fake)
	p := &plan.Plan{
		Steps: []plan.Step{{
			Container:       "box-recreate-running",
			Action:          "recreate",
			Changed:         true,
			RebuildFallback: true,
		}},
	}
	// A running container must be stopped exactly once before the
	// delete+create fallback (real LXD refuses a non-forced delete of a
	// running instance).
	rep, _ := exec.Apply(context.Background(), p, apply.ApplyOpts{Force: true})
	if rep.ExitCode != 0 {
		t.Fatalf("recreate fallback of running container failed, exit code %d: %s", rep.ExitCode, rep.Results[0].Error)
	}
	if stopCalls != 1 {
		t.Errorf("expected exactly 1 stop call for a running container, got %d", stopCalls)
	}
	if _, ok := fake.Instances["box-recreate-running"]; !ok {
		t.Errorf("expected recreated container to exist after fallback")
	}
}

func TestExecutor_DeleteMissingContainer_IsNoop(t *testing.T) {
	fake := lxd.NewFakeInstanceServer()
	exec := apply.NewExecutor(fake)
	pDelete := &plan.Plan{
		Steps: []plan.Step{{Container: "never-existed", Action: "delete", Changed: true}},
	}
	// A container that vanished between plan and apply is already absent:
	// the delete step must not fail on "not found" (idempotent delete).
	rep, _ := exec.Apply(context.Background(), pDelete, apply.ApplyOpts{})
	if rep.ExitCode != 0 {
		t.Fatalf("delete of missing container should be a no-op, exit code %d", rep.ExitCode)
	}
}

func TestExecutor_ErrorPrecedence(t *testing.T) {
	fake := lxd.NewFakeInstanceServer()
	fake.CreateInstanceFunc = func(req api.InstancesPost) error {
		return errors.New("lxd api failure")
	}

	exec := apply.NewExecutor(fake)

	p := &plan.Plan{
		Steps: []plan.Step{{Container: "box1", Action: "create"}},
	}
	rep, _ := exec.Apply(context.Background(), p, apply.ApplyOpts{})
	if rep.ExitCode != 4 {
		t.Errorf("expected exit code 4 (LXD_ERROR) on LXD operation error, got %d", rep.ExitCode)
	}
}

func TestExecutor_CreateStartsContainer_Idempotent(t *testing.T) {
	fake := lxd.NewFakeInstanceServer()
	exec := apply.NewExecutor(fake)

	pCreate := &plan.Plan{
		Steps: []plan.Step{{Container: "box1", Action: "create", Changed: true, PowerTransition: "start"}},
	}

	rep, err := exec.Apply(context.Background(), pCreate, apply.ApplyOpts{})
	if err != nil || rep.ExitCode != 0 {
		t.Fatalf("Create step failed: %v", err)
	}

	if fake.Instances["box1"].Status != "Running" {
		t.Errorf("expected container box1 to be Running after create with PowerTransition=start, got %s", fake.Instances["box1"].Status)
	}
}

func TestExecutor_CreateStoppedContainer_RemainsStopped_Idempotent(t *testing.T) {
	fake := lxd.NewFakeInstanceServer()
	exec := apply.NewExecutor(fake)

	pCreate := &plan.Plan{
		Steps: []plan.Step{{Container: "box2", Action: "create", Changed: true, PowerTransition: "stop"}},
	}

	rep, err := exec.Apply(context.Background(), pCreate, apply.ApplyOpts{})
	if err != nil || rep.ExitCode != 0 {
		t.Fatalf("Create step failed: %v", err)
	}

	if fake.Instances["box2"].Status != "Stopped" {
		t.Errorf("expected container box2 to be Stopped after create with PowerTransition=stop, got %s", fake.Instances["box2"].Status)
	}
}

func TestExecutor_RecreateFallback_StoppedContainer_RemainsStopped(t *testing.T) {
	fake := lxd.NewFakeInstanceServer()
	_ = fake.CreateInstance(api.InstancesPost{Name: "box3"})

	exec := apply.NewExecutor(fake)

	pRecreate := &plan.Plan{
		Steps: []plan.Step{
			{Container: "box3", Action: "recreate", Changed: true, RebuildFallback: true, PowerTransition: "stop"},
		},
	}

	rep, err := exec.Apply(context.Background(), pRecreate, apply.ApplyOpts{Force: true})
	if err != nil || rep.ExitCode != 0 {
		t.Fatalf("Recreate fallback step failed: %v", err)
	}

	if fake.Instances["box3"].Status != "Stopped" {
		t.Errorf("expected container box3 to be Stopped after recreate fallback with PowerTransition=stop, got %s", fake.Instances["box3"].Status)
	}
}

func TestExecutor_RebuildNative_Passes(t *testing.T) {
	fake := lxd.NewFakeInstanceServer()
	_ = fake.CreateInstance(api.InstancesPost{Name: "box4"})

	exec := apply.NewExecutor(fake)

	pRecreate := &plan.Plan{
		Steps: []plan.Step{
			{Container: "box4", Action: "recreate", Changed: true, RebuildFallback: false},
		},
	}

	rep, err := exec.Apply(context.Background(), pRecreate, apply.ApplyOpts{})
	if err != nil || rep.ExitCode != 0 {
		t.Fatalf("Native rebuild step failed: %v", err)
	}
}

func TestReconcilerToExecutor_Integration_PowerTransitions(t *testing.T) {
	fake := lxd.NewFakeInstanceServer()
	_ = fake.CreateInstance(api.InstancesPost{
		Name: "box5",
		InstancePut: api.InstancePut{
			Config: map[string]string{
				"user.lxm.user": "ubuntu",
			},
		},
	})
	fake.Instances["box5"].Status = "Running"
	fake.Instances["box5"].StatusCode = api.Running

	rec := plan.NewReconciler()
	exec := apply.NewExecutor(fake)

	conf := &config.Config{
		Name:  "box5",
		User:  "root",
		State: "stopped",
		Image: "ubuntu:22.04",
	}

	live := map[string]*plan.InstanceSnapshot{
		"box5": {
			Name:   "box5",
			Status: "Running",
			Config: map[string]string{
				"user.lxm.user": "ubuntu",
			},
		},
	}

	p, err := rec.Compute(conf, live, false)
	if err != nil {
		t.Fatalf("Compute error: %v", err)
	}

	if p.Steps[0].PowerTransition != "stop" {
		t.Fatalf("expected PowerTransition 'stop', got %q", p.Steps[0].PowerTransition)
	}

	rep, err := exec.Apply(context.Background(), p, apply.ApplyOpts{})
	if err != nil || rep.ExitCode != 0 {
		t.Fatalf("Apply error: %v", err)
	}

	if fake.Instances["box5"].Status != "Stopped" {
		t.Errorf("expected container box5 to be Stopped after update with state: stopped, got %s", fake.Instances["box5"].Status)
	}
}

func TestExecutor_WaitPolicyTimeout(t *testing.T) {
	fake := lxd.NewFakeInstanceServer()
	_ = fake.CreateInstance(api.InstancesPost{Name: "waitbox"})
	exec := apply.NewExecutor(fake)

	p := &plan.Plan{
		Steps: []plan.Step{
			{
				Container: "waitbox",
				Action:    "start",
				Changed:   true,
				WaitPolicy: &config.WaitConfig{
					CloudInit: "timeout",
					Required:  true,
				},
			},
		},
	}

	rep, _ := exec.Apply(context.Background(), p, apply.ApplyOpts{})
	if rep.ExitCode != 7 {
		t.Errorf("expected exit code 7 (WAIT_TIMEOUT), got %d", rep.ExitCode)
	}
}

func TestExecutor_RecipeExecutionAndSnapshot(t *testing.T) {
	tmpDir := t.TempDir()
	shFile := "setup.sh"
	if err := writeTestScript(tmpDir, shFile, "#!/bin/bash\necho hello"); err != nil {
		t.Fatalf("writing script: %v", err)
	}

	fake := lxd.NewFakeInstanceServer()
	_ = fake.CreateInstance(api.InstancesPost{Name: "recipebox"})
	fake.Instances["recipebox"].Status = "Running"

	exec := apply.NewExecutor(fake)

	p := &plan.Plan{
		Steps: []plan.Step{
			{
				Container:     "recipebox",
				Action:        "noop",
				Changed:       false,
				ConfigBaseDir: tmpDir,
				Recipes: []plan.RecipeStep{
					{Path: shFile, RunAs: "root"},
				},
			},
		},
	}

	rep, err := exec.Apply(context.Background(), p, apply.ApplyOpts{})
	if err != nil || rep.ExitCode != 0 {
		t.Fatalf("recipe execution failed: %v", err)
	}

	// Verify snapshot-before-recipe created
	snaps, err := fake.GetInstanceSnapshots("recipebox")
	if err != nil || len(snaps) == 0 {
		t.Errorf("expected snapshot created before recipe execution, got %d snaps", len(snaps))
	}

	// Verify recipe hash metadata key set
	inst, _, _ := fake.GetInstance("recipebox")
	if inst.Config["user.lxm.recipe.setup_sh.hash"] == "" {
		t.Errorf("expected path-qualified recipe hash key stored in instance metadata")
	}
}

func TestExecutor_ContextCancellation(t *testing.T) {
	fake := lxd.NewFakeInstanceServer()
	_ = fake.CreateInstance(api.InstancesPost{Name: "cancelbox"})

	exec := apply.NewExecutor(fake)
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // Cancel immediately

	p := &plan.Plan{
		Steps: []plan.Step{
			{
				Container: "cancelbox",
				Action:    "noop",
				Recipes: []plan.RecipeStep{
					{Path: "setup.sh", RunAs: "root"},
				},
			},
		},
	}

	rep, _ := exec.Apply(ctx, p, apply.ApplyOpts{})
	if rep.ExitCode == 0 {
		t.Errorf("expected non-zero exit code on context cancellation")
	}
}

func writeTestScript(dir, filename, content string) error {
	path := dir + "/" + filename
	return os.WriteFile(path, []byte(content), 0755)
}

func TestExecutor_DryRun_NoRecipesExecuted(t *testing.T) {
	tmpDir := t.TempDir()
	if err := writeTestScript(tmpDir, "setup.sh", "#!/bin/bash\necho hello"); err != nil {
		t.Fatalf("writing setup.sh: %v", err)
	}

	fake := lxd.NewFakeInstanceServer()
	_ = fake.CreateInstance(api.InstancesPost{Name: "drybox"})

	execCount := 0
	fake.ExecInstanceFunc = func(name string, cmd []string, uid uint32, env map[string]string) (lxd.ExecResult, error) {
		execCount++
		return lxd.ExecResult{ExitCode: 0, Stdout: "ok"}, nil
	}

	exec := apply.NewExecutor(fake)
	ctx := context.Background()

	p := &plan.Plan{
		Steps: []plan.Step{
			{
				Container:     "drybox",
				Action:        "noop",
				ConfigBaseDir: tmpDir,
				Recipes: []plan.RecipeStep{
					{Path: "setup.sh", RunAs: "root"},
				},
			},
		},
	}

	rep, err := exec.Apply(ctx, p, apply.ApplyOpts{DryRun: true})
	if err != nil {
		t.Fatalf("unexpected error on dry run: %v", err)
	}
	if rep.ExitCode != 0 {
		t.Errorf("expected exit code 0 for dry run, got %d", rep.ExitCode)
	}

	if execCount > 0 {
		t.Errorf("expected ZERO script executions under --dry-run, got %d", execCount)
	}

	snaps, _ := fake.GetInstanceSnapshots("drybox")
	if len(snaps) > 0 {
		t.Errorf("expected ZERO snapshots created under --dry-run, got %d", len(snaps))
	}
}

func TestExecutor_NilPlan(t *testing.T) {
	fake := lxd.NewFakeInstanceServer()
	exec := apply.NewExecutor(fake)
	_, err := exec.Apply(context.Background(), nil, apply.ApplyOpts{})
	if err == nil {
		t.Errorf("expected error when applying nil plan")
	}
}

func TestExecutor_SingleFilePruneRestriction(t *testing.T) {
	fake := lxd.NewFakeInstanceServer()
	exec := apply.NewExecutor(fake)
	p := &plan.Plan{Steps: []plan.Step{}}
	rep, err := exec.Apply(context.Background(), p, apply.ApplyOpts{IsSingleFile: true, Prune: true})
	if err == nil || rep.ExitCode != 2 {
		t.Errorf("expected exit code 2 when pruning with single file target")
	}
}

func TestExecutor_WaitPolicy_StopStepNonWaiting(t *testing.T) {
	fake := lxd.NewFakeInstanceServer()
	_ = fake.CreateInstance(api.InstancesPost{Name: "stopbox"})
	fake.Instances["stopbox"].Status = "Running"
	fake.Instances["stopbox"].StatusCode = api.Running

	fake.ExecInstanceFunc = func(name string, cmd []string, uid uint32, env map[string]string) (lxd.ExecResult, error) {
		return lxd.ExecResult{ExitCode: 1, Stdout: "", Stderr: "container is stopped"}, nil
	}

	exec := apply.NewExecutor(fake)
	p := &plan.Plan{
		Steps: []plan.Step{
			{
				Container: "stopbox",
				Action:    "stop",
				WaitPolicy: &config.WaitConfig{
					CloudInit: "10s",
					Required:  true,
				},
			},
		},
	}

	rep, err := exec.Apply(context.Background(), p, apply.ApplyOpts{})
	if err != nil {
		t.Fatalf("unexpected apply error: %v", err)
	}
	if rep.ExitCode != 0 {
		t.Errorf("expected exit code 0 for stop step (should skip wait), got %d", rep.ExitCode)
	}
}

func TestExecutor_WaitPolicy_NonCloudInitImage(t *testing.T) {
	fake := lxd.NewFakeInstanceServer()
	_ = fake.CreateInstance(api.InstancesPost{Name: "alpinebox"})
	fake.Instances["alpinebox"].Status = "Running"
	fake.Instances["alpinebox"].StatusCode = api.Running

	fake.ExecInstanceFunc = func(name string, cmd []string, uid uint32, env map[string]string) (lxd.ExecResult, error) {
		return lxd.ExecResult{ExitCode: 127, Stdout: "", Stderr: "cloud-init: command not found"}, nil
	}

	exec := apply.NewExecutor(fake)
	p := &plan.Plan{
		Steps: []plan.Step{
			{
				Container: "alpinebox",
				Action:    "noop",
				WaitPolicy: &config.WaitConfig{
					CloudInit: "10s",
					Required:  true,
				},
			},
		},
	}

	rep, err := exec.Apply(context.Background(), p, apply.ApplyOpts{})
	if err != nil {
		t.Fatalf("unexpected apply error: %v", err)
	}
	if rep.ExitCode != 0 {
		t.Errorf("expected exit code 0 for non-cloud-init image (command 127), got %d", rep.ExitCode)
	}
	if len(rep.Warnings) == 0 {
		t.Errorf("expected warning for non-cloud-init container, got empty warnings")
	}
}

func TestExecutor_WaitPolicy_SoftWaitWarning(t *testing.T) {
	fake := lxd.NewFakeInstanceServer()
	_ = fake.CreateInstance(api.InstancesPost{Name: "softbox"})
	fake.Instances["softbox"].Status = "Running"
	fake.Instances["softbox"].StatusCode = api.Running

	exec := apply.NewExecutor(fake)
	p := &plan.Plan{
		Steps: []plan.Step{
			{
				Container: "softbox",
				Action:    "noop",
				WaitPolicy: &config.WaitConfig{
					CloudInit: "timeout",
					Required:  false,
				},
			},
		},
	}

	rep, err := exec.Apply(context.Background(), p, apply.ApplyOpts{})
	if err != nil {
		t.Fatalf("unexpected apply error: %v", err)
	}
	if rep.ExitCode != 0 {
		t.Errorf("expected exit code 0 for soft wait timeout, got %d", rep.ExitCode)
	}
	if len(rep.Warnings) == 0 {
		t.Errorf("expected warning recorded for soft wait timeout")
	}
}

func TestExecutor_WaitPolicy_ContextCancelledMidWait(t *testing.T) {
	fake := lxd.NewFakeInstanceServer()
	_ = fake.CreateInstance(api.InstancesPost{Name: "cancelbox"})
	fake.Instances["cancelbox"].Status = "Running"
	fake.Instances["cancelbox"].StatusCode = api.Running

	blockChan := make(chan struct{})
	fake.ExecInstanceFunc = func(name string, cmd []string, uid uint32, env map[string]string) (lxd.ExecResult, error) {
		<-blockChan
		return lxd.ExecResult{ExitCode: 0}, nil
	}

	exec := apply.NewExecutor(fake)
	p := &plan.Plan{
		Steps: []plan.Step{
			{
				Container: "cancelbox",
				Action:    "noop",
				Recipes:   []plan.RecipeStep{{Path: "dummy.sh"}},
				WaitPolicy: &config.WaitConfig{
					CloudInit: "10s",
					Required:  true,
				},
			},
		},
	}

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(20 * time.Millisecond)
		cancel()
		close(blockChan)
	}()

	rep, _ := exec.Apply(ctx, p, apply.ApplyOpts{})
	if rep.ExitCode != 1 {
		t.Errorf("expected exit code 1 (INTERNAL_ERROR) when cancelled mid-wait, got %d", rep.ExitCode)
	}
}

func TestExecutor_WaitPolicy_TransportError(t *testing.T) {
	fake := lxd.NewFakeInstanceServer()
	_ = fake.CreateInstance(api.InstancesPost{Name: "transportbox"})
	fake.Instances["transportbox"].Status = "Running"
	fake.Instances["transportbox"].StatusCode = api.Running

	fake.ExecInstanceFunc = func(name string, cmd []string, uid uint32, env map[string]string) (lxd.ExecResult, error) {
		return lxd.ExecResult{}, fmt.Errorf("websocket transport error")
	}

	exec := apply.NewExecutor(fake)
	p := &plan.Plan{
		Steps: []plan.Step{
			{
				Container: "transportbox",
				Action:    "noop",
				Recipes:   []plan.RecipeStep{{Path: "dummy.sh"}},
				WaitPolicy: &config.WaitConfig{
					CloudInit: "10s",
					Required:  true,
				},
			},
		},
	}

	rep, _ := exec.Apply(context.Background(), p, apply.ApplyOpts{})
	if rep.ExitCode != 4 {
		t.Errorf("expected exit code 4 (LXD_ERROR) for transport error, got %d", rep.ExitCode)
	}
}

func TestExecutor_WaitPolicy_NetworkSuccess(t *testing.T) {
	fake := lxd.NewFakeInstanceServer()
	_ = fake.CreateInstance(api.InstancesPost{Name: "netbox"})
	fake.Instances["netbox"].Status = "Running"
	fake.Instances["netbox"].StatusCode = api.Running

	fake.ExecInstanceFunc = func(name string, cmd []string, uid uint32, env map[string]string) (lxd.ExecResult, error) {
		return lxd.ExecResult{ExitCode: 0, Stdout: "10.0.0.15\n"}, nil
	}

	exec := apply.NewExecutor(fake)
	p := &plan.Plan{
		Steps: []plan.Step{
			{
				Container: "netbox",
				Action:    "noop",
				WaitPolicy: &config.WaitConfig{
					Network:  "500ms",
					Required: true,
				},
			},
		},
	}

	rep, _ := exec.Apply(context.Background(), p, apply.ApplyOpts{})
	if rep.ExitCode != 0 {
		t.Errorf("expected exit code 0 on network readiness success, got %d", rep.ExitCode)
	}
}

func TestExecutor_WaitPolicy_NetworkTimeout(t *testing.T) {
	fake := lxd.NewFakeInstanceServer()
	_ = fake.CreateInstance(api.InstancesPost{Name: "netfailbox"})
	fake.Instances["netfailbox"].Status = "Running"
	fake.Instances["netfailbox"].StatusCode = api.Running

	fake.ExecInstanceFunc = func(name string, cmd []string, uid uint32, env map[string]string) (lxd.ExecResult, error) {
		return lxd.ExecResult{ExitCode: 1, Stdout: ""}, nil
	}

	exec := apply.NewExecutor(fake)
	p := &plan.Plan{
		Steps: []plan.Step{
			{
				Container: "netfailbox",
				Action:    "noop",
				WaitPolicy: &config.WaitConfig{
					Network:  "200ms",
					Required: true,
				},
			},
		},
	}

	rep, _ := exec.Apply(context.Background(), p, apply.ApplyOpts{})
	if rep.ExitCode != 7 {
		t.Errorf("expected exit code 7 (WAIT_TIMEOUT) on network wait timeout, got %d", rep.ExitCode)
	}
}

func TestExecutor_WaitPolicy_NonHostnameImage(t *testing.T) {
	fake := lxd.NewFakeInstanceServer()
	_ = fake.CreateInstance(api.InstancesPost{Name: "nohostnamebox"})
	fake.Instances["nohostnamebox"].Status = "Running"
	fake.Instances["nohostnamebox"].StatusCode = api.Running

	fake.ExecInstanceFunc = func(name string, cmd []string, uid uint32, env map[string]string) (lxd.ExecResult, error) {
		return lxd.ExecResult{ExitCode: 127, Stderr: "executable file not found"}, nil
	}

	exec := apply.NewExecutor(fake)
	p := &plan.Plan{
		Steps: []plan.Step{
			{
				Container: "nohostnamebox",
				Action:    "noop",
				WaitPolicy: &config.WaitConfig{
					Network:  "10s",
					Required: true,
				},
			},
		},
	}

	rep, _ := exec.Apply(context.Background(), p, apply.ApplyOpts{})
	if rep.ExitCode != 0 {
		t.Errorf("expected exit code 0 when hostname binary is missing, got %d", rep.ExitCode)
	}
	if len(rep.Warnings) == 0 {
		t.Errorf("expected warning for missing hostname binary, got none")
	}
}

func TestExecutor_InterruptDuringOperation_ReturnsInternalError(t *testing.T) {
	fake := lxd.NewFakeInstanceServer()
	_ = fake.CreateInstance(api.InstancesPost{Name: "opcancelbox"})
	fake.Instances["opcancelbox"].Status = "Running"
	fake.Instances["opcancelbox"].StatusCode = api.Running

	fake.UpdateInstanceFunc = func(name string, put api.InstancePut, etag string) error {
		return context.Canceled
	}

	exec := apply.NewExecutor(fake)
	p := &plan.Plan{
		Steps: []plan.Step{
			{
				Container: "opcancelbox",
				Action:    "update",
				Changed:   true,
			},
		},
	}

	rep, _ := exec.Apply(context.Background(), p, apply.ApplyOpts{})
	if rep.ExitCode != 1 {
		t.Errorf("expected exit code 1 (INTERNAL_ERROR) on operation cancellation, got %d", rep.ExitCode)
	}
}

func TestExecutor_InterruptDuringRecipeScript_ReturnsInternalError(t *testing.T) {
	fake := lxd.NewFakeInstanceServer()
	_ = fake.CreateInstance(api.InstancesPost{Name: "recipecancelbox"})
	fake.Instances["recipecancelbox"].Status = "Running"
	fake.Instances["recipecancelbox"].StatusCode = api.Running

	fake.ExecInstanceFunc = func(name string, cmd []string, uid uint32, env map[string]string) (lxd.ExecResult, error) {
		return lxd.ExecResult{ExitCode: 1}, context.Canceled
	}

	exec := apply.NewExecutor(fake)
	p := &plan.Plan{
		Steps: []plan.Step{
			{
				Container: "recipecancelbox",
				Action:    "noop",
				Recipes:   []plan.RecipeStep{{Path: "apply.go"}},
			},
		},
	}

	rep, _ := exec.Apply(context.Background(), p, apply.ApplyOpts{})
	if rep.ExitCode != 1 {
		t.Errorf("expected exit code 1 (INTERNAL_ERROR) on recipe execution cancellation, got %d", rep.ExitCode)
	}
}
