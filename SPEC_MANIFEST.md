# lxm v2 Manifest Specification

## 1. Overview & Schema Boundary

`lxm v2` manifests are declarative YAML documents defining desired LXD container fleet configurations. Manifest authoring is validated against two CUE schema definitions residing in `internal/config/schemas/v2.cue`:

1. **`#LXM_AUTHORING`**: The human authoring surface. Supports flexible shorthands, file-local variables (`vars:`), tilde expansion (`~`), and inheritance directives (`include`, `remove`, `replace`). Used for pre-compilation validation.
2. **`#LXM_RESOLVED`**: The strict, closed schema enforced on compiled manifests. Rejects all shorthands, directives, and unrecognized fields. Serves as the authoritative security gate enforcing clean mount destinations (`#CleanMountPath`) and absolute source paths (`source: =~"^/"`).

---

## 2. The 6-Step Compilation Pipeline

Manifest compilation converts raw YAML files into immutable `Manifest` objects through six sequential phases:

```mermaid
flowchart LR
    Step1["1. Parse YAML & Track Presence"] --> Step2["2. Normalize Shorthands & Defaults"]
    Step2 --> Step3["3. Merge Inheritance & Directives"]
    Step3 --> Step4["4. Anchored Regex Templating"]
    Step4 --> Step5["5. CUE Validation (#LXM_RESOLVED)"]
    Step5 --> Step6["6. Materialize Immutable Manifest"]
```

1. **Parse & Presence Tracking**: Parses YAML into `yaml.Node` trees while recording explicit key presence for presence-wins scalar merging.
2. **Normalize Shorthands & Defaults**: Normalizes mount authoring styles into `#MountObjResolved` structures, expands `wait` boolean shorthands into struct objects, and materializes default values.
3. **Merge Inheritance & Directives**: Resolves `include` / `base` manifests. Merges scalar values via presence-wins rules, recursive structs, and applies `remove` / `replace` list directives.
4. **Anchored Regex Templating**: Executes anchored regex parameter substitution for `{{ .Env.VAR }}`, `{{ .Vars.VAR }}`, `{{ .Group }}`, and `{{ .Name }}`. Unbound environment variables trigger **exit code 3**.
5. **CUE Validation**: Validates the merged structure against the strict `#LXM_RESOLVED` CUE schema.
6. **Immutable Manifest**: Instantiates an immutable `Manifest` struct ready for reconciler diff computation.

---

## 3. Merge & Inheritance Semantics

### 3.1 Instance Types (`type`)
The `type` field specifies the instance virtualization model:
* `type: container` (default): System container utilizing Linux kernel namespaces and cgroups.
* `type: virtual-machine` (or authoring shorthand `type: vm`): Hardware-isolated virtual machine running under QEMU/KVM.
* **Mutation Rule**: Switching `type` on an existing instance is non-transmutable and requires recreation (`RequiresRecreate = true`, forcing `RebuildFallback = true`).

### 3.2 Hardware Limits (`limits`)
Unified resource limits across containers and VMs:
```yaml
limits:
  cpu: 4             # Integer vCPU count (VM) / cgroup CFS quota (container), or cpuset range "0-3"
  memory: 8GiB       # Guest RAM size (VM) / memory cgroup limit (container)
  disk: 50GiB        # Root disk volume resize override
```

### 3.3 VM Hypervisor Settings (`vm`)
Exclusive configuration block for virtual machines:
```yaml
vm:
  boot_mode: uefi-secureboot  # "uefi-secureboot" | "uefi-nosecureboot" | "bios"
  secureboot: true            # Authoring shorthand for boot_mode (mutually exclusive with boot_mode)
  hugepages: false            # Back VM memory with host hugepages (limits.memory.hugepages)
  raw_qemu: ""                # QEMU hypervisor argument injection (raw.qemu)
```

### 3.4 Presence-Wins Scalar Merging
Scalar fields (e.g. `user`, `image`, `profiles`, `state`, `type`, `provider`, `remote`, `target`, `project`) merge according to node-level presence:
* **Omitted Field**: Inherits the base manifest value.
* **Explicitly Set Field**: Overrides the base manifest value, even when set to an explicit zero value (e.g. `user: ""` explicitly clears an inherited user).

