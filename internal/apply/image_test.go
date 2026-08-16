package apply_test

import (
	"context"
	"errors"
	"testing"

	"github.com/aiyor/lxm/internal/apply"
	"github.com/aiyor/lxm/internal/config"
	"github.com/aiyor/lxm/internal/lxd"
	"github.com/aiyor/lxm/internal/plan"
	"github.com/canonical/lxd/shared/api"
)

func fetchOp(localAlias string) plan.ImageOp {
	return plan.ImageOp{
		Op:         "fetch",
		Remote:     "ubuntu",
		RemoteURL:  "https://cloud-images.ubuntu.com/releases",
		Alias:      "24.04",
		LocalAlias: localAlias,
		Type:       "container",
	}
}

func debianFetchOp() plan.ImageOp {
	return plan.ImageOp{
		Op:         "fetch",
		Remote:     "images",
		RemoteURL:  "https://images.lxd.canonical.com",
		Alias:      "debian/12",
		LocalAlias: "images/debian/12",
		Type:       "container",
	}
}

func TestExecutor_PhaseMinusOne_FetchesBeforeCreate(t *testing.T) {
	fake := lxd.NewFakeInstanceServer()
	exec := apply.NewExecutor(fake)

	p := &plan.Plan{
		Schema: "lxm/plan/v1",
		Steps: []plan.Step{
			{
				Container:     "box1",
				Action:        "create",
				Changed:       true,
				ImageOps:      []plan.ImageOp{fetchOp("ubuntu/24.04")},
				InstancesPost: &api.InstancesPost{Name: "box1"},
			},
		},
	}

	report, err := exec.Apply(context.Background(), p, apply.ApplyOpts{Jobs: 1})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if report.ExitCode != 0 {
		t.Fatalf("expected exit 0, got %d: %+v", report.ExitCode, report.Errors)
	}
	if len(fake.Images.Fetches) != 1 {
		t.Fatalf("expected exactly 1 fetch, got %d: %+v", len(fake.Images.Fetches), fake.Images.Fetches)
	}
	f := fake.Images.Fetches[0]
	if f.RemoteURL != "https://cloud-images.ubuntu.com/releases" || f.Alias != "24.04" || f.LocalAlias != "ubuntu/24.04" || f.Type != "container" {
		t.Errorf("unexpected fetch record: %+v", f)
	}
	if _, ok := fake.Instances["box1"]; !ok {
		t.Error("expected instance to be created after the fetch")
	}
}

func TestExecutor_PhaseMinusOne_DedupFleet(t *testing.T) {
	fake := lxd.NewFakeInstanceServer()
	exec := apply.NewExecutor(fake)

	p := &plan.Plan{
		Schema: "lxm/plan/v1",
		Steps: []plan.Step{
			{Container: "a", Action: "create", Changed: true, ImageOps: []plan.ImageOp{fetchOp("ubuntu/24.04")}},
			{Container: "b", Action: "create", Changed: true, ImageOps: []plan.ImageOp{fetchOp("ubuntu/24.04")}},
			{Container: "c", Action: "create", Changed: true, ImageOps: []plan.ImageOp{debianFetchOp()}},
		},
	}

	report, err := exec.Apply(context.Background(), p, apply.ApplyOpts{Jobs: 5})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if report.ExitCode != 0 {
		t.Fatalf("expected exit 0, got %d: %+v", report.ExitCode, report.Errors)
	}
	// Identical (RemoteURL, Alias, Type) fetches deduplicate to one.
	if len(fake.Images.Fetches) != 2 {
		t.Errorf("expected 2 deduplicated fetches, got %d: %+v", len(fake.Images.Fetches), fake.Images.Fetches)
	}
}

