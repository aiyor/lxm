package plan

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"

	"github.com/aiyor/lxm/internal/config"
	"github.com/aiyor/lxm/internal/provider"
	"github.com/aiyor/lxm/internal/provider/common"
	"github.com/aiyor/lxm/internal/recipe"
)

// InstanceSnapshot represents a read-only snapshot of a live instance.
type InstanceSnapshot struct {
	Name            string                       `json:"name"`
	Type            string                       `json:"type"` // "container" | "virtual-machine"
	Status          string                       `json:"status"`
	StatusCode      int                          `json:"status_code"`
	Architecture    string                       `json:"architecture"`
	Config          map[string]string            `json:"config,omitempty"`
	ExpandedConfig  map[string]string            `json:"expanded_config,omitempty"`
	Devices         map[string]map[string]string `json:"devices,omitempty"`
	ExpandedDevices map[string]map[string]string `json:"expanded_devices,omitempty"`
	Profiles        []string                     `json:"profiles,omitempty"`
	Ephemeral       bool                         `json:"ephemeral"`
	ETag            string                       `json:"etag"`
	HasSnapshots    bool                         `json:"has_snapshots"`
}

// VolumeOp is a storage-volume mutation that must complete before the owning
// instance step runs (Phase 0, STORAGE-SPEC §10). Ops are idempotent.
type VolumeOp struct {
	Op          string `json:"op"` // "create" | "grow"
	Pool        string `json:"pool"`
	Name        string `json:"name"`
	ContentType string `json:"content_type"` // "filesystem" | "block"
	Size        string `json:"size,omitempty"`
}

// ImageOp is a remote-image mutation that must complete before the owning
// instance step runs (Phase -1, IMAGE-SPEC §5.4). Ops are idempotent: a fetch
// either lands the canonical local alias or is a no-op when it already exists.
type ImageOp struct {
	Op         string `json:"op"`          // "fetch"
	Remote     string `json:"remote"`      // remote name (diagnostics)
	RemoteURL  string `json:"remote_url"`  // resolved simplestreams URL
	Alias      string `json:"alias"`       // alias on the remote
	LocalAlias string `json:"local_alias"` // canonical TYPE-QUALIFIED local alias (§4.2)
	Type       string `json:"type"`        // "container" | "virtual-machine"
}

// Plan represents a complete, serializable reconciliation plan.
type Plan struct {
	Schema       string        `json:"schema"` // "lxm/plan/v1"
	Manifest     string        `json:"manifest,omitempty"`
	NetworkSteps []NetworkStep `json:"network_steps,omitempty"`
	Steps        []Step        `json:"steps"`
	Warnings     []string      `json:"warnings,omitempty"`
	Summary      PlanSummary   `json:"summary"`
}

// Step represents a reconciliation step for a single container.
type Step struct {
	Container       string                           `json:"container"`
	Action          string                           `json:"action"` // create | update | recreate | delete | start | stop | noop
	Changed         bool                             `json:"changed"`
	Diff            []FieldDiff                      `json:"diff,omitempty"`
	Wait            bool                             `json:"wait,omitempty"`
	WaitPolicy      *config.WaitConfig               `json:"wait_policy,omitempty"`
	ConfigBaseDir   string                           `json:"config_base_dir,omitempty"`
	Recipes         []RecipeStep                     `json:"recipes,omitempty"`
	Snapshot        string                           `json:"snapshot,omitempty"`
	ETag            string                           `json:"etag,omitempty"`
	RebuildFallback bool                             `json:"rebuild_fallback,omitempty"`
	PurgeSnapshots  bool                             `json:"purge_snapshots,omitempty"`
	PowerTransition string                           `json:"power_transition,omitempty"` // "start" | "stop" | "restart"
	VolumeOps       []VolumeOp                       `json:"volume_ops,omitempty"`
	ImageOps        []ImageOp                        `json:"image_ops,omitempty"`
	ManagedDisks    []config.DiskConfig              `json:"managed_disks,omitempty"`
	InstancesPost   *provider.InstanceCreateRequest  `json:"instances_post,omitempty"`
	InstancePut     *provider.InstanceUpdateRequest  `json:"instance_put,omitempty"`
	RebuildPost     *provider.InstanceRebuildRequest `json:"rebuild_post,omitempty"`
}

// MissingVolumeError reports an external (source-referenced) custom storage
// volume that does not exist at plan time. Surfaced as exit 4 (LXD_ERROR).
type MissingVolumeError struct {
	Instance string
	Disk     string
	Pool     string
	Volume   string
}

func (e *MissingVolumeError) Error() string {
	return fmt.Sprintf(`external volume "%s/%s" referenced by disk "%s" of instance "%s" does not exist`, e.Pool, e.Volume, e.Disk, e.Instance)
}

// FieldDiff records an exact field-level delta between desired and live state.
type FieldDiff struct {
	Field            string      `json:"field"`
	Old              interface{} `json:"old,omitempty"`
	New              interface{} `json:"new,omitempty"`
	RequiresRecreate bool        `json:"requires_recreate"`
}

// PlanSummary tallies planned actions across all containers.
type PlanSummary struct {
	Create   int `json:"create"`
	Update   int `json:"update"`
	Recreate int `json:"recreate"`
	Delete   int `json:"delete"`
	Start    int `json:"start"`
	Stop     int `json:"stop"`
	Noop     int `json:"noop"`
}

// RecipeStep describes a recipe script execution gate attached to a Step.
type RecipeStep struct {
	Name  string `json:"name,omitempty"`
	Path  string `json:"path"`
	RunAs string `json:"run_as"`
	Key   string `json:"key"`
}

// Reconciler computes an immutable reconciliation Plan from desired manifest and live state.
type Reconciler interface {
	// Compute reconciles one manifest. imageAliases is the live local-alias
	// inventory (a set of alias NAMES from ImageService.GetImageAliases) and
	// imageRemotes the effective remote registry (builtins ∪ declared union,
	// config.EffectiveImageRemotes); both are passed in so Compute stays a
	// pure, offline function. Either may be nil/empty for tests and for
	// non-image flows.
	Compute(manifest *config.Config, live map[string]*InstanceSnapshot, volumes map[string]map[string]*provider.StorageVolume, imageAliases map[string]bool, imageRemotes map[string]string, hasRebuildExt bool) (*Plan, error)
}

type defaultReconciler struct{}

// NewReconciler returns a new default Reconciler.
func NewReconciler() Reconciler {
	return &defaultReconciler{}
}

