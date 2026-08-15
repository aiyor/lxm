# Results & Exit Codes

Every `lxm` command returns a categorized numeric exit code, and every command can emit a structured JSON envelope on stdout with `--format json`. Together they give scripts and agents a deterministic, parseable interface.

This page is the single user-facing source of truth for both. The machine contract lives in `SPEC_RESULT.md`; the JSON transcripts below are verbatim output from the shipped binary.

## The `lxm/result/v1` envelope

Pass `--format json` to any command (except the interactive `shell` and `ssh` carve-outs) and lxm writes one JSON document to **stdout**. Human-readable logging stays on **stderr**, so the two never mix.

```json
{
  "schema": "lxm/result/v1",
  "command": "init",
  "ok": true,
  "target": ".",
  "plan": {
    "summary": {}
  },
  "results": [],
  "warnings": [],
  "errors": [],
  "exit_code": 0
}
```

### Top-level fields

| Field | Type | Meaning |
|---|---|---|
| `schema` | string | Always `"lxm/result/v1"`. |
| `command` | string | The executed subcommand (`plan`, `apply`, `list`, …). |
| `ok` | bool | `true` if and only if `exit_code` is `0`. |
| `target` | string | The first positional argument passed to the command, when any. |
| `plan` | object | `summary` (per-action counts) and `steps` for `plan`/`apply`; otherwise an empty `summary`. |
| `results` | array | Per-container operation results (`container`, `action`, `changed`, `ok`, `duration_ms`). |
| `warnings` | array | Warning message strings emitted during execution. |
| `errors` | array | Error objects (`code`, `container`, `message`, `retryable`) when `exit_code != 0`. |
| `exit_code` | int | The categorized process exit code (0–7). |

### Structural invariants

1. **Schema version.** Every envelope begins with `"schema": "lxm/result/v1"`.
2. **Boolean invariant.** `ok` is `true` *if and only if* `exit_code` is `0`.
3. **No null fields.** Slices and maps are always initialized (`[]` and `{}`), never `null` — so `jq '.errors'` always returns an array, never a null.
4. **Stream separation.** Structured results on stdout; human logging on stderr.

## Exit code catalog

| Exit | Error code | Meaning | Typical trigger |
|:---:|---|---|---|
| **0** | `""` | Success. | Command completed with no errors. |
| **1** | `INTERNAL_ERROR` | Unhandled panic or internal failure. | Unexpected runtime failure, interrupt/cancellation. |
| **2** | `USAGE_ERROR` | Invalid arguments, flag syntax, or interactive carve-out. | `--prune` on a single file; `--format json` on `shell`/`ssh`. |
| **3** | `CONFIG_ERROR` | YAML/schema validation failure or unbound variable. | Unknown key, bad mount path, unbound `{{ .Env.X }}`. |
| **4** | `LXD_ERROR` | LXD daemon/API error, socket error, or ETag conflict. | Connection refused, ETag `412` drift. |
| **5** | `TARGET_NOT_FOUND` | Container, snapshot, or target set not found. | Selector matches zero containers; missing file. |
| **6** | `EXEC_FAILED` | Recipe or script execution failed. | A recipe script exits non-zero. |
| **7** | `WAIT_TIMEOUT` | Readiness wait timed out. | Cloud-init/network wait deadline exceeded. |

### Exit code → error code mapping

Each envelope error object's `code` maps 1-to-1 to its exit class. Scripts should branch on the **numeric exit code** (it is what the shell sees) and use `code` for machine-readable attribution.

| Exit | `code` in envelope |
|:---:|---|
| 0 | `""` |
| 1 | `INTERNAL_ERROR` |
| 2 | `USAGE_ERROR` |
| 3 | `CONFIG_ERROR` |
| 4 | `LXD_ERROR` |
| 5 | `TARGET_NOT_FOUND` |
| 6 | `EXEC_FAILED` |
| 7 | `WAIT_TIMEOUT` |

Two error details worth knowing:

* `retryable: true` appears on ETag (drift) errors — re-computing the plan and re-applying will resolve them.
* `CONFIG_WARN_EMPTY_RECIPE` is emitted as a **warning** (not an error) by `lxm compile` when it prunes a recipe group with empty/comment-only scripts.

## Verified envelopes

### Success (`lxm init --format json`)

