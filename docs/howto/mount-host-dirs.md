# Mounting Host Directories

This guide shows you how to share host directories with containers using the four mount authoring styles, and what lxm validates before it lets a mount through.

## Prerequisites

* lxm installed ([Installation](../getting-started/installation.md))
* A working LXD host
* [Authoring Manifests](author-manifests.md) and the [Manifest Reference](../reference/manifest.md) mounts section

## 1. Choose an authoring style

All four styles are equivalent and normalize to the same object form at load time. Pick whichever reads best.

**Style 1 — string shorthand** (`host:container[:ro|:rw|:recursive]`):

```yaml
mounts:
  - "/tmp/projects:/var/www/html:rw"
  - "/tmp/config:/etc/app:ro"
```

**Style 2 — map form** (`container: host`):

```yaml
mounts:
  /var/www/html: /tmp/projects
```

A map value may also be an object (with `source` and `path`); an explicit `path` inside the object overrides the map key. Complete example: [`mounts-map.yaml`](../examples/mounts-map.yaml).

**Style 3 — object form:**

```yaml
mounts:
  - source: /tmp/projects
    path: /var/www/html
    readonly: true
    recursive: true
```

**Style 4 — mixed list:**

```yaml
mounts:
  - "/tmp/projects:/var/www/html"
  - source: /tmp/readonly
    path: /var/readonly
    readonly: true
```

A complete example using styles 1, 3, and 4 is [`mounts-demo.yaml`](../examples/mounts-demo.yaml):

```yaml
schema: lxm/config/v2
name: mounts-demo
status: present
image: ubuntu:24.04
user: dev
mounts:
  - "/tmp/data:/var/data:rw"
  - source: /tmp/readonly
    path: /var/readonly
    readonly: true
  - source: /tmp/recursive
    path: /var/recursive
    recursive: true
  - "/tmp/mixed:/var/mixed"
```

## 2. Understand how a mount becomes an LXD device

When lxm plans a container, each mount becomes an LXD `disk` device. You can see exactly what would be created with `plan --format json`:

```text
$ lxm plan docs/examples/mounts-demo.yaml --format json
...
    "instances_post": {
      ...
      "devices": {
        "mount0": { "path": "/var/data", "shift": "true", "source": "/tmp/data", "type": "disk" },
        "mount1": { "path": "/var/readonly", "readonly": "true", "shift": "true", "source": "/tmp/readonly", "type": "disk" },
        "mount2": { "path": "/var/recursive", "recursive": "true", "shift": "true", "source": "/tmp/recursive", "type": "disk" },
        "mount3": { "path": "/var/mixed", "shift": "true", "source": "/tmp/mixed", "type": "disk" }
      }
    }
```

`readonly`, `recursive`, and `shift` from the manifest map straight onto the device. This is the file that `apply` sends to LXD, so what you plan is what you get.

!!! note

    lxm creates every host mount with LXD's `shift: true` device option, so host UIDs are idmapped into the container and container root does not see host file ownership directly. If you need to disable idmapping for a mount, set the device option after apply (for example `lxc config device set <container> <device> shift false`).

## 3. Add the mount to a container manifest

Add a `mounts` list to your manifest. Host paths must be **absolute**; `~` and `{{ .Vars.* }}` templates are expanded during authoring:

```yaml
schema: lxm/config/v2
include:
  - _base.yaml
name: web-01
status: present
image: ubuntu:22.04
groups: [web]
mounts:
  - source: /home/me/projects
    path: /workspace
```

## 4. Validate and apply

The mount source must exist on the host at `plan`/`apply` time, or lxm refuses with `exit 3`:

```text
$ lxm plan config/dev.yaml
Error: config validation "config/dev.yaml": mount 0: source path "/tmp/opencode/mntdemo/nonexistent" does not exist on host: stat /tmp/opencode/mntdemo/nonexistent: no such file or directory
$ echo $?
3
```

Create the directory (or fix the path), then plan and apply:

```text
$ lxm plan config/dev.yaml
Plan: 1 to create, 0 to update, 0 to recreate, 0 to delete, 0 noop across 1 manifest(s)
$ lxm apply config/dev.yaml
Applied 1 step(s) across 1 container(s)
```

Confirm the device landed on the container:

```text
$ lxc config show mount-demo
...
  mount0:
    path: /workspace
    source: /tmp/opencode/mntdemo/projects
    type: disk
```

## When things go wrong

| Error | Cause | Fix |
|---|---|---|
| `mount N: source and path are required` | A mount object missing `source` or `path` | Fill in both fields. |
| `invalid container destination path "/proc"` | A mount destination in `/`, `/proc`, `/sys`, or `/dev` | Use a different container path. |
| `mount N: source path "..." does not exist on host` | Host directory missing or typo'd | Create the directory or fix the path. |
| `mount N: source path "..." must be absolute` | Relative host path in resolved form | Use an absolute path or a `~`/`{{ .Vars }}` template. |
| `duplicate mount path "/x"` | Two mounts with the same container path after merge | Deduplicate in the base or the leaf. |
| `cannot unmarshal !!map into []config.Mount` | Should not happen on current builds; the map form is supported | Upgrade the binary. |

!!! warning

    Mounting a sensitive host directory (e.g. `/etc`, `/proc`, `/sys`, `/dev`, or `~/.ssh`) gives the container's root access to that data. Only mount directories you intend the container to modify.

## Next steps

* [Provisioning with Recipes](provision-with-recipes.md) — run setup scripts inside the mounted container.
* [Configuring Networking](configure-networking.md) — give the container a static IP alongside the mounts.
* [Manifest Reference](../reference/manifest.md#mounts) — the full mounts field reference.
