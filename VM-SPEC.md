# lxm Virtual Machine (VM) Specification

## 1. Overview & Architectural Principles

`lxm` is a declarative, plan-first fleet manager designed for reproducible infrastructure on LXD. While `lxm` originally targeted LXD system containers, LXD provides first-class support for hardware-virtualized **Virtual Machines (VMs)** via QEMU/KVM through the exact same underlying REST API.

This specification defines the comprehensive design, schema extensions, inheritance semantics, reconciliation logic, and operational lifecycle required to support **LXD Virtual Machines** natively alongside system containers in `lxm`.

### Foundational Principles

1. **Unified Instance Model**: Containers and Virtual Machines are first-class instances in `lxm`. They share the same manifest syntax, inheritance rules (`include`, `remove`, `replace`), recipes engine, and CLI commands (`plan`, `apply`, `list`, `status`, `shell`, `ssh`, `snapshot`, `rollback`).
2. **Plan-First Determinism**: Changing instance type (`container` $\leftrightarrow$ `virtual-machine`) is a non-transmutable mutation. The diff engine flags this with `RequiresRecreate: true` and forces `RebuildFallback: true` (delete + create), completely bypassing `RebuildInstance`.
3. **Canonical Modern LXD API Alignment**: Manifest firmware settings map to modern LXD keys (`boot.mode: "uefi-secureboot" | "uefi-nosecureboot" | "bios"`), avoiding legacy keys (`security.secureboot`, `security.csm`).
4. **Strict CUE & Normalization Boundary**: Authoring allows clean shorthands (e.g. `type: vm`, integer CPU counts, `secureboot: false`), which normalize into a single canonical resolved representation (`boot_mode: "uefi-nosecureboot"`) validated against `#LXM_RESOLVED`.
5. **Accurate Storage & ID Mapping Separation**:
   - **Containers**: Mount `shift: bool` (default `true`) controls Linux Kernel VFS idmapped mounts (`props["shift"] = "true"`).
   - **VMs**: Host directory mounts use **VirtioFS** (`virtiofsd`). VirtioFS ignores device-level `shift` and relies on instance-level user namespaces (`raw.idmap`).
6. **Hardened Agent-Aware Lifecycle**: VMs require guest `lxd-agent` initialization for file transfer and command execution. `lxm` treats agent startup as a transient wait phase (`wait.agent`, default 2m for VMs) with short 3-second per-attempt timeouts and error classification before executing cloud-init or recipe gates.
7. **Security by Default & Plan Warnings**: UEFI Secure Boot is enabled by default for VMs (`boot.mode: "uefi-secureboot"`). `raw_qemu` is an explicit opt-in hypervisor pass-through that generates a plan-time warning surfaced through `plan.Warnings` and the structured result envelope.

---

## 2. Manifest Schema Design (`lxm/config/v2`)

The manifest schema is extended with top-level fields: `type`, `limits`, and `vm`.

### 2.1 Top-Level Field Reference

| Field | Type | Default | Description |
| :--- | :--- | :--- | :--- |
| `type` | `string` | `"container"` | Instance type: `"container"`, `"virtual-machine"` (authoring accepts `"vm"`). |
| `limits` | `object` | `{}` | Hardware resource constraints (applies to both VMs and Containers). |
| `vm` | `object` | `{}` | Virtual-machine specific hypervisor, firmware, and boot parameters. |

---

### 2.2 Resource Modeling (`limits:`)

Resource limits are unified across containers and virtual machines:

```yaml
limits:
  cpu: 4          # vCPUs for VMs; cgroup CPU limit for containers (int, string, or cpuset)
  memory: 8GiB    # Guest RAM for VMs; cgroup memory limit for containers
  disk: 50GiB     # Root volume size override (devices.root.size)
```

- **`limits.cpu`**: Supports scalar integer (`4`) or cpuset string (`"4"`, `"0-3"`, `"0,2-3"`).
  - For **Containers**: Sets cgroup CPU allowance or cpuset range/list.
  - For **VMs**: Integer values define total vCPU count (`driver_qemu.go:cpuTopology`). String ranges/lists configure physical CPU pinning in LXD.
- **`limits.memory`**: Integer byte size string (e.g. `"4GiB"`, `"8GB"`, `"2048MiB"`). Suffixes strictly conform to LXD `shared/units.ParseByteSizeString` (`B`, `kB`, `MB`, `GB`, `TB`, `PB`, `EB`, `KiB`, `MiB`, `GiB`, `TiB`, `PiB`, `EiB`). Decimals are disallowed.
- **`limits.disk`**: Integer byte size string (e.g. `"50GiB"`, `"100GB"`). Configures the `root` disk device `size` parameter (`devices.root.size`).
  - **Online Grow**: Expanding `limits.disk` (`new > old`) is applied online via `PUT /1.0/instances/{name}`.
  - **Shrink Invariant**: Shrinking `limits.disk` (`new < old`) cannot be performed online in LXD and triggers `RequiresRecreate: true`.
  - **Storage Pool**: On create, the local root disk device sets `"pool": "default"`. On update, the storage pool is preserved from the live expanded root device.
  - **Unmanaged Semantics**: If `limits.disk` is omitted, the root disk size is unmanaged by `lxm`, preserving profile or pre-existing local disk configuration without emitting diffs.