func (r *defaultReconciler) Compute(manifest *config.Config, live map[string]*InstanceSnapshot, volumes map[string]map[string]*provider.StorageVolume, imageAliases map[string]bool, imageRemotes map[string]string, hasRebuildExt bool) (*Plan, error) {
	if manifest == nil {
		return nil, fmt.Errorf("manifest cannot be nil")
	}

	plan := &Plan{
		Schema: "lxm/plan/v1",
		Steps:  []Step{},
	}

	targetName := manifest.Name
	liveInst := live[targetName]

	step := Step{
		Container:     targetName,
		WaitPolicy:    &manifest.WaitPolicy,
		ConfigBaseDir: manifest.ConfigBaseDir,
	}
	if manifest.Status != "absent" {
		for _, d := range manifest.Disks {
			if d.Status != "absent" && isManagedDisk(d) {
				step.ManagedDisks = append(step.ManagedDisks, d)
			}
		}
	}

	// 1. Reconcile absent status
	if manifest.Status == "absent" {
		if liveInst == nil {
			step.Action = "noop"
			step.Changed = false
		} else {
			step.Action = "delete"
			step.Changed = true
			step.ETag = liveInst.ETag
			step.Diff = append(step.Diff, FieldDiff{
				Field: "status",
				Old:   "present",
				New:   "absent",
			})
		}
		plan.Steps = append(plan.Steps, step)
		plan.Summary = computeSummary(plan.Steps)
		return plan, nil
	}

	// 2. Reconcile present status (create)
	if liveInst == nil {
		step.Action = "create"
		step.Changed = true
		step.Wait = manifest.WaitPolicy.Required
		step.Diff = []FieldDiff{
			{Field: "status", Old: "absent", New: "present"},
			{Field: "image", Old: nil, New: manifest.Image},
		}

		desiredState := "running"
		if manifest.State != "" {
			desiredState = manifest.State
		}
		if desiredState == "running" {
			step.PowerTransition = "start"
		} else {
			step.PowerTransition = "stop"
		}

		postPayload, err := buildInstancesPost(manifest)
		if err != nil {
			return nil, fmt.Errorf("building create payload: %w", err)
		}
		step.InstancesPost = postPayload

		// Remote-image fetch decision (§5.3): an uncached remote:alias
		// reference plans a Phase -1 fetch before this create runs. Unknown
		// remotes and fetch-disabled misses are plan-time config errors.
		imageOps, err := r.buildImageOps(manifest, imageAliases, imageRemotes)
		if err != nil {
			return nil, err
		}
		step.ImageOps = imageOps

		// Storage volume pre-provisioning (managed disks) and external-volume
		// existence probe (STORAGE-SPEC §5.3/§7.6). Volumes arrive as a
		// dedicated parameter so the probe works even when no live instances
		// exist to carry them.
		step.VolumeOps = buildCreateVolumeOps(manifest)
		if err := checkExternalVolumes(manifest, volumes); err != nil {
			return nil, err
		}

		// Attach recipe steps for new container
		step.Recipes = buildRecipeSteps(manifest)

		// Plumb raw_qemu warning
		if manifest.Type == "virtual-machine" && manifest.VM != nil && manifest.VM.RawQEMU != "" {
			plan.Warnings = append(plan.Warnings, fmt.Sprintf("instance %q specifies raw.qemu hypervisor arguments: %q", manifest.Name, manifest.VM.RawQEMU))
		}

		plan.Steps = append(plan.Steps, step)
		plan.Summary = computeSummary(plan.Steps)
		return plan, nil
	}

	// 3. Instance exists — compare live state against desired state
	step.ETag = liveInst.ETag
	diffs, requiresRecreate, volumeOps, err := computeDiffs(manifest, liveInst, volumes)
	if err != nil {
		return nil, err
	}
	step.VolumeOps = volumeOps

	// Check image change (RequiresRecreate)
	if requiresRecreate {
		step.Action = "recreate"
		step.Changed = true
		step.Diff = diffs
		step.Wait = manifest.WaitPolicy.Required
		step.PurgeSnapshots = liveInst.HasSnapshots
		if isTypeChange(diffs) || !hasRebuildExt {
			step.RebuildFallback = true
		}

		desiredState := "running"
		if manifest.State != "" {
			desiredState = manifest.State
		}
		if desiredState == "running" {
			step.PowerTransition = "start"
		} else {
			step.PowerTransition = "stop"
		}

		step.RebuildPost = &provider.InstanceRebuildRequest{
			Source: resolvedInstanceSource(manifest.Image, manifest.Type),
		}
		postPayload, err := buildInstancesPost(manifest)
		if err != nil {
			return nil, fmt.Errorf("building recreate payload: %w", err)
		}
		step.InstancesPost = postPayload

		// Remote-image fetch decision (§5.3): a recreate rebuilds from the
		// canonical local alias, so an uncached remote:alias reference for
		// THIS instance type plans a Phase -1 fetch first.
		imageOps, err := r.buildImageOps(manifest, imageAliases, imageRemotes)
		if err != nil {
			return nil, err
		}
		step.ImageOps = imageOps

		// Managed volumes persist across an instance recreate (never deleted);
		// idempotent create keeps them provisioned. External volumes are probed
		// so a recreate does not fail late at apply.
		step.VolumeOps = buildCreateVolumeOps(manifest)
		if err := checkExternalVolumes(manifest, volumes); err != nil {
			return nil, err
		}

		// Recipes must be re-run on recreate
		step.Recipes = buildRecipeSteps(manifest)

		// Plumb raw_qemu warning
		if manifest.Type == "virtual-machine" && manifest.VM != nil && manifest.VM.RawQEMU != "" {
			plan.Warnings = append(plan.Warnings, fmt.Sprintf("instance %q specifies raw.qemu hypervisor arguments: %q", manifest.Name, manifest.VM.RawQEMU))
		}

		plan.Steps = append(plan.Steps, step)
		plan.Summary = computeSummary(plan.Steps)
		return plan, nil
	}

	// 4. Power State Reconciliation (F2 & P1-4)
	desiredState := "running"
	if manifest.State != "" {
		desiredState = manifest.State
	}

	liveState := "stopped"
	if liveInst.Status == "Running" || liveInst.StatusCode == 103 {
		liveState = "running"
	}

	powerStateChanged := liveState != desiredState

	// 5. InPlace Configuration Update
	switch {
	case len(diffs) > 0:
		step.Action = "update"
		step.Changed = true
		step.Diff = diffs
		step.Wait = manifest.WaitPolicy.Required
		putPayload, err := buildInstancePut(manifest, liveInst)
		if err != nil {
			return nil, fmt.Errorf("building update payload: %w", err)
		}
		step.InstancePut = putPayload

		if powerStateChanged {
			if desiredState == "running" {
				step.PowerTransition = "start"
			} else {
				step.PowerTransition = "stop"
			}
			step.Diff = append(step.Diff, FieldDiff{
				Field: "state", Old: liveState, New: desiredState,
			})
		} else if liveState == "running" && (hasVMConfigDiff(diffs) || hasDiskRestartDiff(diffs)) {
			step.PowerTransition = "restart"
		}
	case powerStateChanged:
		step.Changed = true
		if desiredState == "running" {
			step.Action = "start"
		} else {
			step.Action = "stop"
		}
		step.Diff = []FieldDiff{
			{Field: "state", Old: liveState, New: desiredState},
		}
	default:
		step.Action = "noop"
		step.Changed = false
	}

	// Recipe updates for existing container
	step.Recipes = buildRecipeSteps(manifest)

	// Plumb raw_qemu warning
	if manifest.Type == "virtual-machine" && manifest.VM != nil && manifest.VM.RawQEMU != "" {
		plan.Warnings = append(plan.Warnings, fmt.Sprintf("instance %q specifies raw.qemu hypervisor arguments: %q", manifest.Name, manifest.VM.RawQEMU))
	}

	// Warn if a managed disk marked status: absent references a live volume that lacks the managed marker
	for _, d := range manifest.Disks {
		if d.Status == "absent" && isManagedDisk(d) {
			vol := lookupVolume(volumes, d.Pool, d.Source)
			if vol != nil && (vol.Config == nil || vol.Config["user.lxm.managed"] != "true") {
				plan.Warnings = append(plan.Warnings, fmt.Sprintf("disk %q of instance %q: storage volume %s/%s lacks user.lxm.managed marker; detaching from instance without deleting storage volume", d.Name, manifest.Name, d.Pool, d.Source))
			}
		}
	}

	plan.Steps = append(plan.Steps, step)
	plan.Summary = computeSummary(plan.Steps)
	return plan, nil
}

