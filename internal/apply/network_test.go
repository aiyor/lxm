package apply_test

import (
	"context"
	"testing"

	"github.com/aiyor/lxm/internal/apply"
	"github.com/aiyor/lxm/internal/plan"
	"github.com/aiyor/lxm/internal/provider"
	"github.com/aiyor/lxm/internal/provider/fake"
)

func TestApply_NetworkPhase_BeforeInstances(t *testing.T) {
	ctx := context.Background()
	driver := fake.New()
	exec := apply.NewExecutor(driver)

	p := &plan.Plan{
		Schema: "lxm/plan/v1",
		NetworkSteps: []plan.NetworkStep{
			{Kind: "create_acl", Name: "lxm-vmbr0", Changed: true,
				ACLPost: &provider.NetworkACLCreateRequest{Name: "lxm-vmbr0"}},
			{Kind: "create_vswitch", Name: "vmbr0", Changed: true,
				NetPost: &provider.NetworkCreateRequest{Name: "vmbr0", Type: "bridge", Config: map[string]string{"security.acls": "lxm-vmbr0"}}},
		},
		Steps: []plan.Step{
			{Container: "web-a", Action: "create", Changed: true},
		},
	}

	report, err := exec.Apply(ctx, p, apply.ApplyOpts{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if report.ExitCode != 0 {
		t.Fatalf("expected exit 0, got %d (%v)", report.ExitCode, report.Errors)
	}

	// ACL created before vswitch; vswitch references the ACL.
	if len(driver.NetworkACLs) != 1 {
		t.Fatalf("expected 1 ACL, got %d", len(driver.NetworkACLs))
	}
	if _, ok := driver.Networks["vmbr0"]; !ok {
		t.Fatalf("expected vswitch vmbr0 created")
	}
	// Instance created (phase 2 ran because phase 1 succeeded).
	if _, ok := driver.Instances["web-a"]; !ok {
		t.Fatalf("expected instance web-a created after network phase")
	}

	if len(report.NetworkResults) != 2 {
		t.Fatalf("expected 2 network results, got %d", len(report.NetworkResults))
	}
	for _, nr := range report.NetworkResults {
		if !nr.OK {
			t.Fatalf("network result not ok: %+v", nr)
		}
	}
}

func TestApply_NetworkFailure_AbortsBeforeInstances(t *testing.T) {
	ctx := context.Background()
	driver := fake.New()
	// Pre-create vmbr0 so the create_vswitch step fails ("already exists").
	if err := driver.CreateNetwork(ctx, provider.NetworkCreateRequest{Name: "vmbr0", Type: "bridge"}); err != nil {
		t.Fatal(err)
	}

	exec := apply.NewExecutor(driver)

	p := &plan.Plan{
		Schema: "lxm/plan/v1",
		NetworkSteps: []plan.NetworkStep{
			{Kind: "create_vswitch", Name: "vmbr0", Changed: true,
				NetPost: &provider.NetworkCreateRequest{Name: "vmbr0", Type: "bridge"}},
		},
		Steps: []plan.Step{
			{Container: "web-a", Action: "create", Changed: true},
		},
	}

	report, err := exec.Apply(ctx, p, apply.ApplyOpts{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if report.ExitCode != 4 {
		t.Fatalf("expected exit 4 (LXD_ERROR), got %d", report.ExitCode)
	}
	if len(report.NetworkResults) != 1 || report.NetworkResults[0].OK {
		t.Fatalf("expected failing network result, got %+v", report.NetworkResults)
	}
	if _, ok := driver.Instances["web-a"]; ok {
		t.Fatalf("phase-abort violated: instance created despite network failure")
	}
}

func TestApply_DryRun_NoNetworkMutation(t *testing.T) {
	ctx := context.Background()
	driver := fake.New()
	exec := apply.NewExecutor(driver)

	p := &plan.Plan{
		Schema: "lxm/plan/v1",
		NetworkSteps: []plan.NetworkStep{
			{Kind: "create_acl", Name: "lxm-vmbr0", Changed: true,
				ACLPost: &provider.NetworkACLCreateRequest{Name: "lxm-vmbr0"}},
		},
	}
	report, err := exec.Apply(ctx, p, apply.ApplyOpts{DryRun: true})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if report.ExitCode != 0 {
		t.Fatalf("expected exit 0, got %d", report.ExitCode)
	}
	if len(driver.NetworkACLs) != 0 {
		t.Fatalf("dry-run must not create ACLs")
	}
	if len(report.NetworkResults) != 1 {
		t.Fatalf("expected network result recorded in dry-run")
	}
}

func TestApply_UpdateACL_UsesFreshETag(t *testing.T) {
	ctx := context.Background()
	driver := fake.New()
	_ = driver.CreateNetworkACL(ctx, provider.NetworkACLCreateRequest{
		Name:   "lxm-vmbr0",
		Config: map[string]string{},
	})
	exec := apply.NewExecutor(driver)

	p := &plan.Plan{
		Schema: "lxm/plan/v1",
		NetworkSteps: []plan.NetworkStep{
			{Kind: "update_acl", Name: "lxm-vmbr0", Changed: true,
				ACLPut: &provider.NetworkACLUpdateRequest{Config: map[string]string{"user.lxm.managed": "true"}}},
		},
	}
	report, err := exec.Apply(ctx, p, apply.ApplyOpts{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if report.ExitCode != 0 {
		t.Fatalf("expected exit 0, got %d: %v", report.ExitCode, report.Errors)
	}
	if driver.NetworkACLs["lxm-vmbr0"].Config["user.lxm.managed"] != "true" {
		t.Fatalf("ACL config not updated")
	}
}