```json
{
  "schema": "lxm/result/v1",
  "command": "init",
  "ok": true,
  "target": ".",
  "plan": {
    "summary": {}
  },
  "results": [],
  "warnings": [],
  "errors": [],
  "exit_code": 0
}
```

### Error — `TARGET_NOT_FOUND` (`lxm list --format json --name zzz-nomatch`)

```json
{
  "schema": "lxm/result/v1",
  "command": "list",
  "ok": false,
  "target": "",
  "plan": {
    "summary": {}
  },
  "results": [],
  "warnings": [],
  "errors": [
    {
      "code": "TARGET_NOT_FOUND",
      "message": "no containers found matching filter criteria",
      "retryable": false
    }
  ],
  "exit_code": 5
}
```

### Error — `USAGE_ERROR` carve-out (`lxm shell --format json nomatch`)

The interactive commands reject `--format json` with exit code 2, but they still emit the envelope:

```json
{
  "schema": "lxm/result/v1",
  "command": "shell",
  "ok": false,
  "target": "nomatch",
  "plan": {
    "summary": {}
  },
  "results": [],
  "warnings": [],
  "errors": [
    {
      "code": "USAGE_ERROR",
      "message": "interactive command shell rejects --format json",
      "retryable": false
    }
  ],
  "exit_code": 2
}
```

### Error — `CONFIG_ERROR` (`lxm plan --format json` on a manifest whose mount source does not exist)

```json
{
  "schema": "lxm/result/v1",
  "command": "plan",
  "ok": false,
  "target": "docs/examples/dev-station.yaml",
  "plan": {
    "summary": {}
  },
  "results": [],
  "warnings": [],
  "errors": [
    {
      "code": "CONFIG_ERROR",
      "message": "config validation \"docs/examples/dev-station.yaml\": mount 0: source path \"/tmp/projects\" does not exist on host: stat /tmp/projects: no such file or directory",
      "retryable": false
    }
  ],
  "exit_code": 3
}
```

## Driving automation from the envelope

Because the envelope always has the same shape, `jq` recipes are stable across commands:

**Check success and read the exit code:**

```bash
lxm plan config/ --format json | jq -r '.exit_code'
```

**Branch on the plan summary:**

```bash
lxm apply config/ --format json > result.json
jq -e '.plan.summary.create == 0' result.json > /dev/null && echo "nothing to create"
```

**Print the first error message:**

```bash
lxm plan config/ --format json | jq -r '.errors[0].message'
```

**Check for retryable (ETag drift) errors:**

```bash
lxm apply config/ --format json | jq -e '.errors[] | select(.retryable == true)' > /dev/null \
  && echo "drift detected — re-plan and re-apply" || echo "clean"
```

**Fail the pipeline unless the exit code is zero:**

```bash
exit_code=$(lxm plan config/ --format json | jq -r '.exit_code')
[ "$exit_code" -eq 0 ]
```

## Network steps & results

Fleets that use [`vswitches:` / `network_policy:`](manifest.md#virtual-switches-vswitches) produce
an additional, additive surface in the envelope:

* **`plan.network_steps`** — the reconciliation steps for LXD network ACLs and vswitches, executed
  *before* instance steps (`create_acl`/`update_acl` before `create_vswitch`/`update_vswitch`, so
  a network can always reference its ACL). Each step carries `kind`, `name`, and the payload.
* **`network_results`** — the per-step execution outcome from `apply`, alongside `results`:
  `{name, kind, changed, ok, duration_ms}`. A network-step failure is therefore machine-
  distinguishable from an instance-step failure, and aborts the apply **before any instance step
  runs** (networks are prerequisites).
* A failed network step also appears in `errors` with the failing object's name in the `name`
  field (instance errors use `container`).

```bash
# Show the network steps a fleet needs:
lxm plan config/ --format json | jq '.plan.network_steps[] | [.kind, .name]'

# Verify every network step applied cleanly:
lxm apply config/ --format json | jq -e '.network_results[] | select(.ok != true)' > /dev/null \
  && echo "network failure" || echo "networks clean"
```

## Related

* [CLI Reference](cli.md) — per-command exit codes and flags.
* [Automating in CI](../howto/automate-ci.md) — end-to-end pipeline patterns.
* [Troubleshooting](../troubleshooting.md) — exit-code → cause → fix matrix.
