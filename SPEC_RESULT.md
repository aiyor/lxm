# lxm/result/v1 Machine Interface Specification

## 1. Overview & Stability Contract

`lxm` provides a machine-readable JSON result envelope format (`lxm/result/v1`) emitted on `stdout` when `--format json` is passed to any command (excluding interactive TTY sessions like `lxm shell` and `lxm ssh`).

### Structural Invariants
1. **Schema Version**: Every envelope includes `"schema": "lxm/result/v1"`.
2. **Boolean Invariant**: `ok` is `true` *if and only if* the command exit code is `0`.
3. **No Null Fields**: Slices and maps are always initialized (`[]` and `{}`), never `null`.
4. **Stderr Diagnostics**: Human-readable logging (`slog`) remains on `stderr`; structured command results reside on `stdout`.

---

## 2. JSON Envelope Structure

```json
{
  "schema": "lxm/result/v1",
  "command": "apply",
  "ok": true,
  "target": "config/dev.yaml",
  "plan": {
    "summary": {
      "create": 1,
      "update": 0,
      "recreate": 0,
      "delete": 0,
      "start": 0,
      "stop": 0,
      "noop": 0
    },
    "steps": [ ... ]
  },
  "results": [],
  "warnings": [],
  "errors": [],
  "exit_code": 0
}
```

### Top-Level Fields
* `schema` (`string`): The schema identifier string (`"lxm/result/v1"`).
* `command` (`string`): Name of the executed subcommand (e.g. `"plan"`, `"apply"`, `"list"`).
* `ok` (`bool`): `true` if `exit_code == 0`, `false` otherwise.
* `target` (`string`): Target argument passed to the command (if applicable).
* `plan` (`object`): Plan summary map and step list (populated for `plan` and `apply`).
* `results` (`array`): List of individual container operation result items (`ResultItem`).
* `network_results` (`array`, additive): List of network-step result items (`NetworkResult`) for
  `vswitches:`/`network_policy:` fleets.
* `warnings` (`array`): List of warning message strings emitted during execution.
* `errors` (`array`): List of error objects (`ErrorInfo`) when `exit_code != 0`.
* `exit_code` (`int`): Categorized numeric process exit code (0–7).

### Network-Scoped Additive Fields

Fleets that use `vswitches:` / `network_policy:` extend the envelope without changing existing
consumers:

* **`plan.network_steps`** (`array` of `NetworkStep`): reconciliation steps for LXD network ACLs
  and vswitches, executed **before** instance steps — ACL steps (`create_acl`/`update_acl`) before
  vswitch steps (`create_vswitch`/`update_vswitch`), satisfying the C8 dependency (a network may
  reference only existing ACLs). Each step carries `kind`, `name`, `diff`, `changed`, and the
  request payload (`acl_post`/`acl_put`/`network_post`/`network_put`).
* **`network_results`** (`array` of `NetworkResult`): per-step outcomes from `apply` —
  `{name, kind, changed, ok, duration_ms, error?}` — so a phase-1 (network) failure is
  machine-distinguishable from a phase-3 (instance) failure.
* **`errors[]` for network steps** carry the failing object's name in the `name` field (instance
  errors use `container`).
* **Phase-abort**: a network-step LXD error aborts the apply before any instance step runs;
  `network_results` records the failing step and its `PROVIDER_ERROR` (exit 4) surfaces in the top-level
  `errors` array.

Exit-code mapping is unchanged and shared with the instance plane: manifest/policy/union/IPAM/NIC
violations ⇒ `3` (`CONFIG_ERROR`); LXD API/extension errors (including `create_acl`/
`create_vswitch`) ⇒ `4` (`PROVIDER_ERROR`).

---

## 3. Exit Code to Error Code Mapping Catalog

Every envelope error object specifies a string `code` that maps 1-to-1 to its corresponding CLI exit code class (`ExitCodeToErrorCode` in `internal/output/envelope.go`):

| Exit Code | String Error Code | Description | Example Trigger |
| :---: | :--- | :--- | :--- |
| **0** | `""` | Success / OK. | Command completed successfully. |
| **1** | `INTERNAL_ERROR` | Unhandled panic, runtime error, or internal crash. | Unexpected runtime failure or context error. |
| **2** | `USAGE_ERROR` | Flag parsing error, missing arguments, or TTY carve-out. | Passing `--format json` to `lxm shell`. |
| **3** | `CONFIG_ERROR` | Schema validation failure, YAML error, unbound variable. | Unbound variable `{{ .Env.MISSING }}`. |
| **4** | `PROVIDER_ERROR` | LXD daemon API error, socket error, ETag conflict. | Daemon connection refused or ETag 412 drift. |
| **5** | `TARGET_NOT_FOUND` | Container name, snapshot, or target set not found. | Selector matching 0 containers or missing target file. |
| **6** | `EXEC_FAILED` | Recipe execution error, non-zero script exit code. | Recipe bash script returning non-zero exit status. |
| **7** | `WAIT_TIMEOUT` | Cloud-init or network readiness timeout exceeded. | Cloud-init wait deadline exceeded (10m). |

### Error Sub-Codes & Additional Info
* `CONFIG_WARN_EMPTY_RECIPE`: Emitted as a warning during `lxm compile` when empty/scriptless recipe groups are pruned.
* `retryable` (`bool`): Set to `true` on ETag mismatch errors (`412 Precondition Failed`) to indicate that re-computing the plan and re-applying will resolve the drift.

---

## 4. Example Result Envelopes

### 4.1 Success Envelope (`lxm apply --format json`)
```json
{
  "schema": "lxm/result/v1",
  "command": "apply",
  "ok": true,
  "target": "config/dev.yaml",
  "plan": {
    "summary": {
      "create": 1,
      "update": 0,
      "recreate": 0,
      "delete": 0,
      "start": 0,
      "stop": 0,
      "noop": 0
    }
  },
  "results": [
    {
      "container": "dev-station",
      "action": "create",
      "changed": true,
      "ok": true,
      "duration_ms": 1240
    }
  ],
  "warnings": [],
  "errors": [],
  "exit_code": 0
}
```

### 4.2 Error Envelope (Usage Error)
```json
{
  "schema": "lxm/result/v1",
  "command": "plan",
  "ok": false,
  "target": "",
  "plan": {
    "summary": {}
  },
  "results": [],
  "warnings": [],
  "errors": [
    {
      "code": "USAGE_ERROR",
      "message": "accepts 1 arg(s), received 0",
      "retryable": false
    }
  ],
  "exit_code": 2
}
```
