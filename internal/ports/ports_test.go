package ports_test

import (
	"net"
	"reflect"
	"strconv"
	"strings"
	"testing"

	"github.com/aiyor/lxm/internal/ports"
)

func TestParseEndpoint_Valid(t *testing.T) {
	tests := []struct {
		name      string
		proto     string
		raw       string
		wantHost  string
		wantProto string
		wantLoop  bool
		wantWild  bool
		wantIPv6  bool
	}{
		{name: "ipv4", proto: "tcp", raw: "127.0.0.1:8080", wantHost: "127.0.0.1:8080", wantProto: "tcp", wantLoop: true},
		{name: "udp", proto: "udp", raw: "10.0.0.5:5353", wantHost: "10.0.0.5:5353", wantProto: "udp"},
		{name: "ipv6 loopback", proto: "tcp", raw: "[::1]:80", wantHost: "[::1]:80", wantProto: "tcp", wantLoop: true, wantIPv6: true},
		{name: "ipv6 compressed", proto: "tcp", raw: "[2001:db8::1]:443", wantHost: "[2001:db8::1]:443", wantProto: "tcp", wantIPv6: true},
		{name: "ipv4 wildcard", proto: "tcp", raw: "0.0.0.0:8080", wantHost: "0.0.0.0:8080", wantProto: "tcp", wantWild: true},
		{name: "ipv6 wildcard", proto: "udp", raw: "[::]:5353", wantHost: "[::]:5353", wantProto: "udp", wantWild: true, wantIPv6: true},
		{name: "default proto", proto: "", raw: "127.0.0.1:9000", wantHost: "127.0.0.1:9000", wantProto: "tcp", wantLoop: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ep, err := ports.ParseEndpoint(tt.proto, tt.raw)
			if err != nil {
				t.Fatalf("ParseEndpoint(%q, %q) error: %v", tt.proto, tt.raw, err)
			}
			if got := ep.HostPort(); got != tt.wantHost {
				t.Fatalf("HostPort = %q, want %q", got, tt.wantHost)
			}
			if ep.Proto != tt.wantProto {
				t.Fatalf("Proto = %q, want %q", ep.Proto, tt.wantProto)
			}
			if got := ep.IsLoopback(); got != tt.wantLoop {
				t.Fatalf("IsLoopback = %v, want %v", got, tt.wantLoop)
			}
			if got := ep.IsWildcard(); got != tt.wantWild {
				t.Fatalf("IsWildcard = %v, want %v", got, tt.wantWild)
			}
			if got := ep.IsIPv6(); got != tt.wantIPv6 {
				t.Fatalf("IsIPv6 = %v, want %v", got, tt.wantIPv6)
			}
		})
	}
}

func TestParseEndpoint_Invalid(t *testing.T) {
	tests := []struct {
		name  string
		proto string
		raw   string
		want  string
	}{
		{name: "empty", proto: "tcp", raw: "", want: "address is required"},
		{name: "no port", proto: "tcp", raw: "127.0.0.1", want: "explicit IP:port"},
		{name: "missing host", proto: "tcp", raw: ":8080", want: "explicit IP:port"},
		{name: "hostname", proto: "tcp", raw: "localhost:8080", want: "explicit IP:port"},
		{name: "unbracketed ipv6", proto: "tcp", raw: "::1:8080", want: "explicit IP:port"},
		{name: "protocol prefix", proto: "tcp", raw: "tcp:127.0.0.1:80", want: "do not prefix the protocol"},
		{name: "zero port", proto: "tcp", raw: "127.0.0.1:0", want: "port must be in 1-65535"},
		{name: "4in6", proto: "tcp", raw: "[::ffff:1.2.3.4]:80", want: "4-in-6"},
		{name: "zone", proto: "tcp", raw: "[fe80::1%eth0]:80", want: "zone"},
		{name: "bad proto", proto: "sctp", raw: "127.0.0.1:80", want: "invalid protocol"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := ports.ParseEndpoint(tt.proto, tt.raw)
			if err == nil {
				t.Fatalf("ParseEndpoint(%q, %q) succeeded, want error containing %q", tt.proto, tt.raw, tt.want)
			}
			if !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("error = %q, want containing %q", err, tt.want)
			}
		})
	}
}

func TestDeviceString(t *testing.T) {
	ep, err := ports.ParseEndpoint("udp", "[2001:db8::1]:53")
	if err != nil {
		t.Fatal(err)
	}
	if got, want := ep.DeviceString(), "udp:[2001:db8::1]:53"; got != want {
		t.Fatalf("DeviceString = %q, want %q", got, want)
	}
}

