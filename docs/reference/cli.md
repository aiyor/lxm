# CLI Reference

This page is the authoritative, user-facing reference for the `lxm` command line. It covers all 16 subcommands, their exact signatures, flags, arguments, and exit codes.

Every signature block below is a verbatim copy of `lxm <command> --help` output from the shipped binary. The docs CI workflow re-checks this page against the binary on every pull request, so it cannot drift.

If a command is not listed here, it does not exist — run `lxm --help` to see the command list of the binary you are using.

## Command list

```
lxm
├── apply       Reconcile desired state (manifests) against live LXD containers
├── compile     Emit resolved v2 manifests and migrate legacy v1 configs
├── completion  Generate shell completion scripts (bash, zsh, fish, powershell)
├── diff        Show Plan scoped to a single container
├── doctor      Run fleet and host diagnostic checks
├── include     Add an include directive to all configs in a directory
├── init        Scaffold a new fleet directory with base config and template manifests
├── list        List fleet inventory (managed containers and live state)
├── plan        Compute and print the reconciliation Plan without mutating live state
├── rollback    Restore an instance to a previous snapshot
├── run         Run a script across targeted fleet containers
├── script      Run a single script on a container
├── shell       Open an interactive shell in a container
├── snapshot    Manage instance snapshots
├── ssh         Open an SSH session to a container (hardened host-key verification)
└── status      Show cloud-init, network, recipe, and snapshot status for a container
```

## Global persistent flags

These flags are defined on the `lxm` root command and are available on every subcommand:

```
Flags:
      --debug                   Show verbose output
      --dry-run                 Show what would change without applying
      --exclude-group strings   Exclude containers matching ANY tag (OR)
      --force                   Re-run recipes even if hashes match
      --format string           Output format (text, json) (default "text")
  -g, --group strings           Filter to containers matching ANY tag (OR)
      --include-hidden          Include _-prefixed base config files
      --wait                    Wait for cloud-init to finish
```

| Flag | Description |
|---|---|
| `--debug` | Enable verbose debug logging. |
| `--dry-run` | Show what would change without applying any mutation. No container, snapshot, or recipe state changes. |
| `--force` | Re-run recipes even if their idempotency hashes match. |
| `--format` | Output format: `text` (default) or `json` (`lxm/result/v1` envelope, see [Results & Exit Codes](results-and-exit-codes.md)). Rejected with exit code 2 on the interactive `shell` and `ssh` commands. |
| `-g, --group` | Filter to containers matching **any** of the given group tags (OR union). Repeatable, and comma-separated values are accepted. |
| `--exclude-group` | Exclude containers matching **any** of the given group tags (OR). |
| `--include-hidden` | Include `_`-prefixed base config files during directory discovery. |
| `--wait` | Wait for cloud-init readiness before executing recipes. Overrides `wait.required`. |
| `-v, --version` | Print the version and exit. |

