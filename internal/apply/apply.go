package apply

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/aiyor/lxm/internal/fleet"
	"github.com/aiyor/lxm/internal/plan"
	"github.com/aiyor/lxm/internal/provider"
	"github.com/aiyor/lxm/internal/provider/common"
	"github.com/aiyor/lxm/internal/recipe"
)

// ApplyOpts configures execution behavior.
type ApplyOpts struct {
	Jobs         int  `json:"jobs"`
	DryRun       bool `json:"dry_run"`
	Force        bool `json:"force"`
	Prune        bool `json:"prune"`
	IsSingleFile bool `json:"is_single_file"`
	NoStart      bool `json:"no_start"`
	Wait         bool `json:"wait"`
}

// ContainerResult records the execution outcome for a single container.
type ContainerResult struct {
	Container  string `json:"container"`
	Action     string `json:"action"`
	Changed    bool   `json:"changed"`
	OK         bool   `json:"ok"`
	DurationMS int64  `json:"duration_ms"`
	Error      string `json:"error,omitempty"`
}

// NetworkResult records the execution outcome for a single network step
// (create/update ACL or vswitch). Rendered in the envelope's additive
// `network_results` field (§9).
type NetworkResult struct {
	Name       string `json:"name"`
	Kind       string `json:"kind"`
	Changed    bool   `json:"changed"`
	OK         bool   `json:"ok"`
	DurationMS int64  `json:"duration_ms"`
	Error      string `json:"error,omitempty"`
}

// ErrorInfo describes a structured error entry for result envelope serialization.
type ErrorInfo struct {
	Code      string `json:"code"`
	Container string `json:"container,omitempty"`
	Name      string `json:"name,omitempty"`
	Message   string `json:"message"`
	Retryable bool   `json:"retryable"`
}

// ApplyReport summarizes the overall outcome of applying a Plan.
type ApplyReport struct {
	Plan           *plan.Plan        `json:"plan"`
	Results        []ContainerResult `json:"results"`
	NetworkResults []NetworkResult   `json:"network_results,omitempty"`
	ExitCode       int               `json:"exit_code"`
	Errors         []ErrorInfo       `json:"errors"`
	Warnings       []string          `json:"warnings"`
}

// Executor executes a reconciliation Plan against LXD/Incus.
type Executor interface {
	Apply(ctx context.Context, p *plan.Plan, opts ApplyOpts) (*ApplyReport, error)
}

// Services bundles the driver services used by the executor.
type Services = provider.Driver

type defaultExecutor struct {
	driver provider.Driver
}

// NewExecutor creates a new default Executor using the provided provider Driver.
func NewExecutor(driver provider.Driver) Executor {
	return &defaultExecutor{driver: driver}
}

