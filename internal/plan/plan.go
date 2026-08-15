package plan

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"reflect"
	"sort"
	"strings"

	"github.com/aiyor/lxm/internal/config"
	"github.com/aiyor/lxm/internal/recipe"
	"github.com/canonical/lxd/shared/api"
	"github.com/canonical/lxd/shared/units"
)

// InstanceSnapshot represents a read-only snapshot of a live LXD instance.
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
	Container       string                   `json:"container"`
	Action          string                   `json:"action"` // create | update | recreate | delete | start | stop | noop
	Changed         bool                     `json:"changed"`
	Diff            []FieldDiff              `json:"diff,omitempty"`
	Wait            bool                     `json:"wait,omitempty"`
	WaitPolicy      *config.WaitConfig       `json:"wait_policy,omitempty"`
	ConfigBaseDir   string                   `json:"config_base_dir,omitempty"`
	Recipes         []RecipeStep             `json:"recipes,omitempty"`
	Snapshot        string                   `json:"snapshot,omitempty"`
	ETag            string                   `json:"etag,omitempty"`
	RebuildFallback bool                     `json:"rebuild_fallback,omitempty"`
	PurgeSnapshots  bool                     `json:"purge_snapshots,omitempty"`
	PowerTransition string                   `json:"power_transition,omitempty"` // "start" | "stop" | "restart"
	InstancesPost   *api.InstancesPost       `json:"instances_post,omitempty"`
	InstancePut     *api.InstancePut         `json:"instance_put,omitempty"`
	RebuildPost     *api.InstanceRebuildPost `json:"rebuild_post,omitempty"`
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
	Compute(manifest *config.Config, live map[string]*InstanceSnapshot, hasRebuildExt bool) (*Plan, error)
}

type defaultReconciler struct{}

// NewReconciler returns a new default Reconciler.
func NewReconciler() Reconciler {
	return &defaultReconciler{}
}

func (r *defaultReconciler) Compute(manifest *config.Config, live map[string]*InstanceSnapshot, hasRebuildExt bool) (*Plan, error) {
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
	diffs, requiresRecreate := computeDiffs(manifest, liveInst)

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

		step.RebuildPost = &api.InstanceRebuildPost{
			Source: api.InstanceSource{
				Type:  "image",
				Alias: manifest.Image,
			},
		}
		postPayload, err := buildInstancesPost(manifest)
		if err != nil {
			return nil, fmt.Errorf("building recreate payload: %w", err)
		}
		step.InstancesPost = postPayload

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
		} else if liveState == "running" && hasVMConfigDiff(diffs) {
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

	plan.Steps = append(plan.Steps, step)
	plan.Summary = computeSummary(plan.Steps)
	return plan, nil
}

func buildInstancesPost(manifest *config.Config) (*api.InstancesPost, error) {
	instType := api.InstanceTypeContainer
	if manifest.Type == "virtual-machine" {
		instType = api.InstanceTypeVM
	}

	post := &api.InstancesPost{
		Name: manifest.Name,
		Type: instType,
		Source: api.InstanceSource{
			Type:  "image",
			Alias: manifest.Image,
		},
		InstancePut: api.InstancePut{
			Config:  make(map[string]string),
			Devices: make(map[string]map[string]string),
		},
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

	return post, nil
}

func buildInstancePut(manifest *config.Config, live *InstanceSnapshot) (*api.InstancePut, error) {
	put := &api.InstancePut{
		Architecture: live.Architecture,
		Config:       make(map[string]string),
		Devices:      make(map[string]map[string]string),
		Profiles:     live.Profiles,
		Ephemeral:    live.Ephemeral,
	}

	// 1. Copy live configuration base
	for k, v := range live.Config {
		put.Config[k] = v
	}
	put.Config["user.lxm.managed"] = "true"

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

	// 5. Copy live non-managed devices (preserving root and custom non-lxm devices)
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

	return put, nil
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
	oldBytes, err1 := units.ParseByteSizeString(oldSizeStr)
	newBytes, err2 := units.ParseByteSizeString(newSizeStr)
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

func computeDiffs(manifest *config.Config, live *InstanceSnapshot) ([]FieldDiff, bool) {
	var diffs []FieldDiff
	requiresRecreate := false

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

	return diffs, requiresRecreate
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
// image identity of a live instance. LXD reports the live identity as os:release
// (e.g. ubuntu:jammy) or a description, while manifests may reference an alias
// (ubuntu:22.04), a fingerprint, or a fingerprint prefix. The live instance
// config carries the image properties LXD recorded at creation
// (image.os/image.version/image.release/image.description) and the resolved
// fingerprint (volatile.base_image), which imageMatches consults so an alias
// and its resolved image compare equal — otherwise re-planning any
// alias-created container would demand a recreate (B4).
func imageMatches(desired, live string, liveConfig map[string]string) bool {
	if desired == live {
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
		if devProps["type"] == "disk" && devName != "root" {
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
