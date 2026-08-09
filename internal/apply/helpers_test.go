package apply

import "testing"

func TestErrorCodeHelpers(t *testing.T) {
	for i := 1; i <= 7; i++ {
		str := exitToErrorCode(i)
		code := errorCodeToExit(str)
		if code != i {
			t.Errorf("expected %d, got %d for %s", i, code, str)
		}
	}
	_ = exitToErrorCode(999)
	_ = errorCodeToExit("UNKNOWN")

	// Test selectWorstExitCode precedence
	if selectWorstExitCode(0, 4) != 4 {
		t.Errorf("expected 4")
	}
	if selectWorstExitCode(4, 1) != 1 {
		t.Errorf("expected 1")
	}
	if selectWorstExitCode(4, 5) != 4 {
		t.Errorf("expected 4")
	}
}
