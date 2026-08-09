package lxd

import (
	"os"
	"syscall"
	"testing"

	"github.com/canonical/lxd/shared/api"
)

func TestHandleInteractiveSignal_SIGINT(t *testing.T) {
	controlChan := make(chan api.InstanceExecControl, 1)
	done := make(chan struct{})
	opts := interactiveSignalOpts{}

	exit := handleInteractiveSignal(syscall.SIGINT, opts, controlChan, done)
	if exit {
		t.Errorf("expected SIGINT not to exit loop")
	}

	select {
	case ctrl := <-controlChan:
		if ctrl.Command != "signal" || ctrl.Args["signum"] != "2" {
			t.Errorf("unexpected control message for SIGINT: %+v", ctrl)
		}
	default:
		t.Errorf("expected control message for SIGINT, got none")
	}
}

func TestHandleInteractiveSignal_SIGWINCH(t *testing.T) {
	controlChan := make(chan api.InstanceExecControl, 1)
	done := make(chan struct{})
	opts := interactiveSignalOpts{
		getTermSize: func(fd int) (int, int, error) {
			return 120, 40, nil
		},
	}

	exit := handleInteractiveSignal(syscall.SIGWINCH, opts, controlChan, done)
	if exit {
		t.Errorf("expected SIGWINCH not to exit loop")
	}

	select {
	case ctrl := <-controlChan:
		if ctrl.Command != "window-resize" || ctrl.Args["width"] != "120" || ctrl.Args["height"] != "40" {
			t.Errorf("unexpected control message for SIGWINCH: %+v", ctrl)
		}
	default:
		t.Errorf("expected control message for SIGWINCH, got none")
	}
}

func TestHandleInteractiveSignal_SIGTERM_TerminalRestore(t *testing.T) {
	controlChan := make(chan api.InstanceExecControl, 1)
	done := make(chan struct{})
	restored := false
	reRaised := false

	opts := interactiveSignalOpts{
		restoreTerm: func() {
			restored = true
		},
		reRaiseSignal: func(sig os.Signal) {
			if sig == syscall.SIGTERM {
				reRaised = true
			}
		},
	}

	exit := handleInteractiveSignal(syscall.SIGTERM, opts, controlChan, done)
	if !exit {
		t.Errorf("expected SIGTERM to exit loop")
	}
	if !restored {
		t.Errorf("expected terminal restore function to be called on SIGTERM")
	}
	if !reRaised {
		t.Errorf("expected signal to be re-raised on SIGTERM")
	}
}

func TestHandleInteractiveSignal_SIGHUP_TerminalRestore(t *testing.T) {
	controlChan := make(chan api.InstanceExecControl, 1)
	done := make(chan struct{})
	restored := false
	reRaised := false

	opts := interactiveSignalOpts{
		restoreTerm: func() {
			restored = true
		},
		reRaiseSignal: func(sig os.Signal) {
			if sig == syscall.SIGHUP {
				reRaised = true
			}
		},
	}

	exit := handleInteractiveSignal(syscall.SIGHUP, opts, controlChan, done)
	if !exit {
		t.Errorf("expected SIGHUP to exit loop")
	}
	if !restored {
		t.Errorf("expected terminal restore function to be called on SIGHUP")
	}
	if !reRaised {
		t.Errorf("expected signal to be re-raised on SIGHUP")
	}
}

func TestHandleInteractiveSignal_SIGTERM_OrderOfOperations(t *testing.T) {
	controlChan := make(chan api.InstanceExecControl, 1)
	done := make(chan struct{})
	order := []string{}

	opts := interactiveSignalOpts{
		restoreTerm: func() {
			order = append(order, "restore")
		},
		reRaiseSignal: func(sig os.Signal) {
			order = append(order, "re-raise")
		},
	}

	exit := handleInteractiveSignal(syscall.SIGTERM, opts, controlChan, done)
	if !exit {
		t.Errorf("expected SIGTERM to exit loop")
	}
	if len(order) != 2 || order[0] != "restore" || order[1] != "re-raise" {
		t.Errorf("expected restore before re-raise, got order: %v", order)
	}
}
