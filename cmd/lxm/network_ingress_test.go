package main

import (
	"bytes"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"github.com/aiyor/lxm/internal/provider/fake"
)

func writeIngressBase(t *testing.T, dir string, withGrants bool) {
	t.Helper()
	grants := ""
	if withGrants {
		grants = `    host_access: true
`
	}
	policy := ""
	if withGrants {
		policy = `network_policy:
  allow: []
  ingress_allow:
    - to: lab1
      source: 100.99.45.41/32
      ports: ["22", "3000-3999"]
`
	} else {
		policy = `network_policy:
  allow: []
`
	}
	writeTestFile(t, filepath.Join(dir, "_base.yaml"), `schema: lxm/config/v2
base: true
image: ubuntu:24.04
vswitches:
  - name: labbr0
    ipv4: 10.208.0.1/24
    group: lab1
`+grants+policy)
}

func writeIngressLeaf(t *testing.T, dir string) {
	t.Helper()
	writeTestFile(t, filepath.Join(dir, "lab-a.yaml"), `schema: lxm/config/v2
include: [_base.yaml]
name: lab-a
`)
}

func planNetworkSteps(t *testing.T, dir string, driver *fake.FakeDriver) []map[string]any {
	t.Helper()
	var stdout, stderr bytes.Buffer
	if code := run([]string{"plan", dir, "--format", "json"}, &stdout, &stderr, driver); code != 0 {
		t.Fatalf("plan returned %d: %s", code, stderr.String())
	}
	var env map[string]any
	if err := json.Unmarshal(stdout.Bytes(), &env); err != nil {
		t.Fatalf("unmarshal plan envelope: %v", err)
	}
	planObj, _ := env["plan"].(map[string]any)
	raw, _ := planObj["network_steps"].([]any)
	steps := make([]map[string]any, 0, len(raw))
	for _, s := range raw {
		steps = append(steps, s.(map[string]any))
	}
	return steps
}

func stepByName(steps []map[string]any, name, kind string) map[string]any {
	for _, s := range steps {
		if s["name"] == name && s["kind"] == kind {
			return s
		}
	}
	return nil
}

