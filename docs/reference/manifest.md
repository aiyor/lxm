# Manifest Reference

This page documents every field you can use in an `lxm/config/v2` manifest, what it does, and how the value is validated. It is the user-facing counterpart to the repository's `SPEC_MANIFEST.md` contract.

Every field shown below is verified against the shipped binary. Complete, compile-valid example manifests live in `docs/examples/` (at the repository root) and are re-checked by the docs CI gate on every pull request. Field snippets in this page are fragments for illustration; the complete examples are the copy-paste-correct files.

## What a manifest is

A manifest is a YAML file that declares the desired state of one container: *"a container named `X` should exist, be built from image `Y`, have these mounts, networks, users, and recipes."* lxm reads a manifest (or a directory of them), compares it against what is actually running in LXD, and reconciles the difference.

```yaml
schema: lxm/config/v2
name: dev-station
status: present
image: ubuntu:22.04
user: dev
groups: [dev]
```

## Schema surfaces

Manifest authoring is validated against two schema surfaces:

* **Authoring surface** — what you write. Accepts shorthands, file-local `vars:`, `~` and `{{ ... }}` templates, and the inheritance directives (`include`, `remove`, `replace`).
* **Resolved surface** — the strict, closed schema lxm compiles your manifest to before applying it. It rejects all shorthands, directives, and unknown keys, and enforces the security rules (absolute mount sources, clean mount destinations).

You almost never touch the resolved surface directly; `lxm compile` produces it from your authored manifest.

---

## Identity & lifecycle

### `schema`

The manifest schema version. Must be `lxm/config/v2` for the strict schema. If omitted, the file is treated as a legacy `lxm/config/v1` manifest: it still loads, but lxm prints a notice telling you to run `lxm compile` to migrate it. See [Migrating from lxm v1](../howto/migrate-v1.md).

```yaml
schema: lxm/config/v2
```

### `name`

The container name — the unique identity lxm manages. Required in a resolved manifest (a `base` manifest must **not** have one).

```yaml
name: dev-station
```

### `status`

Whether the container should exist. `present` (default) means "ensure it exists"; `absent` means "ensure it does not exist".

```yaml
status: present        # present (default) | absent
```

* `status: present` requires an `image`.
* `status: absent` combined with an explicit `state` fails compilation.
* An `absent` manifest does not need an `image`.

Complete example: [`absent-demo.yaml`](../examples/absent-demo.yaml).

```yaml
schema: lxm/config/v2
name: absent-demo
user: dev
status: absent
groups: [demo]
```

### `state`

The desired power state of a `present` container: `running` (default) or `stopped`.

```yaml
state: running         # running (default) | stopped
```

### `type`

The virtualization model: `container` (default) or `virtual-machine` (or authoring shorthand `vm`).

```yaml
type: virtual-machine  # container (default) | virtual-machine (shorthand: vm)
```

* Containers share the host Linux kernel via LXC namespaces and cgroups.
* Virtual machines are fully hardware-isolated instances running under QEMU/KVM.
* Changing `type` on an existing live instance triggers a recreate action (`delete` + `create`), protected by the `--force` flag.

### `limits`

Hardware resource allocations applicable to both containers and virtual machines:

```yaml
limits:
  cpu: 4               # Integer core count (VM vCPUs / container quota), or cpuset range "0-3"
  memory: 8GiB         # VM guest RAM size or container memory cgroup limit
  disk: 50GiB          # Root disk storage volume resize override
```

* `cpu`: Accepts an integer (e.g. `4`, `8`) or a cpuset string (e.g. `"0-3"`, `"0,2,4"`). Bare `"0"` is rejected.
* `memory`: Size with standard unit suffix (`512MiB`, `8GiB`).
* `disk`: Size with standard unit suffix. Reducing root disk size triggers a recreate action (guarded by `--force`).

### `vm`

Hypervisor and virtual firmware settings. These options are exclusive to virtual machines (`type: virtual-machine`):

```yaml
vm:
  boot_mode: uefi-secureboot  # "uefi-secureboot" | "uefi-nosecureboot" | "bios"
  secureboot: true            # Shorthand for boot_mode (mutually exclusive with boot_mode)
  hugepages: false            # Back VM memory with host HugeTLB pages (limits.memory.hugepages)
  raw_qemu: ""                # QEMU hypervisor CLI argument injection (raw.qemu)
```

* `boot_mode` / `secureboot`: Configures virtual UEFI/BIOS firmware. Defaults to `uefi-secureboot` for VMs.
* Modifying `vm` hypervisor settings on a running VM triggers a clean power restart transition (`stop` $\rightarrow$ `PUT` $\rightarrow$ `start`).