---

### 2.3 VM Hypervisor Settings (`vm:`)

The `vm:` block configures VM-specific firmware, memory backing, and hypervisor behaviors:

```yaml
vm:
  secureboot: false     # Shorthand for boot_mode: uefi-nosecureboot
  boot_mode: ""         # Canonical mode: "uefi-secureboot" | "uefi-nosecureboot" | "bios"
  hugepages: false      # Back guest RAM by host hugepages (limits.memory.hugepages)
  raw_qemu: ""          # Advanced raw QEMU argument pass-through (raw.qemu)
```

#### Mapping to LXD Daemon Config:
- **Firmware & Boot Mode**:
  - `vm.secureboot: true` (or omitted default) $\rightarrow$ `boot.mode: "uefi-secureboot"`.
  - `vm.secureboot: false` $\rightarrow$ `boot.mode: "uefi-nosecureboot"`.
  - `vm.boot_mode: "bios"` $\rightarrow$ `boot.mode: "bios"` (legacy x86 BIOS mode).
  - *Mutual exclusion:* Authoring `secureboot` and `boot_mode` simultaneously is rejected during CUE validation (`Z1 != _|_ && Z2 != _|_`).
  - *Restart requirement:* Modifications to `boot.mode`, `hugepages`, or `raw_qemu` on a running VM set `step.PowerTransition = "restart"` (if desired power state is not explicitly `"stopped"`), as firmware/NVRAM changes and hypervisor memory/argument flags require a guest restart to take effect.
- **Hugepages**:
  - `vm.hugepages: true` $\rightarrow$ `limits.memory.hugepages: "true"`.
  - `vm.hugepages: false` $\rightarrow$ deletes `limits.memory.hugepages` from config map.
- **Raw QEMU (`raw_qemu`)**:
  - Maps to `raw.qemu`. Injected verbatim into the instance configuration.
  - *Plan-time Warning:* When `raw_qemu` is present, `lxm plan` appends an explicit warning to `plan.Warnings`: `instance "<name>" specifies raw.qemu hypervisor arguments: "<args>"`.

---

### 2.4 Storage & Host Mounts (Containers vs. VMs)

Host directory mounts (`mounts:`) work across both containers and VMs:

```yaml
mounts:
  - source: /home/tliang/devel
    path: /mnt/devel
    readonly: false
    shift: true           # Container VFS idmapping (default: true)

  - source: /mnt/nfs/shared
    path: /mnt/nfs
    shift: false          # Container opt-out: required for NFS/FUSE/socket passthrough
```

#### Technical Differences by Instance Type:
1. **Containers**:
   - `shift: true` (default) $\rightarrow$ LXD sets `shift: "true"` on the disk device, activating Linux Kernel VFS idmapped mounts (`MountOwnerShiftDynamic`).
   - `shift: false` $\rightarrow$ LXD sets `shift: "false"`, allowing raw NFS/CIFS/FUSE mounts or socket sharing (e.g. `/var/run/docker.sock`).
2. **Virtual Machines**:
   - LXD attaches host disk mounts via **VirtioFS** (`virtiofsd`).
   - **VirtioFS does not read `device.shift`**. UID/GID translation in VirtioFS is governed by the instance's user namespace mappings (`raw.idmap`).
   - The guest's `lxd-agent` automatically mounts the VirtioFS shares at the target `path`.

---

### 2.5 Wait & Readiness Policies (`wait:`)

Virtual machines require additional time for firmware boot, kernel startup, and `lxd-agent` initialization:

```yaml
wait:
  agent: 2m           # Wait deadline for guest lxd-agent communication (VMs only)
  cloud_init: 10m     # Wait deadline for cloud-init completion
  network: 60s        # Wait deadline for IPv4 address assignment
  poll: 5s            # Polling frequency
  required: true      # Fail-closed (exit code 7) on deadline exceed
```

- **`wait.agent`**: Duration string (e.g. `"2m"`, `"120s"`). Specifies how long `lxm` polls for `lxd-agent` before failing.
  - Defaults to `"2m"` for VMs (`type: "virtual-machine"`).
  - Omitted from resolved manifests for containers (`type: "container"`), as containers use direct kernel execution and do not require `lxd-agent`.
  - Expiry with `required: true` exits with **code 7 (`WAIT_TIMEOUT`)**.
  - Expiry with `required: false` logs a warning and proceeds (exit code 0).

---

## 3. CUE Schema Definition (`internal/config/schemas/v2.cue`)

The CUE schema is updated with precise deltas, aligning byte units with LXD's `ParseByteSizeString` (integers only) and enforcing `secureboot`/`boot_mode` mutual exclusion:

```cue
// ============================================================================
// VM & Resource Grammars
// ============================================================================

#InstanceType: "container" | "virtual-machine"
#InstanceTypeAuthoring: #InstanceType | "vm"

// Strict integer byte sizes aligned with LXD shared/units.ParseByteSizeString
#ByteSize: =~"^[0-9]+(B|kB|MB|GB|TB|PB|EB|KiB|MiB|GiB|TiB|PiB|EiB)?$"

#CPUCountAuthoring: (int & >0) | =~"^[0-9]+(-[0-9]+)?(,[0-9]+(-[0-9]+)?)*$"
#CPUCountResolved:  =~"^[0-9]+(-[0-9]+)?(,[0-9]+(-[0-9]+)?)*$" & !~"^0$"

#MountObjAuthoring: close({
	source:     string
	path:       #CleanMountPath
	readonly?:  bool | *false
	recursive?: bool | *false
	shift?:     bool | *true
})

#MountMapObjAuthoring: close({
	source:     string
	path?:      #CleanMountPath
	readonly?:  bool | *false
	recursive?: bool | *false
	shift?:     bool | *true
})

#MountObjResolved: close({
	source:    string & =~"^/"
	path:      #CleanMountPath
	readonly?: bool | *false
	recursive?: bool | *false
	shift?:    bool | *true
})

#LimitsAuthoring: close({
	cpu?:    #CPUCountAuthoring
	memory?: #ByteSize
	disk?:   #ByteSize
})

#LimitsResolved: close({
	cpu?:    #CPUCountResolved
	memory?: #ByteSize
	disk?:   #ByteSize
})

#VMConfigAuthoring: close({
	Z1="secureboot"?: bool
	Z2="boot_mode"?:  "uefi-secureboot" | "uefi-nosecureboot" | "bios"
	hugepages?:       bool | *false
	raw_qemu?:        string

	if Z1 != _|_ && Z2 != _|_ {
		_|_
	}
})

#VMConfigResolved: close({
	boot_mode?: "uefi-secureboot" | "uefi-nosecureboot" | "bios"
	hugepages?: bool | *false
	raw_qemu?:  string
})

#WaitConfig: close({
	agent?:      string
	cloud_init?: string | *"10m"
	network?:    string | *"60s"
	poll?:       string | *"5s"
	required?:   bool   | *true
})

// ============================================================================
// #LXM_AUTHORING & #LXM_RESOLVED Integration
// ============================================================================

#LXM_AUTHORING: close({
	schema?: "lxm/config/v2"
	base?:   bool | *false
	name?:   string
	type?:   #InstanceTypeAuthoring | *"container"
	image?:  #ImageRef
	user?:   string | *"ubuntu"
	status?: "present" | "absent" | *"present"
	state?:  "running" | "stopped"

	limits?: #LimitsAuthoring
	vm?:     #VMConfigAuthoring

	vars?: {[#EnvKey]: string}
	mounts?: [...(#MountStr | #MountObjAuthoring)] | close({[#CleanMountPath]: (string | #MountMapObjAuthoring)})
	networks?: [...close({
		name?:   string | *"eth0"
		ipv4?:   string
		parent?: string | *"lxdbr0"
	})]
	wait?: bool | #WaitConfig
	recipes?: [...((string & != "" & !~"^\\s*#") | close({root: [...(string & != "" & !~"^\\s*#")] & [_, ...]}) | close({
		run_as?: string | *"root"
		scripts: [...(string & != "" & !~"^\\s*#")] & [_, ...]
	}))]

	"cloud-init-include"?: [...string]
	X1="cloud-init"?:      string
	X2="cloud-init-file"?: string
	"network-config"?:     string

	if X1 != _|_ && X2 != _|_ {
		_|_
	}

	include?: [...string]
	remove?: close({
		mounts?:   [...string]
		networks?: [...string]
		recipes?:  [...string]
	})
	replace?: close({
		mounts?:   [...(#MountStr | #MountObjAuthoring)]
		networks?: [...]
		recipes?:  [...]
	})
	groups?: [...string]
	sudo?:              bool
	"inject_ssh_keys"?: bool
	"ssh_keys"?:        [...string]
})

#LXM_RESOLVED: close({
	schema:  "lxm/config/v2"
	name:    string & strings.MinRunes(1)
	type:    #InstanceType
	image?:  #ImageRef
	user:    string & strings.MinRunes(1)
	status:  "present" | "absent"
	state?:  "running" | "stopped"

	limits?: #LimitsResolved
	vm?:     #VMConfigResolved

	mounts: [...#MountObjResolved]
	networks: [...close({
		name:   string
		ipv4?:  string
		parent: string
	})]
	wait: #WaitConfig
	recipes: [...close({
		run_as:  string
		scripts: [...string] & [_, ...]
	})]

	"cloud-init-include"?: [...string]
	Y1="cloud-init"?:      string
	Y2="cloud-init-file"?: string
	"network-config"?:     string

	if Y1 != _|_ && Y2 != _|_ {
		_|_
	}

	groups?: [...string]
	sudo?:              bool
	"inject_ssh_keys"?: bool
	"ssh_keys"?:        [...string]
})
```

---

## 4. Go Structural Representation & Merge Semantics (`internal/config`)

### 4.1 Go Struct Definitions