func TestRun_VSwitches_HostAccessIngressAllow(t *testing.T) {
	driver := fake.New()
	driver.Extensions["network_acl"] = true
	dir := t.TempDir()
	writeIngressBase(t, dir, true)
	writeIngressLeaf(t, dir)

	// 1. Plan shows the host_access and ingress_allow rules in the ACL payload.
	steps := planNetworkSteps(t, dir, driver)
	create := stepByName(steps, "lxm-labbr0", "create_acl")
	if create == nil {
		t.Fatalf("expected create_acl for lxm-labbr0, got %+v", steps)
	}
	post, _ := create["acl_post"].(map[string]any)
	ingress, _ := post["ingress"].([]any)
	foundHost, foundGrant := false, false
	for _, raw := range ingress {
		rule := raw.(map[string]any)
		if rule["source"] == "10.208.0.1/32" && rule["destination"] == "10.208.0.0/24" {
			foundHost = true
		}
		if rule["source"] == "100.99.45.41/32" && rule["destination"] == "10.208.0.0/24" &&
			rule["protocol"] == "tcp" && rule["destination_port"] == "22,3000-3999" {
			foundGrant = true
		}
	}
	if !foundHost || !foundGrant {
		t.Fatalf("ACL create payload missing host/ingress rules: %+v", ingress)
	}

	// 2. Apply creates the ACL with both rules and is idempotent.
	var stdout, stderr bytes.Buffer
	if code := run([]string{"apply", dir, "--format", "json"}, &stdout, &stderr, driver); code != 0 {
		t.Fatalf("apply returned %d: %s", code, stderr.String())
	}
	acl := driver.NetworkACLs["lxm-labbr0"]
	if acl == nil {
		t.Fatalf("ACL not created")
	}
	hasHost, hasGrant := false, false
	for _, r := range acl.Ingress {
		if r.Source == "10.208.0.1/32" && r.Destination == "10.208.0.0/24" {
			hasHost = true
		}
		if r.Source == "100.99.45.41/32" && r.Destination == "10.208.0.0/24" &&
			r.Protocol == "tcp" && r.DestinationPort == "22,3000-3999" {
			hasGrant = true
		}
	}
	if !hasHost || !hasGrant {
		t.Fatalf("applied ACL missing rules: %+v", acl.Ingress)
	}
	stdout.Reset()
	stderr.Reset()
	if code := run([]string{"apply", dir, "--format", "json"}, &stdout, &stderr, driver); code != 0 {
		t.Fatalf("second apply returned %d: %s", code, stderr.String())
	}
	var secondApply map[string]any
	if err := json.Unmarshal(stdout.Bytes(), &secondApply); err != nil {
		t.Fatalf("unmarshal second apply envelope: %v", err)
	}
	if results, _ := secondApply["network_results"].([]any); len(results) != 0 {
		t.Fatalf("idempotent re-apply must not produce network results: %+v", results)
	}
	if update := stepByName(planNetworkSteps(t, dir, driver), "lxm-labbr0", "update_acl"); update != nil {
		t.Fatalf("idempotent re-plan must not update the ACL: %+v", update)
	}

	// 3. Removing the fields produces a tightening ACL diff and revokes the rules.
	writeIngressBase(t, dir, false)
	steps = planNetworkSteps(t, dir, driver)
	update := stepByName(steps, "lxm-labbr0", "update_acl")
	if update == nil {
		t.Fatalf("expected update_acl after removing grants, got %+v", steps)
	}
	if tightened, _ := update["tightened"].(bool); !tightened {
		t.Fatalf("expected tightened update ACL, got %+v", update)
	}
	diff, _ := update["diff"].([]any)
	foundRemoved := false
	for _, raw := range diff {
		d := raw.(map[string]any)
		if d["field"] == "ingress.removed" && strings.Contains(d["old"].(string), "100.99.45.41/32") {
			foundRemoved = true
		}
	}
	if !foundRemoved {
		t.Fatalf("expected ingress.removed diff, got %+v", diff)
	}

	stdout.Reset()
	stderr.Reset()
	if code := run([]string{"apply", dir, "--format", "json"}, &stdout, &stderr, driver); code != 0 {
		t.Fatalf("revoke apply returned %d: %s", code, stderr.String())
	}
	for _, r := range driver.NetworkACLs["lxm-labbr0"].Ingress {
		if strings.HasPrefix(r.Source, "100.99.45.") || r.Source == "10.208.0.1/32" {
			t.Fatalf("ingress rule not revoked: %+v", r)
		}
	}
	if update := stepByName(planNetworkSteps(t, dir, driver), "lxm-labbr0", "update_acl"); update != nil {
		t.Fatalf("post-revoke plan must be a no-op: %+v", update)
	}
}

func TestRun_VSwitches_IngressAllowWildcardWarning(t *testing.T) {
	driver := fake.New()
	driver.Extensions["network_acl"] = true
	dir := t.TempDir()
	writeTestFile(t, filepath.Join(dir, "_base.yaml"), `schema: lxm/config/v2
base: true
vswitches:
  - name: labbr0
    ipv4: 10.208.0.1/24
    group: lab1
network_policy:
  allow: []
  ingress_allow:
    - to: lab1
      source: 0.0.0.0/0
`)
	writeTestFile(t, filepath.Join(dir, "lab-a.yaml"), `schema: lxm/config/v2
include: [_base.yaml]
name: lab-a
image: ubuntu:24.04
`)

	var stdout, stderr bytes.Buffer
	if code := run([]string{"plan", dir, "--format", "json"}, &stdout, &stderr, driver); code != 0 {
		t.Fatalf("plan returned %d: %s", code, stderr.String())
	}
	var env map[string]any
	if err := json.Unmarshal(stdout.Bytes(), &env); err != nil {
		t.Fatalf("unmarshal plan envelope: %v", err)
	}
	warnings, _ := env["warnings"].([]any)
	found := false
	for _, raw := range warnings {
		if strings.Contains(raw.(string), "wildcard source") {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected wildcard-source warning, got %+v", warnings)
	}
}
