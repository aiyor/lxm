package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/aiyor/lxm/internal/provider"
	"github.com/aiyor/lxm/internal/provider/fake"
)

type portsTestEnvelope struct {
	Schema   string `json:"schema"`
	OK       bool   `json:"ok"`
	ExitCode int    `json:"exit_code"`
	Plan     struct {
		Summary map[string]int `json:"summary"`
		Steps   []struct {
			Container     string                          `json:"container"`
			Action        string                          `json:"action"`
			Diff          []map[string]any                `json:"diff"`
			InstancesPost *provider.InstanceCreateRequest `json:"instances_post"`
			InstancePut   *provider.InstanceUpdateRequest `json:"instance_put"`
		} `json:"steps"`
	} `json:"plan"`
	Results []struct {
		Container      string `json:"container"`
		Action         string `json:"action"`
		PublishedPorts []struct {
			Device  string `json:"device"`
			Name    string `json:"name"`
			Proto   string `json:"proto"`
			Listen  string `json:"listen"`
			Connect string `json:"connect"`
			NAT     bool   `json:"nat"`
			Managed bool   `json:"managed"`
		} `json:"published_ports"`
	} `json:"results"`
	Warnings []string `json:"warnings"`
	Errors   []struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	} `json:"errors"`
}

func parsePortsEnvelope(t *testing.T, data []byte) portsTestEnvelope {
	t.Helper()
	var env portsTestEnvelope
	if err := json.Unmarshal(data, &env); err != nil {
		t.Fatalf("invalid JSON envelope: %v\noutput: %s", err, data)
	}
	return env
}

func writePortsCLIManifest(t *testing.T, path, body string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(body), 0644); err != nil {
		t.Fatalf("writing %s: %v", path, err)
	}
}

func containerManifest(name string, ports string) string {
	body := "schema: lxm/config/v2\nname: " + name + "\nimage: ubuntu:24.04\n"
	if ports != "" {
		body += "ports:\n" + ports
	}
	return body
}

