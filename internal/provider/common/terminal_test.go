package common_test

import (
	"errors"
	"testing"

	"github.com/aiyor/lxm/internal/provider/common"
)

func TestRunInteractiveTerminal_NonTerminalRunner(t *testing.T) {
	executed := false
	err := common.RunInteractiveTerminal(func(width, height int, controlChan <-chan common.ControlMessage) error {
		executed = true
		if width <= 0 || height <= 0 {
			t.Errorf("expected positive dimensions, got %dx%d", width, height)
		}
		if controlChan == nil {
			t.Error("expected non-nil controlChan")
		}
		return nil
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !executed {
		t.Fatal("expected runner to be executed")
	}
}

func TestRunInteractiveTerminal_RunnerErrorPropagation(t *testing.T) {
	sentinel := errors.New("runner failed")
	err := common.RunInteractiveTerminal(func(width, height int, controlChan <-chan common.ControlMessage) error {
		return sentinel
	})
	if !errors.Is(err, sentinel) {
		t.Fatalf("expected error %v, got %v", sentinel, err)
	}
}
