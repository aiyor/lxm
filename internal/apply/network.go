package apply

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/aiyor/lxm/internal/plan"
	"github.com/aiyor/lxm/internal/provider/common"
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
		if step.ACLPost == nil {
			return res, &ErrorInfo{Code: "INTERNAL_ERROR", Name: step.Name, Message: fmt.Sprintf("create_acl step %q has no payload", step.Name)}, ""
		}
		opErr = e.driver.CreateNetworkACL(ctx, *step.ACLPost)
		if opErr == nil {
			// Poll until ACL record is committed and queryable in provider database
			deadline := time.Now().Add(3 * time.Second)
			for time.Now().Before(deadline) {
				if acl, _, err := e.driver.GetNetworkACL(ctx, step.Name); err == nil && acl != nil {
					break
				}
				time.Sleep(50 * time.Millisecond)
			}
		}
	case "update_acl":
		// Fresh ETag and re-fetch immediately before PUT.
		if acl, etag, err := e.driver.GetNetworkACL(ctx, step.Name); err == nil && acl != nil {
			opErr = e.driver.UpdateNetworkACL(ctx, step.Name, *step.ACLPut, etag)
		} else {
			opErr = err
		}
	case "create_vswitch":
		if step.NetPost == nil {
			return res, &ErrorInfo{Code: "INTERNAL_ERROR", Name: step.Name, Message: fmt.Sprintf("create_vswitch step %q has no payload", step.Name)}, ""
		}
		opErr = e.driver.CreateNetwork(ctx, *step.NetPost)
		if opErr == nil {
			// Allow kernel bridge interface, udev, and daemon dnsmasq to fully initialize
			time.Sleep(1 * time.Second)
		}
	case "update_vswitch":
		if net, etag, err := e.driver.GetNetwork(ctx, step.Name); err == nil && net != nil {
			opErr = e.driver.UpdateNetwork(ctx, step.Name, *step.NetPut, etag)
		} else {
			opErr = err
		}
	case "delete_vswitch":
		// Phase 4: delete bridge first, then associated ACL (order is mandatory)
		netErr := e.driver.DeleteNetwork(ctx, step.Name)
		if netErr != nil {
			if code, _ := common.ClassifyError(netErr, "lookup"); code != 5 {
				opErr = netErr
			}
		}
		if opErr == nil {
			aclName := "lxm-" + step.Name
			aclErr := e.driver.DeleteNetworkACL(ctx, aclName)
			if aclErr != nil {
				if code, _ := common.ClassifyError(aclErr, "lookup"); code != 5 {
					opErr = aclErr
				}
			}
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
		code, retryable := common.ClassifyError(opErr, "update")
		return res, &ErrorInfo{
			Code:      exitToErrorCode(code),
			Name:      step.Name,
			Message:   opErr.Error(),
			Retryable: retryable,
		}, ""
	}

	if step.Kind == "update_acl" && step.Tightened {
		// Conntrack lifecycle visibility (§7.6): tightening an ACL (removing or
		// narrowing allows) leaves established flows until conntrack expiry.
		return res, nil, fmt.Sprintf("ACL %q tightened; pre-existing connections may persist until conntrack expiry (see docs: conntrack lifecycle)", step.Name)
	}

	return res, nil, ""
}
