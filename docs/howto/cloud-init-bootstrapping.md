# Cloud-Init Bootstrapping

This guide shows you how to bootstrap a container with cloud-init — installing base packages and writing files before your recipes run.

## Prerequisites

* lxm installed ([Installation](../getting-started/installation.md))
* A working LXD host
* [Authoring Manifests](author-manifests.md) and the [Manifest Reference](../reference/manifest.md#cloud-init) cloud-init section

## 1. How lxm composes cloud-init

lxm builds the container's `user.user-data` from three sources, in this order:

1. **`cloud-init-include`** — one or more cloud-config fragment files to merge.
2. **`cloud-init`** (inline string) or **`cloud-init-file`** (a path) — never both.
3. **lxm's automatic user config** — the manifest `user`, its sudo group, SSH keys, and the `LXM_USER` env file.

The result is passed to LXD as `user.user-data` on create.

## 2. Split your cloud-config into fragments

A fleet usually shares a base package set and a separate per-machine fragment. The repository's own fleet keeps these as plain `#cloud-config` files:

```yaml
# base-ubuntu.yaml — shared base packages
#cloud-config
package_update: true
packages:
  - curl
  - wget
  - git
  - unzip
  - build-essential
```

```yaml
# tools.yaml — writes a provisioning script, runs it
#cloud-config
write_files:
  - path: /root/install-tools.sh
    permissions: "0755"
    content: |
      #!/bin/bash
      set -ex
      . /etc/profile.d/lxm-env.sh
      apt-get update && apt-get install -y google-cloud-cli
runcmd:
  - /root/install-tools.sh
```

The complete example in this guide's repository uses [`cloud-init/base-cloud-config.yaml`](../examples/cloud-init/base-cloud-config.yaml):

```yaml
#cloud-config
package_update: true
packages:
  - curl
  - git
  - unzip
users:
  - default
```

## 3. Reference the fragments from a manifest

Use `cloud-init-include` to pull fragments in, and an inline `cloud-init` block for anything machine-local:

```yaml
# cloud-init-demo.yaml
schema: lxm/config/v2
name: cloud-init-demo
status: present
image: ubuntu:24.04
user: dev
groups: [demo]
cloud-init-include:
  - cloud-init/base-cloud-config.yaml
cloud-init: |
  packages:
    - ripgrep
    - jq
```

Include paths are relative to the manifest's directory. `cloud-init` and `cloud-init-file` are mutually exclusive — setting both fails compilation with `exit 3`.

## 4. Preview the composed cloud-init

`plan --format json` shows the exact `user.user-data` lxm will send to LXD — a merge of the fragment, the inline block, and the automatic user config:

```text
$ lxm plan docs/examples/cloud-init-demo.yaml --format json
...
    "instances_post": {
      "config": {
        "user.lxm.managed": "true",
        "user.lxm.user": "dev",
        "user.user-data": "#cloud-config\npackage_update: true\npackages:\n    - curl\n    - git\n    - unzip\n    - ripgrep\n    - jq\nusers:\n    - default\n    - groups: sudo\n      name: dev\n      shell: /bin/bash\nwrite_files:\n    - content: |\n        export LXM_USER=dev\n      path: /etc/profile.d/lxm-env.sh\n      permissions: \"0644\"\n"
      }
    }
```

Notice what lxm injected automatically: the `dev` user in the `sudo` group with `/bin/bash`, and `/etc/profile.d/lxm-env.sh` exporting `LXM_USER=dev`. Your recipes can rely on `$LXM_USER` inside the container.

The fragments merge key-wise; lists (like `packages`) concatenate — the base `curl/git/unzip` and the inline `ripgrep/jq` appear together.

## 5. Control SSH key injection

By default lxm does **not** inject your host keys. Opt in per manifest:

```yaml
schema: lxm/config/v2
name: web-01
status: present
image: ubuntu:22.04
user: dev
inject_ssh_keys: true
```

or pin explicit keys instead:

```yaml
ssh_keys:
  - "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAEXAMPLE... user@host"
```

!!! warning

    `inject_ssh_keys: true` copies your `~/.ssh/*.pub` keys into the container's `authorized_keys`, giving the container passwordless access from those keys. Enable it only for fleets you trust.

## 6. Gate recipes on cloud-init readiness

Cloud-init runs before your recipes for a reason: recipes assume a bootstrapped system. lxm waits for cloud-init to finish before running recipes, controlled by the manifest `wait` block:

```yaml
wait:
  cloud_init: 10m     # default
  network: 60s        # default
  required: true      # default: fail-closed
```

With `required: true` (the default), a timeout is a hard failure (`exit 7`) and recipes are skipped. See [Provisioning with Recipes](provision-with-recipes.md) for the recipe side.

## When things go wrong

| Error | Cause | Fix |
|---|---|---|
| `reading cloud-init-include "..." : ... no such file` | Fragment path does not resolve | Fix the path; it resolves relative to the manifest. |
| `CONFIG_ERROR_CONFLICT: both cloud-init and cloud-init-file` | Both inline and file cloud-init set | Use one or the other. |
| Container boots without your packages | Fragment/inline merge not as expected | Preview `user.user-data` with `plan --format json` (step 4). |
| `network-config` has no effect | The container was created by an older binary that did not emit `user.network-config` | Re-apply the manifest so `user.network-config` is sent on the config update, or set it manually with `lxc config set <name> user.network-config '...'`. |
| `wait` timeout (exit 7) | Cloud-init did not finish in `cloud_init` | Increase `wait.cloud_init` or fix cloud-init errors inside the container. |

## Next steps

* [Provisioning with Recipes](provision-with-recipes.md) — run scripts after cloud-init completes.
* [Configuring Networking](configure-networking.md) — declare NICs separately.
* [Manifest Reference](../reference/manifest.md#cloud-init) — the cloud-init fields reference.
