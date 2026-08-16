# lxm — Declarative LXD Fleet Manager

`lxm` is a Go CLI tool for declarative, reproducible fleet management of LXD containers and hardware-virtualized Virtual Machines (VMs). Define your fleet's desired state in YAML manifests, preview pure deterministic diffs with `lxm plan`, and apply mutations safely with `lxm apply`.

**[User Guide](docs/index.md)** | **[Quick Installation](docs/getting-started/installation.md)** | **[Quick Start](docs/getting-started/quickstart.md)**

---

## Key Features

* **Plan-First Architecture**: Every infrastructure mutation is preceded by a pure, deterministic diff preview (`lxm plan`).
* **Containers & Virtual Machines**: Unified management for lightweight LXC system containers and hardware-virtualized QEMU/KVM virtual machines (`type: container` / `type: vm`).
* **Managed Virtual Switches & Network Segmentation**: Declare `vswitches:` (LXD managed bridges) and a group-based `network_policy:` — compiled deterministically into LXD network ACLs for isolated, mutually-communicating, and one-way networks.
* **Declarative & Idempotent**: State reconciliation automatically handles instance creation, hardware limits (CPU, memory, disk), VM hypervisor settings, device mounting, network configuration, and image rebuilds.
* **Structured Machine Interface**: Every command (excluding TTY shells) supports `--format json` with standardized `lxm/result/v1` result envelopes and categorized exit codes (0–7).
* **CUE Schema Validation**: Manifest authoring is checked against CUE schemas (`#LXM_AUTHORING` and `#LXM_RESOLVED`) for strict path and security compliance.
* **Security by Default**: Enforces tool-managed host key verification via `~/.config/lxm/known_hosts` with advisory file locking (`syscall.Flock`). Sudo elevation and key injection are strictly opt-in.
* **Fleet Selectors & Parallel Execution**: Flexible group union and name targeting (`-g`, `--name`) with parallel execution pools.
* **Snapshot Safety & Rollback**: Automatic pre-recipe instance snapshots with age/prefix retention garbage collection (`lxm snapshot gc`).

---

## Specifications

* **[NETWORK-SPEC.md](NETWORK-SPEC.md)** — the authoritative spec for the `vswitches:` / `network_policy:` **feature**: group-based traffic policy compiled into LXD network ACLs, the generator matrix, CIDR decomposition, reconciliation/execution model, and verified integration results.
* **[STORAGE-SPEC.md](STORAGE-SPEC.md)** — the authoritative spec for the `disks:` **feature**: additional VM data disks in filesystem or block mode, the mode × ownership matrix, verified LXD constraints, and the reconciliation/execution model.
* **[IMAGE-SPEC.md](IMAGE-SPEC.md)** — the authoritative spec for the `image:` `remote:alias` **feature**: cloud image lookup & fetch, the canonical type-qualified local alias, the cache probe, and the Phase −1 execution model.
* **[VM-SPEC.md](VM-SPEC.md)** — the authoritative spec for virtual-machine fleet management: VM manifest fields, hardware limits, hypervisor settings, and the VM apply/verify lifecycle.
* **[SPEC_MANIFEST.md](SPEC_MANIFEST.md)** — the canonical **manifest schema** contract (all fields, CUE validation, inheritance) — including the authoritative `vswitches:`/`network_policy:` (§3.6/§3.7), `disks:` (§3.9), and `image:`/`image_remotes:` (§3.10) field tables that the feature specs reference.

---

## Requirements

* Linux (Ubuntu 22.04+ recommended; Linux kernel 5.12+ for idmapped mounts)
* LXD 5.0+ LTS
* Host user in the `lxd` system group
* Go 1.26+ to build from source

---

## Installation & Build

