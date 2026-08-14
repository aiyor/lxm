package plan_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/aiyor/lxm/internal/config"
	"github.com/aiyor/lxm/internal/plan"
)

func TestReconciler_NilManifest_Error(t *testing.T) {
	r := plan.NewReconciler()
	_, err := r.Compute(nil, nil, false)
	if err == nil {
		t.Fatalf("expected error for nil manifest")
	}
}

func TestReconciler_Compute_Create(t *testing.T) {
	rec := plan.NewReconciler()

	conf := &config.Config{
		Name:  "box1",
		Image: "ubuntu:24.04",
		User:  "ubuntu",
		Recipes: []config.RecipeGroup{
			{RunAs: "root", Scripts: []string{"setup.sh"}},
		},
	}

	p, err := rec.Compute(conf, nil, false)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(p.Steps) != 1 {
		t.Fatalf("expected 1 step, got %d", len(p.Steps))
	}

	step := p.Steps[0]
	if step.Action != "create" {
		t.Errorf("expected action 'create', got %q", step.Action)
	}
	if !step.Changed {
		t.Errorf("expected changed=true")
	}
	if len(step.Recipes) != 1 {
		t.Errorf("expected 1 recipe step attached")
	}
	if step.InstancesPost.Config["user.lxm.managed"] != "true" {
		t.Errorf("expected user.lxm.managed=true in InstancesPost payload")
	}
}

func TestReconciler_Compute_AbsentStatus(t *testing.T) {
	rec := plan.NewReconciler()

	conf := &config.Config{
		Name:   "box1",
		Status: "absent",
	}

	// Absent status when instance doesn't exist -> noop
	p, err := rec.Compute(conf, nil, false)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if p.Steps[0].Action != "noop" {
		t.Errorf("expected action 'noop' for absent status when non-existent, got %q", p.Steps[0].Action)
	}

	// Absent status when instance exists -> delete
	live := map[string]*plan.InstanceSnapshot{
		"box1": {Name: "box1", Status: "Running", ETag: "etag1"},
	}
	p2, err := rec.Compute(conf, live, false)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if p2.Steps[0].Action != "delete" {
		t.Errorf("expected action 'delete', got %q", p2.Steps[0].Action)
	}
}

func TestReconciler_Compute_Update_UserGroupsMountsNetworks(t *testing.T) {
	rec := plan.NewReconciler()

	conf := &config.Config{
		Name:   "box1",
		Image:  "ubuntu:24.04",
		User:   "devuser",
		Groups: []string{"dev", "staging"},
		Mounts: []config.Mount{
			{Source: "/tmp/data", Path: "/mnt/data"},
		},
		Networks: []config.NetworkConfig{
			{Name: "eth0", IPv4: "10.0.0.10", Parent: "lxdbr0"},
		},
		Recipes: []config.RecipeGroup{
			{Scripts: []string{"app.sh"}},
		},
	}

	live := map[string]*plan.InstanceSnapshot{
		"box1": {
			Name:   "box1",
			Status: "Running",
			Config: map[string]string{
				"image.os":        "ubuntu",
				"image.release":   "24.04",
				"user.lxm.user":   "ubuntu",
				"user.lxm.groups": "oldgroup",
			},
			Devices: map[string]map[string]string{
				"olddisk": {"type": "disk", "source": "/old", "path": "/mnt/old"},
				"eth0":    {"type": "nic", "ipv4.address": "10.0.0.99", "parent": "lxdbr0"},
			},
		},
	}

	p, err := rec.Compute(conf, live, false)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if p.Steps[0].Action != "update" {
		t.Errorf("expected action 'update', got %q", p.Steps[0].Action)
	}
	if len(p.Steps[0].Diff) == 0 {
		t.Errorf("expected field diffs for user, groups, mounts, networks")
	}
	if len(p.Steps[0].Recipes) != 1 {
		t.Errorf("expected recipe attached")
	}
}