func buildInstancesPost(manifest *config.Config) (*provider.InstanceCreateRequest, error) {
	instType := provider.InstanceTypeContainer
	if manifest.Type == "virtual-machine" {
		instType = provider.InstanceTypeVM
	}

	post := &provider.InstanceCreateRequest{
		Name:    manifest.Name,
		Type:    instType,
		Source:  resolvedInstanceSource(manifest.Image, manifest.Type),
		Config:  make(map[string]string),
		Devices: make(map[string]map[string]string),
	}

	// 1. Hardware Limits
	if manifest.Limits != nil {
		if manifest.Limits.CPU != "" {
			post.Config["limits.cpu"] = string(manifest.Limits.CPU)
		}
		if manifest.Limits.Memory != "" {
			post.Config["limits.memory"] = manifest.Limits.Memory
		}
		if manifest.Limits.Disk != "" {
			post.Devices["root"] = map[string]string{
				"type": "disk",
				"path": "/",
				"pool": "default",
				"size": manifest.Limits.Disk,
			}
		}
	}

	// 2. VM Configs
	if manifest.Type == "virtual-machine" && manifest.VM != nil {
		if manifest.VM.BootMode != "" {
			post.Config["boot.mode"] = manifest.VM.BootMode
		}
		if manifest.VM.Hugepages {
			post.Config["limits.memory.hugepages"] = "true"
		}
		if manifest.VM.RawQEMU != "" {
			post.Config["raw.qemu"] = manifest.VM.RawQEMU
		}
	}

	// 3. User, Groups & Cloud-Init
	post.Config["user.lxm.user"] = manifest.User
	post.Config["user.lxm.managed"] = "true"
	// The exact manifest reference is recorded so live-instance image matching
	// works for every remote (custom remotes included) without deriving the OS
	// from the reference string (§4.5).
	post.Config["user.lxm.image"] = manifest.Image
	if len(manifest.Groups) > 0 {
		grps := make([]string, len(manifest.Groups))
		copy(grps, manifest.Groups)
		sort.Strings(grps)
		post.Config["user.lxm.groups"] = strings.Join(grps, ",")
	}
	cloudInit, err := manifest.ResolveCloudInit(manifest.ConfigBaseDir)
	if err != nil {
		return nil, fmt.Errorf("resolving cloud-init: %w", err)
	}
	if cloudInit != "" {
		post.Config["user.user-data"] = cloudInit
	}
	if manifest.NetworkConfig != "" {
		post.Config["user.network-config"] = manifest.NetworkConfig
	}

	// 4. Mounts with Shift
	for i, m := range manifest.Mounts {
		devName := fmt.Sprintf("mount%d", i)
		props := map[string]string{
			"type":   "disk",
			"source": m.Source,
			"path":   m.Path,
		}
		if m.Shift == nil || *m.Shift {
			props["shift"] = "true"
		} else {
			props["shift"] = "false"
		}
		if m.Readonly {
			props["readonly"] = "true"
		}
		if m.Recursive {
			props["recursive"] = "true"
		}
		post.Devices[devName] = props
	}

	// 5. Networks
	for _, n := range manifest.Networks {
		devName := n.Name
		if devName == "" {
			devName = "eth0"
		}
		parent := n.Parent
		if parent == "" {
			parent = "lxdbr0"
		}
		props := map[string]string{
			"type":    "nic",
			"name":    devName,
			"parent":  parent,
			"nictype": "bridged",
		}
		if n.IPv4 != "" {
			props["ipv4.address"] = n.IPv4
		}
		post.Devices[devName] = props
	}

	// 6. Disks (data disks carry source, never size — STORAGE-SPEC §3)
	for _, d := range manifest.Disks {
		if d.Status == "absent" {
			continue
		}
		if d.Attach != nil && !*d.Attach {
			continue
		}
		post.Devices["disk-"+d.Name] = buildDiskDevice(d)
	}

	return post, nil
}

// resolvedInstanceSource maps a manifest image reference to the local
// image identity used in create/rebuild payloads (IMAGE-SPEC §5.1). A hex
// fingerprint goes to Source.Fingerprint (resolves verbatim); a bare
// alias and a remote:alias go to Source.Alias, the latter as the canonical
// TYPE-QUALIFIED local alias (config.ImageLocalRef).
func resolvedInstanceSource(image, instanceType string) provider.InstanceSource {
	src := provider.InstanceSource{Type: "image"}
	if isHexFingerprint(image) {
		src.Fingerprint = image
	} else {
		src.Alias = config.ImageLocalRef(image, instanceType)
	}
	return src
}

