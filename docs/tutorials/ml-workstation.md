# The ML Workstation

This tutorial builds a realistic data/ML workstation: one container with **many host directories mounted in**, **user-scoped tooling installed by recipes**, and deliberate choices about `sudo` and SSH key injection. It mirrors the fleet the lxm repository itself runs (`config/glm.yaml`, `config/omp.yaml`) — the multi-mount, tool-heavy setup a data engineer actually works in.

It takes about thirty minutes, most of it waiting for tool installs. When you finish, `ml-workstation` is a running workstation you can shell into and work from, with your data, models, and code all mounted.

The complete fleet lives in `docs/examples/ml-workstation/`; every command below is verified against the shipped binary.

## Before you begin

* lxm installed ([Installation](../getting-started/installation.md))
* LXD 5.0+ installed and running, with your user in the `lxd` group
* An `ubuntu-24.04` image available to LXD (the repo's own fleet uses this alias):

```bash
lxc image copy ubuntu:24.04 local: --alias ubuntu-24.04
```

* [Your First Dev Container](first-dev-container.md) or the [Quick Start](../getting-started/quickstart.md) — this tutorial assumes you have seen a single-manifest apply.

## 1. Create the fleet directory

```bash
mkdir -p ~/ml-workstation && cd ~/ml-workstation
cp <guide-checkout>/docs/examples/ml-workstation/_base.yaml .
cp <guide-checkout>/docs/examples/ml-workstation/ml-workstation.yaml .
cp -r <guide-checkout>/docs/examples/ml-workstation/cloud-init .
cp -r <guide-checkout>/docs/examples/ml-workstation/recipes .
```

Or write the files by hand — they are shown in full below.

**`_base.yaml`** — the shared defaults:

```yaml
schema: lxm/config/v2
base: true
user: ml
wait:
  cloud_init: 10m
  network: 60s
```

**`cloud-init/base-ubuntu.yaml`** — the package bootstrap, applied via cloud-init before recipes run:

```yaml
#cloud-config
package_update: true
packages:
  - curl
  - wget
  - git
  - unzip
  - build-essential
  - python3-pip
  - python3-venv
users:
  - default
```

**`ml-workstation.yaml`** — the container. This is the file that makes it a *workstation*:

```yaml
schema: lxm/config/v2
include:
  - _base.yaml
name: ml-workstation
status: present
state: running
image: ubuntu-24.04
groups: [ml]
user: ml
sudo: true
inject_ssh_keys: true
mounts:
  - source: /tmp/ml/data
    path: /mnt/data
  - source: /tmp/ml/models
    path: /mnt/models
  - source: /tmp/ml/code
    path: /mnt/code
  - source: /tmp/ml/notebooks
    path: /mnt/notebooks
  - source: /tmp/ml/sandbox
    path: /mnt/workspace
cloud-init-include:
  - cloud-init/base-ubuntu.yaml
recipes:
  - run_as: root
    scripts:
      - recipes/install-mise.sh
      - recipes/install-pyenv.sh
wait:
  cloud_init: 5m
  required: true
```

The pieces, and where the "how" lives:

| Block | What it does | Where the "how" lives |
|---|---|---|
| Five `mounts:` | Puts your data, models, code, notebooks, and a sandbox into the container under `/mnt/...` | [Mounting Host Directories](../howto/mount-host-dirs.md) |
| `cloud-init-include:` | Installs base packages before anything else | [Cloud-Init Bootstrapping](../howto/cloud-init-bootstrapping.md) |
| `recipes:` | Installs `mise` and `pyenv` for the `ml` user | [Provisioning with Recipes](../howto/provision-with-recipes.md) |
| `sudo: true` | Gives the `ml` user passwordless sudo | [Manifest Reference](../reference/manifest.md#security-posture) |
| `inject_ssh_keys: true` | Copies your host's public keys into `ml`'s `authorized_keys` | [Cloud-Init Bootstrapping](../howto/cloud-init-bootstrapping.md) |

!!! warning

    `sudo: true` and `inject_ssh_keys: true` are **opt-in** security choices. `inject_ssh_keys` copies your `~/.ssh/*.pub` keys into the container, giving it passwordless access from those keys. This tutorial enables both because a workstation you shell into daily wants them — but only enable them for fleets you trust. The v2 defaults are `false`.

## 2. Create the host directories

Each mount source must exist before lxm plans the container. Create all five:

```bash
mkdir -p /tmp/ml/data /tmp/ml/models /tmp/ml/code /tmp/ml/notebooks /tmp/ml/sandbox
```

These are the stand-ins for your real data tree; mount real paths the same way.

## 3. Preview the plan

```text
$ lxm plan ml-workstation.yaml
Plan: 1 to create, 0 to update, 0 to recreate, 0 to delete, 0 noop across 1 manifest(s)
$ echo $?
0
```

One container to create. As always, nothing has changed yet.

## 4. Apply — create, bootstrap, provision

`apply` creates the container from `ubuntu-24.04`, waits for cloud-init (which installs the base packages), then runs the two recipes as `root`. The recipes install the tooling **for the `ml` user** via `su`, so the workstation is ready the moment it comes up:

```text
$ lxm apply ml-workstation.yaml
Applied 1 step(s) across 1 container(s)
$ echo $?
0
```

This step is slower than the first tutorial's — cloud-init plus two tool installers. That is expected.

## 5. Verify the recipe hashes

`lxm status` shows both recipes ran and their idempotency hashes:

```text
$ lxm status ml-workstation
Container:     ml-workstation
Status:        Running (Code 103)
Architecture:  x86_64
IP Address:    10.171.13.107
Managed:       true
Groups:        ml

Recipe Hash Trail:
  - recipes_install_pyenv_sh: d54093500153df44a7e42db59a49c5ea3533cb3ad27152a8a3e4cbc0ae9f40ee
  - recipes_install_mise_sh: 07b47651d95b0e313bfd16b8bbc6d9d971fac26290dc526e1be4c8e5862d6cd4
```

## 6. Verify the tools

Run a small script inside the container to confirm the toolchain the recipes installed for the `ml` user:

```bash
mkdir -p scripts
cat > scripts/verify-tools.sh <<'EOF'
#!/bin/bash
su - ml -c 'bash -l -c "export PATH=\$HOME/.local/bin:\$HOME/.pyenv/bin:\$PATH; echo mise: \$(mise --version); echo pyenv: \$(pyenv --version)"'
EOF
```

```text
$ lxm script ml-workstation scripts/verify-tools.sh
mise: 2026.8.3 linux-x64 (2026-08-07)
pyenv: pyenv 2.8.3
$ echo $?
0
```

`mise` and `pyenv` are installed for `ml` and ready to use. Because the recipes appended the PATH lines to `ml`'s `.bashrc`, they are also present in fresh login shells — `lxm shell ml-workstation --run-as ml` gives you a ready workstation.

## 7. Verify the mounts

Every declared mount should be a live LXD disk device with `shift: true` (so host UIDs are idmapped into the container):

```text
$ lxc config show ml-workstation
...
  mount0:
    path: /mnt/data
    shift: "true"
    source: /tmp/ml/data
    type: disk
  mount1:
    path: /mnt/models
    shift: "true"
    source: /tmp/ml/models
    type: disk
  mount2:
    path: /mnt/code
    shift: "true"
    source: /tmp/ml/code
    type: disk
  mount3:
    path: /mnt/notebooks
    shift: "true"
    source: /tmp/ml/notebooks
    type: disk
  mount4:
    path: /mnt/workspace
    shift: "true"
    source: /tmp/ml/sandbox
    type: disk
```

Your five host directories are inside the container. Write to them from either side and both see the same files.

## What you built

* A workstation container with five host mounts under `/mnt/...`.
* A cloud-init bootstrap that installs base packages.
* `mise` and `pyenv` provisioned for the `ml` user by idempotent recipes.
* Deliberate `sudo` and `inject_ssh_keys` choices, made knowingly.

This is the same shape as the repo's own `glm.yaml`/`omp.yaml` fleet — multi-mount, tool-heavy, and driven entirely by two manifest files plus a few scripts.

## Next steps

* [Disposable Test Agents](disposable-agents.md) — run a fleet of these, then tear it down.
* [Provisioning with Recipes](../howto/provision-with-recipes.md) — recipe metadata files, `env`, retries, and snapshot control.
* [Cloud-Init Bootstrapping](../howto/cloud-init-bootstrapping.md) — what the base-package fragment does under the hood.
