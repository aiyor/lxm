package plan_test

import (
	"strings"
	"testing"

	"github.com/aiyor/lxm/internal/config"
	"github.com/aiyor/lxm/internal/plan"
	"github.com/aiyor/lxm/internal/sockets"
)

func mustSocketEndpoint(t *testing.T, raw string) sockets.Endpoint {
	t.Helper()
	ep, err := sockets.ParseEndpoint(raw)
	if err != nil {
		t.Fatalf("ParseEndpoint(%q): %v", raw, err)
	}
	return ep
}

func socketIntPtr(value int) *int { return &value }

func managedSocketDevice(t *testing.T, listen, connect, bind string, uid, gid *int, mode string) map[string]string {
	t.Helper()
	return sockets.BuildDevice(
		mustSocketEndpoint(t, listen),
		mustSocketEndpoint(t, connect),
		bind, uid, gid, mode,
	)
}

func socketSpec(name, listen, connect string) config.SocketConfig {
	return config.SocketConfig{Name: name, Listen: listen, Connect: connect, Bind: sockets.BindHost}
}

func socketManifest(name string, specs ...config.SocketConfig) *config.Config {
	manifest := baseContainer(name)
	manifest.Sockets = specs
	return manifest
}

func TestCompute_Sockets_CreatePayload(t *testing.T) {
	manifest := socketManifest("lab1",
		config.SocketConfig{
			Name:    "broker",
			Listen:  "unix:/run/sshigit/broker.sock",
			Connect: "unix:/run/sshigit-broker/lab1.sock",
			Bind:    sockets.BindInstance,
			UID:     socketIntPtr(1000),
			GID:     socketIntPtr(1000),
			Mode:    "0660",
		},
	)
	p, err := plan.NewReconciler().Compute(manifest, nil, nil, nil, nil, true)
	if err != nil {
		t.Fatalf("Compute: %v", err)
	}
	if len(p.Steps) != 1 || p.Steps[0].InstancesPost == nil {
		t.Fatalf("expected create step: %+v", p.Steps)
	}
	dev := p.Steps[0].InstancesPost.Devices["socket-broker"]
	if dev == nil {
		t.Fatalf("socket-broker device missing: %v", p.Steps[0].InstancesPost.Devices)
	}
	want := map[string]string{
		"type":             "proxy",
		"listen":           "unix:/run/sshigit/broker.sock",
		"connect":          "unix:/run/sshigit-broker/lab1.sock",
		"bind":             "instance",
		"uid":              "1000",
		"gid":              "1000",
		"mode":             "0660",
		sockets.ManagedKey: sockets.ManagedValue,
		sockets.KindKey:    sockets.Kind,
	}
	for key, value := range want {
		if dev[key] != value {
			t.Fatalf("device[%q] = %q, want %q (device: %v)", key, dev[key], value, dev)
		}
	}
}

func TestCompute_Sockets_UpdateDiffAndPayload(t *testing.T) {
	manifest := socketManifest("lab1", socketSpec("broker", "unix:/run/broker2.sock", "unix:/run/host2.sock"))
	live := &plan.InstanceSnapshot{
		Name:   "lab1",
		Status: "Running",
		Config: map[string]string{"user.lxm.user": "ubuntu"},
		Devices: map[string]map[string]string{
			"socket-broker": managedSocketDevice(t, "unix:/run/broker.sock", "unix:/run/host.sock", sockets.BindHost, nil, nil, ""),
		},
	}
	p, err := plan.NewReconciler().Compute(manifest, map[string]*plan.InstanceSnapshot{"lab1": live}, nil, nil, nil, true)
	if err != nil {
		t.Fatalf("Compute: %v", err)
	}
	step := p.Steps[0]
	if step.Action != "update" {
		t.Fatalf("action = %q, want update (diff: %+v)", step.Action, step.Diff)
	}
	if len(step.Diff) != 2 {
		t.Fatalf("expected listen+connect diffs only, got %+v", step.Diff)
	}
	fields := make(map[string]plan.FieldDiff, len(step.Diff))
	for _, d := range step.Diff {
		fields[d.Field] = d
	}
	if d, ok := fields["sockets[broker].listen"]; !ok || d.Old != "unix:/run/broker.sock" || d.New != "unix:/run/broker2.sock" {
		t.Fatalf("unexpected listen diff: %+v", fields["sockets[broker].listen"])
	}
	if _, ok := fields["sockets[broker].connect"]; !ok {
		t.Fatalf("missing connect diff: %+v", step.Diff)
	}
	put := step.InstancePut
	if put == nil || put.Devices["socket-broker"] == nil {
		t.Fatalf("update payload missing socket-broker: %+v", put)
	}
	if put.Devices["socket-broker"]["listen"] != "unix:/run/broker2.sock" {
		t.Fatalf("update payload not rebuilt: %v", put.Devices["socket-broker"])
	}
	if step.PowerTransition != "" {
		t.Fatalf("socket-only update must not restart, got %q", step.PowerTransition)
	}
}