// buildImageOps returns the remote-image fetch ops required before an instance
// step runs (create/recreate only). Local references never fetch; a cached
// canonical local alias never fetches; an uncached remote:alias plans one
// Phase -1 fetch carrying the already-resolved URL (§5.3). Unknown remotes and
// fetch-disabled misses are config errors (exit 3).
func (r *defaultReconciler) buildImageOps(manifest *config.Config, imageAliases map[string]bool, imageRemotes map[string]string) ([]ImageOp, error) {
	if manifest == nil || manifest.Status == "absent" {
		return nil, nil
	}
	remote, alias, isRemote := config.SplitImageRef(manifest.Image)
	if !isRemote {
		return nil, nil // fingerprint / bare alias: local only, never fetched
	}
	url, ok := imageRemotes[remote]
	if !ok {
		//nolint:staticcheck // ST1005: the trailing colon is part of the locked spec message (§4.1).
		return nil, fmt.Errorf(`unknown image remote %q (referenced by image %q of instance %q); declare it under image_remotes:`, remote, manifest.Image, manifest.Name)
	}
	localAlias := config.ImageLocalRef(manifest.Image, manifest.Type)
	if imageAliases[localAlias] {
		return nil, nil // cached: no fetch
	}
	if !imageFetchEnabled() {
		return nil, fmt.Errorf(`image fetch is disabled (LXM_IMAGE_FETCH=0) but image %q of instance %q is not cached locally`, manifest.Image, manifest.Name)
	}
	return []ImageOp{{
		Op:         "fetch",
		Remote:     remote,
		RemoteURL:  url,
		Alias:      alias,
		LocalAlias: localAlias,
		Type:       manifest.Type,
	}}, nil
}

// imageFetchEnabled reports whether remote image fetch is enabled. The
// LXM_IMAGE_FETCH environment variable (default "1") disables it when set to
// "0" or "false" (§7.5).
func imageFetchEnabled() bool {
	v := os.Getenv("LXM_IMAGE_FETCH")
	if v == "" {
		return true
	}
	return v != "0" && !strings.EqualFold(v, "false")
}

func buildInstancePut(manifest *config.Config, live *InstanceSnapshot) (*provider.InstanceUpdateRequest, error) {
	put := &provider.InstanceUpdateRequest{
		Config:   make(map[string]string),
		Devices:  make(map[string]map[string]string),
		Profiles: live.Profiles,
	}

	// 1. Copy live configuration base
	for k, v := range live.Config {
		put.Config[k] = v
	}
	put.Config["user.lxm.managed"] = "true"
	// The manifest reference is re-recorded on every update so the imageMatches
	// fast path (§4.5) stays authoritative and legacy instances created before
	// user.lxm.image existed are backfilled on their next update.
	put.Config["user.lxm.image"] = manifest.Image

	// 2. Recompute user, groups, cloud-init
	if manifest.User != "" {
		put.Config["user.lxm.user"] = manifest.User
	}
	if len(manifest.Groups) > 0 {
		grps := make([]string, len(manifest.Groups))
		copy(grps, manifest.Groups)
		sort.Strings(grps)
		put.Config["user.lxm.groups"] = strings.Join(grps, ",")
	} else {
		delete(put.Config, "user.lxm.groups")
	}
	cloudInit, err := manifest.ResolveCloudInit(manifest.ConfigBaseDir)
	if err != nil {
		return nil, fmt.Errorf("resolving cloud-init: %w", err)
	}
	if cloudInit != "" {
		put.Config["user.user-data"] = cloudInit
	}
	if manifest.NetworkConfig != "" {
		put.Config["user.network-config"] = manifest.NetworkConfig
	} else {
		delete(put.Config, "user.network-config")
	}

	// 3. Apply / Clear Hardware Limits
	if manifest.Limits != nil {
		if manifest.Limits.CPU != "" {
			put.Config["limits.cpu"] = string(manifest.Limits.CPU)
		} else {
			delete(put.Config, "limits.cpu")
		}
		if manifest.Limits.Memory != "" {
			put.Config["limits.memory"] = manifest.Limits.Memory
		} else {
			delete(put.Config, "limits.memory")
		}
		if manifest.Limits.Disk != "" {
			rootDev := map[string]string{"type": "disk", "path": "/", "pool": "default"}
			if live.ExpandedDevices != nil && live.ExpandedDevices["root"] != nil {
				for k, v := range live.ExpandedDevices["root"] {
					rootDev[k] = v
				}
			}
			rootDev["size"] = manifest.Limits.Disk
			put.Devices["root"] = rootDev
		}
	} else {
		delete(put.Config, "limits.cpu")
		delete(put.Config, "limits.memory")
	}

	// 4. Apply / Clear VM Configs
	if manifest.Type == "virtual-machine" && manifest.VM != nil {
		if manifest.VM.BootMode != "" {
			put.Config["boot.mode"] = manifest.VM.BootMode
		}
		if manifest.VM.Hugepages {
			put.Config["limits.memory.hugepages"] = "true"
		} else {
			delete(put.Config, "limits.memory.hugepages")
		}
		if manifest.VM.RawQEMU != "" {
			put.Config["raw.qemu"] = manifest.VM.RawQEMU
		} else {
			delete(put.Config, "raw.qemu")
		}
	}

	// 5. Copy live non-managed devices (preserving root and custom non-lxm
	// devices). NIC devices are rebuilt from the manifest (step 7). Disk
	// devices are partitioned by key prefix (STORAGE-SPEC §5.1): mount*
	// (rebuilt as mounts), disk-* (rebuilt as data disks), and foreign
	// hand-added disk devices (preserved verbatim).
	for dev, props := range live.Devices {
		if dev == "root" {
			if manifest.Limits == nil || manifest.Limits.Disk == "" {
				devCopy := make(map[string]string)
				for k, v := range props {
					devCopy[k] = v
				}
				put.Devices[dev] = devCopy
			}
			continue
		}
		if props["type"] != "disk" && props["type"] != "nic" {
			devCopy := make(map[string]string)
			for k, v := range props {
				devCopy[k] = v
			}
			put.Devices[dev] = devCopy
			continue
		}
		// Disk devices: preserve only foreign ones (no mount* / disk-* prefix).
		if props["type"] == "disk" && !strings.HasPrefix(dev, "mount") && !strings.HasPrefix(dev, "disk-") {
			devCopy := make(map[string]string)
			for k, v := range props {
				devCopy[k] = v
			}
			put.Devices[dev] = devCopy
		}
	}

	// 6. Rebuild Mounts with Shift
	for i, m := range manifest.Mounts {
		devName := fmt.Sprintf("mount%d", i)
		props := map[string]string{
			"type":   "disk",
			"source": m.Source,
			"path":   m.Path,
		}
		if m.Shift == nil || *m.Shift {
			props["shift"] = "true"
		} else {
			props["shift"] = "false"
		}
		if m.Readonly {
			props["readonly"] = "true"
		}
		if m.Recursive {
			props["recursive"] = "true"
		}
		put.Devices[devName] = props
	}

	// 7. Rebuild Networks
	for _, n := range manifest.Networks {
		devName := n.Name
		if devName == "" {
			devName = "eth0"
		}
		parent := n.Parent
		if parent == "" {
			parent = "lxdbr0"
		}
		props := map[string]string{
			"type":    "nic",
			"name":    devName,
			"parent":  parent,
			"nictype": "bridged",
		}
		if n.IPv4 != "" {
			props["ipv4.address"] = n.IPv4
		}
		put.Devices[devName] = props
	}

	// 8. Rebuild Disks (data disks carry source, never size)
	for _, d := range manifest.Disks {
		if d.Status == "absent" {
			continue
		}
		if d.Attach != nil && !*d.Attach {
			continue
		}
		put.Devices["disk-"+d.Name] = buildDiskDevice(d)
	}

	return put, nil
}

