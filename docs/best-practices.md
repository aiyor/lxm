# Best Practices

This page collects the operating habits that keep a fleet reproducible, safe to change, and cheap to maintain: versioning your fleet like code, structuring manifests so defaults stay in one place, keeping provisioning scripts re-runnable, and knowing when the safety rails are meant to be lifted.

The rules here are not lxm features — they are ways to use the features you have already seen in the guides. Each section links the guide that shows the mechanics.

## Keep your fleet in git

Your manifests and their scripts are the source of truth for your machines. Treat them like source code: commit them, review changes, and only apply what has been reviewed.

A practical workflow:

1. **Keep everything in the repo.** The manifests, the recipe scripts, and the cloud-init files all live together, so a checkout contains a complete description of the fleet. lxm resolves script and include paths relative to the manifest, which makes a repo layout the natural one.
2. **Validate before you merge.** `lxm compile` validates every manifest in the directory against the schema and fails the whole run if any file has a schema error or a migration conflict. It also tells you when a manifest relies on a v2 default you probably did not intend:

```text
$ lxm compile no-flips.yaml
Warning: CONFIG_WARN_DEFAULT_FLIP: v2 default for sudo is false (opt-in); set 'sudo: true' to preserve legacy passwordless sudo
Warning: CONFIG_WARN_DEFAULT_FLIP: v2 default for inject_ssh_keys is false (opt-in); set 'inject_ssh_keys: true' to enable auto host-key injection
Successfully compiled 1 manifest(s):
  - .lxm/compiled/no-flips.yaml
```

