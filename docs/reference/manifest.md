# Manifest Reference

This page documents every field you can use in an `lxm/config/v2` manifest, what it does, and how the value is validated. It is the user-facing counterpart to the repository's `SPEC_MANIFEST.md` contract.

Every field shown below is verified against the shipped binary. Complete, compile-valid example manifests live in `docs/examples/` (at the repository root) and are re-checked by the docs CI gate on every pull request. Field snippets in this page are fragments for illustration; the complete examples are the copy-paste-correct files.

## What a manifest is

A manifest is a YAML file that declares the desired state of one container: *"a container named `X` should exist, be built from image `Y`, have these mounts, networks, users, and recipes."* lxm reads a manifest (or a directory of them), compares it against what is actually running in LXD, and reconciles the difference.

```yaml
schema: lxm/config/v2
name: dev-station
status: present
image: ubuntu:22.04
user: dev
groups: [dev]
```

## Schema surfaces

Manifest authoring is validated against two schema surfaces:

* **Authoring surface** — what you write. Accepts shorthands, file-local `vars:`, `~` and `{{ ... }}` templates, and the inheritance directives (`include`, `remove`, `replace`).
* **Resolved surface** — the strict, closed schema lxm compiles your manifest to before applying it. It rejects all shorthands, directives, and unknown keys, and enforces the security rules (absolute mount sources, clean mount destinations).

You almost never touch the resolved surface directly; `lxm compile` produces it from your authored manifest.

---

## Identity & lifecycle

### `schema`

The manifest schema version. Must be `lxm/config/v2` for the strict schema. If omitted, the file is treated as a legacy `lxm/config/v1` manifest: it still loads, but lxm prints a notice telling you to run `lxm compile` to migrate it. See [Migrating from lxm v1](../howto/migrate-v1.md).

```yaml
schema: lxm/config/v2
```

### `name`

The container name — the unique identity lxm manages. Required in a resolved manifest (a `base` manifest must **not** have one).

```yaml
name: dev-station
```

### `status`

Whether the container should exist. `present` (default) means "ensure it exists"; `absent` means "ensure it does not exist".

```yaml
status: present        # present (default) | absent
```

* `status: present` requires an `image`.
* `status: absent` combined with an explicit `state` fails compilation.
* An `absent` manifest does not need an `image`.

Complete example: [`absent-demo.yaml`](../examples/absent-demo.yaml).

```yaml
schema: lxm/config/v2
name: absent-demo
user: dev
status: absent
groups: [demo]
```

### `state`

The desired power state of a `present` container: `running` (default) or `stopped`.

```yaml
state: running         # running (default) | stopped
```

### `image`

The base image the container is created from, as a LXD image reference: a hex fingerprint, a local alias, or a `remote:alias`. The image must already be present in your LXD host; lxm refers to it by that reference.

```yaml
image: ubuntu:22.04
```

### `user`

The primary user lxm creates in the container. Defaults to `ubuntu`. lxm injects the user via cloud-init, adds it to the `sudo` group, and writes `LXM_USER` into `/etc/profile.d/lxm-env.sh`.

```yaml
user: dev
```

### `groups`