```go
var cpuRegex = regexp.MustCompile(`^[0-9]+(-[0-9]+)?(,[0-9]+(-[0-9]+)?)*$`)

// CPUCount handles custom unmarshaling and validation for int or string CPU allocations.
type CPUCount string

func (c *CPUCount) UnmarshalYAML(value *yaml.Node) error {
	if value.Kind == yaml.ScalarNode {
		if value.Tag == "!!int" {
			val, err := strconv.Atoi(value.Value)
			if err != nil || val <= 0 {
				return fmt.Errorf("cpu count must be a positive integer, got %q", value.Value)
			}
			*c = CPUCount(value.Value)
			return nil
		}
		if value.Value == "0" || !cpuRegex.MatchString(value.Value) {
			return fmt.Errorf("invalid cpu count or cpuset format %q", value.Value)
		}
		*c = CPUCount(value.Value)
		return nil
	}
	return fmt.Errorf("invalid cpu count format")
}

// LimitsConfig models CPU, Memory, and Root Disk constraints.
type LimitsConfig struct {
	CPU      CPUCount        `yaml:"cpu,omitempty"`
	Memory   string          `yaml:"memory,omitempty"`
	Disk     string          `yaml:"disk,omitempty"`
	Presence map[string]bool `yaml:"-"`
}

func (l *LimitsConfig) UnmarshalYAML(value *yaml.Node) error {
	type rawLimits LimitsConfig
	var rl rawLimits
	if err := value.Decode(&rl); err != nil {
		return err
	}
	*l = LimitsConfig(rl)
	l.Presence = extractPresenceMap(value)
	return nil
}

// VMConfig models hypervisor-specific firmware and boot flags.
type VMConfig struct {
	SecureBoot *bool           `yaml:"secureboot,omitempty"`
	BootMode   string          `yaml:"boot_mode,omitempty"`
	Hugepages  bool            `yaml:"hugepages,omitempty"`
	RawQEMU    string          `yaml:"raw_qemu,omitempty"`
	Presence   map[string]bool `yaml:"-"`
}

func (v *VMConfig) UnmarshalYAML(value *yaml.Node) error {
	type rawVM VMConfig
	var rv rawVM
	if err := value.Decode(&rv); err != nil {
		return err
	}
	*v = VMConfig(rv)
	v.Presence = extractPresenceMap(value)
	return nil
}

// Mount models a host directory mapped to a container or VM path.
type Mount struct {
	Source    string `yaml:"source"`
	Path      string `yaml:"path"`
	Recursive bool   `yaml:"recursive,omitempty"`
	Readonly  bool   `yaml:"readonly,omitempty"`
	Shift     *bool  `yaml:"shift,omitempty"` // nil = true (default), explicit false disables
}

// WaitConfig specifies timeout and polling deadlines.
type WaitConfig struct {
	Agent     string          `yaml:"agent,omitempty"`
	CloudInit string          `yaml:"cloud_init,omitempty"`
	Network   string          `yaml:"network,omitempty"`
	Poll      string          `yaml:"poll,omitempty"`
	Required  bool            `yaml:"required,omitempty"`
	Presence  map[string]bool `yaml:"-"`
}

func DefaultWaitConfig() WaitConfig {
	return WaitConfig{
		Agent:     "",
		CloudInit: "10m",
		Network:   "60s",
		Poll:      "5s",
		Required:  true,
		Presence:  make(map[string]bool),
	}
}

// Config represents the complete parsed instance manifest.
type Config struct {
	Schema           string            `yaml:"schema,omitempty"`
	Name             string            `yaml:"name,omitempty"`
	Type             string            `yaml:"type,omitempty"` // "container" | "virtual-machine"
	Status           string            `yaml:"status,omitempty"`
	State            string            `yaml:"state,omitempty"`
	Limits           *LimitsConfig     `yaml:"limits,omitempty"`
	VM               *VMConfig         `yaml:"vm,omitempty"`
	WaitPolicy       WaitConfig        `yaml:"wait"`
	LegacyWaitPolicy *WaitConfig       `yaml:"wait_config,omitempty"`
	Image            string            `yaml:"image,omitempty"`
	User             string            `yaml:"user,omitempty"`
	Mounts           Mounts            `yaml:"mounts,omitempty"`
	Networks         []NetworkConfig   `yaml:"networks,omitempty"`
	// ... cloud-init, recipes, security, and inheritance directives
	presence         map[string]bool   `yaml:"-"`
}
```

---

### 4.2 Deep-Copy Inheritance Merge Semantics (`MergeConfigs`)

`internal/config/config.go:MergeConfigs` performs recursive presence-wins struct merging with explicit map deep-copying to prevent aliasing bugs:

1. **`Type` (Scalar)**:
   ```go
   if isPresent(overlay, "type", overlay.Type) {
       res.Type = overlay.Type
   } else {
       res.Type = base.Type
   }
   ```