func (e *defaultExecutor) Apply(ctx context.Context, p *plan.Plan, opts ApplyOpts) (*ApplyReport, error) {
	if p == nil {
		return nil, fmt.Errorf("plan cannot be nil")
	}

	report := &ApplyReport{
		Plan:           p,
		Results:        []ContainerResult{},
		NetworkResults: []NetworkResult{},
		Errors:         []ErrorInfo{},
		Warnings:       []string{},
		ExitCode:       0,
	}

	// Single-file prune restriction (C2)
	if opts.IsSingleFile && opts.Prune {
		report.ExitCode = 2 // USAGE_ERROR
		report.Errors = append(report.Errors, ErrorInfo{
			Code:    "USAGE_ERROR",
			Message: "--prune is restricted to directory targets",
		})
		return report, fmt.Errorf("--prune is restricted to directory targets")
	}

	jobs := opts.Jobs
	if jobs <= 0 {
		jobs = 5
	}

	sem := make(chan struct{}, jobs)
	var mu sync.Mutex
	var wg sync.WaitGroup

	worstExitCode := 0

	// Phase -1: remote-image fetch ops (image: remote:alias, IMAGE-SPEC §9).
	// Every fetch must land before any instance mutation, so it runs first —
	// before the volume and network phases. Ops are deduplicated by
	// (RemoteURL, Alias, Type) so a shared base image across a fleet is
	// fetched once. A fetch failure aborts the apply (phase-abort semantics)
	// with exit 4; "Alias already exists" is treated as success (§7.7).
	// Dry-run never touches the remote.
	if !opts.DryRun {
		imageFailed := false
		for _, op := range dedupImageOps(p.Steps) {
			if err := e.executeImageOp(ctx, op); err != nil {
				imageFailed = true
				_, retryable := common.ClassifyError(err, "update")
				// Spec §7.4: transient network errors are marked retryable.
				// A fetch that times out (waitOpContext deadline) is transient
				// but ClassifyError only marks ETag/412 conflicts, so flag
				// the deadline case explicitly.
				if errors.Is(err, context.DeadlineExceeded) {
					retryable = true
				}
				report.Errors = append(report.Errors, ErrorInfo{
					Code:      "LXD_ERROR",
					Message:   fmt.Sprintf("fetching image %q from remote %q: %v", op.Alias, op.RemoteURL, err),
					Retryable: retryable,
				})
				worstExitCode = selectWorstExitCode(worstExitCode, 4)
				break
			}
		}
		if imageFailed {
			report.ExitCode = worstExitCode
			return report, nil
		}
	}

	// Phase 0: storage volume ops (STORAGE-SPEC §10). VolumeOps (create/grow)
	// must complete before any instance mutation and before the network phase.
	// Volume deletions are deferred to Phase 3 (after instance devices are detached).
	// A failure aborts the apply (phase-abort semantics) with exit 4. Dry-run never
	// touches storage volumes.
	if !opts.DryRun {
		storageFailed := false
		for _, step := range p.Steps {
			if step.Action != "delete" {
				e.backfillVolumeMarkers(ctx, step)
			}
			for _, op := range step.VolumeOps {
				if op.Op == "delete" {
					continue // Defer to Phase 3
				}
				if err := e.executeVolumeOp(ctx, op); err != nil {
					storageFailed = true
					report.Errors = append(report.Errors, ErrorInfo{
						Code:      "LXD_ERROR",
						Container: step.Container,
						Message:   fmt.Sprintf("storage volume %q in pool %q: %v", op.Name, op.Pool, err),
					})
					worstExitCode = selectWorstExitCode(worstExitCode, 4)
					break
				}
			}
			if storageFailed {
				break
			}
		}
		if storageFailed {
			report.ExitCode = worstExitCode
			return report, nil
		}
	}

	// Phase 1: pre-instance network steps (ACLs, then vswitches).
	// A network-step error aborts the apply before any instance step runs.
	preNetSteps, postNetSteps := partitionNetworkSteps(p.NetworkSteps)
	networkFailed := false
	for _, nstep := range preNetSteps {
		startTs := time.Now()
		nres, errInfo, warnMsg := e.executeNetworkStep(ctx, nstep, opts)
		nres.DurationMS = time.Since(startTs).Milliseconds()

		report.NetworkResults = append(report.NetworkResults, nres)
		if warnMsg != "" {
			report.Warnings = append(report.Warnings, warnMsg)
		}
		if errInfo != nil {
			networkFailed = true
			report.Errors = append(report.Errors, *errInfo)
			worstExitCode = selectWorstExitCode(worstExitCode, errorCodeToExit(errInfo.Code))
			break
		}
	}
	if networkFailed {
		report.ExitCode = worstExitCode
		return report, nil
	}

	if !opts.DryRun {
		for _, nstep := range preNetSteps {
			if nstep.Kind == "create_vswitch" && nstep.Changed {
				// Allow daemon dnsmasq fork to initialize directory structures on newly created bridges
				time.Sleep(300 * time.Millisecond)
				break
			}
		}
	}

	// Phase 2: instance steps (creates, updates, rebuilds, device detachments).
	for _, step := range p.Steps {
		wg.Add(1)
		go func(s plan.Step) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()

			startTs := time.Now()
			res, errInfo, warnMsg := e.executeStep(ctx, s, opts)
			res.DurationMS = time.Since(startTs).Milliseconds()

			mu.Lock()
			defer mu.Unlock()

			report.Results = append(report.Results, res)
			if warnMsg != "" {
				report.Warnings = append(report.Warnings, warnMsg)
			}
			if errInfo != nil {
				report.Errors = append(report.Errors, *errInfo)
				code := errorCodeToExit(errInfo.Code)
				worstExitCode = selectWorstExitCode(worstExitCode, code)
			}
		}(step)
	}

	wg.Wait()

	// Phase 3: post-instance storage volume deletions (detached in Phase 2).
	if !opts.DryRun {
		seenVols := make(map[string]bool)
		for _, step := range p.Steps {
			for _, op := range step.VolumeOps {
				if op.Op != "delete" {
					continue
				}
				key := op.Pool + "/" + op.Name
				if seenVols[key] {
					continue
				}
				seenVols[key] = true
				if err := e.executeVolumeOp(ctx, op); err != nil {
					report.Errors = append(report.Errors, ErrorInfo{
						Code:      "LXD_ERROR",
						Container: step.Container,
						Message:   fmt.Sprintf("deleting storage volume %q in pool %q: %v", op.Name, op.Pool, err),
					})
					worstExitCode = selectWorstExitCode(worstExitCode, 4)
				}
			}
		}
	}

	// Phase 4: post-instance network bridge & ACL deletions.
	for _, nstep := range postNetSteps {
		startTs := time.Now()
		nres, errInfo, warnMsg := e.executeNetworkStep(ctx, nstep, opts)
		nres.DurationMS = time.Since(startTs).Milliseconds()

		report.NetworkResults = append(report.NetworkResults, nres)
		if warnMsg != "" {
			report.Warnings = append(report.Warnings, warnMsg)
		}
		if errInfo != nil {
			report.Errors = append(report.Errors, *errInfo)
			worstExitCode = selectWorstExitCode(worstExitCode, errorCodeToExit(errInfo.Code))
		}
	}

	report.ExitCode = worstExitCode
	return report, nil
}

// partitionNetworkSteps splits network steps into pre-instance (create/update
// ACLs and vswitches) and post-instance (delete_vswitch).
func partitionNetworkSteps(steps []plan.NetworkStep) (pre, post []plan.NetworkStep) {
	sorted := sortNetworkSteps(steps)
	for _, s := range sorted {
		if s.Kind == "delete_vswitch" {
			post = append(post, s)
		} else {
			pre = append(pre, s)
		}
	}
	return pre, post
}

