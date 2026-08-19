package common

import (
	"context"
)

// Operation abstracts an asynchronous daemon operation returned by LXD/Incus SDKs.
type Operation interface {
	Wait() error
}

// WaitOpContext waits for an Operation to complete or context cancellation.
func WaitOpContext(ctx context.Context, op Operation) error {
	if op == nil {
		return nil
	}
	if ctx == nil {
		return op.Wait()
	}
	done := make(chan error, 1)
	go func() {
		done <- op.Wait()
	}()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case err := <-done:
		return err
	}
}

// ExtractExecExitCode safely extracts the return code from operation metadata without panicking on nil metadata.
func ExtractExecExitCode(metadata map[string]interface{}, waitErr error) (int, error) {
	exitCode := -1
	if metadata != nil {
		if returnVal, ok := metadata["return"]; ok {
			if codeFloat, ok := returnVal.(float64); ok {
				exitCode = int(codeFloat)
			}
		}
	}
	if waitErr != nil && exitCode == -1 {
		return -1, waitErr
	}
	return exitCode, waitErr
}