func TestCompute_Sockets_RemovalDeletesDevice(t *testing.T) {
	manifest := socketManifest("lab1")
	live := &plan.InstanceSnapshot{
		Name:   "lab1",
		Status: "Running",
		Config: map[string]string{"user.lxm.user": "ubuntu"},
		Devices: map[string]map[string]string{
			"socket-broker": managedSocketDevice(t, "unix:/run/broker.sock", "unix:/run/host.sock", sockets.BindInstance, nil, nil, ""),
		},
	}
	p, err := plan.NewReconciler().Compute(manifest, map[string]*plan.InstanceSnapshot{"lab1": live}, nil, nil, nil, true)
	if err != nil {
		t.Fatalf("Compute: %v", err)
	}
	step := p.Steps[0]
	if step.Action != "update" {
		t.Fatalf("action = %q, diff=%+v", step.Action, step.Diff)
	}
	if step.InstancePut == nil || step.InstancePut.Devices["socket-broker"] != nil {
		t.Fatalf("retired socket must be absent from PUT: %+v", step.InstancePut)
	}
	found := false
	for _, d := range step.Diff {
		if d.Field == "sockets[broker]" && d.New == nil {
			found = true
		}
	}
	if !found {
		t.Fatalf("missing socket removal diff: %+v", step.Diff)
	}
}

func TestCompute_Sockets_ForeignProxyPreserved(t *testing.T) {
	manifest := socketManifest("lab1")
	manifest.User = "admin" // force an update
	live := &plan.InstanceSnapshot{
		Name:   "lab1",
		Status: "Running",
		Config: map[string]string{"user.lxm.user": "ubuntu"},
		Devices: map[string]map[string]string{
			"handmade": {"type": "proxy", "listen": "unix:/run/a.sock", "connect": "unix:/run/b.sock", "bind": "host"},
		},
	}
	p, err := plan.NewReconciler().Compute(manifest, map[string]*plan.InstanceSnapshot{"lab1": live}, nil, nil, nil, true)
	if err != nil {
		t.Fatalf("Compute: %v", err)
	}
	put := p.Steps[0].InstancePut
	if put == nil {
		t.Fatalf("expected update payload: %+v", p.Steps[0])
	}
	if put.Devices["handmade"] == nil {
		t.Fatalf("foreign unix proxy must be preserved: %v", put.Devices)
	}
	for _, d := range p.Steps[0].Diff {
		if strings.HasPrefix(d.Field, "sockets[") {
			t.Fatalf("foreign device must not be diffed: %+v", d)
		}
	}
}

func TestCompute_Sockets_UnmarkedCollisionRejected(t *testing.T) {
	manifest := socketManifest("lab1", socketSpec("broker", "unix:/run/broker.sock", "unix:/run/host.sock"))
	live := &plan.InstanceSnapshot{
		Name:   "lab1",
		Status: "Running",
		Config: map[string]string{"user.lxm.user": "ubuntu"},
		Devices: map[string]map[string]string{
			"socket-broker": {"type": "proxy", "listen": "unix:/run/broker.sock", "connect": "unix:/run/host.sock"},
		},
	}
	_, err := plan.NewReconciler().Compute(manifest, map[string]*plan.InstanceSnapshot{"lab1": live}, nil, nil, nil, true)
	if err == nil || !strings.Contains(err.Error(), "is not owned by the sockets namespace") {
		t.Fatalf("expected ownership conflict error, got: %v", err)
	}
}

