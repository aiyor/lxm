package plan_test

import (
	"strings"
	"testing"

	"github.com/aiyor/lxm/internal/config"
	"github.com/aiyor/lxm/internal/plan"
	"github.com/aiyor/lxm/internal/ports"
)

func mustEndpoint(t *testing.T, proto, raw string) ports.Endpoint {
	t.Helper()
	ep, err := ports.ParseEndpoint(proto, raw)
	if err != nil {
		t.Fatalf("ParseEndpoint(%q, %q): %v", proto, raw, err)
	}
	return ep
}

func managedDevice(t *testing.T, listen, connect string, nat bool) map[string]string {
	t.Helper()
	return ports.BuildDevice(mustEndpoint(t, "tcp", listen), mustEndpoint(t, "tcp", connect), nat)
}

func portSpec(name, listen, connect string) config.PortConfig {
	return config.PortConfig{Name: name, Listen: listen, Connect: connect, Proto: "tcp"}
}

func baseContainer(name string) *config.Config {
	return &config.Config{Name: name, Type: "container", User: "ubuntu"}
}

func TestCompute_Ports_CreatePayload(t *testing.T) {
	manifest := baseContainer("web-a")
	manifest.Ports = []config.PortConfig{
		portSpec("web", "127.0.0.1:8080", "127.0.0.1:80"),
	}
	rec := plan.NewReconciler()
	p, err := rec.Compute(manifest, nil, nil, nil, nil, true)
	if err != nil {
		t.Fatalf("Compute: %v", err)
	}
	if len(p.Steps) != 1 || p.Steps[0].InstancesPost == nil {
		t.Fatalf("expected create step: %+v", p.Steps)
	}
	dev := p.Steps[0].InstancesPost.Devices["proxy-web"]
	if dev == nil {
		t.Fatalf("proxy-web device missing: %v", p.Steps[0].InstancesPost.Devices)
	}
	if dev["type"] != "proxy" || dev["listen"] != "tcp:127.0.0.1:8080" || dev["connect"] != "tcp:127.0.0.1:80" {
		t.Fatalf("unexpected device: %v", dev)
	}
	if dev[ports.ManagedKey] != ports.ManagedValue {
		t.Fatalf("missing managed marker: %v", dev)
	}
	if _, ok := dev["nat"]; ok {
		t.Fatalf("nat must be omitted when false: %v", dev)
	}
}

func TestCompute_Ports_NATDevice(t *testing.T) {
	manifest := baseContainer("web-a")
	manifest.Ports = []config.PortConfig{
		{Name: "https", Listen: "10.10.10.1:8443", Connect: "10.10.10.31:443", Proto: "tcp", NAT: true},
	}
	p, err := plan.NewReconciler().Compute(manifest, nil, nil, nil, nil, true)
	if err != nil {
		t.Fatalf("Compute: %v", err)
	}
	dev := p.Steps[0].InstancesPost.Devices["proxy-https"]
	if dev["nat"] != "true" {
		t.Fatalf("expected nat=true device, got %v", dev)
	}
}

