package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/aiyor/lxm/internal/lxd"
	"github.com/aiyor/lxm/internal/output"
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
	fake := lxd.NewFakeInstanceServer()
	fake.Extensions["network_acl"] = true

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
	code := run([]string{"plan", tmpDir, "--format", "json"}, &stdout, &stderr, fake)
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
	code = run([]string{"apply", tmpDir, "--format", "json"}, &stdout, &stderr, fake)
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

	if _, ok := fake.Nets.Networks["vmbr0"]; !ok {
		t.Fatalf("vmbr0 not created")
	}
	if _, ok := fake.Nets.Networks["svcbr0"]; !ok {
		t.Fatalf("svcbr0 not created")
	}
	if _, ok := fake.Nets.ACLs["lxm-vmbr0"]; !ok {
		t.Fatalf("lxm-vmbr0 ACL not created")
	}
	if _, _, err := fake.GetInstance("web-a"); err != nil {
		t.Fatalf("web-a instance not created: %v", err)
	}

	// 3. Second apply is a no-op (idempotent) — no network steps.
	stdout.Reset()
	stderr.Reset()
	code = run([]string{"apply", tmpDir, "--format", "json"}, &stdout, &stderr, fake)
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
	fake := lxd.NewFakeInstanceServer() // no network_acl extension
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
	code := run([]string{"plan", tmpDir}, &stdout, &stderr, fake)
	if code != 4 {
		t.Fatalf("expected exit 4 (missing network_acl), got %d. Stderr: %s", code, stderr.String())
	}
}

func TestRun_VSwitches_NICSubnetViolation_Exit3(t *testing.T) {
	fake := lxd.NewFakeInstanceServer()
	fake.Extensions["network_acl"] = true
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
	code := run([]string{"plan", tmpDir}, &stdout, &stderr, fake)
	if code != 3 {
		t.Fatalf("expected exit 3 (NIC outside parent subnet), got %d. Stderr: %s", code, stderr.String())
	}
	if !strings.Contains(stderr.String(), "outside parent vswitch") {
		t.Fatalf("expected NIC-outside-subnet message, got: %s", stderr.String())
	}
}