2. **`Limits` (Recursive Struct with Deep Presence Copy)**:
   ```go
   if base.Limits != nil || overlay.Limits != nil {
       res.Limits = &LimitsConfig{Presence: make(map[string]bool)}
       if base.Limits != nil {
           res.Limits.CPU = base.Limits.CPU
           res.Limits.Memory = base.Limits.Memory
           res.Limits.Disk = base.Limits.Disk
           for k, v := range base.Limits.Presence {
               res.Limits.Presence[k] = v
           }
       }
       if overlay.Limits != nil {
           if overlay.Limits.Presence["cpu"] {
               res.Limits.CPU = overlay.Limits.CPU
               res.Limits.Presence["cpu"] = true
           }
           if overlay.Limits.Presence["memory"] {
               res.Limits.Memory = overlay.Limits.Memory
               res.Limits.Presence["memory"] = true
           }
           if overlay.Limits.Presence["disk"] {
               res.Limits.Disk = overlay.Limits.Disk
               res.Limits.Presence["disk"] = true
           }
       }
   }
   ```
3. **`VM` (Recursive Struct with Deep Presence Copy)**:
   ```go
   if base.VM != nil || overlay.VM != nil {
       res.VM = &VMConfig{Presence: make(map[string]bool)}
       if base.VM != nil {
           res.VM.SecureBoot = base.VM.SecureBoot
           res.VM.BootMode = base.VM.BootMode
           res.VM.Hugepages = base.VM.Hugepages
           res.VM.RawQEMU = base.VM.RawQEMU
           for k, v := range base.VM.Presence {
               res.VM.Presence[k] = v
           }
       }
       if overlay.VM != nil {
           if overlay.VM.Presence["secureboot"] {
               res.VM.SecureBoot = overlay.VM.SecureBoot
               res.VM.Presence["secureboot"] = true
           }
           if overlay.VM.Presence["boot_mode"] {
               res.VM.BootMode = overlay.VM.BootMode
               res.VM.Presence["boot_mode"] = true
           }
           if overlay.VM.Presence["hugepages"] {
               res.VM.Hugepages = overlay.VM.Hugepages
               res.VM.Presence["hugepages"] = true
           }
           if overlay.VM.Presence["raw_qemu"] {
               res.VM.RawQEMU = overlay.VM.RawQEMU
               res.VM.Presence["raw_qemu"] = true
           }
       }
   }
   ```
4. **`WaitPolicy.Agent`**:
   ```go
   if effectiveOverlayWait.Presence["agent"] {
       res.WaitPolicy.Agent = effectiveOverlayWait.Agent
       res.WaitPolicy.Presence["agent"] = true
   }
   ```

---

### 4.3 Normalization Pipeline Rules & Insertion Point

Normalization executes in `config.LoadConfig` immediately after `MergeConfigs` and template variable expansion, prior to `#LXM_RESOLVED` CUE validation:

1. `type: "vm"` normalizes to `type: "virtual-machine"`.
2. `type: ""` defaults to `type: "container"`.
3. If `type == "virtual-machine"`:
   - If `vm` is nil, initialize `vm = &VMConfig{BootMode: "uefi-secureboot"}`.
   - If `vm.BootMode == ""` and `vm.SecureBoot != nil`:
     - If `*vm.SecureBoot == true`, normalize `vm.BootMode = "uefi-secureboot"`.
     - If `*vm.SecureBoot == false`, normalize `vm.BootMode = "uefi-nosecureboot"`.
   - If `vm.BootMode == ""` and `vm.SecureBoot == nil`, normalize `vm.BootMode = "uefi-secureboot"`.
   - Clear `vm.SecureBoot = nil` so `#LXM_RESOLVED` only receives the single canonical `boot_mode` field.
   - If `!waitPolicy.Presence["agent"]` or `waitPolicy.Agent == ""`, default `waitPolicy.Agent = "2m"`.
4. If `type == "container"`:
   - `vm` is set to `nil` (omitted from resolved YAML).
   - If `!waitPolicy.Presence["agent"]`, clear `waitPolicy.Agent = ""` (avoids resolved manifest noise for containers).
5. For all mounts, `shift` defaults to `true` if `m.Shift == nil`.

---

## 5. Reconciliation & Diff Engine (`internal/plan`)

### 5.1 Snapshot, Plan & Status Data Model Extensions

1. **`plan.InstanceSnapshot`** (`internal/plan/plan.go`):
   ```go
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
   ```

2. **`plan.Step` Documentation Contract** (`internal/plan/plan.go`):
   ```go
   type Step struct {
       // ...
       PowerTransition string `json:"power_transition,omitempty"` // "start" | "stop" | "restart"
   }
   ```

3. **`plan.Plan` Warning Plumbing** (`internal/plan/plan.go`):
   ```go
   type Plan struct {
       Schema   string      `json:"schema"` // "lxm/plan/v1"
       Manifest string      `json:"manifest,omitempty"`
       Steps    []Step      `json:"steps"`
       Warnings []string    `json:"warnings,omitempty"`
       Summary  PlanSummary `json:"summary"`
   }
   ```

4. **`fleet.InstanceStatus`** (`internal/fleet/inventory.go`):
   ```go
   type InstanceStatus struct {
       Name   string `json:"name"`
       Type   string `json:"type"` // Backs the `lxm list` TYPE column
       Status string `json:"status"`
       // ...
   }
   ```