func TestCompute_Ports_UpdateDiffAndPayload(t *testing.T) {
	manifest := baseContainer("web-a")
	manifest.Ports = []config.PortConfig{portSpec("web", "127.0.0.1:8080", "127.0.0.1:80")}

	live := &plan.InstanceSnapshot{
		Name:   "web-a",
		Status: "Running",
		Config: map[string]string{"user.lxm.user": "ubuntu"},
		Devices: map[string]map[string]string{
			"proxy-web": managedDevice(t, "127.0.0.1:8081", "127.0.0.1:80", false),
		},
	}
	p, err := plan.NewReconciler().Compute(manifest, map[string]*plan.InstanceSnapshot{"web-a": live}, nil, nil, nil, true)
	if err != nil {
		t.Fatalf("Compute: %v", err)
	}
	step := p.Steps[0]
	if step.Action != "update" {
		t.Fatalf("action = %q, want update (diff: %+v)", step.Action, step.Diff)
	}
	wantField := "ports[web].listen"
	found := false
	for _, d := range step.Diff {
		if d.Field == wantField {
			found = true
			if d.Old != "127.0.0.1:8081" || d.New != "127.0.0.1:8080" {
				t.Fatalf("unexpected listen diff: %+v", d)
			}
		}
	}
	if !found {
		t.Fatalf("missing %s diff: %+v", wantField, step.Diff)
	}
	if step.InstancePut == nil || step.InstancePut.Devices["proxy-web"] == nil {
		t.Fatalf("update payload missing proxy-web: %+v", step.InstancePut)
	}
	if step.InstancePut.Devices["proxy-web"]["listen"] != "tcp:127.0.0.1:8080" {
		t.Fatalf("update payload not rebuilt: %v", step.InstancePut.Devices["proxy-web"])
	}
	if step.PowerTransition != "" {
		t.Fatalf("device-only update must not restart, got %q", step.PowerTransition)
	}
}

func TestCompute_Ports_RemovalDeletesDevice(t *testing.T) {
	manifest := baseContainer("web-a")
	manifest.Ports = nil

	live := &plan.InstanceSnapshot{
		Name:   "web-a",
		Status: "Running",
		Config: map[string]string{"user.lxm.user": "ubuntu"},
		Devices: map[string]map[string]string{
			"proxy-web": managedDevice(t, "127.0.0.1:8080", "127.0.0.1:80", false),
		},
	}
	p, err := plan.NewReconciler().Compute(manifest, map[string]*plan.InstanceSnapshot{"web-a": live}, nil, nil, nil, true)
	if err != nil {
		t.Fatalf("Compute: %v", err)
	}
	step := p.Steps[0]
	if step.Action != "update" {
		t.Fatalf("action = %q, diff=%+v", step.Action, step.Diff)
	}
	if step.InstancePut == nil || step.InstancePut.Devices["proxy-web"] != nil {
		t.Fatalf("retired proxy must be absent from PUT: %+v", step.InstancePut)
	}
	found := false
	for _, d := range step.Diff {
		if d.Field == "ports[web]" && d.New == nil {
			found = true
		}
	}
	if !found {
		t.Fatalf("missing removal diff: %+v", step.Diff)
	}
}

func TestCompute_Ports_ForeignProxyPreserved(t *testing.T) {
	manifest := baseContainer("web-a")
	manifest.Ports = nil
	live := &plan.InstanceSnapshot{
		Name:   "web-a",
		Status: "Running",
		Config: map[string]string{"user.lxm.user": "ubuntu"},
		Devices: map[string]map[string]string{
			"my-proxy": {"type": "proxy", "listen": "tcp:127.0.0.1:9000", "connect": "tcp:127.0.0.1:90"},
		},
	}
	// Force an update by changing the user.
	manifest.User = "admin"
	p, err := plan.NewReconciler().Compute(manifest, map[string]*plan.InstanceSnapshot{"web-a": live}, nil, nil, nil, true)
	if err != nil {
		t.Fatalf("Compute: %v", err)
	}
	step := p.Steps[0]
	if step.InstancePut == nil {
		t.Fatalf("expected update payload: %+v", step)
	}
	if step.InstancePut.Devices["my-proxy"] == nil {
		t.Fatalf("foreign proxy must be preserved: %v", step.InstancePut.Devices)
	}
}

