# LXM / Incus Orchestrator — Independent Design, Implementation & Feature Assessment

**Reviewer:** OpenCode
**Date:** 2026-09-08
**Scope:** full repository at commit `585d41a` (`refactor: modernise codebase with Go 1.27 idioms…`)
**Method:** static review of all non-test Go sources (~16.2k LOC), targeted test/race runs, `go build`/`go vet`, CLI smoke tests, and adversarial probes against suspected edge cases.

---

## 1. Executive Summary

`lxm` is a genuinely well-architected declarative fleet manager for Incus and LXD. Its core
design — *compile manifests → compute a pure plan → execute through a thin provider driver* — is
sound, the provider abstraction is clean, the test suite is large and mostly fast, and the
documentation set is unusually thorough for a tool of this size.

The implementation is *not* in the "prototype" category: it has ETag/OCC discipline, phase-ordered
apply semantics, deterministic network ACL compilation with real CIDR decomposition, mTLS remote
trust, and marker-based ownership. That is a strong foundation.

However, this review found **several concrete correctness defects**, a **dead legacy package**, a
**build-toolchain inconsistency that breaks CI**, and a handful of **documented-but-unimplemented
features**. None are architectural; most are localized. The most serious are:

| # | Severity | Finding |
|---|----------|---------|
| B1 | **High** | `status: absent` on a `vswitch` crashes fleet union with `invalid CIDR address: <nil>` |
| B2 | **High** | Recipe metadata files with multiple `scripts:` silently run **only the first** |
| B3 | **High** | `go.mod` requires Go 1.27 but CI/`.go-version` pin Go 1.26 → clean CI build fails |
| B4 | **Medium** | Manifest-declared `remotes:` is parsed and validated but **never wired** to resolution |
| B5 | **Medium** | `lxm include` is a no-op stub; its helpers are dead code |
| B6 | **Medium** | Recipe `sudo:` is documented as enforced but **never read** |
| B7 | **Medium** | `--format json` is ignored by `disk gc` / `vswitch gc` |
| B8 | **Medium** | `lxdbr0` hardcoded as default NIC parent even on Incus (`incusbr0`) |
| B9 | **Low** | `internal/lxm` is a dead legacy package (~300 impl + 1.1k test LOC) |
| B10 | **Low** | Exit-code mapping is triplicated across `apply` and `output` |

The remainder of this document expands these, records additional design observations, and proposes a
prioritized improvement/extension roadmap.

---

## 2. What Works Well (Credit Where Due)

### 2.1 Architecture
- **Clean layering.** `config → plan → apply → provider` with `network`, `fleet`, `recipe`, `output`
  as supporting domains. There is no circular import and the dependency graph in `ARCHITECTURE.md`
  matches the code.
- **Provider abstraction is real.** `provider.Driver` (`internal/provider/driver.go`) composes six
  service interfaces and carries **no LXD/Incus SDK types** in the core. The `fake.FakeDriver`
  (928 LOC) is used by the entire test suite, which is why the suite is fast and hermetic.
- **Plan is pure and serializable.** `plan.Reconciler.Compute` takes all live state as parameters
  (including image aliases and remotes) and returns a deterministic `*Plan`. This is exactly the
  right shape for offline planning and testing.
- **Apply is phase-ordered** (`apply.go:89-300`): Phase −1 image fetch → Phase 0 volume
  provisioning → Phase 1 network (ACLs before vswitches) → Phase 2 instances → Phase 3 volume
  deletion → Phase 4 network deletion. Phase-abort semantics are explicit and defensible.
- **Ownership by marker, not name** (`user.lxm.managed: "true"`). Name conventions are treated as
  ergonomics only. This is the correct primitive for safe GC/adoption.
- **Deterministic network compiler.** `internal/network/compile.go` emits generators G0–G9 in a
  fixed order, dedups and sorts rules, and uses real prefix decomposition (`SubtractCIDRs`). The
  bridge-vs-OVN behavioral split (host-gateway protection, DNS carve-outs, port-53 leak sealing) is
  carefully reasoned and documented in `NETWORK-SPEC.md`.

