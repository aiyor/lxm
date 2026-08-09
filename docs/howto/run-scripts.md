# Running Scripts

This guide shows you how to execute a local script inside one container or across a whole fleet with `lxm run` and `lxm script` — the ad-hoc counterpart to the declarative recipe system.

## Prerequisites

* lxm installed ([Installation](../getting-started/installation.md))
* A running container, or a fleet directory of manifests ([Authoring Manifests](author-manifests.md))

## 1. Choose the right command

| Command | Runs the script on |
|---|---|
| `lxm script <name> <path> [user]` | A single named container |
| `lxm run <target> <path>` | A container **or** a fleet directory (all matching manifests) |

Both execute the local file inside the container via the LXD exec API (`/bin/bash -l -c <script bytes>`), so the script does not need to be copied into the container first.

## 2. Run a script on one container

```text
$ lxm script glm scripts/install.sh
```

Preview first with `--dry-run`:

```text
$ lxm script glm scripts/install.sh --dry-run
time=2026-08-09T10:17:52.460+10:00 level=INFO msg="Dry-run mode enabled. No changes will be made."
[DRY-RUN] Would run script "scripts/install.sh" on container "glm"
```

## 3. Run a script across a fleet

`lxm run` accepts a manifest file, a directory, or a single container name. Against a directory it targets every manifest there (respecting selectors):

```text
$ lxm run docs/examples/ recipes/bootstrap.sh --dry-run
time=2026-08-09T10:17:52.444+10:00 level=INFO msg="Dry-run mode enabled. No changes will be made."
[DRY-RUN] Would run script "recipes/bootstrap.sh" on target "docs/examples/"
```

Scope it with the selector flags just like `plan`/`apply`:

```bash
lxm run config/ scripts/cleanup.sh -g web
```

## 4. Control the user and environment

* **`--run-as`** — run the script as a specific user (default `root`). `lxm script` also accepts the user as a positional argument: `lxm script <name> <path> <user>`.
* **`--env KEY=VAL`** — repeatable environment variables delivered to the script. Keys must match the POSIX identifier regex `^[a-zA-Z_][a-zA-Z0-9_]*$`, or lxm fails with `exit 3`:

```text
$ lxm script glm x.sh --env '1BADKEY=value' --dry-run
time=2026-08-09T10:18:24.042+10:00 level=INFO msg="Dry-run mode enabled. No changes will be made."
Error: invalid POSIX environment variable key "1BADKEY"
$ echo $?
3
```

```bash
lxm script glm scripts/backup.sh --env BACKUP_DIR=/srv/backup --env LOG_LEVEL=info
```

## 5. Branch on the exit code

A script that exits non-zero fails the command with `exit 6` (`EXEC_FAILED`) and its stderr is surfaced in the error message. In a shell:

```bash
if lxm script glm scripts/backup.sh; then
  echo "script ok"
else
  echo "script failed with exit $?"
fi
```

In CI, that is the signal to fail the job. See [Automating in CI](automate-ci.md).

## When things go wrong

| Error | Cause | Fix |
|---|---|---|
| `container "X" not found` (exit 5) | The container does not exist | Check the name with `lxm list`. |
| `target container or directory "X" not found` (exit 5) | `run` target is neither a container nor a fleet dir | Pass a real container name or manifest directory. |
| `invalid POSIX environment variable key "..."` (exit 3) | An `--env` key is not a valid identifier | Rename the key to `[a-zA-Z_][a-zA-Z0-9_]*`. |
| Script fails with `exit 6` | The script exited non-zero | Read the script's stderr; it runs via `/bin/bash -l -c`. |
| `--run-as` user missing inside the container | The user does not exist yet | Create the user (via the manifest `user`/cloud-init) first. |

## Next steps

* [Provisioning with Recipes](provision-with-recipes.md) — make scripts declarative and idempotent.
* [Interactive Shell & SSH](interact-shell-ssh.md) — interactive access to the same containers.
* [CLI Reference](../reference/cli.md#lxm-run) — the run/script command reference.