func TestBuildAndParseDevice(t *testing.T) {
	listen, err := ports.ParseEndpoint("tcp", "127.0.0.1:8080")
	if err != nil {
		t.Fatal(err)
	}
	connect, err := ports.ParseEndpoint("tcp", "127.0.0.1:80")
	if err != nil {
		t.Fatal(err)
	}

	props := ports.BuildDevice(listen, connect, false)
	if props["type"] != ports.ProxyType {
		t.Fatalf("type = %q", props["type"])
	}
	if props["listen"] != "tcp:127.0.0.1:8080" || props["connect"] != "tcp:127.0.0.1:80" {
		t.Fatalf("unexpected device endpoints: %v", props)
	}
	if props[ports.ManagedKey] != ports.ManagedValue {
		t.Fatalf("missing managed marker: %v", props)
	}
	if _, ok := props["nat"]; ok {
		t.Fatalf("nat must be omitted when false: %v", props)
	}
	if !ports.IsManagedProxy("proxy-web", props) {
		t.Fatal("IsManagedProxy = false for managed device")
	}

	dev, ok := ports.ParseProxy("proxy-web", props)
	if !ok {
		t.Fatalf("ParseProxy failed for %v", props)
	}
	if dev.Name != "web" || dev.NAT || dev.Listen.HostPort() != "127.0.0.1:8080" || dev.Connect.HostPort() != "127.0.0.1:80" {
		t.Fatalf("unexpected parsed device: %+v", dev)
	}

	natProps := ports.BuildDevice(listen, connect, true)
	if natProps["nat"] != "true" {
		t.Fatalf("nat = %q, want true", natProps["nat"])
	}
	dev, ok = ports.ParseProxy("proxy-web", natProps)
	if !ok || !dev.NAT {
		t.Fatalf("parsed NAT device = %+v ok=%v", dev, ok)
	}

	// Foreign-named proxy devices parse with an empty derived name.
	dev, ok = ports.ParseProxy("my-proxy", props)
	if !ok || dev.Name != "" {
		t.Fatalf("foreign-named device = %+v ok=%v", dev, ok)
	}
	if ports.IsManagedProxy("my-proxy", props) != true {
		t.Fatal("ownership must be marker-based, not name-based")
	}

	// Malformed / foreign devices.
	if _, ok := ports.ParseProxy("web", map[string]string{"type": "disk"}); ok {
		t.Fatal("non-proxy device parsed as proxy")
	}
	bad := map[string]string{"type": "proxy", "listen": "127.0.0.1:80", "connect": "tcp:127.0.0.1:80"}
	if _, ok := ports.ParseProxy("proxy-web", bad); ok {
		t.Fatal("bear listen without proto prefix parsed")
	}
	bad = map[string]string{"type": "proxy", "listen": "tcp:127.0.0.1:80", "connect": "tcp:127.0.0.1:80", "nat": "maybe"}
	if _, ok := ports.ParseProxy("proxy-web", bad); ok {
		t.Fatal("invalid nat value parsed")
	}
}

func TestParseBool(t *testing.T) {
	trues := []string{"1", "true", "TRUE", "yes", "Yes", "on", "ON"}
	falses := []string{"", "0", "false", "FALSE", "no", "off"}
	for _, v := range trues {
		got, ok := ports.ParseBool(v)
		if !ok || !got {
			t.Fatalf("ParseBool(%q) = %v, %v; want true, true", v, got, ok)
		}
	}
	for _, v := range falses {
		got, ok := ports.ParseBool(v)
		if !ok || got {
			t.Fatalf("ParseBool(%q) = %v, %v; want false, true", v, got, ok)
		}
	}
	if _, ok := ports.ParseBool("maybe"); ok {
		t.Fatal("ParseBool(maybe) ok = true, want false")
	}
}

