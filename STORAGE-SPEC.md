# STORAGE-SPEC — Additional VM Data Disks (`disks:`)

**Feature**: `disks:` — declarative management of additional storage volumes attached to virtual
machines, in **filesystem** mode (guest-mounted) or **block** mode (raw device), each either
**lxm-managed** (lxm provisions the volume) or **external** (a pre-existing custom volume referenced
by name).

**Status**: IMPLEMENTED. Verified against a live LXD 6.9 daemon (ZFS driver) during design review —
the LXD constraints below are pinned to LXD 6.9 at implementation time.

**Schema**: `lxm/config/v2`. The canonical manifest-schema reference (the field tables) lives in
[`SPEC_MANIFEST.md`](SPEC_MANIFEST.md); keep the two in sync when either changes.

---

## 1. Overview & Goals

`lxm` manages exactly one disk per instance — the root volume (`limits.disk` →
`devices.root.size`). Historically every non-root `type: disk` device was conflated with a host-path
mount. This feature adds a top-level `disks:` section that provisions **additional storage-pool
volumes** attached to virtual machines, without touching the existing `mounts:` semantics.

1. Attach one or more additional disks to a `type: vm` instance, declaratively and idempotently.
2. Two attachment modes, orthogonal to volume ownership (§3):
   * **Filesystem mode** — a custom `filesystem` volume mounted by the guest `lxd-agent` at a guest
     path (same mechanism as the root disk).
   * **Block mode** — a raw block device (e.g. `/dev/sdb`) exposed to the guest for custom
     partitioning (databases, Ceph OSDs, …).
3. Attach **pre-existing external custom volumes** by name, in either mode.
4. Preserve the plan-first contract: all disk mutations appear in `lxm plan` diffs with
   deterministic, non-destructive update semantics.

### Non-Goals (v1)

* `disks:` on containers (VM-only; compile-time rejection, §8).
* Shared filesystem volumes between instances via LXD `sharing` (future work).
* Volume snapshots / per-disk IOPS-throughput `limits` (future work).
* Deleting detached volumes (§7.5).

---

## 2. Verified LXD constraints (these drive the design)

All constraints below were verified empirically against a live LXD 6.9 daemon (ZFS pool) during the
design review.

| # | Constraint (LXD 6.9 behavior) | Consequence for this feature |
| :- | :-- | :-- |
| C1 | Non-root disk devices **require** a `source` — `Non root disk devices require the "source" property`. | Every emitted disk device carries `source`. No anonymous inline volumes; managed volumes are pre-created via the storage API. |
| C2 | `size` is forbidden on non-root device maps — `Only the root disk may have a size quota`. | `size` lives exclusively on `api.StorageVolume.Config["size"]`, never on the device map. Live size is read from storage-volume metadata. |
| C3 | Custom **filesystem** volumes require a `path` when attached to an instance — `Custom filesystem volumes require a path to be defined`. | External-filesystem mode must declare `path`. |
| C4 | Custom **block** volumes cannot be attached to containers — `Custom block volumes cannot be used on containers`. | Block mode is inherently VM-only (matches the VM-only v1 scope). |
| C5 | Block volumes cannot be shrunk — `Block volumes cannot be shrunk: Cannot be shrunk`. | Managed-disk shrink is rejected at plan time (§7.4). |
| C6 | Filesystem volumes **can** be shrunk at the LXD layer (ZFS), but shrinking a live guest filesystem is destructive. | v1 rejects all managed-disk shrink (§7.4). |
| C7 | **No "in use" resize lock.** Growing (and filesystem-shrinking) volumes while attached to a *running* VM succeeds on ZFS/6.9. LXD's "still in use" guard applies to *delete/rename*, not resize. | Online grow is the primary grow path — no hot-unplug sequencing (§7.4, §10). |
| C8 | `custom_block_volumes` API extension gates block-volume support. | Block-mode disks require the extension; missing ⇒ exit 4 (§9). |

---

## 3. Manifest Schema & Mode × Ownership Matrix

> **Schema authority.** The canonical field tables live in [`SPEC_MANIFEST.md`](SPEC_MANIFEST.md)
> (§3.9), mirroring the CUE schemas in `internal/config/schemas/v2.cue`. The table below is a
> self-contained summary for this feature spec.

The two axes are **orthogonal**:

* **Mode** = filesystem vs block — selected by `path` presence/absence.
* **Ownership** = managed (lxm-created) vs external (pre-existing) — selected by `source`
  absence/presence.

```yaml
schema: lxm/config/v2
name: db-vm
type: vm
image: ubuntu:24.04

limits:
  disk: 30GiB          # root disk — unchanged

disks:
  - name: data                    # filesystem (managed): path present, source derived
    size: 100GiB
    path: /var/lib/postgresql

  - name: wal                     # block (managed): no path
    size: 20GiB
    bus: nvme

  - name: shared-fs               # filesystem (external): source + path
    source: web-root-vol
    pool: fast-pool
    path: /srv/www
    readonly: true

  - name: shared-block            # block (external): source, no path
    source: ceph-osd-vol
    pool: fast-pool
```

