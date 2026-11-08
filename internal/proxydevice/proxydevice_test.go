package proxydevice_test

import (
	"testing"

	"github.com/aiyor/lxm/internal/proxydevice"
)

func TestIsManaged(t *testing.T) {
	tests := []struct {
		name  string
		props map[string]string
		want  bool
	}{
		{name: "nil", props: nil},
		{name: "non-proxy", props: map[string]string{"type": "disk", proxydevice.ManagedKey: proxydevice.ManagedValue}},
		{name: "unmarked proxy", props: map[string]string{"type": "proxy"}},
		{name: "wrong marker value", props: map[string]string{"type": "proxy", proxydevice.ManagedKey: "false"}},
		{name: "marked proxy", props: map[string]string{"type": "proxy", proxydevice.ManagedKey: proxydevice.ManagedValue}, want: true},
		{name: "marked socket", props: map[string]string{"type": "proxy", proxydevice.ManagedKey: proxydevice.ManagedValue, proxydevice.KindKey: proxydevice.KindSocket}, want: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := proxydevice.IsManaged(tt.props); got != tt.want {
				t.Fatalf("IsManaged(%#v) = %v, want %v", tt.props, got, tt.want)
			}
		})
	}
}

func TestNamespacePredicates(t *testing.T) {
	port := map[string]string{"type": "proxy", proxydevice.ManagedKey: proxydevice.ManagedValue}
	socket := map[string]string{"type": "proxy", proxydevice.ManagedKey: proxydevice.ManagedValue, proxydevice.KindKey: proxydevice.KindSocket}
	unknownKind := map[string]string{"type": "proxy", proxydevice.ManagedKey: proxydevice.ManagedValue, proxydevice.KindKey: "future"}

	if proxydevice.IsSocket(port) {
		t.Error("port device classified as socket")
	}
	if !proxydevice.IsSocket(socket) {
		t.Error("socket device not classified as socket")
	}
	if proxydevice.IsSocket(unknownKind) {
		t.Error("unknown kind classified as socket")
	}
	if got := proxydevice.Kind(socket); got != proxydevice.KindSocket {
		t.Errorf("Kind(socket) = %q", got)
	}
	if got := proxydevice.Kind(nil); got != "" {
		t.Errorf("Kind(nil) = %q", got)
	}
}

func TestScheme(t *testing.T) {
	tests := []struct {
		endpoint string
		scheme   string
		rest     string
		ok       bool
	}{
		{"tcp:127.0.0.1:80", "tcp", "127.0.0.1:80", true},
		{"UNIX:/run/a.sock", "unix", "/run/a.sock", true},
		{"unix:/run/with:colon.sock", "unix", "/run/with:colon.sock", true},
		{"no-scheme", "", "", false},
	}
	for _, tt := range tests {
		scheme, rest, ok := proxydevice.Scheme(tt.endpoint)
		if scheme != tt.scheme || rest != tt.rest || ok != tt.ok {
			t.Errorf("Scheme(%q) = (%q, %q, %v), want (%q, %q, %v)",
				tt.endpoint, scheme, rest, ok, tt.scheme, tt.rest, tt.ok)
		}
	}
}

func TestParseBool(t *testing.T) {
	tests := []struct {
		raw   string
		value bool
		ok    bool
	}{
		{"", false, true},
		{"0", false, true},
		{"false", false, true},
		{"no", false, true},
		{"off", false, true},
		{"1", true, true},
		{"true", true, true},
		{"YES", true, true},
		{"on", true, true},
		{"maybe", false, false},
	}
	for _, tt := range tests {
		value, ok := proxydevice.ParseBool(tt.raw)
		if value != tt.value || ok != tt.ok {
			t.Errorf("ParseBool(%q) = (%v, %v), want (%v, %v)", tt.raw, value, ok, tt.value, tt.ok)
		}
	}
}
