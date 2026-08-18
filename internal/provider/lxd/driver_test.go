package lxd_test

import (
	"errors"
	"fmt"
	"testing"

	"github.com/canonical/lxd/shared/api"

	"github.com/aiyor/lxm/internal/provider/lxd"
)

func TestClassifyLXDError(t *testing.T) {
	d := lxd.NewDriver(nil)

	tests := []struct {
		name         string
		err          error
		intent       string
		wantExitCode int
		wantRetry    bool
	}{
		{
			name:         "nil error",
			err:          nil,
			intent:       "lookup",
			wantExitCode: 0,
			wantRetry:    false,
		},
		{
			name:         "not found with lookup intent",
			err:          errors.New("instance not found"),
			intent:       "lookup",
			wantExitCode: 5,
			wantRetry:    false,
		},
		{
			name:         "not found with existence check intent",
			err:          errors.New("instance not found"),
			intent:       "check",
			wantExitCode: 0,
			wantRetry:    false,
		},
		{
			name:         "404 StatusError lookup",
			err:          api.StatusErrorf(404, "not found"),
			intent:       "lookup",
			wantExitCode: 5,
			wantRetry:    false,
		},
		{
			name:         "412 StatusError ETag conflict",
			err:          api.StatusErrorf(412, "ETag does not match: abc vs def"),
			intent:       "mutate",
			wantExitCode: 4,
			wantRetry:    true,
		},
		{
			name:         "Generic ETag conflict message",
			err:          fmt.Errorf("ETag does not match: 123 vs 456. The configuration has been modified since this change began."),
			intent:       "mutate",
			wantExitCode: 4,
			wantRetry:    true,
		},
		{
			name:         "Generic error",
			err:          errors.New("connection refused"),
			intent:       "mutate",
			wantExitCode: 4,
			wantRetry:    false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			code, retry := d.ClassifyError(tt.err, tt.intent)
			if code != tt.wantExitCode || retry != tt.wantRetry {
				t.Errorf("ClassifyError(%v, %q) = (%d, %v), want (%d, %v)", tt.err, tt.intent, code, retry, tt.wantExitCode, tt.wantRetry)
			}
		})
	}
}
