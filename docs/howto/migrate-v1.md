# Migrating from lxm v1

This guide shows you how to upgrade legacy `lxm/config/v1` manifests to the strict `lxm/config/v2` schema with `lxm compile`.

## Prerequisites

* lxm installed ([Installation](../getting-started/installation.md))
* A fleet with legacy (no `schema:`) manifests

## 1. Know whether you need to migrate

lxm treats any manifest **without** a `schema:` field as `lxm/config/v1`. It still loads, but:

* it prints a stderr notice on every load, and
* it skips the strict schema validation that v2 enforces (unknown keys, mount security rules, `run_as` requirements).

`lxm doctor` flags un-migrated configs as a warning:

```text
$ lxm doctor --skip-remote .
Running lxm doctor diagnostic checks...
[SKIP] Remote LXD socket check skipped
[OK] lxd group membership
[OK] Kernel idmapped mounts support
Warning: Un-migrated config (missing schema: lxm/config/v2): legacy.yaml
```

And `plan` prints the notice when it loads one:

```text
$ lxm plan legacy.yaml
notice: legacy.yaml declares no schema (lxm/config/v1 compat mode) — run lxm compile to migrate to lxm/config/v2
Plan: 1 to create, 0 to update, 0 to recreate, 0 to delete, 0 noop across 1 manifest(s)
```

## 2. Compile (non-destructive by default)

`lxm compile` migrates every manifest it finds and writes the results to `.lxm/compiled/`, leaving your source untouched:

```text
$ lxm compile legacy.yaml
Successfully compiled 1 manifest(s):
  - .lxm/compiled/legacy.yaml
```

The compiled file has `schema: lxm/config/v2` and normalized fields:

```yaml
schema: lxm/config/v2
name: legacy-box
image: ubuntu:24.04
user: dev
status: present
mounts:
  - /tmp/data:/var/data
wait:
  required: false
sudo: true
inject_ssh_keys: true
```

A scalar `wait: false` is canonicalized to `wait: {required: false}`.

## 3. Verify the compiled output

Confirm the migrated file loads and plans before you adopt it:

```text
$ lxm plan .lxm/compiled/legacy.yaml
Plan: 1 to create, 0 to update, 0 to recreate, 0 to delete, 0 noop across 1 manifest(s)
```

## 4. Apply the migration in place

Once you are happy with the compiled output, rewrite your sources with `--in-place` (atomic temp-file + rename):

```text
$ lxm compile legacy.yaml --in-place
Successfully compiled 1 manifest(s):
  - legacy.yaml
```

After this, `legacy.yaml` is a v2 manifest and the doctor warning is gone.

## 5. Understand what compile changes and warns about

* **Semantic default flips.** v2 makes `sudo` and `inject_ssh_keys` **opt-in** (`false` by default). A v1 manifest that relied on the old implicit behavior gets a `CONFIG_WARN_DEFAULT_FLIP` warning telling you exactly which field to add. The tool does **not** add them for you — it cannot infer intent:

```text
Warning: CONFIG_WARN_DEFAULT_FLIP: v2 default for sudo is false (opt-in); set 'sudo: true' to preserve legacy passwordless sudo
Warning: CONFIG_WARN_DEFAULT_FLIP: v2 default for inject_ssh_keys is false (opt-in); set 'inject_ssh_keys: true' to enable auto host-key injection
```

* **Empty recipe groups are pruned.** A recipe group with empty/comment-only `scripts` is dropped with `CONFIG_WARN_EMPTY_RECIPE`.
* **Unknown top-level keys become warnings** so you can fix them before the strict v2 parse rejects them.
* **Conflicts become errors.** `cloud-init` + `cloud-init-file` both set → `exit 3`. A mount destination of `/`, `/proc`, `/sys`, or `/dev` → `exit 3`.

## When things go wrong

| Error | Cause | Fix |
|---|---|---|
| `Warning: Un-migrated config (missing schema: lxm/config/v2)` | A manifest still lacks `schema:` | Run `lxm compile` on it (this is how `doctor` surfaces them). |
| `CONFIG_ERROR: unknown or unsupported schema version "lxm/config/v3"` | A future/unrecognized schema value | Use `lxm/config/v2`; a future v3 ships its own migration. |
| `CONFIG_ERROR_CONFLICT: both cloud-init and cloud-init-file` | Both fields set in a v1 file | Keep one; compile refuses to guess. |
| `CONFIG_ERROR_SECURITY: mount destination "/proc" is restricted` | A mount path in `/`, `/proc`, `/sys`, `/dev` | Change the destination before compiling. |

## Next steps

* [Authoring Manifests](author-manifests.md) — structure your migrated fleet.
* [Diagnosing with doctor](diagnose-with-doctor.md) — re-check after migration.
* [Manifest Reference](../reference/manifest.md) — the v2 fields you are migrating to.
