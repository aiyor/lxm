package plan_test

import (
	"strings"
	"testing"

	"github.com/aiyor/lxm/internal/config"
	"github.com/aiyor/lxm/internal/plan"
)

// testRemotes returns the effective registry used by plan tests: the built-in
// remotes overlaid with a custom remote so image: remote:alias resolution is
// exercised deterministically.
func testRemotes() map[string]string {
	out := config.BuiltinImageRemotes()
	out["corp-images"] = "https://images.corp.example.com"
	return out
}

func TestReconciler_CreateRemoteUncached_PlansFetch(t *testing.T) {
	rec := plan.NewReconciler()
	conf := &config.Config{
		Name:  "box1",
		Image: "ubuntu:24.04",
		Type:  "container",
		User:  "ubuntu",
	}
	p, err := rec.Compute(conf, nil, nil, nil, testRemotes(), false)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	step := p.Steps[0]
	if len(step.ImageOps) != 1 {
		t.Fatalf("expected 1 image op, got %d: %+v", len(step.ImageOps), step.ImageOps)
	}
	op := step.ImageOps[0]
	if op.Op != "fetch" {
		t.Errorf("expected op 'fetch', got %q", op.Op)
	}
	if op.Remote != "ubuntu" || op.Alias != "24.04" || op.LocalAlias != "ubuntu/24.04" || op.Type != "container" {
		t.Errorf("unexpected fetch op: %+v", op)
	}
	if op.RemoteURL != "https://cloud-images.ubuntu.com/releases" {
		t.Errorf("expected resolved built-in URL, got %q", op.RemoteURL)
	}
	// The create payload must reference the canonical local alias, never the
	// literal remote:alias (which LXD's alias grammar forbids).
	if got := step.InstancesPost.Source.Alias; got != "ubuntu/24.04" {
		t.Errorf("expected Source.Alias %q, got %q", "ubuntu/24.04", got)
	}
	if got := step.InstancesPost.Source.Fingerprint; got != "" {
		t.Errorf("expected empty Source.Fingerprint, got %q", got)
	}
}

func TestReconciler_CreateRemoteCached_NoFetch(t *testing.T) {
	rec := plan.NewReconciler()
	conf := &config.Config{
		Name:  "box1",
		Image: "ubuntu:24.04",
		Type:  "container",
		User:  "ubuntu",
	}
	aliases := map[string]bool{"ubuntu/24.04": true}
	p, err := rec.Compute(conf, nil, nil, aliases, testRemotes(), false)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(p.Steps[0].ImageOps) != 0 {
		t.Errorf("expected no fetch for cached image, got %+v", p.Steps[0].ImageOps)
	}
	if got := p.Steps[0].InstancesPost.Source.Alias; got != "ubuntu/24.04" {
		t.Errorf("expected Source.Alias %q, got %q", "ubuntu/24.04", got)
	}
}

func TestReconciler_CreateTypeQualifiedCache(t *testing.T) {
	// A container alias cached but a VM manifest referencing the same
	// remote:alias must still fetch (the canonical aliases are distinct).
	rec := plan.NewReconciler()
	confVM := &config.Config{
		Name:  "vm1",
		Image: "ubuntu:24.04",
		Type:  "virtual-machine",
		User:  "ubuntu",
	}
	aliases := map[string]bool{"ubuntu/24.04": true} // container alias only
	p, err := rec.Compute(confVM, nil, nil, aliases, testRemotes(), false)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	op := p.Steps[0].ImageOps
	if len(op) != 1 || op[0].LocalAlias != "ubuntu/24.04/vm" {
		t.Errorf("expected VM fetch for canonical alias ubuntu/24.04/vm, got %+v", op)
	}
	if got := p.Steps[0].InstancesPost.Source.Alias; got != "ubuntu/24.04/vm" {
		t.Errorf("expected Source.Alias ubuntu/24.04/vm, got %q", got)
	}

	// Both cached -> no fetch.
	both := map[string]bool{"ubuntu/24.04": true, "ubuntu/24.04/vm": true}
	p2, err := rec.Compute(confVM, nil, nil, both, testRemotes(), false)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(p2.Steps[0].ImageOps) != 0 {
		t.Errorf("expected no fetch when both type-qualified aliases cached, got %+v", p2.Steps[0].ImageOps)
	}
}

