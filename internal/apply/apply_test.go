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
	"github.com/aiyor/lxm/internal/plan"
	"github.com/aiyor/lxm/internal/provider"
	"github.com/aiyor/lxm/internal/provider/fake"
)

func TestMain(m *testing.M) {
	tmpDir, err := os.MkdirTemp("", "lxm_apply_test_*")
	if err == nil {
		_ = os.Setenv("LXM_KNOWN_HOSTS_FILE", filepath.Join(tmpDir, "known_hosts"))
	}
	code := m.Run()
	if tmpDir != "" {
		_ = os.RemoveAll(tmpDir)
	}
	os.Exit(code)
}

func TestExecutor_DryRun(t *testing.T) {
	ctx := context.Background()
	driver := fake.New()
	exec := apply.NewExecutor(driver)

	p := &plan.Plan{
		Schema: "lxm/plan/v1",
		Steps: []plan.Step{
			{Container: "box1", Action: "create", Changed: true},
		},
	}

	report, err := exec.Apply(ctx, p, apply.ApplyOpts{DryRun: true})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if report.ExitCode != 0 {
		t.Errorf("expected exit code 0, got %d", report.ExitCode)
	}
	if len(driver.Instances) != 0 {
		t.Errorf("expected 0 instances created in dry-run mode")
	}
}

