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

	"github.com/aiyor/lxm/internal/output"
	"github.com/aiyor/lxm/internal/provider"
	"github.com/aiyor/lxm/internal/provider/fake"
)

func writeTestFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatalf("writing %s: %v", path, err)
	}
}

// TestRun_VSwitches_PlanApply exercises the full vswitches:/network_policy:
// path through the CLI: base-file inheritance, fleet dedup, plan network
// steps, apply phase ordering, and the JSON envelope's additive fields.
func TestRun_VSwitches_PlanApply(t *testing.T) {
	driver := fake.New()
	driver.Extensions["network_acl"] = true

	tmpDir := t.TempDir()
	writeTestFile(t, filepath.Join(tmpDir, "_base.yaml"), `schema: lxm/config/v2
base: true
image: ubuntu:22.04
vswitches:
  - name: vmbr0
    ipv4: 10.30.0.1/24
    group: vms
  - name: svcbr0
    ipv4: 10.50.0.1/24
    group: services
network_policy:
  allow:
    - from: vms
      to: services
`)
	writeTestFile(t, filepath.Join(tmpDir, "web-a.yaml"), `schema: lxm/config/v2
include: [_base.yaml]
name: web-a
networks:
  - name: eth0
    parent: vmbr0
`)

	// 1. plan --format json carries network_steps + the network summary.
	var stdout, stderr bytes.Buffer
	code := run([]string{"plan", tmpDir, "--format", "json"}, &stdout, &stderr, driver)
	if code != 0 {
		t.Fatalf("plan returned %d, want 0. Stderr: %s", code, stderr.String())
	}
	var env output.Envelope
	if err := json.Unmarshal(stdout.Bytes(), &env); err != nil {
		t.Fatalf("unmarshal envelope: %v", err)
	}
	netSteps, ok := env.Plan.NetworkSteps.([]interface{})
	if !ok || len(netSteps) == 0 {
		t.Fatalf("expected network_steps in plan envelope, got %T", env.Plan.NetworkSteps)
	}

	// 2. apply creates ACLs + vswitches before instances, records network_results.
	stdout.Reset()
	stderr.Reset()
	code = run([]string{"apply", tmpDir, "--format", "json"}, &stdout, &stderr, driver)
	if code != 0 {
		t.Fatalf("apply returned %d, want 0. Stderr: %s", code, stderr.String())
	}
	env = output.Envelope{}
	if err := json.Unmarshal(stdout.Bytes(), &env); err != nil {
		t.Fatalf("unmarshal envelope: %v", err)
	}
	if len(env.NetworkResults) == 0 {
		t.Fatalf("expected network_results in apply envelope")
	}
	for _, nr := range env.NetworkResults {
		if !nr.OK {
			t.Fatalf("network result failed: %+v", nr)
		}
	}

	if _, ok := driver.Networks["vmbr0"]; !ok {
		t.Fatalf("vmbr0 not created")
	}
	if _, ok := driver.Networks["svcbr0"]; !ok {
		t.Fatalf("svcbr0 not created")
	}
	if _, ok := driver.NetworkACLs["lxm-vmbr0"]; !ok {
		t.Fatalf("lxm-vmbr0 ACL not created")
	}
	if _, _, err := driver.GetInstance(context.Background(), "web-a"); err != nil {
		t.Fatalf("web-a instance not created: %v", err)
	}

	// 3. Second apply is a no-op (idempotent) — no network steps.
	stdout.Reset()
	stderr.Reset()
	code = run([]string{"apply", tmpDir, "--format", "json"}, &stdout, &stderr, driver)
	if code != 0 {
		t.Fatalf("second apply returned %d, want 0. Stderr: %s", code, stderr.String())
	}
	env = output.Envelope{}
	if err := json.Unmarshal(stdout.Bytes(), &env); err != nil {
		t.Fatalf("unmarshal envelope: %v", err)
	}
	if len(env.NetworkResults) != 0 {
		t.Fatalf("expected no network steps on idempotent re-apply, got %d", len(env.NetworkResults))
	}
}

