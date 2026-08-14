# safeguards.md — Independent Review & Assessment

Reviewer: opencode (independent). Date: 2026-08-14.

Scope: `safeguards.md` (static-analysis & edge-case safeguarding plan), the fix
commit `6ad66c5` ("fix: various issues caught by lint"), the committed
`.golangci.yml`, `Taskfile.yml`, `.github/workflows/ci.yml`, and the current
source tree. Every A–E claim was re-verified by reading the code and by running
the linters directly.

---

## 0. Verdict (short)

The `safeguards.md` list is **factually accurate** — all five latent-edge-case
claims (A–E) exist in the code exactly as cited, with correct line numbers — and
adding a `.golangci.yml` + CI lint gate is **a good idea**. However, the plan as
written is **not actually in effect**: the committed `.golangci.yml` enables
*none* of the proposed linters, so the pipeline is blind to the very bugs the
document exists to catch. There are also four accuracy/severity gaps that should
be corrected before the document is treated as authoritative.

---

## 1. Accuracy of the A–E claims (all verified real)

| Claim | Location | Verified status |
| :--- | :--- | :--- |
| A. Swallowed infra error (`nilerr`) | `internal/lxm/container.go:248-252` | **Confirmed** — `GetInstance` error → `return nil`. |
| B. Uncancellable subprocesses (`noctx`) | `commands.go:1218`, `known_hosts.go:72,99` | **Confirmed**, but **incomplete** (see §2.3). |
| C. Broken error wrapping (`errorlint`) | `internal/lxm/manager.go:71` | **Confirmed** — `fmt.Errorf("%w: %v", ErrWaitTimeout, err)`. |
| D. Bypassed `defer` on `os.Exit` (`exitAfterDefer`) | `main.go:52`, `main_test.go:26`, `apply_test.go:25`, `known_hosts_test.go:18` | **Confirmed** — and worse than stated (see §2.5). |
| E. Slice append without reassignment (`appendAssign`) | `config.go:984,990,996` | **Confirmed** — `res.Mounts = append(base.Mounts, ...)`. |

Confirmation via a direct run against the tree:

```
$ golangci-lint run ./...                                  → 0 issues
$ golangci-lint run --enable=nilerr,noctx,errorlint,gocritic,unparam,unconvert,misspell ./...  → 30 issues
```

The 30 issues reproduce A (1× `nilerr`), C (1× `errorlint`), D (4×
`exitAfterDefer`), E (3× `appendAssign`), plus 3× `noctx`, 3× `ifElseChain`, and
15× `unparam`.

---

## 2. Critical findings

### 2.1 The committed `.golangci.yml` does not enable the proposed linters

The `.golangci.yml` added by `6ad66c5` contains **only** `errcheck` settings and
exclusion rules. There is no `linters.enable:` list, so golangci-lint v2 falls
back to its default set (`errcheck`, `govet`, `ineffassign`, `staticcheck`,
`unused`). None of the nine linters in §3 of `safeguards.md` (`errorlint`,
`nilerr`, `nilnil`, `noctx`, `gocritic`, `unparam`, `unconvert`, `misspell`) is
enabled.

Consequence: `golangci-lint run ./...` reports **0 issues** while all A–E bugs
remain in the code. The §2 findings were evidently produced by a one-off manual
`--enable=...` run, not by the committed configuration. The document must (a)
state this, and (b) actually add an `enable:` list for the proposal to be real.

### 2.2 The commit fixed style, not the A–E bugs

`6ad66c5` remediated:

- `applyErr` was being silently dropped in `newApplyCmd` (`commands.go`) — **a
  genuine bug fix**, and the one substantive change.
- `staticcheck` ST1005 capitalization (`Failed to connect to LXD` → lowercase,
  `Key %q` → `key %q`).
- Removed unused struct fields `gc`, `outDir`, `insecure` (`root.go`).
- `if/else` → `switch` refactors (`config.go`, `migrator.go`, `lxd.go`).
- `WriteString`+`Sprintf` → `fmt.Fprintf` (`known_hosts_test.go`).