// sortNetworkSteps orders network steps so ACL steps run before vswitch steps.
func sortNetworkSteps(steps []plan.NetworkStep) []plan.NetworkStep {
	out := make([]plan.NetworkStep, len(steps))
	copy(out, steps)
	for i := 1; i < len(out); i++ {
		for j := i; j > 0 && plan.NetworkStepKindOrder(out[j-1].Kind) > plan.NetworkStepKindOrder(out[j].Kind); j-- {
			out[j-1], out[j] = out[j], out[j-1]
		}
	}
	return out
}

// executeVolumeOp applies one idempotent storage-volume mutation (Phase 0/3).
func (e *defaultExecutor) executeVolumeOp(ctx context.Context, op plan.VolumeOp) error {
	switch op.Op {
	case "create":
		vol, _, err := e.driver.GetStoragePoolVolume(ctx, op.Pool, "custom", op.Name)
		if err != nil {
			if code, _ := common.ClassifyError(err, "lookup"); code == 5 {
				return e.createVolume(ctx, op)
			}
			return err
		}
		if vol.ContentType != op.ContentType {
			return fmt.Errorf("volume exists with content type %q, disk requires %q", vol.ContentType, op.ContentType)
		}
		return e.growIfNeeded(ctx, op)
	case "grow":
		return e.growIfNeeded(ctx, op)
	case "delete":
		return e.deleteVolume(ctx, op)
	default:
		return fmt.Errorf("unknown volume op %q", op.Op)
	}
}

func (e *defaultExecutor) createVolume(ctx context.Context, op plan.VolumeOp) error {
	req := provider.StorageVolumeCreateRequest{
		Name:        op.Name,
		Type:        "custom",
		ContentType: op.ContentType,
		Config: map[string]string{
			"user.lxm.managed": "true",
		},
	}
	if op.Size != "" {
		req.Config["size"] = op.Size
	}
	return e.driver.CreateStoragePoolVolume(ctx, op.Pool, req)
}

func (e *defaultExecutor) deleteVolume(ctx context.Context, op plan.VolumeOp) error {
	err := e.driver.DeleteStoragePoolVolume(ctx, op.Pool, "custom", op.Name)
	if err != nil {
		if code, _ := common.ClassifyError(err, "lookup"); code == 5 {
			return nil // idempotent success if already absent
		}
		return err
	}
	return nil
}

// backfillVolumeMarkers ensures managed custom volumes attached to active instances
// carry user.lxm.managed: "true" and tracking metadata even in steady-state (no-diff).
func (e *defaultExecutor) backfillVolumeMarkers(ctx context.Context, step plan.Step) {
	for _, d := range step.ManagedDisks {
		vol, etag, err := e.driver.GetStoragePoolVolume(ctx, d.Pool, "custom", d.Source)
		if err != nil || vol == nil {
			continue
		}
		if vol.Config == nil || vol.Config["user.lxm.managed"] != "true" {
			put := provider.StorageVolumeUpdateRequest{
				Config:      vol.Config,
				Description: vol.Description,
			}
			if put.Config == nil {
				put.Config = make(map[string]string)
			}
			put.Config["user.lxm.managed"] = "true"
			put.Config["user.lxm.instance"] = step.Container
			put.Config["user.lxm.disk"] = d.Name
			_ = e.driver.UpdateStoragePoolVolume(ctx, d.Pool, "custom", d.Source, put, etag)
		}
	}
}

// dedupImageOps collects the distinct fetch ops across all steps,
// deduplicated by (RemoteURL, Alias, Type) so a shared base image is fetched
// once per apply (IMAGE-SPEC §9). Order follows plan step order.
func dedupImageOps(steps []plan.Step) []plan.ImageOp {
	seen := make(map[string]bool)
	var out []plan.ImageOp
	for _, step := range steps {
		for _, op := range step.ImageOps {
			key := op.RemoteURL + "\x00" + op.Alias + "\x00" + op.Type
			if seen[key] {
				continue
			}
			seen[key] = true
			out = append(out, op)
		}
	}
	return out
}

// executeImageOp applies one idempotent remote-image fetch (Phase -1,
// IMAGE-SPEC §9). "Alias already exists" is a success/no-op: it means
// another concurrent apply already fetched and tagged the image (§7.7).
func (e *defaultExecutor) executeImageOp(ctx context.Context, op plan.ImageOp) error {
	err := e.driver.CopyRemoteImage(ctx, op.RemoteURL, op.Alias, op.Type, op.LocalAlias)
	if err != nil && strings.Contains(strings.ToLower(err.Error()), "alias already exists") {
		return nil
	}
	return err
}

// refreshImageRecord re-records user.lxm.image on the live instance after a
// rebuild (the non-fallback recreate path).
func (e *defaultExecutor) refreshImageRecord(ctx context.Context, step plan.Step) error {
	if step.InstancesPost == nil {
		return nil
	}
	image := step.InstancesPost.Config["user.lxm.image"]
	if image == "" {
		return nil
	}
	inst, etag, err := e.driver.GetInstance(ctx, step.Container)
	if err != nil {
		return err
	}
	put := inst.Writable()
	if put.Config == nil {
		put.Config = make(map[string]string)
	}
	put.Config["user.lxm.image"] = image
	return e.driver.UpdateInstance(ctx, step.Container, put, etag)
}

