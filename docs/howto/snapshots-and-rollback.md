# Snapshots & Rollback

This guide shows you how to take snapshots, roll a container back to one, and clean up old snapshots with GC. lxm takes snapshots automatically before running recipes, so a failed provisioning step is always one command away from an undo.

## Prerequisites

* lxm installed ([Installation](../getting-started/installation.md))
* A working LXD host
* [Provisioning with Recipes](provision-with-recipes.md) (recipes create the automatic snapshots)

## 1. Create a snapshot

```bash
lxm snapshot create glm pre-upgrade
```

Preview what it would do with `--dry-run` (the verified transcript below uses a live container):

```text
$ lxm snapshot create glm pre-upgrade --dry-run
time=2026-08-09T10:24:08.571+10:00 level=INFO msg="Dry-run mode enabled. No changes will be made."
[DRY-RUN] Would create snapshot "pre-upgrade" for instance "glm"
```

## 2. List snapshots

```text
$ lxm snapshot list glm
SNAPSHOT  STATEFUL  CREATED
```

The pre-recipe snapshots lxm takes automatically use the `user.lxm.snap.` prefix and show up here. For example, after a recipe run:

```text
$ lxm snapshot list recipe-demo
SNAPSHOT                                       STATEFUL  CREATED
user.lxm.snap.recipe-demo-1786234954443424477  false     2026-08-09T00:22:34Z
```

## 3. Roll back to a snapshot

`rollback` restores the container filesystem and state to the snapshot:

```bash
lxm rollback glm pre-upgrade
```

Preview with `--dry-run`:

```text
$ lxm rollback glm pre-upgrade --dry-run
time=2026-08-09T10:24:08.592+10:00 level=INFO msg="Dry-run mode enabled. No changes will be made."
[DRY-RUN] Would restore container "glm" to snapshot "pre-upgrade"
```

!!! warning

    `rollback` discards every change made since the snapshot — files, package installs, config. It does **not** consult the manifest first. After rolling back, run `lxm apply` again to bring the container back to your declared state (which may re-run recipes if their hashes no longer match).

## 4. Delete a snapshot

```bash
lxm snapshot delete glm pre-upgrade
```

Preview with `--dry-run`:

```text
$ lxm snapshot delete glm pre-upgrade --dry-run
time=2026-08-09T10:24:08.582+10:00 level=INFO msg="Dry-run mode enabled. No changes will be made."
[DRY-RUN] Would delete snapshot "pre-upgrade" for instance "glm"
```

## 5. Garbage-collect old snapshots

`snapshot gc` prunes aged snapshots matching a prefix. The default prefix is `user.lxm.snap.` (the automatic pre-recipe snapshots), and `--older-than` takes a duration:

```text
$ lxm snapshot gc glm --older-than 7d --dry-run
time=2026-08-09T10:24:08.582+10:00 level=INFO msg="Dry-run mode enabled. No changes will be made."
[DRY-RUN] Would prune 0 snapshot(s) matching prefix "user.lxm.snap."
```

Without a container argument, `gc` scans every instance on the host. `--older-than` accepts Go durations (`24h`) and day suffixes (`7d`).

Run it for real without `--dry-run` when you are ready to prune:

```bash
lxm snapshot gc glm --older-than 7d
```

!!! warning

    `snapshot gc` deletes snapshots permanently. Preview with `--dry-run` first, and remember the automatic recipe snapshots are your primary rollback point — pruning them removes that safety net for older containers.

## When things go wrong

| Error | Cause | Fix |
|---|---|---|
| `container "X" not found` (exit 5) | The container name is wrong or the container is gone | Check `lxm list`. |
| `snapshot create requires container name and snapshot name` | Missing arguments | Pass `<container> <snapshot>`. |
| Rollback did not restore what you expected | You rolled back to the wrong snapshot, or state changed after it | List snapshots (`lxm snapshot list <name>`) and reconcile with `lxm apply`. |
| `rebuild: WARNING — all instance snapshots will be permanently destroyed (requires --force)` | A re-apply wants to recreate, which destroys snapshots | Take a manual snapshot first, then decide on `--force` knowingly (see [Provisioning with Recipes](provision-with-recipes.md)). |

## Next steps

* [Provisioning with Recipes](provision-with-recipes.md) — how the automatic snapshots are created.
* [Pruning Orphans](prune-orphans.md) — deleting whole containers with `--prune`.
* [CLI Reference](../reference/cli.md#lxm-snapshot) — the snapshot and rollback commands.
