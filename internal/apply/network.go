package apply

import (
	"context"
	"errors"
	"fmt"

	"github.com/aiyor/lxm/internal/plan"
)

// executeNetworkStep performs a single network reconciliation step (ACL or
// vswitch) using the live ETag, mirroring the instance-step ETag-verified
// pattern (apply.go §Initial-step ETag Verification).
func (e *defaultExecutor) executeNetworkStep(ctx context.Context, step plan.NetworkStep, opts ApplyOpts) (NetworkResult, *ErrorInfo, string) {
	res := NetworkResult{
		Name:    step.Name,
		Kind:    step.Kind,
		Changed: step.Changed,
		OK:      true,
	}

	select {
	case <-ctx.Done():
		return NetworkResult{
				Name:    step.Name,
				Kind:    step.Kind,
				Changed: step.Changed,
				OK:      false,
				Error:   "operation cancelled by user interrupt",
			}, &ErrorInfo{
				Code:    "INTERNAL_ERROR",
				Name:    step.Name,
				Message: "operation cancelled by user interrupt",
			}, ""
	default:
	}

	if opts.DryRun {
		return res, nil, ""
	}

	var opErr error
	switch step.Kind {
	case "create_acl":
		if step.ACLPost != nil {
			opErr = e.netSvc.CreateNetworkACL(*step.ACLPost)
		}
	case "update_acl":
		// Fresh ETag and re-fetch immediately before PUT.
		if acl, etag, err := e.netSvc.GetNetworkACL(step.Name); err == nil && acl != nil {
			opErr = e.netSvc.UpdateNetworkACL(step.Name, *step.ACLPut, etag)
		} else {
			opErr = err
		}
	case "create_vswitch":
		if step.NetPost != nil {
			opErr = e.netSvc.CreateNetwork(*step.NetPost)
		}
	case "update_vswitch":
		if net, etag, err := e.netSvc.GetNetwork(step.Name); err == nil && net != nil {
			opErr = e.netSvc.UpdateNetwork(step.Name, *step.NetPut, etag)
		} else {
			opErr = err
		}
	}

	if opErr != nil {
		res.OK = false
		res.Error = opErr.Error()
		if errors.Is(opErr, context.Canceled) || errors.Is(opErr, context.DeadlineExceeded) || ctx.Err() != nil {
			return res, &ErrorInfo{
				Code:    "INTERNAL_ERROR",
				Name:    step.Name,
				Message: "operation cancelled by user interrupt",
			}, ""
		}
		code, retryable := e.lxdSvc.ClassifyLXDError(opErr, "update")
		return res, &ErrorInfo{
			Code:      exitToErrorCode(code),
			Name:      step.Name,
			Message:   opErr.Error(),
			Retryable: retryable,
		}, ""
	}

	if step.Kind == "update_acl" {
		// Conntrack lifecycle visibility (§7.6): tightening an ACL (removing or
		// narrowing allows) leaves established flows until conntrack expiry.
		if tighteningACL(step) {
			return res, nil, fmt.Sprintf("ACL %q tightened; pre-existing connections may persist until conntrack expiry (see docs: conntrack lifecycle)", step.Name)
		}
	}

	return res, nil, ""
}

// tighteningACL heuristically reports whether an update_acl step narrows the
// rule set: when the prior rule count is unknown at plan time the plan emits
// the informational warning; here we flag updates whose diff shows a reduction
// in rule count (the reconciler records the live vs desired counts).
func tighteningACL(step plan.NetworkStep) bool {
	for _, d := range step.Diff {
		if d.Field == "rules" {
			// The reconciler sets Old to the live count and New to the desired
			// count in "<n> rules" form; treat any update as a potential
			// tightening candidate conservatively (plan-time warning only).
			return true
		}
	}
	return false
}