// B2/B3: mount devices must carry shift=true (idmapping) and NIC devices must
// carry nictype=bridged on both create and update payloads, matching the
// SPEC_MANIFEST contract and the legacy internal/lxm device helpers.
func TestReconciler_Compute_MountAndNicDeviceProps(t *testing.T) {
	rec := plan.NewReconciler()

	conf := &config.Config{
		Name:  "box1",
		Image: "ubuntu:24.04",
		User:  "ubuntu",
		Mounts: []config.Mount{
			{Source: "/tmp/data", Path: "/mnt/data"},
		},
		Networks: []config.NetworkConfig{
			{Name: "eth0", Parent: "lxdbr0"},
		},
	}

	// Create path
	p, err := rec.Compute(conf, nil, false)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	post := p.Steps[0].InstancesPost
	mountDev, ok := post.Devices["mount0"]
	if !ok {
		t.Fatalf("expected mount0 device in create payload, got %v", post.Devices)
	}
	if mountDev["shift"] != "true" {
		t.Errorf("expected shift=true on mount0, got %q", mountDev["shift"])
	}
	nicDev, ok := post.Devices["eth0"]
	if !ok {
		t.Fatalf("expected eth0 device in create payload, got %v", post.Devices)
	}
	if nicDev["nictype"] != "bridged" {
		t.Errorf("expected nictype=bridged on eth0, got %q", nicDev["nictype"])
	}

	// Update path (live image matches manifest, but user differs -> update)
	live := map[string]*plan.InstanceSnapshot{
		"box1": {
			Name:   "box1",
			Status: "Running",
			Config: map[string]string{
				"image.os":      "ubuntu",
				"image.release": "24.04",
				"user.lxm.user": "olduser",
			},
			Devices: map[string]map[string]string{
				"root":   {"type": "disk", "path": "/", "pool": "default"},
				"mount0": {"type": "disk", "source": "/tmp/data", "path": "/mnt/data", "shift": "true"},
				"eth0":   {"type": "nic", "name": "eth0", "nictype": "bridged", "parent": "lxdbr0"},
			},
		},
	}
	p2, err := rec.Compute(conf, live, false)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if p2.Steps[0].Action != "update" {
		t.Fatalf("expected action 'update', got %q", p2.Steps[0].Action)
	}
	put := p2.Steps[0].InstancePut
	putMount := put.Devices["mount0"]
	if putMount["shift"] != "true" {
		t.Errorf("expected shift=true on mount0 in update payload, got %q", putMount["shift"])
	}
	putNic := put.Devices["eth0"]
	if putNic["nictype"] != "bridged" {
		t.Errorf("expected nictype=bridged on eth0 in update payload, got %q", putNic["nictype"])
	}
}

// B5: network-config must be emitted as user.network-config on create and
// config update, and a drift must trigger an update (not silently inert).
func TestReconciler_Compute_NetworkConfig_EmittedAndDiffed(t *testing.T) {
	rec := plan.NewReconciler()

	const netCfg = "version: 2\nethernets:\n  eth0:\n    dhcp4: true\n"
	conf := &config.Config{
		Name:          "box1",
		Image:         "ubuntu:24.04",
		User:          "ubuntu",
		NetworkConfig: netCfg,
	}

	// Create path: user.network-config present in the create payload.
	p, err := rec.Compute(conf, nil, false)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got := p.Steps[0].InstancesPost.Config["user.network-config"]; got != netCfg {
		t.Errorf("expected user.network-config on create payload, got %q", got)
	}

	// Update path: live has a different network-config -> update with the new value.
	live := map[string]*plan.InstanceSnapshot{
		"box1": {
			Name:   "box1",
			Status: "Running",
			Config: map[string]string{
				"image.os":            "ubuntu",
				"image.release":       "24.04",
				"user.lxm.user":       "ubuntu",
				"user.network-config": "version: 2\nethernets:\n  eth0:\n    dhcp4: false\n",
			},
		},
	}
	p2, err := rec.Compute(conf, live, false)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if p2.Steps[0].Action != "update" {
		t.Fatalf("expected action 'update' on network-config drift, got %q", p2.Steps[0].Action)
	}
	if got := p2.Steps[0].InstancePut.Config["user.network-config"]; got != netCfg {
		t.Errorf("expected updated user.network-config in update payload, got %q", got)
	}

	// Noop path: live network-config already matches.
	liveMatch := map[string]*plan.InstanceSnapshot{
		"box1": {
			Name:   "box1",
			Status: "Running",
			Config: map[string]string{
				"image.os":            "ubuntu",
				"image.release":       "24.04",
				"user.lxm.user":       "ubuntu",
				"user.network-config": netCfg,
			},
		},
	}
	p3, err := rec.Compute(conf, liveMatch, false)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if p3.Steps[0].Action != "noop" {
		t.Errorf("expected action 'noop' when network-config matches, got %q", p3.Steps[0].Action)
	}

	// Drop path: a manifest without network-config, applied to a container
	// with a stale user.network-config, must clear the key on the next update.
	confNoNet := &config.Config{
		Name:  "box1",
		Image: "ubuntu:24.04",
		User:  "newuser", // different user forces an update
	}
	liveStale := map[string]*plan.InstanceSnapshot{
		"box1": {
			Name:   "box1",
			Status: "Running",
			Config: map[string]string{
				"image.os":            "ubuntu",
				"image.release":       "24.04",
				"user.lxm.user":       "ubuntu",
				"user.network-config": "version: 2\nethernets:\n  eth0:\n    dhcp4: false\n",
			},
		},
	}
	p4, err := rec.Compute(confNoNet, liveStale, false)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if p4.Steps[0].Action != "update" {
		t.Fatalf("expected action 'update' for user drift, got %q", p4.Steps[0].Action)
	}
	if _, exists := p4.Steps[0].InstancePut.Config["user.network-config"]; exists {
		t.Errorf("expected stale user.network-config to be cleared in update payload, got %q",
			p4.Steps[0].InstancePut.Config["user.network-config"])
	}
}

