package common

import (
	"context"
	"encoding/json"
	"reflect"
	"strconv"

	"github.com/aiyor/lxm/internal/provider"
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

// ExtractOperationMetadata extracts the Metadata map from an operation object (e.g. op.Get().Metadata).
func ExtractOperationMetadata(op any) map[string]interface{} {
	if op == nil {
		return nil
	}
	val := reflect.ValueOf(op)
	if val.Kind() == reflect.Pointer && val.IsNil() {
		return nil
	}

	// Try op.Get() method
	getMethod := val.MethodByName("Get")
	if !getMethod.IsValid() {
		return nil
	}
	results := getMethod.Call(nil)
	if len(results) == 0 {
		return nil
	}

	res := results[0]
	if res.Kind() == reflect.Pointer {
		if res.IsNil() {
			return nil
		}
		res = res.Elem()
	}
	if res.Kind() == reflect.Struct {
		metaField := res.FieldByName("Metadata")
		if metaField.IsValid() && !metaField.IsNil() {
			if metaMap, ok := metaField.Interface().(map[string]interface{}); ok {
				return metaMap
			}
			if metaMap, ok := metaField.Interface().(map[string]any); ok {
				return metaMap
			}
		}
	}

	return nil
}

// ExtractExecExitCode safely extracts the return code from operation metadata without panicking on nil metadata.
func ExtractExecExitCode(metadata map[string]interface{}, waitErr error) (int, error) {
	exitCode := -1
	if metadata != nil {
		if returnVal, ok := metadata["return"]; ok && returnVal != nil {
			switch v := returnVal.(type) {
			case int:
				exitCode = v
			case int32:
				exitCode = int(v)
			case int64:
				exitCode = int(v)
			case float64:
				exitCode = int(v)
			case float32:
				exitCode = int(v)
			case json.Number:
				if n, err := v.Int64(); err == nil {
					exitCode = int(n)
				}
			case string:
				if n, err := strconv.Atoi(v); err == nil {
					exitCode = n
				}
			}
		}
	}
	if waitErr != nil && exitCode == -1 {
		return -1, waitErr
	}
	return exitCode, waitErr
}

// SafeExecResult extracts operation metadata, determines the exit code, and formats provider.ExecResult.
func SafeExecResult(op any, stdout, stderr string, waitErr error) (provider.ExecResult, error) {
	metadata := ExtractOperationMetadata(op)
	exitCode, finalErr := ExtractExecExitCode(metadata, waitErr)
	return provider.ExecResult{
		ExitCode: exitCode,
		Stdout:   stdout,
		Stderr:   stderr,
	}, finalErr
}
