# Pruning Orphans

This guide shows you how to garbage-collect managed containers that no longer have a manifest — with the strict scoping rules that keep `--prune` safe.

## Prerequisites

* lxm installed ([Installation](../getting-started/installation.md))
* A working LXD host
* [Targeting with Selectors](fleet-selectors.md) (selectors bound the prune)

## 1. What an orphan is

An **orphan** is a live container that:

* is managed by lxm (`user.lxm.managed=true` — created by an lxm `apply`), **and**
* matches the active selector set, **and**
* is **not** declared by any manifest in the target directory.

You create one by deleting a container's manifest from a fleet (or removing `status: present` for it). The container itself keeps running; only its declaration is gone.

## 2. Preview with `plan --prune` (safe, non-mutating)

`plan --prune` shows the delete steps without deleting anything. Start here:

```text
$ lxm plan config/ --prune
Plan: 1 to create, 0 to update, 0 to recreate, 3 to delete, 0 noop across 1 manifest(s)
```

The `3 to delete` line is the orphans lxm would remove.

## 3. Understand the scope — and the trap

**Prune scope = target directory ∩ active selectors.** Every managed container not declared in the targeted directory is a candidate — not just the ones you think of as "this fleet". On a host shared with other fleets, an unscoped prune will propose deleting *their* managed containers too:

```text
$ lxm plan docs/examples/ --prune
Plan: 6 to create, 0 to update, 0 to recreate, 2 to delete, 1 noop across 7 manifest(s)
```

Here `docs/examples/` declares seven manifests, and the `2 to delete` are two **other** managed containers on this host that no manifest in that directory declares. If you ran `apply --prune` here, they would be deleted.

## 4. Bound the prune with selectors

Add `--group` or `--name` to scope the delete list. The selector applies to both the manifests and the orphan candidates, so you only prune what you intended:

```text
$ lxm plan config/ --prune -g dev
time=2026-08-09T10:17:39.499+10:00 level=INFO msg="Group filter enabled" groups=[dev]
Plan: 1 to create, 0 to update, 0 to recreate, 1 to delete, 0 noop across 1 manifest(s)
```

The `-g dev` selector narrowed the orphans to the single `dev`-group container whose manifest you removed; the unrelated managed containers on the shared host are no longer candidates.

!!! warning

    `--prune` deletes containers permanently. Scope is strictly **target directory ∩ selectors** — it will delete every managed container in scope that has no manifest, including containers that belong to other fleets sharing the same LXD host. Always preview with `plan --prune` first, and combine with `--group`/`--name` to bound it.

## 5. Apply the prune

Once the preview shows exactly the containers you intend to remove, execute it:

```bash
lxm apply config/ --prune -g dev
```

A single-file target never accepts `--prune`:

```text
$ lxm plan config/dev.yaml --prune
Error: --prune is only allowed on directory targets
$ echo $?
2
```

## When things go wrong

| Error | Cause | Fix |
|---|---|---|
| `--prune is only allowed on directory targets` (exit 2) | `--prune` on a single file | Target the directory instead. |
| Prune proposes deleting containers you wanted to keep | They are managed but not declared in this directory | Move/keep their manifests in the target directory, or scope with selectors. |
| Prune deletes nothing | The orphan does not match the active selectors | Widen the selector; orphans must match `target dir ∩ selectors`. |
| `target not found: no manifests found matching filter criteria` (exit 5) | The selector matches no manifests in the dir | Keep at least one matching manifest in the directory you prune. |

## Next steps

* [Targeting with Selectors](fleet-selectors.md) — how `--group`/`--name` scope the prune.
* [Snapshots & Rollback](snapshots-and-rollback.md) — snapshot before you prune a container you might regret.
* [CLI Reference](../reference/cli.md#prune-scope-rules) — the formal prune scope rules.