func TestReconciler_Compute_Recreate_ImageChange(t *testing.T) {
	rec := plan.NewReconciler()

	conf := &config.Config{
		Name:  "box1",
		Image: "ubuntu:24.04",
		User:  "ubuntu",
		Recipes: []config.RecipeGroup{
			{RunAs: "root", Scripts: []string{"setup.sh"}},
		},
	}

	live := map[string]*plan.InstanceSnapshot{
		"box1": {
			Name:         "box1",
			Status:       "Running",
			HasSnapshots: true,
			Config: map[string]string{
				"image.os":      "ubuntu",
				"image.release": "22.04",
			},
		},
	}

	// Without LXD rebuild extension -> RebuildFallback = true
	p, err := rec.Compute(conf, live, false)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	step := p.Steps[0]
	if step.Action != "recreate" {
		t.Errorf("expected action 'recreate', got %q", step.Action)
	}
	if !step.RebuildFallback {
		t.Errorf("expected RebuildFallback=true when rebuild extension missing")
	}
	if !step.PurgeSnapshots {
		t.Errorf("expected PurgeSnapshots=true when instance has snapshots")
	}

	// With LXD rebuild extension -> RebuildFallback = false
	p2, _ := rec.Compute(conf, live, true)
	if p2.Steps[0].RebuildFallback {
		t.Errorf("expected RebuildFallback=false when rebuild extension present")
	}
}

// Regression (lxm_mount_bug.md §6.1): config.Mounts is now the named Mounts
// type; reflect.DeepEqual against the []config.Mount live list must not
// produce spurious mount diffs when the live devices match the manifest.
func TestReconciler_Compute_NoSpuriousMountDiff(t *testing.T) {
	rec := plan.NewReconciler()

	conf := &config.Config{
		Name:  "box1",
		Image: "ubuntu:24.04",
		User:  "ubuntu",
		Mounts: []config.Mount{
			{Source: "/tmp/data", Path: "/mnt/data"},
		},
	}

	live := map[string]*plan.InstanceSnapshot{
		"box1": {
			Name:   "box1",
			Status: "Running",
			Config: map[string]string{
				"image.os":      "ubuntu",
				"image.release": "24.04",
				"user.lxm.user": "ubuntu",
			},
			Devices: map[string]map[string]string{
				"root":   {"type": "disk", "source": "ubuntu-24.04", "path": "/"},
				"mount0": {"type": "disk", "source": "/tmp/data", "path": "/mnt/data"},
			},
		},
	}

	p, err := rec.Compute(conf, live, false)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if p.Steps[0].Action != "noop" {
		t.Fatalf("expected action 'noop' when mounts match, got %q (diffs=%+v)", p.Steps[0].Action, p.Steps[0].Diff)
	}
}

func TestReconciler_Compute_PowerStateStop(t *testing.T) {
	rec := plan.NewReconciler()

	conf := &config.Config{
		Name:  "box1",
		Image: "ubuntu:24.04",
		State: "stopped",
	}

	live := map[string]*plan.InstanceSnapshot{
		"box1": {
			Name:   "box1",
			Status: "Running",
			Config: map[string]string{
				"image.os":      "ubuntu",
				"image.release": "24.04",
			},
		},
	}

	p, err := rec.Compute(conf, live, false)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if p.Steps[0].Action != "stop" {
		t.Errorf("expected action 'stop', got %q", p.Steps[0].Action)
	}
}

func TestReconciler_Compute_CreateStoppedContainer_SetsStopPowerTransition(t *testing.T) {
	rec := plan.NewReconciler()

	conf := &config.Config{
		Name:  "box1",
		Image: "ubuntu:22.04",
		State: "stopped",
	}

	p, err := rec.Compute(conf, nil, false)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if p.Steps[0].Action != "create" {
		t.Errorf("expected action 'create', got %q", p.Steps[0].Action)
	}
	if p.Steps[0].PowerTransition != "stop" {
		t.Errorf("expected PowerTransition 'stop' for state: stopped create, got %q", p.Steps[0].PowerTransition)
	}
}

func TestReconciler_Compute_RecreatePowerTransitions(t *testing.T) {
	rec := plan.NewReconciler()

	confRunning := &config.Config{
		Name:  "box1",
		Image: "ubuntu:24.04",
		State: "running",
	}

	confStopped := &config.Config{
		Name:  "box2",
		Image: "ubuntu:24.04",
		State: "stopped",
	}

	liveRunning := map[string]*plan.InstanceSnapshot{
		"box1": {
			Name:   "box1",
			Status: "Running",
			Config: map[string]string{"image.os": "ubuntu", "image.release": "22.04"},
		},
	}
	liveStopped := map[string]*plan.InstanceSnapshot{
		"box2": {
			Name:   "box2",
			Status: "Stopped",
			Config: map[string]string{"image.os": "ubuntu", "image.release": "22.04"},
		},
	}

	pRun, err := rec.Compute(confRunning, liveRunning, false)
	if err != nil || pRun.Steps[0].PowerTransition != "start" {
		t.Errorf("expected PowerTransition 'start' for running recreate, got %q", pRun.Steps[0].PowerTransition)
	}

	pStop, err := rec.Compute(confStopped, liveStopped, false)
	if err != nil || pStop.Steps[0].PowerTransition != "stop" {
		t.Errorf("expected PowerTransition 'stop' for stopped recreate, got %q", pStop.Steps[0].PowerTransition)
	}
}

