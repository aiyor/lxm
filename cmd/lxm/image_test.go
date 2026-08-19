package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/aiyor/lxm/internal/plan"
	"github.com/aiyor/lxm/internal/provider"
	"github.com/aiyor/lxm/internal/provider/fake"
)

func writeManifest(t *testing.T, content string) string {
	t.Helper()
	dir := t.TempDir()
	cfgFile := filepath.Join(dir, "dev.yaml")
	if err := os.WriteFile(cfgFile, []byte(content), 0644); err != nil {
		t.Fatalf("writing manifest: %v", err)
	}
	return cfgFile
}

func TestRun_ApplyRemoteImage_FetchesAndCreates(t *testing.T) {
	driver := fake.New()
	cfgFile := writeManifest(t, "name: dev-box\nimage: ubuntu:24.04\nstatus: present\n")

	var stdout, stderr bytes.Buffer
	code := run([]string{"apply", cfgFile}, &stdout, &stderr, driver)
	if code != 0 {
		t.Fatalf("apply returned %d, want 0. Stderr: %s", code, stderr.String())
	}
	if len(driver.Fetches) != 1 {
		t.Fatalf("expected 1 remote fetch, got %d: %+v", len(driver.Fetches), driver.Fetches)
	}
	f := driver.Fetches[0]
	if f.RemoteURL != "https://cloud-images.ubuntu.com/releases" || f.Alias != "24.04" || f.LocalAlias != "ubuntu/24.04" {
		t.Errorf("unexpected fetch record: %+v", f)
	}
	inst, _, err := driver.GetInstance(context.Background(), "dev-box")
	if err != nil {
		t.Fatalf("expected dev-box created: %v", err)
	}
	if inst.Config["user.lxm.image"] != "ubuntu:24.04" {
		t.Errorf("expected user.lxm.image recorded on instance, got %q", inst.Config["user.lxm.image"])
	}
}

func TestRun_ApplyCachedRemote_NoFetch(t *testing.T) {
	driver := fake.New()
	// Pre-seed the canonical local alias: the image is already cached. The
	// seeding itself is not an apply-time fetch, so its record is cleared.
	if err := driver.CopyRemoteImage(context.Background(), "https://cloud-images.ubuntu.com/releases", "24.04", "container", "ubuntu/24.04"); err != nil {
		t.Fatalf("seeding fake alias: %v", err)
	}
	driver.Fetches = nil
	cfgFile := writeManifest(t, "name: dev-box\nimage: ubuntu:24.04\nstatus: present\n")

	var stdout, stderr bytes.Buffer
	code := run([]string{"apply", cfgFile}, &stdout, &stderr, driver)
	if code != 0 {
		t.Fatalf("apply returned %d, want 0. Stderr: %s", code, stderr.String())
	}
	if len(driver.Fetches) != 0 {
		t.Errorf("cached image must not be re-fetched, got %d fetch(es): %+v", len(driver.Fetches), driver.Fetches)
	}
	if _, _, err := driver.GetInstance(context.Background(), "dev-box"); err != nil {
		t.Errorf("expected dev-box created from cached image: %v", err)
	}
}