func TestCompute_Sockets_IdempotentWhenMatching(t *testing.T) {
	manifest := socketManifest("lab1",
		config.SocketConfig{
			Name:    "broker",
			Listen:  "unix:/run/broker.sock",
			Connect: "unix:/run/host.sock",
			Bind:    sockets.BindInstance,
			UID:     socketIntPtr(1000),
			Mode:    "0660",
		},
	)
	live := &plan.InstanceSnapshot{
		Name:   "lab1",
		Status: "Running",
		Config: map[string]string{"user.lxm.user": "ubuntu"},
		Devices: map[string]map[string]string{
			"socket-broker": managedSocketDevice(t, "unix:/run/broker.sock", "unix:/run/host.sock", sockets.BindInstance, socketIntPtr(1000), nil, "0660"),
		},
	}
	p, err := plan.NewReconciler().Compute(manifest, map[string]*plan.InstanceSnapshot{"lab1": live}, nil, nil, nil, true)
	if err != nil {
		t.Fatalf("Compute: %v", err)
	}
	if p.Steps[0].Action != "noop" {
		t.Fatalf("expected noop, got %q with diff %+v", p.Steps[0].Action, p.Steps[0].Diff)
	}
}

func TestCompute_Sockets_MalformedManagedDeviceRepaired(t *testing.T) {
	manifest := socketManifest("lab1", socketSpec("broker", "unix:/run/broker.sock", "unix:/run/host.sock"))
	live := &plan.InstanceSnapshot{
		Name:   "lab1",
		Status: "Running",
		Config: map[string]string{"user.lxm.user": "ubuntu"},
		Devices: map[string]map[string]string{
			"socket-broker": {
				"type":             "proxy",
				"listen":           "unix:relative.sock",
				"connect":          "unix:/run/host.sock",
				"bind":             "host",
				sockets.ManagedKey: sockets.ManagedValue,
				sockets.KindKey:    sockets.Kind,
			},
		},
	}
	p, err := plan.NewReconciler().Compute(manifest, map[string]*plan.InstanceSnapshot{"lab1": live}, nil, nil, nil, true)
	if err != nil {
		t.Fatalf("Compute: %v", err)
	}
	step := p.Steps[0]
	if step.Action != "update" || step.InstancePut == nil || step.InstancePut.Devices["socket-broker"] == nil {
		t.Fatalf("malformed managed device must be repaired: action=%q put=%+v", step.Action, step.InstancePut)
	}
	if step.InstancePut.Devices["socket-broker"]["listen"] != "unix:/run/broker.sock" {
		t.Fatalf("repaired device should carry the manifest payload: %v", step.InstancePut.Devices["socket-broker"])
	}
	var repairs []plan.FieldDiff
	for _, d := range step.Diff {
		if d.Field == "sockets[broker]" {
			repairs = append(repairs, d)
		}
	}
	if len(repairs) != 1 || repairs[0].New == nil {
		t.Fatalf("expected exactly one repair diff, got %+v", step.Diff)
	}
}

func TestCompute_Sockets_MalformedManagedDeviceRemoved(t *testing.T) {
	manifest := socketManifest("lab1")
	live := &plan.InstanceSnapshot{
		Name:   "lab1",
		Status: "Running",
		Config: map[string]string{"user.lxm.user": "ubuntu"},
		Devices: map[string]map[string]string{
			"socket-broker": {
				"type":             "proxy",
				"listen":           "not-a-unix-endpoint",
				sockets.ManagedKey: sockets.ManagedValue,
				sockets.KindKey:    sockets.Kind,
			},
		},
	}
	p, err := plan.NewReconciler().Compute(manifest, map[string]*plan.InstanceSnapshot{"lab1": live}, nil, nil, nil, true)
	if err != nil {
		t.Fatalf("Compute: %v", err)
	}
	step := p.Steps[0]
	if step.Action != "update" || step.InstancePut == nil || step.InstancePut.Devices["socket-broker"] != nil {
		t.Fatalf("malformed managed device must be removed: action=%q put=%+v", step.Action, step.InstancePut)
	}
}