**None of A–E was fixed.** That is acceptable only because §5 leaves them
unchecked (`[ ]`), but the commit message "fix: various issues caught by lint"
overstates it — the A–E bugs remain "caught" but "not fixed."

### 2.3 The `noctx` list is incomplete

§2-B lists 3 sites. There are **6** non-test `exec.Command` calls in the tree:

| Site | Listed? |
| :--- | :--- |
| `cmd/lxm/commands.go:1218` (`ssh`) | yes |
| `internal/fleet/known_hosts.go:72` (`ssh-keygen -R`) | yes |
| `internal/fleet/known_hosts.go:99` (`ssh-keygen -F`) | yes |
| `internal/fleet/known_hosts.go:116` (`ssh-keygen -F`) | **no** |
| `internal/fleet/known_hosts.go:124` (`ssh-keyscan`, mitigated by `-T 5`) | **no** |
| `internal/lxm/shell.go:29` (`lxc exec`) | **no** (dead code) |

The three omitted sites are equally uncancellable subprocesses and should be
added to the remediation list.

### 2.4 A and C are in dead code (`internal/lxm`)

`internal/lxm` is **unreferenced by any non-test code**. `cmd/lxm` wires
`internal/plan`/`internal/apply`; the only `internal/lxm` references in the
module are its own tests and comments. Bugs A (`container.go`) and C
(`manager.go`) are therefore in dead-but-compiled code, and the §2.3 `noctx`
site in `internal/lxm/shell.go` too.

`VM-SPEC.md` §7.4 already documents this package as legacy. `safeguards.md`
presents A and C as live production bugs without that context, inflating their
severity. The honest treatment is either "delete `internal/lxm`" (recommended —
it removes A, C, and the shell `noctx` site entirely) or a clear
"dead-code / low severity" annotation.

### 2.5 Finding D is worse than stated

In `cmd/lxm/main_test.go:24`, `internal/fleet/known_hosts_test.go:16`, and
`internal/apply/apply_test.go:24`, the pattern is:

```go
tmpDir, _ := os.MkdirTemp("", "lxm_*")
os.Setenv("LXM_KNOWN_HOSTS_FILE", filepath.Join(tmpDir, "known_hosts"))
defer os.RemoveAll(tmpDir)   // never runs
os.Exit(m.Run())
```

Because `os.Exit` terminates without running deferred functions, the
`defer os.RemoveAll(tmpDir)` is **dead code** — the temp directory leaks on
every test run. This is a real, trivially-fixable resource leak, not merely a
lint nit. The fix is to capture the exit code and clean up before exiting:

```go
code := m.Run()
_ = os.RemoveAll(tmpDir)
os.Exit(code)
```

(`main.go:52` `os.Exit(code)` after `defer cancel()` is the standard CLI-main
idiom and is effectively harmless — the process is about to exit anyway.)

---

## 3. Soundness of the proposed linter list

| Linter | Verdict | Rationale |
| :--- | :--- | :--- |
| `errorlint` | Enable | Standard, low FP; catches C. |
| `nilerr` | Enable | Standard, low FP; catches A. |
| `noctx` | Enable | Correct for a CLI tool; needs full remediation of all 6 sites. |
| `gocritic` | Enable | Umbrella; `exitAfterDefer`/`appendAssign` are exactly D/E. `ifElseChain` is style (the commit already applies it manually). |
| `errcheck` | Enable (already default) | Sound; the `exclude-functions` list is reasonable for a CLI. |
| `unconvert` | Fine | Zero findings; low value but harmless. |
| `misspell` | Fine | Zero findings; low value but harmless. |
| `nilnil` | **Drop / run manually** | High false-positive rate; `(nil, nil)` is often legitimate. Not in any recommended preset. |
| `unparam` | **Drop as a blocking gate** | 15 findings, all false positives from the uniform `newXxxCmd(...)` constructor signatures (unused `ctx`/`stderr`/`opts` are intentional interface consistency). Would require `//nolint` noise for zero benefit. |