func TestReconciler_Compute_ImageDescriptionFallback(t *testing.T) {
	rec := plan.NewReconciler()

	conf := &config.Config{
		Name:  "box1",
		Image: "Ubuntu 22.04 LTS server",
	}

	// Live snapshot with image.description instead of image.os
	live := map[string]*plan.InstanceSnapshot{
		"box1": {
			Name:   "box1",
			Status: "Running",
			Config: map[string]string{
				"image.description": "Ubuntu 22.04 LTS server",
			},
		},
	}

	p, err := rec.Compute(conf, live, false)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if p.Steps[0].Action != "noop" {
		t.Errorf("expected action 'noop' when image description matches, got %q", p.Steps[0].Action)
	}
}

// B4: a container created from alias ubuntu:22.04 reports its live image as
// os:release (ubuntu:jammy, the codename). Re-planning must be a no-op, not a
// recreate — and an alias for a different OS must still recreate.
func TestReconciler_Compute_AliasImage_ReplanNoop(t *testing.T) {
	rec := plan.NewReconciler()

	conf := &config.Config{
		Name:  "box1",
		Image: "ubuntu:22.04",
		User:  "ubuntu",
	}

	live := map[string]*plan.InstanceSnapshot{
		"box1": {
			Name:   "box1",
			Status: "Running",
			Config: map[string]string{
				"image.os":            "ubuntu",
				"image.release":       "jammy",
				"image.version":       "22.04",
				"image.description":   "ubuntu 22.04 LTS amd64 (release) (20240111_07:42)",
				"volatile.base_image": "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855",
				"user.lxm.user":       "ubuntu",
			},
		},
	}

	p, err := rec.Compute(conf, live, false)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if p.Steps[0].Action != "noop" {
		t.Errorf("expected action 'noop' for alias-created container on re-plan, got %q", p.Steps[0].Action)
	}

	// Same alias against a live container from a different OS must recreate.
	liveAlpine := map[string]*plan.InstanceSnapshot{
		"box1": {
			Name:   "box1",
			Status: "Running",
			Config: map[string]string{
				"image.os":      "alpine",
				"image.release": "3.19",
				"image.version": "3.19.1",
			},
		},
	}
	p2, err := rec.Compute(conf, liveAlpine, false)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if p2.Steps[0].Action != "recreate" {
		t.Errorf("expected action 'recreate' for image OS mismatch, got %q", p2.Steps[0].Action)
	}
}

// B4: fingerprint manifests match the live volatile.base_image, so re-planning
// a container created from a fingerprint is a no-op.
func TestReconciler_Compute_FingerprintImage_ReplanNoop(t *testing.T) {
	rec := plan.NewReconciler()

	const fp = "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"
	conf := &config.Config{
		Name:  "box1",
		Image: fp,
		User:  "ubuntu",
	}

	live := map[string]*plan.InstanceSnapshot{
		"box1": {
			Name:   "box1",
			Status: "Running",
			Config: map[string]string{
				"volatile.base_image": fp,
				"user.lxm.user":       "ubuntu",
			},
		},
	}

	p, err := rec.Compute(conf, live, false)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if p.Steps[0].Action != "noop" {
		t.Errorf("expected action 'noop' for fingerprint-created container on re-plan, got %q", p.Steps[0].Action)
	}

	// A fingerprint prefix (12 hex chars, per the #Fingerprint schema) must
	// match the same live instance.
	confPrefix := &config.Config{
		Name:  "box1",
		Image: fp[:12],
		User:  "ubuntu",
	}
	pPrefix, err := rec.Compute(confPrefix, live, false)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if pPrefix.Steps[0].Action != "noop" {
		t.Errorf("expected action 'noop' for fingerprint-prefix re-plan, got %q", pPrefix.Steps[0].Action)
	}
}

func TestReconciler_Compute_NilConfigSnapshot(t *testing.T) {
	rec := plan.NewReconciler()

	conf := &config.Config{
		Name:  "box1",
		Image: "ubuntu:24.04",
	}

	live := map[string]*plan.InstanceSnapshot{
		"box1": {
			Name:   "box1",
			Status: "Running",
		},
	}

	p, err := rec.Compute(conf, live, false)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if p.Steps[0].Action != "update" && p.Steps[0].Action != "noop" {
		t.Errorf("unexpected action: %q", p.Steps[0].Action)
	}
}

func TestPlan_ToJSON(t *testing.T) {
	p := &plan.Plan{
		Schema: "lxm/plan/v1",
		Steps: []plan.Step{
			{Container: "box1", Action: "create", Changed: true},
		},
	}

	jsonStr, err := p.ToJSON()
	if err != nil {
		t.Fatalf("unexpected error serializing plan to JSON: %v", err)
	}
	if jsonStr == "" {
		t.Errorf("expected non-empty JSON output")
	}
}

func TestReconciler_Compute_CloudInitError_ReturnsError(t *testing.T) {
	rec := plan.NewReconciler()

	// CloudInit set to non-existent file path
	conf := &config.Config{
		Name:      "box1",
		Image:     "ubuntu:22.04",
		CloudInit: "nonexistent_file_path_12345.yaml",
	}

	_, err := rec.Compute(conf, nil, false)
	if err == nil {
		t.Fatalf("expected error when CloudInit file resolution fails")
	}

	live := map[string]*plan.InstanceSnapshot{
		"box1": {
			Name:   "box1",
			Status: "Running",
			Config: map[string]string{"image.os": "ubuntu", "image.release": "22.04"},
		},
	}
	confUpdate := &config.Config{
		Name:      "box1",
		Image:     "ubuntu:22.04",
		User:      "newuser",
		CloudInit: "nonexistent_file_path_12345.yaml",
	}
	_, err = rec.Compute(confUpdate, live, false)
	if err == nil {
		t.Fatalf("expected error when CloudInit file resolution fails on existing container update")
	}
}