func TestCompute_Ports_UnmarkedCollisionRejected(t *testing.T) {
	manifest := baseContainer("web-a")
	manifest.Ports = []config.PortConfig{portSpec("web", "127.0.0.1:8080", "127.0.0.1:80")}
	live := &plan.InstanceSnapshot{
		Name:   "web-a",
		Status: "Running",
		Config: map[string]string{"user.lxm.user": "ubuntu"},
		Devices: map[string]map[string]string{
			"proxy-web": {"type": "proxy", "listen": "tcp:127.0.0.1:8080", "connect": "tcp:127.0.0.1:80"},
		},
	}
	_, err := plan.NewReconciler().Compute(manifest, map[string]*plan.InstanceSnapshot{"web-a": live}, nil, nil, nil, true)
	if err == nil || !strings.Contains(err.Error(), "is not lxm-managed") {
		t.Fatalf("expected ownership conflict error, got: %v", err)
	}
}

func TestCompute_Ports_IdempotentWhenMatching(t *testing.T) {
	manifest := baseContainer("web-a")
	manifest.Ports = []config.PortConfig{portSpec("web", "127.0.0.1:8080", "127.0.0.1:80")}
	live := &plan.InstanceSnapshot{
		Name:   "web-a",
		Status: "Running",
		Config: map[string]string{"user.lxm.user": "ubuntu"},
		Devices: map[string]map[string]string{
			"proxy-web": managedDevice(t, "127.0.0.1:8080", "127.0.0.1:80", false),
		},
	}
	p, err := plan.NewReconciler().Compute(manifest, map[string]*plan.InstanceSnapshot{"web-a": live}, nil, nil, nil, true)
	if err != nil {
		t.Fatalf("Compute: %v", err)
	}
	if p.Steps[0].Action != "noop" {
		t.Fatalf("expected noop, got %q with diff %+v", p.Steps[0].Action, p.Steps[0].Diff)
	}
}

func TestCompute_Ports_MalformedManagedDeviceRepaired(t *testing.T) {
	manifest := baseContainer("web-a")
	manifest.Ports = []config.PortConfig{portSpec("web", "127.0.0.1:8080", "127.0.0.1:80")}
	live := &plan.InstanceSnapshot{
		Name:   "web-a",
		Status: "Running",
		Config: map[string]string{"user.lxm.user": "ubuntu"},
		Devices: map[string]map[string]string{
			"proxy-web": {
				"type":           "proxy",
				"listen":         "tcp:127.0.0.1:8080",
				"connect":        "tcp:127.0.0.1:80",
				"nat":            "maybe",
				ports.ManagedKey: ports.ManagedValue,
			},
		},
	}
	p, err := plan.NewReconciler().Compute(manifest, map[string]*plan.InstanceSnapshot{"web-a": live}, nil, nil, nil, true)
	if err != nil {
		t.Fatalf("Compute: %v", err)
	}
	step := p.Steps[0]
	if step.Action != "update" || step.InstancePut == nil || step.InstancePut.Devices["proxy-web"] == nil {
		t.Fatalf("malformed managed device must be repaired: action=%q put=%+v", step.Action, step.InstancePut)
	}
	if step.InstancePut.Devices["proxy-web"]["nat"] != "" {
		t.Fatalf("repaired device should not carry invalid nat: %v", step.InstancePut.Devices["proxy-web"])
	}
}

func TestCompute_Ports_MalformedManagedDeviceRemoved(t *testing.T) {
	manifest := baseContainer("web-a")
	live := &plan.InstanceSnapshot{
		Name:   "web-a",
		Status: "Running",
		Config: map[string]string{"user.lxm.user": "ubuntu"},
		Devices: map[string]map[string]string{
			"proxy-ghost": {
				"type":           "proxy",
				"listen":         "not-an-address",
				ports.ManagedKey: ports.ManagedValue,
			},
		},
	}
	p, err := plan.NewReconciler().Compute(manifest, map[string]*plan.InstanceSnapshot{"web-a": live}, nil, nil, nil, true)
	if err != nil {
		t.Fatalf("Compute: %v", err)
	}
	step := p.Steps[0]
	if step.Action != "update" {
		t.Fatalf("expected update to remove ghost device, got %q", step.Action)
	}
	if step.InstancePut.Devices["proxy-ghost"] != nil {
		t.Fatalf("ghost managed device must not be preserved: %v", step.InstancePut.Devices)
	}
}