| Mode | `source` | `path` | `size` | Volume content type | LXD device (key `disk-<name>`) |
| :-- | :-- | :-- | :-- | :-- | :-- |
| **FS (managed)** | derived `<inst>-<name>` | set | required | `filesystem` | `{type: disk, pool, source, path, readonly?}` |
| **FS (external)** | set | set | forbidden | `filesystem` | `{type: disk, pool, source, path, readonly?}` |
| **Block (managed)** | derived `<inst>-<name>` | — | required | `block` | `{type: disk, pool, source, io.bus, readonly?}` |
| **Block (external)** | set | — | forbidden | `block` | `{type: disk, pool, source, io.bus, readonly?}` |

Two hard invariants:

1. **Every emitted disk device has `source`** (C1) — derived `<instance>-<name>` for managed, the
   declared name for external.
2. **`size` never appears on a device map** (C2) — it is read/written via the storage-volume API
   only.

### 3.1 Field reference (summary)

| Field | Type | Default | Rules |
| :-- | :-- | :-- | :-- |
| `name` | string | — (required) | `=~"^[a-z][a-z0-9-]{0,30}$"`, not `root`. Device key `disk-<name>`. |
| `size` | string | — | `#ByteSize`. **Required** when `source` unset (managed). **Forbidden** when `source` set (external). |
| `pool` | string | `"default"` | Storage pool name. |
| `path` | string | — | Guest mount path (`#CleanMountPath`). Presence ⇒ filesystem mode; absence ⇒ block mode. |
| `source` | string | — | Pre-existing custom volume name in `pool`. Presence ⇒ external ownership. |
| `readonly` | bool | `false` | → device `readonly: "true"`. |
| `bus` | string | `"virtio-scsi"` | `"virtio-scsi" \| "virtio-blk" \| "nvme"` → `io.bus`. **Block mode only**; rejected with `path`. |

---

## 4. Go Config Model (`internal/config`)

```go
type DiskConfig struct {
    Name     string `yaml:"name"`
    Size     string `yaml:"size,omitempty"`
    Pool     string `yaml:"pool,omitempty"`
    Path     string `yaml:"path,omitempty"`
    Source   string `yaml:"source,omitempty"`
    Readonly bool   `yaml:"readonly,omitempty"`
    Bus      string `yaml:"bus,omitempty"`
}
```

* `Config.Disks []DiskConfig`; no custom `UnmarshalYAML` (plain closed-object list).
* `RemoveDirective.Disks []string` / `ReplaceDirective.Disks []DiskConfig` — `remove.disks` matches
  by `name`; non-matching ⇒ exit 3.
* **Normalization** (post-merge, in `LoadConfig`): materialize `pool` → `"default"`, `bus` →
  `"virtio-scsi"` (block mode only), and derived `source` → `<instance>-<name>` for managed disks.
  Managed disks keep their declared `size`; external disks clear `size`. `readonly` is a zero-value
  bool resolved by the CUE `*false` default (no explicit assignment needed).
* **VM-only guard**: `disks:` on a `container` ⇒ compile error, exit 3.
* **`ValidatePostMerge`**: `name` required, `!= "root"`, unique; the union of `mounts[].path` and
  filesystem `disks[].path` (cleaned) contains no duplicates.

---

## 5. Reconciliation (`internal/plan`)

### 5.1 Live-state reconstruction

* `getLiveMounts` re-scoped by **key prefix**: only devices whose key starts with `mount`
  (`mount%d` and legacy `mount-<path>`) and `type == "disk"` are read as mounts. This stops foreign
  (operator hand-added) disk devices and `disk-*` data disks from being misread as mounts.
* New `getLiveDisks`: devices with key prefix `disk-` and `type == "disk"` reconstruct `DiskConfig`
  from the device map (`pool`, `path`, `source`, `readonly`, `io.bus`); **`size` is read from
  storage-volume metadata**, never the device map (C2).
* `fetchLiveSnapshots` (`cmd/lxm/commands.go`) additionally queries custom volumes (via the
  `StorageService`) for every pool referenced by a loaded manifest and returns them **as a dedicated
  parameter** to `Reconciler.Compute` (pool → volume-name → `api.StorageVolume`). Volumes are not
  carried on `InstanceSnapshot`, so they survive an **empty instance list** — the create path probes
  external volumes on a fresh LXD with zero live instances — while keeping `Compute` a pure offline
  function.
* **Foreign devices**: non-root `type: disk` devices with no `mount*` / no `disk-*` prefix are
  ignored by both live readers and preserved verbatim by `buildInstancePut`.