func TestReconciler_Compute_MountsAndNetworksDiff(t *testing.T) {
	rec := plan.NewReconciler()

	conf := &config.Config{
		Name:  "box1",
		Image: "ubuntu:24.04",
		Mounts: []config.Mount{
			{Source: "/host/data", Path: "/mnt/data", Recursive: true, Readonly: true},
		},
		Networks: []config.NetworkConfig{
			{Name: "eth0", IPv4: "10.0.0.20", Parent: "lxdbr0"},
		},
	}

	// Live has different mount and network
	live := map[string]*plan.InstanceSnapshot{
		"box1": {
			Name:   "box1",
			Status: "Running",
			Config: map[string]string{"image.os": "ubuntu", "image.release": "24.04"},
			Devices: map[string]map[string]string{
				"mount0": {"type": "disk", "source": "/host/old", "path": "/mnt/data"},
				"eth0":   {"type": "nic", "ipv4.address": "10.0.0.10", "parent": "lxdbr0"},
			},
		},
	}

	p, err := rec.Compute(conf, live, false)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if p.Steps[0].Action != "update" {
		t.Fatalf("expected action 'update', got %q", p.Steps[0].Action)
	}
}

func TestReconciler_Compute_RecipeYAML(t *testing.T) {
	tmpDir := t.TempDir()
	recipeFile := filepath.Join(tmpDir, "test_recipe.yaml")
	_ = os.WriteFile(recipeFile, []byte("schema: lxm/recipe/v1\nname: custom-recipe\nrun_as: appuser\nscripts: [setup.sh]\n"), 0644)

	rec := plan.NewReconciler()
	conf := &config.Config{
		Name:          "box1",
		Image:         "ubuntu:24.04",
		ConfigBaseDir: tmpDir,
		Recipes: []config.RecipeGroup{
			{RunAs: "overridden-user", Scripts: []string{"test_recipe.yaml"}},
		},
	}

	p, err := rec.Compute(conf, nil, false)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(p.Steps[0].Recipes) != 1 {
		t.Fatalf("expected 1 recipe step")
	}
	if p.Steps[0].Recipes[0].Name != "custom-recipe" {
		t.Errorf("expected recipe name 'custom-recipe', got %q", p.Steps[0].Recipes[0].Name)
	}
}

func TestReconciler_Compute_AllSummaryActions(t *testing.T) {
	rec := plan.NewReconciler()

	// Test recreate
	confRecreate := &config.Config{Name: "box1", Image: "ubuntu:24.04"}
	liveRecreate := map[string]*plan.InstanceSnapshot{
		"box1": {Name: "box1", Status: "Running", Config: map[string]string{"image.os": "ubuntu", "image.release": "22.04"}},
	}
	p1, _ := rec.Compute(confRecreate, liveRecreate, false)
	if p1.Summary.Recreate != 1 {
		t.Errorf("expected summary Recreate=1, got %d", p1.Summary.Recreate)
	}

	// Test delete
	confDelete := &config.Config{Name: "box1", Status: "absent"}
	p2, _ := rec.Compute(confDelete, liveRecreate, false)
	if p2.Summary.Delete != 1 {
		t.Errorf("expected summary Delete=1, got %d", p2.Summary.Delete)
	}

	// Test start / stop
	confStart := &config.Config{Name: "box1", Image: "ubuntu:22.04", State: "running"}
	liveStopped := map[string]*plan.InstanceSnapshot{
		"box1": {Name: "box1", Status: "Stopped", Config: map[string]string{"image.os": "ubuntu", "image.release": "22.04"}},
	}
	p3, _ := rec.Compute(confStart, liveStopped, false)
	if p3.Summary.Start != 1 {
		t.Errorf("expected summary Start=1, got %d", p3.Summary.Start)
	}
}

func TestReconciler_Compute_RecreateBuildPayloadError(t *testing.T) {
	rec := plan.NewReconciler()

	conf := &config.Config{
		Name:      "box1",
		Image:     "ubuntu:24.04",
		CloudInit: "invalid_cloud_init_file_9999.yaml",
	}

	live := map[string]*plan.InstanceSnapshot{
		"box1": {
			Name:   "box1",
			Status: "Running",
			Config: map[string]string{"image.os": "ubuntu", "image.release": "22.04"},
		},
	}

	_, err := rec.Compute(conf, live, false)
	if err == nil {
		t.Fatalf("expected error when building recreate payload fails due to cloud-init error")
	}
}

func TestReconciler_Compute_EmptyGroupsClearsKey(t *testing.T) {
	rec := plan.NewReconciler()

	conf := &config.Config{
		Name:   "box1",
		Image:  "ubuntu:22.04",
		Groups: []string{},
	}

	live := map[string]*plan.InstanceSnapshot{
		"box1": {
			Name:   "box1",
			Status: "Running",
			Config: map[string]string{
				"image.os":        "ubuntu",
				"image.release":   "22.04",
				"user.lxm.groups": "oldgroup",
			},
		},
	}

	p, err := rec.Compute(conf, live, false)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if _, ok := p.Steps[0].InstancePut.Config["user.lxm.groups"]; ok {
		t.Errorf("expected user.lxm.groups to be removed when manifest groups is empty")
	}
}

