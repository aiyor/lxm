# lxm — Declarative LXD Fleet Manager

`lxm` is a command-line tool for **declarative, reproducible management of LXD container fleets**. You describe the containers you want in YAML manifests — their image, users, mounts, networks, and provisioning scripts — and `lxm` reconciles your live LXD host to match.

Every mutation is **previewed first** as a pure, deterministic diff (`lxm plan`), then applied only when you say so (`lxm apply`). Reconcile as often as you like — `apply` only performs the steps your fleet actually needs to match what you declared.

## Why use lxm?

* **Plan-first, never blind.** Every change — create, update, rebuild, delete, start, stop — is a previewable, machine-readable diff before it touches LXD.
* **Idempotent by design.** Reconcile as often as you like; `apply` only changes what drifted.
* **A whole fleet from one directory.** One base manifest + one small manifest per container. Teams share and version them like code.
* **Provisioning built in.** Recipes (shell scripts) run after the container boots, guarded by wait-for-cloud-init, snapshotted before they run, and safe to roll back.
* **Safe by default.** Snapshot-before-mutation, host-key-verified SSH, opt-in sudo and key injection, and scoped orphan pruning.
* **Automation-ready.** Every command emits structured JSON and categorized exit codes, and honors `LXM_*` environment variables.

## Where do you want to start?

=== "I want a container running now"

    Start here if you have LXD installed and want to see lxm work end-to-end in a few minutes.

    [**Get Started →**](getting-started/quickstart.md)

    Or read the [Installation](getting-started/installation.md) guide first.

=== "I want to understand how it works"

    Read a plain-language explanation of what declarative reconciliation means for your fleet, before you write anything.

    [**Read the Concepts →**](getting-started/concepts.md)

=== "I'm automating lxm in CI or scripts"

    Jump straight to the automation surface: structured JSON output, exit codes, and environment variables.

    [**Automating in CI →**](howto/automate-ci.md)
    [**Results & Exit Codes →**](reference/results-and-exit-codes.md)

=== "I want to build something real"

    Follow a complete, worked tutorial that takes a real use case from nothing to a running, configured fleet.

    [**Browse the Tutorials →**](tutorials/index.md)

## Guide structure

The guide is organized by how you work:

| Section | For | Start with |
|---|---|---|
| [**Getting Started**](getting-started/index.md) | Learning, step by step | [Quick Start](getting-started/quickstart.md) |
| [**Tutorials**](tutorials/index.md) | Building real fleets end-to-end | [Your First Dev Container](tutorials/first-dev-container.md) |
| [**How-To Guides**](howto/index.md) | Accomplishing a specific task | [Authoring Manifests](howto/author-manifests.md) |
| [**Reference**](reference/index.md) | Looking up exact facts | [CLI Reference](reference/cli.md) |
| [**Best Practices**](best-practices.md) | Doing things well | [Best Practices](best-practices.md) |
| [**Troubleshooting**](troubleshooting.md) | Fixing what's broken | [Troubleshooting](troubleshooting.md) |

!!! note

    These docs are for **users** of lxm. For the internal architecture, schema mechanics, and the developer contract, see the technical documents in the repository: `ARCHITECTURE.md`, `SPEC_CLI.md`, `SPEC_MANIFEST.md`, and `SPEC_RESULT.md`.
