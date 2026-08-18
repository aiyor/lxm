# lxm v2 CLI Specification

## 1. Overview & Command Hierarchy

`lxm` provides a unified, structured command-line interface for managing LXD container fleets. All commands adhere to standardized flag parsing, exit code categorization, and structured result envelope formatting.

The binary registers 16 subcommands:

```
lxm
├── apply       Apply reconciliation plan to live infrastructure
├── compile     Compile and migrate v1 manifests to v2 schema
├── completion  Generate shell completion scripts (bash, zsh, fish, powershell)
├── diff        Preview reconciliation plan for a single container against a config file
├── doctor      Audit host environment, LXD daemon, group membership, and fleet health
├── include     Add an include directive to all configs in a directory (stub)
├── init        Initialize fleet manifest structure (_base.yaml & config/dev.yaml)
├── list        List fleet container inventory and live state
├── plan        Compute and preview reconciliation plan for a manifest or directory
├── rollback    Roll back a container to a named snapshot
├── run         Execute a local script across a container or fleet directory
├── script      Execute a local script inside a container
├── shell       Open an interactive shell inside a container (TTY carve-out)
├── snapshot    Manage container snapshots (create, delete, list, gc)
├── ssh         Execute commands or interactive shell via SSH (TTY carve-out)
└── status      Display detailed status and recipe history for a container
```

---

## 2. Global Persistent Flags

The following persistent flags are defined on the `lxm` root command:

| Flag | Short | Type | Description |
| :--- | :---: | :--- | :--- |
| `--dry-run` | | `bool` | Show what would change without applying mutations. |
| `--debug` | | `bool` | Enable verbose debug logging (`slog.LevelDebug`). |
| `--wait` | | `bool` | Wait for cloud-init readiness before executing recipes. |
| `--force` | | `bool` | Re-run recipes even if idempotency hashes match. |
| `--include-hidden` | | `bool` | Include `_`-prefixed base config files during directory discovery. |
| `--format` | | `string` | Output format: `text` (default) or `json` (`lxm/result/v1` envelope). |
| `--group` | `-g` | `stringSlice` | Filter containers matching ANY specified group tag (OR union). |
| `--exclude-group` | | `stringSlice` | Exclude containers matching ANY specified group tag. |
| `--provider` | | `string` | Target provider type (`incus`, `lxd`, or `auto`). |
| `--remote` | | `string` | Target remote name from `~/.config/lxm/remotes.yaml`. |
| `--target` | | `string` | Target cluster member node name for instance placement. |
| `--project` | | `string` | Target project namespace boundary. |
| `--version` | | `bool` | Display version information and exit. |

---

## 3. Exit Code Catalog

`lxm` returns categorized exit codes to allow scripts and automation pipelines to distinguish failure modes:

| Exit Code | Code Name | Description |
| :---: | :--- | :--- |
| **0** | `OK` | Command completed successfully with no errors. |
| **1** | `INTERNAL_ERROR` | Unhandled error, panic, or internal logic failure. |
| **2** | `USAGE_ERROR` | Invalid command arguments, flag syntax, or TTY carve-out violation. |
| **3** | `CONFIG_ERROR` | YAML syntax error, schema validation failure, or unbound variable. |
| **4** | `LXD_ERROR` | LXD daemon connection failure, API error, or ETag concurrency conflict. |
| **5** | `TARGET_NOT_FOUND` | Container name, snapshot, or selector target set not found (empty match). |
| **6** | `EXEC_FAILED` | Recipe execution failure, non-zero script exit code, or command error. |
| **7** | `WAIT_TIMEOUT` | Cloud-init or network readiness wait timeout exceeded. |

---

## 4. Command Reference

### 4.1 `lxm init`
Initializes starter `_base.yaml` and `config/dev.yaml` manifests in the target directory.

```bash
lxm init [path] [--force]
```
* **Arguments**: Optional target directory path (default: `.`).
* **Flags**: `--force`: Overwrite existing manifest files if present.
* **Exit Codes**: `0` (success), `2` (target files exist without `--force`).

### 4.2 `lxm plan`
Computes and previews the deterministic reconciliation plan comparing manifests against live LXD state.

```bash
lxm plan <file|dir> [--name N] [--prune] [--format json|text]
```
* **Arguments**: Exactly 1 argument specifying a manifest file or directory path.
* **Flags**:
  * `--name`: Filter container name by pattern.
  * `--prune`: Include delete steps for orphaned containers in directory targets.
  * `--format`: Output format (`text` or `json`).
* **Exit Codes**: `0` (success), `2` (single-file `--prune` or usage error), `3` (config error), `4` (LXD error), `5` (empty match or missing file).

### 4.3 `lxm apply`
Computes the reconciliation plan and applies required mutations against the LXD daemon.

```bash
lxm apply <file|dir> [--name N] [--rename-to NAME] [--prune] [--no-start]
```
* **Arguments**: Exactly 1 argument specifying a manifest file or directory path.
* **Flags**:
  * `--name`: Filter container name by pattern.
  * `--rename-to`: Override container target name (single-file targets only).
  * `--prune`: Garbage-collect orphaned managed containers (directory targets only; exit `2` on single-file).
  * `--no-start`: Do not start stopped containers after applying updates.

### 4.4 `lxm diff`
Previews the reconciliation plan for a single target container against a specific manifest file.