### 3.5 Provider & Remote Targeting (`provider`, `remote`, `target`, `project`, `remotes`)
`lxm` supports multi-provider execution (Incus 7.x and LXD), cluster node placement, and remote mTLS management:
```yaml
schema: lxm/config/v2
name: web-frontend
type: container
image: images:ubuntu/24.04

# Provider & Remote Routing
provider: incus       # "incus" | "lxd" | "auto" (default: auto-detected)
remote: lab-node1     # Target remote name declared in remotes.yaml or manifest
target: incus-node2   # Cluster member node for instance placement
project: production   # Incus/LXD project isolation boundary (default: "default")

# Manifest-Declared Remotes (Optional fleet-scoped endpoint definitions)
remotes:
  lab-node1:
    address: https://10.171.13.50:8443
    provider: incus
    project: production
    insecure: false
```

### 3.5 Network Schema (`#NetworkObj`)
Network interfaces are declared using the `#NetworkObj` schema:
```yaml
networks:
  - name: eth0
    ipv4: 10.10.10.50
    parent: lxdbr0
```
Fields allowed: `name` (required string), `ipv4` (optional IPv4 address string), `parent` (optional network bridge name string).

### 3.6 Virtual Switches (`#VSwitchObjAuthoring` / fleet-scoped)

> This section is the **canonical manifest-schema reference** for `vswitches:` and `network_policy:`
> (it mirrors the CUE schemas in `internal/config/schemas/v2.cue`). The feature-level spec — the
> generator matrix, CIDR decomposition, reconciliation/execution model, and verification — lives in
> [`NETWORK-SPEC.md`](NETWORK-SPEC.md). Keep the two schema tables in sync when either changes.

`vswitches:` declares provider managed virtual switches (Linux bridges or OVN overlay networks) that lxm creates and owns. It is **fleet-scoped**: the
union of every loaded manifest's `vswitches:` is compiled (identical duplicates dedup; conflicting
specs exit 3 citing both files). Usually declared in a `_base.yaml` and inherited via `include`.

```yaml
vswitches:
  - name: vmbr0
    type: bridge
    ipv4: 10.30.0.1/24
    group: vms

  - name: ovnbr0
    type: ovn
    parent: lxdbr0
    ipv4: 10.60.0.1/24
    group: ovnservices
    mtu: 1442
```

| Field | Type | Default | Rules |
| :-- | :-- | :-- | :-- |
| `name` | string | — (required) | `=~"^[a-z][a-z0-9-]{0,30}$"`. |
| `type` | string | `"bridge"` | `"bridge"` \| `"ovn"`. `"bridge"` creates a local Linux bridge; `"ovn"` creates an Open Virtual Network overlay switch across cluster nodes. Immutable after create (exit 3). |
| `parent` | string | — | Uplink parent network (e.g. `lxdbr0` / `incusbr0`). Required on `type: ovn`; forbidden on `type: bridge`. Immutable after create (exit 3). |
| `driver` | string | `"native"` | `"native" \| "openvswitch"`; bridge-only (forbidden on `type: ovn`); immutable after create (exit 3). |
| `ipv4` | string | — (required) | CIDR whose address is the **first usable host** (network `.1`); prefix `/8`–`/29` (Go-validated). Immutable after create (exit 3). |
| `ipv6` | string | `"none"` | v1 lock: only `"none"`. |
| `nat` | bool | `true` | → `ipv4.nat`. |
| `group` | string | — | Group membership; absence ⇒ managed for addressing only (no ACLs). |
| `internet` | bool | `true` | Only meaningful with `group`. |
| `mtu` | int | — | Optional MTU override (maps to `bridge.mtu` on OVN; must be ∈ [576, 65535]). |
| `config` | map[string]string | — | Optional backend-specific passthrough options (managed keys take strict precedence). |

An ungrouped vswitch gets no ACLs (stock routing). Removing `group` detaches the ACL; removing
the vswitch stops managing it (lxm never deletes networks).

