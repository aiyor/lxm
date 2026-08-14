# Static Analysis & Edge-Case Safeguards Review

This document reviews the analysis gaps in the initial test and CI pipeline, documents latent edge cases uncovered by static analyzers, and specifies the safeguards implemented across the codebase.

---

## 1. Context & Root Cause: Why Early Testing Missed These Issues

During early development and testing, several classes of edge cases and formatting/style discrepancies bypassed automated checks. The root causes were:

1. **CI Pipeline Omission**:
   - The GitHub Actions workflow ([.github/workflows/ci.yml](file:///mnt/devel/tools/lxm/.github/workflows/ci.yml)) only executed `gofmt -l .`, `go vet ./...`, and unit tests (`go test -race`).
   - `golangci-lint` was configured in [Taskfile.yml](file:///mnt/devel/tools/lxm/Taskfile.yml) for local use but was **never wired into CI**.
2. **Unit Test Blind Spots**:
   - Unit tests validate expected inputs against mocked responses (e.g. `FakeInstanceServer`).
   - They do not detect static bugs in error paths, unhandled context cancellations, bypassed `defer` statements during `os.Exit`, or fragile error-wrapping patterns across untrodden branches.
3. **Absence of Dedicated Linter Configuration**:
   - The repository lacked a checked-in [.golangci.yml](file:///mnt/devel/tools/lxm/.golangci.yml) file.
   - Without explicit linter rules, standard `go vet` only performs shallow compiler checks, missing critical security, error flow, and concurrency safeguards.

---

## 2. Edge Cases Identified & Remediated

Deep static analysis using specialized analyzers (`nilerr`, `noctx`, `errorlint`, `gocritic`, `gosec`, `forcetypeassert`) detected the following concrete bugs and design risks:

### A. Swallowed Infrastructure Errors (`nilerr`) [FIXED]
* **Location**: [internal/lxm/container.go:248-255](file:///mnt/devel/tools/lxm/internal/lxm/container.go#L248-L255) *(Note: `internal/lxm` is a legacy package per `VM-SPEC.md §7.4`)*
* **Issue**: If `GetInstance` failed due to network outage or daemon error, it logged "does not exist" and returned `nil`.
* **Fix**: Integrated `ClassifyLXDError` to verify exit code `5` (not found) before suppressing the error, returning real errors if any other failure occurred.

### B. Uncancellable Subprocesses & Orphaned Processes (`noctx`) [FIXED]
* **Locations**:
  - [cmd/lxm/commands.go:1218](file:///mnt/devel/tools/lxm/cmd/lxm/commands.go#L1218) (`exec.CommandContext(ctx, "ssh", ...)`)
  - [internal/fleet/known_hosts.go:72](file:///mnt/devel/tools/lxm/internal/fleet/known_hosts.go#L72) (`exec.CommandContext(ctx, "ssh-keygen", "-R", ...)`)
  - [internal/fleet/known_hosts.go:99](file:///mnt/devel/tools/lxm/internal/fleet/known_hosts.go#L99) (`exec.CommandContext(ctx, "ssh-keygen", "-F", ...)`)
  - [internal/fleet/known_hosts.go:116](file:///mnt/devel/tools/lxm/internal/fleet/known_hosts.go#L116) (`exec.CommandContext(ctx, "ssh-keygen", "-F", ...)`)
  - [internal/fleet/known_hosts.go:124](file:///mnt/devel/tools/lxm/internal/fleet/known_hosts.go#L124) (`exec.CommandContext(ctx, "ssh-keyscan", ...)`)
  - [internal/lxm/shell.go:29](file:///mnt/devel/tools/lxm/internal/lxm/shell.go#L29) (`exec.CommandContext(ctx, lxcPath, ...)`)
* **Issue**: `exec.Command(...)` without `context.Context` meant child subprocesses ignored signals and timeouts.
* **Fix**: Converted all 6 sites to `exec.CommandContext(ctx, ...)` with context propagation.

### C. Broken Error Wrapping Chains (`errorlint`) [FIXED]
* **Location**: [internal/lxm/manager.go:71](file:///mnt/devel/tools/lxm/internal/lxm/manager.go#L71)
* **Issue**: Used `fmt.Errorf("%w: %v", ErrWaitTimeout, err)`, discarding the inner error chain.
* **Fix**: Updated format verb to `%w: %w` so `errors.Is`/`errors.As` traverses the full chain.

### D. Bypassed `defer` Handlers on Immediate Exit (`gocritic: exitAfterDefer`) [FIXED]
* **Locations**:
  - [cmd/lxm/main.go:52](file:///mnt/devel/tools/lxm/cmd/lxm/main.go#L52) (`cancel()` before `os.Exit(code)`)
  - [cmd/lxm/main_test.go:26](file:///mnt/devel/tools/lxm/cmd/lxm/main_test.go#L26)
  - [internal/apply/apply_test.go:25](file:///mnt/devel/tools/lxm/internal/apply/apply_test.go#L25)
  - [internal/fleet/known_hosts_test.go:18](file:///mnt/devel/tools/lxm/internal/fleet/known_hosts_test.go#L18)
* **Issue**: Calling `os.Exit(m.Run())` terminated the process immediately without running deferred `defer os.RemoveAll(tmpDir)`, leaking `/tmp` files on every run.
* **Fix**: Captured exit code explicitly, cleaned up `tmpDir`, and then called `os.Exit(code)`.

### E. Struct Slice Appends Without Reassignment (`gocritic: appendAssign`) [FIXED]
* **Locations**:
  - [internal/config/config.go:984](file:///mnt/devel/tools/lxm/internal/config/config.go#L984) (`res.Mounts`)
  - [internal/config/config.go:990](file:///mnt/devel/tools/lxm/internal/config/config.go#L990) (`res.Networks`)
  - [internal/config/config.go:996](file:///mnt/devel/tools/lxm/internal/config/config.go#L996) (`res.Recipes`)
* **Issue**: Appending to `base` slices with excess capacity mutated the underlying backing array of `base`.
* **Fix**: Cloned slice headers (`append(append(T(nil), base...), overlay...)`) ensuring immutable base slices.

### F. Unchecked Type Assertions (`forcetypeassert`) [FIXED]
* **Location**: [internal/config/config.go:704](file:///mnt/devel/tools/lxm/internal/config/config.go#L704)
* **Issue**: `deepMerge` result was type-asserted without checking `ok`.
* **Fix**: Safely checked `if merged, ok := deepMerge(...).(map[string]interface{}); ok`.

---

## 3. Active Static Analysis Safeguards (.golangci.yml)

The following suite of analyzers is enabled in [.golangci.yml](file:///mnt/devel/tools/lxm/.golangci.yml):

| Linter | Category | Safeguard Provided |
| :--- | :--- | :--- |
| **`errorlint`** | Error Handling | Enforces standard Go 1.13+ error wrapping (`%w`) and checks that `errors.Is`/`errors.As` are used. |
| **`nilerr`** | Error Handling | Catches swallowed errors where an error is verified non-nil but `nil` is returned. |
| **`noctx`** | Concurrency / OS | Flags HTTP requests and `os/exec` invocations that do not pass a `context.Context`. |
| **`gocritic`** | Bugs & Diagnostics | Detects subtle bugs, skipped `defer`s on `os.Exit`, slice mutation side-effects, and performance anti-patterns. |
| **`gosec`** | Security | Inspects source code for security vulnerabilities, weak file permissions, and injection vectors (enforced via per-site review annotations). |
| **`forcetypeassert`** | Robustness | Enforces checked type assertions across dynamic interface decoding. |
| **`errcheck`** | Robustness | Ensures unchecked error returns from I/O and critical functions are flagged and handled. |
| **`govet`** | Standard Vet | Core Go vet checks (printf formatting, struct tags, lock copying, unreachable code). |
| **`staticcheck`** | Core Static Analysis | Advanced static analysis for bugs, dead code, and standard library simplifications. |
| **`ineffassign`** | Dead Code | Detects ineffectual variable assignments. |
| **`unused`** | Dead Code | Flags unused constants, variables, functions, and struct fields. |
| **`unconvert`** | Cleanliness | Detects redundant type conversions. |
| **`misspell`** | Documentation | Catches spelling mistakes in logs, comments, and error messages. |
| **`nolintlint`** | Hygiene & Integrity | Ensures `//nolint` directives are actively used, specific, and include explanatory justification. |

---

## 4. CI/CD & Local Pipeline Enforcement

### A. GitHub Actions CI ([.github/workflows/ci.yml](file:///mnt/devel/tools/lxm/.github/workflows/ci.yml))

Integrated `golangci-lint-action@v6` before the test step so pull requests and commits are verified automatically:

```yaml
      - name: Check gofmt
        run: |
          if [ -n "$(gofmt -l .)" ]; then
            echo "The following files are not formatted properly:"
            gofmt -l .
            exit 1
          fi

      - name: Run golangci-lint
        uses: golangci/golangci-lint-action@v6
        with:
          version: v2.12.2

      - name: Run go vet
        run: go vet ./...

      - name: Run tests with race detector
        run: go test -v -race -cover ./...
```

### B. Taskfile Integration ([Taskfile.yml](file:///mnt/devel/tools/lxm/Taskfile.yml))

- `task lint` runs both formatting and `golangci-lint run ./...`.
- `task build` and `task test` enforce formatting and compilation.

---

## 5. Completed Implementation Status

- [x] **Configured `.golangci.yml`** with 13 comprehensive linters and tuned exclusions.
- [x] **Updated `.github/workflows/ci.yml`** to enforce `golangci-lint` in CI.
- [x] **Remediated all identified bugs**:
  - [x] Fixed `DeleteContainer` error checking in `internal/lxm/container.go`.
  - [x] Threaded `context.Context` into `exec.CommandContext` across all 6 sites (`commands.go`, `known_hosts.go`, `shell.go`).
  - [x] Fixed error wrapping `%w: %w` in `internal/lxm/manager.go`.
  - [x] Isolated slice appends in `internal/config/config.go` with slice cloning.
  - [x] Fixed `os.Exit` and `TestMain` `/tmp` directory cleanup in `main.go`, `main_test.go`, `apply_test.go`, and `known_hosts_test.go`.
  - [x] Checked type assertions in `internal/config/config.go:deepMerge`.
- [x] **Verified**: Full pipeline passes cleanly with 0 lint issues and 100% test success.
