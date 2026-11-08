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

type socketsTestEnvelope struct {
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
		Container        string `json:"container"`
		Action           string `json:"action"`
		PublishedSockets []struct {
			Device  string `json:"device"`
			Name    string `json:"name"`
			Listen  string `json:"listen"`
			Connect string `json:"connect"`
			Bind    string `json:"bind"`
			UID     *int   `json:"uid"`
			GID     *int   `json:"gid"`
			Mode    string `json:"mode"`
			Managed bool   `json:"managed"`
		} `json:"published_sockets"`
	} `json:"results"`
	Warnings []string `json:"warnings"`
	Errors   []struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	} `json:"errors"`
}

func parseSocketsEnvelope(t *testing.T, data []byte) socketsTestEnvelope {
	t.Helper()
	var env socketsTestEnvelope
	if err := json.Unmarshal(data, &env); err != nil {
		t.Fatalf("invalid JSON envelope: %v\noutput: %s", err, data)
	}
	return env
}

func writeSocketsCLIManifest(t *testing.T, path, body string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(body), 0644); err != nil {
		t.Fatalf("writing %s: %v", path, err)
	}
}

func socketContainerManifest(name, socketsBlock string) string {
	body := "schema: lxm/config/v2\nname: " + name + "\nimage: ubuntu:24.04\n"
	if socketsBlock != "" {
		body += "sockets:\n" + socketsBlock
	}
	return body
}