```bash
lxm diff <config-file> <container-name>
```
* **Arguments**: Exactly 2 arguments: the manifest file path and container name.

### 4.5 `lxm list`
Displays managed fleet inventory, live container status, IP addresses, groups, and base images.

```bash
lxm list [--name N] [--format json|text]
```

### 4.6 `lxm status`
Displays detailed operational status, device mounts, networks, and recipe execution trails for a container.

```bash
lxm status <container-name>
```
* **Arguments**: Exactly 1 argument specifying the target container name.

### 4.7 `lxm compile`
Compiles and migrates legacy v1 manifests to the `lxm/config/v2` schema format.

```bash
lxm compile <target> [--in-place]
```
* **Flags**: `--in-place`: Atomically rewrite source files with compiled v2 manifests.

### 4.8 `lxm doctor`
Performs environment diagnostic checks (LXD socket reachability, `lxd` system group membership, `/proc/self/uid_map` kernel idmapped mounts existence, manifest schema migration status).

```bash
lxm doctor [target_dir] [--skip-remote] [--format json|text]
```
* **Flags**: `--skip-remote`: Skip remote LXD socket reachability check.

### 4.9 `lxm shell` (Interactive Carve-Out)
Opens an interactive TTY shell session inside a live container via native LXD websocket streaming.

```bash
lxm shell <container-name> [--run-as USER]
```
* **Flags**: `--run-as`: Target user account inside container (default: `root`).
* **Carve-Out Rule**: Passing `--format json` to `lxm shell` is explicitly rejected with **exit code 2**.

### 4.10 `lxm ssh` (Interactive Carve-Out)
Establishes an OpenSSH connection to a container using tool-managed `known_hosts` key verification.

```bash
lxm ssh <container-name> [command...] [-o Option=Value]
```
* **Security & Options**: Host keys are registered on first connect via `ssh-keyscan` under advisory file lock (`syscall.Flock`). Options disabling verification (`-o StrictHostKeyChecking=no`, `-o UserKnownHostsFile=/dev/null`) trigger a security warning.
* **Carve-Out Rule**: Passing `--format json` to `lxm ssh` is explicitly rejected with **exit code 2**.

### 4.11 `lxm snapshot`
Manages container snapshots (create, delete, list, gc).

```bash
lxm snapshot create <container-name> <snapshot-name>
lxm snapshot delete <container-name> <snapshot-name>
lxm snapshot list <container-name>
lxm snapshot gc <container-name> [--older-than DURATION] [--prefix PREFIX] [--delete]
```

### 4.12 `lxm rollback`
Rolls back a container to a named snapshot.

```bash
lxm rollback <container-name> <snapshot-name>
```

### 4.13 `lxm run` & `lxm script`
Executes a local script file across a container or fleet directory (`run`), or inside a single container (`script`).

```bash
lxm run <target> <script-path> [--run-as USER] [--env KEY=VAL]
lxm script <container-name> <script-path> [user] [--run-as USER] [--env KEY=VAL]
```
* **Arguments**:
  * `run`: Exactly 2 arguments (`target` container or fleet directory, and `script-path`).
  * `script`: 2 to 3 arguments (`container-name`, `script-path`, and optional positional `user`).
* **Flags**: `--run-as USER` (default: `root`), `--env KEY=VAL` (repeatable environment variables map).

### 4.14 `lxm completion`
Generates shell autocompletion scripts for `bash`, `zsh`, `fish`, or `powershell`.

```bash
lxm completion <bash|zsh|fish|powershell>
```

### 4.15 `lxm include` (Stub Command)
Adds an include directive to all configs in a target directory (registered stub command).

```bash
lxm include <config_dir> <include_file>
```

### 4.16 `lxm remote`
Manages remote Incus and LXD daemon endpoints, trust token enrollment, and client mTLS credentials:

```bash
# List configured remotes
lxm remote list [--format json|text]

# Add a remote endpoint and enroll client mTLS certificate via trust token
lxm remote add <name> <address> [--token <token>] [--provider incus|lxd] [--project <project>] [--insecure]

# Remove a remote configuration
lxm remote remove <name>

# Switch default remote
lxm remote set-default <name>

# Set default project for remote
lxm remote set-project <name> <project>
```

---

## 5. Selector Algebra & Targeting Rules

Fleet selection filters containers based on group membership and container names:

1. **Group Union (OR)**: Multiple `--group` flags or comma-separated lists evaluate as a union (`-g dev,staging` or `-g dev -g staging`).
2. **Group x Name Intersection (AND)**: Combining `--group` and `--name` evaluates as an intersection.
3. **Empty Selector Match**: If selector evaluation matches zero containers, the command exits with **exit code 5** (`TARGET_NOT_FOUND`).

---

## 6. Prune Scope Rules

Orphan garbage collection via `--prune` operates under strict scope boundaries:

* **Directory Target Only**: `--prune` is accepted only when targeting a directory (e.g. `lxm apply config/ --prune`). Invoking `apply` or `plan` on a single file with `--prune` exits immediately with **exit code 2**.
* **Orphan Definition**: An instance is considered an orphan *iff* it is managed (`user.lxm.managed=true`), matches the active selector set, and is NOT declared in any manifest within the target directory.
* **Effective Scope**: `Prune Scope = Target Directory ∩ Active Selectors`.
