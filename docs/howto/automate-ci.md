# Automating in CI

This guide shows you how to drive lxm from CI/CD pipelines and scripts: machine-readable JSON, exit-code branching, dry-run previews, and the environment overrides that make headless runs deterministic.

## Prerequisites

* lxm installed in the pipeline ([Installation](../getting-started/installation.md))
* A fleet in the repository ([Authoring Manifests](author-manifests.md))
* [Results & Exit Codes](../reference/results-and-exit-codes.md) — the envelope and catalog

## 1. Use the JSON envelope

Every non-interactive command emits one `lxm/result/v1` JSON document on stdout with `--format json`. Logging stays on stderr, so stdout is always clean parseable JSON:

```text
$ lxm plan docs/examples/absent-demo.yaml --format json
{
  "schema": "lxm/result/v1",
  "command": "plan",
  "ok": true,
  "target": "docs/examples/absent-demo.yaml",
  "plan": {
    "summary": {
      "create": 0,
      "delete": 0,
      "noop": 1,
      "recreate": 0,
      "start": 0,
      "stop": 0,
      "update": 0
    },
    "steps": [
      {
        "container": "absent-demo",
        "action": "noop",
        "changed": false,
        "wait_policy": {
          "CloudInit": "10m",
          "Network": "60s",
          "Poll": "5s",
          "Required": true,
          "Presence": {}
        },
        "config_base_dir": "/mnt/devel/tools/lxm/docs/examples"
      }
    ]
  },
  "results": [],
  "warnings": [],
  "errors": [],
  "exit_code": 0
}
```

The `ok` field is `true` if and only if `exit_code` is `0`, and arrays/maps are always `[]`/`{}` — never `null`. See the [envelope reference](../reference/results-and-exit-codes.md) for the full field list.

## 2. Branch on exit codes

The categorized exit codes (0–7) are the shell contract:

```bash
lxm plan config/ --format json > plan.json
case $? in
  0)   echo "plan computed" ;;
  3)   echo "config error"; exit 1 ;;
  5)   echo "nothing to reconcile"; exit 0 ;;
  *)   echo "lxm failed"; exit 1 ;;
esac
```

Branching on `5` is the standard "no-op" signal for pipelines: an empty selector match means there is nothing to do, not a failure.

## 3. Read the summary and errors with jq

Because the envelope shape never changes, `jq` recipes are stable across commands:

```bash
# gate on "nothing to create"
jq -e '.plan.summary.create == 0' plan.json > /dev/null || echo "containers will be created"

# print the first error message
jq -r '.errors[0].message' plan.json

# detect retryable (ETag drift) errors
jq -e '.errors[] | select(.retryable == true)' plan.json > /dev/null \
  && echo "drift — re-plan and re-apply" || echo "clean"
```

## 4. Preview before you mutate

Pipelines should `plan` first and `apply` only when the plan is acceptable:

```bash
lxm plan config/ --format json > plan.json
[ "$(jq -r '.exit_code' plan.json)" = 0 ] || exit 1

# fail the pipeline if any destructive action is planned
jq -e '.plan.summary.delete == 0 and .plan.summary.recreate == 0' plan.json > /dev/null \
  || { echo "destructive changes planned; aborting"; exit 1; }

lxm apply config/
```

`apply` also honors the global `--dry-run` flag, which computes the plan and prints the summary without mutating anything — useful as a pipeline smoke test:

```bash
lxm apply config/ --dry-run
```

## 5. Make runs deterministic

* **Scoped selectors.** Target exactly what the job should reconcile: `lxm apply config/ -g web`. A group that no longer exists yields exit `5` — fail the job rather than silently touching the whole fleet.
* **Readiness env override.** `LXM_WAIT_REQUIRED` overrides the manifest's `wait.required` for a single run, so a pipeline can force fail-closed readiness without editing manifests:

```bash
LXM_WAIT_REQUIRED=true lxm apply config/
```

* **Isolated known-hosts file.** CI runners should keep `lxm ssh`'s host keys isolated with `LXM_KNOWN_HOSTS_FILE`, so jobs never race on the default file:

```bash
export LXM_KNOWN_HOSTS_FILE="$RUNNER_TEMP/known_hosts"
```

See [Environment Variables](../reference/environment-variables.md) for the full list of what the binary actually reads.

## 6. A complete GitHub Actions job

```yaml
name: reconcile-fleet
on:
  schedule:
    - cron: "0 2 * * *"
  workflow_dispatch:

jobs:
  reconcile:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v4

      - name: Install lxm
        run: |
          curl -sLO "https://github.com/aiyor/lxm/releases/download/v0.1.0/lxm_0.1.0_linux_amd64.tar.gz"
          tar -xzf lxm_0.1.0_linux_amd64.tar.gz
          sudo install -m 0755 lxm /usr/local/bin/

      - name: Validate manifests
        run: lxm compile config/

      - name: Plan (fail on destructive)
        run: |
          lxm plan config/ --format json > plan.json
          jq -e '.exit_code == 0' plan.json
          jq -e '.plan.summary.delete == 0 and .plan.summary.recreate == 0' plan.json

      - name: Apply
        run: |
          export LXM_WAIT_REQUIRED=true
          lxm apply config/
```

Adjust the release version to the one you install.

## When things go wrong

| Error | Cause | Fix |
|---|---|---|
| `invalid format "yaml", expected text|json` | Wrong `--format` value | Use `text` or `json`. |
| `interactive command ssh rejects --format json` | `--format json` on `ssh`/`shell` | Do not pass `--format` to interactive commands (exit 2 by design). |
| Exit `5` on an empty fleet | Selector matched nothing | Treat `5` as "nothing to do" or widen the selector. |
| Envelope is `null` when a command fails | An exit path did not emit JSON | Check the command wrote to stdout; usage errors from cobra may not emit an envelope. |
| `retryable: true` on an apply error | ETag drift (external change) | Re-run `plan` then `apply` in the same job. |

## Next steps

* [Results & Exit Codes](../reference/results-and-exit-codes.md) — envelope and exit-code reference.
* [Environment Variables](../reference/environment-variables.md) — compile-time vs runtime `LXM_*`.
* [Targeting with Selectors](fleet-selectors.md) — scoped fleet operations.
