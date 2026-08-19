package common

import (
	"fmt"
	"os"
	"os/signal"
	"strconv"
	"sync"
	"syscall"

	"golang.org/x/term"
)

// ControlMessage represents a control message sent over an exec control websocket.
type ControlMessage struct {
	Command string            `json:"command"`
	Args    map[string]string `json:"args"`
}

// RunInteractiveTerminal puts the stdin terminal into raw mode, queries terminal size,
// monitors signals (SIGWINCH for window resize, SIGINT for interrupt forwarding),
// and delegates to runner with window dimensions and a control channel.
func RunInteractiveTerminal(runner func(width, height int, controlChan <-chan ControlMessage) error) error {
	stdinFd := int(os.Stdin.Fd())
	var oldState *term.State
	var isTerminal bool

	if term.IsTerminal(stdinFd) {
		isTerminal = true
		state, err := term.MakeRaw(stdinFd)
		if err != nil {
			return fmt.Errorf("setting terminal to raw mode: %w", err)
		}
		oldState = state
		defer func() {
			_ = term.Restore(stdinFd, oldState)
		}()
	}

	width, height := 80, 24
	if isTerminal {
		if w, h, err := term.GetSize(stdinFd); err == nil {
			width, height = w, h
		}
	}

	controlChan := make(chan ControlMessage, 10)
	done := make(chan struct{})
	defer func() {
		close(done)
		close(controlChan)
	}()

	defer func() {
		if r := recover(); r != nil {
			if isTerminal && oldState != nil {
				_ = term.Restore(stdinFd, oldState)
			}
			panic(r)
		}
	}()

	if isTerminal {
		sigChan := make(chan os.Signal, 1)
		signal.Notify(sigChan, syscall.SIGWINCH, syscall.SIGINT, syscall.SIGTERM, syscall.SIGHUP)
		defer signal.Stop(sigChan)

		var wg sync.WaitGroup
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case sig := <-sigChan:
					switch sig {
					case syscall.SIGWINCH:
						w, h, err := term.GetSize(stdinFd)
						if err == nil {
							select {
							case controlChan <- ControlMessage{
								Command: "window-resize",
								Args: map[string]string{
									"width":  strconv.Itoa(w),
									"height": strconv.Itoa(h),
								},
							}:
							case <-done:
								return
							}
						}
					case syscall.SIGINT:
						select {
						case controlChan <- ControlMessage{
							Command: "signal",
							Args: map[string]string{
								"signum": strconv.Itoa(int(syscall.SIGINT)),
							},
						}:
						case <-done:
							return
						}
					default:
						if oldState != nil {
							_ = term.Restore(stdinFd, oldState)
						}
						signal.Stop(sigChan)
						if p, err := os.FindProcess(os.Getpid()); err == nil {
							_ = p.Signal(sig)
						}
						return
					}
				case <-done:
					return
				}
			}
		}()
		defer wg.Wait()
	}

	return runner(width, height, controlChan)
}
