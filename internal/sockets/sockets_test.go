package sockets_test

import (
	"reflect"
	"strings"
	"testing"

	"github.com/aiyor/lxm/internal/proxydevice"
	"github.com/aiyor/lxm/internal/sockets"
)

func mustEndpoint(t *testing.T, raw string) sockets.Endpoint {
	t.Helper()
	ep, err := sockets.ParseEndpoint(raw)
	if err != nil {
		t.Fatalf("ParseEndpoint(%q): %v", raw, err)
	}
	return ep
}

func intPtr(v int) *int { return &v }

func TestParseEndpoint_Valid(t *testing.T) {
	tests := []struct {
		name string
		raw  string
		want string
	}{
		{name: "absolute", raw: "unix:/run/app.sock", want: "unix:/run/app.sock"},
		{name: "nested", raw: "unix:/run/sshigit/broker.sock", want: "unix:/run/sshigit/broker.sock"},
		{name: "colon in path", raw: "unix:/run/app:1.sock", want: "unix:/run/app:1.sock"},
		{name: "spaces trimmed", raw: "  unix:/tmp/a.sock  ", want: "unix:/tmp/a.sock"},
		{name: "max length 107", raw: "unix:/" + strings.Repeat("a", 106), want: "unix:/" + strings.Repeat("a", 106)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ep, err := sockets.ParseEndpoint(tt.raw)
			if err != nil {
				t.Fatalf("ParseEndpoint(%q): %v", tt.raw, err)
			}
			if got := ep.DeviceString(); got != tt.want {
				t.Fatalf("DeviceString() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestParseEndpoint_Invalid(t *testing.T) {
	overlong := "unix:/" + strings.Repeat("a", 107) // 108-byte path > 107-byte sun_path limit
	tests := []struct {
		name string
		raw  string
		want string
	}{
		{name: "empty", raw: "", want: "endpoint is required"},
		{name: "blank", raw: "   ", want: "endpoint is required"},
		{name: "missing scheme", raw: "/run/app.sock", want: "must be a unix:"},
		{name: "tcp", raw: "tcp:127.0.0.1:8080", want: "use ports:"},
		{name: "udp", raw: "udp:127.0.0.1:5353", want: "use ports:"},
		{name: "unknown scheme", raw: "vsock:10", want: "unsupported endpoint scheme"},
		{name: "empty path", raw: "unix:", want: "socket path is required"},
		{name: "abstract", raw: "unix:@lxm", want: "abstract unix sockets"},
		{name: "relative", raw: "unix:app.sock", want: "must be absolute"},
		{name: "dot segment", raw: "unix:/run/../tmp/app.sock", want: "must be clean"},
		{name: "dot", raw: "unix:/run/./app.sock", want: "must be clean"},
		{name: "trailing slash", raw: "unix:/run/app.sock/", want: "must be clean"},
		{name: "double slash", raw: "unix://run/app.sock", want: "must be clean"},
		{name: "nul", raw: "unix:/run/app\x00.sock", want: "control characters"},
		{name: "newline", raw: "unix:/run/app\n.sock", want: "control characters"},
		{name: "overlong", raw: overlong, want: "sun_path limit"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := sockets.ParseEndpoint(tt.raw)
			if err == nil {
				t.Fatalf("ParseEndpoint(%q) succeeded, want error containing %q", tt.raw, tt.want)
			}
			if !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("ParseEndpoint(%q) error = %q, want containing %q", tt.raw, err, tt.want)
			}
		})
	}
}

func TestParseBind(t *testing.T) {
	tests := []struct {
		raw  string
		want string
		ok   bool
	}{
		{"", sockets.BindHost, true},
		{"host", sockets.BindHost, true},
		{"HOST", sockets.BindHost, true},
		{"instance", sockets.BindInstance, true},
		{"guest", sockets.BindInstance, true},
		{"container", sockets.BindInstance, true},
		{"both", "", false},
	}
	for _, tt := range tests {
		got, ok := sockets.ParseBind(tt.raw)
		if got != tt.want || ok != tt.ok {
			t.Errorf("ParseBind(%q) = (%q, %v), want (%q, %v)", tt.raw, got, ok, tt.want, tt.ok)
		}
	}
	if err := sockets.ValidateBind("both"); err == nil || !strings.Contains(err.Error(), "must be host or instance") {
		t.Errorf("ValidateBind(both) = %v, want targeted error", err)
	}
}

func TestParseMode(t *testing.T) {
	valid := map[string]string{
		"0660": "0660",
		"660":  "0660",
		"0600": "0600",
		"600":  "0600",
		"0000": "0000",
		"0777": "0777",
		"777":  "0777",
	}
	// Surrounding whitespace is trimmed before validation.
	if got, err := sockets.ParseMode(" 660 "); err != nil || got != "0660" {
		t.Errorf("ParseMode(trimmed) = (%q, %v), want (0660, nil)", got, err)
	}
	for raw, want := range valid {
		got, err := sockets.ParseMode(raw)
		if err != nil {
			t.Errorf("ParseMode(%q): %v", raw, err)
			continue
		}
		if got != want {
			t.Errorf("ParseMode(%q) = %q, want %q", raw, got, want)
		}
	}

	invalid := []string{"", "0", "99", "0899", "0999", "07777", "800", "6600", "0o660", "rw-rw----", "0778"}
	for _, raw := range invalid {
		if got, err := sockets.ParseMode(raw); err == nil {
			t.Errorf("ParseMode(%q) = %q, want error", raw, got)
		}
	}
}

func TestValidateID(t *testing.T) {
	for _, ok := range []int{0, 1000, sockets.MaxID} {
		if err := sockets.ValidateID("uid", ok); err != nil {
			t.Errorf("ValidateID(uid, %d): %v", ok, err)
		}
	}
	for _, bad := range []int{-1, sockets.MaxID + 1} {
		if err := sockets.ValidateID("uid", bad); err == nil {
			t.Errorf("ValidateID(uid, %d) succeeded, want error", bad)
		}
	}
}

func TestBuildDevice(t *testing.T) {
	listen := mustEndpoint(t, "unix:/run/broker.sock")
	connect := mustEndpoint(t, "unix:/run/host/broker.sock")

	host := sockets.BuildDevice(listen, connect, sockets.BindHost, nil, nil, "")
	wantHost := map[string]string{
		"type":                 "proxy",
		"listen":               "unix:/run/broker.sock",
		"connect":              "unix:/run/host/broker.sock",
		"bind":                 "host",
		proxydevice.ManagedKey: proxydevice.ManagedValue,
		proxydevice.KindKey:    proxydevice.KindSocket,
	}
	if !reflect.DeepEqual(host, wantHost) {
		t.Fatalf("host device = %#v, want %#v", host, wantHost)
	}

	instance := sockets.BuildDevice(listen, connect, sockets.BindInstance, intPtr(1000), intPtr(1000), "0660")
	wantInstance := map[string]string{
		"type":                 "proxy",
		"listen":               "unix:/run/broker.sock",
		"connect":              "unix:/run/host/broker.sock",
		"bind":                 "instance",
		"uid":                  "1000",
		"gid":                  "1000",
		"mode":                 "0660",
		proxydevice.ManagedKey: proxydevice.ManagedValue,
		proxydevice.KindKey:    proxydevice.KindSocket,
	}
	if !reflect.DeepEqual(instance, wantInstance) {
		t.Fatalf("instance device = %#v, want %#v", instance, wantInstance)
	}
}

func TestParseProxy_RoundTrip(t *testing.T) {
	listen := mustEndpoint(t, "unix:/run/broker.sock")
	connect := mustEndpoint(t, "unix:/run/host/broker.sock")
	props := sockets.BuildDevice(listen, connect, sockets.BindInstance, intPtr(1000), nil, "600")

	dev, ok := sockets.ParseProxy("socket-broker", props)
	if !ok {
		t.Fatalf("ParseProxy failed for canonical device: %v", props)
	}
	want := sockets.Device{
		Name:    "broker",
		Listen:  listen,
		Connect: connect,
		Bind:    sockets.BindInstance,
		UID:     intPtr(1000),
		Mode:    "0600",
	}
	if !reflect.DeepEqual(dev, want) {
		t.Fatalf("ParseProxy = %#v, want %#v", dev, want)
	}
	if got := dev.HostPath(); got != "/run/host/broker.sock" {
		t.Errorf("HostPath = %q, want host-side connect path", got)
	}
	if got := dev.InstanceListenPath(); got != "/run/broker.sock" {
		t.Errorf("InstanceListenPath = %q", got)
	}

	hostDev := sockets.Device{Listen: listen, Connect: connect, Bind: sockets.BindHost}
	if got := hostDev.HostPath(); got != "/run/broker.sock" {
		t.Errorf("host HostPath = %q, want listen path", got)
	}
	if got := hostDev.InstanceListenPath(); got != "" {
		t.Errorf("host InstanceListenPath = %q, want empty", got)
	}
}

func TestParseProxy_Rejects(t *testing.T) {
	_ = proxydevice.KindSocket
	listen := "unix:/run/broker.sock"
	connect := "unix:/run/host/broker.sock"
	canonical := func(mutators ...func(map[string]string)) map[string]string {
		props := sockets.BuildDevice(mustEndpoint(t, listen), mustEndpoint(t, connect), sockets.BindHost, nil, nil, "")
		for _, mutate := range mutators {
			mutate(props)
		}
		return props
	}

	tests := []struct {
		name  string
		props map[string]string
	}{
		{name: "non-proxy", props: map[string]string{"type": "disk"}},
		{name: "foreign", props: map[string]string{"type": "proxy", "listen": listen, "connect": connect}},
		{name: "missing marker", props: map[string]string{"type": "proxy", "kind": proxydevice.KindSocket}},
		{name: "tcp listen", props: canonical(func(m map[string]string) { m["listen"] = "tcp:127.0.0.1:80" })},
		{name: "tcp connect", props: canonical(func(m map[string]string) { m["connect"] = "tcp:127.0.0.1:80" })},
		{name: "bad bind", props: canonical(func(m map[string]string) { m["bind"] = "guest-of-the-state" })},
		{name: "nat true", props: canonical(func(m map[string]string) { m["nat"] = "true" })},
		{name: "bad nat", props: canonical(func(m map[string]string) { m["nat"] = "maybe" })},
		{name: "bad uid", props: canonical(func(m map[string]string) { m["uid"] = "2147483648" })},
		{name: "bad gid", props: canonical(func(m map[string]string) { m["gid"] = "-1" })},
		{name: "bad mode", props: canonical(func(m map[string]string) { m["mode"] = "999" })},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if dev, ok := sockets.ParseProxy("socket-broker", tt.props); ok {
				t.Fatalf("ParseProxy accepted invalid device: %#v", dev)
			}
		})
	}
}