func TestReconciler_CreateFingerprint_SetsSourceFingerprint(t *testing.T) {
	rec := plan.NewReconciler()
	fp := "8d3c2a0f5e6b4a9c8d7e6f5a4b3c2d1e0f9e8d7c"
	conf := &config.Config{
		Name:  "box1",
		Image: fp,
		Type:  "container",
		User:  "ubuntu",
	}
	p, err := rec.Compute(conf, nil, nil, nil, testRemotes(), false)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	src := p.Steps[0].InstancesPost.Source
	if src.Fingerprint != fp {
		t.Errorf("expected Source.Fingerprint %q, got %q", fp, src.Fingerprint)
	}
	if src.Alias != "" {
		t.Errorf("expected empty Source.Alias for fingerprint, got %q", src.Alias)
	}
	if len(p.Steps[0].ImageOps) != 0 {
		t.Errorf("fingerprint reference must never fetch, got %+v", p.Steps[0].ImageOps)
	}
}

func TestReconciler_CreateLocalAlias_Unchanged(t *testing.T) {
	rec := plan.NewReconciler()
	conf := &config.Config{
		Name:  "box1",
		Image: "jammy",
		Type:  "container",
		User:  "ubuntu",
	}
	p, err := rec.Compute(conf, nil, nil, nil, testRemotes(), false)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got := p.Steps[0].InstancesPost.Source.Alias; got != "jammy" {
		t.Errorf("expected local alias preserved, got %q", got)
	}
	if len(p.Steps[0].ImageOps) != 0 {
		t.Errorf("local alias must never fetch, got %+v", p.Steps[0].ImageOps)
	}
}

