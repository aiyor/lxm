package lxd

import (
	"errors"
	"fmt"
	"testing"

	"github.com/canonical/lxd/shared/api"
)

func TestLXDService_ClassifyLXDError_Production(t *testing.T) {
	svc := &lxdService{}

	// 1. nil error -> (0, false)
	code, retryable := svc.ClassifyLXDError(nil, "lookup")
	if code != 0 || retryable {
		t.Errorf("expected (0, false) for nil error, got (%d, %v)", code, retryable)
	}

	// 2. api.StatusError 404 + lookup -> (5, false)
	code, retryable = svc.ClassifyLXDError(api.NewStatusError(404, "not found"), "lookup")
	if code != 5 || retryable {
		t.Errorf("expected (5, false) for 404 lookup, got (%d, %v)", code, retryable)
	}

	// 3. api.StatusError 404 + check -> (0, false)
	code, retryable = svc.ClassifyLXDError(api.NewStatusError(404, "not found"), "check")
	if code != 0 || retryable {
		t.Errorf("expected (0, false) for 404 check, got (%d, %v)", code, retryable)
	}

	// 4. api.StatusError 412 (ETag mismatch) -> (4, true)
	code, retryable = svc.ClassifyLXDError(api.NewStatusError(412, "etag mismatch"), "update")
	if code != 4 || !retryable {
		t.Errorf("expected (4, true) for 412 update, got (%d, %v)", code, retryable)
	}

	// 5. api.StatusError 500 -> (4, false)
	code, retryable = svc.ClassifyLXDError(api.NewStatusError(500, "server error"), "update")
	if code != 4 || retryable {
		t.Errorf("expected (4, false) for 500 update, got (%d, %v)", code, retryable)
	}

	// 6. Error string "not found" + lookup -> (5, false)
	code, retryable = svc.ClassifyLXDError(errors.New("instance not found"), "lookup")
	if code != 5 || retryable {
		t.Errorf("expected (5, false) for 'not found' lookup, got (%d, %v)", code, retryable)
	}

	// 7. Error string "not found" + check -> (0, false)
	code, retryable = svc.ClassifyLXDError(errors.New("instance not found"), "check")
	if code != 0 || retryable {
		t.Errorf("expected (0, false) for 'not found' check, got (%d, %v)", code, retryable)
	}

	// 8. Error string "etag mismatch" -> (4, true)
	code, retryable = svc.ClassifyLXDError(errors.New("etag mismatch error"), "update")
	if code != 4 || !retryable {
		t.Errorf("expected (4, true) for etag mismatch, got (%d, %v)", code, retryable)
	}

	// 9. Error string containing "412" -> (4, true)
	code, retryable = svc.ClassifyLXDError(errors.New("http 412 precondition failed"), "update")
	if code != 4 || !retryable {
		t.Errorf("expected (4, true) for 412 error, got (%d, %v)", code, retryable)
	}

	// 10. Generic error -> (4, false)
	code, retryable = svc.ClassifyLXDError(errors.New("connection reset"), "update")
	if code != 4 || retryable {
		t.Errorf("expected (4, false) for generic error, got (%d, %v)", code, retryable)
	}
}

// TestLXDService_ClassifyLXDError_RealLXD412 covers UG5 B1: the real LXD
// daemon's 412 message ("ETag does not match: <old> vs <new>. The
// configuration has been modified since this change began. ...") must be
// classified (4, true) — retryable — so drift errors honor the
// results-and-exit-codes contract. The previous string heuristic only
// matched the synthetic "etag mismatch" text, and the api.StatusError
// assertion missed wrapped errors, so live drift surfaced retryable: false.
func TestLXDService_ClassifyLXDError_RealLXD412(t *testing.T) {
	svc := &lxdService{}

	const real412 = "ETag does not match: 97c66a57b26926ecdf0bbc74f97170f96b782e6e6ba4570ff8ac52d808011c70 vs 11e05802de2a480b425c25c2d2d4136a75f765427be5641bc9c953995b46b8d5. The configuration has been modified since this change began. Please retrieve the updated configuration before proceeding."

	// 1. The daemon's literal 412 message -> (4, true)
	code, retryable := svc.ClassifyLXDError(errors.New(real412), "update")
	if code != 4 || !retryable {
		t.Errorf("real 412 message: expected (4, true), got (%d, %v)", code, retryable)
	}

	// 2. Wrapped api.StatusError 412 -> (4, true) via errors.As. Regression
	// guard for the errors.As change (commit 1f00574): before that commit the
	// classifier used a direct `err.(api.StatusError)` assertion, which fails
	// on a wrapped error and fell through to a string heuristic that missed
	// this input (the inner message carries no etag markers).
	wrapped := fmt.Errorf("updating container: %w", api.NewStatusError(412, "precondition failed"))
	code, retryable = svc.ClassifyLXDError(wrapped, "update")
	if code != 4 || !retryable {
		t.Errorf("wrapped StatusError 412: expected (4, true), got (%d, %v)", code, retryable)
	}

	// 3. Fake server path: the fake now emits the real message, so
	// classification of the fake's own error must stay retryable.
	fake := NewFakeInstanceServer()
	_ = fake.CreateInstance(api.InstancesPost{Name: "box1"})
	code, retryable = fake.ClassifyLXDError(errors.New(real412), "update")
	if code != 4 || !retryable {
		t.Errorf("fake classify of real 412 message: expected (4, true), got (%d, %v)", code, retryable)
	}

	// 4. Non-412 StatusError carrying the ETag text must still be retryable
	// (a daemon returning the conflict under a non-standard code is the same
	// drift, and the message is authoritative).
	code, retryable = svc.ClassifyLXDError(api.NewStatusError(500, real412), "update")
	if code != 4 || !retryable {
		t.Errorf("StatusError 500 with ETag text: expected (4, true), got (%d, %v)", code, retryable)
	}

	// 5. A non-412 StatusError whose message merely CONTAINS "412" (e.g. a
	// port number) must NOT be retryable: the "412" substring heuristic is
	// generic-path-only, so it must not broaden the StatusError branch.
	code, retryable = svc.ClassifyLXDError(api.NewStatusError(500, "listen tcp 127.0.0.1:412: bind"), "update")
	if code != 4 || retryable {
		t.Errorf("StatusError 500 with '412' substring: expected (4, false), got (%d, %v)", code, retryable)
	}

	// 6. The generic (non-StatusError) path still honors a plain "412" error
	// string as an ETag conflict.
	code, retryable = svc.ClassifyLXDError(errors.New("http 412 precondition failed"), "update")
	if code != 4 || !retryable {
		t.Errorf("generic '412' error: expected (4, true), got (%d, %v)", code, retryable)
	}
}