func TestCompute_Sockets_NamespacePartitionWithPorts(t *testing.T) {
	// A manifest that declares both a TCP port and a socket must diff each
	// device in its own namespace; neither reconciler may claim (and
	// rewrite/delete) the other's device.
	manifest := baseContainer("lab1")
	manifest.Ports = []config.PortConfig{portSpec("web", "127.0.0.1:8080", "127.0.0.1:80")}
	manifest.Sockets = []config.SocketConfig{socketSpec("broker", "unix:/run/broker.sock", "unix:/run/host.sock")}
	live := &plan.InstanceSnapshot{
		Name:   "lab1",
		Status: "Running",
		Config: map[string]string{"user.lxm.user": "ubuntu"},
		Devices: map[string]map[string]string{
			"proxy-web":     managedDevice(t, "127.0.0.1:8080", "127.0.0.1:80", false),
			"socket-broker": managedSocketDevice(t, "unix:/run/broker.sock", "unix:/run/host.sock", sockets.BindHost, nil, nil, ""),
		},
	}
	p, err := plan.NewReconciler().Compute(manifest, map[string]*plan.InstanceSnapshot{"lab1": live}, nil, nil, nil, true)
	if err != nil {
		t.Fatalf("Compute: %v", err)
	}
	if p.Steps[0].Action != "noop" {
		t.Fatalf("expected noop, got %q with diff %+v", p.Steps[0].Action, p.Steps[0].Diff)
	}

	// Removing only the socket must leave the port device untouched and delete
	// only socket-broker.
	manifest.Sockets = nil
	p, err = plan.NewReconciler().Compute(manifest, map[string]*plan.InstanceSnapshot{"lab1": live}, nil, nil, nil, true)
	if err != nil {
		t.Fatalf("Compute: %v", err)
	}
	put := p.Steps[0].InstancePut
	if put == nil || put.Devices["socket-broker"] != nil || put.Devices["proxy-web"] == nil {
		t.Fatalf("namespace partition failed: %+v", put)
	}
}

func TestCompute_Sockets_RecreatePayloadIncludesSockets(t *testing.T) {
	manifest := socketManifest("lab1", socketSpec("broker", "unix:/run/broker.sock", "unix:/run/host.sock"))
	manifest.Image = "jammy"
	live := &plan.InstanceSnapshot{
		Name:   "lab1",
		Status: "Running",
		Config: map[string]string{"user.lxm.user": "ubuntu", "user.lxm.image": "debian:12", "image.os": "debian", "image.release": "12"},
	}
	p, err := plan.NewReconciler().Compute(manifest, map[string]*plan.InstanceSnapshot{"lab1": live}, nil, nil, nil, true)
	if err != nil {
		t.Fatalf("Compute: %v", err)
	}
	step := p.Steps[0]
	if step.Action != "recreate" {
		t.Fatalf("expected recreate, got %q (diff %+v)", step.Action, step.Diff)
	}
	if step.InstancesPost == nil || step.InstancesPost.Devices["socket-broker"] == nil {
		t.Fatalf("recreate payload missing socket device: %+v", step.InstancesPost)
	}
}

func TestCompute_Sockets_Warnings(t *testing.T) {
	manifest := socketManifest("lab1", socketSpec("listener", "unix:/run/listen.sock", "unix:/run/instance.sock"))
	p, err := plan.NewReconciler().Compute(manifest, nil, nil, nil, nil, true)
	if err != nil {
		t.Fatalf("Compute: %v", err)
	}
	foundHost := false
	for _, w := range p.Warnings {
		if strings.Contains(w, "bind: host") && strings.Contains(w, "bypassing network_policy") {
			foundHost = true
		}
	}
	if !foundHost {
		t.Fatalf("expected bind: host exposure warning, got %v", p.Warnings)
	}

	instanceOnly := socketManifest("lab2", config.SocketConfig{
		Name:    "broker",
		Listen:  "unix:/run/broker.sock",
		Connect: "unix:/run/host.sock",
		Bind:    sockets.BindInstance,
	})
	p, err = plan.NewReconciler().Compute(instanceOnly, nil, nil, nil, nil, true)
	if err != nil {
		t.Fatalf("Compute: %v", err)
	}
	for _, w := range p.Warnings {
		if strings.Contains(w, "bind: host") {
			t.Fatalf("bind: instance must not emit the host-listener warning: %v", p.Warnings)
		}
	}
}

