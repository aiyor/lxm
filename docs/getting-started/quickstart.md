# Quick Start

This tour takes about ten minutes. You will declare one container, preview the change lxm wants to make, apply it, and then inspect and shell into the running container — the complete day-to-day loop:

```
init → plan → apply → list → status → shell
```

At the end you have a running container that lxm manages for you.

## Before you begin

You need:

* lxm installed ([Installation](installation.md))
* LXD 5.0+ installed and running
* Your user in the `lxd` group
* An Ubuntu base image available to LXD

lxm creates containers from images that are already present in your LXD host (it refers to them by alias or fingerprint). If you have not pulled an image yet, fetch one now — this is a one-time step:

```bash
lxc image copy ubuntu:22.04 local: --alias ubuntu:22.04
```

You can use any image you already have instead; the rest of this tour only assumes the image is available.

## 1. Initialize a fleet directory

`lxm init` scaffolds the two files that make up a minimal fleet in your current directory:

```bash
$ lxm init
Initialized lxm fleet in .:
  - _base.yaml
  - config/dev.yaml
```

**`_base.yaml`** holds the shared defaults every container inherits:

```yaml
schema: lxm/config/v2
base: true
user: ubuntu
wait:
  cloud_init: 10m
  network: 60s
```

**`config/dev.yaml`** declares your first container:

```yaml
schema: lxm/config/v2
include:
  - ../_base.yaml
name: dev-station
status: present
image: ubuntu:22.04
groups: [dev]
```

That manifest says: *"a container named `dev-station` should exist, built from `ubuntu:22.04`, tagged with the group `dev`, and it inherits the shared defaults from `_base.yaml`."* Nothing is running yet — this is just a declaration of the state you want.

## 2. Preview the plan

`lxm plan` reads your manifests, compares them against what is actually in your LXD host, and prints the difference — without changing anything:

```bash
$ lxm plan config/dev.yaml
Plan: 1 to create, 0 to update, 0 to recreate, 0 to delete, 0 noop across 1 manifest(s)
$ echo $?
0
```

One container needs to be created, nothing else. `plan` is your safety net: you always see exactly what would change before it changes.

## 3. Apply it

`lxm apply` executes the plan. It creates the container from the base image and starts it; this is the step that actually changes your LXD host:

```bash
$ lxm apply config/dev.yaml
Applied 1 step(s) across 1 container(s)
$ echo $?
0
```

Your container now exists. The exit code `0` means the apply succeeded.

## 4. See your fleet

`lxm list` shows the containers lxm can see. Filtering by name keeps the output focused on your new container:

```bash
$ lxm list --name dev-station
NAME         STATUS   MANAGED  GROUPS  IMAGE   IP
dev-station  Running  true     dev     ubuntu  10.171.13.47
$ echo $?
0
```

Without `--name`, `lxm list` shows every container on your host. The `MANAGED` column is `true` here — lxm tracks this container as part of your fleet and keeps it matched to its manifest from now on. Your IP address will differ, since LXD assigns it from your `lxdbr0` bridge.

For the details lxm tracks, use `lxm status`:

```bash
$ lxm status dev-station
Container:     dev-station
Status:        Running (Code 103)
Architecture:  x86_64
IP Address:    10.171.13.47
Managed:       true
Groups:        dev

Recipe Hash Trail:
  (none)
$ echo $?
0
```

## 5. Open a shell

`lxm shell` opens an interactive shell inside the container through LXD itself:

```bash
$ lxm shell dev-station
root@dev-station:~# exit
logout
```

You are in the container. Type `exit` to leave.

## You now have a managed container

* Your desired state lives in `config/dev.yaml` — one small file, versionable like code.
* Run `lxm plan config/dev.yaml` and `lxm apply config/dev.yaml` any time to bring the live container back to what the manifest declares.
* Add more containers by adding more files under `config/`, or shared defaults in `_base.yaml`.

## Next steps

* [Concepts](concepts.md) — what it means to manage a fleet declaratively, and why the plan-first loop is safe.
* [Authoring Manifests](../howto/author-manifests.md) — mounts, networking, users, and provisioning.
* [Your First Dev Container](../tutorials/first-dev-container.md) — a full end-to-end tutorial that builds on this tour.