### 2.2 Engineering hygiene
- **Test volume and coverage are strong:** ~18.4k test LOC vs ~16.2k implementation LOC, 524 test
  functions. Coverage on the core: `plan` 93.4%, `config` 91.4%, `output` 91.9%, `apply` 86.7%,
  `fleet` 81.1%, `recipe` 81.4%, `network` 75.2%.
- **`go build ./...`, `go vet ./...`, and `go test -race ./...` all pass** on the review host.
- **Linting is configured seriously** (`.golangci.yml`): `errcheck`, `gosec`, `errorlint`,
  `noctx`, `gocritic`, `forcetypeassert`, plus `nolintlint` requiring explanations.
- **Security defaults are least-privilege:** sudo and SSH-key injection are opt-in; `known_hosts`
  is tool-managed under `syscall.Flock`; remote trust is TOFU-pinned with mTLS.
- **Documentation is a first-class asset:** 32 markdown docs plus six authoritative specs
  (NETWORK/STORAGE/IMAGE/VM/MANIFEST/CLI). `ARCHITECTURE.md` even honestly self-documents its own
  ownership-tagging gaps (§4.5).

---

## 3. Correctness Defects

### B1 — `status: absent` vswitch panics fleet union (High)

**Where:** `internal/network/fleet.go:251-256`

```go
func (f *Fleet) managedSubnets() []string {
    out := make([]string, 0, len(f.VSwitches))
    for _, vs := range f.VSwitches {
        out = append(out, vs.Subnet.String()) // Subnet is nil for absent vswitches
    }
    return out
}
```

In `Union` (`fleet.go:138-151`), `Subnet` is only populated when `raw.Status != "absent"`:

```go
var ipnet *net.IPNet
if raw.Status != "absent" {
    _, ipnet, err = ParseSubnet(raw.IPv4)
    ...
}
```

`managedSubnets()` is then called unconditionally (`fleet.go:229`) and appends `vs.Subnet.String()`.
`(*net.IPNet)(nil).String()` returns the literal `"<nil>"`, which `CanonicalizeCIDRs` rejects.

**Verified repro** (temporary white-box test):
```
Union err: invalid CIDR address: <nil>
```

**Impact:** Any manifest that declares a vswitch with `status: absent` (the documented deletion
path) fails `lxm plan`/`lxm apply` with exit 3 and a confusing message. The `delete_vswitch`
reconciliation logic in `plan/network.go:147-162` is therefore unreachable in practice.

**Fix:** skip absent vswitches in `managedSubnets()`, or guard `if vs.Subnet != nil`.

---

### B2 — Multi-script recipes run only the first script (High)

**Where:** `internal/apply/apply.go:1054-1055`

```go
scriptFile := rStep.Path
if len(rMeta.Scripts) > 0 {
    scriptFile = rMeta.Scripts[0]
}
```

The recipe schema explicitly permits one-or-more scripts
(`internal/recipe/schemas/recipe_v1.cue`): `scripts: [...string] & [_, ...]`, and the docs
(`docs/howto/provision-with-recipes.md:110`) advertise a metadata file that "wraps one or more
scripts". Only `Scripts[0]` is ever hashed or executed. The remaining scripts are silently dropped —
no error, no warning.

**Impact:** Silent under-provisioning. A user who moves a multi-step bootstrap into one metadata
file gets only the first step applied and a green result.

**Fix:** either loop over all `rMeta.Scripts` in `executeRecipes`, or tighten the schema to exactly
one script and document that. Silently ignoring input is the worst of the three options.

---

### B3 — Build toolchain mismatch breaks CI (High)

**Where:** `go.mod:3` (`go 1.27.0`) vs `.go-version` (`1.26.1`) vs
`.github/workflows/{ci,docs,release}.yml` (`go-version: '1.26'`).