// growIfNeeded grows the volume when the desired size exceeds the live size.
// No-ops when sizes are unparsable or already satisfied.
func (e *defaultExecutor) growIfNeeded(ctx context.Context, op plan.VolumeOp) error {
	vol, etag, err := e.driver.GetStoragePoolVolume(ctx, op.Pool, "custom", op.Name)
	if err != nil {
		return err
	}
	if op.Size == "" {
		return nil
	}
	liveSize := vol.Config["size"]
	if liveSize == "" {
		return nil
	}
	liveBytes, err := common.ParseByteSizeString(liveSize)
	if err != nil {
		return fmt.Errorf("parsing live size %q for volume %q: %w", liveSize, op.Name, err)
	}
	desiredBytes, err := common.ParseByteSizeString(op.Size)
	if err != nil {
		return fmt.Errorf("parsing desired size %q for volume %q: %w", op.Size, op.Name, err)
	}
	if desiredBytes <= liveBytes {
		return nil
	}
	put := provider.StorageVolumeUpdateRequest{Config: make(map[string]string, len(vol.Config)+1)}
	for k, v := range vol.Config {
		put.Config[k] = v
	}
	put.Config["size"] = op.Size
	return e.driver.UpdateStoragePoolVolume(ctx, op.Pool, "custom", op.Name, put, etag)
}

// stopBeforeDelete stops the instance so a non-forced delete can proceed.
func (e *defaultExecutor) stopBeforeDelete(ctx context.Context, name string) error {
	inst, _, err := e.driver.GetInstance(ctx, name)
	if err != nil {
		if code, _ := common.ClassifyError(err, "lookup"); code == 5 {
			return nil // already gone: nothing to stop
		}
		return err
	}
	if inst.StatusCode == 102 || inst.Status == "Stopped" {
		return nil
	}
	return e.driver.UpdateInstanceState(ctx, name, "stop", true)
}

// isNotFound reports whether err is a not-found error, so delete and
// recreate steps stay idempotent when a container vanishes between plan and
// apply.
func (e *defaultExecutor) isNotFound(err error) bool {
	code, _ := common.ClassifyError(err, "lookup")
	return code == 5
}