### 3.7 Network Policy (`network_policy` / fleet-scoped)

`network_policy:` is a group-based traffic policy compiled deterministically into LXD network ACLs
(`lxm-<vswitch>`, `reject` default). Fleet-scoped; `allow` entries and `internal_cidrs` are unioned
and deduplicated across manifests (identical dedup; conflicting `(from,to)` with differing
`direction` ⇒ exit 3).

```yaml
network_policy:
  internal_cidrs:              # optional; ADDITIVE to the locked default internal set (§3.6/NETWORK-SPEC §5.2)
    - 192.168.77.0/24
  allow:
    - from: vms                # required; a group with ≥1 vswitch
      to: services             # required; a group with ≥1 vswitch
      direction: both          # "both" (default, mutual) | "egress" (one-way initiation)
```

`allow` entries reference network groups (from `vswitches[].group`):

| Field | Type | Default | Rules |
| :-- | :-- | :-- | :-- |
| `from` | string | — (required) | A group with ≥1 vswitch; unknown group ⇒ exit 3. |
| `to` | string | — (required) | A group with ≥1 vswitch; unknown group ⇒ exit 3. |
| `direction` | string | `"both"` | `"both"` (mutual) \| `"egress"` (one-way initiation). |

Rules: `from == to` is a no-op (intra-group is already allowed; plan warning); `internal_cidrs`
entries must be valid CIDRs, are additive to the locked default internal set (RFC1918 supernets,
`100.64/10`, loopback, link-local) that `internet: true` groups may not reach, and duplicates dedup
silently.

### 3.8 List Directives (`remove` and `replace`)
List fields (`mounts`, `networks`, `recipes`, `disks`) concatenate by default. Inheritance behavior can be modified using directives:

```yaml
remove:
  mounts: ["/var/data"]
  recipes: ["recipes/db/install.sh"]
  disks: ["data"]
replace:
  networks:
    - name: eth0
      ipv4: 10.10.10.50
      parent: lxdbr0
  disks:
    - name: data
      size: 100GiB
      path: /var/lib/postgresql
```

* **`remove` Matching**: `remove.mounts` matches by normalized destination path (`filepath.Clean`), `remove.networks` matches by network interface `name`, `remove.recipes` matches by exact resolved script path or recipe metadata `name`, and `remove.disks` matches by disk `name`. Non-matching `remove` directives fail compilation with **exit code 3**.
* **`replace` Directive**: Completely replaces inherited list items with the newly declared items.

### 3.9 Data Disks (`disks` / VM-only)

> This section is the **canonical manifest-schema reference** for `disks:` (it mirrors the CUE
> schemas in `internal/config/schemas/v2.cue`). The feature-level spec — the mode × ownership
> matrix, verified LXD constraints, reconciliation/execution model, and locked behavioral rules —
> lives in [`STORAGE-SPEC.md`](STORAGE-SPEC.md). Keep the two schema tables in sync when either
> changes.

`disks:` attaches **additional storage-pool volumes** to virtual machines. It is **instance-scoped**
(declared on the leaf manifest, inherited like other lists) and **VM-only in v1** (declaring it on a
`type: container` is a compile error, exit 3). Each disk is one of four combinations of two
orthogonal axes:

* **Mode** — filesystem (guest-mounted) vs block (raw device) — selected by `path` presence.
* **Ownership** — lxm-managed (lxm provisions the volume) vs external (a pre-existing custom volume)
  — selected by `source` presence.

```yaml
schema: lxm/config/v2
name: db-vm
type: vm

disks:
  - name: data                    # filesystem (managed)
    size: 100GiB                  # required (managed); forbidden when source set
    path: /var/lib/postgresql     # presence ⇒ filesystem mode

  - name: wal                     # block (managed)
    size: 20GiB
    bus: nvme                     # block-only; default "virtio-scsi"

  - name: shared-fs               # filesystem (external)
    source: web-root-vol          # pre-existing custom volume
    pool: fast-pool
    path: /srv/www
    readonly: true

  - name: shared-block            # block (external)
    source: ceph-osd-vol
    pool: fast-pool
```