func TestCompute_Sockets_DriftAndProfileShadowWarnings(t *testing.T) {
	manifest := socketManifest("lab1",
		socketSpec("broker", "unix:/run/broker.sock", "unix:/run/host.sock"),
		socketSpec("agent", "unix:/run/agent.sock", "unix:/run/host-agent.sock"),
	)
	live := &plan.InstanceSnapshot{
		Name:   "lab1",
		Status: "Running",
		Config: map[string]string{"user.lxm.user": "ubuntu"},
		Devices: map[string]map[string]string{
			"socket-broker": {
				"type":             "proxy",
				"listen":           "unix:/run/broker.sock",
				"connect":          "unix:/run/host.sock",
				"bind":             "guest", // legacy alias for instance; canonical form rewrites it
				"extra":            "property",
				sockets.ManagedKey: sockets.ManagedValue,
				sockets.KindKey:    sockets.Kind,
			},
		},
		ExpandedDevices: map[string]map[string]string{
			"socket-agent": {"type": "proxy", "listen": "unix:/run/other.sock", "connect": "unix:/run/h.sock"},
		},
	}
	p, err := plan.NewReconciler().Compute(manifest, map[string]*plan.InstanceSnapshot{"lab1": live}, nil, nil, nil, true)
	if err != nil {
		t.Fatalf("Compute: %v", err)
	}
	var drift, shadow bool
	for _, w := range p.Warnings {
		if strings.Contains(w, "non-canonical device properties") {
			drift = true
		}
		if strings.Contains(w, "profile-provided device") {
			shadow = true
		}
	}
	if !drift {
		t.Fatalf("expected drift warning, got %v", p.Warnings)
	}
	if !shadow {
		t.Fatalf("expected profile-shadow warning, got %v", p.Warnings)
	}

	// The legacy "guest" bind alias parses as instance, so besides the repair
	// entry a bind field diff must be reported against the canonical value.
	var bindDiff *plan.FieldDiff
	for i, d := range p.Steps[0].Diff {
		if d.Field == "sockets[broker].bind" {
			bindDiff = &p.Steps[0].Diff[i]
		}
	}
	if bindDiff == nil || bindDiff.Old != sockets.BindInstance || bindDiff.New != sockets.BindHost {
		t.Fatalf("unexpected bind diff: %+v", bindDiff)
	}
}

func TestCompute_Sockets_DeterministicDiffOrder(t *testing.T) {
	for range 25 {
		manifest := socketManifest("lab1",
			socketSpec("zeta", "unix:/run/z.sock", "unix:/run/zh.sock"),
			socketSpec("alpha", "unix:/run/a.sock", "unix:/run/ah.sock"),
		)
		live := &plan.InstanceSnapshot{
			Name:   "lab1",
			Status: "Running",
			Config: map[string]string{"user.lxm.user": "ubuntu"},
			Devices: map[string]map[string]string{
				"socket-zeta":  managedSocketDevice(t, "unix:/run/z-old.sock", "unix:/run/zh.sock", sockets.BindHost, nil, nil, ""),
				"socket-alpha": managedSocketDevice(t, "unix:/run/a-old.sock", "unix:/run/ah.sock", sockets.BindHost, nil, nil, ""),
			},
		}
		p, err := plan.NewReconciler().Compute(manifest, map[string]*plan.InstanceSnapshot{"lab1": live}, nil, nil, nil, true)
		if err != nil {
			t.Fatalf("Compute: %v", err)
		}
		var order []string
		for _, d := range p.Steps[0].Diff {
			if strings.HasSuffix(d.Field, ".listen") {
				order = append(order, d.Field)
			}
		}
		want := []string{"sockets[alpha].listen", "sockets[zeta].listen"}
		if len(order) != 2 || order[0] != want[0] || order[1] != want[1] {
			t.Fatalf("non-deterministic diff order: %v", order)
		}
	}
}