### `image`

The base image the instance is created from, as a LXD image reference: a hex fingerprint, a local
alias, or a `remote:alias`.

* **Fingerprint / bare alias** (local): the image must already be present in your LXD host; lxm
  refers to it by that exact reference.
* **`remote:alias`**: when the image is not already cached locally, lxm looks it up on the named
  remote (a simplestreams image server) and fetches it into the local store before creating or
  rebuilding the instance. The fetched image is tagged with a deterministic, **type-qualified**
  local alias (`<remote>/<alias>` for containers, `<remote>/<alias>/vm` for virtual machines),
  which is what create/rebuild payloads use. Once cached, the reference never re-fetches.

```yaml
image: ubuntu:24.04
```

The named remote resolves to a URL from `image_remotes:` (below) or the built-in remotes
(`ubuntu`, `ubuntu-daily`, `images`). Referencing an undeclared remote is a plan-time error
(exit 3); set `LXM_IMAGE_FETCH=0` to disable fetching and turn an uncached remote reference into
the same error.

### `image_remotes`

Fleet-scoped mapping of remote **name** to simplestreams **URL**, for the `remote:alias` image
form. Usually declared in a `_base.yaml` and inherited via `include`. A declaration overrides a
same-named built-in; across the fleet, identical `(name, url)` duplicates dedup silently and a
name with conflicting URLs fails (exit 3).

```yaml
image_remotes:
  corp-images: https://images.corp.example.com
```

URLs must be `https://` (or `http://` for loopback hosts) with a non-empty host; they are
canonicalized (lowercase scheme+host, trailing `/` trimmed) before comparison.

### `user`

The primary user lxm creates in the container. Defaults to `ubuntu`. lxm injects the user via cloud-init, adds it to the `sudo` group, and writes `LXM_USER` into `/etc/profile.d/lxm-env.sh`.

```yaml
user: dev
```

### `groups`

