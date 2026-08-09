# Provisioning with Recipes

This guide shows you how to run provisioning scripts inside a container during `apply` — with snapshot-before-recipe safety, content-hash idempotency, and the `--force` re-run gate.

## Prerequisites

* lxm installed ([Installation](../getting-started/installation.md))
* A working LXD host
* [Authoring Manifests](author-manifests.md) and the [Manifest Reference](../reference/manifest.md#recipes) recipes section

## 1. Declare recipes in the manifest

Recipes are groups of scripts, each with a `run_as` user (default `root`):

```yaml
# config/dev.yaml
schema: lxm/config/v2
include:
  - ../_base.yaml
name: recipe-demo
status: present
image: ubuntu:22.04
groups: [demo]
recipes:
  - run_as: root
    scripts:
      - recipes/hello.sh
  - run_as: dev
    scripts:
      - recipes/user-setup.sh
```

Script paths resolve relative to the **manifest's directory** (`config/` in this example), so `recipes/hello.sh` means `config/recipes/hello.sh`. Keep your scripts next to the manifests, like the repository's own `config/recipes/`.

Script shorthands are also accepted: a bare path (`- recipes/hello.sh`) means run as `root`, and `root:` groups the same way. All normalize to the object form.

A minimal script looks like this:

```bash
#!/bin/bash
# config/recipes/hello.sh
set -euo pipefail
echo "hello from recipe"
```

## 2. Apply — lxm runs the recipes

On the first `apply`, lxm creates the container, waits for cloud-init, runs each recipe, and records a content hash for idempotency:

```text
$ lxm apply config/dev.yaml
Applied 1 step(s) across 1 container(s)
```

Before the first recipe runs, lxm takes a snapshot named `user.lxm.snap.<container>-<timestamp>`, so a failed provisioning step has a one-command undo:

```text
$ lxm snapshot list recipe-demo
SNAPSHOT                                       STATEFUL  CREATED
user.lxm.snap.recipe-demo-1786234954443424477  false     2026-08-09T00:22:34Z
```

## 3. Verify with status

`lxm status` shows the stored hash for every recipe — this is the idempotency record:

```text
$ lxm status recipe-demo
Container:     recipe-demo
Status:        Running (Code 103)
Architecture:  x86_64
IP Address:    10.171.13.22
Managed:       true
Groups:        demo

Recipe Hash Trail:
  - recipes_hello_sh: 7fa88e111c39acc718c42743e60475b76e2498d9722859a4049a84e57c853a8b
```

The hash key is the cleaned script path; the value is the SHA-256 of the script's bytes. If the script file changes, the hash changes and the recipe runs again on the next apply.

## 4. Re-running: `--force` and the recreate guard

lxm skips a recipe when its stored hash matches the current script bytes — that is the idempotency contract. Two things force it to run again:

* the script file's content changed (new hash), or
* you pass the global `--force` flag: "Re-run recipes even if hashes match".

!!! warning

    `--force` re-executes every recipe regardless of its stored hash. Use it deliberately — provisioning scripts are not always safe to run twice (installers, user creation, and config writes can fail or duplicate state on re-run).

There is a second, related guard you will meet when the container's **image** genuinely changes (you edit `image:` in the manifest): a recreate destroys snapshots, and lxm refuses to destroy them without `--force`:

```text
$ lxm apply config/dev.yaml
Error: rebuild: WARNING — all instance snapshots will be permanently destroyed (requires --force)
```

!!! warning

    Accepting a recreate with `--force` **permanently destroys the container's snapshots**, including the `user.lxm.snap.*` pre-recipe snapshots. If you might need to roll back, take a manual snapshot first (`lxm snapshot create <name> before-rebuild`) and confirm your changes are committed to the manifest.

!!! note

    **Image identity matching.** lxm compares the manifest `image` against the identity LXD recorded at creation (`image.os`/`image.release`, the resolved fingerprint, and the version/description properties). An alias like `ubuntu:22.04` matches a container created from it even though LXD reports the release codename (`ubuntu:jammy`), so re-applying an unchanged manifest is a no-op. Only a real image change plans a recreate.

## 5. Recipe metadata files

Beyond inline groups, a recipe may be a YAML **metadata file** validated against the `lxm/recipe/v1` schema. It wraps one or more scripts with env, retry, sudo, and snapshot settings:

```yaml
# config/recipes/install-mise.yaml
schema: lxm/recipe/v1
name: install-mise
run_as: dev
env:
  MISE_INSTALL_DIR: /opt/mise
sudo: true
snapshot: true
retries: 2
scripts:
  - install-mise.sh
```

Refer to it from the manifest by path instead of a script:

```yaml
recipes:
  - recipes/install-mise.yaml
```

* `name` — used for the idempotency hash key (`user.lxm.recipe.install-mise.hash`).
* `run_as` — defaults to the user who runs the recipe.
* `env` — passed to the script via the LXD exec API environment map (never shell-interpolated). Keys must match `^[a-zA-Z_][a-zA-Z0-9_]*$` or compilation fails with `exit 3`.
* `sudo` — explicit opt-in to passwordless sudo for the script.
* `snapshot` — set `false` to skip the pre-recipe snapshot for this recipe.
* `retries` — number of retries on transient execution failure.
* `scripts` — bash bodies referenced by this metadata file, resolved relative to the metadata file.

## When things go wrong

| Error | Cause | Fix |
|---|---|---|
| `reading script file "..." no such file or directory` | Script path does not resolve relative to the manifest | Check the path; it resolves from the manifest's directory. |
| `invalid POSIX environment variable key "1BADKEY"` | A recipe `env` key is not a valid identifier | Rename the key to match `[a-zA-Z_][a-zA-Z0-9_]*`. |
| `rebuild: WARNING — all instance snapshots will be permanently destroyed (requires --force)` | A recreate is needed and snapshots exist | Take a manual snapshot, then re-apply with `--force` knowingly. |
| Recipe keeps failing with `exit 6` | The script exits non-zero inside the container | Check the script's stderr; scripts run via `/bin/bash -l -c`. |
| `status` shows a stale hash key | A recipe was removed from the manifest | The stale hash is pruned on the next reconcile of that container. |

## Next steps

* [Snapshots & Rollback](snapshots-and-rollback.md) — undo a recipe run with the pre-recipe snapshot.
* [Cloud-Init Bootstrapping](cloud-init-bootstrapping.md) — run setup before recipes via cloud-init.
* [Manifest Reference](../reference/manifest.md#recipes) — the full recipes field reference.