func TestReconciler_Compute_UpdateWithPowerStateTransition(t *testing.T) {
	rec := plan.NewReconciler()

	conf := &config.Config{
		Name:  "box1",
		Image: "ubuntu:24.04",
		User:  "newuser",
		State: "stopped",
	}

	live := map[string]*plan.InstanceSnapshot{
		"box1": {
			Name:   "box1",
			Status: "Running",
			Config: map[string]string{"image.os": "ubuntu", "image.release": "24.04", "user.lxm.user": "olduser"},
		},
	}

	p, err := rec.Compute(conf, live, false)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if p.Steps[0].Action != "update" {
		t.Errorf("expected action 'update', got %q", p.Steps[0].Action)
	}
	if p.Steps[0].PowerTransition != "stop" {
		t.Errorf("expected PowerTransition 'stop', got %q", p.Steps[0].PowerTransition)
	}

	confStart := &config.Config{
		Name:  "box1",
		Image: "ubuntu:24.04",
		User:  "newuser",
		State: "running",
	}
	liveStopped := map[string]*plan.InstanceSnapshot{
		"box1": {
			Name:   "box1",
			Status: "Stopped",
			Config: map[string]string{"image.os": "ubuntu", "image.release": "24.04", "user.lxm.user": "olduser"},
		},
	}
	pStart, err := rec.Compute(confStart, liveStopped, false)
	if err != nil || pStart.Steps[0].PowerTransition != "start" {
		t.Errorf("expected PowerTransition 'start', got %q", pStart.Steps[0].PowerTransition)
	}
}

func TestReconciler_NilManifestAndRebuildExtension(t *testing.T) {
	rec := plan.NewReconciler()

	// Nil manifest error
	if _, err := rec.Compute(nil, nil, false); err == nil {
		t.Errorf("expected error for nil manifest")
	}

	// Recreate with rebuild extension vs fallback
	confRecreate := &config.Config{
		Name:  "box1",
		Image: "ubuntu:24.04",
	}
	liveSnap := map[string]*plan.InstanceSnapshot{
		"box1": {
			Name:         "box1",
			Status:       "Running",
			Config:       map[string]string{"image.os": "ubuntu", "image.release": "22.04"},
			HasSnapshots: true,
		},
	}

	// hasRebuildExt = true
	pExt, err := rec.Compute(confRecreate, liveSnap, true)
	if err != nil {
		t.Fatalf("Compute failed: %v", err)
	}
	if pExt.Steps[0].Action != "recreate" || pExt.Steps[0].RebuildFallback {
		t.Errorf("expected recreate action with RebuildFallback false, got %v", pExt.Steps[0])
	}
	if !pExt.Steps[0].PurgeSnapshots {
		t.Errorf("expected PurgeSnapshots true when live container has snapshots")
	}

	// hasRebuildExt = false
	pFallback, err := rec.Compute(confRecreate, liveSnap, false)
	if err != nil {
		t.Fatalf("Compute failed: %v", err)
	}
	if !pFallback.Steps[0].RebuildFallback {
		t.Errorf("expected RebuildFallback true when hasRebuildExt is false")
	}

	// Live image description fallback
	liveDesc := map[string]*plan.InstanceSnapshot{
		"box1": {
			Name:   "box1",
			Status: "Running",
			Config: map[string]string{"image.description": "ubuntu 24.04 lts"},
		},
	}
	pDesc, err := rec.Compute(confRecreate, liveDesc, true)
	if err != nil {
		t.Fatalf("Compute failed: %v", err)
	}
	if pDesc.Steps[0].Action != "noop" {
		t.Errorf("expected image match via description to yield action noop, got %q", pDesc.Steps[0].Action)
	}

	// YAML recipe file loading in buildRecipeSteps
	dir := t.TempDir()
	recipeMetaFile := filepath.Join(dir, "setup.yaml")
	_ = os.WriteFile(recipeMetaFile, []byte("schema: lxm/recipe/v1\nname: setup-tool\nrun_as: ubuntu\nscripts:\n  - setup.sh\n"), 0644)
	confYAMLRecipe := &config.Config{
		Name:          "box1",
		Image:         "ubuntu:24.04",
		ConfigBaseDir: dir,
		Recipes: []config.RecipeGroup{
			{RunAs: "root", Scripts: []string{"setup.yaml"}},
		},
	}
	pYAMLRecipe, err := rec.Compute(confYAMLRecipe, nil, false)
	if err != nil {
		t.Fatalf("Compute failed: %v", err)
	}
	if len(pYAMLRecipe.Steps[0].Recipes) != 1 || pYAMLRecipe.Steps[0].Recipes[0].Name != "setup-tool" {
		t.Errorf("expected YAML recipe metadata to be loaded into step recipes, got %v", pYAMLRecipe.Steps[0].Recipes)
	}

	// Plan.ToJSON test
	pJSON, err := pYAMLRecipe.ToJSON()
	if err != nil || !strings.Contains(pJSON, `"schema": "lxm/plan/v1"`) {
		t.Fatalf("Plan.ToJSON failed: %v, output: %s", err, pJSON)
	}

	// Pure power state change (stop action)
	confStop := &config.Config{
		Name:  "box1",
		Image: "ubuntu:24.04",
		User:  "ubuntu",
		State: "stopped",
	}
	liveRunning := map[string]*plan.InstanceSnapshot{
		"box1": {
			Name:   "box1",
			Status: "Running",
			Config: map[string]string{"image.os": "ubuntu", "image.release": "24.04", "user.lxm.user": "ubuntu"},
		},
	}
	pStop, err := rec.Compute(confStop, liveRunning, false)
	if err != nil || pStop.Steps[0].Action != "stop" {
		t.Errorf("expected action 'stop' for pure power state change, got %v, err %v", pStop, err)
	}

	// Build error handling for cloud-init error in create
	confBadCloudInit := &config.Config{
		Name:             "box1",
		Image:            "ubuntu:24.04",
		CloudInitInclude: []string{"nonexistent_file_9999.yaml"},
	}
	if _, err := rec.Compute(confBadCloudInit, nil, false); err == nil {
		t.Errorf("expected error when cloud-init resolution fails during create")
	}

	// Mounts options & network properties in buildInstancesPost & buildInstancePut
	confFullProps := &config.Config{
		Name:   "full-box",
		Image:  "ubuntu:24.04",
		User:   "ubuntu",
		Groups: []string{"sudo", "docker"},
		Mounts: []config.Mount{
			{Source: "/tmp/src", Path: "/mnt/dst", Recursive: true, Readonly: true},
		},
		Networks: []config.NetworkConfig{
			{IPv4: "10.0.0.50", Parent: "custombr0"},
		},
	}
	pFull, err := rec.Compute(confFullProps, nil, false)
	if err != nil {
		t.Fatalf("Compute failed: %v", err)
	}
	devs := pFull.Steps[0].InstancesPost.Devices
	if devs["mount0"]["recursive"] != "true" || devs["mount0"]["readonly"] != "true" {
		t.Errorf("expected recursive and readonly mount options in InstancesPost, got %v", devs["mount0"])
	}
	if devs["eth0"]["ipv4.address"] != "10.0.0.50" || devs["eth0"]["parent"] != "custombr0" {
		t.Errorf("expected network properties in InstancesPost, got %v", devs["eth0"])
	}
}

