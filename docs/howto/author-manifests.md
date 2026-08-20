# Authoring Manifests

This guide shows you how to structure a fleet of lxm manifests: what goes in a container manifest, how to share defaults with a base manifest and inheritance, and how to shape inherited lists with `remove` and `replace`. If you only write one container, start with the [Quick Start](../getting-started/quickstart.md) instead; this page is for fleets of several containers.

## Prerequisites

* lxm installed ([Installation](../getting-started/installation.md))
* A working LXD host
* Familiarity with the [manifest fields](../reference/manifest.md)

## 1. Scaffold a fleet directory

`lxm init` creates the two files that make up a minimal fleet:

```text
$ lxm init
Initialized lxm fleet in .:
  - _base.yaml
  - config/dev.yaml
```

* **`_base.yaml`** holds the shared defaults every container inherits.
* **`config/dev.yaml`** declares your first container.

```yaml
# _base.yaml
schema: lxm/config/v2
base: true
user: ubuntu
wait:
  cloud_init: 10m
  network: 60s
```

```yaml
# config/dev.yaml
schema: lxm/config/v2
include:
  - ../_base.yaml
name: dev-station
status: present
image: ubuntu:22.04
groups: [dev]
```

Add more containers by adding more files under `config/`, one manifest per container. Each file is a complete declaration of that one container's desired state.

## 2. Share defaults with a base manifest

A file marked `base: true` is never a container itself — it is a bundle of inherited defaults. A base manifest must **not** set `name` or `image` (both are per-container facts):

```yaml
# config/_base.yaml
schema: lxm/config/v2
base: true
user: dev
wait:
  cloud_init: 10m
  network: 60s
inject_ssh_keys: true
```

Files whose names start with `_` (like `_base.yaml`) are treated as base files: lxm skips them during directory discovery and refuses to load one that does not declare `base: true`.

A container manifest pulls the base in with `include` and only declares what differs:

```yaml
# config/web.yaml
schema: lxm/config/v2
include:
  - _base.yaml
name: web-01
status: present
image: ubuntu:22.04
groups: [web]
```

Because `include` paths resolve relative to the including file, `config/web.yaml` includes `config/_base.yaml` as `_base.yaml` (same directory). From `config/dev.yaml` the base at the fleet root is `../_base.yaml`.

## 3. Understand how overrides merge

When a container manifest and its base both set the same scalar field, the **leaf wins — even when it sets an empty value**. An explicitly set field always overrides, which is why an explicit empty value is different from an omitted one:

```yaml
# base sets user: dev
# leaf sets user: ""   -> leaf wins, user is cleared
```

Omitted fields inherit; explicitly set fields (including explicit zeros) override. This is the single most important rule to remember when a container is not inheriting what you expect.

Lists behave differently: `mounts`, `networks`, and `recipes` **concatenate** by default (base first, then leaf) unless you use `remove` or `replace` (next step).

## 4. Shape inherited lists with `remove` and `replace`

Because lists concatenate, a leaf can end up with base mounts it does not want. Use the directives:

```yaml
# config/web.yaml
schema: lxm/config/v2
include:
  - _base.yaml
name: web-01
status: present
image: ubuntu:22.04
groups: [web]
remove:
  mounts:
    - /mnt/shared          # drop the base's /mnt/shared mount
  recipes:
    - recipes/common.sh    # drop one inherited recipe script
replace:
  networks:                # discard ALL inherited networks, use only these
    - name: eth0
      ipv4: 10.10.10.11
      parent: lxdbr0
```

* `remove.mounts` matches by normalized container path.
* `remove.networks` matches by interface name.
* `remove.recipes` matches by exact script path.
* A `remove` entry that matches nothing fails compilation with `exit 3` — it is a signal you are pruning something that is not there.

A complete, compile-valid example of this pattern (plus a presence-wins `wait` override) is `inheritance-demo.yaml`, which inherits `_inheritance-base.yaml`:

```yaml
# _inheritance-base.yaml
schema: lxm/config/v2
base: true
user: dev
wait:
  cloud_init: 10m
  required: true
mounts:
  - source: /srv/shared
    path: /mnt/shared
networks:
  - name: eth0
    ipv4: 10.0.0.5
    parent: lxdbr0
recipes:
  - run_as: root
    scripts:
      - recipes/common.sh
```

```yaml
# inheritance-demo.yaml
schema: lxm/config/v2
include:
  - _inheritance-base.yaml
name: inheritance-demo
status: present
image: ubuntu:24.04
groups: [demo]
remove:
  mounts:
    - /mnt/shared
  recipes:
    - recipes/common.sh
replace:
  networks:
    - name: eth0
      ipv4: 10.0.0.50
      parent: lxdbr0
wait:
  cloud_init: 5m
  required: false
```