### 5.2 Diff rules

Identity = `name`. Order-insensitive comparison (mirrors `areMountsEqual`).

| Change | FieldDiff | Action |
| :-- | :-- | :-- |
| disk added | `disks[<name>]` (old `∅`) | `update` (or `create`) + `VolumeOps{create}` for managed |
| disk removed | `disks[<name>]` (new `∅`) | `update` (detach only; volume NOT deleted, §7.5) |
| `size` grow (managed) | `disks[<name>].size` | `update` + `VolumeOps{grow}` — online, no restart (C7). Compared by parsed bytes, so reworded-equal sizes (`10GiB` vs `10737418240`) never produce a perpetual diff. |
| `size` shrink (managed) | `disks[<name>].size` | **plan-time config error, exit 3** (§7.4) |
| `pool` change (external) | `disks[<name>].pool` | `update` + restart if running (device re-points to another pool; existence probed at plan time) |
| `pool` change (managed) | `disks[<name>].pool` | `VolumeOps{create}` in new pool + detach/attach (old volume orphaned) + restart if running |
| `path` change (fs→fs) | `disks[<name>].path` | `update` + restart if running (agent remount) |
| `readonly` change | `disks[<name>].readonly` | `update` (device property) |
| `bus` change (block) | `disks[<name>].bus` | `update` + restart if running (QEMU re-plug) |
| `source` change (external) | `disks[<name>].source` | `update` + restart if running |
| filesystem ⇄ block switch | `disks[<name>].path` | **plan-time config error, exit 3** — volume name+content type are fixed per pool; re-provision manually |
| external `size` omitted | — | never diffed (unmanaged) |

Note: **instance `recreate` does not resize custom volumes** (they persist across rebuild), so
disk-level changes are never satisfied by an instance rebuild.

### 5.3 Payload construction

* `buildInstancesPost` / `buildInstancePut` emit `devices["disk-<name>"]` per §3, always carrying
  `source` and never `size`; after mounts and networks.
* Managed volumes are provisioned by `Step.VolumeOps` (§10) *before* instance create / device
  attach.

---

## 6. CUE Schema (`internal/config/schemas/v2.cue`)

```cue
#DiskObjAuthoring: close({
	name:      string & =~"^[a-z][a-z0-9-]{0,30}$" & != "root"
	size?:     #ByteSize
	pool?:     string | *"default"
	P1="path"?:     #CleanMountPath
	S1="source"?:   string
	readonly?: bool | *false
	B1="bus"?:      "virtio-scsi" | "virtio-blk" | "nvme"

	// size is FORBIDDEN when the disk is external (source set)
	if S1 != _|_ && size != _|_ { _|_ }
	// bus is FORBIDDEN in filesystem mode (path set)
	if P1 != _|_ && B1 != _|_ { _|_ }
})

#DiskObjResolved: close({
	name:      string & =~"^[a-z][a-z0-9-]{0,30}$" & != "root"
	size?:     #ByteSize
	pool:      string
	path?:     #CleanMountPath
	source?:   string
	readonly?: bool | *false
	bus?:      "virtio-scsi" | "virtio-blk" | "nvme"
})
```

Notes:

* `bus` has **no default** in CUE; the Go normalizer materializes `"virtio-scsi"` for block-mode
  disks only. `path` and `source` are **not** mutually exclusive (external filesystem volumes carry
  both) — the draft's `path XOR source` rule is gone.