The codebase actively uses **Go 1.27-only** features: `errors.AsType` (`cmd/lxm/main.go:114`),
`sync.OnceValues` (`main.go:69`), `wg.Go` (`internal/provider/common/terminal.go:69`), and the
`omitzero` JSON tag (many files). A Go 1.26 toolchain cannot compile this. `go.mod` has no
`toolchain` directive, so a 1.26 environment will not auto-upgrade.

**Impact:** The documented CI (`README` "Go 1.26+ to build from source") and the CI workflow are
both wrong; a clean runner fails. The review host happens to have Go 1.27, which masks the problem.

**Fix:** bump CI and `.go-version` to 1.27, or add `toolchain go1.27.0` and update the README
requirement. Keep all four in sync.

---

### B4 — Manifest `remotes:` is dead input (Medium)

**Where:** `internal/config/config.go:172-179` (type + `Remotes` field), `config.go:680-687`
(validation), `internal/provider/remote/resolver.go:23,116-118` (`ManifestRemotes` branch).

The resolver *can* consult `opts.ManifestRemotes`, but a repo-wide search shows
**no code ever populates `ResolveOptions.ManifestRemotes`**, and there is no conversion from
`config.RemoteConfig` to `remote.RemoteEntry`. The only `RemoteEntry` constructions are the
built-in `local` and `lxm remote add`.

**Impact:** A manifest that declares `remotes:` passes validation and silently has no effect; the
remote must still exist in `~/.config/lxm/remotes.yaml`. This is a surprising failure mode for a
declarative tool. Either wire it up or reject the field.

---

### B5 — `lxm include` is a no-op; helpers are dead (Medium)

**Where:** `cmd/lxm/commands.go:1494-1503`

```go
RunE: func(cmd *cobra.Command, args []string) error {
    return nil
},
```

The command is registered (`commands.go:42`) and listed in the README, but does nothing. Meanwhile
`config.AddIncludeToYAMLFile` and `config.HasIncludeInYAMLFile` (`config.go:1762,1823`) exist and
are referenced by **no non-test caller**. This is both a broken UX and dead code.

**Fix:** implement it using the existing helpers, or remove the command and helpers.

---

### B6 — Recipe `sudo:` is never enforced (Medium)

**Where:** `internal/recipe/recipe.go:34` (`Sudo bool`)

`sudo` is declared in `RecipeMetadata`, accepted by the CUE schema, and documented as "explicit
opt-in to passwordless sudo for the script"
(`docs/howto/provision-with-recipes.md:136`). A search for `.Sudo` outside its definition and test
files returns **nothing** — it is never read by `executeRecipes` or the executor. The recipe still
runs as `run_as` (default `root`), but the per-recipe sudo semantics the docs promise do not exist.

**Fix:** either implement it (e.g. wrap the script in `sudo -n` / inject a sudoers rule) or remove
the field and the documentation claim.

---

### B7 — `--format json` ignored by GC commands (Medium)

**Where:** `cmd/lxm/gc.go` (both `disk gc` and `vswitch gc`)

Neither command checks `opts.format`; both print human tables unconditionally and never populate
`lastCommandResults`. Because the global `--format` flag is accepted, `lxm disk gc --format json`
produces a `lxm/result/v1` envelope with an empty `results` array while the actual findings were
written as text to stdout. This violates the project's own "Machine Interface First" principle
(ARCHITECTURE §1.3).

**Fix:** honor `--format` and populate structured results.

---

### B8 — `lxdbr0` hardcoded as default parent on Incus (Medium)

**Where:** `internal/plan/plan.go:495,725`, `internal/network/integrity.go:37`

```go
parent := n.Parent
if parent == "" {
    parent = "lxdbr0"
}
```

When a manifest omits `parent`, lxm assumes `lxdbr0`. On Incus the stock bridge is `incusbr0`.
Despite the dual-provider positioning, this default is LXD-specific and will silently attach (or
fail to attach) NICs to a nonexistent network on Incus.

**Fix:** resolve the default per provider (`incusbr0` for Incus) or require an explicit `parent`.

