# Disposable Test Agents

This tutorial runs a small **fleet lifecycle end to end**: two identical test agents built from one shared base, applied in parallel as a group, used to run a job, then torn down with `--prune` until nothing is left. It is the pattern for CI runners, job workers, and any fleet you stand up and take down repeatedly.

It takes about twenty-five minutes. When you finish you will have seen the complete loop — create, use, retire — and a host with no leftover state.

The fleet lives in `docs/examples/agents/`; every command below is verified against the shipped binary.

## Before you begin

* lxm installed ([Installation](../getting-started/installation.md))
* LXD 5.0+ installed and running, with your user in the `lxd` group
* An `ubuntu:22.04` image available to LXD:

```bash
lxc image copy ubuntu:22.04 local: --alias ubuntu:22.04
```

* The [Quick Start](../getting-started/quickstart.md) or [Your First Dev Container](first-dev-container.md) — a single apply should be familiar.

## 1. Create the fleet

```bash
mkdir -p ~/agents && cd ~/agents
cp <guide-checkout>/docs/examples/agents/_base.yaml .
cp <guide-checkout>/docs/examples/agents/agent01.yaml .
cp <guide-checkout>/docs/examples/agents/agent02.yaml .
```

Or write them by hand. The base is the familiar shared defaults:

```yaml
schema: lxm/config/v2
base: true
user: ubuntu
wait:
  cloud_init: 10m
  network: 60s
```

Each agent is a two-line declaration on top of it:

```yaml
# agent01.yaml
schema: lxm/config/v2
include:
  - _base.yaml
name: ci-agent-01
status: present
state: running
image: ubuntu:22.04
groups: [ci]
user: ubuntu
sudo: false
inject_ssh_keys: true
```

```yaml
# agent02.yaml
schema: lxm/config/v2
include:
  - _base.yaml
name: ci-agent-02
status: present
state: running
image: ubuntu:22.04
groups: [ci]
user: ubuntu
sudo: false
inject_ssh_keys: true
```

The only differences between the two agents are their `name`s. Everything else — user, image, group, security posture — comes from the base. Both are tagged with the group `ci`, which is what lets you address them together.

## 2. Preview the whole fleet

Point `lxm plan` at the **directory** (not one file) and it plans every manifest in it:

```text
$ lxm plan .
time=2026-08-09T12:14:00.847+10:00 level=WARN msg="Skipping base config file; remove _ prefix or add 'base: true' if intended as a container" file=_base.yaml
Plan: 2 to create, 0 to update, 0 to recreate, 0 to delete, 0 noop across 2 manifest(s)
$ echo $?
0
```

Two containers to create, across two manifests. (The `WARN` line is lxm noticing the `_base.yaml` is a base file, not a container — expected.)

## 3. Apply in parallel

```text
$ lxm apply .
time=2026-08-09T12:14:04.814+10:00 level=WARN msg="Skipping base config file; remove _ prefix or add 'base: true' if intended as a container" file=_base.yaml
Applied 2 step(s) across 2 container(s)
$ echo $?
0
```

Both agents were created in parallel — `2 step(s) across 2 container(s)`. A fleet of dozens applies just as fast.

## 4. See them as a group

The `ci` group selects both at once:

```text
$ lxm list -g ci
time=2026-08-09T12:14:15.312+10:00 level=INFO msg="Group filter enabled" groups=[ci]
NAME         STATUS   MANAGED  GROUPS  IMAGE   IP
ci-agent-01  Running  true     ci      ubuntu  10.171.13.131
ci-agent-02  Running  true     ci      ubuntu  10.171.13.242
$ echo $?
0
```

Group selectors are how you target a whole fleet — or a subset — with one command. See [Targeting with Selectors](../howto/fleet-selectors.md).

## 5. Run a job on both

Write a small job script and run it across the `ci` group:

```bash
mkdir -p scripts
cat > scripts/smoke.sh <<'EOF'
#!/bin/bash
echo "smoke test ok on $(hostname)"
EOF
```

```text
$ lxm run . scripts/smoke.sh -g ci
time=2026-08-09T12:14:19.332+10:00 level=INFO msg="Group filter enabled" groups=[ci]
time=2026-08-09T12:14:19.336+10:00 level=WARN msg="Skipping base config file; remove _ prefix or add 'base: true' if intended as a container" file=_base.yaml
$ echo $?
0
```

