package output

import (
	"fmt"
)

const SchemaVersion = "lxm/result/v1"

// Envelope represents the standardized lxm result envelope schema (lxm/result/v1).
type Envelope struct {
	Schema         string          `json:"schema"`
	Command        string          `json:"command"`
	OK             bool            `json:"ok"`
	Target         string          `json:"target"`
	Plan           PlanSummary     `json:"plan"`
	Results        []ResultItem    `json:"results"`
	NetworkResults []NetworkResult `json:"network_results,omitempty"`
	Warnings       []string        `json:"warnings"`
	Errors         []ErrorInfo     `json:"errors"`
	ExitCode       int             `json:"exit_code"`
}

// PlanSummary represents the plan summary structure within the envelope.
type PlanSummary struct {
	Summary      map[string]int `json:"summary"`
	Steps        interface{}    `json:"steps,omitempty"`
	NetworkSteps interface{}    `json:"network_steps,omitempty"`
}

// ResultItem represents an individual container operation result.
type ResultItem struct {
	Container  string `json:"container,omitempty"`
	Action     string `json:"action,omitempty"`
	Changed    bool   `json:"changed"`
	OK         bool   `json:"ok"`
	DurationMS int64  `json:"duration_ms,omitempty"`
}

// NetworkResult represents an individual network-step operation result.
type NetworkResult struct {
	Name       string `json:"name"`
	Kind       string `json:"kind"`
	Changed    bool   `json:"changed"`
	OK         bool   `json:"ok"`
	DurationMS int64  `json:"duration_ms,omitempty"`
	Error      string `json:"error,omitempty"`
}

// ErrorInfo represents a structured error entry inside the envelope.
type ErrorInfo struct {
	Code      string `json:"code"`
	Container string `json:"container,omitempty"`
	Name      string `json:"name,omitempty"`
	Message   string `json:"message"`
	Retryable bool   `json:"retryable"`
}

// NewEnvelope creates a new initialized Envelope for a command.
// Empty slices and maps are explicitly initialized so they serialize as [] and {}, never null.
func NewEnvelope(command string, target string) *Envelope {
	return &Envelope{
		Schema:   SchemaVersion,
		Command:  command,
		OK:       true,
		Target:   target,
		Plan:     PlanSummary{Summary: make(map[string]int)},
		Results:  make([]ResultItem, 0),
		Warnings: make([]string, 0),
		Errors:   make([]ErrorInfo, 0),
		ExitCode: 0,
	}
}

// ExitCodeToErrorCode maps a numeric exit code to its corresponding error code string (F8 catalog).
func ExitCodeToErrorCode(code int) string {
	switch code {
	case 0:
		return ""
	case 1:
		return "INTERNAL_ERROR"
	case 2:
		return "USAGE_ERROR"
	case 3:
		return "CONFIG_ERROR"
	case 4:
		return "LXD_ERROR"
	case 5:
		return "TARGET_NOT_FOUND"
	case 6:
		return "EXEC_FAILED"
	case 7:
		return "WAIT_TIMEOUT"
	default:
		return "INTERNAL_ERROR"
	}
}

// SetExitCode sets the numeric exit code, updates the OK flag, and appends an ErrorInfo if code != 0.
func (e *Envelope) SetExitCode(code int, err error, container string, retryable bool) {
	e.ExitCode = code
	e.OK = (code == 0)

	if code != 0 {
		msg := ""
		if err != nil {
			msg = err.Error()
		} else {
			msg = fmt.Sprintf("command failed with exit code %d", code)
		}

		errCode := ExitCodeToErrorCode(code)
		e.Errors = append(e.Errors, ErrorInfo{
			Code:      errCode,
			Container: container,
			Message:   msg,
			Retryable: retryable,
		})
	}
}
