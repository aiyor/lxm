package common_test

import (
	"errors"
	"testing"

	"github.com/aiyor/lxm/internal/provider/common"
)

type mockStatusError struct {
	code int
	msg  string
}

func (m *mockStatusError) Error() string { return m.msg }
func (m *mockStatusError) Status() int   { return m.code }

func TestClassifyError(t *testing.T) {
	if code, retry := common.ClassifyError(nil, "lookup"); code != 0 || retry {
		t.Errorf("expected 0, false for nil error, got %d, %v", code, retry)
	}

	notFoundErr := &mockStatusError{code: 404, msg: "not found"}
	if code, _ := common.ClassifyError(notFoundErr, "lookup"); code != 5 {
		t.Errorf("expected 5 for 404 lookup, got %d", code)
	}
	if code, _ := common.ClassifyError(notFoundErr, "create"); code != 0 {
		t.Errorf("expected 0 for 404 create check, got %d", code)
	}

	etagErr := &mockStatusError{code: 412, msg: "ETag mismatch"}
	if code, retry := common.ClassifyError(etagErr, "update"); code != 4 || !retry {
		t.Errorf("expected 4, true for ETag mismatch, got %d, %v", code, retry)
	}

	rawETagErr := errors.New("configuration has been modified since this change began")
	if code, retry := common.ClassifyError(rawETagErr, "update"); code != 4 || !retry {
		t.Errorf("expected 4, true for raw ETag conflict string, got %d, %v", code, retry)
	}
}