func TestRun_VSwitches_MissingExtension_Exit4(t *testing.T) {
	driver := fake.New() // no network_acl extension
	tmpDir := t.TempDir()
	writeTestFile(t, filepath.Join(tmpDir, "_base.yaml"), `schema: lxm/config/v2
base: true
vswitches:
  - name: vmbr0
    ipv4: 10.30.0.1/24
    group: vms
`)
	writeTestFile(t, filepath.Join(tmpDir, "web-a.yaml"), `schema: lxm/config/v2
include: [_base.yaml]
name: web-a
image: ubuntu:22.04
`)

	var stdout, stderr bytes.Buffer
	code := run([]string{"plan", tmpDir}, &stdout, &stderr, driver)
	if code != 4 {
		t.Fatalf("expected exit 4 (missing network_acl), got %d. Stderr: %s", code, stderr.String())
	}
}

func TestRun_VSwitches_NICSubnetViolation_Exit3(t *testing.T) {
	driver := fake.New()
	driver.Extensions["network_acl"] = true
	tmpDir := t.TempDir()
	writeTestFile(t, filepath.Join(tmpDir, "_base.yaml"), `schema: lxm/config/v2
base: true
vswitches:
  - name: vmbr0
    ipv4: 10.30.0.1/24
    group: vms
`)
	writeTestFile(t, filepath.Join(tmpDir, "web-a.yaml"), `schema: lxm/config/v2
include: [_base.yaml]
name: web-a
image: ubuntu:22.04
networks:
  - name: eth0
    parent: vmbr0
    ipv4: 10.99.0.5
`)

	var stdout, stderr bytes.Buffer
	code := run([]string{"plan", tmpDir}, &stdout, &stderr, driver)
	if code != 3 {
		t.Fatalf("expected exit 3 (NIC outside parent subnet), got %d. Stderr: %s", code, stderr.String())
	}
	if !strings.Contains(stderr.String(), "outside parent vswitch") {
		t.Fatalf("expected NIC-outside-subnet message, got: %s", stderr.String())
	}
}

// TestRun_VSwitches_NetworkErrorEnvelopeName is the regression test for the
// code-review finding: a failed network step must surface the offending
// vswitch/ACL name in the JSON envelope's error entry, not an empty
// container field.
func TestRun_VSwitches_NetworkErrorEnvelopeName(t *testing.T) {
	driver := fake.New()
	driver.Extensions["network_acl"] = true
	driver.CreateNetworkFunc = func(req provider.NetworkCreateRequest) error {
		return errors.New("network create rejected by external policy")
	}

	tmpDir := t.TempDir()
	writeTestFile(t, filepath.Join(tmpDir, "_base.yaml"), `schema: lxm/config/v2
base: true
image: ubuntu:22.04
vswitches:
  - name: vmbr0
    ipv4: 10.30.0.1/24
    group: vms
`)
	writeTestFile(t, filepath.Join(tmpDir, "web-a.yaml"), `schema: lxm/config/v2
include: [_base.yaml]
name: web-a
`)

	var stdout, stderr bytes.Buffer
	code := run([]string{"apply", tmpDir, "--format", "json"}, &stdout, &stderr, driver)
	if code != 4 {
		t.Fatalf("expected exit 4 (PROVIDER_ERROR), got %d. Stderr: %s", code, stderr.String())
	}
	var env output.Envelope
	if err := json.Unmarshal(stdout.Bytes(), &env); err != nil {
		t.Fatalf("unmarshal envelope: %v", err)
	}
	if len(env.Errors) == 0 {
		t.Fatalf("expected at least one error in envelope")
	}
	if env.Errors[0].Name != "vmbr0" {
		t.Fatalf("expected error name vmbr0, got %q (container=%q)", env.Errors[0].Name, env.Errors[0].Container)
	}
	if !strings.Contains(env.Errors[0].Message, "rejected by external policy") {
		t.Fatalf("unexpected error message: %s", env.Errors[0].Message)
	}
}

