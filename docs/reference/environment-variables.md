# Environment Variables

This page documents the environment variables lxm reads, when they bind, and how they combine with flags and manifest values. The variables split into two groups by when they take effect:

* **Compile-time** — read while a manifest is compiled or a template is expanded.
* **Runtime** — read at command execution time, below flags.

The precedence rule that matters: **flag > environment > manifest**. Where a variable mirrors a flag or a manifest field, the flag wins, then the environment variable, then the manifest value.

## Runtime variables

These are read when a command actually runs.

### `LXM_WAIT_REQUIRED`

Overrides the `wait.required` readiness gate for `lxm apply` at execution time. The value `true` or `1` makes readiness mandatory (fail-closed); anything else makes it a soft warning.

Precedence for the final decision:

1. `--wait` flag — always forces `required: true`.
2. `LXM_WAIT_REQUIRED` environment variable.
3. `wait.required` from the manifest (default `true`).

```bash
# Force readiness waits on even if the manifest says required: false
LXM_WAIT_REQUIRED=true lxm apply config/
```

### `LXM_KNOWN_HOSTS_FILE`

Overrides where `lxm ssh` stores and checks container host keys. The default is `~/.config/lxm/known_hosts` (with a sibling `.lock` file for advisory locking).

```bash
LXM_KNOWN_HOSTS_FILE=/var/lib/ci/known_hosts lxm ssh dev-station
```

Useful in CI and shared runners where each job should keep an isolated known-hosts file.

### `LXD_SOCKET`

The LXD client's Unix socket path. lxm uses it to talk to the LXD daemon instead of the default local socket. This is the same variable the upstream LXD client honors.

```bash
LXD_SOCKET=/var/snap/lxd/common/lxd/unix.socket lxm list
```

## Compile-time variables (manifest templating)

Manifest fields can reference environment variables with the anchored `{{ .Env.NAME }}` template. The value is read from the process environment **while the manifest is compiled** (that is, when `plan`/`apply`/`compile` loads the manifest). An unbound variable is a hard error: exit code `3` (`CONFIG_ERROR`).

```yaml
schema: lxm/config/v2
name: dev-station
image: '{{ .Env.LXM_TEST_IMAGE }}'
```

```bash
LXM_TEST_IMAGE=ubuntu:24.04 lxm apply config/dev.yaml
```

Related file-local and identity templates are documented in the [Manifest Reference](manifest.md#variables-templates):

| Template | Source |
|---|---|
| `{{ .Env.NAME }}` | Host environment at compile time (unbound → `exit 3`) |
| `{{ .Vars.KEY }}` | The manifest's own `vars:` map |
| `{{ .Name }}` | The container name |
| `{{ .Group }}` | The container's first group |

Escape a literal `{{` or `}}` as `\{{` / `\}}`.

## Inside the container: `LXM_USER`

When lxm creates the container user, it also writes `/etc/profile.d/lxm-env.sh` exporting `LXM_USER`. Recipes and interactive sessions inside the container can use it to target the primary user without hard-coding the name:

```bash
echo "provisioning for $LXM_USER"
```

This is a *container-side* variable produced by lxm's cloud-init, not a host-side override — do not set it on your shell to change behavior.

## What is not implemented

The design documentation (design D10) catalogs a broader runtime set such as `LXM_DRY_RUN`, `LXM_FORCE`, `LXM_GROUP`, `LXM_JOBS`, and `LXM_RUN_AS`. Those are **not yet wired into the shipped binary**. Flags remain the way to set them; this page lists only variables the binary actually reads today.

## Related

* [CLI Reference](cli.md) — global flags that the variables mirror.
* [Manifest Reference](manifest.md) — `wait`, `vars`, and template fields.
* [Automating in CI](../howto/automate-ci.md) — headless invocation patterns.