5. **`fetchLiveSnapshots`** (`cmd/lxm/commands.go`):
   Sources `instFull.ExpandedDevices` and `instFull.ExpandedConfig` from `api.InstanceFull` (returned via `svc.ListInstances()`), matching each instance and preserving the per-instance ETag.

---

### 5.2 Plan Construction: `buildInstancesPost` & `buildInstancePut`

#### 1. Creation Payload (`buildInstancesPost`):
```go
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
            // Root disk requires "pool" property on creation in LXD
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
```

#### 2. Update Payload (`buildInstancePut`):
```go
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
            // Inherit pool from expanded view if present
            if live.ExpandedDevices["root"] != nil {
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
            // Preserve local root if not being explicitly overridden by limits.disk
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
```

---

### 5.3 Diff Computation & Helper Invariants

```go
func isTypeChange(diffs []FieldDiff) bool {
    for _, d := range diffs {
        if d.Field == "type" {
            return true
        }
    }
    return false
}

func hasBootModeDiff(diffs []FieldDiff) bool {
    for _, d := range diffs {
        if d.Field == "boot.mode" {
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
        return true // Fail-safe: treat unparseable size comparison as requiring recreate
    }
    return newBytes < oldBytes
}

func getLiveMounts(live *InstanceSnapshot) []config.Mount {
    var mounts []config.Mount
    for devName, dev := range live.Devices {
        if dev["type"] == "disk" && devName != "root" {
            shiftVal := (dev["shift"] == "true" || dev["shift"] == "")
            mounts = append(mounts, config.Mount{
                Source:    dev["source"],
                Path:      dev["path"],
                Readonly:  dev["readonly"] == "true",
                Recursive: dev["recursive"] == "true",
                Shift:     &shiftVal,
            })
        }
    }
    sortMounts(mounts) // Deterministic sort for stable diffs
    return mounts
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
```

#### Diff Engine Rules in `computeDiffs`:
```go
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
        if live.ExpandedDevices["root"] != nil {
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

    // 3. VM Boot Mode Diffs (diffed against local Config with default normalization for pre-existing VMs)
    if manifest.Type == "virtual-machine" {
        desiredBoot := "uefi-secureboot"
        if manifest.VM != nil && manifest.VM.BootMode != "" {
            desiredBoot = manifest.VM.BootMode
        }
        liveBoot := live.Config["boot.mode"]
        if liveBoot == "" {
            liveBoot = "uefi-secureboot" // Normalize empty live boot.mode
        }
        if desiredBoot != liveBoot {
            diffs = append(diffs, FieldDiff{Field: "boot.mode", Old: liveBoot, New: desiredBoot})
        }
    }

    // 4. Mounts & Shift Normalization (order-insensitive)
    liveMounts := getLiveMounts(live)
    if !areMountsEqual(manifest.Mounts, liveMounts) {
        diffs = append(diffs, FieldDiff{Field: "mounts", Old: liveMounts, New: manifest.Mounts})
    }

    return diffs, requiresRecreate
}
```

#### Reconciler PowerTransition & Rebuild Bypass Invariants:
In `internal/plan/plan.go:Compute`:
```go
// 4. Power State Reconciliation with Normalized desiredState
desiredState := "running"
if manifest.State != "" {
    desiredState = manifest.State
}

liveState := "stopped"
if liveInst.Status == "Running" || liveInst.StatusCode == 103 {
    liveState = "running"
}

powerStateChanged := liveState != desiredState

// 5. Action and Transition Scheduling
if requiresRecreate {
    step.Action = "recreate"
    step.Changed = true
    step.Diff = diffs
    step.Wait = manifest.WaitPolicy.Required
    step.PurgeSnapshots = liveInst.HasSnapshots

    // CRITICAL: Type change cannot use RebuildInstance (POST /rebuild).
    // Force RebuildFallback = true (delete + create) whenever type changes.
    if isTypeChange(diffs) || !hasRebuildExt {
        step.RebuildFallback = true
    }
} else if len(diffs) > 0 {
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
    } else if liveState == "running" && hasBootModeDiff(diffs) {
        // If running, power state unchanged, and boot.mode changed, schedule restart
        step.PowerTransition = "restart"
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

// Plumb raw_qemu warning
if manifest.Type == "virtual-machine" && manifest.VM != nil && manifest.VM.RawQEMU != "" {
    plan.Warnings = append(plan.Warnings, fmt.Sprintf("instance %q specifies raw.qemu hypervisor arguments: %q", manifest.Name, manifest.VM.RawQEMU))
}
```

---

## 6. Execution & LXD Agent Handshake (`internal/apply`)

### The VM Guest Agent Handshake

In containers, commands (`lxc exec`, `cloud-init status`) execute directly via host kernel namespaces. In VMs, commands execute over a `vsock` socket connection to the guest's **`lxd-agent`**.

### Implementation in `internal/apply/apply.go:checkWaitPolicy`:

```go
var transientAgentErrors = []string{
    "The LXD agent is not running on this instance",
    "LXD agent not running",
    "Failed to connect to lxd-agent",
    "Failed to connect to instance socket",
    "websocket: close 1006 (abnormal closure)",
}

func isTransientAgentError(errMsg string) bool {
    for _, pattern := range transientAgentErrors {
        if strings.Contains(errMsg, pattern) {
            return true
        }
    }
    return false
}

func (e *defaultExecutor) checkWaitPolicy(ctx context.Context, step plan.Step, opts ApplyOpts) (*ErrorInfo, string) {
    inst, _, err := e.lxdSvc.GetInstance(step.Container)
    if err != nil || inst == nil || (inst.Status != "Running" && inst.StatusCode != 103) {
        return nil, ""
    }

    // 1. VM Agent Handshake Gate (VM instances only)
    if inst.Type == "virtual-machine" {
        agentTimeout := 120 * time.Second
        if step.WaitPolicy != nil && step.WaitPolicy.Agent != "" {
            if d, err := time.ParseDuration(step.WaitPolicy.Agent); err == nil {
                agentTimeout = d
            }
        }

        agentCtx, cancelAgent := context.WithTimeout(ctx, agentTimeout)
        defer cancelAgent()

        agentReady := false
        ticker := time.NewTicker(2 * time.Second)
        defer ticker.Stop()

    agentLoop:
        for {
            select {
            case <-agentCtx.Done():
                break agentLoop
            case <-ticker.C:
                // Attempt exec with short 3-second context to prevent blocking the full deadline
                execCtx, cancelExec := context.WithTimeout(agentCtx, 3*time.Second)
                _, execErr := e.lxdSvc.ExecInstanceContext(execCtx, step.Container, []string{"systemctl", "is-system-running"}, 0, nil)
                cancelExec()

                if execErr == nil {
                    agentReady = true
                    break agentLoop
                }

                if !isTransientAgentError(execErr.Error()) {
                    // Non-agent error, agent is communicating
                    agentReady = true
                    break agentLoop
                }
            }
        }

        if !agentReady {
            if step.WaitPolicy != nil && step.WaitPolicy.Required {
                return &ErrorInfo{
                    Code:      "WAIT_TIMEOUT", // Exit Code 7
                    Container: step.Container,
                    Message:   fmt.Sprintf("lxd-agent wait timed out after %s on %q", agentTimeout, step.Container),
                }, ""
            }
            return nil, fmt.Sprintf("lxd-agent wait timed out after %s on VM %q (soft wait)", agentTimeout, step.Container)
        }
    }

    // 2. Proceed to cloud-init status --wait and network polling...
    // ...
}
```

---

## 7. Operational & Lifecycle Integration

### 7.1 Snapshots & Rollbacks for VMs
- Automatic pre-recipe snapshots (`user.lxm.snapshot.*`) and manual snapshot commands (`lxm snapshot create`, `lxm rollback`, `lxm snapshot gc`) use LXD's native instance snapshot API (`CreateInstanceSnapshot`, `RestoreInstanceSnapshot`).
- **VM Snapshot Semantics:** Pre-recipe VM snapshots are **stateless and crash-consistent**. Stateful snapshots (preserving guest RAM) require LXD `migration.stateful: true` and are not created automatically.

### 7.2 SSH Operations & Key Lifecycle
- `lxm ssh` resolves the VM's IPv4 address from LXD and connects via OpenSSH with managed host keys in `~/.config/lxm/known_hosts`.
- On instance recreate or deletion (including type changes), `PurgeContainerKey` automatically flushes stale OpenSSH host keys under `syscall.Flock` via the recreate branch in `apply.go:326`.

### 7.3 `lxm doctor` Diagnostics
`newDoctorCmd` (`cmd/lxm/commands.go`) adds KVM hardware acceleration checks:
```go
// Check /dev/kvm accessibility
if file, err := os.OpenFile("/dev/kvm", os.O_RDWR, 0); err == nil {
    _ = file.Close()
    checks = append(checks, "[OK] KVM hardware virtualization (/dev/kvm accessible)")
} else {
    warnings = append(warnings, "KVM hardware virtualization (/dev/kvm) not accessible; VMs will run without hardware acceleration or fail to launch")
    checks = append(checks, "[WARN] KVM hardware virtualization")
}
```

### 7.4 Legacy `internal/lxm` Package Status
- The `internal/lxm` package is confirmed as legacy/unreferenced by the Cobra CLI (`cmd/lxm` exclusively wires `internal/plan` and `internal/apply`).
- All active VM features and mount shift logic are implemented exclusively in `internal/config`, `internal/plan`, `internal/apply`, `internal/fleet`, and `internal/lxd`.

---

## 8. Complete Manifest Examples

### 8.1 Standalone VM Manifest with Shorthands (`config/ml-workstation.yaml`)