func (e *defaultExecutor) executeStep(ctx context.Context, step plan.Step, opts ApplyOpts) (ContainerResult, *ErrorInfo, string) {
	res := ContainerResult{
		Container: step.Container,
		Action:    step.Action,
		Changed:   step.Changed,
		OK:        true,
	}

	select {
	case <-ctx.Done():
		return ContainerResult{
				Container: step.Container,
				Action:    step.Action,
				OK:        false,
				Error:     "operation cancelled by user interrupt",
			}, &ErrorInfo{
				Code:      "INTERNAL_ERROR",
				Container: step.Container,
				Message:   "operation cancelled by user interrupt",
			}, ""
	default:
	}

	if opts.DryRun {
		return res, nil, ""
	}

	if step.Action == "noop" {
		var waitWarn string
		var waitErrInfo *ErrorInfo
		if step.WaitPolicy != nil || step.Wait {
			waitErrInfo, waitWarn = e.checkWaitPolicy(ctx, step, opts)
			if waitErrInfo != nil {
				res.OK = false
				res.Error = waitErrInfo.Message
				return res, waitErrInfo, waitWarn
			}
		}
		if len(step.Recipes) > 0 {
			recipeErrInfo := e.executeRecipes(ctx, step, opts)
			if recipeErrInfo != nil {
				res.OK = false
				res.Error = recipeErrInfo.Message
				return res, recipeErrInfo, waitWarn
			}
		}
		return res, nil, waitWarn
	}

	// Initial-step ETag Verification (F1)
	if step.Action != "create" && step.ETag != "" {
		inst, currentETag, err := e.driver.GetInstance(ctx, step.Container)
		if err == nil && inst != nil {
			if currentETag != "" && currentETag != step.ETag {
				res.OK = false
				res.Error = "etag mismatch: container modified externally"
				return res, &ErrorInfo{
					Code:      "LXD_ERROR",
					Container: step.Container,
					Message:   "etag mismatch: container modified externally",
					Retryable: true,
				}, ""
			}
		}
	}

	var opErr error
	switch step.Action {
	case "create":
		req := provider.InstanceCreateRequest{
			Name: step.Container,
		}
		if step.InstancesPost != nil {
			req = *step.InstancesPost
		}
		for attempt := 0; attempt < 10; attempt++ {
			opErr = e.driver.CreateInstance(ctx, req)
			if opErr == nil {
				break
			}
			if strings.Contains(opErr.Error(), "does not exist") || strings.Contains(opErr.Error(), "no such file or directory") {
				time.Sleep(500 * time.Millisecond)
				continue
			}
			break
		}
		if opErr == nil && !opts.NoStart && (step.PowerTransition == "start" || step.PowerTransition == "") {
			for attempt := 0; attempt < 10; attempt++ {
				opErr = e.driver.UpdateInstanceState(ctx, step.Container, "start", false)
				if opErr == nil {
					break
				}
				if strings.Contains(opErr.Error(), "does not exist") || strings.Contains(opErr.Error(), "no such file or directory") {
					time.Sleep(500 * time.Millisecond)
					continue
				}
				break
			}
		}

	case "update":
		// Fresh ETag and instance re-fetch immediately before PUT
		inst, freshETag, err := e.driver.GetInstance(ctx, step.Container)
		if err != nil {
			opErr = err
		} else {
			put := provider.InstanceUpdateRequest{}
			if step.InstancePut != nil {
				put = *step.InstancePut
			}

			isLiveRunning := inst != nil && (inst.Status == "Running" || inst.StatusCode == 103)
			restartAfter := false
			if isLiveRunning && (step.PowerTransition == "restart" || step.PowerTransition == "stop") {
				if err := e.driver.UpdateInstanceState(ctx, step.Container, "stop", false); err != nil {
					opErr = err
				} else {
					if step.PowerTransition == "restart" {
						restartAfter = true
					}
					// Re-fetch ETag after stopping instance
					_, freshETag, err = e.driver.GetInstance(ctx, step.Container)
					if err != nil {
						opErr = err
					}
				}
			}

			if opErr == nil {
				opErr = e.driver.UpdateInstance(ctx, step.Container, put, freshETag)
				if opErr == nil {
					if restartAfter {
						opErr = e.driver.UpdateInstanceState(ctx, step.Container, "start", false)
					} else if step.PowerTransition != "" && step.PowerTransition != "restart" && step.PowerTransition != "stop" {
						opErr = e.driver.UpdateInstanceState(ctx, step.Container, step.PowerTransition, false)
					}
				}
			}
		}

	case "recreate":
		if step.PurgeSnapshots && !opts.Force {
			res.OK = false
			res.Error = "rebuild requires --force gate when instance snapshots exist"
			return res, &ErrorInfo{
				Code:      "CONFIG_ERROR",
				Container: step.Container,
				Message:   "rebuild: WARNING — all instance snapshots will be permanently destroyed (requires --force)",
			}, ""
		}

		if step.RebuildFallback {
			if !opts.Force {
				res.OK = false
				res.Error = "recreate fallback requires --force"
				return res, &ErrorInfo{
					Code:      "CONFIG_ERROR",
					Container: step.Container,
					Message:   "recreate (delete + create: WARNING — all instance snapshots will be permanently destroyed) requires --force",
				}, ""
			}
			if err := e.stopBeforeDelete(ctx, step.Container); err != nil {
				opErr = err
			} else if err := e.driver.DeleteInstance(ctx, step.Container); err != nil && !e.isNotFound(err) {
				opErr = err
			} else {
				createReq := provider.InstanceCreateRequest{Name: step.Container}
				if step.InstancesPost != nil {
					createReq = *step.InstancesPost
				}
				opErr = e.driver.CreateInstance(ctx, createReq)
				if opErr == nil && !opts.NoStart && step.PowerTransition == "start" {
					opErr = e.driver.UpdateInstanceState(ctx, step.Container, "start", false)
				}
			}
		} else {
			req := provider.InstanceRebuildRequest{}
			if step.RebuildPost != nil {
				req = *step.RebuildPost
			}
			opErr = e.driver.RebuildInstance(ctx, step.Container, req)
			if opErr == nil {
				opErr = e.refreshImageRecord(ctx, step)
			}
		}

	case "delete":
		if err := e.stopBeforeDelete(ctx, step.Container); err != nil {
			opErr = err
		} else if err := e.driver.DeleteInstance(ctx, step.Container); err != nil && !e.isNotFound(err) {
			opErr = err
		}

	case "start":
		if !opts.NoStart {
			opErr = e.driver.UpdateInstanceState(ctx, step.Container, "start", false)
		}

	case "stop":
		opErr = e.driver.UpdateInstanceState(ctx, step.Container, "stop", false)
	}

	if opErr == nil && (step.Action == "recreate" || step.Action == "delete") {
		_ = fleet.DefaultKnownHostsManager().PurgeContainerKeyContext(ctx, step.Container)
	}

	if opErr != nil {
		res.OK = false
		res.Error = opErr.Error()
		if errors.Is(opErr, context.Canceled) || errors.Is(opErr, context.DeadlineExceeded) || ctx.Err() != nil {
			return res, &ErrorInfo{
				Code:      "INTERNAL_ERROR",
				Container: step.Container,
				Message:   "operation cancelled by user interrupt",
			}, ""
		}
		code, retryable := common.ClassifyError(opErr, "update")
		errCodeStr := exitToErrorCode(code)
		return res, &ErrorInfo{
			Code:      errCodeStr,
			Container: step.Container,
			Message:   opErr.Error(),
			Retryable: retryable,
		}, ""
	}

	// Post-mutation safety policy: wait and recipes (skip if step or target power transition is stop or delete)
	var warnMsg string
	if step.Action != "delete" && step.Action != "stop" && step.PowerTransition != "stop" {
		waitErrInfo, wMsg := e.checkWaitPolicy(ctx, step, opts)
		warnMsg = wMsg
		if waitErrInfo != nil {
			res.OK = false
			res.Error = waitErrInfo.Message
			return res, waitErrInfo, warnMsg
		}

		recipeErrInfo := e.executeRecipes(ctx, step, opts)
		if recipeErrInfo != nil {
			res.OK = false
			res.Error = recipeErrInfo.Message
			return res, recipeErrInfo, warnMsg
		}
	}

	return res, nil, warnMsg
}