// buildDiskDevice renders a DiskConfig as a LXD device map. It always carries
// `source` and never `size` (LXD forbids `size` on non-root device maps);
// filesystem mode adds `path`, block mode adds `io.bus` (STORAGE-SPEC §3).
func buildDiskDevice(d config.DiskConfig) map[string]string {
	props := map[string]string{
		"type":   "disk",
		"pool":   d.Pool,
		"source": d.Source,
	}
	if d.Path != "" {
		props["path"] = d.Path
	} else if d.Bus != "" && d.Bus != "virtio-scsi" {
		// Only non-default io.bus values are emitted. virtio-scsi is LXD's own
		// block-disk default, so omitting it keeps the device valid on servers
		// without the disk_io_bus extension — the default needs no gate
		// (STORAGE-SPEC §3).
		props["io.bus"] = d.Bus
	}
	if d.Readonly {
		props["readonly"] = "true"
	}
	return props
}

func isTypeChange(diffs []FieldDiff) bool {
	for _, d := range diffs {
		if d.Field == "type" {
			return true
		}
	}
	return false
}

func hasVMConfigDiff(diffs []FieldDiff) bool {
	for _, d := range diffs {
		if d.Field == "boot.mode" || d.Field == "limits.memory.hugepages" || d.Field == "raw.qemu" {
			return true
		}
	}
	return false
}

func isDiskShrink(oldSizeStr, newSizeStr string) bool {
	if oldSizeStr == "" || newSizeStr == "" {
		return false
	}
	oldBytes, err1 := common.ParseByteSizeString(oldSizeStr)
	newBytes, err2 := common.ParseByteSizeString(newSizeStr)
	if err1 != nil || err2 != nil {
		return true
	}
	return newBytes < oldBytes
}

func sortMounts(mounts []config.Mount) {
	sort.Slice(mounts, func(i, j int) bool {
		if mounts[i].Path != mounts[j].Path {
			return mounts[i].Path < mounts[j].Path
		}
		return mounts[i].Source < mounts[j].Source
	})
}

func areMountsEqual(manifestMounts, liveMounts []config.Mount) bool {
	if len(manifestMounts) != len(liveMounts) {
		return false
	}
	m1 := make([]config.Mount, len(manifestMounts))
	copy(m1, manifestMounts)
	sortMounts(m1)

	m2 := make([]config.Mount, len(liveMounts))
	copy(m2, liveMounts)
	sortMounts(m2)

	for i := range m1 {
		s1 := (m1[i].Shift == nil || *m1[i].Shift)
		s2 := (m2[i].Shift == nil || *m2[i].Shift)
		if m1[i].Source != m2[i].Source || m1[i].Path != m2[i].Path || m1[i].Readonly != m2[i].Readonly || m1[i].Recursive != m2[i].Recursive || s1 != s2 {
			return false
		}
	}
	return true
}

func computeDiffs(manifest *config.Config, live *InstanceSnapshot, volumes map[string]map[string]*provider.StorageVolume) ([]FieldDiff, bool, []VolumeOp, error) {
	var diffs []FieldDiff
	requiresRecreate := false
	var volumeOps []VolumeOp

	// 1. Instance Type Invariant
	if live.Type != "" && manifest.Type != "" && live.Type != manifest.Type {
		diffs = append(diffs, FieldDiff{
			Field:            "type",
			Old:              live.Type,
			New:              manifest.Type,
			RequiresRecreate: true,
		})
		requiresRecreate = true
	}

	liveImage := getLiveImage(live)
	if manifest.Image != "" && liveImage != "" && !imageMatches(manifest.Image, liveImage, live.Config) {
		diffs = append(diffs, FieldDiff{
			Field:            "image",
			Old:              liveImage,
			New:              manifest.Image,
			RequiresRecreate: true,
		})
		requiresRecreate = true
	}

	liveUser := live.Config["user.lxm.user"]
	if manifest.User != "" && manifest.User != liveUser {
		diffs = append(diffs, FieldDiff{
			Field: "user",
			Old:   liveUser,
			New:   manifest.User,
		})
	}

	liveGroups := live.Config["user.lxm.groups"]
	desiredGroups := ""
	if len(manifest.Groups) > 0 {
		sort.Strings(manifest.Groups)
		desiredGroups = strings.Join(manifest.Groups, ",")
	}
	if desiredGroups != liveGroups {
		diffs = append(diffs, FieldDiff{
			Field: "groups",
			Old:   liveGroups,
			New:   desiredGroups,
		})
	}

	liveNetworkConfig := live.Config["user.network-config"]
	if manifest.NetworkConfig != "" && manifest.NetworkConfig != liveNetworkConfig {
		diffs = append(diffs, FieldDiff{
			Field: "network-config",
			Old:   liveNetworkConfig,
			New:   manifest.NetworkConfig,
		})
	}

	// 2. Hardware Limits Diffs (diffed against local Config to stay idempotent with profile limits)
	desiredCPU := ""
	desiredMem := ""
	if manifest.Limits != nil {
		desiredCPU = string(manifest.Limits.CPU)
		desiredMem = manifest.Limits.Memory
	}

	liveCPU := live.Config["limits.cpu"]
	if desiredCPU != liveCPU {
		diffs = append(diffs, FieldDiff{Field: "limits.cpu", Old: liveCPU, New: desiredCPU})
	}

	liveMem := live.Config["limits.memory"]
	if desiredMem != liveMem {
		diffs = append(diffs, FieldDiff{Field: "limits.memory", Old: liveMem, New: desiredMem})
	}

	// Disk limit is only diffed when explicitly managed in manifest
	if manifest.Limits != nil && manifest.Limits.Disk != "" {
		liveDisk := ""
		if live.ExpandedDevices != nil && live.ExpandedDevices["root"] != nil {
			liveDisk = live.ExpandedDevices["root"]["size"]
		}
		if manifest.Limits.Disk != liveDisk {
			if isDiskShrink(liveDisk, manifest.Limits.Disk) {
				diffs = append(diffs, FieldDiff{Field: "limits.disk", Old: liveDisk, New: manifest.Limits.Disk, RequiresRecreate: true})
				requiresRecreate = true
			} else {
				diffs = append(diffs, FieldDiff{Field: "limits.disk", Old: liveDisk, New: manifest.Limits.Disk})
			}
		}
	}

	// 3. VM Config Diffs (boot.mode, hugepages, raw_qemu)
	if manifest.Type == "virtual-machine" {
		desiredBoot := "uefi-secureboot"
		if manifest.VM != nil && manifest.VM.BootMode != "" {
			desiredBoot = manifest.VM.BootMode
		}
		liveBoot := live.Config["boot.mode"]
		if liveBoot == "" {
			liveBoot = "uefi-secureboot"
		}
		if desiredBoot != liveBoot {
			diffs = append(diffs, FieldDiff{Field: "boot.mode", Old: liveBoot, New: desiredBoot})
		}

		desiredHugepages := ""
		if manifest.VM != nil && manifest.VM.Hugepages {
			desiredHugepages = "true"
		}
		liveHugepages := live.Config["limits.memory.hugepages"]
		if desiredHugepages != liveHugepages {
			diffs = append(diffs, FieldDiff{Field: "limits.memory.hugepages", Old: liveHugepages, New: desiredHugepages})
		}

		desiredRawQEMU := ""
		if manifest.VM != nil {
			desiredRawQEMU = manifest.VM.RawQEMU
		}
		liveRawQEMU := live.Config["raw.qemu"]
		if desiredRawQEMU != liveRawQEMU {
			diffs = append(diffs, FieldDiff{Field: "raw.qemu", Old: liveRawQEMU, New: desiredRawQEMU})
		}
	}

	// 4. Mounts & Shift Normalization (order-insensitive)
	liveMounts := getLiveMounts(live)
	if !areMountsEqual(manifest.Mounts, liveMounts) {
		diffs = append(diffs, FieldDiff{
			Field: "mounts",
			Old:   liveMounts,
			New:   manifest.Mounts,
		})
	}

	liveNetworks := getLiveNetworks(live)
	if !reflect.DeepEqual(manifest.Networks, liveNetworks) {
		diffs = append(diffs, FieldDiff{
			Field: "networks",
			Old:   liveNetworks,
			New:   manifest.Networks,
		})
	}

	// 5. Disks (data disks, STORAGE-SPEC §5.2)
	diskDiffs, diskOps, err := diffDisks(manifest, live, volumes)
	if err != nil {
		return nil, false, nil, err
	}
	diffs = append(diffs, diskDiffs...)
	volumeOps = append(volumeOps, diskOps...)

	return diffs, requiresRecreate, volumeOps, nil
}