`run` executed the script on every manifest in the target that matches the selector; exit `0` means it succeeded on all of them. To see an individual agent's output, target it directly:

```text
$ lxm script ci-agent-01 scripts/smoke.sh
smoke test ok on ci-agent-01
```

Your two disposable agents just ran their job. Now tear them down.

## 6. Retire one agent

Delete one manifest — the agent it declared is now an **orphan**: still running, still managed, but with no manifest in the fleet:

```bash
rm agent02.yaml
```

Preview the prune without deleting anything:

```text
$ lxm plan . --prune -g ci
time=2026-08-09T12:14:34.739+10:00 level=INFO msg="Group filter enabled" groups=[ci]
time=2026-08-09T12:14:34.739+10:00 level=WARN msg="Skipping base config file; remove _ prefix or add 'base: true' if intended as a container" file=_base.yaml
Plan: 0 to create, 0 to update, 0 to recreate, 1 to delete, 1 noop across 1 manifest(s)
$ echo $?
0
```

`1 to delete` is the orphaned `ci-agent-02`; `1 noop` is the surviving `ci-agent-01`. The `-g ci` selector scopes the prune to your agents, so nothing else on the host is at risk.

## 7. Apply the prune

```text
$ lxm apply . --prune -g ci
time=2026-08-09T12:14:34.891+10:00 level=INFO msg="Group filter enabled" groups=[ci]
time=2026-08-09T12:14:34.891+10:00 level=WARN msg="Skipping base config file; remove _ prefix or add 'base: true' if intended as a container" file=_base.yaml
Applied 2 step(s) across 1 container(s)
$ echo $?
0
```

`ci-agent-02` is gone. Verify:

```text
$ lxm list -g ci
NAME         STATUS   MANAGED  GROUPS  IMAGE   IP
ci-agent-01  Running  true     ci      ubuntu  10.171.13.131
```

!!! warning

    `--prune` deletes containers permanently. Its scope is strictly **target directory ∩ active selectors**: every managed container in scope with no manifest is deleted — including containers other fleets share on the same LXD host. Always preview with `plan --prune` first and bound it with `--group`/`--name`, exactly as this tutorial does. See [Pruning Orphans](../howto/prune-orphans.md).

## 8. Tear down the last agent

`--prune` needs **at least one manifest** in the target directory to scope itself — once every manifest is gone, an empty directory is a no-target error (`exit 5`). So for the final agent, retire it declaratively instead: set `status: absent` in its manifest and `apply`. That is a plain reconcile, works on a single file, and needs no anchor manifest:

```yaml
# agent01.yaml
schema: lxm/config/v2
include:
  - _base.yaml
name: ci-agent-01
status: absent
user: ubuntu
groups: [ci]
sudo: false
inject_ssh_keys: false
```

```text
$ lxm plan agent01.yaml
Plan: 0 to create, 0 to update, 0 to recreate, 1 to delete, 0 noop across 1 manifest(s)
$ lxm apply agent01.yaml
Applied 1 step(s) across 1 container(s)
```

Both agents are now gone:

```text
$ lxm list -g ci
time=2026-08-09T12:23:48.480+10:00 level=INFO msg="Group filter enabled" groups=[ci]
Error: no containers found matching filter criteria
$ echo $?
5
```

Your disposable agents existed, did their job, and left nothing behind.

!!! note

    Either retirement path is valid: `--prune` for an orphan whose manifest is already deleted (it just needs one other manifest in the directory to scope against), or `status: absent` for the last container. See [Pruning Orphans](../howto/prune-orphans.md) for the scope rules that make `--prune` safe.

## What you built

* A two-container fleet from one shared base — two names, everything else inherited.
* Parallel fleet apply and group-targeted job execution.
* Scoped `--prune` teardown that removes exactly what you retired.

This is the whole point of declarative fleets: standing a fleet up is a directory of small files, and taking it down is deleting those files and pruning.

## Next steps

* [Targeting with Selectors](../howto/fleet-selectors.md) — the selector algebra behind `-g ci`.
* [Pruning Orphans](../howto/prune-orphans.md) — prune scope rules and the safety contract.
* [Automating in CI](../howto/automate-ci.md) — run this lifecycle from a pipeline.