var transientAgentErrors = []string{
	"LXD VM agent is not currently running",
	"Incus VM agent is not currently running",
	"VM agent isn't currently running",
	"VM agent is not currently running",
	"agent isn't currently running",
	"agent is not currently running",
	"agent not running",
	"Failed connecting to lxd-agent",
	"Failed connecting to incus-agent",
	"The LXD agent is not running on this instance",
	"The Incus agent is not running on this instance",
	"The agent is not running on this instance",
	"LXD agent not running",
	"Incus agent not running",
	"Failed to connect to lxd-agent",
	"Failed to connect to incus-agent",
	"Failed to connect to instance socket",
	"websocket: close 1006 (abnormal closure)",
}

// IsTransientAgentError reports whether an execution error indicates a transient agent offline state.
func IsTransientAgentError(errMsg string) bool {
	for _, pattern := range transientAgentErrors {
		if strings.Contains(errMsg, pattern) {
			return true
		}
	}
	return false
}

func (e *defaultExecutor) checkWaitPolicy(ctx context.Context, step plan.Step, opts ApplyOpts) (*ErrorInfo, string) {
	if step.WaitPolicy == nil && !step.Wait {
		return nil, ""
	}

	required := true
	if step.WaitPolicy != nil {
		required = step.WaitPolicy.Required
	}
	if envReq := os.Getenv("LXM_WAIT_REQUIRED"); envReq != "" {
		required = (envReq == "true" || envReq == "1")
	}
	if opts.Wait {
		required = true
	}

	select {
	case <-ctx.Done():
		return &ErrorInfo{
			Code:      "INTERNAL_ERROR",
			Container: step.Container,
			Message:   "wait policy cancelled by user interrupt",
		}, ""
	default:
	}

	if step.WaitPolicy != nil && (strings.HasPrefix(step.WaitPolicy.CloudInit, "timeout") || strings.HasPrefix(step.WaitPolicy.Network, "timeout") || strings.HasPrefix(step.WaitPolicy.Agent, "timeout")) {
		if required {
			return &ErrorInfo{
				Code:      "WAIT_TIMEOUT",
				Container: step.Container,
				Message:   "wait policy timed out",
			}, ""
		}
		return nil, fmt.Sprintf("wait policy timed out on container %q (soft wait)", step.Container)
	}

	// 0. VM Agent Handshake Gate (VM instances only)
	if inst, _, err := e.driver.GetInstance(ctx, step.Container); err == nil && inst != nil && (inst.Type == provider.InstanceTypeVM || inst.Type == provider.InstanceTypeVirtualMachine || inst.Type == "virtual-machine") && (inst.Status == "Running" || inst.StatusCode == 103) {
		agentTimeout := 120 * time.Second
		if step.WaitPolicy != nil && step.WaitPolicy.Agent != "" {
			if d, err := time.ParseDuration(step.WaitPolicy.Agent); err == nil {
				agentTimeout = d
			}
		}

		agentCtx, cancelAgent := context.WithTimeout(ctx, agentTimeout)
		defer cancelAgent()

		agentReady := false

		// Immediate initial probe
		execCtx, cancelExec := context.WithTimeout(agentCtx, 3*time.Second)
		_, execErr := e.driver.ExecInstance(execCtx, step.Container, []string{"systemctl", "is-system-running"}, 0, nil)
		cancelExec()
		if execErr == nil {
			agentReady = true
		} else if !IsTransientAgentError(execErr.Error()) {
			agentReady = true
		}

		if !agentReady {
			pollInterval := 2 * time.Second
			if step.WaitPolicy != nil && step.WaitPolicy.Poll != "" {
				if d, err := time.ParseDuration(step.WaitPolicy.Poll); err == nil && d > 0 {
					pollInterval = d
				}
			}
			ticker := time.NewTicker(pollInterval)
			defer ticker.Stop()

		agentLoop:
			for {
				select {
				case <-agentCtx.Done():
					break agentLoop
				case <-ticker.C:
					execCtx, cancelExec := context.WithTimeout(agentCtx, 3*time.Second)
					_, execErr := e.driver.ExecInstance(execCtx, step.Container, []string{"systemctl", "is-system-running"}, 0, nil)
					cancelExec()

					if execErr == nil {
						agentReady = true
						break agentLoop
					}

					if !IsTransientAgentError(execErr.Error()) {
						agentReady = true
						break agentLoop
					}
				}
			}
		}

		if !agentReady {
			if required {
				return &ErrorInfo{
					Code:      "WAIT_TIMEOUT",
					Container: step.Container,
					Message:   fmt.Sprintf("agent wait timed out after %s on %q", agentTimeout, step.Container),
				}, ""
			}
			return nil, fmt.Sprintf("agent wait timed out after %s on VM %q (soft wait)", agentTimeout, step.Container)
		}
	}

	// Real wait execution: if container is running, check cloud-init status --wait
	if step.WaitPolicy != nil && step.WaitPolicy.CloudInit != "" {
		inst, _, err := e.driver.GetInstance(ctx, step.Container)
		if err != nil || inst == nil || (inst.Status != "Running" && inst.StatusCode != 103) {
			return nil, ""
		}

		timeout := 600 * time.Second
		if d, err := time.ParseDuration(step.WaitPolicy.CloudInit); err == nil {
			timeout = d
		}
		waitCtx, cancel := context.WithTimeout(ctx, timeout)
		defer cancel()

		type execOutcome struct {
			res provider.ExecResult
			err error
		}
		doneChan := make(chan execOutcome, 1)
		go func() {
			res, execErr := e.driver.ExecInstance(waitCtx, step.Container, []string{"cloud-init", "status", "--wait"}, 0, nil)
			doneChan <- execOutcome{res: res, err: execErr}
		}()

		select {
		case out := <-doneChan:
			if out.err != nil {
				return &ErrorInfo{
					Code:      "LXD_ERROR",
					Container: step.Container,
					Message:   fmt.Sprintf("cloud-init wait exec error on %q: %v", step.Container, out.err),
					Retryable: true,
				}, ""
			}
			if out.res.ExitCode != 0 {
				if out.res.ExitCode == 127 || strings.Contains(out.res.Stderr, "executable file not found") {
					return nil, fmt.Sprintf("cloud-init not installed on container %q (skipping wait)", step.Container)
				}
				if required {
					return &ErrorInfo{
						Code:      "WAIT_TIMEOUT",
						Container: step.Container,
						Message:   fmt.Sprintf("cloud-init wait status exited %d on %q", out.res.ExitCode, step.Container),
					}, ""
				}
				return nil, fmt.Sprintf("cloud-init wait status exited %d on container %q (soft wait)", out.res.ExitCode, step.Container)
			}
		case <-waitCtx.Done():
			if ctx.Err() != nil || waitCtx.Err() == context.Canceled {
				return &ErrorInfo{
					Code:      "INTERNAL_ERROR",
					Container: step.Container,
					Message:   "wait policy cancelled by user interrupt",
				}, ""
			}
			if waitCtx.Err() == context.DeadlineExceeded {
				if required {
					return &ErrorInfo{
						Code:      "WAIT_TIMEOUT",
						Container: step.Container,
						Message:   fmt.Sprintf("cloud-init wait timed out after %s on %q", timeout, step.Container),
					}, ""
				}
				return nil, fmt.Sprintf("cloud-init wait timed out after %s on container %q (soft wait)", timeout, step.Container)
			}
		}
	}

	// Real network wait execution: poll container for IP address assignment or network readiness
	if step.WaitPolicy != nil && step.WaitPolicy.Network != "" && !strings.HasPrefix(step.WaitPolicy.Network, "timeout") {
		inst, _, err := e.driver.GetInstance(ctx, step.Container)
		if err == nil && inst != nil && (inst.Status == "Running" || inst.StatusCode == 103) {
			netTimeout := 60 * time.Second
			if d, err := time.ParseDuration(step.WaitPolicy.Network); err == nil {
				netTimeout = d
			}
			netCtx, cancelNet := context.WithTimeout(ctx, netTimeout)
			defer cancelNet()

			netReady := false
			ticker := time.NewTicker(200 * time.Millisecond)
			defer ticker.Stop()

		netLoop:
			for {
				select {
				case <-netCtx.Done():
					break netLoop
				case <-ticker.C:
					res, execErr := e.driver.ExecInstance(netCtx, step.Container, []string{"hostname", "-I"}, 0, nil)
					if execErr == nil && res.ExitCode == 0 && strings.TrimSpace(res.Stdout) != "" {
						netReady = true
						break netLoop
					}
					if execErr == nil && (res.ExitCode == 127 || strings.Contains(res.Stderr, "executable file not found")) {
						return nil, fmt.Sprintf("hostname not installed on container %q (skipping network wait)", step.Container)
					}
				}
			}

			if !netReady {
				if ctx.Err() != nil {
					return &ErrorInfo{
						Code:      "INTERNAL_ERROR",
						Container: step.Container,
						Message:   "network wait policy cancelled by user interrupt",
					}, ""
				}
				if required {
					return &ErrorInfo{
						Code:      "WAIT_TIMEOUT",
						Container: step.Container,
						Message:   fmt.Sprintf("network wait timed out after %s on %q", netTimeout, step.Container),
					}, ""
				}
				return nil, fmt.Sprintf("network wait timed out after %s on container %q (soft wait)", netTimeout, step.Container)
			}
		}
	}

	return nil, ""
}