The compile gate in [Automating in CI](howto/automate-ci.md) runs this in your pipeline, so a broken manifest stops a merge instead of a deployment. Two things the gate does and one it does not: it **expands** `{{ .Env.* }}`, `{{ .Vars.* }}`, and `{{ .Name }}` templates and fails the run if a referenced environment variable is unset (exit 3, same as `plan`/`apply` — see [Troubleshooting](troubleshooting.md#unbound-template-variable)); it does **not** resolve `include:` chains, which are still resolved when `plan`/`apply` load the manifests. Set the environment variables your templates reference before running the gate, and see [Environment Variables](reference/environment-variables.md) for the template rules.
3. **Plan in review, apply in CI.** The diff between "what the fleet is" and "what the manifests declare" is exactly the plan. Reviewing the plan output for a pull request is the cheapest review your containers will ever get: it costs nothing and shows precisely what would change.
4. **Roll back manifests, not containers.** A bad change is reverted in git and re-applied. Because `apply` only closes the difference between manifests and live state, the second run repairs the drift without you scripting the undo.

## Structure a fleet with a base manifest

A fleet of several containers should have one small file per container and one shared base. The base holds the defaults that every container inherits; each container file declares only what differs.

* `_base.yaml` with `base: true` — shared user, wait timeouts, and opt-in security settings. A base file must not set `name` or `image`, which are per-container facts.
* One file per container in `config/`, each pulling in the base with `include:`.

The [Authoring Manifests](howto/author-manifests.md) guide works through this pattern. Two rules from it matter most when you grow a fleet:

* **Scalar fields: an explicit value wins.** An explicitly set field in the container file overrides the base — even an explicit empty value. If a container is not inheriting what you expect, look for an explicit override.
* **Lists concatenate unless you say otherwise.** Inherited `mounts`, `networks`, and `recipes` merge base-first, then leaf. Use `remove:` to drop a specific inherited entry and `replace:` to discard an inherited list entirely. A `remove` that matches nothing is a compile error — a deliberate signal that the fleet no longer contains what you thought it did.

When a container stops matching its manifest (a mount no longer exists on the host, a network address moved), fix the manifest — that is the file your teammates will read next year.

## Write recipes that survive re-runs

lxm runs a recipe when its script content changes, and skips it when the stored content hash matches. That hash contract is what makes `apply` safe to run on a schedule, but it only protects you if your scripts are written to tolerate re-runs:

* **Guard one-shot steps.** "Add this line to `.bashrc` if absent", "install this package only if not installed", "create this user if missing" — idempotent forms fail cleanly when run a second time.
* **Prefer declarative installers.** A script that runs `apt-get install` is mostly idempotent on its own; one that appends to config files needs the guard. Where your tooling offers one (mise, pyenv, uv), use the installer's own "already present" behavior instead of writing your own.
* **Let the hash work for you.** A script change re-runs that recipe on the next `apply` — that is the mechanism, not a bug. When you want the new version of a script to take effect, commit the change and `apply`; the container converges.
* **`--force` is the deliberate escape hatch.** It re-runs every recipe regardless of stored hashes, and lxm says so:

```text
$ lxm apply --force bp-recipe.yaml
time=2026-08-09T13:52:40.213+10:00 level=INFO msg="Force mode enabled. Will re-run recipes even if hashes match."
Applied 1 step(s) across 1 container(s)
```

Use it when you know the script is safe to re-run, or when the container state is already broken and you want the script to repair it.

!!! warning

    `--force` re-executes every recipe even when its stored hash matches. Provisioning scripts are not always safe to run twice — installers, user creation, and config writes can fail or duplicate state on re-run. Prefer changing the script (which re-runs just that recipe) over forcing everything.

**Every recipe run is snapshot-protected.** Before the first recipe of an apply runs, lxm snapshots the container, and the hash trail shows you what is stored:

```text
$ lxm status ug5-bp
Container:     ug5-bp
Status:        Running (Code 103)
Architecture:  x86_64
IP Address:    10.171.13.208
Managed:       true
Groups:        ug5

Recipe Hash Trail:
  - hello_sh: 7fa88e111c39acc718c42743e60475b76e2498d9722859a4049a84e57c853a8b

$ lxm snapshot list ug5-bp
SNAPSHOT                                  STATEFUL  CREATED
user.lxm.snap.ug5-bp-1786247559939068917  false     2026-08-09T03:52:39Z
```

A failed provisioning run is therefore one `rollback` away — see [Snapshots & Rollback](howto/snapshots-and-rollback.md) for the undo flow.

## Set a snapshot retention and GC cadence

The automatic pre-recipe snapshots accumulate on every recipe change. They are cheap and they are your rollback safety net, but an unbounded pile costs disk and makes the meaningful rollback points hard to find. Give them a lifetime:

```text
$ lxm snapshot gc ug5-bp --older-than 7d --dry-run
time=2026-08-09T13:52:40.165+10:00 level=INFO msg="Dry-run mode enabled. No changes will be made."
[DRY-RUN] Would prune 0 snapshot(s) matching prefix "user.lxm.snap."
```

A workable cadence for a dev fleet:

* **Daily recipe churn:** `lxm snapshot gc --older-than 7d` weekly, so a week of pre-recipe snapshots survives each container.
* **Weekly recipe churn:** `--older-than 30d` monthly.
* **Always before a big change:** take a manual, named snapshot (`lxm snapshot create <name> before-upgrade`) in addition to the automatic ones — a manual snapshot has a name you will recognize, and survives even if you later drop recipe snapshots from the GC prefix.

`gc` without a container argument scans every instance on the host, so a scheduled job can prune the whole fleet in one command. Preview with `--dry-run` until the counts look right, then drop the flag.

!!! warning

    `snapshot gc` deletes snapshots permanently. The automatic recipe snapshots are your primary rollback point; pruning them removes that safety net for older containers. Preview with `--dry-run` first.

## Tune wait policies for slow images

`apply` waits for the container to be ready before it runs recipes — cloud-init first, then the network. The defaults are generous for most images (a plan on a manifest without a `wait:` block shows them):

```text
$ lxm plan no-wait.yaml --format json | jq '.plan.steps[0].wait_policy'
{
  "CloudInit": "10m",
  "Network": "60s",
  "Poll": "5s",
  "Required": true,
  "Presence": {}
}
```

When you know an image is slow — a large base image, heavy `package_upgrade` in cloud-init, or a slow network on the host — raise the timeout in the **base manifest** so every container inherits it:

```yaml
# _base.yaml
schema: lxm/config/v2
base: true
wait:
  cloud_init: 20m
  network: 120s
```

The `required` field is the fail-closed switch: with `required: true` (the default) a timed-out wait fails the apply with `exit 7`; with `required: false` the same timeout is a warning and provisioning continues. For an interactive first bring-up, a soft wait plus a manual check is often nicer; for CI, keep it required and let `LXM_WAIT_REQUIRED=true` force fail-closed readiness for a single run without editing manifests (see [Environment Variables](reference/environment-variables.md)).

A wait timeout is a signal, not just a failure: the container is taking longer than your policy expects. [Troubleshooting](troubleshooting.md#wait-timeout) walks through what to check when it fires.

## Security defaults: start strict, opt in deliberately

The v2 schema ships fail-safe defaults, and lxm tells you when a manifest silently relies on them (the compile warnings above):

* **`sudo` is opt-in.** A container gets no passwordless sudo unless the manifest says `sudo: true`.
* **`inject_ssh_keys` is opt-in.** lxm does not push your SSH keys into containers unless the manifest says so.
* **Host keys are verified on every `ssh`.** `lxm ssh` registers the container's host key on first use and connects with strict verification. If a container is recreated, lxm forgets the stale key as part of the reconcile; a key that changes without a reconcile surfaces as a verification failure rather than being silently accepted. Do not disable this with `--insecure` or `-o StrictHostKeyChecking=no` on containers you do not fully control — the flag exists for lab machines, not production fleets.
* **Deletes are scoped.** `--prune` only touches containers lxm itself marked as managed, inside the directory you targeted and the selectors you passed — but inside that scope it deletes everything unmanaged-by-manifest, including containers that belong to other fleets sharing the host. Preview with `plan --prune` and bound with `--group`/`--name` (see [Pruning Orphans](howto/prune-orphans.md)).
* **Recreate destroys snapshots.** When a manifest change requires rebuilding a container, lxm refuses to proceed if snapshots exist. Accepting the rebuild with `--force` destroys them — take a manual snapshot first if you might need to roll back.

The general rule: **every permission is a manifest line.** If a capability is important enough to grant, it is important enough to be in git, reviewed, and reproducible — that is what "declarative" buys you.

## Next steps

* [Troubleshooting](troubleshooting.md) — exit-code matrix and failure signatures.
* [Authoring Manifests](howto/author-manifests.md) — the base-manifest mechanics in full.
* [Provisioning with Recipes](howto/provision-with-recipes.md) — recipe semantics and the `--force` gate.
* [Pruning Orphans](howto/prune-orphans.md) — the exact scope rules that keep `--prune` safe.