Group tags for fleet targeting. Containers can be selected with `--group` / `--exclude-group`. See [Selector algebra](cli.md#selector-algebra).

```yaml
groups: [dev, backend]
```

---

## Inheritance

Fleets usually share defaults. lxm merges manifests through a base file and the `include` directive, using *presence-wins* rules: an explicitly set field overrides the base value (even when set to an empty/zero value), while an omitted field inherits.

### `base`

Marks a file as a base manifest (shared defaults). A base file must **not** have `name` or `image`. Files whose names start with `_` must declare `base: true` or lxm refuses to load them.

```yaml
schema: lxm/config/v2
base: true
user: ubuntu
wait:
  cloud_init: 10m
  network: 60s
```

Complete example: [`_base.yaml`](../examples/_base.yaml).

### `include`

List of manifest files to merge as the base for this manifest. Paths are relative to the including file. Inheritance is depth-first; later files override earlier ones, and the leaf file's own fields override everything.

```yaml
schema: lxm/config/v2
include:
  - _base.yaml
name: dev-station
```

### `remove`

Removes specific items from an inherited list. Matching rules: `remove.mounts` matches by normalized container path, `remove.networks` by interface name, `remove.recipes` by exact script path, `remove.disks` by disk name. A `remove` entry that matches nothing fails compilation.

```yaml
remove:
  mounts:
    - /mnt/shared
  recipes:
    - recipes/common.sh
```

### `replace`

Replaces an inherited list wholesale instead of concatenating it.

```yaml
replace:
  networks:
    - name: eth0
      ipv4: 10.0.0.50
      parent: lxdbr0
```

Complete example for `include` + `remove` + `replace` + presence-wins `wait`: [`inheritance-demo.yaml`](../examples/inheritance-demo.yaml) inherits [`_inheritance-base.yaml`](../examples/_inheritance-base.yaml).

---

## Mounts

Host directories are made available inside the container as bind mounts. All styles normalize to the same object form. The resolved form is:

```yaml
mounts:
  - source: /tmp/projects     # absolute host path (required)
    path: /var/www/html       # absolute container path (required)
    readonly: false           # optional; default false
    recursive: true           # optional; default false
```

### Authoring styles

**Style 1 — string shorthand** (`host:container[:ro|:rw|:recursive]`):

```yaml
mounts:
  - "/tmp/host-data:/var/data:rw"
  - "/tmp/host-config:/etc/app:ro"
```

**Style 2 — map form** (`container: host`):

```yaml
mounts:
  /var/log: /tmp/host-logs
```

The map form is loadable and normalizes to the object form at load time. A map value may also be an object (with `source` and `path`); an explicit `path` inside the object overrides the map key.

Complete example: [`mounts-map.yaml`](../examples/mounts-map.yaml).

**Style 3 — object form:**

```yaml
mounts:
  - source: /tmp/projects
    path: /var/www/html
    readonly: true
    recursive: true
```

**Style 4 — mixed list:**

```yaml
mounts:
  - "/tmp/host-data:/var/data"
  - source: /tmp/projects
    path: /var/www/html
```

Complete example: [`mounts-demo.yaml`](../examples/mounts-demo.yaml).

### Security rules

* **Absolute sources.** In the resolved schema, mount `source` must start with `/`. Tilde (`~/...`) and `{{ .Vars.* }}` templates are expanded during authoring.
* **Clean destinations.** `path` values of `/`, `/proc`, `/sys`, and `/dev` are rejected during compilation (`exit 3`).
* **Host-side existence.** At `plan`/`apply` time the source directory must exist on the host, or lxm exits `3` (`config validation ... source path ... does not exist on host`).
* **ID mapping (`shift`).** lxm defaults host mounts to `shift: true`. For containers, this activates dynamic Linux Kernel VFS idmapping. For virtual machines, sharing is handled via VirtioFS. To disable shifting on containers (e.g. for raw NFS/FUSE/socket mounts), explicitly set `shift: false`.
* **Duplicate destinations.** Two mounts with the same container path are rejected after merge.

!!! warning

    Mounting a sensitive host directory (e.g. `/etc`, `/proc`, `/sys`, `/dev`, or `~/.ssh`) into a container you do not control gives that container's root access to the mounted data.

---

## Networks

Network interfaces are declared as a list. `name` defaults to `eth0`, `parent` to `lxdbr0`.

```yaml
networks:
  - name: eth0
    ipv4: 10.10.10.50
    parent: lxdbr0
```

Duplicate interface names are rejected after merge, and `ipv4` must parse as an IPv4 address.

---

## Virtual switches (`vswitches`)

Fleet-scoped declaration of LXD managed bridges that lxm creates, owns, and reconciles. Usually
declared in `_base.yaml` and inherited by every leaf via `include`; identical declarations across
loaded manifests are deduplicated, and conflicting ones fail with both file paths cited.

```yaml
vswitches:
  - name: vmbr0
    ipv4: 10.30.0.1/24     # required; gateway must be the first usable host (network .1)
    group: vms             # optional; network group for network_policy
    type: bridge           # optional; v1 only "bridge"
    driver: native         # optional; "native" | "openvswitch" (immutable after create)
    nat: true              # optional; default true
    internet: true         # optional; only meaningful with a group; default true
    ipv6: none             # optional; v1 only "none"
```

Validation (Go-side, exit 3): `ipv4` must parse as a CIDR whose address is the first usable host
(`10.10.50.1/16` is rejected — it is not the first host of `10.10.0.0/16`); prefix length must be
`/8`–`/29`; names must be unique; subnets must not overlap; `internet: false` requires a `group`.

An **ungrouped** vswitch is managed for addressing only — stock LXD open routing, no ACLs. Removing
a `group` later detaches the ACL and leaves it unmanaged. Removing a vswitch from the manifests
stops managing it; lxm never deletes networks.

---

## Network policy (`network_policy`)

Fleet-scoped, group-based traffic policy compiled deterministically into LXD network ACLs (`lxm-
<vswitch>` per grouped vswitch) with a `reject` default. Like `vswitches`, it is typically declared
in `_base.yaml`, unioned (and deduplicated) across all loaded manifests.

```yaml
network_policy:
  internal_cidrs:            # optional; ADDITIVE to the locked default internal set
    - 192.168.77.0/24
  allow:
    - from: vms              # required; a group with ≥1 vswitch
      to: services           # required; a group with ≥1 vswitch
      direction: both        # "both" (default, mutual) | "egress" (one-way initiation)
```

* `allow` entries referencing a group with no vswitches fail with exit 3.
* Identical duplicate `allow` entries and `internal_cidrs` are deduplicated silently; the same
  `(from, to)` pair declared with differing `direction` fails with exit 3.
* An `allow` with `from == to` is a no-op (intra-group is already allowed) and is flagged by a plan
  warning.
* `internal_cidrs` adds operator-declared networks to the **internal set** that `internet: true`
  groups may not reach — this is the remedy for the [non-RFC1918 host-address caveat](../howto/configure-networking.md#internal_cidrs--declaring-more-internal-space).

See [Configuring Networking → Managed virtual switches & network segmentation](../howto/configure-networking.md#managed-virtual-switches--network-segmentation) for the full model,
the `parent:` join pattern, and the caveats.

---

## Data disks (`disks`)

Additional storage-pool volumes attached to **virtual machines** (VM-only in v1 — declaring `disks:`
on a `type: container` fails with `exit 3`). Each disk is one of four combinations of two
orthogonal axes:

* **Mode** — filesystem (guest-mounted) vs block (raw device) — selected by `path` presence.
* **Ownership** — lxm-managed (lxm provisions the volume) vs external (a pre-existing custom volume)
  — selected by `source` presence.

```yaml
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
| `name` | string | — (required) | `^[a-z][a-z0-9-]{0,30}$`; not `root`. LXD device key `disk-<name>`. |
| `size` | string | — | `#ByteSize`. **Required** when `source` unset (managed). **Forbidden** when `source` set (external). |
| `pool` | string | `"default"` | Storage pool name. |
| `path` | string | — | Guest mount path. **Presence ⇒ filesystem mode**; absence ⇒ block mode. |
| `source` | string | — | Pre-existing custom volume name in `pool`. **Presence ⇒ external ownership**. |
| `readonly` | bool | `false` | Maps to device `readonly: "true"`. |
| `bus` | string | `"virtio-scsi"` | `"virtio-scsi" \| "virtio-blk" \| "nvme"` → `io.bus`. **Block mode only**; rejected with `path`. The default `virtio-scsi` is LXD's own bus default and is omitted from the device map. |

Managed volumes are named `<instance>-<name>`; their `size` is managed via the storage-volume API
(`size` never appears on the LXD device map). External volumes are probed at plan time — a missing
external volume fails with `exit 4`. Growing a managed volume is an online update (no restart);
shrinking is rejected at plan time (`exit 3`). Removing a disk from the manifest detaches the device
only — lxm never deletes storage volumes.

See `STORAGE-SPEC.md` (repository root) for the authoritative feature specification.

## Recipes

Recipes are provisioning scripts that run inside the container during `apply`. The supported v2 form is a list of groups, each with an optional `run_as` user (default `root`) and a non-empty `scripts` list:

```yaml
recipes:
  - run_as: root
    scripts:
      - recipes/bootstrap.sh
  - run_as: dev
    scripts:
      - recipes/user-setup.sh
```

* **Idempotency.** After a script runs, lxm stores a SHA-256 hash of its content in the container's config (`user.lxm.recipe.*.hash`) and skips it on later applies unless the file changed or `--force` is passed.
* **Snapshots.** Before the first recipe runs, lxm takes a snapshot named `user.lxm.snap.<container>-<timestamp>` (unless the recipe metadata disables it). `lxm snapshot gc` cleans these up.
* **Metadata.** A recipe can also be a YAML file (`lxm/recipe/v1`) declaring `run_as`, `env`, `sudo`, `snapshot`, `retries`, and a `scripts` list. See the `config/recipes/` examples in the repository.
* **Shorthands.** The authoring schema also accepts a bare script-path string (`- recipes/bootstrap.sh`), a `root:` shorthand (`- root: [recipes/setup.sh]`), and legacy `scripts:`-only groups (common in v1 configs). All normalize to the object form at load time, with `run_as` defaulting to `root`; `lxm compile` emits the object form.

Complete example: [`recipes-demo.yaml`](../examples/recipes-demo.yaml).

---

## Cloud-init

lxm composes the container's `user.user-data` from three sources, in this order: `cloud-init-include` files, then either an inline `cloud-init` string or a `cloud-init-file` path (never both), then lxm's automatic user configuration.

### `cloud-init-include`

List of cloud-init fragment files (relative to the manifest) to merge in.

```yaml
cloud-init-include:
  - cloud-init/base-cloud-config.yaml
```

### `cloud-init`

An inline `#cloud-config` body as a YAML block.

```yaml
cloud-init: |
  packages:
    - ripgrep
    - jq
```

### `cloud-init-file`

A path to a `#cloud-config` file, as an alternative to the inline form.

```yaml
cloud-init-file: cloud-init/local.yaml
```

`cloud-init` and `cloud-init-file` are mutually exclusive; setting both fails compilation (`exit 3`).

### `network-config`

An inline network configuration for cloud-init.

```yaml
network-config: |
  version: 2
  ethernets:
    eth0:
      dhcp4: true
```

!!! note

    lxm passes the field through to LXD as `user.network-config` on create and on config update, where cloud-init applies it as the instance's network configuration (cloud-init network config v2, no `#cloud-config` header). The `networks:` list creates the NIC devices; `network-config` configures them inside the container.

Complete example: [`cloud-init-demo.yaml`](../examples/cloud-init-demo.yaml) includes [`cloud-init/base-cloud-config.yaml`](../examples/cloud-init/base-cloud-config.yaml).

---

## Wait & readiness

Readiness gates delay recipe execution until cloud-init finishes or the container has a network address.

```yaml
wait:
  agent: 2m           # default 2m for VMs (waits for guest lxd-agent handshake)
  cloud_init: 10m     # default 10m
  network: 60s        # default 60s
  poll: 5s            # default 5s
  required: true      # default true (fail-closed)
```

* `wait.agent`: Polling deadline for guest `lxd-agent` over virtio channel in virtual machines (default: `2m`).
* `wait: true` / `wait: false` is accepted as a shorthand for `required: true` / `required: false` (legacy v1 style).
* With `required: true` (the default), a readiness timeout is a hard failure: `exit 7` and recipes are skipped.
* With `required: false`, timeouts degrade to warnings and recipes still run.
* The `--wait` flag forces `required: true`; `LXM_WAIT_REQUIRED` can override it. See [Environment Variables](environment-variables.md).

---

## Variables & templates

Manifest values can be parameterized. Templates are expanded with anchored replacement during compilation; unbound variables are a hard error (`exit 3`).

### `vars`

File-local variables, reusable across the file:

```yaml
vars:
  workspace: /tmp/projects

mounts:
  - source: "{{ .Vars.workspace }}"
    path: /workspace
```

### Environment and identity templates

| Template | Expands to |
|---|---|
| `{{ .Vars.KEY }}` | A file-local `vars:` value |
| `{{ .Env.NAME }}` | The host environment variable `NAME` at compile time (unbound → `exit 3`) |
| `{{ .Name }}` | The container name |
| `{{ .Group }}` | The container's first group |

To emit a literal `{{` or `}}`, escape it as `\{{` / `\}}`.

---

## Security posture

These fields control the container's user and SSH surface (all opt-in, all default `false`):

### `sudo`

Passwordless sudo for the manifest `user`.

```yaml
sudo: true
```

### `inject_ssh_keys`

Automatically inject your host's public keys (`~/.ssh/*.pub`) into the container user's `authorized_keys`.

```yaml
inject_ssh_keys: true
```

### `ssh_keys`

Explicit list of public keys to install, instead of discovering them from the host.

```yaml
ssh_keys:
  - "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAEXAMPLE... user@host"
```

!!! warning

    `inject_ssh_keys` and `ssh_keys` give the container passwordless access from the listed keys. Only enable them for fleets you trust. The v2 defaults for `sudo` and `inject_ssh_keys` are `false`; legacy v1 configs that relied on the old implicit behavior are flagged during `lxm compile`.

---

## Validation summary

| Rule | Consequence |
|---|---|
| Unknown top-level key in a v2 manifest | `exit 3` |
| `status: present` without `image` | `exit 3` |
| `status: absent` with `state` | `exit 3` |
| `vm.secureboot` and `vm.boot_mode` both set | `exit 3` |
| Bare `"0"` CPU count | `exit 3` |
| Floating point byte size (e.g. `1.5GB`) | `exit 3` |
| Base manifest with `name` or `image` | `exit 3` |
| `_`-prefixed file without `base: true` | `exit 3` |
| Mount source that is not absolute (resolved) | `exit 3` |
| Mount destination `/`, `/proc`, `/sys`, `/dev` | `exit 3` |
| Duplicate mount path or network name | `exit 3` |
| Duplicate disk name / `disk` named `root` / mount–disk path collision | `exit 3` |
| `disks:` on a `type: container` | `exit 3` |
| Managed disk (`disks`) without `size`, or external disk (`source`) with `size` | `exit 3` |
| Disk `bus` set in filesystem mode (`path`) | `exit 3` |
| `remove` matching nothing | `exit 3` |
| `cloud-init` and `cloud-init-file` both set | `exit 3` |
| Unbound `{{ .Vars.* }}` / `{{ .Env.* }}` | `exit 3` |
| Circular `include` chain | `exit 3` |

## Related

* [CLI Reference](cli.md) — how manifests are targeted (`plan`, `apply`, `diff`).
* [Results & Exit Codes](results-and-exit-codes.md) — what `exit 3` looks like in JSON.
* [Authoring Manifests](../howto/author-manifests.md) — the task-focused guide for writing fleets.