// diffDisks compares resolved manifest disks against live data-disk devices
// order-insensitively and returns the field diffs plus any storage-volume
// operations required (create/grow). Errors are config-level (shrink, mode
// switch) or state-level (missing external volume).
func diffDisks(manifest *config.Config, live *InstanceSnapshot, volumes map[string]map[string]*provider.StorageVolume) ([]FieldDiff, []VolumeOp, error) {
	var diffs []FieldDiff
	var ops []VolumeOp

	// External volumes referenced by the manifest must exist regardless of
	// whether the disk is being added or updated (STORAGE-SPEC §7.6).
	if err := checkExternalVolumes(manifest, volumes); err != nil {
		return nil, nil, err
	}

	liveByName := make(map[string]config.DiskConfig)
	for _, ld := range getLiveDisks(live, volumes) {
		liveByName[ld.Name] = ld
	}

	for _, md := range manifest.Disks {
		ld, exists := liveByName[md.Name]

		if md.Status == "absent" {
			if exists {
				diffs = append(diffs, FieldDiff{Field: "disks[" + md.Name + "]", Old: ld, New: nil})
			}
			vol := lookupVolume(volumes, md.Pool, md.Source)
			// Ownership invariant (ARCHITECTURE §4.5): only delete volumes carrying
			// user.lxm.managed: "true". External volumes (even if matching <instance>-<disk>)
			// lack the marker and must NEVER be deleted.
			if vol != nil && vol.Config != nil && vol.Config["user.lxm.managed"] == "true" {
				ops = append(ops, VolumeOp{
					Op:          "delete",
					Pool:        md.Pool,
					Name:        md.Source,
					ContentType: diskContentType(md),
				})
			}
			continue
		}

		if md.Attach != nil && !*md.Attach {
			if exists {
				diffs = append(diffs, FieldDiff{Field: fmt.Sprintf("disks[%s].attach", md.Name), Old: true, New: false})
			} else if isManagedDisk(md) {
				if vol := lookupVolume(volumes, md.Pool, md.Source); vol == nil {
					ops = append(ops, managedDiskCreateOps(manifest, md)...)
				}
			}
			continue
		}

		if !exists {
			// Disk added: hotplug (update) or part of create. Managed disks
			// need their volume provisioned first (Phase 0).
			diffs = append(diffs, FieldDiff{Field: "disks[" + md.Name + "]", Old: nil, New: md})
			ops = append(ops, managedDiskCreateOps(manifest, md)...)
			continue
		}

		if md.Pool != ld.Pool {
			diffs = append(diffs, FieldDiff{Field: fmt.Sprintf("disks[%s].pool", md.Name), Old: ld.Pool, New: md.Pool})
			// Managed: provision a fresh volume in the new pool (old volume
			// orphaned, never deleted). External: device re-points only.
			ops = append(ops, managedDiskCreateOps(manifest, md)...)
		}

		if md.Path != ld.Path {
			if md.Path == "" || ld.Path == "" {
				// filesystem ⇄ block switch: volume name + content type are
				// fixed per pool — re-provisioning needs manual disposal.
				return nil, nil, fmt.Errorf(`disk %q of instance %q mode switch (filesystem ⇄ block) cannot be reconciled automatically; remove the disk, re-provision the volume manually, and re-add it with the new mode`, md.Name, manifest.Name)
			}
			diffs = append(diffs, FieldDiff{Field: fmt.Sprintf("disks[%s].path", md.Name), Old: ld.Path, New: md.Path})
		}

		if md.Source != ld.Source {
			diffs = append(diffs, FieldDiff{Field: fmt.Sprintf("disks[%s].source", md.Name), Old: ld.Source, New: md.Source})
		}

		if md.Readonly != ld.Readonly {
			diffs = append(diffs, FieldDiff{Field: fmt.Sprintf("disks[%s].readonly", md.Name), Old: ld.Readonly, New: md.Readonly})
		}

		if md.Bus != ld.Bus {
			diffs = append(diffs, FieldDiff{Field: fmt.Sprintf("disks[%s].bus", md.Name), Old: ld.Bus, New: md.Bus})
		}

		// size is compared only for managed disks (external size is unmanaged).
		// Compare parsed bytes so a reworded-but-equal size (10GiB vs
		// 10737418240) does not produce a perpetual diff (LXD preserves the
		// size string verbatim).
		if isManagedDisk(md) && diskSizeDiffers(ld.Size, md.Size) {
			if isDiskShrink(ld.Size, md.Size) {
				return nil, nil, fmt.Errorf(`disk %q of instance %q cannot be shrunk (%s → %s); storage volumes cannot be shrunk in place — delete and recreate the volume to provision a smaller disk`, md.Name, manifest.Name, ld.Size, md.Size)
			}
			diffs = append(diffs, FieldDiff{Field: fmt.Sprintf("disks[%s].size", md.Name), Old: ld.Size, New: md.Size})
			ops = append(ops, VolumeOp{Op: "grow", Pool: md.Pool, Name: md.Source, ContentType: diskContentType(md), Size: md.Size})
		}
	}

	// Disks removed from the manifest: detach only (never delete the volume).
	manifestByName := make(map[string]config.DiskConfig)
	for _, md := range manifest.Disks {
		manifestByName[md.Name] = md
	}
	for _, ld := range getLiveDisks(live, volumes) {
		if _, exists := manifestByName[ld.Name]; !exists {
			diffs = append(diffs, FieldDiff{Field: "disks[" + ld.Name + "]", Old: ld, New: nil})
		}
	}

	return diffs, ops, nil
}