func TestIsManagedSocket(t *testing.T) {
	props := sockets.BuildDevice(mustEndpoint(t, "unix:/a.sock"), mustEndpoint(t, "unix:/b.sock"), sockets.BindHost, nil, nil, "")
	if !sockets.IsManagedSocket(props) {
		t.Fatal("canonical socket device not reported as managed")
	}

	// A ports device (no kind marker) must never be claimed by the sockets
	// namespace, and vice versa.
	portDevice := map[string]string{
		"type":                 "proxy",
		proxydevice.ManagedKey: proxydevice.ManagedValue,
		"listen":               "tcp:127.0.0.1:80",
	}
	if sockets.IsManagedSocket(portDevice) {
		t.Fatal("ports device claimed by sockets namespace")
	}
}

func TestPublished(t *testing.T) {
	devices := map[string]map[string]string{
		"socket-broker": sockets.BuildDevice(
			mustEndpoint(t, "unix:/run/broker.sock"),
			mustEndpoint(t, "unix:/run/host/broker.sock"),
			sockets.BindInstance, intPtr(1000), intPtr(1000), "0660"),
		// Foreign Unix proxy device (no lxm markers) is surfaced as unmanaged.
		"handmade": {"type": "proxy", "listen": "unix:/run/a.sock", "connect": "unix:/run/b.sock"},
		// Ports and non-proxy devices are outside this projection.
		"proxy-web": {"type": "proxy", "listen": "tcp:127.0.0.1:8080", "connect": "tcp:127.0.0.1:80"},
		"eth0":      {"type": "nic", "name": "eth0"},
		// Malformed managed socket devices are skipped, not surfaced.
		"socket-bad": {proxydevice.Type: "proxy", proxydevice.ManagedKey: proxydevice.ManagedValue,
			proxydevice.KindKey: proxydevice.KindSocket, "listen": "unix:relative.sock", "connect": "unix:/b.sock"},
	}

	got := sockets.Published(devices)
	if len(got) != 2 {
		t.Fatalf("Published returned %d entries, want 2: %#v", len(got), got)
	}
	if got[0].Device != "handmade" || got[0].Managed || got[0].Name != "" {
		t.Fatalf("unexpected foreign entry: %#v", got[0])
	}
	broker := got[1]
	if broker.Device != "socket-broker" || broker.Name != "broker" || !broker.Managed {
		t.Fatalf("unexpected managed entry: %#v", broker)
	}
	if broker.Listen != "unix:/run/broker.sock" || broker.Connect != "unix:/run/host/broker.sock" {
		t.Fatalf("unexpected endpoints: %#v", broker)
	}
	if broker.Bind != sockets.BindInstance || broker.Mode != "0660" {
		t.Fatalf("unexpected bind/mode: %#v", broker)
	}
	if broker.UID == nil || *broker.UID != 1000 || broker.GID == nil || *broker.GID != 1000 {
		t.Fatalf("unexpected uid/gid: %#v", broker)
	}

	if sockets.Published(nil) != nil {
		t.Fatal("Published(nil) must be nil")
	}
}

func TestDeviceName(t *testing.T) {
	if got := sockets.DeviceName("broker"); got != "socket-broker" {
		t.Fatalf("DeviceName = %q", got)
	}
}