Group tags for fleet targeting. Containers can be selected with `--group` / `--exclude-group`. See [Selector algebra](cli.md#selector-algebra).

```yaml
groups: [dev, backend]
```

---

## Inheritance

Fleets usually share defaults. lxm merges manifests through a base file and the `include` directive, using *presence-wins* rules: an explicitly set field overrides the base value (even when set to an empty/zero value), while an omitted field inherits.

### `base`

Marks a file as a base manifest (shared defaults). A base file must **not** have `name` or `image`. Files whose names start with `_` must declare `base: true` or lxm refuses to load them.

```yaml
schema: lxm/config/v2
base: true
user: ubuntu
wait:
  cloud_init: 10m
  network: 60s
```

Complete example: [`_base.yaml`](../examples/_base.yaml).

### `include`

List of manifest files to merge as the base for this manifest. Paths are relative to the including file. Inheritance is depth-first; later files override earlier ones, and the leaf file's own fields override everything.

```yaml
schema: lxm/config/v2
include:
  - _base.yaml
name: dev-station
```

### `remove`

Removes specific items from an inherited list. Matching rules: `remove.mounts` matches by normalized container path, `remove.networks` by interface name, `remove.recipes` by exact script path. A `remove` entry that matches nothing fails compilation.

```yaml
remove:
  mounts:
    - /mnt/shared
  recipes:
    - recipes/common.sh
```

### `replace`

Replaces an inherited list wholesale instead of concatenating it.

```yaml
replace:
  networks:
    - name: eth0
      ipv4: 10.0.0.50
      parent: lxdbr0
```

Complete example for `include` + `remove` + `replace` + presence-wins `wait`: [`inheritance-demo.yaml`](../examples/inheritance-demo.yaml) inherits [`_inheritance-base.yaml`](../examples/_inheritance-base.yaml).

---

## Mounts

Host directories are made available inside the container as bind mounts. All styles normalize to the same object form. The resolved form is:

```yaml
mounts:
  - source: /tmp/projects     # absolute host path (required)
    path: /var/www/html       # absolute container path (required)
    readonly: false           # optional; default false
    recursive: true           # optional; default false
```

### Authoring styles

**Style 1 — string shorthand** (`host:container[:ro|:rw|:recursive]`):

```yaml
mounts:
  - "/tmp/host-data:/var/data:rw"
  - "/tmp/host-config:/etc/app:ro"
```

**Style 2 — map form** (`container: host`):

```yaml
mounts:
  /var/log: /tmp/host-logs
```

The map form is loadable and normalizes to the object form at load time. A map value may also be an object (with `source` and `path`); an explicit `path` inside the object overrides the map key.

Complete example: [`mounts-map.yaml`](../examples/mounts-map.yaml).

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
  - "/tmp/host-data:/var/data"
  - source: /tmp/projects
    path: /var/www/html
```

Complete example: [`mounts-demo.yaml`](../examples/mounts-demo.yaml).

### Security rules

* **Absolute sources.** In the resolved schema, mount `source` must start with `/`. Tilde (`~/...`) and `{{ .Vars.* }}` templates are expanded during authoring.
* **Clean destinations.** `path` values of `/`, `/proc`, `/sys`, and `/dev` are rejected during compilation (`exit 3`).
* **Host-side existence.** At `plan`/`apply` time the source directory must exist on the host, or lxm exits `3` (`config validation ... source path ... does not exist on host`).
* **ID mapping.** lxm creates every host mount with LXD's `shift: true` device option, so host UIDs are idmapped into the container and container root does not see host file ownership directly.
* **Duplicate destinations.** Two mounts with the same container path are rejected after merge.

!!! warning

    Mounting a sensitive host directory (e.g. `/etc`, `/proc`, `/sys`, `/dev`, or `~/.ssh`) into a container you do not control gives that container's root access to the mounted data.

---

## Networks

Network interfaces are declared as a list. `name` defaults to `eth0`, `parent` to `lxdbr0`.

```yaml
networks:
  - name: eth0
    ipv4: 10.10.10.50
    parent: lxdbr0
```

Duplicate interface names are rejected after merge, and `ipv4` must parse as an IPv4 address.

---

## Recipes

Recipes are provisioning scripts that run inside the container during `apply`. The supported v2 form is a list of groups, each with an optional `run_as` user (default `root`) and a non-empty `scripts` list:

```yaml
recipes:
  - run_as: root
    scripts:
      - recipes/bootstrap.sh
  - run_as: dev
    scripts:
      - recipes/user-setup.sh
```

* **Idempotency.** After a script runs, lxm stores a SHA-256 hash of its content in the container's config (`user.lxm.recipe.*.hash`) and skips it on later applies unless the file changed or `--force` is passed.
* **Snapshots.** Before the first recipe runs, lxm takes a snapshot named `user.lxm.snap.<container>-<timestamp>` (unless the recipe metadata disables it). `lxm snapshot gc` cleans these up.
* **Metadata.** A recipe can also be a YAML file (`lxm/recipe/v1`) declaring `run_as`, `env`, `sudo`, `snapshot`, `retries`, and a `scripts` list. See the `config/recipes/` examples in the repository.
* **Shorthands.** The authoring schema also accepts a bare script-path string (`- recipes/bootstrap.sh`), a `root:` shorthand (`- root: [recipes/setup.sh]`), and legacy `scripts:`-only groups (common in v1 configs). All normalize to the object form at load time, with `run_as` defaulting to `root`; `lxm compile` emits the object form.

Complete example: [`recipes-demo.yaml`](../examples/recipes-demo.yaml).

---

## Cloud-init

lxm composes the container's `user.user-data` from three sources, in this order: `cloud-init-include` files, then either an inline `cloud-init` string or a `cloud-init-file` path (never both), then lxm's automatic user configuration.

### `cloud-init-include`

List of cloud-init fragment files (relative to the manifest) to merge in.

```yaml
cloud-init-include:
  - cloud-init/base-cloud-config.yaml
```

### `cloud-init`

An inline `#cloud-config` body as a YAML block.

```yaml
cloud-init: |
  packages:
    - ripgrep
    - jq
```

### `cloud-init-file`

A path to a `#cloud-config` file, as an alternative to the inline form.

```yaml
cloud-init-file: cloud-init/local.yaml
```

`cloud-init` and `cloud-init-file` are mutually exclusive; setting both fails compilation (`exit 3`).

### `network-config`

An inline network configuration for cloud-init.

```yaml
network-config: |
  version: 2
  ethernets:
    eth0:
      dhcp4: true
```

!!! note

    lxm passes the field through to LXD as `user.network-config` on create and on config update, where cloud-init applies it as the instance's network configuration (cloud-init network config v2, no `#cloud-config` header). The `networks:` list creates the NIC devices; `network-config` configures them inside the container.

Complete example: [`cloud-init-demo.yaml`](../examples/cloud-init-demo.yaml) includes [`cloud-init/base-cloud-config.yaml`](../examples/cloud-init/base-cloud-config.yaml).

---

## Wait & readiness

Readiness gates delay recipe execution until cloud-init finishes or the container has a network address.

```yaml
wait:
  cloud_init: 10m     # default 10m
  network: 60s        # default 60s
  poll: 5s            # default 5s
  required: true      # default true (fail-closed)
```

* `wait: true` / `wait: false` is accepted as a shorthand for `required: true` / `required: false` (legacy v1 style).
* With `required: true` (the default), a readiness timeout is a hard failure: `exit 7` and recipes are skipped.
* With `required: false`, timeouts degrade to warnings and recipes still run.
* The `--wait` flag forces `required: true`; `LXM_WAIT_REQUIRED` can override it. See [Environment Variables](environment-variables.md).

---

## Variables & templates

Manifest values can be parameterized. Templates are expanded with anchored replacement during compilation; unbound variables are a hard error (`exit 3`).

### `vars`

File-local variables, reusable across the file:

```yaml
vars:
  workspace: /tmp/projects

mounts:
  - source: "{{ .Vars.workspace }}"
    path: /workspace
```

### Environment and identity templates

| Template | Expands to |
|---|---|
| `{{ .Vars.KEY }}` | A file-local `vars:` value |
| `{{ .Env.NAME }}` | The host environment variable `NAME` at compile time (unbound → `exit 3`) |
| `{{ .Name }}` | The container name |
| `{{ .Group }}` | The container's first group |

To emit a literal `{{` or `}}`, escape it as `\{{` / `\}}`.

---

## Security posture

These fields control the container's user and SSH surface (all opt-in, all default `false`):

### `sudo`

Passwordless sudo for the manifest `user`.

```yaml
sudo: true
```

### `inject_ssh_keys`

Automatically inject your host's public keys (`~/.ssh/*.pub`) into the container user's `authorized_keys`.

```yaml
inject_ssh_keys: true
```

### `ssh_keys`

Explicit list of public keys to install, instead of discovering them from the host.

```yaml
ssh_keys:
  - "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAEXAMPLE... user@host"
```

!!! warning

    `inject_ssh_keys` and `ssh_keys` give the container passwordless access from the listed keys. Only enable them for fleets you trust. The v2 defaults for `sudo` and `inject_ssh_keys` are `false`; legacy v1 configs that relied on the old implicit behavior are flagged during `lxm compile`.

---

## Validation summary

| Rule | Consequence |
|---|---|
| Unknown top-level key in a v2 manifest | `exit 3` |
| `status: present` without `image` | `exit 3` |
| `status: absent` with `state` | `exit 3` |
| Base manifest with `name` or `image` | `exit 3` |
| `_`-prefixed file without `base: true` | `exit 3` |
| Mount source that is not absolute (resolved) | `exit 3` |
| Mount destination `/`, `/proc`, `/sys`, `/dev` | `exit 3` |
| Duplicate mount path or network name | `exit 3` |
| `remove` matching nothing | `exit 3` |
| `cloud-init` and `cloud-init-file` both set | `exit 3` |
| Unbound `{{ .Vars.* }}` / `{{ .Env.* }}` | `exit 3` |
| Circular `include` chain | `exit 3` |

## Related

* [CLI Reference](cli.md) — how manifests are targeted (`plan`, `apply`, `diff`).
* [Results & Exit Codes](results-and-exit-codes.md) — what `exit 3` looks like in JSON.
* [Authoring Manifests](../howto/author-manifests.md) — the task-focused guide for writing fleets.