| Field | Type | Default | Rules |
| :-- | :-- | :-- | :-- |
| `name` | string | — (required) | `=~"^[a-z][a-z0-9-]{0,30}$"`, must not be `root`. Primary identity; LXD device key `disk-<name>`. |
| `size` | string | — | `#ByteSize` (same grammar as `limits.disk`). **Required** when `source` is unset (managed). **Forbidden** when `source` is set (external). Never written to the device map. |
| `pool` | string | `"default"` | Storage pool name. |
| `path` | string | — | Guest mount path (`#CleanMountPath`). **Presence selects filesystem mode**; absence selects block mode. Allowed with or without `source`. |
| `source` | string | — | Name of a pre-existing custom storage volume in `pool`. **Presence selects external ownership**; absence selects lxm-managed (volume `<instance>-<name>`). Mutually exclusive with `size`. |
| `readonly` | bool | `false` | Maps to device `readonly: "true"`. |
| `bus` | string | `"virtio-scsi"` | `"virtio-scsi" \| "virtio-blk" \| "nvme"` → device `io.bus`. **Block mode only**; rejected when `path` is set. The default `virtio-scsi` is LXD's own bus default and is omitted from the device map (`io.bus` is emitted only for non-default buses). |

Mode × ownership matrix (see [`STORAGE-SPEC.md`](STORAGE-SPEC.md) §3 for the full device shape):

| Mode | `source` | `path` | `size` | Volume content type |
| :-- | :-- | :-- | :-- | :-- |
| FS (managed) | derived `<inst>-<name>` | set | required | `filesystem` |
| FS (external) | set | set | forbidden | `filesystem` |
| Block (managed) | derived `<inst>-<name>` | — | required | `block` |
| Block (external) | set | — | forbidden | `block` |

Post-merge validation (`ValidatePostMerge`): `name` required, `!= "root"`, unique within `disks`;
the union of `mounts[].path` and filesystem `disks[].path` (cleaned) has no duplicate mount paths
(exit 3). Size rules: `size` with `source` is forbidden by CUE; `size` required for managed disks
(`source` unset) is enforced Go-side in `LoadConfig` normalization.

### 3.10 Image References & Remotes (`image` / `image_remotes` / fleet-scoped)

> This section is the **canonical manifest-schema reference** for `image:` and `image_remotes:`
> (it mirrors the CUE schemas in `internal/config/schemas/v2.cue`). The feature-level spec — remote
> resolution order, the canonical type-qualified local alias, the fetch decision, and the Phase −1
> execution model — lives in [`IMAGE-SPEC.md`](IMAGE-SPEC.md). Keep the two schema tables in sync
> when either changes.

`image:` is a LXD image reference. Its interpretation depends on its form:

| Form | CUE match | Behaviour |
| :-- | :-- | :-- |
| hex fingerprint (12–64 hex chars) | `#Fingerprint` | **Local only.** Never fetched. A miss fails at apply (exit 4) exactly as today. |
| bare alias (no `:`) | `#LocalAlias` | **Local only.** Never fetched. A miss fails at apply (exit 4) exactly as today. |
| `remote:alias` (exactly one `:`) | `#RemoteAlias` | **Remote.** When the canonical local alias is not cached, lxm looks the image up on the named remote (a simplestreams server) and fetches it before create/recreate. |

```yaml
image: ubuntu:22.04        # remote:alias — fetched into the local store when uncached
image: jammy                # bare local alias
image: 8d3c…d7c             # fingerprint
```

The `remote:alias` form is resolved and fetched; the fetched image is tagged in the local LXD store
with a deterministic, **type-qualified** local alias — `ubuntu/22.04` for containers,
`ubuntu/22.04/vm` for virtual machines — which is what create/rebuild payloads use as
`Source.Alias`. Bare aliases and fingerprints keep their literal identity.