func TestListenersConflict(t *testing.T) {
	mk := func(proto, addr string, port uint16, nat bool) ports.Listener {
		ep, err := ports.ParseEndpoint(proto, netipHostPort(addr, port))
		if err != nil {
			t.Fatalf("parse %s: %v", addr, err)
		}
		return ports.NewListener(ep, nat)
	}

	tests := []struct {
		name string
		a    ports.Listener
		b    ports.Listener
		want bool
	}{
		{name: "exact v4", a: mk("tcp", "127.0.0.1", 8080, false), b: mk("tcp", "127.0.0.1", 8080, false), want: true},
		{name: "different port", a: mk("tcp", "127.0.0.1", 8080, false), b: mk("tcp", "127.0.0.1", 8081, false), want: false},
		{name: "different proto", a: mk("tcp", "127.0.0.1", 8080, false), b: mk("udp", "127.0.0.1", 8080, false), want: false},
		{name: "v4 vs v6 loopback", a: mk("tcp", "127.0.0.1", 8080, false), b: mk("tcp", "::1", 8080, false), want: false},
		{name: "v4 wildcard vs v4 loopback", a: mk("tcp", "0.0.0.0", 8080, false), b: mk("tcp", "127.0.0.1", 8080, false), want: true},
		{name: "v4 wildcard vs v6 loopback", a: mk("tcp", "0.0.0.0", 8080, false), b: mk("tcp", "::1", 8080, false), want: true},
		{name: "v6 wildcard vs v4 loopback", a: mk("tcp", "::", 8080, false), b: mk("tcp", "127.0.0.1", 8080, false), want: true},
		{name: "wildcards", a: mk("tcp", "0.0.0.0", 8080, false), b: mk("tcp", "::", 8080, false), want: true},
		{name: "nat exact", a: mk("tcp", "10.0.0.1", 8443, true), b: mk("tcp", "10.0.0.1", 8443, true), want: true},
		{name: "nat different addr", a: mk("tcp", "10.0.0.1", 8443, true), b: mk("tcp", "10.0.0.2", 8443, true), want: false},
		{name: "nat vs non-nat same addr", a: mk("tcp", "10.0.0.1", 8443, true), b: mk("tcp", "10.0.0.1", 8443, false), want: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := ports.ListenersConflict(tt.a, tt.b); got != tt.want {
				t.Fatalf("ListenersConflict = %v, want %v (a=%+v b=%+v)", got, tt.want, tt.a, tt.b)
			}
			if got := ports.ListenersConflict(tt.b, tt.a); got != tt.want {
				t.Fatalf("ListenersConflict reversed = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestPublished(t *testing.T) {
	listen, _ := ports.ParseEndpoint("tcp", "127.0.0.1:8080")
	connect, _ := ports.ParseEndpoint("tcp", "127.0.0.1:80")
	devices := map[string]map[string]string{
		"proxy-web":   ports.BuildDevice(listen, connect, false),
		"proxy-empty": nil,
		"disk-root":   {"type": "disk", "path": "/"},
		"eth0":        {"type": "nic", "network": "lxdbr0"},
	}
	foreignListen, _ := ports.ParseEndpoint("tcp", "127.0.0.1:9090")
	foreignConnect, _ := ports.ParseEndpoint("tcp", "127.0.0.1:90")
	devices["outside"] = map[string]string{
		"type":    "proxy",
		"listen":  foreignListen.DeviceString(),
		"connect": foreignConnect.DeviceString(),
	}

	got := ports.Published(devices)
	if len(got) != 2 {
		t.Fatalf("Published returned %d entries, want 2: %+v", len(got), got)
	}
	if got[0].Device != "outside" || got[0].Managed || got[0].Name != "" {
		t.Fatalf("unexpected foreign entry: %+v", got[0])
	}
	if got[1].Device != "proxy-web" || !got[1].Managed || got[1].Name != "web" || got[1].Listen != "127.0.0.1:8080" {
		t.Fatalf("unexpected managed entry: %+v", got[1])
	}
	if !reflect.DeepEqual(got, ports.Published(devices)) {
		t.Fatal("Published is not deterministic")
	}

	if entries := ports.Published(nil); entries != nil {
		t.Fatalf("Published(nil) = %v, want nil", entries)
	}
}

func netipHostPort(addr string, port uint16) string {
	return net.JoinHostPort(addr, strconv.Itoa(int(port)))
}

func TestPublished_ExcludesSocketNamespaceDevices(t *testing.T) {
	// A hand-edited sockets-namespace device with TCP endpoints must not leak
	// into published_ports; that projection is TCP/UDP-ports-namespace only.
	devices := map[string]map[string]string{
		"proxy-web": {
			"type":             "proxy",
			"listen":           "tcp:127.0.0.1:8080",
			"connect":          "tcp:127.0.0.1:80",
			"user.lxm.managed": "true",
		},
		"socket-weird": {
			"type":             "proxy",
			"listen":           "tcp:127.0.0.1:9090",
			"connect":          "tcp:127.0.0.1:90",
			"user.lxm.managed": "true",
			"user.lxm.kind":    "socket",
		},
	}
	published := ports.Published(devices)
	if len(published) != 1 {
		t.Fatalf("Published returned %d entries, want 1: %+v", len(published), published)
	}
	if published[0].Device != "proxy-web" {
		t.Fatalf("socket-namespace device leaked into published_ports: %+v", published)
	}
}