---

### B9 — `internal/lxm` is dead legacy code (Low)

**Where:** `internal/lxm/` — `container.go`, `devices.go`, `manager.go`, `script.go`,
`shell.go`, `wait.go` (~300 LOC) plus `lxm_test.go` (1,118 LOC).

No non-test file imports `internal/lxm`; `go list -deps ./cmd/lxm` does not include it. The test
file is `package lxm` (white-box) and does not test any public surface. It appears to be the
pre-provider-abstraction implementation. Its 48.8% coverage is meaningless because nothing ships.

**Fix:** delete the package. It is misleading (duplicate `Manager`, `wait.go`, `shell.go` concepts
that now live in `apply`/`fleet`/`provider/common`).

---

### B10 — Exit-code mapping is triplicated (Low)

`internal/output/envelope.go:75-96` (`ExitCodeToErrorCode`), `internal/apply/apply.go:1180-1219`
(`errorCodeToExit` + `exitToErrorCode`), and the `selectWorstExitCode` precedence map
(`apply.go:1160-1178`) all encode the same 0–7 catalog. Three copies will drift. The precedence map
in particular is non-obvious (`4→5, 5→4, …`) and easy to break.

**Fix:** one `output` package table with rank-based selection.

---

## 4. Design & Robustness Observations

These are not defects but limit the tool's ceiling or violate its own stated principles.

### 4.1 Plan is serializable but not usable as an apply input
ARCHITECTURE §1.2 calls the plan "a first-class artifact". In practice there is no
`lxm plan -o plan.json` / `lxm apply --plan plan.json`, and `plan` exits 0 whether or not there are
changes. The "review then apply the exact reviewed plan" workflow the design implies is not
possible. Adding `--detailed-exitcode` (Terraform-style) and apply-from-plan would materially
improve CI ergonomics.

### 4.2 Purity leak in `ComputeNetworks`
`internal/plan/network.go:70-80` mutates its input: `vs.DNSResolvers = resolvers`. The reconciler is
documented as pure; mutating the fleet it was handed makes repeated/reused plans order-dependent and
complicates testing. Compute the derived resolvers into a local map instead.

### 4.3 Non-recursive manifest discovery
`cmd/lxm/helpers.go:21` uses `os.ReadDir` (single level). `docs/howto/author-manifests.md` implies a
`config/` layout and the README quick-start uses `config/dev.yaml`, but a nested
`config/staging/app.yaml` is silently ignored by `lxm apply config/`. A recursive walk (or an
explicit glob) is expected for "fleet directory" semantics.

### 4.4 No dependency ordering between instances
Phase 2 runs instance steps concurrently (`apply.go:231-255`) with no `depends_on` primitive. A
manifest that mounts a volume or network produced by another instance has no ordering guarantee
beyond the coarse phase barriers. For real fleets this is the most-requested missing feature.

### 4.5 Readiness checks are hardcoded to systemd/cloud-init
`checkWaitPolicy` (`apply.go:789-1016`) shells out to `systemctl is-system-running`,
`cloud-init status --wait`, and `hostname -I`. This excludes Alpine, non-systemd images, and
non-cloud-init provisioners. A configurable/pluggable readiness probe would generalize the model.

### 4.6 Context is not propagated into SDK calls
Drivers accept `ctx`, but the LXD/Incus SDK calls (`GetInstance`, `ListInstances`, `GetNetworks`,
…) are synchronous and context-free; only async operations are wrapped by `WaitOpContext`
(`provider/common/exec.go`). A `SIGINT` during a blocking HTTP GET is not honored. This is partly
an SDK limitation, but it means the documented "operation cancellation" guarantee (ARCHITECTURE
§4.4) is weaker than stated.

### 4.7 N+1 provider round-trips during planning
`cmd/lxm/commands.go:1838-1846` iterates `ListInstances` results and calls `GetInstance` again for
each — even though `GetInstancesFull` already returned config/devices. On a large fleet this doubles
the API calls before planning even begins.

