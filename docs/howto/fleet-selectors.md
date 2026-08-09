# Targeting with Selectors

This guide shows you how to operate on a subset of your fleet using the selector flags `--group`, `--exclude-group`, and `--name`. Selectors work on `plan`, `apply`, `run`, and `list`.

## Prerequisites

* lxm installed ([Installation](../getting-started/installation.md))
* A fleet with group-tagged manifests ([Authoring Manifests](author-manifests.md))

## 1. Groups

Containers are tagged with groups in their manifest:

```yaml
# config/web-01.yaml
schema: lxm/config/v2
include:
  - _base.yaml
name: web-01
status: present
image: ubuntu:22.04
groups: [web]
```

The groups in this guide's `docs/examples/` fleet are `dev` (dev-station), `recipes` (recipes-demo), and `demo` (cloud-init-demo, inheritance-demo, absent-demo); `mounts-demo` and `mounts-map` have none.

## 2. Filter by one group

`-g, --group` selects containers in any listed group (OR union):

```text
$ lxm plan docs/examples/ -g demo
time=2026-08-09T10:16:21.796+10:00 level=INFO msg="Group filter enabled" groups=[demo]
Plan: 2 to create, 0 to update, 0 to recreate, 0 to delete, 1 noop across 3 manifest(s)
```

Only the three `demo`-group manifests were considered. (The `1 noop` is `absent-demo`: its container is already absent, so nothing to do.)

## 3. OR across groups

Repeated `--group` flags or a comma-separated list are a union:

```text
$ lxm plan docs/examples/ -g demo,recipes
Plan: 3 to create, 0 to update, 0 to recreate, 0 to delete, 1 noop across 4 manifest(s)
```

The `demo` and `recipes` manifests (4 total) are selected.

## 4. AND with a name pattern

Combining `--group` and `--name` is an intersection — both must match:

```text
$ lxm plan docs/examples/ -g dev --name dev-station
time=2026-08-09T10:16:21.881+10:00 level=INFO msg="Group filter enabled" groups=[dev]
Plan: 1 to create, 0 to update, 0 to recreate, 0 to delete, 0 noop across 1 manifest(s)
```

`--name` is a regular expression. A bare value like `dev-station` is anchored (`^dev-station$`), so `dev-station` matches but `dev` alone would not.

## 5. Exclude a group

`--exclude-group` removes containers matching any listed group, applied before the other criteria:

```text
$ lxm plan docs/examples/ --exclude-group demo
Plan: 4 to create, 0 to update, 0 to recreate, 0 to delete, 0 noop across 4 manifest(s)
```

All manifests except the three `demo` ones are considered.

## 6. Empty matches fail with exit 5

A selector that matches zero manifests fails fast with `exit 5` (`TARGET_NOT_FOUND`) — the same code as a missing target:

```text
$ lxm plan docs/examples/ -g demo --name zzz
Error: target not found: no manifests found matching filter criteria
$ echo $?
5
```

`list` behaves the same way against the live inventory:

```text
$ lxm list --name zzz
Error: no containers found matching filter criteria
$ echo $?
5
```

This is deliberate: pipelines can branch on `5` to mean "nothing matched" instead of treating it as success.

## When things go wrong

| Error | Cause | Fix |
|---|---|---|
| `target not found: no manifests found matching filter criteria` | The selector matches zero manifests | Widen the group or fix the name pattern. |
| `no containers found matching filter criteria` (on `list`) | The selector matches zero live containers | Check the container exists and its groups (`lxm status <name>`). |
| `--prune` deletes more than expected | Prune scope is target-dir ∩ selectors | Combine `--prune` with `--group`/`--name`; see [Pruning Orphans](prune-orphans.md). |

## Next steps

* [Pruning Orphans](prune-orphans.md) — selectors bound the destructive `--prune` operation.
* [Automating in CI](automate-ci.md) — selectors make fleet-wide operations scriptable.
* [CLI Reference](../reference/cli.md#selector-algebra) — the full selector algebra.
