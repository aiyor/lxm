package common_test

import (
	"testing"

	"github.com/aiyor/lxm/internal/provider/common"
)

func TestDeviceName(t *testing.T) {
	tests := []struct {
		path     string
		expected string
	}{
		{"/mnt/data", "mount--mnt-data"},
		{"mnt/data", "mount-mnt-data"},
		{"/data", "mount--data"},
	}

	for _, tt := range tests {
		got := common.DeviceName(tt.path)
		if got != tt.expected {
			t.Errorf("DeviceName(%q) = %q, expected %q", tt.path, got, tt.expected)
		}
	}
}

func TestIsHex(t *testing.T) {
	if !common.IsHex("1234abcd") {
		t.Errorf("expected 1234abcd to be hex")
	}
	if !common.IsHex("ABCD12") {
		t.Errorf("expected ABCD12 to be hex")
	}
	if common.IsHex("1234xyz") {
		t.Errorf("expected 1234xyz not to be hex")
	}
	if common.IsHex("") {
		t.Errorf("expected empty string not to be hex")
	}
}