func TestRun_Sockets_PlanApplyRemoveLifecycle(t *testing.T) {
	driver := fake.New()
	dir := t.TempDir()
	manifest := filepath.Join(dir, "lab1.yaml")
	writeSocketsCLIManifest(t, manifest, socketContainerManifest("lab1", `  - name: broker
    listen: unix:/run/sshigit/broker.sock
    connect: unix:/run/sshigit-broker/lab1.sock
    bind: instance
    uid: 1000
    gid: 1000
    mode: "0660"
`))

	// 1. Plan: create step carries the socket device and a sockets diff.
	var stdout, stderr bytes.Buffer
	code := run([]string{"plan", manifest, "--format", "json"}, &stdout, &stderr, driver)
	if code != 0 {
		t.Fatalf("plan returned %d, want 0. stderr: %s", code, stderr.String())
	}
	env := parseSocketsEnvelope(t, stdout.Bytes())
	if !env.OK || env.ExitCode != 0 {
		t.Fatalf("unexpected plan envelope: ok=%v exit=%d", env.OK, env.ExitCode)
	}
	if len(env.Plan.Steps) != 1 || env.Plan.Steps[0].Action != "create" {
		t.Fatalf("unexpected plan steps: %+v", env.Plan.Steps)
	}
	post := env.Plan.Steps[0].InstancesPost
	if post == nil || post.Devices["socket-broker"] == nil {
		t.Fatalf("plan create payload missing socket-broker: %+v", post)
	}
	dev := post.Devices["socket-broker"]
	if dev["listen"] != "unix:/run/sshigit/broker.sock" ||
		dev["connect"] != "unix:/run/sshigit-broker/lab1.sock" ||
		dev["bind"] != "instance" || dev["uid"] != "1000" || dev["gid"] != "1000" || dev["mode"] != "0660" {
		t.Fatalf("unexpected device payload: %v", dev)
	}
	if dev["user.lxm.kind"] != "socket" || dev["user.lxm.managed"] != "true" {
		t.Fatalf("missing socket namespace markers: %v", dev)
	}
	// Create steps express device intent through the create payload (like
	// ports); per-device diffs are emitted for updates, below.
	// 2. Apply: device is created.
	stdout.Reset()
	stderr.Reset()
	code = run([]string{"apply", manifest, "--format", "json"}, &stdout, &stderr, driver)
	if code != 0 {
		t.Fatalf("apply returned %d, want 0. stderr: %s", code, stderr.String())
	}
	inst := driver.Instances["lab1"]
	if inst == nil || inst.Devices["socket-broker"] == nil {
		t.Fatalf("socket-broker device not created: %+v", inst)
	}

	// 3. Second plan: noop (idempotent).
	stdout.Reset()
	stderr.Reset()
	code = run([]string{"plan", manifest, "--format", "json"}, &stdout, &stderr, driver)
	if code != 0 {
		t.Fatalf("second plan returned %d, want 0. stderr: %s", code, stderr.String())
	}
	env = parseSocketsEnvelope(t, stdout.Bytes())
	if len(env.Plan.Steps) != 1 || env.Plan.Steps[0].Action != "noop" {
		t.Fatalf("expected noop after apply, got %+v", env.Plan.Steps)
	}

	// 3b. A mode change is a field-level diff in plan JSON.
	writeSocketsCLIManifest(t, manifest, socketContainerManifest("lab1", `  - name: broker
    listen: unix:/run/sshigit/broker.sock
    connect: unix:/run/sshigit-broker/lab1.sock
    bind: instance
    uid: 1000
    gid: 1000
    mode: "0600"
`))
	stdout.Reset()
	stderr.Reset()
	code = run([]string{"plan", manifest, "--format", "json"}, &stdout, &stderr, driver)
	if code != 0 {
		t.Fatalf("mode-change plan returned %d, want 0. stderr: %s", code, stderr.String())
	}
	env = parseSocketsEnvelope(t, stdout.Bytes())
	changeFound := false
	for _, d := range env.Plan.Steps[0].Diff {
		if d["field"] == "sockets[broker].mode" && d["new"] == "0600" {
			changeFound = true
		}
	}
	if !changeFound {
		t.Fatalf("plan JSON missing sockets[broker].mode diff: %+v", env.Plan.Steps[0].Diff)
	}
	// Restore the live-applied mode for the list/status assertions below.
	writeSocketsCLIManifest(t, manifest, socketContainerManifest("lab1", `  - name: broker
    listen: unix:/run/sshigit/broker.sock
    connect: unix:/run/sshigit-broker/lab1.sock
    bind: instance
    uid: 1000
    gid: 1000
    mode: "0660"
`))

	// 4. List JSON surfaces the published socket.
	stdout.Reset()
	stderr.Reset()
	code = run([]string{"list", "--format", "json"}, &stdout, &stderr, driver)
	if code != 0 {
		t.Fatalf("list returned %d, want 0. stderr: %s", code, stderr.String())
	}
	env = parseSocketsEnvelope(t, stdout.Bytes())
	if len(env.Results) != 1 || len(env.Results[0].PublishedSockets) != 1 {
		t.Fatalf("list did not surface published sockets: %+v", env.Results)
	}
	pub := env.Results[0].PublishedSockets[0]
	if pub.Device != "socket-broker" || pub.Name != "broker" || !pub.Managed ||
		pub.Bind != "instance" || pub.Mode != "0660" ||
		pub.Listen != "unix:/run/sshigit/broker.sock" || pub.Connect != "unix:/run/sshigit-broker/lab1.sock" {
		t.Fatalf("unexpected published socket: %+v", pub)
	}
	if pub.UID == nil || *pub.UID != 1000 || pub.GID == nil || *pub.GID != 1000 {
		t.Fatalf("unexpected published socket ids: %+v", pub)
	}

	// 5. List text includes the SOCKETS column/value.
	stdout.Reset()
	stderr.Reset()
	code = run([]string{"list"}, &stdout, &stderr, driver)
	if code != 0 {
		t.Fatalf("list text returned %d, want 0. stderr: %s", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "SOCKETS") ||
		!strings.Contains(stdout.String(), "unix:/run/sshigit/broker.sock->unix:/run/sshigit-broker/lab1.sock (instance)") {
		t.Fatalf("list text missing sockets column: %s", stdout.String())
	}

	// 6. Status text + JSON surface the published socket.
	stdout.Reset()
	stderr.Reset()
	code = run([]string{"status", "lab1", "--format", "json"}, &stdout, &stderr, driver)
	if code != 0 {
		t.Fatalf("status returned %d, want 0. stderr: %s", code, stderr.String())
	}
	env = parseSocketsEnvelope(t, stdout.Bytes())
	if len(env.Results) != 1 || len(env.Results[0].PublishedSockets) != 1 {
		t.Fatalf("status JSON missing published sockets: %+v", env.Results)
	}

	stdout.Reset()
	stderr.Reset()
	code = run([]string{"status", "lab1"}, &stdout, &stderr, driver)
	if code != 0 {
		t.Fatalf("status text returned %d, want 0. stderr: %s", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "Unix Sockets:") || !strings.Contains(stdout.String(), "bind=instance, uid=1000, gid=1000, mode=0660, managed") {
		t.Fatalf("status text missing sockets block: %s", stdout.String())
	}

	// 7. Removing the socket deletes the device on the next apply.
	writeSocketsCLIManifest(t, manifest, socketContainerManifest("lab1", ""))
	stdout.Reset()
	stderr.Reset()
	code = run([]string{"apply", manifest, "--format", "json"}, &stdout, &stderr, driver)
	if code != 0 {
		t.Fatalf("apply removal returned %d, want 0. stderr: %s", code, stderr.String())
	}
	if _, ok := driver.Instances["lab1"].Devices["socket-broker"]; ok {
		t.Fatalf("removed socket device still present: %+v", driver.Instances["lab1"].Devices)
	}
}

func TestRun_Sockets_DuplicateHostPathRejected(t *testing.T) {
	driver := fake.New()
	dir := t.TempDir()
	writeSocketsCLIManifest(t, filepath.Join(dir, "a.yaml"), socketContainerManifest("a", `  - {name: broker, listen: "unix:/run/a.sock", connect: "unix:/run/shared.sock", bind: instance}
`))
	writeSocketsCLIManifest(t, filepath.Join(dir, "b.yaml"), socketContainerManifest("b", `  - {name: broker, listen: "unix:/run/b.sock", connect: "unix:/run/shared.sock", bind: instance}
`))

	var stdout, stderr bytes.Buffer
	code := run([]string{"plan", dir}, &stdout, &stderr, driver)
	if code != 3 {
		t.Fatalf("duplicate host path returned %d, want 3. stderr: %s", code, stderr.String())
	}
	if !strings.Contains(stderr.String(), "host-side unix socket path") {
		t.Fatalf("missing duplicate-host-path diagnostic: %s", stderr.String())
	}
}

func TestRun_Sockets_ValidationRejected(t *testing.T) {
	driver := fake.New()
	dir := t.TempDir()
	manifest := filepath.Join(dir, "bad.yaml")
	writeSocketsCLIManifest(t, manifest, `schema: lxm/config/v2
name: bad
type: virtual-machine
image: ubuntu:24.04
vm: {boot_mode: uefi-nosecureboot}
sockets:
  - {name: broker, listen: "unix:/run/broker.sock", connect: "unix:/run/host.sock"}
`)
	var stdout, stderr bytes.Buffer
	code := run([]string{"plan", manifest}, &stdout, &stderr, driver)
	if code != 3 {
		t.Fatalf("VM socket returned %d, want 3. stderr: %s", code, stderr.String())
	}
	if !strings.Contains(stderr.String(), "container-only") {
		t.Fatalf("missing container-only diagnostic: %s", stderr.String())
	}
}

func TestRun_Sockets_UnmarkedDeviceCollisionRejected(t *testing.T) {
	driver := fake.New()
	driver.Instances["lab1"] = &provider.Instance{
		Name:   "lab1",
		Status: "Running",
		Config: map[string]string{"user.lxm.user": "ubuntu", "user.lxm.image": "ubuntu:24.04"},
		Devices: map[string]map[string]string{
			"socket-broker": {"type": "proxy", "listen": "unix:/run/broker.sock", "connect": "unix:/run/host.sock"},
		},
	}
	dir := t.TempDir()
	manifest := filepath.Join(dir, "lab1.yaml")
	writeSocketsCLIManifest(t, manifest, socketContainerManifest("lab1", `  - {name: broker, listen: "unix:/run/broker.sock", connect: "unix:/run/host.sock"}
`))

	var stdout, stderr bytes.Buffer
	code := run([]string{"plan", manifest}, &stdout, &stderr, driver)
	if code != 3 {
		t.Fatalf("unmarked collision returned %d, want 3. stderr: %s", code, stderr.String())
	}
	if !strings.Contains(stderr.String(), "is not owned by the sockets namespace") {
		t.Fatalf("missing ownership diagnostic: %s", stderr.String())
	}
}

func TestRun_Sockets_CapabilityGate(t *testing.T) {
	dir := t.TempDir()
	manifest := filepath.Join(dir, "lab1.yaml")
	writeSocketsCLIManifest(t, manifest, socketContainerManifest("lab1", `  - {name: broker, listen: "unix:/run/broker.sock", connect: "unix:/run/host.sock", uid: 1000, mode: "0600"}
`))

	t.Run("proxy_unix missing", func(t *testing.T) {
		driver := fake.New()
		delete(driver.Extensions, "proxy_unix")
		var stdout, stderr bytes.Buffer
		code := run([]string{"plan", manifest}, &stdout, &stderr, driver)
		if code != 4 {
			t.Fatalf("missing proxy_unix returned %d, want 4. stderr: %s", code, stderr.String())
		}
		if !strings.Contains(stderr.String(), "proxy_unix extension") {
			t.Fatalf("missing capability diagnostic: %s", stderr.String())
		}
	})

	t.Run("proxy_unix_dac_properties missing", func(t *testing.T) {
		driver := fake.New()
		delete(driver.Extensions, "proxy_unix_dac_properties")
		var stdout, stderr bytes.Buffer
		code := run([]string{"plan", manifest}, &stdout, &stderr, driver)
		if code != 4 {
			t.Fatalf("missing proxy_unix_dac_properties returned %d, want 4. stderr: %s", code, stderr.String())
		}
		if !strings.Contains(stderr.String(), "proxy_unix_dac_properties extension") {
			t.Fatalf("missing DAC capability diagnostic: %s", stderr.String())
		}
	})

	t.Run("no DAC options needs no DAC extension", func(t *testing.T) {
		driver := fake.New()
		delete(driver.Extensions, "proxy_unix_dac_properties")
		plain := filepath.Join(dir, "plain.yaml")
		writeSocketsCLIManifest(t, plain, socketContainerManifest("plain", `  - {name: broker, listen: "unix:/run/broker.sock", connect: "unix:/run/host.sock"}
`))
		var stdout, stderr bytes.Buffer
		code := run([]string{"plan", plain}, &stdout, &stderr, driver)
		if code != 0 {
			t.Fatalf("plain socket returned %d, want 0. stderr: %s", code, stderr.String())
		}
	})
}

func TestRun_Sockets_HandoffWarning(t *testing.T) {
	driver := fake.New()
	driver.Instances["a"] = &provider.Instance{
		Name:   "a",
		Status: "Running",
		Config: map[string]string{"user.lxm.user": "ubuntu", "user.lxm.image": "ubuntu:24.04"},
		Devices: map[string]map[string]string{
			"socket-listener": {
				"type":             "proxy",
				"listen":           "unix:/run/shared.sock",
				"connect":          "unix:/run/instance.sock",
				"bind":             "host",
				"user.lxm.managed": "true",
				"user.lxm.kind":    "socket",
			},
		},
	}
	dir := t.TempDir()
	writeSocketsCLIManifest(t, filepath.Join(dir, "a.yaml"), socketContainerManifest("a", ""))
	writeSocketsCLIManifest(t, filepath.Join(dir, "b.yaml"), socketContainerManifest("b", `  - {name: listener, listen: "unix:/run/shared.sock", connect: "unix:/run/instance.sock"}
`))

	var stdout, stderr bytes.Buffer
	code := run([]string{"plan", dir, "--format", "json"}, &stdout, &stderr, driver)
	if code != 0 {
		t.Fatalf("handoff plan returned %d, want 0. stderr: %s", code, stderr.String())
	}
	env := parseSocketsEnvelope(t, stdout.Bytes())
	found := false
	for _, w := range env.Warnings {
		if strings.Contains(w, "unix socket path") && strings.Contains(w, `moves from instance "a" to instance "b"`) {
			found = true
		}
	}
	if !found {
		t.Fatalf("missing socket handoff warning; warnings=%v stdout=%s", env.Warnings, stdout.String())
	}
}

func TestRun_Sockets_RebuildSecondApplyConverges(t *testing.T) {
	// Native daemon rebuild (instances_rebuild) does not PUT devices. A socket
	// change in the same step as an image recreate therefore lands on the next
	// apply; this pins the documented convergence behaviour (SOCKETS-SPEC §8,
	// mirroring the ports test).
	driver := fake.New()
	driver.Instances["lab1"] = &provider.Instance{
		Name:   "lab1",
		Status: "Running",
		Config: map[string]string{
			"user.lxm.user":  "ubuntu",
			"user.lxm.image": "debian:12",
			"image.os":       "debian",
			"image.release":  "12",
		},
		Devices: map[string]map[string]string{
			"socket-broker": {
				"type":             "proxy",
				"listen":           "unix:/run/old.sock",
				"connect":          "unix:/run/host.sock",
				"bind":             "host",
				"user.lxm.managed": "true",
				"user.lxm.kind":    "socket",
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
	manifest := filepath.Join(dir, "lab1.yaml")
	writeSocketsCLIManifest(t, manifest, `schema: lxm/config/v2
name: lab1
image: jammy
user: ubuntu
sockets:
  - {name: broker, listen: "unix:/run/new.sock", connect: "unix:/run/host.sock", bind: host}
`)

	var stdout, stderr bytes.Buffer
	if code := run([]string{"apply", manifest, "--format", "json"}, &stdout, &stderr, driver); code != 0 {
		t.Fatalf("first apply returned %d, want 0. stderr: %s", code, stderr.String())
	}
	first := parseSocketsEnvelope(t, stdout.Bytes())
	if len(first.Plan.Steps) != 1 || first.Plan.Steps[0].Action != "recreate" {
		t.Fatalf("expected recreate step, got %+v", first.Plan.Steps)
	}
	if got := driver.Instances["lab1"].Devices["socket-broker"]["listen"]; got != "unix:/run/old.sock" {
		t.Fatalf("fake rebuild should leave devices untouched, got listen %q", got)
	}

	stdout.Reset()
	stderr.Reset()
	if code := run([]string{"plan", manifest, "--format", "json"}, &stdout, &stderr, driver); code != 0 {
		t.Fatalf("second plan returned %d, want 0. stderr: %s", code, stderr.String())
	}
	second := parseSocketsEnvelope(t, stdout.Bytes())
	if len(second.Plan.Steps) != 1 || second.Plan.Steps[0].Action != "update" {
		t.Fatalf("expected a deferred socket update, got %+v", second.Plan.Steps)
	}

	stdout.Reset()
	stderr.Reset()
	if code := run([]string{"apply", manifest, "--format", "json"}, &stdout, &stderr, driver); code != 0 {
		t.Fatalf("second apply returned %d, want 0. stderr: %s", code, stderr.String())
	}
	if got := driver.Instances["lab1"].Devices["socket-broker"]["listen"]; got != "unix:/run/new.sock" {
		t.Fatalf("second apply did not converge device listen, got %q", got)
	}

	stdout.Reset()
	stderr.Reset()
	if code := run([]string{"plan", manifest, "--format", "json"}, &stdout, &stderr, driver); code != 0 {
		t.Fatalf("third plan returned %d, want 0. stderr: %s", code, stderr.String())
	}
	third := parseSocketsEnvelope(t, stdout.Bytes())
	if len(third.Plan.Steps) != 1 || third.Plan.Steps[0].Action != "noop" {
		t.Fatalf("expected noop after second apply, got %+v", third.Plan.Steps)
	}
}
