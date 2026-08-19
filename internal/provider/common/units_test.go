package common_test

import (
	"testing"

	"github.com/aiyor/lxm/internal/provider/common"
)

func TestParseByteSizeString(t *testing.T) {
	tests := []struct {
		input    string
		expected int64
		wantErr  bool
	}{
		{"1024", 1024, false},
		{"1024B", 1024, false},
		{"1KiB", 1024, false},
		{"1KB", 1000, false},
		{"10GiB", 10 * 1024 * 1024 * 1024, false},
		{"10GB", 10 * 1000 * 1000 * 1000, false},
		{"500MB", 500 * 1000 * 1000, false},
		{"500MiB", 500 * 1024 * 1024, false},
		{"0", 0, false},
		{"", 0, true},
		{"invalid", 0, true},
		{"-10GiB", 0, true},
		{"10Foo", 0, true},
	}

	for _, tt := range tests {
		got, err := common.ParseByteSizeString(tt.input)
		if (err != nil) != tt.wantErr {
			t.Errorf("ParseByteSizeString(%q) error = %v, wantErr %v", tt.input, err, tt.wantErr)
			continue
		}
		if !tt.wantErr && got != tt.expected {
			t.Errorf("ParseByteSizeString(%q) = %d, expected %d", tt.input, got, tt.expected)
		}
	}
}