Targeting (`--group` × `--name`) is described in [Selector algebra](#selector-algebra) below.

## Exit codes

Every command returns one of the categorized exit codes in [Results & Exit Codes](results-and-exit-codes.md#exit-code-catalog). The table there is the single source of truth; each command section below lists the exit codes that command can actually produce.

---

## `lxm apply`

Reconcile desired state (manifests) against live LXD containers.

```
Usage:
  lxm apply <file|dir> [flags]
```

| Flag | Description |
|---|---|
| `--name string` | Filter container name by pattern |
| `--no-start` | Do not start stopped containers after apply |
| `--prune` | Garbage-collect orphaned managed containers (deletes containers with `user.lxm.managed=true` missing from target dir) |
| `--rename-to string` | Rename container (single-file target only) |

**Arguments:** exactly one — a manifest file or a directory of manifests.

**Exit codes:** `0` success · `1` internal · `2` `--prune` on a single file, `--rename-to` on a directory · `3` config load/validation error · `4` LXD error (including ETag conflicts) · `5` target not found or selector matches nothing · `6` recipe execution failed · `7` wait timeout.

**Notes:**

* `--prune` is only allowed on directory targets; on a single file it exits `2`. See [Prune scope](#prune-scope-rules).
* `--rename-to` is only allowed on single-file targets; on a directory it exits `2`.
* `apply` prints the same plan summary shape as `plan` while it works, then applies the plan.

Example (text):

```text
$ lxm apply config/dev.yaml
Applied 1 step(s) across 1 container(s)
$ echo $?
0
```

---

## `lxm plan`

Compute and print the reconciliation Plan without mutating live state.

```
Usage:
  lxm plan <file|dir> [flags]
```

| Flag | Description |
|---|---|
| `--format string` | Output format (text\|json) (default "text") |
| `--name string` | Filter container name |
| `--prune` | Garbage-collect orphaned managed containers (deletes containers with `user.lxm.managed=true` missing from target dir) |

**Arguments:** exactly one — a manifest file or a directory of manifests.

**Exit codes:** `0` success · `2` `--prune` on a single file or invalid `--format` · `3` config error · `5` target not found or selector matches nothing.

**Notes:**

* `plan` never mutates state. If LXD is unreachable it plans against an empty live state rather than failing, so you can preview what a fresh host would create.
* With `--format json` the full step list is emitted in the envelope's `plan` field.

Example (text):

```text
$ lxm plan config/dev.yaml
Plan: 1 to create, 0 to update, 0 to recreate, 0 to delete, 0 noop across 1 manifest(s)
$ echo $?
0
```

---

## `lxm diff`

Show Plan scoped to a single container.

```
Usage:
  lxm diff <config-file> <container> [flags]
```

| Flag | Description |
|---|---|
| `--format string` | Output format (text\|json) (default "text") |

**Arguments:** exactly two — a manifest file path and the container name.

**Exit codes:** `0` success · `2` invalid `--format` · `3` config error.

**Notes:** `diff` loads one manifest, applies it to the named container, and prints the field-level differences for that container's first step.

Example (text):

```text
$ lxm diff docs/examples/dev-station.yaml dev-station
Diff for container dev-station (action: create, changed: true):
  - status: old=absent -> new=present
  - image: old=<nil> -> new=ubuntu:22.04
```

---

## `lxm list`

List fleet inventory (managed containers and live state).

```
Usage:
  lxm list [flags]
```

| Flag | Description |
|---|---|
| `--format string` | Output format (text\|json) (default "text") |
| `--name string` | Filter container name by pattern |

**Arguments:** none.

**Exit codes:** `0` success · `2` invalid `--format` · `4` LXD error · `5` no containers match the filter criteria.

**Notes:** `--name` is a regex pattern. `--group` and `--exclude-group` also apply.

Example (text):

```text
$ lxm list --name dev-station
NAME         STATUS   MANAGED  GROUPS  IMAGE   IP
dev-station  Running  true     dev     ubuntu  10.171.13.47
```

---

## `lxm status`

Show cloud-init, network, recipe, and snapshot status for a container.

```
Usage:
  lxm status <name> [flags]
```

| Flag | Description |
|---|---|
| `--format string` | Output format (text\|json) (default "text") |

**Arguments:** exactly one — the target container name.

**Exit codes:** `0` success · `2` invalid `--format` · `4` LXD error · `5` container not found.

**Notes:** the recipe hash trail lists the stored idempotency hashes lxm uses to decide whether a recipe needs to run again.

Example (text):

```text
$ lxm status dev-station
Container:     dev-station
Status:        Running (Code 103)
Architecture:  x86_64
IP Address:    10.171.13.47
Managed:       true
Groups:        dev

Recipe Hash Trail:
  (none)
```

---

## `lxm init`

Scaffold a new fleet directory with base config and template manifests.

```
Usage:
  lxm init [target_dir] [flags]
```

| Flag | Description |
|---|---|
| `--format string` | Output format (text\|json) (default "text") |

**Arguments:** at most one — the target directory (default `.`).

**Exit codes:** `0` success · `2` target files already exist without `--force`, or directory creation failed.

**Notes:** writes `_base.yaml` and `config/dev.yaml`. Refuse to overwrite existing files unless `--force` is given.

Example (text):

```text
$ lxm init
Initialized lxm fleet in .:
  - _base.yaml
  - config/dev.yaml
$ echo $?
0
```

---

## `lxm run`

Run a script across targeted fleet containers.

```
Usage:
  lxm run <target> <script> [flags]
```

| Flag | Description |
|---|---|
| `--dry-run` | Preview execution without modifying containers |
| `--env strings` | Environment variables |
| `--format string` | Output format (text\|json) (default "text") |
| `--run-as string` | User to run script as (default "root") |

**Arguments:** exactly two — the target (a container name, or a manifest file/directory), and the local script path.

**Exit codes:** `0` success · `2` invalid `--format` or selector error · `3` config error or invalid `--env` key · `4` LXD error · `5` target not found or selector matches nothing · `6` a script failed on one or more containers.

**Notes:** `--env` takes repeatable `KEY=VAL` entries. Keys must match the POSIX identifier regex `^[a-zA-Z_][a-zA-Z0-9_]*$`.

---

## `lxm script`

Run a single script on a container.

```
Usage:
  lxm script <name> <path> [user] [flags]
```

| Flag | Description |
|---|---|
| `--dry-run` | Preview execution without modifying container |
| `--env strings` | Environment variables |
| `--format string` | Output format (text\|json) (default "text") |
| `--run-as string` | User to run script as (default "root") |

**Arguments:** two or three — container name, script path, and an optional positional `user`.

**Exit codes:** `0` success · `2` invalid `--format` · `3` config error or invalid `--env` key · `4` LXD error · `5` container not found · `6` script execution failed.

---

## `lxm snapshot`

Manage instance snapshots.

```
Usage:
  lxm snapshot [name] [snapshot_name] [flags]
```

| Flag | Description |
|---|---|
| `--delete string` | Delete specified snapshot name |
| `--dry-run` | Preview snapshot operations without modifying LXD state |
| `--format string` | Output format (text\|json) (default "text") |
| `--gc` | Prune aged/recipe snapshots |
| `--older-than string` | Age threshold for snapshot GC (e.g. 7d, 24h) |
| `--prefix string` | Snapshot prefix for GC (default "user.lxm.snap.") |

**Arguments:** the operation is chosen by the first positional word, followed by the container (and snapshot) names:

```text
lxm snapshot create <container> <snapshot>
lxm snapshot delete <container> <snapshot>
lxm snapshot list <container>
lxm snapshot gc [container] [--older-than DURATION] [--prefix PREFIX]
```

With no subcommand word, `lxm snapshot <container>` lists that container's snapshots.

**Exit codes:** `0` success · `2` missing required arguments or subcommand misuse · `4` LXD error · `5` container not found.

**Notes:**

* Snapshot names created by recipe execution use the `user.lxm.snap.` prefix, so `gc` targets them by default.
* `--older-than` accepts Go duration strings (`24h`, `7d` also works for days).

Example (text):

```text
$ lxm snapshot create glm pre-upgrade --dry-run
time=2026-08-09T08:55:19.004+10:00 level=INFO msg="Dry-run mode enabled. No changes will be made."
[DRY-RUN] Would create snapshot "pre-upgrade" for instance "glm"
$ lxm snapshot gc glm --older-than 7d --dry-run
time=2026-08-09T08:55:19.015+10:00 level=INFO msg="Dry-run mode enabled. No changes will be made."
[DRY-RUN] Would prune 0 snapshot(s) matching prefix "user.lxm.snap."
```

---

## `lxm rollback`

Restore an instance to a previous snapshot.

```
Usage:
  lxm rollback <name> <snapshot> [flags]
```

| Flag | Description |
|---|---|
| `--dry-run` | Preview rollback operation |
| `--format string` | Output format (text\|json) (default "text") |

**Arguments:** exactly two — container name and snapshot name.

**Exit codes:** `0` success · `2` invalid `--format` · `4` LXD error · `5` container not found.

!!! warning

    `rollback` restores the container filesystem and state to the snapshot, discarding changes made since. It does not consult the manifest first — reconcile with `lxm apply` afterwards to bring the container back to your declared state.

---

## `lxm ssh`

Open an SSH session to a container (hardened host-key verification).

```
Usage:
  lxm ssh <name> [ssh_args...]
```

`ssh` disables normal flag parsing and interprets its arguments itself, so the signature above is not printed by `--help`. The container name is the first non-flag argument; everything after it is passed through to the local `ssh` client.

| Flag / option | Description |
|---|---|
| `--user <user>` / `-u <user>` / `--run-as <user>` | SSH user. Defaults to the manifest `user` (via `user.lxm.user`), then `root`. |
| `-i <identity>` / `--identity <identity>` | Identity file, passed as `-o IdentitiesOnly=yes -i <identity>`. |
| `--insecure` | Disable host-key verification (`StrictHostKeyChecking=no`, `UserKnownHostsFile=/dev/null`) with a warning. |
| `--dry-run` | Print the `ssh` invocation without running it. |
| `-o Option=Value` | Any other `-o` option is passed to `ssh`. |
| `--` | Everything after is passed to `ssh` verbatim. |

**Exit codes:** `0` success · `2` no arguments, or `--format json` (the interactive carve-out) · `4` LXD error · `5` container not found · `6` host-key registration failed, container has no IPv4/is not running, or the `ssh` session failed.

**Notes:**

* On first connect, lxm registers the container's host key into `~/.config/lxm/known_hosts` (overridable with `LXM_KNOWN_HOSTS_FILE`) under an advisory lock, then runs `ssh` with `StrictHostKeyChecking=yes`.
* Passing `--format json` is rejected with exit code 2 — `ssh` is interactive.

!!! warning

    `--insecure` and `-o StrictHostKeyChecking=no` / `-o UserKnownHostsFile=/dev/null` disable host-key verification and are printed as warnings. Never use them against containers you do not fully trust.

---

## `lxm shell`

Open an interactive shell in a container.

```
Usage:
  lxm shell <name> [flags]
```

| Flag | Description |
|---|---|
| `--run-as string` | User to run shell as |

**Arguments:** exactly one — the target container name.

**Exit codes:** `0` success · `2` `--format json` (the interactive carve-out) · `4` LXD error · `5` container not found · `6` user resolution or interactive exec failed.

**Notes:** opens `/bin/bash -l` inside the container over the LXD websocket. The default user is `root` unless `--run-as` is given. Passing `--format json` is rejected with exit code 2.

---

## `lxm include`

Add an include directive to all configs in a directory.

```
Usage:
  lxm include <config_dir> <include_file> [flags]
```

**Arguments:** exactly two — a config directory and the include file to add.

**Exit codes:** `0` always. This command is registered as a **stub** and currently performs no operation.

---

## `lxm compile`

Emit resolved v2 manifests and migrate legacy v1 configs.

```
Usage:
  lxm compile <target> [flags]
```

| Flag | Description |
|---|---|
| `--format string` | Output format (text\|json) (default "text") |
| `--in-place` | Overwrite source YAML files in place |

**Arguments:** exactly one — a manifest file or directory.

**Exit codes:** `0` success · `2` invalid `--format` · `3` config/schema error, or a migration conflict (e.g. both `cloud-init` and `cloud-init-file` set) · `5` target not found.

**Notes:**

* Non-destructive by default: migrated manifests are written under `.lxm/compiled/` next to the source. `--in-place` rewrites the source files atomically instead.
* Compilation is the migration tool for legacy `lxm/config/v1` files (those without `schema: lxm/config/v2`). See [Migrating from lxm v1](../howto/migrate-v1.md).
* Warnings about v2 semantic default flips (`sudo`, `inject_ssh_keys`) and pruned empty recipe groups are printed to stderr; they do not change the exit code.

Example (text):

```text
$ lxm compile docs/examples/
Successfully compiled 7 manifest(s):
  - docs/examples/.lxm/compiled/absent-demo.yaml
  - docs/examples/.lxm/compiled/cloud-init-demo.yaml
  - docs/examples/.lxm/compiled/dev-station.yaml
  - docs/examples/.lxm/compiled/inheritance-demo.yaml
  - docs/examples/.lxm/compiled/mounts-demo.yaml
  - docs/examples/.lxm/compiled/mounts-map.yaml
  - docs/examples/.lxm/compiled/recipes-demo.yaml
```

---

## `lxm doctor`

Run fleet and host diagnostic checks.

```
Usage:
  lxm doctor [target_dir] [flags]
```

| Flag | Description |
|---|---|
| `--format string` | Output format (text\|json) (default "text") |
| `--skip-remote` | Skip remote checks |

**Arguments:** at most one — a directory to scan for manifests (default `.`).

**Exit codes:** `0` success (warnings do not change the exit code) · `2` invalid `--format` · `4` LXD socket unreachable when `--skip-remote` is not set.

**Notes:** checks LXD socket reachability, `lxd` group membership, kernel idmapped-mount support, and flags un-migrated manifests. See [Diagnosing with doctor](../howto/diagnose-with-doctor.md).

Example (text):

```text
$ lxm doctor --skip-remote docs/examples/
Running lxm doctor diagnostic checks...
[SKIP] Remote LXD socket check skipped
[OK] lxd group membership
[OK] Kernel idmapped mounts support
[OK] All discovered configs migrated to lxm/config/v2
```

---

## `lxm completion`

Generate the autocompletion script for lxm for the specified shell.

```
Usage:
  lxm completion [command]
```

| Subcommand | Description |
|---|---|
| `bash` | Generate the autocompletion script for bash |
| `fish` | Generate the autocompletion script for fish |
| `powershell` | Generate the autocompletion script for powershell |
| `zsh` | Generate the autocompletion script for zsh |

**Exit codes:** `0` success · `2` invalid shell.

Example (bash):

```bash
source <(lxm completion bash)
```

---

## Selector algebra

Fleet selection combines group membership and container names when you pass `--group`, `--exclude-group`, and `--name`:

1. **Group union (OR):** multiple `--group` values — repeated flags or comma-separated lists — match a container that has **any** of the groups (`-g dev,staging` or `-g dev -g staging`).
2. **Name intersection (AND):** combining `--group` and `--name` matches only containers that satisfy **both**.
3. **Empty match:** if a selector matches zero containers, the command exits `5` (`TARGET_NOT_FOUND`).
4. **Exclusion:** `--exclude-group` removes containers matching any listed group, and is applied before the other criteria.

`--name` is interpreted as a regular expression; a bare value like `dev-station` is anchored (`^dev-station$`).

## Prune scope rules

`--prune` garbage-collects orphaned managed containers and is strictly scoped:

* **Directory targets only.** `apply` or `plan` on a single file with `--prune` exits `2`.
* **Orphan definition.** An instance is an orphan if it is managed (`user.lxm.managed=true`), matches the active selector set, and is not declared in any manifest under the target directory.
* **Effective scope.** Orphans are only considered inside `Target Directory ∩ Active Selectors` — pruning never reaches outside the directory you targeted or the containers your selectors match.

!!! warning

    `--prune` deletes containers permanently. It only touches containers lxm itself marked as managed and missing from the targeted directory, but any data in them is gone.

## Related

* [Results & Exit Codes](results-and-exit-codes.md) — the `lxm/result/v1` JSON envelope and the full exit-code catalog.
* [Manifest Reference](manifest.md) — every manifest field.
* [Environment Variables](environment-variables.md) — `LXM_*` variables and precedence.