func TestReconciler_Compute_VM_Create(t *testing.T) {
	rec := plan.NewReconciler()
	shiftFalse := false

	conf := &config.Config{
		Name:  "vm1",
		Type:  "virtual-machine",
		Image: "ubuntu:24.04",
		User:  "ubuntu",
		Limits: &config.LimitsConfig{
			CPU:    "4",
			Memory: "8GiB",
			Disk:   "50GiB",
		},
		VM: &config.VMConfig{
			BootMode:  "uefi-nosecureboot",
			Hugepages: true,
			RawQEMU:   "-cpu host",
		},
		Mounts: []config.Mount{
			{Source: "/tmp/data", Path: "/mnt/data", Shift: &shiftFalse},
		},
	}

	p, err := rec.Compute(conf, nil, false)
	if err != nil {
		t.Fatalf("Compute failed: %v", err)
	}

	if len(p.Steps) != 1 {
		t.Fatalf("expected 1 step, got %d", len(p.Steps))
	}
	step := p.Steps[0]
	if step.Action != "create" {
		t.Errorf("expected create action, got %q", step.Action)
	}
	if step.InstancesPost.Type != "virtual-machine" {
		t.Errorf("expected type virtual-machine, got %q", step.InstancesPost.Type)
	}
	if step.InstancesPost.Config["limits.cpu"] != "4" {
		t.Errorf("expected limits.cpu 4, got %q", step.InstancesPost.Config["limits.cpu"])
	}
	if step.InstancesPost.Config["limits.memory"] != "8GiB" {
		t.Errorf("expected limits.memory 8GiB, got %q", step.InstancesPost.Config["limits.memory"])
	}
	if step.InstancesPost.Config["boot.mode"] != "uefi-nosecureboot" {
		t.Errorf("expected boot.mode uefi-nosecureboot, got %q", step.InstancesPost.Config["boot.mode"])
	}
	if step.InstancesPost.Config["limits.memory.hugepages"] != "true" {
		t.Errorf("expected hugepages true, got %q", step.InstancesPost.Config["limits.memory.hugepages"])
	}
	if step.InstancesPost.Devices["root"]["size"] != "50GiB" || step.InstancesPost.Devices["root"]["pool"] != "default" {
		t.Errorf("expected root disk 50GiB on default pool, got %+v", step.InstancesPost.Devices["root"])
	}
	if step.InstancesPost.Devices["mount0"]["shift"] != "false" {
		t.Errorf("expected mount shift false, got %+v", step.InstancesPost.Devices["mount0"])
	}
	if len(p.Warnings) != 1 || !strings.Contains(p.Warnings[0], "raw.qemu") {
		t.Errorf("expected raw_qemu warning, got %+v", p.Warnings)
	}
}

