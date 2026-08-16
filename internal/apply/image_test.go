package apply_test

import (
	"context"
	"errors"
	"testing"

	"github.com/aiyor/lxm/internal/apply"
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