---

## 4. Gaps — what the list is missing

1. **`gosec` (security)** — the most notable omission. `lxm` manages SSH host
   keys (`known_hosts.go`), spawns `ssh`/`ssh-keygen`/`ssh-keyscan` subprocesses,
   and writes files under `~/.config/lxm`. G204 (subprocess args), G304 (file
   inclusion), and G306 (file permissions) are directly relevant.
2. **`staticcheck`** — should be named explicitly. It is the workhorse that
   actually produced the commit's fixes (ST1005, unused); the document implies
   the specialized analyzers did the work.
3. **`govet` with extended checks** — CI runs bare `go vet ./...`; enable
   `shadow` and `lostcancel` (or golangci-lint's `govet.enable-all`). Shadowing
   is a common bug source at this codebase's size.
4. **`forcetypeassert`** — `config.go:deepMerge` has several unchecked
   `.(map[string]interface{})` / `.([]interface{})` assertions.
5. Optional, lower priority: `wrapcheck`, `errname`, `makezero`,
   `durationcheck`, `nolintlint`, `revive`, `prealloc`, and complexity gating
   (`gocognit`/`cyclop`) for the large `commands.go` constructors and
   `computeDiffs`.

---

## 5. Process / configuration inconsistencies

1. **CI enforcement (§4A) was not implemented.** `.github/workflows/ci.yml`
   still runs only `gofmt -l`, `go vet ./...`, and `go test -race`. The commit
   did not touch it. The document frames §4 as "Proposed", but the summary makes
   it easy to misread as done.
2. **`task build` depends on `gofmt -s -w .`** — a file-mutating task as a
   dependency of `build` is questionable; builds should not rewrite source as a
   side effect. Consider a separate `fmt` gate that only *checks* (`gofmt -l`)
   in CI, and keep `-w` as an explicit developer action.
3. **`gofmt -l .` (CI) vs `gofmt -s -w .` (Taskfile)** — CI never enforces the
   `-s` simplification. Minor, but the two should agree (either check `-s` in CI
   or drop it locally).
4. **`.golangci.yml` `version: "2"` syntax is correct** for golangci-lint
   v2.12.2 (the pinned `golangci-lint-action` version), so no migration issue —
   the only problem is the missing `enable:` list.

---

## 6. Recommended corrections to `safeguards.md`

1. Add an `linters.enable:` list to `.golangci.yml` matching §3, minus `nilnil`
   and `unparam`, plus `gosec`, `staticcheck` (explicit), `govet` (extended
   checks), and `forcetypeassert`.
2. Wire `golangci-lint-action` into `ci.yml` (currently only proposed).
3. Annotate A, C, and the `internal/lxm/shell.go` noctx site as dead-code/low
   severity — or add "delete `internal/lxm`" as a remediation item (removes
   three findings at once).
4. Complete the `noctx` list with `known_hosts.go:116` and `:124`.
5. Fix the three `TestMain` temp-dir leaks (D) — they are a real resource leak,
   not just lint noise.
6. Correct the §2 preamble: the specialized analyzers were run manually
   (`--enable`), not by the committed config; state this explicitly so the
   document's "detected" language is accurate.

---

## 7. Final assessment

`safeguards.md` is a **well-founded and accurate diagnostic** — its root-cause
analysis (§1) matches the actual CI setup, and its five latent edge cases are
real and correctly located. The *idea* (a committed linter config + CI gate) is
sound and worth doing.

What undermines it is execution and framing: the committed config doesn't turn
on the analyzers the document relies on, the commit didn't fix the bugs it
lists, two of the headline bugs are in dead code, and the `noctx` inventory is
incomplete. Once those are corrected — especially the missing `enable:` list and
the CI wiring — the plan becomes a solid, proportionate safeguard for the
codebase.