### 4.8 Doctor assumes LXD
`cmd/lxm/commands.go:1581` unconditionally reports `[OK] LXD socket reachable`, and the group check
(1659-1677) only looks for the `lxd` group — never `incus-admin`. The diagnostic should reflect the
detected provider.

### 4.9 Ownership gaps remain (self-documented)
ARCHITECTURE §4.5 admits: storage volumes only recently got markers, mount devices and the
plan-path NIC device have none, and snapshots/images rely on prefix/alias identity. The unified
marker-keyed inventory (`lxm list --all`) is still not implemented; each resource class needs its
own probe. Snapshot GC matching a name prefix can, in principle, delete a foreign snapshot.

### 4.10 Global mutable command state
`lastComputedPlan`, `lastApplyReport`, `lastCommandResults` (`cmd/lxm/main.go:30-34`) are package
globals reset per invocation. They make the CLI hard to test in parallel and couple command
handlers to the envelope emitter through hidden state. A per-invocation result context is cleaner.

### 4.11 Provider driver test coverage is thin
`internal/provider/incus` 30.9%, `internal/provider/lxd` 30.3%, `internal/provider/remote` 30.1%.
All higher-level tests use `FakeDriver`, so the actual SDK translation layer (the part most likely
to break across daemon versions) is the least tested. There are no automated integration tests in
CI; `simulation/` is manual.

### 4.12 Stale lint configuration
`.golangci.yml` excludes `staticcheck` on `internal/lxd/fake*.go`, but `internal/lxd` no longer
exists. These exclusions are dead.

### 4.13 Minor: manual flag parsing in `ssh`
`newSSHCmd` sets `DisableFlagParsing: true` and hand-rolls `--format`/`--remote`/`--provider`/…
parsing (`commands.go:1355+`). It duplicates cobra and is easy to desynchronize from the global
flags. A `cobra.FParseErrWhitelist` + `Args` approach would be safer.

### 4.14 Minor: magic status codes
`102`/`103` are used as bare literals for Stopped/Running throughout (`apply.go:496,621,827,966`,
etc.). Named constants in `provider` would reduce misclassification risk.

---

## 5. Feature-Set Assessment

### Present and credible
- Plan/apply/diff with deterministic diffs and `--dry-run`.
- Dual provider (Incus/LXD) with auto-detection and local/remote mTLS.
- Containers **and** VMs; CPU/memory/disk limits; VM boot mode/hugepages/raw.qemu.
- Managed bridges and OVN overlay switches with group-based `network_policy` → ACLs.
- Managed data disks (filesystem/block, managed vs external ownership) with grow/delete lifecycle.
- Cloud-image fetch (`image: remote:alias`) with type-qualified local aliases and a cache probe.
- Recipes (inline scripts and `lxm/recipe/v1` metadata) with content-hash idempotency, retries,
  env, snapshots.
- Cloud-init composition/inheritance, template variables, include/remove/replace directives.
- Fleet selectors (`-g`, `--exclude-group`, `--name`), bounded parallel apply, prune, GC.
- Structured `lxm/result/v1` envelopes and a categorized 0–7 exit-code catalog.
- `doctor`, `compile`/v1→v2 migration, snapshot create/list/delete/gc, rollback, shell/ssh.

### Notable gaps / opportunities
- **No apply-from-plan / plan artifact persistence** (see 4.1).
- **No drift-only CI mode** (`plan --detailed-exitcode`).
- **No inter-resource dependency graph** (`depends_on`).
- **No cross-resource unified inventory** (marker-keyed `list --all`).
- **No `lxm import`** to adopt existing unmanaged resources.
- **No state locking / concurrent-apply guard** beyond ETag OCC.
- **No secret management** — cloud-init and env values live in plaintext manifests.
- **No pluggable provisioners** (Ansible, ignition) beyond shell recipes.
- **No multi-target fan-out** (`--target a,b`) or cluster evacuation orchestration.
- **No health/status watch** (`status --wait`).
- **`internal/logging/` is an empty directory** — suggests planned structured logging that never
  landed; `slog` is currently wired only in the CLI.
