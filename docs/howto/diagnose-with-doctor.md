# Diagnosing with doctor

This guide shows you how to use `lxm doctor` to check that your environment can run lxm, and to spot manifests that are not yet on the v2 schema.

## Prerequisites

* lxm installed ([Installation](../getting-started/installation.md))

## 1. Run doctor against your LXD host

`lxm doctor` runs a set of read-only environment checks. From a directory with no YAML files (so nothing gets scanned), with LXD reachable:

```text
$ lxm doctor
Running lxm doctor diagnostic checks...
[OK] LXD socket reachable
[OK] lxd group membership
[OK] Kernel idmapped mounts support
$ echo $?
0
```

* **LXD socket reachable** — lxm can talk to the LXD daemon.
* **lxd group membership** — your user is in the `lxd` group (or is root), so the daemon accepts your requests.
* **Kernel idmapped mounts support** — `/proc/self/uid_map` exists, which idmapped host mounts need.

Every line reading `[OK]` and an `exit 0` means lxm is ready to use. Warnings do **not** change the exit code.

## 2. Check the manifest side

Point doctor at a fleet directory to check that every manifest is on the v2 schema. It scans `*.yaml`/`*.yml` in the directory (not subdirectories), skips files that are not lxm manifests, and warns about legacy v1 configs:

```text
$ lxm doctor --skip-remote docs/examples/
Running lxm doctor diagnostic checks...
[SKIP] Remote LXD socket check skipped
[OK] lxd group membership
[OK] Kernel idmapped mounts support
[OK] All discovered configs migrated to lxm/config/v2
```

Use `--skip-remote` when you only want the local checks (for example, on a CI runner with no LXD socket). It replaces the socket check with `[SKIP] Remote LXD socket check skipped`.

## 3. Read the warnings

The warning shapes you will see, and what they actually mean:

* **`Un-migrated config (missing schema: lxm/config/v2): <file>`** — a manifest with no `schema:` field (v1 or legacy). It loads on the v1-compat surface but skips v2 validation. Fix with `lxm compile` (see [Migrating from lxm v1](migrate-v1.md)).
* **`Config <file> fails to load: ...`** — a manifest that declares `schema: lxm/config/v2` but does not load cleanly. The message carries the real load error.

What doctor deliberately does **not** warn about:

* **`base: true` files** (e.g. `_base.yaml`) — they declare `schema: lxm/config/v2` and are intentionally not standalone-loadable (no `name`), so they are not "un-migrated".
* **Non-lxm YAML** (e.g. `mkdocs.yml`, `.goreleaser.yml`, `Taskfile.yml`) — files without a schema, `name`, `image`, or `base` are not lxm manifests and are skipped entirely.

## When things go wrong

| Output | Meaning | Fix |
|---|---|---|
| `Error: Failed to connect to LXD: ...` (exit 4) | LXD daemon not running or socket missing | Start LXD (`snap start lxd`), or set `LXD_SOCKET` if your socket is non-standard. |
| `[WARN] lxd group membership` | Your user is not in the `lxd` group | `sudo usermod -aG lxd "$USER"`, then log out/in. |
| `[WARN] Kernel idmapped mounts support` | `/proc/self/uid_map` absent | Kernel does not support idmapped mounts; older kernels (pre-5.12) are affected. |
| `Config ... fails to load: resolved schema validation failed` | A v2 manifest does not validate | Read the load error and fix the manifest (missing field, invalid value). |

## Next steps

* [Migrating from lxm v1](migrate-v1.md) — fix the real "un-migrated" warnings.
* [Installation](../getting-started/installation.md) — the environment prerequisites doctor verifies.
* [CLI Reference](../reference/cli.md#lxm-doctor) — the doctor command reference.