func TestExecutor_PhaseMinusOne_FetchFailureAborts(t *testing.T) {
	fake := lxd.NewFakeInstanceServer()
	fake.CopyRemoteImageFunc = func(ctx context.Context, remoteURL, alias, imageType, localAlias string) error {
		return errors.New("failed resolving alias")
	}
	exec := apply.NewExecutor(fake)

	p := &plan.Plan{
		Schema: "lxm/plan/v1",
		Steps: []plan.Step{
			{
				Container:     "box1",
				Action:        "create",
				Changed:       true,
				ImageOps:      []plan.ImageOp{fetchOp("ubuntu/24.04")},
				InstancesPost: &api.InstancesPost{Name: "box1"},
			},
		},
	}

	report, _ := exec.Apply(context.Background(), p, apply.ApplyOpts{Jobs: 1})
	if report.ExitCode != 4 {
		t.Fatalf("expected exit 4 on fetch failure, got %d", report.ExitCode)
	}
	if len(report.Errors) != 1 || report.Errors[0].Code != "LXD_ERROR" {
		t.Errorf("expected one LXD_ERROR entry, got %+v", report.Errors)
	}
	// Phase-abort semantics: the instance step must NOT run after a fetch failure.
	if _, ok := fake.Instances["box1"]; ok {
		t.Error("instance must not be created after a fetch failure")
	}
}

func TestExecutor_PhaseMinusOne_AliasAlreadyExistsIsSuccess(t *testing.T) {
	fake := lxd.NewFakeInstanceServer()
	exec := apply.NewExecutor(fake)

	fetchOnly := &plan.Plan{
		Schema: "lxm/plan/v1",
		Steps: []plan.Step{
			{Container: "box1", Action: "noop", Changed: false, ImageOps: []plan.ImageOp{fetchOp("ubuntu/24.04")}},
		},
	}
	if _, err := exec.Apply(context.Background(), fetchOnly, apply.ApplyOpts{Jobs: 1}); err != nil {
		t.Fatalf("first apply failed: %v", err)
	}

	// A second apply with the same fetch op: the fake (like LXD) rejects the
	// duplicate alias, which the executor treats as a no-op.
	report, err := exec.Apply(context.Background(), fetchOnly, apply.ApplyOpts{Jobs: 1})
	if err != nil {
		t.Fatalf("second apply failed: %v", err)
	}
	if report.ExitCode != 0 {
		t.Fatalf("expected exit 0 on Alias already exists, got %d: %+v", report.ExitCode, report.Errors)
	}
}

func TestExecutor_PhaseMinusOne_DryRunSkipsFetch(t *testing.T) {
	fake := lxd.NewFakeInstanceServer()
	exec := apply.NewExecutor(fake)

	p := &plan.Plan{
		Schema: "lxm/plan/v1",
		Steps: []plan.Step{
			{Container: "box1", Action: "create", Changed: true, ImageOps: []plan.ImageOp{fetchOp("ubuntu/24.04")}},
		},
	}

	report, err := exec.Apply(context.Background(), p, apply.ApplyOpts{DryRun: true, Jobs: 1})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if report.ExitCode != 0 {
		t.Fatalf("expected exit 0, got %d", report.ExitCode)
	}
	if len(fake.Images.Fetches) != 0 {
		t.Errorf("dry-run must not fetch, got %d fetch(es)", len(fake.Images.Fetches))
	}
}