For complete installation details and options (including [mise](https://mise.jdx.dev/), prebuilt release binaries, and building from source), see the **[Quick Installation Guide](docs/getting-started/installation.md)**.

### One-Line Installer

Install the latest release binary with the one-line installer:

```bash
curl -fsSL https://raw.githubusercontent.com/aiyor/lxm/main/install.sh | sh
```

Or install and pin a version with [mise](https://mise.jdx.dev/):

```bash
mise use -g github:aiyor/lxm
```

### Building from Source

Build the `lxm` binary directly from source:

```bash
go build -o lxm ./cmd/lxm
```

The compiled binary is self-contained with zero CGO runtime dependencies.

### Development

The repository uses [Task](https://taskfile.dev/) for local automation:

```bash
task fmt    # Format Go source files (gofmt -s -w .)
task lint   # Run golangci-lint across all packages
task test   # Run full test suite with race detector
task build  # Format and build ./bin/lxm binary
```

---

## Quick Start

For a detailed walkthrough, refer to the **[Quick Start Guide](docs/getting-started/quickstart.md)** or browse the full **[User Guide](docs/index.md)**.

### 1. Initialize a Fleet Structure
Create a starter `_base.yaml` and `config/dev.yaml` manifest structure in your working directory:

```bash
lxm init
```

### 2. Configure Your Manifest & Host Directories (`config/dev.yaml`)
Create the host mount source directory:
```bash
mkdir -p /tmp/projects
```

Edit `config/dev.yaml`:
```yaml
schema: lxm/config/v2
include:
  - ../_base.yaml
name: dev-station
image: ubuntu:22.04
status: present
state: running

user: ubuntu
sudo: false           # opt-in to passwordless sudo if needed
inject_ssh_keys: true # inject host SSH public keys

mounts:
  - source: /tmp/projects
    path: /var/www/html

networks:
  - name: eth0
    ipv4: 10.10.10.50
    parent: lxdbr0

wait:
  cloud_init: 5m
  required: true
```

### 3. Preview Plan & Apply Changes
```bash
# Preview the deterministic reconciliation plan for a manifest
lxm plan config/dev.yaml

# Apply mutations to LXD
lxm apply config/dev.yaml

# Verify live fleet inventory
lxm list

# Check detailed container status and IP addresses
lxm status dev-station

# Open an interactive shell
lxm shell dev-station
```

---

## Security Posture & SSH Operations

`lxm` implements a hardened security posture:

1. **Tool-Managed `known_hosts`**: All SSH connections use `~/.config/lxm/known_hosts` managed under advisory file lock (`syscall.Flock` on `~/.config/lxm/known_hosts.lock`).
2. **First-Connect Registration**: On first connection to a container, `lxm` non-interactively registers its SSH host key via `ssh-keyscan` before executing `ssh` with `StrictHostKeyChecking=yes`.
3. **Key Purging on Recreate/Delete**: When containers are recreated or deleted, stale host keys (including hashed OpenSSH entries) are automatically purged via `ssh-keygen -R <container-name>`.
4. **Least Privilege Defaults**: Cloud-init user creation omits passwordless `sudo` (`NOPASSWD:ALL`) and host SSH key injection unless explicitly set via `sudo: true` and `inject_ssh_keys: true`.

---

## Commands Reference

The `lxm` CLI binary registers 16 subcommands:

| Command | Usage | Description |
| :--- | :--- | :--- |
| `apply` | `lxm apply <file\|dir> [flags]` | Apply reconciliation plan to live infrastructure. |
| `compile` | `lxm compile <target> [--in-place]` | Compile and migrate v1 manifests to `v2` schema. |
| `completion`| `lxm completion <bash\|zsh\|fish\|powershell>` | Generate shell completion scripts. |
| `diff` | `lxm diff <config-file> <container>` | Preview plan for a single container against a config file. |
| `doctor` | `lxm doctor [dir] [--skip-remote]` | Audit host environment, LXD daemon, and fleet health. |
| `include` | `lxm include <config_dir> <include_file>` | Add an include directive to all configs (stub). |
| `init` | `lxm init [path] [--force]` | Initialize `_base.yaml` and `config/dev.yaml` structure. |
| `list` | `lxm list [--name N] [--format]` | List fleet containers, IPs, groups, and status. |
| `plan` | `lxm plan <file\|dir> [flags]` | Preview deterministic reconciliation plan. |
| `rollback`| `lxm rollback <container> <snap>` | Roll back a container to a named snapshot. |
| `run` | `lxm run <target> <script-path>` | Run a local script across a container or fleet directory. |
| `script` | `lxm script <container> <path> [user]`| Execute a local script inside a container. |
| `shell` | `lxm shell <container> [--run-as U]` | Open interactive TTY shell via websocket (`root` default). |
| `snapshot`| `lxm snapshot <action> <container>` | Manage snapshots (create, delete, list, gc). |
| `ssh` | `lxm ssh <container> [cmd]` | Connect via OpenSSH with managed host keys. |
| `status` | `lxm status <container>` | Display detailed container status and recipe history. |

---

## Exit Code Catalog

`lxm` returns categorized exit codes for reliable script automation:

* **`0` (`OK`)**: Success.
* **`1` (`INTERNAL_ERROR`)**: Runtime panic or unhandled error.
* **`2` (`USAGE_ERROR`)**: CLI flag parse error or TTY carve-out violation.
* **`3` (`CONFIG_ERROR`)**: Manifest syntax, CUE validation, or unbound variable error.
* **`4` (`LXD_ERROR`)**: LXD API, socket, or ETag concurrency error.
* **`5` (`TARGET_NOT_FOUND`)**: Target container, snapshot, or selector match not found.
* **`6` (`EXEC_FAILED`)**: Recipe execution error.
* **`7` (`WAIT_TIMEOUT`)**: Cloud-init or network wait deadline exceeded.