func TestCompute_Ports_Warnings(t *testing.T) {
	manifest := baseContainer("web-a")
	manifest.Ports = []config.PortConfig{
		portSpec("public", "0.0.0.0:8080", "127.0.0.1:80"),
	}
	p, err := plan.NewReconciler().Compute(manifest, nil, nil, nil, nil, true)
	if err != nil {
		t.Fatalf("Compute: %v", err)
	}
	found := false
	for _, w := range p.Warnings {
		if strings.Contains(w, "non-loopback") && strings.Contains(w, "IPv6") {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected dual-stack non-loopback warning, got %v", p.Warnings)
	}

	loopback := baseContainer("web-b")
	loopback.Ports = []config.PortConfig{portSpec("web", "127.0.0.1:8080", "127.0.0.1:80")}
	p, err = plan.NewReconciler().Compute(loopback, nil, nil, nil, nil, true)
	if err != nil {
		t.Fatalf("Compute: %v", err)
	}
	for _, w := range p.Warnings {
		if strings.Contains(w, "non-loopback") {
			t.Fatalf("loopback listener must not warn: %v", p.Warnings)
		}
	}
}

func TestCompute_Ports_ProfileShadowWarning(t *testing.T) {
	manifest := baseContainer("web-a")
	manifest.Ports = []config.PortConfig{portSpec("web", "127.0.0.1:8080", "127.0.0.1:80")}
	live := &plan.InstanceSnapshot{
		Name:            "web-a",
		Status:          "Running",
		Config:          map[string]string{"user.lxm.user": "ubuntu"},
		Devices:         map[string]map[string]string{},
		ExpandedDevices: map[string]map[string]string{"proxy-web": {"type": "proxy"}},
	}
	p, err := plan.NewReconciler().Compute(manifest, map[string]*plan.InstanceSnapshot{"web-a": live}, nil, nil, nil, true)
	if err != nil {
		t.Fatalf("Compute: %v", err)
	}
	found := false
	for _, w := range p.Warnings {
		if strings.Contains(w, "profile-provided device") {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected profile-shadow warning, got %v", p.Warnings)
	}
}

func TestDetectListenerHandoffs(t *testing.T) {
	from := &config.Config{Schema: "lxm/config/v2", Name: "a", Status: "present", Ports: nil}
	to := &config.Config{Schema: "lxm/config/v2", Name: "b", Status: "present",
		Ports: []config.PortConfig{portSpec("web", "127.0.0.1:8080", "127.0.0.1:80")}}
	live := map[string]*plan.InstanceSnapshot{
		"a": {
			Name:    "a",
			Devices: map[string]map[string]string{"proxy-web": managedDevice(t, "127.0.0.1:8080", "127.0.0.1:80", false)},
		},
	}
	steps := []plan.Step{
		{Container: "a", Action: "update"},
		{Container: "b", Action: "create"},
	}
	warnings := plan.DetectListenerHandoffs([]*config.Config{from, to}, live, steps)
	if len(warnings) != 1 || !strings.Contains(warnings[0], `moves from instance "a" to instance "b"`) {
		t.Fatalf("unexpected handoff warnings: %v", warnings)
	}

	// Same instance keeping the listener is not a handoff.
	keep := &config.Config{Schema: "lxm/config/v2", Name: "a", Status: "present",
		Ports: []config.PortConfig{portSpec("web", "127.0.0.1:8080", "127.0.0.1:80")}}
	if warnings := plan.DetectListenerHandoffs([]*config.Config{keep}, live, []plan.Step{{Container: "a", Action: "update"}}); len(warnings) != 0 {
		t.Fatalf("same-instance listener should not warn: %v", warnings)
	}

	// Prune/delete handoff.
	deleteSteps := []plan.Step{
		{Container: "a", Action: "delete"},
		{Container: "b", Action: "create"},
	}
	if warnings := plan.DetectListenerHandoffs([]*config.Config{to}, live, deleteSteps); len(warnings) != 1 {
		t.Fatalf("expected delete handoff warning, got %v", warnings)
	}
}

func TestCompute_Ports_RecreatePayloadIncludesPorts(t *testing.T) {
	manifest := baseContainer("web-a")
	manifest.Image = "jammy"
	manifest.Ports = []config.PortConfig{portSpec("web", "127.0.0.1:8080", "127.0.0.1:80")}
	live := &plan.InstanceSnapshot{
		Name:   "web-a",
		Status: "Running",
		Config: map[string]string{"user.lxm.user": "ubuntu", "user.lxm.image": "debian:12", "image.os": "debian", "image.release": "12"},
		Devices: map[string]map[string]string{
			"proxy-web": managedDevice(t, "127.0.0.1:8080", "127.0.0.1:80", false),
		},
	}
	p, err := plan.NewReconciler().Compute(manifest, map[string]*plan.InstanceSnapshot{"web-a": live}, nil, nil, nil, true)
	if err != nil {
		t.Fatalf("Compute: %v", err)
	}
	step := p.Steps[0]
	if step.Action != "recreate" {
		t.Fatalf("expected recreate, got %q (diff %+v)", step.Action, step.Diff)
	}
	if step.InstancesPost == nil || step.InstancesPost.Devices["proxy-web"] == nil {
		t.Fatalf("recreate payload missing proxy device: %+v", step.InstancesPost)
	}
}

func TestCompute_Ports_ForeignPreservedWithManagedRemoval(t *testing.T) {
	manifest := baseContainer("web-a")
	manifest.User = "admin"
	live := &plan.InstanceSnapshot{
		Name:   "web-a",
		Status: "Running",
		Config: map[string]string{"user.lxm.user": "ubuntu"},
		Devices: map[string]map[string]string{
			"proxy-web": managedDevice(t, "127.0.0.1:8080", "127.0.0.1:80", false),
			"my-proxy":  {"type": "proxy", "listen": "tcp:127.0.0.1:9000", "connect": "tcp:127.0.0.1:90"},
		},
	}
	p, err := plan.NewReconciler().Compute(manifest, map[string]*plan.InstanceSnapshot{"web-a": live}, nil, nil, nil, true)
	if err != nil {
		t.Fatalf("Compute: %v", err)
	}
	put := p.Steps[0].InstancePut
	if put == nil {
		t.Fatalf("expected update payload: %+v", p.Steps[0])
	}
	if put.Devices["my-proxy"] == nil {
		t.Fatalf("foreign proxy must be preserved: %v", put.Devices)
	}
	if put.Devices["proxy-web"] != nil {
		t.Fatalf("managed proxy must be removed: %v", put.Devices)
	}
}

func TestCompute_Ports_IdempotentNAT(t *testing.T) {
	manifest := baseContainer("web-a")
	manifest.Ports = []config.PortConfig{
		{Name: "https", Listen: "10.10.10.1:8443", Connect: "10.10.10.31:443", Proto: "tcp", NAT: true},
	}
	live := &plan.InstanceSnapshot{
		Name:   "web-a",
		Status: "Running",
		Config: map[string]string{"user.lxm.user": "ubuntu"},
		Devices: map[string]map[string]string{
			"proxy-https": managedDevice(t, "10.10.10.1:8443", "10.10.10.31:443", true),
		},
	}
	p, err := plan.NewReconciler().Compute(manifest, map[string]*plan.InstanceSnapshot{"web-a": live}, nil, nil, nil, true)
	if err != nil {
		t.Fatalf("Compute: %v", err)
	}
	if p.Steps[0].Action != "noop" {
		t.Fatalf("expected NAT noop, got %q with diff %+v", p.Steps[0].Action, p.Steps[0].Diff)
	}
}