func TestRun_Ports_PlanApplyRemoveLifecycle(t *testing.T) {
	driver := fake.New()
	dir := t.TempDir()
	manifest := filepath.Join(dir, "web.yaml")
	writePortsCLIManifest(t, manifest, containerManifest("web-a", `  - {name: web, listen: "127.0.0.1:8080", connect: "127.0.0.1:80"}
`))

	// 1. Plan: create step carries the proxy device and a ports diff.
	var stdout, stderr bytes.Buffer
	code := run([]string{"plan", manifest, "--format", "json"}, &stdout, &stderr, driver)
	if code != 0 {
		t.Fatalf("plan returned %d, want 0. stderr: %s", code, stderr.String())
	}
	env := parsePortsEnvelope(t, stdout.Bytes())
	if !env.OK || env.ExitCode != 0 {
		t.Fatalf("unexpected plan envelope: ok=%v exit=%d", env.OK, env.ExitCode)
	}
	if len(env.Plan.Steps) != 1 || env.Plan.Steps[0].Action != "create" {
		t.Fatalf("unexpected plan steps: %+v", env.Plan.Steps)
	}
	post := env.Plan.Steps[0].InstancesPost
	if post == nil || post.Devices["proxy-web"] == nil {
		t.Fatalf("plan create payload missing proxy-web: %+v", post)
	}
	if post.Devices["proxy-web"]["listen"] != "tcp:127.0.0.1:8080" {
		t.Fatalf("unexpected device payload: %v", post.Devices["proxy-web"])
	}

	// 2. Apply: device is created.
	stdout.Reset()
	stderr.Reset()
	code = run([]string{"apply", manifest, "--format", "json"}, &stdout, &stderr, driver)
	if code != 0 {
		t.Fatalf("apply returned %d, want 0. stderr: %s", code, stderr.String())
	}
	inst := driver.Instances["web-a"]
	if inst == nil || inst.Devices["proxy-web"] == nil {
		t.Fatalf("proxy-web device not created: %+v", inst)
	}

	// 3. Second plan: noop.
	stdout.Reset()
	stderr.Reset()
	code = run([]string{"plan", manifest, "--format", "json"}, &stdout, &stderr, driver)
	if code != 0 {
		t.Fatalf("second plan returned %d, want 0. stderr: %s", code, stderr.String())
	}
	env = parsePortsEnvelope(t, stdout.Bytes())
	if len(env.Plan.Steps) != 1 || env.Plan.Steps[0].Action != "noop" {
		t.Fatalf("expected noop after apply, got %+v", env.Plan.Steps)
	}

	// 3b. A port change is a field-level diff in plan JSON.
	writePortsCLIManifest(t, manifest, containerManifest("web-a", `  - {name: web, listen: "127.0.0.1:8080", connect: "127.0.0.1:81"}
`))
	stdout.Reset()
	stderr.Reset()
	code = run([]string{"plan", manifest, "--format", "json"}, &stdout, &stderr, driver)
	if code != 0 {
		t.Fatalf("port-change plan returned %d, want 0. stderr: %s", code, stderr.String())
	}
	env = parsePortsEnvelope(t, stdout.Bytes())
	if len(env.Plan.Steps) != 1 {
		t.Fatalf("expected one port-change step, got %+v", env.Plan.Steps)
	}
	changeFound := false
	for _, d := range env.Plan.Steps[0].Diff {
		if d["field"] == "ports[web].connect" && d["new"] == "127.0.0.1:81" {
			changeFound = true
		}
	}
	if !changeFound {
		t.Fatalf("plan JSON missing ports[web].connect diff: %+v", env.Plan.Steps[0].Diff)
	}
	// Restore the underlying port for the list/status assertions below (they
	// inspect live state, which the apply above left at connect :80).
	writePortsCLIManifest(t, manifest, containerManifest("web-a", `  - {name: web, listen: "127.0.0.1:8080", connect: "127.0.0.1:80"}
`))

	// 4. List JSON surfaces the published port.
	stdout.Reset()
	stderr.Reset()
	code = run([]string{"list", "--format", "json"}, &stdout, &stderr, driver)
	if code != 0 {
		t.Fatalf("list returned %d, want 0. stderr: %s", code, stderr.String())
	}
	env = parsePortsEnvelope(t, stdout.Bytes())
	if len(env.Results) != 1 || len(env.Results[0].PublishedPorts) != 1 {
		t.Fatalf("list did not surface published ports: %+v", env.Results)
	}
	pub := env.Results[0].PublishedPorts[0]
	if pub.Device != "proxy-web" || pub.Name != "web" || pub.Listen != "127.0.0.1:8080" || pub.Connect != "127.0.0.1:80" || !pub.Managed {
		t.Fatalf("unexpected published port: %+v", pub)
	}

	// 5. List text includes the PORTS column/value.
	stdout.Reset()
	stderr.Reset()
	code = run([]string{"list"}, &stdout, &stderr, driver)
	if code != 0 {
		t.Fatalf("list text returned %d, want 0. stderr: %s", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "PORTS") || !strings.Contains(stdout.String(), "127.0.0.1:8080->127.0.0.1:80") {
		t.Fatalf("list text missing ports column: %s", stdout.String())
	}

	// 6. Status text + JSON surface the published port.
	stdout.Reset()
	stderr.Reset()
	code = run([]string{"status", "web-a", "--format", "json"}, &stdout, &stderr, driver)
	if code != 0 {
		t.Fatalf("status returned %d, want 0. stderr: %s", code, stderr.String())
	}
	env = parsePortsEnvelope(t, stdout.Bytes())
	if len(env.Results) != 1 || len(env.Results[0].PublishedPorts) != 1 {
		t.Fatalf("status JSON missing published ports: %+v", env.Results)
	}

	stdout.Reset()
	stderr.Reset()
	code = run([]string{"status", "web-a"}, &stdout, &stderr, driver)
	if code != 0 {
		t.Fatalf("status text returned %d, want 0. stderr: %s", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "Published Ports:") || !strings.Contains(stdout.String(), "(managed)") {
		t.Fatalf("status text missing ports block: %s", stdout.String())
	}

	// 7. Removing the port deletes the device on the next apply.
	writePortsCLIManifest(t, manifest, containerManifest("web-a", ""))
	stdout.Reset()
	stderr.Reset()
	code = run([]string{"apply", manifest, "--format", "json"}, &stdout, &stderr, driver)
	if code != 0 {
		t.Fatalf("apply removal returned %d, want 0. stderr: %s", code, stderr.String())
	}
	if _, ok := driver.Instances["web-a"].Devices["proxy-web"]; ok {
		t.Fatalf("removed port device still present: %+v", driver.Instances["web-a"].Devices)
	}
}

func TestRun_Ports_DuplicateListenerRejected(t *testing.T) {
	driver := fake.New()
	dir := t.TempDir()
	writePortsCLIManifest(t, filepath.Join(dir, "a.yaml"), containerManifest("a", `  - {name: web, listen: "127.0.0.1:8080", connect: "127.0.0.1:80"}
`))
	writePortsCLIManifest(t, filepath.Join(dir, "b.yaml"), containerManifest("b", `  - {name: web, listen: "127.0.0.1:8080", connect: "127.0.0.1:81"}
`))

	var stdout, stderr bytes.Buffer
	code := run([]string{"plan", dir}, &stdout, &stderr, driver)
	if code != 3 {
		t.Fatalf("duplicate listener returned %d, want 3. stderr: %s", code, stderr.String())
	}
	if !strings.Contains(stderr.String(), "published by both") {
		t.Fatalf("missing duplicate-listener diagnostic: %s", stderr.String())
	}
}

func TestRun_Ports_ValidationRejected(t *testing.T) {
	driver := fake.New()
	dir := t.TempDir()
	manifest := filepath.Join(dir, "bad.yaml")
	writePortsCLIManifest(t, manifest, `schema: lxm/config/v2
name: bad
image: ubuntu:24.04
networks: [{name: eth0, parent: lxdbr0, ipv4: 10.0.0.5}]
ports: [{name: web, listen: "127.0.0.1:8080", connect: "10.0.0.5:80", nat: true}]
`)
	var stdout, stderr bytes.Buffer
	code := run([]string{"plan", manifest}, &stdout, &stderr, driver)
	if code != 3 {
		t.Fatalf("invalid NAT listener returned %d, want 3. stderr: %s", code, stderr.String())
	}
	if !strings.Contains(stderr.String(), "cannot listen on a loopback address") {
		t.Fatalf("missing loopback-NAT diagnostic: %s", stderr.String())
	}
}

func TestRun_Ports_UnmarkedDeviceCollisionRejected(t *testing.T) {
	driver := fake.New()
	driver.Instances["web-a"] = &provider.Instance{
		Name:   "web-a",
		Status: "Running",
		Config: map[string]string{"user.lxm.user": "ubuntu", "user.lxm.image": "ubuntu:24.04"},
		Devices: map[string]map[string]string{
			"proxy-web": {"type": "proxy", "listen": "tcp:127.0.0.1:8080", "connect": "tcp:127.0.0.1:80"},
		},
	}
	dir := t.TempDir()
	manifest := filepath.Join(dir, "web.yaml")
	writePortsCLIManifest(t, manifest, containerManifest("web-a", `  - {name: web, listen: "127.0.0.1:8080", connect: "127.0.0.1:80"}
`))

	var stdout, stderr bytes.Buffer
	code := run([]string{"plan", manifest}, &stdout, &stderr, driver)
	if code != 3 {
		t.Fatalf("unmarked collision returned %d, want 3. stderr: %s", code, stderr.String())
	}
	if !strings.Contains(stderr.String(), "is not lxm-managed") {
		t.Fatalf("missing ownership diagnostic: %s", stderr.String())
	}
}

func TestRun_Ports_HandoffWarning(t *testing.T) {
	driver := fake.New()
	driver.Instances["a"] = &provider.Instance{
		Name:   "a",
		Status: "Running",
		Config: map[string]string{"user.lxm.user": "ubuntu", "user.lxm.image": "ubuntu:24.04"},
		Devices: map[string]map[string]string{
			"proxy-web": {
				"type":             "proxy",
				"listen":           "tcp:127.0.0.1:8080",
				"connect":          "tcp:127.0.0.1:80",
				"user.lxm.managed": "true",
			},
		},
	}
	dir := t.TempDir()
	writePortsCLIManifest(t, filepath.Join(dir, "a.yaml"), containerManifest("a", ""))
	writePortsCLIManifest(t, filepath.Join(dir, "b.yaml"), containerManifest("b", `  - {name: web, listen: "127.0.0.1:8080", connect: "127.0.0.1:80"}
`))

	var stdout, stderr bytes.Buffer
	code := run([]string{"plan", dir, "--format", "json"}, &stdout, &stderr, driver)
	if code != 0 {
		t.Fatalf("handoff plan returned %d, want 0. stderr: %s", code, stderr.String())
	}
	env := parsePortsEnvelope(t, stdout.Bytes())
	found := false
	for _, w := range env.Warnings {
		if strings.Contains(w, `moves from instance "a" to instance "b"`) {
			found = true
		}
	}
	if !found {
		t.Fatalf("missing handoff warning; warnings=%v stdout=%s", env.Warnings, stdout.String())
	}

	// Text output prints the warning to stderr as well.
	stdout.Reset()
	stderr.Reset()
	code = run([]string{"plan", dir}, &stdout, &stderr, driver)
	if code != 0 {
		t.Fatalf("handoff plan text returned %d, want 0. stderr: %s", code, stderr.String())
	}
	if !strings.Contains(stderr.String(), "moves from instance") {
		t.Fatalf("missing handoff warning in text output: %s", stderr.String())
	}
}

func TestRun_Ports_DiffText(t *testing.T) {
	driver := fake.New()
	driver.Instances["web-a"] = &provider.Instance{
		Name:    "web-a",
		Status:  "Running",
		Config:  map[string]string{"user.lxm.user": "ubuntu"},
		Devices: map[string]map[string]string{},
	}
	dir := t.TempDir()
	manifest := filepath.Join(dir, "web.yaml")
	writePortsCLIManifest(t, manifest, containerManifest("web-a", `  - {name: web, listen: "127.0.0.1:8080", connect: "127.0.0.1:80"}
`))

	var stdout, stderr bytes.Buffer
	code := run([]string{"diff", manifest, "web-a"}, &stdout, &stderr, driver)
	if code != 0 {
		t.Fatalf("diff returned %d, want 0. stderr: %s", code, stderr.String())
	}
	out := stdout.String()
	if !strings.Contains(out, "ports[web]") {
		t.Fatalf("diff text missing ports field: %s", out)
	}
	if !strings.Contains(out, "tcp 127.0.0.1:8080 -> 127.0.0.1:80") {
		t.Fatalf("diff text missing human-readable port value: %s", out)
	}
}

func TestRun_Ports_ApplyHandoff(t *testing.T) {
	driver := fake.New()
	driver.Instances["a"] = &provider.Instance{
		Name:   "a",
		Status: "Running",
		Config: map[string]string{"user.lxm.user": "ubuntu", "user.lxm.image": "ubuntu:24.04"},
		Devices: map[string]map[string]string{
			"proxy-web": {
				"type":             "proxy",
				"listen":           "tcp:127.0.0.1:18080",
				"connect":          "tcp:127.0.0.1:80",
				"user.lxm.managed": "true",
			},
		},
	}
	dir := t.TempDir()
	writePortsCLIManifest(t, filepath.Join(dir, "a.yaml"), containerManifest("a", ""))
	writePortsCLIManifest(t, filepath.Join(dir, "b.yaml"), containerManifest("b", `  - {name: web, listen: "127.0.0.1:18080", connect: "127.0.0.1:80"}
`))

	var stdout, stderr bytes.Buffer
	code := run([]string{"apply", dir, "--format", "json"}, &stdout, &stderr, driver)
	if code != 0 {
		t.Fatalf("handoff apply returned %d, want 0. stderr: %s", code, stderr.String())
	}
	env := parsePortsEnvelope(t, stdout.Bytes())
	found := false
	for _, w := range env.Warnings {
		if strings.Contains(w, "moves from instance") {
			found = true
		}
	}
	if !found {
		t.Fatalf("apply did not emit handoff warning: %v", env.Warnings)
	}
	if driver.Instances["a"].Devices["proxy-web"] != nil {
		t.Fatalf("source instance still has proxy-web: %+v", driver.Instances["a"].Devices)
	}
	if driver.Instances["b"].Devices["proxy-web"] == nil {
		t.Fatalf("target instance missing proxy-web: %+v", driver.Instances["b"].Devices)
	}
}

func TestRun_Ports_NATLifecycle(t *testing.T) {
	driver := fake.New()
	dir := t.TempDir()
	manifest := filepath.Join(dir, "nat.yaml")
	writePortsCLIManifest(t, manifest, `schema: lxm/config/v2
name: nat-web
image: ubuntu:24.04
networks:
  - {name: eth0, parent: lxdbr0, ipv4: 10.0.0.50}
ports:
  - {name: https, listen: "10.0.0.1:8443", connect: "10.0.0.50:443", nat: true}
`)

	var stdout, stderr bytes.Buffer
	if code := run([]string{"apply", manifest, "--format", "json"}, &stdout, &stderr, driver); code != 0 {
		t.Fatalf("NAT apply returned %d, want 0. stderr: %s", code, stderr.String())
	}
	dev := driver.Instances["nat-web"].Devices["proxy-https"]
	if dev == nil || dev["nat"] != "true" {
		t.Fatalf("NAT proxy device not created: %+v", dev)
	}

	stdout.Reset()
	stderr.Reset()
	if code := run([]string{"plan", manifest, "--format", "json"}, &stdout, &stderr, driver); code != 0 {
		t.Fatalf("NAT re-plan returned %d, want 0. stderr: %s", code, stderr.String())
	}
	env := parsePortsEnvelope(t, stdout.Bytes())
	if len(env.Plan.Steps) != 1 || env.Plan.Steps[0].Action != "noop" {
		t.Fatalf("expected NAT noop after apply, got %+v", env.Plan.Steps)
	}
	if len(env.Plan.Steps[0].Diff) != 0 {
		t.Fatalf("expected no NAT diff after apply, got %+v", env.Plan.Steps[0].Diff)
	}
}

func TestRun_Ports_RebuildSecondApplyConverges(t *testing.T) {
	// Native daemon rebuild (instances_rebuild) does not PUT devices. A port
	// change in the same step as an image recreate therefore lands on the next
	// apply; this pins the documented convergence behaviour (PORTS-SPEC §8).
	driver := fake.New()
	driver.Instances["web-a"] = &provider.Instance{
		Name:   "web-a",
		Status: "Running",
		Config: map[string]string{
			"user.lxm.user":  "ubuntu",
			"user.lxm.image": "debian:12",
			"image.os":       "debian",
			"image.release":  "12",
		},
		Devices: map[string]map[string]string{
			"proxy-web": {
				"type":             "proxy",
				"listen":           "tcp:127.0.0.1:8079",
				"connect":          "tcp:127.0.0.1:80",
				"user.lxm.managed": "true",
			},
		},
	}
	driver.RebuildInstanceFunc = func(name string, req provider.InstanceRebuildRequest) error {
		inst := driver.Instances[name]
		inst.Config["user.lxm.image"] = "jammy"
		delete(inst.Config, "image.os")
		delete(inst.Config, "image.release")
		driver.ETags[name] = "fake-etag-rebuilt"
		return nil
	}

	dir := t.TempDir()
	manifest := filepath.Join(dir, "web.yaml")
	writePortsCLIManifest(t, manifest, `schema: lxm/config/v2
name: web-a
image: jammy
user: ubuntu
ports:
  - {name: web, listen: "127.0.0.1:8080", connect: "127.0.0.1:80"}
`)

	var stdout, stderr bytes.Buffer
	if code := run([]string{"apply", manifest, "--format", "json"}, &stdout, &stderr, driver); code != 0 {
		t.Fatalf("first apply returned %d, want 0. stderr: %s", code, stderr.String())
	}
	first := parsePortsEnvelope(t, stdout.Bytes())
	if len(first.Plan.Steps) != 1 || first.Plan.Steps[0].Action != "recreate" {
		t.Fatalf("expected recreate step, got %+v", first.Plan.Steps)
	}
	if got := driver.Instances["web-a"].Devices["proxy-web"]["listen"]; got != "tcp:127.0.0.1:8079" {
		t.Fatalf("fake rebuild should leave devices untouched, got listen %q", got)
	}

	stdout.Reset()
	stderr.Reset()
	if code := run([]string{"plan", manifest, "--format", "json"}, &stdout, &stderr, driver); code != 0 {
		t.Fatalf("second plan returned %d, want 0. stderr: %s", code, stderr.String())
	}
	second := parsePortsEnvelope(t, stdout.Bytes())
	if len(second.Plan.Steps) != 1 || second.Plan.Steps[0].Action != "update" {
		t.Fatalf("expected a deferred port update, got %+v", second.Plan.Steps)
	}

	stdout.Reset()
	stderr.Reset()
	if code := run([]string{"apply", manifest, "--format", "json"}, &stdout, &stderr, driver); code != 0 {
		t.Fatalf("second apply returned %d, want 0. stderr: %s", code, stderr.String())
	}
	if got := driver.Instances["web-a"].Devices["proxy-web"]["listen"]; got != "tcp:127.0.0.1:8080" {
		t.Fatalf("second apply did not converge device listen, got %q", got)
	}

	stdout.Reset()
	stderr.Reset()
	if code := run([]string{"plan", manifest, "--format", "json"}, &stdout, &stderr, driver); code != 0 {
		t.Fatalf("third plan returned %d, want 0. stderr: %s", code, stderr.String())
	}
	third := parsePortsEnvelope(t, stdout.Bytes())
	if len(third.Plan.Steps) != 1 || third.Plan.Steps[0].Action != "noop" {
		t.Fatalf("expected noop after second apply, got %+v", third.Plan.Steps)
	}
}