After resolution this container has: no mounts, one network at `10.0.0.50`, no recipes, and `wait: {cloud_init: 5m, required: false}` — the leaf's `wait` fields override the base's, and the base's `10m`/`required: true` are gone.

## 5. Authoring Virtual Machines (VMs)

To declare a hardware-isolated Virtual Machine instead of a container, set `type: virtual-machine` (or authoring shorthand `type: vm`):

```yaml
# config/node01.yaml
schema: lxm/config/v2
include:
  - _base_vm.yaml
name: k8s-node-01
status: present
image: ubuntu:24.04

limits:
  cpu: 8               # 8 dedicated vCPUs
  memory: 16GiB        # 16 GiB guest RAM
  disk: 100GiB         # 100 GiB root disk

vm:
  boot_mode: uefi-secureboot

wait:
  agent: 2m            # wait for guest lxd-agent handshake
```

* **Hardware Limits**: `limits.cpu`, `limits.memory`, and `limits.disk` allocate physical resources.
* **Firmware & QEMU**: The `vm:` block controls UEFI/BIOS firmware and QEMU arguments.
* **Agent Handshake**: `wait.agent` ensures `lxm` waits for the guest `lxd-agent` over virtio channel before running recipes or network gates.

## 6. Authoring Virtual Switches & Network Policy

To manage software-defined virtual switches (Linux bridges or multi-node OVN overlay switches) and traffic policies across your fleet, declare `vswitches:` and `network_policy:` (typically in `_base.yaml`):

```yaml
# _base.yaml
schema: lxm/config/v2
base: true

vswitches:
  # Linux bridge for local communication
  - name: vmbr0
    type: bridge
    ipv4: 10.30.0.1/24
    group: internal

  # Distributed OVN overlay virtual switch across cluster nodes
  - name: ovnbr0
    type: ovn
    parent: lxdbr0       # uplink parent bridge
    ipv4: 10.60.0.1/24
    group: services
    mtu: 1442

network_policy:
  allow:
    - from: internal
      to: services
      direction: both
```

* **`vswitches`**: Declares managed bridges or OVN overlay networks. `parent:` is required on `type: ovn` to specify the uplink network.
* **`network_policy`**: Groups communicate freely within their group; inter-group traffic is denied by default unless permitted by an `allow` rule.
* **Fleet-scoped**: `vswitches` and `network_policy` are unioned across all manifests in your fleet.

## 7. Validate your manifests

Before applying anything, confirm every file compiles and preview what it would do:

```text
$ lxm compile docs/examples/
Successfully compiled 8 manifest(s):
  - docs/examples/.lxm/compiled/absent-demo.yaml
  - docs/examples/.lxm/compiled/cloud-init-demo.yaml
  - docs/examples/.lxm/compiled/dev-station.yaml
  - docs/examples/.lxm/compiled/inheritance-demo.yaml
  - docs/examples/.lxm/compiled/mounts-demo.yaml
  - docs/examples/.lxm/compiled/mounts-map.yaml
  - docs/examples/.lxm/compiled/ovn-network-demo.yaml
  - docs/examples/.lxm/compiled/recipes-demo.yaml
```

`lxm compile` validates every manifest (resolving includes and directives) and writes the compiled copies to `.lxm/compiled/` without touching your source. Any schema error or unbound template variable fails the whole directory with `exit 3`.

Then preview the reconciliation plan:

```text
$ lxm plan docs/examples/
Plan: 6 to create, 0 to update, 0 to recreate, 0 to delete, 1 noop across 7 manifest(s)
```

`plan` reads every manifest in the directory, compares it against live LXD state, and prints the summary — without changing anything.

## When things go wrong

| Error | Cause | Fix |
|---|---|---|
| `file "x.yaml" has '_' prefix but base is not true` | A `_`-prefixed file without `base: true` | Add `base: true`, or rename the file. |
| `base config must not have a name` / `... an image` | A base manifest set a per-container field | Remove `name`/`image` from the base file. |
| `circular include detected` | Two manifests include each other | Break the cycle. |
| `unknown top-level key` | A typo or a v1-era field in a v2 manifest | Fix or migrate the field; see [Migrating from lxm v1](migrate-v1.md). |
| `remove.recipes: "x" matched no recipe` | A `remove` target that does not exist in the merged manifest | Remove the directive or fix the path. |
| `duplicate mount path` / `duplicate network name` | Two mounts/networks with the same key after merge | Deduplicate in the base or the leaf. |

## Next steps

* [Mounting Host Directories](mount-host-dirs.md) — add bind mounts to a container.
* [Targeting with Selectors](fleet-selectors.md) — operate on a subset of the fleet.
* [Manifest Reference](../reference/manifest.md) — every field, with examples.