func TestExecutor_SingleFilePrune_Fails(t *testing.T) {
	ctx := context.Background()
	driver := fake.New()
	exec := apply.NewExecutor(driver)

	p := &plan.Plan{Schema: "lxm/plan/v1"}
	report, err := exec.Apply(ctx, p, apply.ApplyOpts{
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
	ctx := context.Background()
	driver := fake.New()
	_ = driver.CreateInstance(ctx, provider.InstanceCreateRequest{
		Name: "box1",
	})
	driver.ETags["box1"] = "etag-new"

	exec := apply.NewExecutor(driver)

	p := &plan.Plan{
		Schema: "lxm/plan/v1",
		Steps: []plan.Step{
			{Container: "box1", Action: "update", Changed: true, ETag: "etag-old"},
		},
	}

	report, _ := exec.Apply(ctx, p, apply.ApplyOpts{})
	if report.ExitCode != 4 {
		t.Errorf("expected exit code 4 for ETag mismatch, got %d", report.ExitCode)
	}
	if len(report.Errors) == 0 || !report.Errors[0].Retryable {
		t.Errorf("expected retryable error for ETag mismatch")
	}
}

func TestExecutor_RealLXD412PUT_Retryable(t *testing.T) {
	ctx := context.Background()
	driver := fake.New()
	_ = driver.CreateInstance(ctx, provider.InstanceCreateRequest{
		Name: "box1",
	})
	driver.UpdateInstanceFunc = func(name string, put provider.InstanceUpdateRequest, etag string) error {
		return fmt.Errorf("ETag does not match: stale-etag vs fresh-etag. The configuration has been modified since this change began. Please retrieve the updated configuration before proceeding.")
	}

	exec := apply.NewExecutor(driver)

	p := &plan.Plan{
		Schema: "lxm/plan/v1",
		Steps: []plan.Step{
			{Container: "box1", Action: "update", Changed: true, ETag: "fake-etag-created"},
		},
	}

	report, _ := exec.Apply(ctx, p, apply.ApplyOpts{})
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
	ctx := context.Background()
	driver := fake.New()
	_ = driver.CreateInstance(ctx, provider.InstanceCreateRequest{
		Name: "box1",
	})

	exec := apply.NewExecutor(driver)

	p := &plan.Plan{
		Schema: "lxm/plan/v1",
		Steps: []plan.Step{
			{Container: "box1", Action: "recreate", Changed: true, PurgeSnapshots: true},
		},
	}

	// Without --force gate -> fails
	report, _ := exec.Apply(ctx, p, apply.ApplyOpts{Force: false})
	if report.ExitCode != 3 {
		t.Errorf("expected exit code 3 (CONFIG_ERROR) when rebuilding snapshot-bearing instance without --force, got %d", report.ExitCode)
	}

	// With --force gate -> passes
	report, _ = exec.Apply(ctx, p, apply.ApplyOpts{Force: true})
	if report.ExitCode != 0 {
		t.Errorf("expected exit code 0 with --force gate, got %d", report.ExitCode)
	}
}

func TestExecutor_RecreateFallbackGate(t *testing.T) {
	ctx := context.Background()
	driver := fake.New()
	_ = driver.CreateInstance(ctx, provider.InstanceCreateRequest{
		Name: "box1",
	})

	exec := apply.NewExecutor(driver)

	p := &plan.Plan{
		Schema: "lxm/plan/v1",
		Steps: []plan.Step{
			{Container: "box1", Action: "recreate", Changed: true, RebuildFallback: true},
		},
	}

	// Without --force -> fails
	report, _ := exec.Apply(ctx, p, apply.ApplyOpts{Force: false})
	if report.ExitCode != 3 {
		t.Errorf("expected exit code 3 (CONFIG_ERROR) for rebuild fallback without --force, got %d", report.ExitCode)
	}

	// With --force -> passes
	report, _ = exec.Apply(ctx, p, apply.ApplyOpts{Force: true})
	if report.ExitCode != 0 {
		t.Errorf("expected exit code 0 for rebuild fallback with --force, got %d", report.ExitCode)
	}
}

func TestExecutor_Actions_CreateUpdateDeleteStartStop(t *testing.T) {
	ctx := context.Background()
	driver := fake.New()
	exec := apply.NewExecutor(driver)

	// Create
	pCreate := &plan.Plan{
		Steps: []plan.Step{{Container: "box1", Action: "create", Changed: true}},
	}
	rep, _ := exec.Apply(ctx, pCreate, apply.ApplyOpts{})
	if rep.ExitCode != 0 || len(driver.Instances) != 1 {
		t.Fatalf("Create step failed")
	}

	// Update
	pUpdate := &plan.Plan{
		Steps: []plan.Step{{Container: "box1", Action: "update", Changed: true}},
	}
	rep, _ = exec.Apply(ctx, pUpdate, apply.ApplyOpts{})
	if rep.ExitCode != 0 {
		t.Fatalf("Update step failed")
	}

	// Start
	pStart := &plan.Plan{
		Steps: []plan.Step{{Container: "box1", Action: "start", Changed: true}},
	}
	rep, _ = exec.Apply(ctx, pStart, apply.ApplyOpts{})
	if rep.ExitCode != 0 || driver.Instances["box1"].Status != "Running" {
		t.Fatalf("Start step failed")
	}

	// Stop
	pStop := &plan.Plan{
		Steps: []plan.Step{{Container: "box1", Action: "stop", Changed: true}},
	}
	rep, _ = exec.Apply(ctx, pStop, apply.ApplyOpts{})
	if rep.ExitCode != 0 || driver.Instances["box1"].Status != "Stopped" {
		t.Fatalf("Stop step failed")
	}

	// Delete
	pDelete := &plan.Plan{
		Steps: []plan.Step{{Container: "box1", Action: "delete", Changed: true}},
	}
	rep, _ = exec.Apply(ctx, pDelete, apply.ApplyOpts{})
	if rep.ExitCode != 0 || len(driver.Instances) != 0 {
		t.Fatalf("Delete step failed")
	}
}

func TestExecutor_DeleteRunningContainer_StopsFirst(t *testing.T) {
	ctx := context.Background()
	driver := fake.New()
	_ = driver.CreateInstance(ctx, provider.InstanceCreateRequest{Name: "box-running"})
	if err := driver.UpdateInstanceState(ctx, "box-running", "start", false); err != nil {
		t.Fatalf("start failed: %v", err)
	}
	if driver.Instances["box-running"].Status != "Running" {
		t.Fatalf("expected box-running to be Running")
	}

	exec := apply.NewExecutor(driver)

	// Deleting a running container must stop it first — real LXD/Incus refuses a
	// non-forced delete of a running instance.
	pDelete := &plan.Plan{
		Steps: []plan.Step{{Container: "box-running", Action: "delete", Changed: true}},
	}
	rep, _ := exec.Apply(ctx, pDelete, apply.ApplyOpts{})
	if rep.ExitCode != 0 {
		t.Fatalf("delete of running container failed (expected stop-then-delete), exit code %d", rep.ExitCode)
	}
	if len(driver.Instances) != 0 {
		t.Errorf("expected container to be deleted, got %d instance(s)", len(driver.Instances))
	}
}

func TestExecutor_DeleteStoppedContainer_SkipsStop(t *testing.T) {
	ctx := context.Background()
	driver := fake.New()
	_ = driver.CreateInstance(ctx, provider.InstanceCreateRequest{Name: "box-stopped"})
	if driver.Instances["box-stopped"].StatusCode != 102 {
		t.Fatalf("expected box-stopped to be Stopped by default")
	}

	// Spy: a stop attempt on an already-stopped instance must not happen.
	stopCalled := false
	driver.UpdateInstanceStateFunc = func(name, action string, force bool) error {
		stopCalled = true
		return nil
	}

	exec := apply.NewExecutor(driver)
	pDelete := &plan.Plan{
		Steps: []plan.Step{{Container: "box-stopped", Action: "delete", Changed: true}},
	}
	rep, _ := exec.Apply(ctx, pDelete, apply.ApplyOpts{})
	if rep.ExitCode != 0 {
		t.Fatalf("delete of stopped container failed, exit code %d: %s", rep.ExitCode, rep.Results[0].Error)
	}
	if stopCalled {
		t.Errorf("expected no stop call for an already-stopped container")
	}
	if len(driver.Instances) != 0 {
		t.Errorf("expected container to be deleted, got %d instance(s)", len(driver.Instances))
	}
}

func TestExecutor_RecreateFallback_StoppedContainer(t *testing.T) {
	ctx := context.Background()
	driver := fake.New()
	_ = driver.CreateInstance(ctx, provider.InstanceCreateRequest{Name: "box-recreate"})
	if driver.Instances["box-recreate"].StatusCode != 102 {
		t.Fatalf("expected box-recreate to be Stopped by default")
	}

	exec := apply.NewExecutor(driver)
	p := &plan.Plan{
		Steps: []plan.Step{{
			Container:       "box-recreate",
			Action:          "recreate",
			Changed:         true,
			RebuildFallback: true,
		}},
	}
	rep, _ := exec.Apply(ctx, p, apply.ApplyOpts{Force: true})
	if rep.ExitCode != 0 {
		t.Fatalf("recreate fallback of stopped container failed, exit code %d: %s", rep.ExitCode, rep.Results[0].Error)
	}
	if _, ok := driver.Instances["box-recreate"]; !ok {
		t.Errorf("expected recreated container to exist after fallback")
	}
}

func TestExecutor_RecreateFallback_RunningContainer(t *testing.T) {
	ctx := context.Background()
	driver := fake.New()
	_ = driver.CreateInstance(ctx, provider.InstanceCreateRequest{Name: "box-recreate-running"})
	if err := driver.UpdateInstanceState(ctx, "box-recreate-running", "start", false); err != nil {
		t.Fatalf("start failed: %v", err)
	}

	stopCalls := 0
	driver.UpdateInstanceStateFunc = func(name, action string, force bool) error {
		if action == "stop" {
			stopCalls++
			inst := driver.Instances[name]
			inst.Status = "Stopped"
			inst.StatusCode = 102
		}
		return nil
	}

	exec := apply.NewExecutor(driver)
	p := &plan.Plan{
		Steps: []plan.Step{{
			Container:       "box-recreate-running",
			Action:          "recreate",
			Changed:         true,
			RebuildFallback: true,
		}},
	}
	rep, _ := exec.Apply(ctx, p, apply.ApplyOpts{Force: true})
	if rep.ExitCode != 0 {
		t.Fatalf("recreate fallback of running container failed, exit code %d: %s", rep.ExitCode, rep.Results[0].Error)
	}
	if stopCalls != 1 {
		t.Errorf("expected exactly 1 stop call for a running container, got %d", stopCalls)
	}
	if _, ok := driver.Instances["box-recreate-running"]; !ok {
		t.Errorf("expected recreated container to exist after fallback")
	}
}

func TestExecutor_DeleteMissingContainer_IsNoop(t *testing.T) {
	ctx := context.Background()
	driver := fake.New()
	exec := apply.NewExecutor(driver)
	pDelete := &plan.Plan{
		Steps: []plan.Step{{Container: "never-existed", Action: "delete", Changed: true}},
	}
	rep, _ := exec.Apply(ctx, pDelete, apply.ApplyOpts{})
	if rep.ExitCode != 0 {
		t.Fatalf("delete of missing container should be a no-op, exit code %d", rep.ExitCode)
	}
}

func TestExecutor_ErrorPrecedence(t *testing.T) {
	ctx := context.Background()
	driver := fake.New()
	driver.CreateInstanceFunc = func(req provider.InstanceCreateRequest) error {
		return errors.New("lxd api failure")
	}

	exec := apply.NewExecutor(driver)

	p := &plan.Plan{
		Steps: []plan.Step{{Container: "box1", Action: "create"}},
	}
	rep, _ := exec.Apply(ctx, p, apply.ApplyOpts{})
	if rep.ExitCode != 4 {
		t.Errorf("expected exit code 4 (PROVIDER_ERROR) on LXD operation error, got %d", rep.ExitCode)
	}
}

func TestExecutor_CreateStartsContainer_Idempotent(t *testing.T) {
	ctx := context.Background()
	driver := fake.New()
	exec := apply.NewExecutor(driver)

	pCreate := &plan.Plan{
		Steps: []plan.Step{{Container: "box1", Action: "create", Changed: true, PowerTransition: "start"}},
	}

	rep, err := exec.Apply(ctx, pCreate, apply.ApplyOpts{})
	if err != nil || rep.ExitCode != 0 {
		t.Fatalf("Create step failed: %v", err)
	}

	if driver.Instances["box1"].Status != "Running" {
		t.Errorf("expected container box1 to be Running after create with PowerTransition=start, got %s", driver.Instances["box1"].Status)
	}
}

func TestExecutor_CreateStoppedContainer_RemainsStopped_Idempotent(t *testing.T) {
	ctx := context.Background()
	driver := fake.New()
	exec := apply.NewExecutor(driver)

	pCreate := &plan.Plan{
		Steps: []plan.Step{{Container: "box2", Action: "create", Changed: true, PowerTransition: "stop"}},
	}

	rep, err := exec.Apply(ctx, pCreate, apply.ApplyOpts{})
	if err != nil || rep.ExitCode != 0 {
		t.Fatalf("Create step failed: %v", err)
	}

	if driver.Instances["box2"].Status != "Stopped" {
		t.Errorf("expected container box2 to be Stopped after create with PowerTransition=stop, got %s", driver.Instances["box2"].Status)
	}
}

func TestExecutor_RecreateFallback_StoppedContainer_RemainsStopped(t *testing.T) {
	ctx := context.Background()
	driver := fake.New()
	_ = driver.CreateInstance(ctx, provider.InstanceCreateRequest{Name: "box3"})

	exec := apply.NewExecutor(driver)

	pRecreate := &plan.Plan{
		Steps: []plan.Step{
			{Container: "box3", Action: "recreate", Changed: true, RebuildFallback: true, PowerTransition: "stop"},
		},
	}

	rep, err := exec.Apply(ctx, pRecreate, apply.ApplyOpts{Force: true})
	if err != nil || rep.ExitCode != 0 {
		t.Fatalf("Recreate fallback step failed: %v", err)
	}

	if driver.Instances["box3"].Status != "Stopped" {
		t.Errorf("expected container box3 to be Stopped after recreate fallback with PowerTransition=stop, got %s", driver.Instances["box3"].Status)
	}
}

func TestExecutor_RebuildNative_Passes(t *testing.T) {
	ctx := context.Background()
	driver := fake.New()
	_ = driver.CreateInstance(ctx, provider.InstanceCreateRequest{Name: "box4"})

	exec := apply.NewExecutor(driver)

	pRecreate := &plan.Plan{
		Steps: []plan.Step{
			{Container: "box4", Action: "recreate", Changed: true, RebuildFallback: false},
		},
	}

	rep, err := exec.Apply(ctx, pRecreate, apply.ApplyOpts{})
	if err != nil || rep.ExitCode != 0 {
		t.Fatalf("Native rebuild step failed: %v", err)
	}
}

func TestReconcilerToExecutor_Integration_PowerTransitions(t *testing.T) {
	ctx := context.Background()
	driver := fake.New()
	_ = driver.CreateInstance(ctx, provider.InstanceCreateRequest{
		Name: "box5",
		Config: map[string]string{
			"user.lxm.user": "ubuntu",
		},
	})
	driver.Instances["box5"].Status = "Running"
	driver.Instances["box5"].StatusCode = 103

	rec := plan.NewReconciler()
	exec := apply.NewExecutor(driver)

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

	p, err := rec.Compute(conf, live, nil, nil, config.BuiltinImageRemotes(), false)
	if err != nil {
		t.Fatalf("Compute error: %v", err)
	}

	if p.Steps[0].PowerTransition != "stop" {
		t.Fatalf("expected PowerTransition 'stop', got %q", p.Steps[0].PowerTransition)
	}

	rep, err := exec.Apply(ctx, p, apply.ApplyOpts{})
	if err != nil || rep.ExitCode != 0 {
		t.Fatalf("Apply error: %v", err)
	}

	if driver.Instances["box5"].Status != "Stopped" {
		t.Errorf("expected container box5 to be Stopped after update with state: stopped, got %s", driver.Instances["box5"].Status)
	}
}

func TestExecutor_WaitPolicyTimeout(t *testing.T) {
	ctx := context.Background()
	driver := fake.New()
	_ = driver.CreateInstance(ctx, provider.InstanceCreateRequest{Name: "waitbox"})
	exec := apply.NewExecutor(driver)

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

	rep, _ := exec.Apply(ctx, p, apply.ApplyOpts{})
	if rep.ExitCode != 7 {
		t.Errorf("expected exit code 7 (WAIT_TIMEOUT), got %d", rep.ExitCode)
	}
}

func TestExecutor_RecipeExecutionAndSnapshot(t *testing.T) {
	ctx := context.Background()
	tmpDir := t.TempDir()
	shFile := "setup.sh"
	if err := writeTestScript(tmpDir, shFile, "#!/bin/bash\necho hello"); err != nil {
		t.Fatalf("writing script: %v", err)
	}

	driver := fake.New()
	_ = driver.CreateInstance(ctx, provider.InstanceCreateRequest{Name: "recipebox"})
	driver.Instances["recipebox"].Status = "Running"
	driver.Instances["recipebox"].StatusCode = 103

	exec := apply.NewExecutor(driver)

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

	rep, err := exec.Apply(ctx, p, apply.ApplyOpts{})
	if err != nil || rep.ExitCode != 0 {
		t.Fatalf("recipe execution failed: %v", err)
	}

	// Verify snapshot-before-recipe created
	snaps, err := driver.GetInstanceSnapshots(ctx, "recipebox")
	if err != nil || len(snaps) == 0 {
		t.Errorf("expected snapshot created before recipe execution, got %d snaps", len(snaps))
	}

	// Verify recipe hash metadata key set
	inst, _, _ := driver.GetInstance(ctx, "recipebox")
	if inst.Config["user.lxm.recipe.setup_sh.hash"] == "" {
		t.Errorf("expected path-qualified recipe hash key stored in instance metadata")
	}
}

func TestExecutor_ContextCancellation(t *testing.T) {
	driver := fake.New()
	_ = driver.CreateInstance(context.Background(), provider.InstanceCreateRequest{Name: "cancelbox"})

	exec := apply.NewExecutor(driver)
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

	ctx := context.Background()
	driver := fake.New()
	_ = driver.CreateInstance(ctx, provider.InstanceCreateRequest{Name: "drybox"})

	execCount := 0
	driver.ExecInstanceFunc = func(name string, cmd []string, uid uint32, env map[string]string) (provider.ExecResult, error) {
		execCount++
		return provider.ExecResult{ExitCode: 0, Stdout: "ok"}, nil
	}

	exec := apply.NewExecutor(driver)

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

	snaps, _ := driver.GetInstanceSnapshots(ctx, "drybox")
	if len(snaps) > 0 {
		t.Errorf("expected ZERO snapshots created under --dry-run, got %d", len(snaps))
	}
}

func TestExecutor_NilPlan(t *testing.T) {
	driver := fake.New()
	exec := apply.NewExecutor(driver)
	_, err := exec.Apply(context.Background(), nil, apply.ApplyOpts{})
	if err == nil {
		t.Errorf("expected error when applying nil plan")
	}
}

func TestExecutor_SingleFilePruneRestriction(t *testing.T) {
	driver := fake.New()
	exec := apply.NewExecutor(driver)
	p := &plan.Plan{Steps: []plan.Step{}}
	rep, err := exec.Apply(context.Background(), p, apply.ApplyOpts{IsSingleFile: true, Prune: true})
	if err == nil || rep.ExitCode != 2 {
		t.Errorf("expected exit code 2 when pruning with single file target")
	}
}

func TestExecutor_WaitPolicy_StopStepNonWaiting(t *testing.T) {
	ctx := context.Background()
	driver := fake.New()
	_ = driver.CreateInstance(ctx, provider.InstanceCreateRequest{Name: "stopbox"})
	driver.Instances["stopbox"].Status = "Running"
	driver.Instances["stopbox"].StatusCode = 103

	driver.ExecInstanceFunc = func(name string, cmd []string, uid uint32, env map[string]string) (provider.ExecResult, error) {
		return provider.ExecResult{ExitCode: 1, Stdout: "", Stderr: "container is stopped"}, nil
	}

	exec := apply.NewExecutor(driver)
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

	rep, err := exec.Apply(ctx, p, apply.ApplyOpts{})
	if err != nil {
		t.Fatalf("unexpected apply error: %v", err)
	}
	if rep.ExitCode != 0 {
		t.Errorf("expected exit code 0 for stop step (should skip wait), got %d", rep.ExitCode)
	}
}

func TestExecutor_WaitPolicy_NonCloudInitImage(t *testing.T) {
	ctx := context.Background()
	driver := fake.New()
	_ = driver.CreateInstance(ctx, provider.InstanceCreateRequest{Name: "alpinebox"})
	driver.Instances["alpinebox"].Status = "Running"
	driver.Instances["alpinebox"].StatusCode = 103

	driver.ExecInstanceFunc = func(name string, cmd []string, uid uint32, env map[string]string) (provider.ExecResult, error) {
		return provider.ExecResult{ExitCode: 127, Stdout: "", Stderr: "cloud-init: command not found"}, nil
	}

	exec := apply.NewExecutor(driver)
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

	rep, err := exec.Apply(ctx, p, apply.ApplyOpts{})
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
	ctx := context.Background()
	driver := fake.New()
	_ = driver.CreateInstance(ctx, provider.InstanceCreateRequest{Name: "softbox"})
	driver.Instances["softbox"].Status = "Running"
	driver.Instances["softbox"].StatusCode = 103

	exec := apply.NewExecutor(driver)
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

	rep, err := exec.Apply(ctx, p, apply.ApplyOpts{})
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
	ctx := context.Background()
	driver := fake.New()
	_ = driver.CreateInstance(ctx, provider.InstanceCreateRequest{Name: "cancelbox"})
	driver.Instances["cancelbox"].Status = "Running"
	driver.Instances["cancelbox"].StatusCode = 103

	blockChan := make(chan struct{})
	driver.ExecInstanceFunc = func(name string, cmd []string, uid uint32, env map[string]string) (provider.ExecResult, error) {
		<-blockChan
		return provider.ExecResult{ExitCode: 0}, nil
	}

	exec := apply.NewExecutor(driver)
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

	cCtx, cancel := context.WithCancel(ctx)
	go func() {
		time.Sleep(20 * time.Millisecond)
		cancel()
		close(blockChan)
	}()

	rep, _ := exec.Apply(cCtx, p, apply.ApplyOpts{})
	if rep.ExitCode != 1 {
		t.Errorf("expected exit code 1 (INTERNAL_ERROR) when cancelled mid-wait, got %d", rep.ExitCode)
	}
}

func TestExecutor_WaitPolicy_TransportError(t *testing.T) {
	ctx := context.Background()
	driver := fake.New()
	_ = driver.CreateInstance(ctx, provider.InstanceCreateRequest{Name: "transportbox"})
	driver.Instances["transportbox"].Status = "Running"
	driver.Instances["transportbox"].StatusCode = 103

	driver.ExecInstanceFunc = func(name string, cmd []string, uid uint32, env map[string]string) (provider.ExecResult, error) {
		return provider.ExecResult{}, fmt.Errorf("websocket transport error")
	}

	exec := apply.NewExecutor(driver)
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

	rep, _ := exec.Apply(ctx, p, apply.ApplyOpts{})
	if rep.ExitCode != 4 {
		t.Errorf("expected exit code 4 (PROVIDER_ERROR) for transport error, got %d", rep.ExitCode)
	}
}

func TestExecutor_WaitPolicy_NetworkSuccess(t *testing.T) {
	ctx := context.Background()
	driver := fake.New()
	_ = driver.CreateInstance(ctx, provider.InstanceCreateRequest{Name: "netbox"})
	driver.Instances["netbox"].Status = "Running"
	driver.Instances["netbox"].StatusCode = 103

	driver.ExecInstanceFunc = func(name string, cmd []string, uid uint32, env map[string]string) (provider.ExecResult, error) {
		return provider.ExecResult{ExitCode: 0, Stdout: "10.0.0.15\n"}, nil
	}

	exec := apply.NewExecutor(driver)
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

	rep, _ := exec.Apply(ctx, p, apply.ApplyOpts{})
	if rep.ExitCode != 0 {
		t.Errorf("expected exit code 0 on network readiness success, got %d", rep.ExitCode)
	}
}

func TestExecutor_WaitPolicy_NetworkTimeout(t *testing.T) {
	ctx := context.Background()
	driver := fake.New()
	_ = driver.CreateInstance(ctx, provider.InstanceCreateRequest{Name: "netfailbox"})
	driver.Instances["netfailbox"].Status = "Running"
	driver.Instances["netfailbox"].StatusCode = 103

	driver.ExecInstanceFunc = func(name string, cmd []string, uid uint32, env map[string]string) (provider.ExecResult, error) {
		return provider.ExecResult{ExitCode: 1, Stdout: ""}, nil
	}

	exec := apply.NewExecutor(driver)
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

	rep, _ := exec.Apply(ctx, p, apply.ApplyOpts{})
	if rep.ExitCode != 7 {
		t.Errorf("expected exit code 7 (WAIT_TIMEOUT) on network wait timeout, got %d", rep.ExitCode)
	}
}

func TestExecutor_WaitPolicy_NonHostnameImage(t *testing.T) {
	ctx := context.Background()
	driver := fake.New()
	_ = driver.CreateInstance(ctx, provider.InstanceCreateRequest{Name: "nohostnamebox"})
	driver.Instances["nohostnamebox"].Status = "Running"
	driver.Instances["nohostnamebox"].StatusCode = 103

	driver.ExecInstanceFunc = func(name string, cmd []string, uid uint32, env map[string]string) (provider.ExecResult, error) {
		return provider.ExecResult{ExitCode: 127, Stderr: "executable file not found"}, nil
	}

	exec := apply.NewExecutor(driver)
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

	rep, _ := exec.Apply(ctx, p, apply.ApplyOpts{})
	if rep.ExitCode != 0 {
		t.Errorf("expected exit code 0 when hostname binary is missing, got %d", rep.ExitCode)
	}
	if len(rep.Warnings) == 0 {
		t.Errorf("expected warning for missing hostname binary, got none")
	}
}

func TestExecutor_InterruptDuringOperation_ReturnsInternalError(t *testing.T) {
	ctx := context.Background()
	driver := fake.New()
	_ = driver.CreateInstance(ctx, provider.InstanceCreateRequest{Name: "opcancelbox"})
	driver.Instances["opcancelbox"].Status = "Running"
	driver.Instances["opcancelbox"].StatusCode = 103

	driver.UpdateInstanceFunc = func(name string, put provider.InstanceUpdateRequest, etag string) error {
		return context.Canceled
	}

	exec := apply.NewExecutor(driver)
	p := &plan.Plan{
		Steps: []plan.Step{
			{
				Container: "opcancelbox",
				Action:    "update",
				Changed:   true,
			},
		},
	}

	rep, _ := exec.Apply(ctx, p, apply.ApplyOpts{})
	if rep.ExitCode != 1 {
		t.Errorf("expected exit code 1 (INTERNAL_ERROR) on operation cancellation, got %d", rep.ExitCode)
	}
}

func TestExecutor_InterruptDuringRecipeScript_ReturnsInternalError(t *testing.T) {
	ctx := context.Background()
	driver := fake.New()
	_ = driver.CreateInstance(ctx, provider.InstanceCreateRequest{Name: "recipecancelbox"})
	driver.Instances["recipecancelbox"].Status = "Running"
	driver.Instances["recipecancelbox"].StatusCode = 103

	driver.ExecInstanceFunc = func(name string, cmd []string, uid uint32, env map[string]string) (provider.ExecResult, error) {
		return provider.ExecResult{ExitCode: 1}, context.Canceled
	}

	exec := apply.NewExecutor(driver)
	p := &plan.Plan{
		Steps: []plan.Step{
			{
				Container: "recipecancelbox",
				Action:    "noop",
				Recipes:   []plan.RecipeStep{{Path: "apply.go"}},
			},
		},
	}

	rep, _ := exec.Apply(ctx, p, apply.ApplyOpts{})
	if rep.ExitCode != 1 {
		t.Errorf("expected exit code 1 (INTERNAL_ERROR) on recipe execution cancellation, got %d", rep.ExitCode)
	}
}

func TestIsTransientAgentError(t *testing.T) {
	transientMatches := []string{
		"LXD VM agent is not currently running",
		"Failed connecting to lxd-agent",
		"The LXD agent is not running on this instance",
		"LXD agent not running",
		"Failed to connect to lxd-agent",
		"Failed to connect to instance socket",
		"websocket: close 1006 (abnormal closure)",
	}

	for _, msg := range transientMatches {
		if !apply.IsTransientAgentError(msg) {
			t.Errorf("expected %q to be transient agent error", msg)
		}
	}

	nonTransient := []string{
		"systemctl: command not found",
		"exit status 1",
		"permission denied",
	}

	for _, msg := range nonTransient {
		if apply.IsTransientAgentError(msg) {
			t.Errorf("expected %q NOT to be transient agent error", msg)
		}
	}
}

func TestExecutor_WaitPolicy_VMAgentHandshake(t *testing.T) {
	ctx := context.Background()
	driver := fake.New()

	attempts := 0
	driver.ExecInstanceFunc = func(name string, cmd []string, uid uint32, env map[string]string) (provider.ExecResult, error) {
		attempts++
		if attempts <= 2 {
			return provider.ExecResult{ExitCode: -1}, fmt.Errorf("LXD VM agent is not currently running")
		}
		return provider.ExecResult{ExitCode: 0, Stdout: "running\n"}, nil
	}

	exec := apply.NewExecutor(driver)
	p := &plan.Plan{
		Steps: []plan.Step{
			{
				Container:     "test-vm",
				Action:        "create",
				InstancesPost: &provider.InstanceCreateRequest{Name: "test-vm", Type: "virtual-machine"},
				Wait:          true,
				WaitPolicy: &config.WaitConfig{
					Agent:    "5s",
					Poll:     "10ms",
					Required: true,
				},
			},
		},
	}

	rep, err := exec.Apply(ctx, p, apply.ApplyOpts{})
	if err != nil || rep.ExitCode != 0 {
		t.Fatalf("expected VM agent handshake success, got exit code %d, error: %v", rep.ExitCode, err)
	}
	if attempts < 3 {
		t.Errorf("expected at least 3 attempts to establish handshake, got %d", attempts)
	}
}

func TestExecutor_WaitPolicy_VMAgentTimeout(t *testing.T) {
	ctx := context.Background()
	driver := fake.New()

	driver.ExecInstanceFunc = func(name string, cmd []string, uid uint32, env map[string]string) (provider.ExecResult, error) {
		return provider.ExecResult{ExitCode: -1}, fmt.Errorf("LXD VM agent is not currently running")
	}

	exec := apply.NewExecutor(driver)
	p := &plan.Plan{
		Steps: []plan.Step{
			{
				Container:     "timeout-vm",
				Action:        "create",
				InstancesPost: &provider.InstanceCreateRequest{Name: "timeout-vm", Type: "virtual-machine"},
				Wait:          true,
				WaitPolicy: &config.WaitConfig{
					Agent:    "50ms",
					Poll:     "10ms",
					Required: true,
				},
			},
		},
	}

	rep, _ := exec.Apply(ctx, p, apply.ApplyOpts{})
	if rep.ExitCode != 7 {
		t.Errorf("expected exit code 7 (WAIT_TIMEOUT) on VM agent timeout, got %d", rep.ExitCode)
	}
}

func TestExecutor_Update_RunningVM_NonLiveUpdatable_StopBeforePUT(t *testing.T) {
	ctx := context.Background()
	driver := fake.New()
	_ = driver.CreateInstance(ctx, provider.InstanceCreateRequest{
		Name: "running-vm",
		Type: "virtual-machine",
	})
	driver.Instances["running-vm"].Status = "Running"
	driver.Instances["running-vm"].StatusCode = 103
	driver.Instances["running-vm"].Config = map[string]string{
		"boot.mode": "uefi-secureboot",
	}

	exec := apply.NewExecutor(driver)

	// Step updating raw.qemu and hugepages with PowerTransition = "restart"
	p := &plan.Plan{
		Steps: []plan.Step{
			{
				Container:       "running-vm",
				Action:          "update",
				Changed:         true,
				PowerTransition: "restart",
				InstancePut: &provider.InstanceUpdateRequest{
					Config: map[string]string{
						"boot.mode":               "uefi-secureboot",
						"limits.memory.hugepages": "true",
						"raw.qemu":                "-cpu host",
					},
				},
			},
		},
	}

	rep, err := exec.Apply(ctx, p, apply.ApplyOpts{})
	if err != nil || rep.ExitCode != 0 {
		t.Fatalf("expected successful update via stop->PUT->start, got exit code %d, err: %v", rep.ExitCode, err)
	}

	inst, _, err := driver.GetInstance(ctx, "running-vm")
	if err != nil {
		t.Fatalf("failed to get instance: %v", err)
	}
	if inst.Config["raw.qemu"] != "-cpu host" {
		t.Errorf("expected raw.qemu to be updated, got %q", inst.Config["raw.qemu"])
	}
	if inst.Config["limits.memory.hugepages"] != "true" {
		t.Errorf("expected limits.memory.hugepages to be updated, got %q", inst.Config["limits.memory.hugepages"])
	}
	if inst.Status != "Running" {
		t.Errorf("expected instance to be restarted and Running, got %q", inst.Status)
	}
}

func TestExecutor_Update_RunningVM_NonLiveUpdatable_StopPowerTransition(t *testing.T) {
	ctx := context.Background()
	driver := fake.New()
	_ = driver.CreateInstance(ctx, provider.InstanceCreateRequest{
		Name: "running-vm-stop",
		Type: "virtual-machine",
	})
	driver.Instances["running-vm-stop"].Status = "Running"
	driver.Instances["running-vm-stop"].StatusCode = 103
	driver.Instances["running-vm-stop"].Config = map[string]string{
		"boot.mode": "uefi-secureboot",
	}

	exec := apply.NewExecutor(driver)

	// Step updating raw.qemu and hugepages with PowerTransition = "stop" (desired state: stopped)
	p := &plan.Plan{
		Steps: []plan.Step{
			{
				Container:       "running-vm-stop",
				Action:          "update",
				Changed:         true,
				PowerTransition: "stop",
				InstancePut: &provider.InstanceUpdateRequest{
					Config: map[string]string{
						"boot.mode":               "uefi-secureboot",
						"limits.memory.hugepages": "true",
						"raw.qemu":                "-cpu host",
					},
				},
			},
		},
	}

	rep, err := exec.Apply(ctx, p, apply.ApplyOpts{})
	if err != nil || rep.ExitCode != 0 {
		t.Fatalf("expected successful update via stop->PUT, got exit code %d, err: %v", rep.ExitCode, err)
	}

	inst, _, err := driver.GetInstance(ctx, "running-vm-stop")
	if err != nil {
		t.Fatalf("failed to get instance: %v", err)
	}
	if inst.Config["raw.qemu"] != "-cpu host" {
		t.Errorf("expected raw.qemu to be updated, got %q", inst.Config["raw.qemu"])
	}
	if inst.Config["limits.memory.hugepages"] != "true" {
		t.Errorf("expected limits.memory.hugepages to be updated, got %q", inst.Config["limits.memory.hugepages"])
	}
	if inst.Status != "Stopped" {
		t.Errorf("expected instance to remain Stopped, got %q", inst.Status)
	}
}