func TestDetectSocketHandoffs(t *testing.T) {
	configs := []*config.Config{
		socketManifest("a"),
		socketManifest("b", socketSpec("listener", "unix:/run/shared.sock", "unix:/run/b.sock")),
	}
	live := map[string]*plan.InstanceSnapshot{
		"a": {
			Name:   "a",
			Status: "Running",
			Devices: map[string]map[string]string{
				"socket-listener": managedSocketDevice(t, "unix:/run/shared.sock", "unix:/run/a.sock", sockets.BindHost, nil, nil, ""),
			},
		},
		"b": {Name: "b", Status: "Running", Devices: map[string]map[string]string{}},
	}
	steps := []plan.Step{
		{Container: "a", Action: "update"},
		{Container: "b", Action: "create"},
	}
	warnings := plan.DetectListenerHandoffs(configs, live, steps)
	found := false
	for _, w := range warnings {
		if strings.Contains(w, "unix socket path") && strings.Contains(w, `"a" to instance "b"`) {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected socket path handoff warning, got %v", warnings)
	}

	// bind=instance host-side connect paths are not host listeners; moving the
	// same connect target between instances is not a handoff race.
	instanceConfigs := []*config.Config{
		{Name: "a", Sockets: []config.SocketConfig{{Name: "broker", Listen: "unix:/run/a.sock", Connect: "unix:/run/shared.sock", Bind: sockets.BindInstance}}},
		{Name: "b", Sockets: []config.SocketConfig{{Name: "broker", Listen: "unix:/run/b.sock", Connect: "unix:/run/shared.sock", Bind: sockets.BindInstance}}},
	}
	instanceLive := map[string]*plan.InstanceSnapshot{
		"a": {Name: "a", Status: "Running", Devices: map[string]map[string]string{
			"socket-broker": managedSocketDevice(t, "unix:/run/a.sock", "unix:/run/shared.sock", sockets.BindInstance, nil, nil, ""),
		}},
		"b": {Name: "b", Status: "Running", Devices: map[string]map[string]string{}},
	}
	for _, w := range plan.DetectListenerHandoffs(instanceConfigs, instanceLive, steps) {
		if strings.Contains(w, "unix socket path") {
			t.Fatalf("bind=instance connect move must not warn: %v", w)
		}
	}
}

func TestCompute_Sockets_RenamedMarkerDeviceRemoved(t *testing.T) {
	// A marker-carrying socket device under a non-canonical name can never
	// satisfy a manifest socket: it is retired with a devices[<device>] diff
	// (outside the sockets[<name>] namespace) while the declared socket is
	// created normally.
	manifest := socketManifest("lab1", socketSpec("broker", "unix:/run/broker.sock", "unix:/run/host.sock"))
	live := &plan.InstanceSnapshot{
		Name:   "lab1",
		Status: "Running",
		Config: map[string]string{"user.lxm.user": "ubuntu"},
		Devices: map[string]map[string]string{
			"handmade-socket": {
				"type":             "proxy",
				"listen":           "unix:/run/x.sock",
				"connect":          "unix:/run/y.sock",
				"bind":             "host",
				"user.lxm.managed": "true",
				"user.lxm.kind":    "socket",
			},
		},
	}
	p, err := plan.NewReconciler().Compute(manifest, map[string]*plan.InstanceSnapshot{"lab1": live}, nil, nil, nil, true)
	if err != nil {
		t.Fatalf("Compute: %v", err)
	}
	step := p.Steps[0]
	if step.Action != "update" {
		t.Fatalf("action = %q, want update (diff: %+v)", step.Action, step.Diff)
	}
	var removed, added bool
	for _, d := range step.Diff {
		switch d.Field {
		case "devices[handmade-socket]":
			removed = true
			if d.New != nil {
				t.Fatalf("anomalous device removal must have New == nil: %+v", d)
			}
		case "sockets[broker]":
			added = d.New != nil
		}
	}
	if !removed {
		t.Fatalf("missing devices[handmade-socket] removal diff: %+v", step.Diff)
	}
	if !added {
		t.Fatalf("missing sockets[broker] add diff: %+v", step.Diff)
	}
	put := step.InstancePut
	if put == nil {
		t.Fatalf("expected update payload: %+v", step)
	}
	if put.Devices["handmade-socket"] != nil {
		t.Fatalf("anomalous managed device must be retired: %v", put.Devices)
	}
	if put.Devices["socket-broker"] == nil {
		t.Fatalf("declared socket device missing from PUT: %v", put.Devices)
	}
}