func TestReconciler_CreateUnknownRemote_Error(t *testing.T) {
	rec := plan.NewReconciler()
	conf := &config.Config{
		Name:  "box1",
		Image: "no-such-remote:22.04",
		Type:  "container",
		User:  "ubuntu",
	}
	_, err := rec.Compute(conf, nil, nil, nil, config.BuiltinImageRemotes(), false)
	if err == nil {
		t.Fatal("expected unknown-remote error")
	}
	if !strings.Contains(err.Error(), `unknown image remote "no-such-remote"`) {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestReconciler_CustomRemote_ResolvesDeclaredURL(t *testing.T) {
	rec := plan.NewReconciler()
	conf := &config.Config{
		Name:  "box1",
		Image: "corp-images:alpine",
		Type:  "container",
		User:  "ubuntu",
	}
	p, err := rec.Compute(conf, nil, nil, nil, testRemotes(), false)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	op := p.Steps[0].ImageOps
	if len(op) != 1 || op[0].RemoteURL != "https://images.corp.example.com" {
		t.Errorf("expected custom remote URL resolved onto the fetch op, got %+v", op)
	}
	if got := p.Steps[0].InstancesPost.Source.Alias; got != "corp-images/alpine" {
		t.Errorf("expected Source.Alias corp-images/alpine, got %q", got)
	}
}

func TestReconciler_AbsentStatus_NoFetch(t *testing.T) {
	rec := plan.NewReconciler()
	conf := &config.Config{
		Name:   "box1",
		Status: "absent",
	}
	p, err := rec.Compute(conf, map[string]*plan.InstanceSnapshot{
		"box1": {Name: "box1", Status: "Running", ETag: "etag1"},
	}, nil, nil, testRemotes(), false)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if p.Steps[0].Action != "delete" {
		t.Errorf("expected delete action, got %q", p.Steps[0].Action)
	}
	if len(p.Steps[0].ImageOps) != 0 {
		t.Errorf("absent status must never plan a fetch, got %+v", p.Steps[0].ImageOps)
	}
}

func TestReconciler_CreateRecordsUserLXMImage(t *testing.T) {
	rec := plan.NewReconciler()
	conf := &config.Config{
		Name:  "box1",
		Image: "ubuntu:24.04",
		Type:  "container",
		User:  "ubuntu",
	}
	p, err := rec.Compute(conf, nil, nil, nil, testRemotes(), false)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got := p.Steps[0].InstancesPost.Config["user.lxm.image"]; got != "ubuntu:24.04" {
		t.Errorf("expected user.lxm.image recorded, got %q", got)
	}
}

func TestReconciler_RecreateRemoteUncached_PlansFetch(t *testing.T) {
	rec := plan.NewReconciler()
	conf := &config.Config{
		Name:  "box1",
		Image: "ubuntu:24.04",
		Type:  "container",
		User:  "ubuntu",
	}
	live := map[string]*plan.InstanceSnapshot{
		"box1": {
			Name:   "box1",
			Status: "Running",
			ETag:   "etag1",
			Config: map[string]string{
				"image.os":      "ubuntu",
				"image.release": "22.04",
				// no user.lxm.image: legacy instance exercising the fallback
				// heuristic (os:release drift -> recreate)
			},
		},
	}
	p, err := rec.Compute(conf, live, nil, nil, testRemotes(), true)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	step := p.Steps[0]
	if step.Action != "recreate" {
		t.Fatalf("expected recreate, got %q", step.Action)
	}
	if len(step.ImageOps) != 1 || step.ImageOps[0].LocalAlias != "ubuntu/24.04" {
		t.Errorf("expected fetch op for uncached recreate, got %+v", step.ImageOps)
	}
	if step.RebuildPost == nil || step.RebuildPost.Source.Alias != "ubuntu/24.04" {
		t.Errorf("expected rebuilt source to use canonical alias, got %+v", step.RebuildPost)
	}
	if step.InstancesPost == nil || step.InstancesPost.Source.Alias != "ubuntu/24.04" {
		t.Errorf("expected recreate payload to use canonical alias, got %+v", step.InstancesPost)
	}
}

func TestReconciler_RecreateFallback_PlansFetch(t *testing.T) {
	rec := plan.NewReconciler()
	conf := &config.Config{
		Name:  "box1",
		Image: "ubuntu:24.04",
		Type:  "container",
		User:  "ubuntu",
	}
	live := map[string]*plan.InstanceSnapshot{
		"box1": {
			Name:         "box1",
			Status:       "Running",
			ETag:         "etag1",
			HasSnapshots: true,
			Config: map[string]string{
				"image.os":      "ubuntu",
				"image.release": "22.04",
			},
		},
	}
	// hasRebuildExt=false -> delete+create fallback path.
	p, err := rec.Compute(conf, live, nil, nil, testRemotes(), false)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	step := p.Steps[0]
	if step.Action != "recreate" || !step.RebuildFallback {
		t.Errorf("expected fallback recreate, got action=%q fallback=%v", step.Action, step.RebuildFallback)
	}
	if len(step.ImageOps) != 1 {
		t.Errorf("expected fetch op attached to fallback recreate, got %+v", step.ImageOps)
	}
	if step.InstancesPost == nil || step.InstancesPost.Source.Alias != "ubuntu/24.04" {
		t.Errorf("expected fallback create payload to use canonical alias, got %+v", step.InstancesPost)
	}
}

func TestReconciler_ImageFetchDisabled_Gate(t *testing.T) {
	t.Setenv("LXM_IMAGE_FETCH", "0")
	rec := plan.NewReconciler()
	conf := &config.Config{
		Name:  "box1",
		Image: "ubuntu:24.04",
		Type:  "container",
		User:  "ubuntu",
	}
	_, err := rec.Compute(conf, nil, nil, nil, testRemotes(), false)
	if err == nil {
		t.Fatal("expected fetch-disabled error")
	}
	if !strings.Contains(err.Error(), "image fetch is disabled (LXM_IMAGE_FETCH=0)") {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestReconciler_ImageFetchEnabled_GateNotSet(t *testing.T) {
	rec := plan.NewReconciler()
	conf := &config.Config{
		Name:  "box1",
		Image: "ubuntu:24.04",
		Type:  "container",
		User:  "ubuntu",
	}
	p, err := rec.Compute(conf, nil, nil, nil, testRemotes(), false)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(p.Steps[0].ImageOps) != 1 {
		t.Errorf("expected fetch planned with default-enabled gate, got %+v", p.Steps[0].ImageOps)
	}
}

// TestReconciler_ImageMatches_RecordedReference is the §4.5 regression: a live
// instance recording user.lxm.image must match regardless of the remote's OS,
// so no perpetual recreate is planned for custom/multi-OS remotes.
func TestReconciler_ImageMatches_RecordedReference(t *testing.T) {
	cases := []struct {
		name     string
		image    string
		recorded string
		want     string // expected action: "match" (no recreate) | "recreate"
	}{
		{"images:debian/12 matches recorded", "images:debian/12", "images:debian/12", "match"},
		{"images:ubuntu/22.04 matches recorded", "images:ubuntu/22.04", "images:ubuntu/22.04", "match"},
		{"ubuntu-daily:24.04 matches recorded", "ubuntu-daily:24.04", "ubuntu-daily:24.04", "match"},
		{"corp-images:alpine matches recorded", "corp-images:alpine", "corp-images:alpine", "match"},
		{"bare alias matches recorded", "jammy", "jammy", "match"},
		{"recorded mismatch recreates", "images:debian/12", "images:ubuntu/22.04", "recreate"},
	}
	rec := plan.NewReconciler()
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			conf := &config.Config{
				Name:  "box1",
				Image: tc.image,
				Type:  "container",
				User:  "ubuntu",
			}
			live := map[string]*plan.InstanceSnapshot{
				"box1": {
					Name:   "box1",
					Status: "Running",
					ETag:   "etag1",
					Config: map[string]string{
						"user.lxm.image": tc.recorded,
						"image.os":       "debian",
						"image.release":  "bookworm",
					},
				},
			}
			p, err := rec.Compute(conf, live, nil, nil, testRemotes(), true)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if tc.want == "match" {
				if p.Steps[0].Action == "recreate" {
					t.Errorf("expected no recreate for recorded match, got action %q (diff %+v)", p.Steps[0].Action, p.Steps[0].Diff)
				}
				return
			}
			if p.Steps[0].Action != "recreate" {
				t.Errorf("expected action %q, got %q (diff %+v)", tc.want, p.Steps[0].Action, p.Steps[0].Diff)
			}
		})
	}
}

// TestReconciler_ImageMatches_LegacyFallback guards the existing os:release
// heuristic for instances created before user.lxm.image existed.
func TestReconciler_ImageMatches_LegacyFallback(t *testing.T) {
	rec := plan.NewReconciler()
	conf := &config.Config{
		Name:  "box1",
		Image: "ubuntu:22.04",
		Type:  "container",
		User:  "ubuntu",
	}
	live := map[string]*plan.InstanceSnapshot{
		"box1": {
			Name:   "box1",
			Status: "Running",
			ETag:   "etag1",
			Config: map[string]string{
				"image.os":      "ubuntu",
				"image.release": "jammy",
				"image.version": "22.04",
				"user.lxm.user": "ubuntu",
			},
		},
	}
	p, err := rec.Compute(conf, live, nil, nil, testRemotes(), true)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if p.Steps[0].Action != "noop" {
		t.Errorf("expected noop for legacy ubuntu:22.04 -> ubuntu:jammy, got %q", p.Steps[0].Action)
	}
}

// TestReconciler_UpdateBackfillsImageRecord covers M3: an instance created
// before user.lxm.image existed that is updated (not recreated) must get the
// record backfilled on the update payload, so the imageMatches fast path
// (§4.5) becomes authoritative rather than relying on the legacy heuristic
// forever.
func TestReconciler_UpdateBackfillsImageRecord(t *testing.T) {
	rec := plan.NewReconciler()
	conf := &config.Config{
		Name:  "box1",
		Image: "ubuntu:22.04",
		Type:  "container",
		User:  "ubuntu",
	}
	live := map[string]*plan.InstanceSnapshot{
		"box1": {
			Name:   "box1",
			Status: "Running",
			ETag:   "etag1",
			Config: map[string]string{
				"image.os":      "ubuntu",
				"image.release": "jammy",
				"image.version": "22.04",
				"user.lxm.user": "alice", // drift → update step
				// no user.lxm.image: pre-feature instance
			},
		},
	}
	p, err := rec.Compute(conf, live, nil, nil, testRemotes(), true)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if p.Steps[0].Action != "update" {
		t.Fatalf("expected update step, got %q", p.Steps[0].Action)
	}
	if got := p.Steps[0].InstancePut.Config["user.lxm.image"]; got != "ubuntu:22.04" {
		t.Errorf("expected user.lxm.image backfilled on update, got %q", got)
	}
}
