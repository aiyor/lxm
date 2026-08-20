# Concepts

This page explains the ideas lxm is built on — what "declarative" means here, why everything goes through a preview first, and why you can run lxm as often as you like.

## Declarative management

Most tools ask you to spell out the steps: *create a container, then add this mount, then set this IP*. That is the **imperative** way — you describe how to get somewhere, one command at a time.

lxm works the other way around. You describe the **end state** you want, and lxm works out the steps itself. Instead of a script of commands, you keep a small YAML **manifest**:

```yaml
schema: lxm/config/v2
include:
  - ../_base.yaml
name: dev-station
status: present
image: ubuntu:22.04
groups: [dev]
```

Read literally, this says: *"a container named `dev-station` should exist, built from `ubuntu:22.04`, in the `dev` group."* It does not say how to create it, only what the result should look like.

Manifests build on each other. A shared **base manifest** (`_base.yaml`) holds the defaults every container inherits — the user account, the wait timeouts — and each container has its own small file. Two containers that share the same base differ by only a few lines. Because everything lives in files, your fleet is versionable like source code: reviewed, diffed, and rolled back in git.

## The plan-first workflow

Between your declaration and your live LXD or Incus host sits one guardrail: every change is previewed before it is made.

* **`lxm plan`** reads your manifests, compares them against what is actually running, and prints the difference — what would be created, updated, recreated, or deleted. It changes nothing.
* **`lxm apply`** takes the steps the plan describes and performs them.

You always run `plan` first. Before anything touches your provider, you see exactly what would change and can stop if the plan does not match your intent. A `plan` that surprises you is a free opportunity to fix the manifest — no harm done.

## Why idempotency matters

lxm computes its work from the *difference* between your manifests and the live host. When the two already match, there is no difference to close and the plan is empty — applying changes nothing.

That property, **idempotency**, is what makes lxm safe to run on a schedule, in a script, or in CI:

* You never need to track "is this already applied?" — lxm knows, because it compares state rather than assuming.
* You can re-run `apply` after a failure and it finishes only the work that is still outstanding.
* A teammate can apply the same manifests to their own LXD or Incus host and get the same result — reproducible fleets, not one-off setups.

## What happens when you run lxm

In broad terms, every `plan`/`apply` pass does the same thing:

1. Read every manifest in the target directory and merge each instance's file with the shared base.
2. Fetch the current state of your LXD/Incus host.
3. Compare the two and work out the minimal set of steps — create what is missing, update what drifted, leave what already matches alone.
4. `plan` prints those steps; `apply` executes them.

That is the whole model: **declare, preview, apply.** Everything else in lxm — mounts, virtual switches, network segmentation, disks, provisioning recipes, snapshots — is a way to say more about the end state you want.

## Next steps

* Follow the [Quick Start](quickstart.md) to see the loop in action.
* Learn what you can declare in a manifest in [Authoring Manifests](../howto/author-manifests.md).