// TestRun_NoVSwitches_NoFalseNICWarnings is the F1 regression: a vswitch-less
// fleet with the stock lxdbr0 parent must not emit the "not a known LXD
// network or declared vswitch" warning.
func TestRun_NoVSwitches_NoFalseNICWarnings(t *testing.T) {
	driver := fake.New()
	driver.Extensions["network_acl"] = true
	// The fake reports lxdbr0 as a live network by default.
	tmpDir := t.TempDir()
	writeTestFile(t, filepath.Join(tmpDir, "inst-a.yaml"), `schema: lxm/config/v2
name: inst-a
image: ubuntu:22.04
networks:
  - name: eth0
    parent: lxdbr0
`)

	var stdout, stderr bytes.Buffer
	code := run([]string{"plan", tmpDir, "--format", "json"}, &stdout, &stderr, driver)
	if code != 0 {
		t.Fatalf("plan returned %d, want 0. Stderr: %s", code, stderr.String())
	}
	var env output.Envelope
	if err := json.Unmarshal(stdout.Bytes(), &env); err != nil {
		t.Fatalf("unmarshal envelope: %v", err)
	}
	for _, w := range env.Warnings {
		if strings.Contains(w, "not a known LXD network") || strings.Contains(w, "not a known provider network") {
			t.Fatalf("false unknown-parent warning for vswitch-less fleet: %q", w)
		}
	}
}

// TestRun_LiveStateListError_Exit4 is the F2 regression: a live-state listing
// failure must exit 4 at plan time instead of planning against an empty live
// set (which would bypass adoption-refusal/foreign-ACL checks).
func TestRun_LiveStateListError_Exit4(t *testing.T) {
	driver := fake.New()
	driver.Extensions["network_acl"] = true
	driver.GetNetworksFunc = func() ([]provider.Network, error) {
		return nil, errors.New("daemon hiccup listing networks")
	}
	tmpDir := t.TempDir()
	writeTestFile(t, filepath.Join(tmpDir, "_base.yaml"), `schema: lxm/config/v2
base: true
image: ubuntu:22.04
vswitches:
  - name: vmbr0
    ipv4: 10.30.0.1/24
    group: vms
`)
	writeTestFile(t, filepath.Join(tmpDir, "web-a.yaml"), `schema: lxm/config/v2
include: [_base.yaml]
name: web-a
`)

	var stdout, stderr bytes.Buffer
	code := run([]string{"plan", tmpDir, "--format", "json"}, &stdout, &stderr, driver)
	if code != 4 {
		t.Fatalf("expected exit 4 on live-state listing failure, got %d. Stderr: %s", code, stderr.String())
	}
	if !strings.Contains(stderr.String(), "listing provider networks") && !strings.Contains(stderr.String(), "listing LXD networks") {
		t.Fatalf("expected listing-error message, got: %s", stderr.String())
	}
}

// TestRun_NoNetworkACLExtension_VswitchlessStillPlans is the N2 regression:
// on a server without the network_acl extension the ACL listing must be
// skipped (not 404-fail), so a vswitch-less fleet still plans cleanly.
func TestRun_NoNetworkACLExtension_VswitchlessStillPlans(t *testing.T) {
	driver := fake.New() // no network_acl extension
	// Force the ACL endpoint to fail if it is ever called.
	driver.GetNetworkACLsFunc = func() ([]provider.NetworkACL, error) {
		return nil, errors.New("network ACLs endpoint not found")
	}
	tmpDir := t.TempDir()
	writeTestFile(t, filepath.Join(tmpDir, "inst-a.yaml"), `schema: lxm/config/v2
name: inst-a
image: ubuntu:22.04
networks:
  - name: eth0
    parent: lxdbr0
`)
	var stdout, stderr bytes.Buffer
	code := run([]string{"plan", tmpDir, "--format", "json"}, &stdout, &stderr, driver)
	if code != 0 {
		t.Fatalf("plan returned %d, want 0 (ACL listing must be gated on the extension). Stderr: %s", code, stderr.String())
	}
}
