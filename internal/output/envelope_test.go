package output

import (
	"bytes"
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

func TestNewEnvelope_Invariants(t *testing.T) {
	env := NewEnvelope("apply", "config/")

	if env.Schema != "lxm/result/v1" {
		t.Errorf("schema = %q, want %q", env.Schema, "lxm/result/v1")
	}
	if env.Command != "apply" {
		t.Errorf("command = %q, want %q", env.Command, "apply")
	}
	if !env.OK {
		t.Errorf("ok = %v, want true", env.OK)
	}
	if env.Target != "config/" {
		t.Errorf("target = %q, want %q", env.Target, "config/")
	}
	if env.ExitCode != 0 {
		t.Errorf("exit_code = %d, want 0", env.ExitCode)
	}

	// Verify JSON serialization of empty fields as [] and {} (never null)
	data, err := json.Marshal(env)
	if err != nil {
		t.Fatalf("json.Marshal failed: %v", err)
	}

	jsonStr := string(data)
	if strings.Contains(jsonStr, "null") {
		t.Errorf("serialized JSON contains null: %s", jsonStr)
	}
	if !strings.Contains(jsonStr, `"results":[]`) {
		t.Errorf("serialized JSON missing empty results array: %s", jsonStr)
	}
	if !strings.Contains(jsonStr, `"warnings":[]`) {
		t.Errorf("serialized JSON missing empty warnings array: %s", jsonStr)
	}
	if !strings.Contains(jsonStr, `"errors":[]`) {
		t.Errorf("serialized JSON missing empty errors array: %s", jsonStr)
	}
	if !strings.Contains(jsonStr, `"plan":{"summary":{}}`) {
		t.Errorf("serialized JSON missing empty plan summary map: %s", jsonStr)
	}
}

func TestExitCodeToErrorCode_Catalog(t *testing.T) {
	tests := []struct {
		code int
		want string
	}{
		{0, ""},
		{1, "INTERNAL_ERROR"},
		{2, "USAGE_ERROR"},
		{3, "CONFIG_ERROR"},
		{4, "PROVIDER_ERROR"},
		{5, "TARGET_NOT_FOUND"},
		{6, "EXEC_FAILED"},
		{7, "WAIT_TIMEOUT"},
		{99, "INTERNAL_ERROR"},
	}

	for _, tt := range tests {
		got := ExitCodeToErrorCode(tt.code)
		if got != tt.want {
			t.Errorf("ExitCodeToErrorCode(%d) = %q, want %q", tt.code, got, tt.want)
		}
	}
}

func TestSetExitCode(t *testing.T) {
	env := NewEnvelope("run", "box1")
	testErr := errors.New("script execution failed")
	env.SetExitCode(6, testErr, "box1", false)

	if env.OK {
		t.Errorf("ok = true, want false when exit code is 6")
	}
	if env.ExitCode != 6 {
		t.Errorf("exit_code = %d, want 6", env.ExitCode)
	}
	if len(env.Errors) != 1 {
		t.Fatalf("errors count = %d, want 1", len(env.Errors))
	}

	errInfo := env.Errors[0]
	if errInfo.Code != "EXEC_FAILED" {
		t.Errorf("error code = %q, want EXEC_FAILED", errInfo.Code)
	}
	if errInfo.Container != "box1" {
		t.Errorf("error container = %q, want box1", errInfo.Container)
	}
	if errInfo.Message != "script execution failed" {
		t.Errorf("error message = %q, want %q", errInfo.Message, "script execution failed")
	}
	if errInfo.Retryable {
		t.Errorf("retryable = true, want false")
	}

	// Test SetExitCode with nil error
	env2 := NewEnvelope("apply", ".")
	env2.SetExitCode(3, nil, "", false)
	if env2.Errors[0].Message != "command failed with exit code 3" {
		t.Errorf("error message for nil err = %q, want %q", env2.Errors[0].Message, "command failed with exit code 3")
	}
}

func TestEmit(t *testing.T) {
	env := NewEnvelope("list", "")

	// Test JSON emit
	var jsonBuf bytes.Buffer
	err := Emit(&jsonBuf, "json", env)
	if err != nil {
		t.Fatalf("Emit(json) failed: %v", err)
	}
	if !strings.Contains(jsonBuf.String(), `"schema": "lxm/result/v1"`) {
		t.Errorf("Emit(json) output missing schema: %s", jsonBuf.String())
	}

	// Test Text emit for success (silent)
	var textBuf bytes.Buffer
	err = Emit(&textBuf, "text", env)
	if err != nil {
		t.Fatalf("Emit(text) failed: %v", err)
	}
	if textBuf.Len() != 0 {
		t.Errorf("Emit(text) on success produced output, want empty: %s", textBuf.String())
	}

	// Test Text emit for failure
	envFail := NewEnvelope("apply", "dev.yaml")
	envFail.SetExitCode(3, errors.New("invalid manifest"), "dev-box", false)
	var textBufFail bytes.Buffer
	err = Emit(&textBufFail, "text", envFail)
	if err != nil {
		t.Fatalf("Emit(text fail) failed: %v", err)
	}
	if !strings.Contains(textBufFail.String(), "CONFIG_ERROR") {
		t.Errorf("Emit(text fail) output missing CONFIG_ERROR: %s", textBufFail.String())
	}
}