`image_remotes:` maps a remote **name** to a simplestreams **URL**. It is **fleet-scoped**: the
effective registry is the union of every loaded manifest's declarations plus the locked built-ins
(`ubuntu`, `ubuntu-daily`, `images`), with a same-named declaration overriding a built-in. Usually
declared in a `_base.yaml` and inherited via `include`. Rich URL validation (scheme, host,
loopback-http rule) and URL canonicalization run Go-side in `ValidatePostMerge`.

```yaml
schema: lxm/config/v2
image_remotes:
  ubuntu: https://cloud-images.ubuntu.com/releases   # overrides the built-in (e.g. to a mirror)
  corp-images: https://images.corp.example.com       # custom remote
```

| Field (map key) | Type | Default | Rules |
| :-- | :-- | :-- | :-- |
| `<remote>` | string | — | `=~"^[a-zA-Z0-9_.-]+$"` (same charset as the remote part of `#RemoteAlias`). Declared in `#LXM_AUTHORING`; enforced by `#LXM_RESOLVED` (paired `#ImageRemoteName` / `#ImageRemoteNameInvalid` patterns) and by Go (`validateImageRemoteNames`, run from `ValidatePostMerge` and `MigrateManifest`), which reports the offending key with a precise message on every load and compile path. |
| `<url>` | string | — | `http://` or `https://` URL with a non-empty host. Scheme `https` required in production; `http` only for loopback hosts. Canonicalized (lowercase scheme+host, trailing `/` trimmed) before storage and comparison. Go-validated in `ValidatePostMerge`. |

Merge semantics: within an include chain `image_remotes` merges **key-wise** (overlay wins per
remote name); across sibling manifests identical `(name, url)` duplicates dedup silently and the
same name with a different canonical URL fails with **exit code 3** citing both files. Referencing
an undeclared remote in `image:` fails at plan time with **exit code 3**; disabling fetch
(`LXM_IMAGE_FETCH=0`) turns an uncached remote reference into the same plan-time error.

---

## 4. Mount Authoring & Security Rules

`lxm` supports four mount authoring styles, all of which normalize into canonical `#MountObjResolved` representations:

### 4.1 Authoring Styles
```yaml
# Style 1: String shorthand (host:container[:ro|rw])
mounts:
  - "/tmp/host-data:/var/data:rw"

# Style 2: Map shorthand (container: host)
mounts:
  /var/log: /tmp/host-logs

# Style 3: LXD-native object
mounts:
  - source: /tmp/host-data
    path: /var/data
    readonly: true

# Style 4: Mixed list
mounts:
  - "/tmp/host-data:/var/data"
  - source: /tmp/host-config
    path: /etc/app
```

### 4.2 Security Constraints (`#CleanMountPath`)
* **Absolute Source Paths**: Mount `source` paths must be absolute (`source: =~"^/"`).
* **ID Mapping (`shift`)**: Host mounts default to `shift: true` (activating dynamic Linux Kernel VFS idmapped mounts for containers and VirtioFS for VMs). Can be explicitly opted out (`shift: false`) for raw NFS/FUSE/socket passthrough on containers.

---

## 5. Container Power State & Status Rules

Declarative container status and power state are governed by `status` and `state`:

```yaml
schema: lxm/config/v2
name: dev-station
image: ubuntu:22.04
status: present    # present (default) | absent
state: running     # running (default) | stopped
```

* **Default Power State**: Containers with `status: present` default to power state `running` (`Step.Action: "start"`).
* **Explicit Stopped State**: Setting `state: stopped` renders a power state stop transition (`Step.Action: "stop"`).
* **Absent Status Invariant**: Declaring `status: absent` combined with an explicit `state` fails compilation (**exit code 3**). `image?` is optional under `status: absent`.

---

## 6. Wait & Readiness Configuration

Container readiness gates are configured via `wait`:

```yaml
wait:
  cloud_init: 10m
  network: 60s
  poll: 5s
  required: true    # fail-closed (default: true)
```

* **Boolean Shorthand**: Setting `wait: false` normalizes to `{required: false}`, converting readiness timeouts into soft warnings.
* **Fail-Closed Default**: `wait.required: true` (default) causes readiness timeouts to fail execution with **exit code 7** and skip recipe execution.