func TestReconciler_Compute_TypeChange_ForcesRebuildFallback(t *testing.T) {
	rec := plan.NewReconciler()

	conf := &config.Config{
		Name:  "box1",
		Type:  "virtual-machine",
		Image: "ubuntu:24.04",
		User:  "ubuntu",
	}

	live := map[string]*plan.InstanceSnapshot{
		"box1": {
			Name:   "box1",
			Type:   "container",
			Status: "Running",
			Config: map[string]string{
				"image.os":      "ubuntu",
				"image.release": "24.04",
				"user.lxm.user": "ubuntu",
			},
		},
	}

	p, err := rec.Compute(conf, live, true) // hasRebuildExt = true
	if err != nil {
		t.Fatalf("Compute failed: %v", err)
	}

	step := p.Steps[0]
	if step.Action != "recreate" {
		t.Errorf("expected action recreate, got %q", step.Action)
	}
	if !step.RebuildFallback {
		t.Errorf("expected RebuildFallback=true for type change even when hasRebuildExt is true")
	}
}

func TestReconciler_Compute_BootMode_RunningRestart(t *testing.T) {
	rec := plan.NewReconciler()

	conf := &config.Config{
		Name:  "vm1",
		Type:  "virtual-machine",
		Image: "ubuntu:24.04",
		User:  "ubuntu",
		VM: &config.VMConfig{
			BootMode: "uefi-nosecureboot",
		},
	}

	live := map[string]*plan.InstanceSnapshot{
		"vm1": {
			Name:   "vm1",
			Type:   "virtual-machine",
			Status: "Running",
			Config: map[string]string{
				"image.os":      "ubuntu",
				"image.release": "24.04",
				"user.lxm.user": "ubuntu",
				"boot.mode":     "uefi-secureboot",
			},
		},
	}

	p, err := rec.Compute(conf, live, false)
	if err != nil {
		t.Fatalf("Compute failed: %v", err)
	}

	step := p.Steps[0]
	if step.Action != "update" {
		t.Errorf("expected action update, got %q", step.Action)
	}
	if step.PowerTransition != "restart" {
		t.Errorf("expected PowerTransition restart for boot.mode change on running VM, got %q", step.PowerTransition)
	}
}

func TestReconciler_Compute_DiskShrink_Recreate(t *testing.T) {
	rec := plan.NewReconciler()

	conf := &config.Config{
		Name:  "box1",
		Image: "ubuntu:24.04",
		User:  "ubuntu",
		Limits: &config.LimitsConfig{
			Disk: "20GiB",
		},
	}

	live := map[string]*plan.InstanceSnapshot{
		"box1": {
			Name:   "box1",
			Type:   "container",
			Status: "Running",
			Config: map[string]string{
				"image.os":      "ubuntu",
				"image.release": "24.04",
				"user.lxm.user": "ubuntu",
			},
			ExpandedDevices: map[string]map[string]string{
				"root": {"type": "disk", "path": "/", "size": "50GiB"},
			},
		},
	}

	p, err := rec.Compute(conf, live, false)
	if err != nil {
		t.Fatalf("Compute failed: %v", err)
	}

	step := p.Steps[0]
	if step.Action != "recreate" {
		t.Errorf("expected recreate action for disk shrink (50GiB -> 20GiB), got %q", step.Action)
	}
}

func TestReconciler_Compute_VM_HugepagesAndRawQEMU_DiffAndRestart(t *testing.T) {
	rec := plan.NewReconciler()

	conf := &config.Config{
		Name:  "vm1",
		Type:  "virtual-machine",
		Image: "ubuntu:24.04",
		User:  "ubuntu",
		VM: &config.VMConfig{
			Hugepages: true,
			RawQEMU:   "-cpu host,kvm=off",
		},
	}

	live := map[string]*plan.InstanceSnapshot{
		"vm1": {
			Name:   "vm1",
			Type:   "virtual-machine",
			Status: "Running",
			Config: map[string]string{
				"image.os":      "ubuntu",
				"image.release": "24.04",
				"user.lxm.user": "ubuntu",
				"boot.mode":     "uefi-secureboot",
			},
		},
	}

	p, err := rec.Compute(conf, live, false)
	if err != nil {
		t.Fatalf("Compute failed: %v", err)
	}

	step := p.Steps[0]
	if step.Action != "update" {
		t.Fatalf("expected action update, got %q", step.Action)
	}
	if step.PowerTransition != "restart" {
		t.Errorf("expected PowerTransition restart for VM hypervisor config changes on running instance, got %q", step.PowerTransition)
	}

	hasHugepagesDiff := false
	hasRawQEMUDiff := false
	for _, d := range step.Diff {
		if d.Field == "limits.memory.hugepages" && d.New == "true" {
			hasHugepagesDiff = true
		}
		if d.Field == "raw.qemu" && d.New == "-cpu host,kvm=off" {
			hasRawQEMUDiff = true
		}
	}
	if !hasHugepagesDiff {
		t.Errorf("expected diff for limits.memory.hugepages, got diffs: %+v", step.Diff)
	}
	if !hasRawQEMUDiff {
		t.Errorf("expected diff for raw.qemu, got diffs: %+v", step.Diff)
	}
}
