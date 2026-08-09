# Troubleshooting

This page is the map from "lxm did something I did not expect" to "here is what happened and what to do". Start with the exit code — every command prints one — then find your failure signature below. Every entry here was reproduced against the shipped binary while writing this page; transcripts are verbatim.

The exit-code catalog itself lives in [Results & Exit Codes](reference/results-and-exit-codes.md). This page is the operational counterpart: cause, diagnosis, fix.

## Start with doctor

When in doubt, run `lxm doctor`. It checks the environment lxm depends on — the LXD socket, your group membership, and kernel support for idmapped mounts — and warns about manifests that are not yet on the v2 schema:

```text
$ lxm doctor
Running lxm doctor diagnostic checks...
[OK] LXD socket reachable
[OK] lxd group membership
[OK] Kernel idmapped mounts support
$ echo $?
0
```

```text
$ lxm doctor --skip-remote docs/examples/
Running lxm doctor diagnostic checks...
[SKIP] Remote LXD socket check skipped
[OK] lxd group membership
[OK] Kernel idmapped mounts support
[OK] All discovered configs migrated to lxm/config/v2
```

Warnings do not change the exit code. Reading the lines:

| Line | Meaning | If it fails |
|---|---|---|
| `[OK] LXD socket reachable` | lxm can talk to the LXD daemon | LXD is not running or `LXD_SOCKET` points at the wrong socket (see [LXD unreachable](#lxd-unreachable)) |
| `[OK] lxd group membership` | your user can talk to the daemon | add yourself to the `lxd` group (`sudo usermod -aG lxd "$USER"`) and log out/in |
| `[OK] Kernel idmapped mounts support` | host mounts can use idmapping | kernel too old (pre-5.12) for idmapped mounts; older kernels are affected |
| `[OK] All discovered configs migrated to lxm/config/v2` | every manifest in the target directory declares `schema: lxm/config/v2` | see [Un-migrated manifests](#un-migrated-manifests) below |

The full interpretation is in [Diagnosing with doctor](howto/diagnose-with-doctor.md).

## Exit-code → cause → fix matrix

Every `lxm` command exits with a categorized code from 0–7. The table maps each code to the causes reproduced while writing this guide; the signatures below give the transcripts and the step-by-step fix.

| Exit | Meaning | Reproduced causes | Fix |
|:---:|---|---|---|
| **0** | Success | Command completed with no errors. | — |
| **1** | Internal error | Not user-triggerable in normal operation; an interrupted wait can surface as "wait policy cancelled by user interrupt". | Re-run; if it persists, report it with the output. |
| **2** | Usage error | `--prune` on a single file; `--format json` on `shell`/`ssh`; an invalid `--format` value. | Read the error message; it names the flag. See [Prune rejected](#prune-rejected-on-a-single-file) and [Interactive commands reject the JSON format](#interactive-commands-reject-the-json-format). |
| **3** | Config error | Unknown manifest key; unbound `{{ .Env.X }}` template variable; invalid value (bad mount path, bad IP). | Fix the manifest. `lxm compile` reports the exact file and field. See [Unbound template variable](#unbound-template-variable). |
| **4** | LXD error | LXD socket unreachable; image alias not resolvable; the container was modified outside lxm (drift). | Depends on the message — see [LXD unreachable](#lxd-unreachable), [Image not found](#image-alias-not-found), [Drift](#drift-container-modified-outside-lxm). |
| **5** | Target not found | Selector matches no containers; target file does not exist. | For selectors: nothing to do — treat as "no-op" in pipelines. For files: check the path. See [Selector matches nothing](#selector-matches-nothing). |
| **6** | Execution failed | Recipe script exits non-zero; `run`/`script` command fails; `ssh`/`shell` cannot reach or authenticate to the container. | Check the script's output, or the ssh errors in [Host key problems](#host-key-problems). |
| **7** | Wait timeout | Cloud-init or network readiness did not complete within the configured wait. | See [Wait timeout](#wait-timeout). |

## Common failure signatures

### Drift: container modified outside lxm

**Symptoms.** `apply` fails with a message about the configuration having changed since the operation began:

```text
$ lxm apply dup/
Error: ETag does not match: 97c66a57b26926ecdf0bbc74f97170f96b782e6e6ba4570ff8ac52d808011c70 vs 11e05802de2a480b425c25c2d2d4136a75f765427be5641bc9c953995b46b8d5. The configuration has been modified since this change began. Please retrieve the updated configuration before proceeding.
$ echo $?
4
```

**Cause.** Something else changed the container **while lxm was working** — another `lxc`/`lxm` process or a script wrote to it between the moment lxm computed its plan and the moment it wrote its own change. lxm protects the apply with an optimistic check: if the live container no longer matches what the plan was computed against, the change is refused instead of overwriting the concurrent modification.

**Fix.** Re-run `plan`, review the (new) plan, and `apply` again — the fresh plan is computed against current state, so the conflict resolves itself. This error is classified as retryable, and the JSON output names the container and the flag so a pipeline can act on it:

```json
{
  "code": "LXD_ERROR",
  "container": "ug5-etag3",
  "retryable": true
}
```

[Automating in CI](howto/automate-ci.md#3-read-the-summary-and-errors-with-jq) shows the pipeline pattern: detect the retryable error, re-plan, re-apply.

**Prevention.** If it keeps happening, something on your host is mutating containers behind lxm's back — find it. The error is a safety feature, not a bug: it exists so that two writers cannot silently clobber each other.

### Prune rejected on a single file

**Symptoms.** `--prune` on a manifest file, not a directory:

```text
$ lxm plan config/dev.yaml --prune
Error: --prune is only allowed on directory targets
$ echo $?
2
```

**Cause.** Orphans are defined relative to a *directory* of manifests — there is no meaningful "missing from this file" scope. lxm refuses the combination rather than guess.

**Fix.** Point `--prune` at the fleet directory. See [Pruning Orphans](howto/prune-orphans.md) for scope rules and the `plan --prune` preview step.

### Prune proposes more than you expected

**Symptoms.** `plan --prune` lists containers you did not intend to delete.

**Cause.** Prune scope is **target directory ∩ selectors** — *every* lxm-managed container in scope that no manifest declares is a candidate, including containers belonging to other fleets sharing the same LXD host.

**Fix.** This is a preview — nothing was deleted. Bound the scope with `--group`/`--name` and re-preview until the list matches your intent, then `apply --prune` with the same selector. The warning in [Pruning Orphans](howto/prune-orphans.md) explains the exact rule.

### Interactive commands reject the JSON format

**Symptoms.** `shell` or `ssh` with `--format json`:

```text
$ lxm shell dev-station --format json
Error: interactive command shell rejects --format json
$ echo $?
2
```

**Cause.** There is no meaningful machine-readable result for an interactive session. The rejection is by design so scripts cannot hang on a TTY.

**Fix.** Do not pass `--format` to `shell`/`ssh`. Use `script`/`run` for non-interactive work, or `--dry-run` on `ssh` to print the exact command it would run.

### Unbound template variable

**Symptoms.** A manifest uses `{{ .Env.X }}` and the variable is not set:

```text
$ lxm plan unbound-env.yaml
Error: loading manifest "unbound-env.yaml": template expansion in "unbound-env.yaml": unbound environment variable .Env.LXM_UG5_NOPE
$ echo $?
3
```

**Cause.** Manifest templating is **fail-closed**: referencing an environment variable that is not set is a hard error, so a typo in the variable name or a missing export cannot silently produce a wrong container definition.

**Fix.** Set the variable (`LXM_UG5_NOPE=... lxm plan unbound-env.yaml`), fix the name, or remove the template. Templates are documented in [Environment Variables](reference/environment-variables.md#compile-time-variables-manifest-templating).

### LXD unreachable

**Symptoms.** Every LXD-touching command fails with a connect error:

```text
$ LXD_SOCKET=/tmp/does-not-exist.sock lxm list
Error: Failed to connect to LXD: Get "http://unix.socket/1.0": dial unix /tmp/does-not-exist.sock: connect: no such file or directory
$ echo $?
4
```

**Cause.** The LXD daemon is not running, or `LXD_SOCKET` points at a socket that is not there. lxm uses the default local socket unless `LXD_SOCKET` overrides it.

**Fix.** Start LXD (`snap start lxd` on snap installs) and confirm the socket path. Unset a stale `LXD_SOCKET` if your shell exported one. `lxm doctor` (without `--skip-remote`) reports the same failure with a clearer intent.

### Image alias not found

**Symptoms.** `apply` fails at create time:

```text
$ lxm apply image-missing.yaml
Error: Image not provided for instance creation
$ echo $?
4
```

**Cause.** lxm passes the manifest `image` to the LXD daemon as a **local alias or fingerprint**. A fresh LXD host has no images; an alias you have not copied yet does not resolve.

**Fix.** Copy the image to the local daemon once, using the exact alias your manifest references:

```bash
lxc image copy ubuntu:22.04 local: --alias ubuntu:22.04
```

The [Quick Start](getting-started/quickstart.md) covers this one-time step; check your local aliases with `lxc image list` if you are unsure what name to use.

### Recipe or script fails

**Symptoms.** `apply` with a recipe, or `run`/`script`, fails with:

```text
$ lxm apply recipe-fail.yaml
Error: recipe script "fail.sh" failed with exit code 1 after 1 attempt(s)
$ echo $?
6
```

**Cause.** The script ran inside the container and exited non-zero. Recipes run via `/bin/bash -l -c`; any failing command in the script fails the step.

**Fix.** Look at the script's own output — lxm reports the exit code and attempt count. Run the script interactively (`lxm shell <name>` then execute it) to see stderr. The container is left running and the pre-recipe snapshot is in place, so you can also roll back and fix forward — see [Snapshots & Rollback](howto/snapshots-and-rollback.md).

### Wait timeout

**Symptoms.** `apply` fails after the configured wait deadline:

```text
$ lxm apply wait-timeout.yaml
Error: cloud-init wait timed out after 3s on "ug5-wait"
$ echo $?
7
```

**Cause.** The container did not reach readiness inside `wait.cloud_init` (or `wait.network`). On a first apply this usually means the image is slow to boot and run cloud-init — not that anything is broken.

**Fix.** Check the container actually works (`lxm shell <name>`, `lxc list`) and then raise the timeout in the manifest — see [Best Practices: Tune wait policies](best-practices.md#tune-wait-policies-for-slow-images). If the container is genuinely stuck, `lxc exec <name> -- cloud-init status` shows where cloud-init stopped. `wait.required: false` downgrades the timeout to a warning while you tune the value; `LXM_WAIT_REQUIRED=true` re-arms fail-closed behavior for a single CI run.

### Selector matches nothing

**Symptoms.** A filter (`--group`, `--name`) or target matches zero containers:

```text
$ lxm list --name zzz-nomatch
Error: no containers found matching filter criteria
$ echo $?
5
```

**Cause.** The selector is a legitimate no-op, not a failure — an empty fleet, a group that no longer exists, or a name pattern that matches nothing.

**Fix.** For pipelines, treat exit `5` as "nothing to do" (the standard no-op signal — see [Automating in CI](howto/automate-ci.md#2-branch-on-exit-codes)). Interactively, widen the selector or check `lxm list` for the actual names.

### Host key problems

**Symptoms.** `lxm ssh` fails with one of two messages:

```text
$ lxm ssh ug5-ssh 'hostname'
Error: host key registration failed for "ug5-ssh" (10.171.13.114): ssh-keyscan on ug5-ssh (10.171.13.114) failed: : exit status 1
$ echo $?
6
```

```text
Error: ssh session failed: exit status 255
```

**Cause.** `lxm ssh` verifies the container's host key before connecting (strict checking is on by default). The first message means no SSH server was reachable to register a key (no sshd inside the container, or it is not running yet). The second — ssh's `Host key verification failed` — means the key in `known_hosts` no longer matches the container: the container was recreated outside lxm, or its SSH host keys were regenerated inside it.

**Fix.**

* **No sshd:** the container needs an SSH server (or use `lxm shell`, which needs none — see [Interactive Shell & SSH](howto/interact-shell-ssh.md)).
* **Stale key:** when lxm itself recreates a container it purges the old key as part of the reconcile; if the key changed some other way, remove the stale entry and reconnect:

```bash
ssh-keygen -R <container-name> -f ~/.config/lxm/known_hosts
lxm ssh <container-name>
```

The known-hosts file lives at `~/.config/lxm/known_hosts` (override with `LXM_KNOWN_HOSTS_FILE`, e.g. per CI job).

!!! warning

    Do not reach for `--insecure` or `-o StrictHostKeyChecking=no` to silence these errors — they disable host-key verification entirely. Use them only on containers you fully control; the flag prints a loud warning precisely because it removes the protection.

### Un-migrated manifests

**Symptoms.** `lxm doctor` warns that a manifest is not on the v2 schema:

```text
Warning: Un-migrated config (missing schema: lxm/config/v2): /tmp/ug5-verify/v1dir/legacy.yaml
```

**Cause.** The file loads on the v1-compat surface but skips v2 validation — it predates the `schema: lxm/config/v2` field, or was written by an older tool.

**Fix.** Migrate it with `lxm compile` (add `--in-place` to rewrite the source files):

```bash
lxm compile config/ --in-place
```

See [Migrating from lxm v1](howto/migrate-v1.md) for what the migration changes and what to review after.

## Next steps

* [Best Practices](best-practices.md) — the operating habits that prevent most of these failures.
* [Results & Exit Codes](reference/results-and-exit-codes.md) — the exact machine contract for the codes above.
* [Diagnosing with doctor](howto/diagnose-with-doctor.md) — the doctor output in full.
