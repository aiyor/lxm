# Your First Dev Container

This tutorial builds the dev container the [Quick Start](../getting-started/quickstart.md) promised you could have: a single managed container with **your project directory mounted in**, a **predictable static IP**, a **provisioning recipe** that installs your tools, and a **snapshot / rollback beat** so a bad change is one command away from an undo.

It takes about twenty minutes. When you finish, `dev-station` is a real, running, lxm-managed container you can reconcile, rebuild, and roll back — the pattern you will reuse for every container after this one.

The complete fleet this tutorial uses lives in `docs/examples/first-dev/` in the repository; every step below is verified against the shipped binary.

## Before you begin

You need the same things the quickstart asked for:

* lxm installed ([Installation](../getting-started/installation.md))
* LXD 5.0+ installed and running, with your user in the `lxd` group
* An `ubuntu:22.04` image available to LXD:

```bash
lxc image copy ubuntu:22.04 local: --alias ubuntu:22.04
```

You will also follow three how-to guides along the way — this tutorial shows you the journey, and those pages carry the "how". They are linked at each step.

## 1. Create the fleet directory

A fleet is just a directory of manifests. Make one for this tutorial and copy in the example fleet:

```bash
mkdir -p ~/first-dev && cd ~/first-dev
cp <guide-checkout>/docs/examples/first-dev/_base.yaml .
cp <guide-checkout>/docs/examples/first-dev/dev-station.yaml .
cp -r <guide-checkout>/docs/examples/first-dev/recipes .
```

(`<guide-checkout>` is wherever you keep the lxm repository. If you do not have the repo checked out, write the two files below by hand — they are the whole fleet.)

The shared base, `_base.yaml`, holds the defaults every container inherits:

```yaml
schema: lxm/config/v2
base: true
user: ubuntu
wait:
  cloud_init: 10m
  network: 60s
```

`dev-station.yaml` declares the container. Read it top to bottom — this is the whole point of the tutorial:

```yaml
schema: lxm/config/v2
include:
  - _base.yaml
name: dev-station
status: present
state: running
image: ubuntu:22.04
groups: [dev]
user: ubuntu
sudo: false
inject_ssh_keys: true
mounts:
  - source: /tmp/projects
    path: /workspace
networks:
  - name: eth0
    ipv4: 10.171.13.200
    parent: lxdbr0
recipes:
  - run_as: root
    scripts:
      - recipes/setup.sh
wait:
  cloud_init: 5m
  required: true
```

Each block is one of the four capabilities you are learning today:

| Block | What it does | Where the "how" lives |
|---|---|---|
| `mounts:` | Shares the host directory `/tmp/projects` into the container at `/workspace` | [Mounting Host Directories](../howto/mount-host-dirs.md) |
| `networks:` | Gives the container a static address `10.171.13.200` on `lxdbr0` | [Configuring Networking](../howto/configure-networking.md) |
| `recipes:` | Runs `recipes/setup.sh` after boot to install `jq` and `ripgrep` | [Provisioning with Recipes](../howto/provision-with-recipes.md) |
| `wait:` | Holds recipes until cloud-init finishes, failing closed on timeout | [Cloud-Init Bootstrapping](../howto/cloud-init-bootstrapping.md) |

!!! note

    The static address must be **inside your bridge's subnet** and unused. The example uses `10.171.13.200` because the guide's dev host bridges on `10.171.13.0/24`; check yours with `lxc network show lxdbr0` and pick a free address from it (see [Configuring Networking](../howto/configure-networking.md)).

## 2. Create the host mount source

The mount source must exist on your host before lxm will plan it. That is the only host-side preparation — everything else lives in the two manifest files:

```bash
mkdir -p /tmp/projects
```

## 3. Preview the plan

`lxm plan` reads `dev-station.yaml`, compares it against your live LXD host, and prints the difference — without changing anything:

```text
$ lxm plan dev-station.yaml
Plan: 1 to create, 0 to update, 0 to recreate, 0 to delete, 0 noop across 1 manifest(s)
$ echo $?
0
```

One container needs to be created, nothing else. This is your safety net: you always see exactly what would change before it changes.

## 4. Apply — create the container, run the recipe

`lxm apply` executes the plan. It creates the container from `ubuntu:22.04`, starts it, waits for cloud-init, takes a pre-recipe snapshot, and runs the setup recipe:

```text
$ lxm apply dev-station.yaml
Applied 1 step(s) across 1 container(s)
$ echo $?
0
```

Behind that one line, lxm ran `recipes/setup.sh` — `apt-get install jq ripgrep` — and recorded a hash of the script so it can skip the recipe next time.

## 5. See your container in the fleet

```text
$ lxm list --name dev-station
NAME         STATUS   MANAGED  GROUPS  IMAGE   IP
dev-station  Running  true     dev     ubuntu  10.171.13.200
$ echo $?
0
```

`MANAGED` is `true`: lxm owns this container and will keep it matched to its manifest. The static IP you declared is the one it got.

`lxm status` shows the details lxm tracks — including the recipe's idempotency hash:

```text
$ lxm status dev-station
Container:     dev-station
Status:        Running (Code 103)
Architecture:  x86_64
IP Address:    10.171.13.200
Managed:       true
Groups:        dev

Recipe Hash Trail:
  - recipes_setup_sh: 368e264c14778e1e638bd15140c946f30d69896de514b0f7b9894aaab261aa9a
```

The `recipes_setup_sh` hash is the record that the recipe already ran. Change the script and the hash changes; the recipe runs again. Keep it the same and a re-`apply` is a no-op. That is [idempotency](../getting-started/concepts.md) in action.

## 6. Verify the mount and the tools

The mount should be live at `/workspace`, and the recipe's tools on `PATH`. Open a shell:

```text
$ lxm shell dev-station
root@dev-station:~# which jq rg
/usr/bin/jq
/usr/bin/rg
root@dev-station:~# ls /workspace
root@dev-station:~# exit
logout
```

`jq` and `rg` are installed by the recipe, and `/workspace` is your mounted `/tmp/projects`. Everything you declared is real.

## 7. Snapshot before a change

Now the safety net. Before you try something risky in the container, take a named snapshot you can roll back to:

```text
$ lxm snapshot create dev-station before-edits
Created snapshot "before-edits" for instance "dev-station"
$ echo $?
0
```

You now have two snapshots: your manual `before-edits`, and the automatic `user.lxm.snap.*` one lxm took before the recipe ran. List them:

```text
$ lxm snapshot list dev-station
SNAPSHOT                                       STATEFUL  CREATED
user.lxm.snap.dev-station-1786241327744973521  false     2026-08-09T02:08:47Z
before-edits                                   false     2026-08-09T02:09:18Z
```

## 8. Break something

Simulate a change that goes wrong — remove a tool the recipe installed:

```bash
cat > break.sh <<'EOF'
#!/bin/bash
set -euo pipefail
apt-get remove -y -qq jq >/dev/null
echo "jq removed"
EOF
```

```text
$ lxm script dev-station break.sh
jq removed
$ echo $?
0
```

And the damage is real:

```text
$ lxc exec dev-station -- which jq
$ echo $?
1
```

## 9. Roll back

One command restores the container to the snapshot:

```text
$ lxm rollback dev-station before-edits
Successfully restored container "dev-station" to snapshot "before-edits"
$ echo $?
0
```

Verify the tool is back:

```text
$ lxc exec dev-station -- which jq
/usr/bin/jq
```

Your container is exactly as it was before the bad change — files, packages, and state.

!!! warning

    `rollback` discards **everything** since the snapshot and does **not** consult the manifest first. After rolling back, run `lxm apply` to bring the container back to your declared state — and note that rolling back past a pre-recipe snapshot removes the recipe's stored hash, so the next `apply` re-runs the recipe. See [Snapshots & Rollback](../howto/snapshots-and-rollback.md).

## What you built

* A declarative fleet in two small files (`_base.yaml` + `dev-station.yaml`).
* A container with a host mount, a static IP, and provisioned tooling.
* A repeatable safety cycle: snapshot → change → roll back.
* An idempotent `apply` you can run any time to reconcile drift.

Your fleet is in `~/first-dev/`. Commit the two manifests and the recipe to git, and this container is reproducible anywhere lxm runs.

## Next steps

* [The ML Workstation](ml-workstation.md) — scale the same pattern to a realistic multi-mount workstation.
* [Disposable Test Agents](disposable-agents.md) — turn it into a fleet that cleans up after itself.
* [Authoring Manifests](../howto/author-manifests.md) — inheritance, `remove`, `replace`, and fleet structure.