func TestRun_PlanRemoteImage_JSONShowsFetchOp(t *testing.T) {
	driver := fake.New()
	cfgFile := writeManifest(t, "name: dev-box\nimage: ubuntu:24.04\nstatus: present\n")

	var stdout, stderr bytes.Buffer
	code := run([]string{"plan", cfgFile, "--format", "json"}, &stdout, &stderr, driver)
	if code != 0 {
		t.Fatalf("plan returned %d, want 0. Stderr: %s", code, stderr.String())
	}
	var env struct {
		Plan struct {
			Steps []struct {
				ImageOps []plan.ImageOp `json:"image_ops"`
			} `json:"steps"`
		} `json:"plan"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &env); err != nil {
		t.Fatalf("parsing plan JSON: %v. Output: %s", err, stdout.String())
	}
	if len(env.Plan.Steps) == 0 {
		t.Fatalf("expected plan steps, got %+v", env.Plan)
	}
	ops := env.Plan.Steps[0].ImageOps
	if len(ops) != 1 {
		t.Fatalf("expected 1 image op in plan, got %d: %+v", len(ops), ops)
	}
	op := ops[0]
	if op.Op != "fetch" || op.Remote != "ubuntu" || op.LocalAlias != "ubuntu/24.04" {
		t.Errorf("unexpected image op: %+v", op)
	}
	// The plan is offline: the fetch op carries the resolved URL but the
	// remote is never contacted.
	if len(driver.Fetches) != 0 {
		t.Errorf("plan must not contact the remote, got %d fetch(es)", len(driver.Fetches))
	}
}

func TestRun_PlanUnknownRemote_Exit3(t *testing.T) {
	driver := fake.New()
	cfgFile := writeManifest(t, "name: dev-box\nimage: no-such-remote:22.04\nstatus: present\n")

	var stdout, stderr bytes.Buffer
	code := run([]string{"plan", cfgFile}, &stdout, &stderr, driver)
	if code != 3 {
		t.Fatalf("plan with unknown remote returned %d, want 3 (CONFIG_ERROR). Stderr: %s", code, stderr.String())
	}
	if !strings.Contains(stderr.String(), `unknown image remote "no-such-remote"`) {
		t.Errorf("stderr missing unknown-remote error: %q", stderr.String())
	}
}

func TestRun_PlanImageRemotesConflict_Exit3(t *testing.T) {
	driver := fake.New()
	dir := t.TempDir()
	// Two leaf manifests declare the same remote with different URLs: fleet
	// union must fail with exit 3 citing both files.
	cfgA := filepath.Join(dir, "a.yaml")
	cfgB := filepath.Join(dir, "b.yaml")
	_ = os.WriteFile(cfgA, []byte("name: a\nimage: mirror:alpine\nstatus: present\nimage_remotes:\n  mirror: https://a.example.com\n"), 0644)
	_ = os.WriteFile(cfgB, []byte("name: b\nimage: mirror:alpine\nstatus: present\nimage_remotes:\n  mirror: https://b.example.com\n"), 0644)

	var stdout, stderr bytes.Buffer
	code := run([]string{"plan", dir}, &stdout, &stderr, driver)
	if code != 3 {
		t.Fatalf("plan with conflicting image_remotes returned %d, want 3. Stderr: %s", code, stderr.String())
	}
	if !strings.Contains(stderr.String(), "conflicting URLs") {
		t.Errorf("stderr missing conflict error: %q", stderr.String())
	}
}

// TestRun_ApplyImageAliasProbeFailure_Exit4 covers M1: a failed local-alias
// inventory probe at apply must be fatal (exit 4), so a broken probe cannot
// silently plan redundant simplestreams pulls for every cached remote image.
// plan/diff remain lenient (offline-capable).
func TestRun_ApplyImageAliasProbeFailure_Exit4(t *testing.T) {
	driver := fake.New()
	driver.GetImageAliasesFunc = func() ([]provider.ImageAlias, error) {
		return nil, errors.New("probe failed")
	}
	cfgFile := writeManifest(t, "name: dev-box\nimage: ubuntu:24.04\nstatus: present\n")

	var stdout, stderr bytes.Buffer
	code := run([]string{"apply", cfgFile}, &stdout, &stderr, driver)
	if code != 4 {
		t.Fatalf("apply with probe failure returned %d, want 4 (LXD_ERROR). Stderr: %s", code, stderr.String())
	}
	if !strings.Contains(stderr.String(), "listing local image aliases") {
		t.Errorf("stderr missing probe error: %q", stderr.String())
	}

	// plan stays lenient: probe failure degrades to an empty inventory.
	stdout.Reset()
	stderr.Reset()
	code = run([]string{"plan", cfgFile, "--format", "json"}, &stdout, &stderr, driver)
	if code != 0 {
		t.Errorf("plan with probe failure returned %d, want 0 (lenient). Stderr: %s", code, stderr.String())
	}
}

// TestRun_ApplyPlan_ImageOpRoundTrip verifies the plan->apply contract end to
// end: the fetch op computed at plan time carries the resolved URL, and the
// executor consumes exactly that op.
func TestRun_ApplyPlan_ImageOpRoundTrip(t *testing.T) {
	driver := fake.New()
	cfgFile := writeManifest(t, "name: dev-box\nimage: ubuntu:24.04\nstatus: present\n")

	var planOut, planErr bytes.Buffer
	code := run([]string{"plan", cfgFile, "--format", "json"}, &planOut, &planErr, driver)
	if code != 0 {
		t.Fatalf("plan returned %d, want 0. Stderr: %s", code, planErr.String())
	}
	var env struct {
		Plan struct {
			Steps []struct {
				ImageOps []plan.ImageOp `json:"image_ops"`
			} `json:"steps"`
		} `json:"plan"`
	}
	if err := json.Unmarshal(planOut.Bytes(), &env); err != nil {
		t.Fatalf("parsing plan JSON: %v", err)
	}
	if len(env.Plan.Steps) != 1 || len(env.Plan.Steps[0].ImageOps) != 1 {
		t.Fatalf("expected 1 image op at plan time, got %+v", env.Plan.Steps)
	}

	var out, errB bytes.Buffer
	code = run([]string{"apply", cfgFile}, &out, &errB, driver)
	if code != 0 {
		t.Fatalf("apply returned %d, want 0. Stderr: %s", code, errB.String())
	}
	if len(driver.Fetches) != 1 {
		t.Fatalf("expected the planned fetch to be executed once, got %d", len(driver.Fetches))
	}
}