// isManagedDisk reports whether a disk is managed by lxm (provisions its own
// volume) rather than being an external reference. Managed disks have an
// explicit size specification or the managed flag set during normalization.
func isManagedDisk(d config.DiskConfig) bool {
	return d.Size != "" || d.Managed
}

// buildCreateVolumeOps returns the idempotent create ops for all managed disks
// in a create/recreate plan (external disks need no provisioning).
func buildCreateVolumeOps(manifest *config.Config) []VolumeOp {
	var ops []VolumeOp
	for _, d := range manifest.Disks {
		if d.Status == "absent" {
			continue
		}
		ops = append(ops, managedDiskCreateOps(manifest, d)...)
	}
	return ops
}

// managedDiskCreateOps returns a create op for a managed disk (size set), or
// none for an external disk. A create op is idempotent: create if absent, grow
// if smaller.
func managedDiskCreateOps(manifest *config.Config, d config.DiskConfig) []VolumeOp {
	if !isManagedDisk(d) {
		return nil // external: volume is not provisioned by lxm
	}
	return []VolumeOp{{
		Op:          "create",
		Pool:        d.Pool,
		Name:        d.Source,
		ContentType: diskContentType(d),
		Size:        d.Size,
	}}
}

// diskContentType returns the LXD volume content type for a disk's mode.
func diskContentType(d config.DiskConfig) string {
	if d.Path != "" {
		return "filesystem"
	}
	return "block"
}

// checkExternalVolumes verifies every external (source-referenced) disk has a
// live custom volume. A missing volume is a plan-time error surfaced as exit 4
// (STORAGE-SPEC §7.6).
func checkExternalVolumes(manifest *config.Config, volumes map[string]map[string]*provider.StorageVolume) error {
	if manifest == nil {
		return nil
	}
	for _, d := range manifest.Disks {
		if d.Status == "absent" {
			continue
		}
		if isManagedDisk(d) {
			continue // managed: volume is provisioned by lxm
		}
		var found bool
		if volumes != nil {
			if poolVols, ok := volumes[d.Pool]; ok {
				if _, ok := poolVols[d.Source]; ok {
					found = true
				}
			}
		}
		if !found {
			return &MissingVolumeError{Instance: manifest.Name, Disk: d.Name, Pool: d.Pool, Volume: d.Source}
		}
	}
	return nil
}

// hasDiskRestartDiff reports whether a disk field change requires a running VM
// restart: path (filesystem remount), source (external volume re-point), bus
// (block re-plug), or pool (device re-point to a volume in another pool).
// Adding or re-attaching a disk is hotplugged live without restart.
func hasDiskRestartDiff(diffs []FieldDiff) bool {
	for _, d := range diffs {
		if !strings.HasPrefix(d.Field, "disks[") {
			continue
		}
		if strings.HasSuffix(d.Field, ".path") || strings.HasSuffix(d.Field, ".source") || strings.HasSuffix(d.Field, ".bus") || strings.HasSuffix(d.Field, ".pool") {
			return true
		}
	}
	return false
}

// diskSizeDiffers reports whether two byte-size strings denote different sizes,
// comparing parsed bytes so reworded-equal sizes (10GiB vs 10737418240) compare
// equal. Falls back to raw-string inequality when either value is empty or
// unparsable.
func diskSizeDiffers(a, b string) bool {
	if a == "" || b == "" {
		return a != b
	}
	aBytes, errA := common.ParseByteSizeString(a)
	bBytes, errB := common.ParseByteSizeString(b)
	if errA != nil || errB != nil {
		return a != b
	}
	return aBytes != bBytes
}

func getLiveImage(live *InstanceSnapshot) string {
	if live.Config == nil {
		return ""
	}
	if img, ok := live.Config["image.os"]; ok && img != "" {
		if rel, ok2 := live.Config["image.release"]; ok2 && rel != "" {
			return img + ":" + rel
		}
		return img
	}
	return live.Config["image.description"]
}

// imageMatches reports whether the desired manifest image reference matches the
// image identity of a live instance. It first consults the exact manifest
// reference lxm recorded on the instance at create/recreate (user.lxm.image,
// §4.5): that fast path is authoritative and works for every remote — custom
// remotes included, where the OS cannot be derived from the reference string.
// The fingerprint + os:release heuristics remain as a fallback for legacy
// instances created before user.lxm.image existed.
func imageMatches(desired, live string, liveConfig map[string]string) bool {
	if desired == live {
		return true
	}
	if recorded := strings.ToLower(strings.TrimSpace(liveConfig["user.lxm.image"])); recorded != "" &&
		recorded == strings.ToLower(strings.TrimSpace(desired)) {
		return true
	}
	if live == "" {
		return false
	}
	cleanDesired := strings.ToLower(strings.TrimSpace(desired))
	cleanLive := strings.ToLower(live)

	// Fingerprint references: a live instance created from that image records
	// the resolved fingerprint in volatile.base_image.
	if isHexFingerprint(cleanDesired) {
		fp := strings.ToLower(liveConfig["volatile.base_image"])
		return fp != "" && (strings.HasPrefix(fp, cleanDesired) || strings.HasPrefix(cleanDesired, fp))
	}

	// Alias text heuristic (legacy behavior).
	if strings.Contains(cleanLive, strings.ReplaceAll(cleanDesired, ":", " ")) {
		return true
	}

	// os:release drift — manifest version (ubuntu:22.04) vs live codename
	// (ubuntu:jammy). Compare the desired version component against the image
	// properties LXD recorded on the instance.
	osPart, verPart, hasColon := strings.Cut(cleanDesired, ":")
	if hasColon && verPart != "" {
		if liveOS := strings.ToLower(liveConfig["image.os"]); liveOS != "" && osPart != "" && osPart != liveOS {
			return false
		}
		for _, field := range []string{"image.version", "image.release", "image.description"} {
			if v := strings.ToLower(liveConfig[field]); v != "" && strings.Contains(v, verPart) {
				return true
			}
		}
	}
	return false
}