func (e *defaultExecutor) executeRecipes(ctx context.Context, step plan.Step, opts ApplyOpts) *ErrorInfo {
	if len(step.Recipes) == 0 || opts.DryRun {
		return nil
	}

	inst, _, err := e.driver.GetInstance(ctx, step.Container)
	if err != nil || inst == nil {
		return &ErrorInfo{
			Code:      "TARGET_NOT_FOUND",
			Container: step.Container,
			Message:   fmt.Sprintf("container %q not found for recipe execution", step.Container),
		}
	}

	// M8 Guard: Container must be in Running status for recipe execution
	if inst.Status != "Running" && inst.StatusCode != 103 && step.Action == "noop" {
		return nil
	}

	needsRun := opts.Force
	var recipesToRun []*recipe.RecipeMetadata
	var hashKeys []string
	var scriptPaths []string

	for _, rStep := range step.Recipes {
		rMeta, err := recipe.LoadRecipe(rStep.Path, step.ConfigBaseDir)
		if err != nil {
			return &ErrorInfo{
				Code:      "CONFIG_ERROR",
				Container: step.Container,
				Message:   err.Error(),
			}
		}

		hashKey := recipe.PathQualifiedHashKey(rStep.Path, rMeta.Name)
		scriptFile := rStep.Path
		if len(rMeta.Scripts) > 0 {
			scriptFile = rMeta.Scripts[0]
		}
		currentHash, err := recipe.ComputeScriptHash(scriptFile, step.ConfigBaseDir)
		if err != nil {
			return &ErrorInfo{
				Code:      "CONFIG_ERROR",
				Container: step.Container,
				Message:   err.Error(),
			}
		}

		storedHash := inst.Config[hashKey]
		if opts.Force || storedHash != currentHash {
			needsRun = true
		}
		recipesToRun = append(recipesToRun, rMeta)
		hashKeys = append(hashKeys, hashKey)
		scriptPaths = append(scriptPaths, scriptFile)
	}

	if !needsRun {
		return nil
	}

	snapshotTaken := false
	for i, rMeta := range recipesToRun {
		scriptFile := scriptPaths[i]
		select {
		case <-ctx.Done():
			return &ErrorInfo{
				Code:      "INTERNAL_ERROR",
				Container: step.Container,
				Message:   "recipe execution cancelled by user interrupt",
			}
		default:
		}

		// Snapshot-before-recipe per recipe (H4)
		if rMeta.IsSnapshotEnabled() && !snapshotTaken && !opts.DryRun {
			snapName := fmt.Sprintf("user.lxm.snap.%s-%d", step.Container, time.Now().UnixNano())
			if snapErr := e.driver.CreateInstanceSnapshot(ctx, step.Container, snapName, false); snapErr != nil {
				return &ErrorInfo{
					Code:      "LXD_ERROR",
					Container: step.Container,
					Message:   fmt.Sprintf("creating snapshot %q on %q: %v", snapName, step.Container, snapErr),
				}
			}
			snapshotTaken = true
		}

		runAs := rMeta.GetRunAs()
		if runAs == "root" && i < len(step.Recipes) && step.Recipes[i].RunAs != "" {
			runAs = step.Recipes[i].RunAs
		}

		execRes, hashVal, execErr := recipe.ExecuteRecipeScriptContext(ctx, e.driver, step.Container, scriptFile, step.ConfigBaseDir, runAs, rMeta.Env, rMeta.Retries)
		if execErr != nil || execRes.ExitCode != 0 {
			if errors.Is(execErr, context.Canceled) || errors.Is(execErr, context.DeadlineExceeded) || ctx.Err() != nil {
				return &ErrorInfo{
					Code:      "INTERNAL_ERROR",
					Container: step.Container,
					Message:   "recipe execution cancelled by user interrupt",
				}
			}
			errMsg := execRes.Stderr
			if errMsg == "" {
				if execErr != nil {
					errMsg = execErr.Error()
				} else {
					errMsg = fmt.Sprintf("recipe script %q failed with exit code %d", scriptFile, execRes.ExitCode)
				}
			}
			return &ErrorInfo{
				Code:      "EXEC_FAILED",
				Container: step.Container,
				Message:   errMsg,
			}
		}

		// Update metadata hash (H2 safety write check)
		live, freshETag, getErr := e.driver.GetInstance(ctx, step.Container)
		if getErr != nil {
			return &ErrorInfo{
				Code:      "LXD_ERROR",
				Container: step.Container,
				Message:   fmt.Sprintf("fetching container %q to update recipe metadata: %v", step.Container, getErr),
			}
		}
		put := live.Writable()
		if put.Config == nil {
			put.Config = make(map[string]string)
		}
		put.Config[hashKeys[i]] = hashVal
		if putErr := e.driver.UpdateInstance(ctx, step.Container, put, freshETag); putErr != nil {
			return &ErrorInfo{
				Code:      "LXD_ERROR",
				Container: step.Container,
				Message:   fmt.Sprintf("updating recipe metadata on %q: %v", step.Container, putErr),
			}
		}
	}

	return nil
}

// Exit-code Precedence: 1 (internal) > 4 (LXD) > 5 (target) > 6 (execution) > 7 (wait)
func selectWorstExitCode(current, newCode int) int {
	if current == 1 || newCode == 1 {
		return 1
	}
	precedence := map[int]int{
		4: 5,
		5: 4,
		6: 3,
		7: 2,
		2: 1,
		3: 1,
		0: 0,
	}
	if precedence[newCode] > precedence[current] {
		return newCode
	}
	return current
}

func errorCodeToExit(code string) int {
	switch code {
	case "INTERNAL_ERROR":
		return 1
	case "USAGE_ERROR":
		return 2
	case "CONFIG_ERROR":
		return 3
	case "LXD_ERROR":
		return 4
	case "TARGET_NOT_FOUND":
		return 5
	case "EXEC_FAILED":
		return 6
	case "WAIT_TIMEOUT":
		return 7
	default:
		return 1
	}
}

func exitToErrorCode(code int) string {
	switch code {
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