```yaml
schema: lxm/config/v2
name: ml-workstation
type: vm                     # Shorthand for virtual-machine
image: ubuntu-24.04
status: present
state: running

user: ubuntu
sudo: true
inject_ssh_keys: true

limits:
  cpu: 8                     # Unquoted integer shorthand
  memory: 16GiB
  disk: 100GiB

vm:
  secureboot: false          # Shorthand for boot_mode: uefi-nosecureboot (mutually exclusive with boot_mode)
  hugepages: false

mounts:
  - source: /home/tliang/devel
    path: /mnt/devel
  - source: /mnt/nfs/datasets
    path: /mnt/datasets
    shift: false             # Disable shift for NFS mount
    readonly: true

networks:
  - name: eth0
    ipv4: 10.10.10.60
    parent: lxdbr0

recipes:
  - run_as: root
    scripts:
      - recipes/install-cuda.sh
      - recipes/install-docker.sh

wait:
  agent: 3m
  cloud_init: 10m
  network: 60s
```

### 8.2 Fleet Inheritance (`_base_vm.yaml` $\rightarrow$ `config/node01.yaml`)

**`_base_vm.yaml`**:
```yaml
schema: lxm/config/v2
base: true
type: virtual-machine
image: ubuntu-24.04
user: ubuntu
sudo: true
inject_ssh_keys: true

limits:
  cpu: 4
  memory: 8GiB
  disk: 50GiB

vm:
  boot_mode: uefi-secureboot

networks:
  - name: eth0
    parent: lxdbr0

wait:
  agent: 2m
  cloud_init: 5m
```

**`config/node01.yaml`**:
```yaml
schema: lxm/config/v2
include:
  - ../_base_vm.yaml
name: k8s-node-01
groups:
  - k8s
  - cluster

limits:
  cpu: 8                     # Overrides base CPU, inherits base Memory & Disk

networks:
  - name: eth0
    ipv4: 10.10.10.101
    parent: lxdbr0

recipes:
  - run_as: root
    scripts:
      - recipes/k8s/install-kubelet.sh
```

---

## 9. Implementation Checklist & Phase Plan

| Phase | Component / Package | Specific Tasks & File Changes |
| :--- | :--- | :--- |
| **Phase 1: Schemas & Config** | `internal/config` | 1. Update `v2.cue` with `#InstanceType`, `#LimitsAuthoring`, `#LimitsResolved`, `#VMConfigAuthoring` (with mutual exclusion between `secureboot` and `boot_mode`), `#VMConfigResolved`, `#ByteSize`, `#CPUCountAuthoring`, `#CPUCountResolved`, preserving cloud-init & recipe guards.<br>2. Add custom `UnmarshalYAML` for `CPUCount`, `LimitsConfig`, `VMConfig`.<br>3. Add `Shift *bool` to `Mount`.<br>4. Keep parameterless `DefaultWaitConfig()`, update `MergeConfigs` with deep presence copying for `Type`, `Limits`, `VM`, `WaitPolicy.Agent`.<br>5. Implement normalization rules (`vm` $\rightarrow$ `virtual-machine`, `secureboot` $\rightarrow$ `boot_mode`, concrete `vm` defaults, presence-aware container `wait.agent` cleanup). |
| **Phase 2: Reconciler & Plan** | `internal/plan` | 1. Add `Type`, `ExpandedDevices`, and `ExpandedConfig` to `InstanceSnapshot`.<br>2. Add `Warnings []string` to `plan.Plan`.<br>3. Implement non-destructive `buildInstancePut` and `buildInstancesPost` (with `"pool": "default"` on root disk) preserving existing devices/networks while reconciling `limits.*`, `boot.mode`, and mount `shift`.<br>4. Extend `computeDiffs` for `type`, `limits.cpu`/`memory` against local `live.Config`, `limits.disk` unmanaged semantics, `boot.mode` (triggering restart `PowerTransition` respecting normalized `desiredState` precedence), `hasBootModeDiff`, and deterministic mount shift normalization via `getLiveMounts` and `areMountsEqual`.<br>5. Force `RebuildFallback = true` whenever `type` changes.<br>6. Plumb `raw_qemu` warnings into `plan.Warnings`. |
| **Phase 3: Executor & Handshake** | `internal/apply` | 1. Add `lxd-agent` polling retry loop to `checkWaitPolicy` with 3s per-attempt timeout.<br>2. Classify transient agent errors via `isTransientAgentError`.<br>3. Verify `PowerTransition: "restart"` passthrough on update after config PUT.<br>4. Map `wait.agent` expiry to exit code 7 (`WAIT_TIMEOUT`). |
| **Phase 4: Fleet & Diagnostics** | `cmd/lxm`, `internal/fleet` | 1. Add `Type` to `fleet.InstanceStatus`.<br>2. Update `fetchLiveSnapshots` in `commands.go` to capture `inst.Type`, `inst.ExpandedDevices`, and `inst.ExpandedConfig` from `api.InstanceFull`.<br>3. Add `TYPE` column to `lxm list`.<br>4. Add `/dev/kvm` diagnostic check to `lxm doctor`. |
| **Phase 5: Tests & Docs** | `internal/...`, `docs/` | 1. Add unit tests for `CPUCount` int/string decoding, `MergeConfigs` struct merge with deep copying, VM plan generation, update diffs, order-insensitive mount diffs, type-change rebuild fallback, and `PowerTransition = "restart"` precedence.<br>2. Update user guide and schema documentation. |
