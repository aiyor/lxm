package fleet

import (
	"testing"

	"github.com/aiyor/lxm/internal/provider"
	"github.com/aiyor/lxm/internal/provider/fake"
	"github.com/aiyor/lxm/internal/sockets"
)

func TestGetInventory_PublishedSockets(t *testing.T) {
	ctx := t.Context()
	driver := fake.New()
	driver.Instances["lab1"] = &provider.Instance{
		Name:   "lab1",
		Status: "Running",
		Config: map[string]string{"user.lxm.managed": "true"},
		Devices: map[string]map[string]string{
			"socket-broker": sockets.BuildDevice(
				mustSocketEndpoint(t, "unix:/run/broker.sock"),
				mustSocketEndpoint(t, "unix:/run/host/broker.sock"),
				sockets.BindInstance, intPtr(1000), nil, "0660",
			),
			// Foreign (unmarked) unix proxy device: surfaced as unmanaged.
			"handmade": {"type": "proxy", "listen": "unix:/run/a.sock", "connect": "unix:/run/b.sock", "bind": "host"},
			// TCP proxy devices belong to published_ports, not sockets.
			"proxy-web": {"type": "proxy", "listen": "tcp:127.0.0.1:8080", "connect": "tcp:127.0.0.1:80"},
		},
	}

	inv, err := GetInventory(ctx, driver)
	if err != nil {
		t.Fatalf("GetInventory: %v", err)
	}
	if len(inv.Instances) != 1 {
		t.Fatalf("expected 1 instance, got %d", len(inv.Instances))
	}
	published := inv.Instances[0].PublishedSockets
	if len(published) != 2 {
		t.Fatalf("expected 2 published sockets, got %+v", published)
	}
	if published[0].Device != "handmade" || published[0].Managed || published[0].Name != "" {
		t.Fatalf("unexpected foreign entry: %+v", published[0])
	}
	broker := published[1]
	if broker.Device != "socket-broker" || !broker.Managed || broker.Name != "broker" {
		t.Fatalf("unexpected managed entry: %+v", broker)
	}
	if broker.Bind != sockets.BindInstance || broker.Mode != "0660" {
		t.Fatalf("unexpected socket options: %+v", broker)
	}
	if broker.UID == nil || *broker.UID != 1000 {
		t.Fatalf("unexpected uid: %+v", broker)
	}
	if len(inv.Instances[0].PublishedPorts) != 1 || inv.Instances[0].PublishedPorts[0].Device != "proxy-web" {
		t.Fatalf("ports projection must stay TCP/UDP-only: %+v", inv.Instances[0].PublishedPorts)
	}
}

func TestGetInventory_ProfilePublishedSockets(t *testing.T) {
	ctx := t.Context()
	driver := fake.New()
	driver.Instances["lab1"] = &provider.Instance{
		Name:    "lab1",
		Status:  "Running",
		Config:  map[string]string{"user.lxm.managed": "true"},
		Devices: map[string]map[string]string{},
		ExpandedDevices: map[string]map[string]string{
			"profile-socket": {"type": "proxy", "listen": "unix:/run/a.sock", "connect": "unix:/run/b.sock", "bind": "host"},
		},
	}
	inv, err := GetInventory(ctx, driver)
	if err != nil {
		t.Fatalf("GetInventory: %v", err)
	}
	published := inv.Instances[0].PublishedSockets
	if len(published) != 1 || published[0].Device != "profile-socket" || published[0].Managed || published[0].Name != "" {
		t.Fatalf("expected profile-provided foreign socket in published sockets, got %+v", published)
	}
}

func mustSocketEndpoint(t *testing.T, raw string) sockets.Endpoint {
	t.Helper()
	ep, err := sockets.ParseEndpoint(raw)
	if err != nil {
		t.Fatalf("ParseEndpoint(%q): %v", raw, err)
	}
	return ep
}

func intPtr(value int) *int { return &value }