// isHexFingerprint reports whether s looks like a LXD image fingerprint or a
// fingerprint prefix (hex, 12-64 chars, per the #Fingerprint schema).
func isHexFingerprint(s string) bool {
	if len(s) < 12 || len(s) > 64 {
		return false
	}
	for _, c := range s {
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') && (c < 'A' || c > 'F') {
			return false
		}
	}
	return true
}

func getLiveMounts(live *InstanceSnapshot) config.Mounts {
	var mounts config.Mounts
	for devName, devProps := range live.Devices {
		// Partition by device-key prefix (STORAGE-SPEC §5.1): only mount*
		// devices (mount%d and legacy mount-<path>) are host mounts. Non-root
		// disk devices with other keys (disk-* data disks, foreign
		// hand-added disks) are not mounts and are ignored here.
		if devProps["type"] == "disk" && devName != "root" && strings.HasPrefix(devName, "mount") {
			rec := devProps["recursive"] == "true"
			ro := devProps["readonly"] == "true"
			shiftVal := (devProps["shift"] == "true" || devProps["shift"] == "")
			mounts = append(mounts, config.Mount{
				Source:    devProps["source"],
				Path:      devProps["path"],
				Recursive: rec,
				Readonly:  ro,
				Shift:     &shiftVal,
			})
		}
	}
	sortMounts(mounts)
	return mounts
}

// getLiveDisks reconstructs DiskConfig values from live devices whose key
// starts with "disk-" (data disks, STORAGE-SPEC §5.1). `size` is read from the
// storage-volume metadata, never from the device map (LXD forbids `size` on
// non-root device maps).
func getLiveDisks(live *InstanceSnapshot, volumes map[string]map[string]*provider.StorageVolume) []config.DiskConfig {
	var disks []config.DiskConfig
	for devName, devProps := range live.Devices {
		if devProps["type"] != "disk" || !strings.HasPrefix(devName, "disk-") {
			continue
		}
		d := config.DiskConfig{
			Name:     strings.TrimPrefix(devName, "disk-"),
			Pool:     devProps["pool"],
			Path:     devProps["path"],
			Source:   devProps["source"],
			Readonly: devProps["readonly"] == "true",
			Bus:      devProps["io.bus"],
		}
		if d.Path == "" && d.Bus == "" {
			// Block-mode disks default to virtio-scsi (LXD's bus default); lxm
			// omits the key on the device map, so reconstruct it here.
			d.Bus = "virtio-scsi"
		}
		if vol := lookupVolume(volumes, d.Pool, d.Source); vol != nil {
			d.Size = vol.Config["size"]
		}
		disks = append(disks, d)
	}
	sort.Slice(disks, func(i, j int) bool { return disks[i].Name < disks[j].Name })
	return disks
}

// lookupVolume returns the live custom volume for pool/name, or nil.
func lookupVolume(volumes map[string]map[string]*provider.StorageVolume, pool, name string) *provider.StorageVolume {
	if volumes == nil {
		return nil
	}
	if poolVols, ok := volumes[pool]; ok {
		return poolVols[name]
	}
	return nil
}

func getLiveNetworks(live *InstanceSnapshot) []config.NetworkConfig {
	var nets []config.NetworkConfig
	for devName, devProps := range live.Devices {
		if devProps["type"] == "nic" {
			ip := devProps["ipv4.address"]
			parent := devProps["parent"]
			nets = append(nets, config.NetworkConfig{
				Name:   devName,
				IPv4:   ip,
				Parent: parent,
			})
		}
	}
	sort.Slice(nets, func(i, j int) bool {
		return nets[i].Name < nets[j].Name
	})
	return nets
}

func computeSummary(steps []Step) PlanSummary {
	var s PlanSummary
	for _, step := range steps {
		switch step.Action {
		case "create":
			s.Create++
		case "update":
			s.Update++
		case "recreate":
			s.Recreate++
		case "delete":
			s.Delete++
		case "start":
			s.Start++
		case "stop":
			s.Stop++
		case "noop":
			s.Noop++
		}
	}
	return s
}

// ToJSON serializes the Plan to JSON format.
func (p *Plan) ToJSON() (string, error) {
	data, err := json.MarshalIndent(p, "", "  ")
	if err != nil {
		return "", err
	}
	return string(data), nil
}
func buildRecipeSteps(manifest *config.Config) []RecipeStep {
	var steps []RecipeStep
	for _, rg := range manifest.Recipes {
		defaultRunAs := rg.RunAs
		if defaultRunAs == "" {
			defaultRunAs = manifest.User
		}
		for _, scriptPath := range rg.Scripts {
			ext := strings.ToLower(filepath.Ext(scriptPath))
			if ext == ".yaml" || ext == ".yml" {
				rMeta, err := recipe.LoadRecipe(scriptPath, manifest.ConfigBaseDir)
				if err == nil && rMeta != nil {
					effectiveRunAs := rMeta.GetRunAs()
					if (effectiveRunAs == "root" || effectiveRunAs == "") && rg.RunAs != "" {
						effectiveRunAs = rg.RunAs
					}
					if (effectiveRunAs == "root" || effectiveRunAs == "") && manifest.User != "" {
						effectiveRunAs = manifest.User
					}
					key := recipe.PathQualifiedHashKey(scriptPath, rMeta.Name)
					steps = append(steps, RecipeStep{
						Name:  rMeta.Name,
						Path:  scriptPath,
						RunAs: effectiveRunAs,
						Key:   key,
					})
					continue
				}
			}

			key := recipe.PathQualifiedHashKey(scriptPath, "")
			steps = append(steps, RecipeStep{
				Path:  scriptPath,
				RunAs: defaultRunAs,
				Key:   key,
			})
		}
	}
	return steps
}
