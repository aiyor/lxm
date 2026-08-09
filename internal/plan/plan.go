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
)

// InstanceSnapshot represents a read-only snapshot of a live LXD container instance.
type InstanceSnapshot struct {
	Name         string                       `json:"name"`
	Status       string                       `json:"status"`
	StatusCode   int                          `json:"status_code"`
	Architecture string                       `json:"architecture"`
	Config       map[string]string            `json:"config,omitempty"`
	Devices      map[string]map[string]string `json:"devices,omitempty"`
	Profiles     []string                     `json:"profiles,omitempty"`
	Ephemeral    bool                         `json:"ephemeral"`
	ETag         string                       `json:"etag"`
	HasSnapshots bool                         `json:"has_snapshots"`
}

// Plan represents a complete, serializable reconciliation plan.
type Plan struct {
	Schema   string      `json:"schema"` // "lxm/plan/v1"
	Manifest string      `json:"manifest,omitempty"`
	Steps    []Step      `json:"steps"`
	Summary  PlanSummary `json:"summary"`
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
	PowerTransition string                   `json:"power_transition,omitempty"` // "start" | "stop"
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
		if !hasRebuildExt {
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
	if len(diffs) > 0 {
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
		}
	} else if powerStateChanged {
		step.Changed = true
		if desiredState == "running" {
			step.Action = "start"
		} else {
			step.Action = "stop"
		}
		step.Diff = []FieldDiff{
			{Field: "state", Old: liveState, New: desiredState},
		}
	} else {
		step.Action = "noop"
		step.Changed = false
	}

	// Recipe updates for existing container
	step.Recipes = buildRecipeSteps(manifest)

	plan.Steps = append(plan.Steps, step)
	plan.Summary = computeSummary(plan.Steps)
	return plan, nil
}

func buildInstancesPost(manifest *config.Config) (*api.InstancesPost, error) {
	post := &api.InstancesPost{
		Name: manifest.Name,
		Source: api.InstanceSource{
			Type:  "image",
			Alias: manifest.Image,
		},
		InstancePut: api.InstancePut{
			Config: map[string]string{
				"user.lxm.user":    manifest.User,
				"user.lxm.managed": "true",
			},
			Devices: make(map[string]map[string]string),
		},
	}
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

	for i, m := range manifest.Mounts {
		devName := fmt.Sprintf("mount%d", i)
		props := map[string]string{
			"type":   "disk",
			"source": m.Source,
			"path":   m.Path,
			"shift":  "true",
		}
		if m.Recursive {
			props["recursive"] = "true"
		}
		if m.Readonly {
			props["readonly"] = "true"
		}
		post.Devices[devName] = props
	}

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

	for k, v := range live.Config {
		put.Config[k] = v
	}
	put.Config["user.lxm.managed"] = "true"
	for dev, props := range live.Devices {
		devCopy := make(map[string]string)
		for k, v := range props {
			devCopy[k] = v
		}
		put.Devices[dev] = devCopy
	}

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

	// Remove old lxm disk & nic devices
	for devName, devProps := range put.Devices {
		if devProps["type"] == "disk" && devName != "root" {
			delete(put.Devices, devName)
		}
		if devProps["type"] == "nic" {
			delete(put.Devices, devName)
		}
	}

	for i, m := range manifest.Mounts {
		devName := fmt.Sprintf("mount%d", i)
		props := map[string]string{
			"type":   "disk",
			"source": m.Source,
			"path":   m.Path,
			"shift":  "true",
		}
		if m.Recursive {
			props["recursive"] = "true"
		}
		if m.Readonly {
			props["readonly"] = "true"
		}
		put.Devices[devName] = props
	}

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

func computeDiffs(manifest *config.Config, live *InstanceSnapshot) ([]FieldDiff, bool) {
	var diffs []FieldDiff
	requiresRecreate := false

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

	liveMounts := getLiveMounts(live)
	if !reflect.DeepEqual(manifest.Mounts, liveMounts) {
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
			mounts = append(mounts, config.Mount{
				Source:    devProps["source"],
				Path:      devProps["path"],
				Recursive: rec,
				Readonly:  ro,
			})
		}
	}
	sort.Slice(mounts, func(i, j int) bool {
		return mounts[i].Path < mounts[j].Path
	})
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