- **No SBOM/signing** in the release pipeline (goreleaser produces checksums only).

---

## 6. Prioritized Recommendations

### P0 — Correctness (do first)
1. **B1** guard `managedSubnets()` against nil subnets; add a regression test for
   `status: absent` vswitches.
2. **B2** execute all recipe scripts (or reject multi-script metadata explicitly).
3. **B3** align `go.mod`/`.go-version`/CI on Go 1.27.
4. **B4** wire manifest `remotes:` through to `ResolveOptions.ManifestRemotes` (or reject the field).
5. **B6** implement or remove recipe `sudo:`.
6. **B8** provider-aware default NIC parent.
7. **B7** honor `--format json` in `disk gc` / `vswitch gc`.

### P1 — Hygiene / leverage
8. **B9** delete `internal/lxm`.
9. **B5** implement or delete `lxm include` and its helpers.
10. **B10** consolidate exit-code mapping into `internal/output`.
11. **4.2** remove the `ComputeNetworks` input mutation.
12. **4.12** remove stale `internal/lxd` lint exclusions.
13. **4.7** drop the redundant per-instance `GetInstance` during planning.

### P2 — Capability extensions (highest user value)
14. **Apply-from-plan + `plan -o` + `--detailed-exitcode`** — makes the "plan as artifact" principle
    real and enables safe CI gating.
15. **`depends_on` ordering** between instances and between instances and networks/volumes.
16. **Recursive manifest discovery** with an explicit include/exclude glob.
17. **Unified marker-keyed inventory** (`lxm list --all`) and marker completion for mounts/NICs/
    snapshots (closes ARCHITECTURE §4.5).
18. **`lxm import`** for adopting existing resources, with a dry-run adoption plan.
19. **Pluggable readiness probes** and provisioners (replace hardcoded `systemctl`/`cloud-init`).
20. **Secret handling** (env references / external secret files) and redaction in plan JSON.

### P3 — Operational maturity
21. **Provider integration test matrix** in CI (Incus + LXD, container + VM) using the existing
    `simulation/` harness, plus per-driver contract tests.
22. **Structured logging** via the empty `internal/logging` package and `slog` context propagation.
23. **State locking / concurrent-apply protection** (server-side lock or local lock file).
24. **Release hardening**: signed checksums, SBOM, provenance.
25. **Per-invocation result context** replacing the three package globals in `cmd/lxm`.

---

## 7. Verification Notes

Commands run on the review host (Go 1.27.0):

```
go build ./...        # exit 0
go vet ./...          # exit 0
go test ./...         # all packages ok
go test -race ./...   # apply/plan/network/config/cmd ok
go test ./... -cover  # plan 93.4%, config 91.4%, apply 86.7%, ...
./lxm --help          # 20 commands registered
./lxm doctor --skip-remote /tmp
./lxm apply /nonexistent --format json   # correct envelope, exit 5
```

The B1 defect was confirmed with a temporary white-box test in `internal/network` which returned
`invalid CIDR address: <nil>` for a `status: absent` vswitch; the test file was removed after
verification. B2–B10 were confirmed by static search and/or CLI inspection as described in each
finding.

---

## 8. Closing Assessment

`lxm` is a strong, thoughtfully engineered tool with an architecture that will scale — the
provider abstraction and pure-plan core are the right long-term bets. The defects found are
localized and fixable in days, not weeks, and the two highest-impact ones (absent vswitch, multi-
script recipes) affect exactly the declarative/lifecycle features the tool is sold on. The biggest
*strategic* gap is that the plan — the tool's central abstraction — cannot yet be persisted, diffed
for CI exit status, or applied directly, which leaves much of its value on the table.

Fix the P0 list, delete the dead legacy package, then invest in apply-from-plan and dependency
ordering; that sequence converts a good tool into a dependable one.
