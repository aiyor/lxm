package common

import (
	"errors"
	"strings"
)

// StatusError is an interface satisfied by SDK errors carrying an HTTP status code.
type StatusError interface {
	Status() int
}

// IsETagConflictMessage reports whether an error string indicates an ETag mismatch / concurrent modification.
func IsETagConflictMessage(errStr string) bool {
	lower := strings.ToLower(errStr)
	return strings.Contains(lower, "etag mismatch") ||
		strings.Contains(lower, "etag does not match") ||
		strings.Contains(lower, "configuration has been modified since this change began")
}

// ClassifyError classifies an error from LXD/Incus daemons into an exit code and retryable flag.
func ClassifyError(err error, intent string) (int, bool) {
	if err == nil {
		return 0, false
	}

	errStr := err.Error()
	var statusErr StatusError
	if errors.As(err, &statusErr) {
		code := statusErr.Status()
		if code == 404 {
			if intent == "lookup" {
				return 5, false // TARGET_NOT_FOUND
			}
			return 0, false // existence check -> create signal
		}
		if IsETagConflictMessage(errStr) || code == 412 {
			return 4, true // PROVIDER_ERROR, ETag mismatch retryable
		}
		return 4, false
	}

	if strings.Contains(errStr, "not found") {
		if intent == "lookup" {
			return 5, false
		}
		return 0, false
	}

	if IsETagConflictMessage(errStr) || strings.Contains(errStr, "412") {
		return 4, true
	}

	return 4, false
}