* The "size required when managed" rule is enforced **Go-side** in `LoadConfig` normalization (CUE
  cannot express `== _|_` in this schema; the codebase's CUE guards use the `!= _|_` pattern only).
* `#LXM_AUTHORING` gains `disks?: [...#DiskObjAuthoring]` and `remove.disks` / `replace.disks`;
  `#LXM_RESOLVED` gains `disks?: [...#DiskObjResolved]`.

---

## 7. Locked Behavioral Rules

### 7.1 Unmanaged root disk is unchanged
`limits.disk` keeps owning `devices.root`. `disks[].name` may not be `root`.

### 7.2 No interference with `mounts`
Mount keys stay `mount%d`; disk keys are `disk-<name>`; live reconstruction is prefix-partitioned
(§5.1). Duplicate cleaned paths across `mounts` + filesystem `disks` ⇒ exit 3.

### 7.3 Pool change & mode switch
External pool change re-points the device. Managed pool change provisions a new volume in the new
pool (old volume orphaned). Filesystem ⇄ block switch is a config error (exit 3): the volume name is
fixed per pool, so re-provisioning with a different content type requires manual volume disposal.

### 7.4 Grow online; shrink rejected
* **Grow** (managed): `VolumeOps{grow}` online, no restart (C7). Driver-specific resize rejection
  surfaces as a retryable LXD error (exit 4).
* **Shrink** (managed): plan-time config error (exit 3). Block volumes hard-reject shrink (C5);
  filesystem shrink is destructive (C6); and instance recreate does not resize custom volumes.

### 7.5 Removal semantics (locked)
Removing a disk from the manifest **detaches the device only**. `lxm` never deletes storage
volumes, in any code path. Orphaned-volume cleanup is a future `lxm disk gc` concern.

### 7.6 External volume existence check
`lxm plan` probes external volumes via the storage API; a missing volume is a plan-time error,
exit 4 (`LXD_ERROR`):
`external volume "<pool>/<source>" referenced by disk "<name>" of instance "<name>" does not exist`.
A same-name volume with the **wrong content type** (e.g. a block volume adopted as filesystem) is
detected at apply time (`VolumeOps` create-or-adopt, exit 4) rather than plan time — the content
type is not probed at plan time in v1.

---

## 8. VM-only guard

`len(conf.Disks) > 0 && conf.Type == "container"` ⇒ compile error, exit 3:
`field "disks" is only supported for type: virtual-machine (instance "<name>")`. Block mode is
further enforced by LXD itself (C4); filesystem mode would work on containers when this guard is
relaxed in a future release (the schema is type-agnostic).

---

## 9. Service Extensions (`internal/lxd`)

```go
type StorageService interface {
    GetStoragePoolVolume(pool, volType, name string) (*api.StorageVolume, string, error)
    GetStoragePoolVolumes(pool string) ([]api.StorageVolume, error)
    CreateStoragePoolVolume(pool string, vol api.StorageVolumesPost) error
    UpdateStoragePoolVolume(pool, volType, name string, vol api.StorageVolumePut, etag string) error
    DeleteStoragePoolVolume(pool, volType, name string) error // reserved for future `lxm disk gc`
}
```

* Volume operations return asynchronous LXD `Operation`s and **must** be awaited (`op.Wait()`),
  matching the existing `waitOpContext` pattern.
* Managed volumes: `api.StorageVolumesPost{Name, Type: "custom", ContentType: "filesystem"|"block",
  StorageVolumePut: {Config: {"size": …}}}`.
* Block mode gated on `HasExtension("custom_block_volumes")`; a non-default `io.bus`
  (`nvme`/`virtio-blk`) additionally requires `disk_io_bus` / `disk_io_bus_virtio_blk`; missing ⇒
  exit 4.
* `internal/lxd/fake.go` gains in-memory volume storage + `StorageService` methods for tests.

---

## 10. Execution (`internal/apply`)

1. `Step.VolumeOps []VolumeOp{Op: create|grow, Pool, Name, ContentType, Size}`.
2. VolumeOps execute as **Phase 0** (before network steps, before instance steps), idempotently:
   `create` no-ops if a volume of the right content type exists (grow-if-smaller); a same-name
   volume with a *different* content type is an error. `grow` requires the volume to exist and
   grows only when desired > live. **Dry-run skips Phase 0 entirely** (no volume mutation).
3. Disk device add/remove/repoint rides `PUT /1.0/instances/{name}` with ETag re-verification —
   LXD hotplugs virtio disks on running VMs.
4. `PowerTransition: "restart"` for disk `path`/`pool`/`source`/`bus` changes on running VMs reuses
   the existing restart plumbing (a pool change detaches a volume in one pool and attaches a fresh
   one in another, which invalidates the guest mount/device).

---

## 11. Result Envelope & Exit Codes

* Disk diffs appear as regular `FieldDiff` entries (`disks[data].size` …); `VolumeOps` are part of
  the owning step — no envelope change.
* Exit codes: manifest/policy violations ⇒ 3 (`CONFIG_ERROR`); missing external volume / missing
  disk extensions (`custom_block_volumes`, `disk_io_bus`, `disk_io_bus_virtio_blk`) / API errors ⇒ 4
  (`LXD_ERROR`). No new codes.

---

## 12. Test Plan

| Area | Cases |
| :-- | :-- |
| `config` | `disks` parsing; defaults materialization; size guards; bus-with-path rejection; container guard; duplicate name; mount-path collision; include/remove/replace directives; resolved round-trip. |
| `plan` | create payload device shape (4 modes); add/remove/update diffs; grow → `VolumeOps{grow}`; shrink → exit 3; pool/mode-switch semantics; restart flags (`path`/`pool`/`source`/`bus`); reworded-equal size no-diff; external volume present on empty live map; `getLiveMounts` no longer sees `disk-*`; foreign device preserved; live size from volume metadata. |
| `apply` | VolumeOps Phase-0 ordering; idempotent create/grow; ETag conflict on concurrent change; dry-run no volume mutation; external volume missing ⇒ exit 4; disk extension missing ⇒ exit 4. |
| `lxd/fake` | volume CRUD (filesystem + block); grow no-op when live ≥ desired. |