// TestExecutor_Rebuild_RefreshesImageRecord is the H1 regression: LXD's
// rebuild preserves user.* config but resets image.* to the new image, so the
// recorded user.lxm.image would otherwise stay stale and re-plan a perpetual
// recreate for non-OS remotes (images:…, custom remotes). After a rebuild the
// executor must re-record the reference on the live instance so the next plan
// is a no-op.
func TestExecutor_Rebuild_RefreshesImageRecord(t *testing.T) {
	fake := lxd.NewFakeInstanceServer()
	_ = fake.CreateInstance(api.InstancesPost{Name: "box1", InstancePut: api.InstancePut{Config: map[string]string{
		"user.lxm.managed": "true",
		"user.lxm.user":    "ubuntu",
		"user.lxm.image":   "images:debian/11",
		"image.os":         "debian",
		"image.release":    "bullseye",
	}}})

	exec := apply.NewExecutor(fake)

	// Plan a recreate from images:debian/11 → images:debian/12 (non-fallback
	// rebuild). The step carries both the rebuild source and the create payload
	// whose config holds the fresh user.lxm.image.
	conf := &config.Config{
		Name:  "box1",
		Image: "images:debian/12",
		Type:  "container",
		User:  "ubuntu",
	}
	rec := plan.NewReconciler()
	p, err := rec.Compute(conf, map[string]*plan.InstanceSnapshot{
		"box1": {
			Name:   "box1",
			Status: "Running",
			Config: map[string]string{
				"user.lxm.managed": "true",
				"user.lxm.user":    "ubuntu",
				"user.lxm.image":   "images:debian/11",
				"image.os":         "debian",
				"image.release":    "bullseye",
			},
		},
	}, nil, nil, config.BuiltinImageRemotes(), true)
	if err != nil {
		t.Fatalf("Compute: %v", err)
	}
	step := p.Steps[0]
	if step.Action != "recreate" || step.RebuildFallback {
		t.Fatalf("expected non-fallback recreate, got action=%q fallback=%v", step.Action, step.RebuildFallback)
	}
	if step.RebuildPost == nil || step.RebuildPost.Source.Alias != "images/debian/12" {
		t.Fatalf("expected rebuild source images/debian/12, got %+v", step.RebuildPost)
	}

	report, err := exec.Apply(context.Background(), p, apply.ApplyOpts{Jobs: 1})
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if report.ExitCode != 0 {
		t.Fatalf("expected exit 0, got %d: %+v", report.ExitCode, report.Errors)
	}

	// The live record must be refreshed to the new reference.
	inst, _, err := fake.GetInstance("box1")
	if err != nil {
		t.Fatalf("GetInstance: %v", err)
	}
	if got := inst.Config["user.lxm.image"]; got != "images:debian/12" {
		t.Fatalf("user.lxm.image not refreshed after rebuild, got %q", got)
	}

	// Re-plan against the refreshed live state: must be a no-op, not a recreate.
	p2, err := rec.Compute(conf, map[string]*plan.InstanceSnapshot{
		"box1": {
			Name:   "box1",
			Status: "Running",
			Config: inst.Config,
		},
	}, nil, nil, config.BuiltinImageRemotes(), true)
	if err != nil {
		t.Fatalf("re-Compute: %v", err)
	}
	if p2.Steps[0].Action == "recreate" {
		t.Errorf("expected no perpetual recreate after rebuild refresh, got %q (diff %+v)", p2.Steps[0].Action, p2.Steps[0].Diff)
	}
}

// TestExecutor_PhaseMinusOne_DeadlineRetryable covers L1: a fetch that times
// out (waitOpContext deadline) is a transient network error and must be marked
// retryable per spec §7.4, even though ClassifyLXDError only marks ETag/412
// conflicts.
func TestExecutor_PhaseMinusOne_DeadlineRetryable(t *testing.T) {
	fake := lxd.NewFakeInstanceServer()
	fake.CopyRemoteImageFunc = func(ctx context.Context, remoteURL, alias, imageType, localAlias string) error {
		return context.DeadlineExceeded
	}
	exec := apply.NewExecutor(fake)

	p := &plan.Plan{
		Schema: "lxm/plan/v1",
		Steps: []plan.Step{
			{Container: "box1", Action: "create", Changed: true, ImageOps: []plan.ImageOp{fetchOp("ubuntu/24.04")}},
		},
	}
	report, _ := exec.Apply(context.Background(), p, apply.ApplyOpts{Jobs: 1})
	if report.ExitCode != 4 {
		t.Fatalf("expected exit 4, got %d", report.ExitCode)
	}
	if len(report.Errors) != 1 || !report.Errors[0].Retryable {
		t.Errorf("expected retryable deadline-exceeded error, got %+v", report.Errors)
	}
}
