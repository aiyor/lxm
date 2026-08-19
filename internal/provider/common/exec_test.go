package common_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/aiyor/lxm/internal/provider/common"
)

type mockOp struct {
	waitDelay time.Duration
	err       error
}

func (m *mockOp) Wait() error {
	if m.waitDelay > 0 {
		time.Sleep(m.waitDelay)
	}
	return m.err
}

func TestWaitOpContext(t *testing.T) {
	// Nil op
	if err := common.WaitOpContext(context.Background(), nil); err != nil {
		t.Errorf("expected nil for nil op, got %v", err)
	}

	// Normal completion
	op := &mockOp{err: nil}
	if err := common.WaitOpContext(context.Background(), op); err != nil {
		t.Errorf("expected nil error, got %v", err)
	}

	// Context cancellation
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	slowOp := &mockOp{waitDelay: 100 * time.Millisecond}
	if err := common.WaitOpContext(ctx, slowOp); !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("expected context deadline exceeded, got %v", err)
	}
}

func TestExtractExecExitCode(t *testing.T) {
	// Nil metadata with error
	code, err := common.ExtractExecExitCode(nil, errors.New("fail"))
	if code != -1 || err == nil {
		t.Errorf("expected -1 and error, got %d, %v", code, err)
	}

	// Normal exit code 0
	meta0 := map[string]interface{}{"return": float64(0)}
	code, err = common.ExtractExecExitCode(meta0, nil)
	if code != 0 || err != nil {
		t.Errorf("expected 0 and nil error, got %d, %v", code, err)
	}

	// Non-zero exit code 127
	meta127 := map[string]interface{}{"return": float64(127)}
	code, err = common.ExtractExecExitCode(meta127, nil)
	if code != 127 || err != nil {
		t.Errorf("expected 127 and nil error, got %d, %v", code, err)
	}

	// Integer return code 2
	metaInt := map[string]interface{}{"return": 2}
	code, err = common.ExtractExecExitCode(metaInt, nil)
	if code != 2 || err != nil {
		t.Errorf("expected 2 and nil error, got %d, %v", code, err)
	}
}

type mockOpWithMeta struct {
	meta map[string]interface{}
}

func (m *mockOpWithMeta) Wait() error { return nil }
func (m *mockOpWithMeta) Get() struct{ Metadata map[string]interface{} } {
	return struct{ Metadata map[string]interface{} }{Metadata: m.meta}
}

func TestSafeExecResult(t *testing.T) {
	op := &mockOpWithMeta{
		meta: map[string]interface{}{"return": float64(0)},
	}
	res, err := common.SafeExecResult(op, "out\n", "", nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.ExitCode != 0 || res.Stdout != "out\n" {
		t.Errorf("unexpected exec result: %+v", res)
	}

	// Nil op
	resNil, errNil := common.SafeExecResult(nil, "", "err\n", errors.New("exec failed"))
	if errNil == nil || resNil.ExitCode != -1 || resNil.Stderr != "err\n" {
		t.Errorf("unexpected nil op result: %+v, err: %v", resNil, errNil)
	}
}
