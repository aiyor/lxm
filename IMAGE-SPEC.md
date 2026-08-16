# IMAGE-SPEC — Cloud Image Lookup & Fetch (`image:` remote resolution)

**Feature**: `image: <remote>:<alias>` (the existing `#RemoteAlias` form) resolves and fetches a
cloud image from a simplestreams image server when the image is not already cached in the local LXD
image store, before creating or rebuilding an instance.

**Status**: IMPLEMENTED. The design (including the independent review resolution) lives in
`.scratch/image_design.md`; this spec is the authoritative, locked contract for the implementation.

**Schema**: `lxm/config/v2`. The canonical manifest-schema reference (the field tables) lives in
[`SPEC_MANIFEST.md`](SPEC_MANIFEST.md); keep the two in sync when either changes.

---

## 1. Overview & Goals

`lxm` treats every `image:` reference as a **local** LXD image identity. The plan engine copies
`manifest.Image` verbatim into the create payload (`api.InstanceSource{Alias: ...}`). If the
reference is not already present in the local LXD image store, `lxm apply` fails late with LXD's
`image not found`.

The manifest schema has always accepted the `remote:alias` form (`#RemoteAlias`), and the user guide
documents `image: ubuntu:22.04` as a valid reference — but nothing actually resolved the `remote:`
prefix. This feature makes `remote:alias` **meaningful**: when such a reference is not cached
locally, lxm looks the image up on the named remote (a simplestreams image server) and fetches it
into the local store before creating or rebuilding the instance.

1. Make `image: <remote>:<alias>` resolve and fetch from a cloud image server when the image is not
   already cached locally — with zero new fields required for the common case.
2. Preserve the exact current behaviour for **local** references: a bare alias (`#LocalAlias`) or a
   fingerprint (`#Fingerprint`) never triggers a remote fetch, and a miss fails exactly as it does
   today.
3. Let operators declare **custom remotes** and **override the built-in remotes** with a new
   fleet-scoped `image_remotes:` section, in the same style as `vswitches:` / `network_policy:`.
4. Preserve the plan-first contract: `lxm plan` is deterministic, offline with respect to the image
   remote, and surfaces the fetch as an explicit planned action; `lxm apply` executes it before any
   instance mutation.

### Non-Goals (v1)

- Fetching from LXD servers that require authentication or custom certificates (public simplestreams
  over HTTPS only).
- Per-instance architecture or variant selection (delegated to the LXD daemon's simplestreams
  resolver — host architecture, default variant — identical to `lxc image copy images:… local:`).
- Refreshing already-cached images. Once cached, lxm never re-fetches under the same reference.
- Property-based cache detection. The cache probe keys on a single deterministic alias name (§4.3).

---

## 2. Manifest Schema (`lxm/config/v2`)

### 2.1 `image:` (unchanged, semantics expanded)

| Form | CUE match | Behaviour |
| :-- | :-- | :-- |
| fingerprint | `#Fingerprint` | Local only. Never fetched. Miss fails as today (exit 4). |
| bare alias (no `:`) | `#LocalAlias` | Local only. Never fetched. Miss fails as today (exit 4). |
| `remote:alias` (one `:`) | `#RemoteAlias` | **Remote.** Fetched on miss (§4). |

`ubuntu/22.04` (slash, no colon) matches neither form and is rejected at authoring time (exit 3)
exactly as it is today. The colon form is the **only** remote form.

### 2.2 `image_remotes:` (new, fleet-scoped)

A fleet-scoped registry mapping a remote **name** to a simplestreams **URL**. Declared like
`vswitches:`/`network_policy:` — typically in `_base.yaml`, inherited via `include`, and unioned
across every loaded manifest. Built-in remotes (§4.1) form the base layer; a same-named declaration
**overrides** a built-in.

```yaml
schema: lxm/config/v2
image_remotes:
  ubuntu: https://cloud-images.ubuntu.com/releases   # overrides the built-in (e.g. to a mirror)
  corp-images: https://images.corp.example.com       # custom remote
```

| Field (key) | Type | Default | Rules |
| :-- | :-- | :-- | :-- |
| `<remote>` (map key) | string | — | `=~"^[a-zA-Z0-9_.-]+$"` (same charset as the remote part of `#RemoteAlias`). |
| `<url>` (map value) | string | — | An `http://` or `https://` URL with a non-empty host (Go-validated, §7.2). Scheme `https` is required in production; `http` is rejected unless the host is loopback. Stored and compared in **canonical form** (§2.3). |

Merge semantics (locked):

* **Within an include chain** (`MergeConfigs`): `image_remotes` is merged **key-wise**, overlay
  winning per key.
* **Across sibling manifests** (fleet union): the effective registry is the union of every loaded
  manifest's `image_remotes` plus the built-ins. Identical `(name, url)` duplicates are deduplicated
  silently; the **same name with a different canonical URL** across two loaded manifests fails with
  exit 3 citing both files (mirrors the `vswitches:` conflict rule).

### 2.3 URL canonicalization (locked)

URLs are **canonicalized** before storage and comparison:

1. `url.Parse`; reject unparsable URLs (exit 3).
2. Lowercase the **scheme** and **host**.
3. Trim trailing `/` from the path, and drop the path entirely when empty.
4. The canonical string is the normalized scheme+host+path (port preserved).

`ValidatePostMerge` (Go-side) and the fleet-union conflict check both operate on the canonical form,
so `https://cloud-images.ubuntu.com/releases` and `https://cloud-images.ubuntu.com/releases/` (and
`HTTPS://CLOUD-IMAGES.UBUNTU.COM/releases`) compare equal.

Locked deviations / edge notes (verified against the implementation):

* **Query strings are preserved** in the canonical form (`path?query`), so two URLs differing only by
  `?token=` never compare equal and hide a genuine conflict.
* **The port is preserved verbatim**, so `https://h:443/x` and `https://h/x` compare **unequal**
  (explicit default port vs omitted). This is intentional and conservative — a same-endpoint false
  conflict is safer than a different-endpoint false equality.

---

## 3. Go Structs & Resolution Helpers (`internal/config`)

`Config.ImageRemotes map[string]string` (`yaml:"image_remotes,omitempty"`). No custom
`UnmarshalYAML`; `MergeConfigs` merges it key-wise (§2.2). Two pure helpers (deterministic, no I/O):

```go
// SplitImageRef returns (remote, alias, true) for the remote:alias form and
// ("", image, false) otherwise (fingerprint or bare alias).
func SplitImageRef(image string) (remote, alias string, isRemote bool)

// ImageLocalRef returns the local LXD image identity for the resolved instance
// type. For remote:alias it is the canonical TYPE-QUALIFIED local alias
// (§4.2); for fingerprint and bare alias it is the reference itself.
func ImageLocalRef(image, instanceType string) string
```

```go
var builtinImageRemotes = map[string]string{
    "ubuntu":       "https://cloud-images.ubuntu.com/releases",
    "ubuntu-daily": "https://cloud-images.ubuntu.com/daily",
    "images":       "https://images.lxd.canonical.com",
}
```

`CanonicalizeImageRemoteURL(name, raw)` validates + canonicalizes a URL (§2.3/§7.2).
`EffectiveImageRemotes(configs)` compiles the fleet registry (built-ins overlaid by declarations,
with conflict detection).

---

## 4. Image Reference Resolution & Identity (locked)

### 4.1 Remote name → URL resolution order

For `image: <remote>:<alias>`, the effective URL is resolved in this order:

1. `image_remotes[<remote>]` (fleet-scoped declaration),
2. `builtinImageRemotes[<remote>]`,
3. otherwise — **plan-time config error, exit 3**:
   `unknown image remote "<remote>" (referenced by image "<remote>:<alias>" of instance "<name>");
   declare it under image_remotes:`.

Resolution is deterministic and offline; it happens at **plan time** and the resulting URL is
materialized onto the planned fetch op (§5.4).

### 4.2 Canonical local alias (the idempotency key, TYPE-QUALIFIED)

A fetched `remote:alias` image is tagged in the local store with a deterministic alias name that
**encodes the instance type**, so a container image and a virtual-machine image for the same
`remote:alias` never collide:

```
container         ubuntu:22.04        →  ubuntu/22.04
                  images:ubuntu/22.04  →  images/ubuntu/22.04
                  ubuntu-daily:24.04   →  ubuntu-daily/24.04
virtual-machine   ubuntu:22.04         →  ubuntu/22.04/vm
                  images:debian/12     →  images/debian/12/vm
```

LXD image aliases are unique per project+name (`UNIQUE (project_id, name)`), and creating an image
whose alias already exists is rejected with `Alias already exists`. Baking the type into the name
(`/vm` suffix) removes the ambiguity entirely and makes the cache probe a plain name lookup (§4.3).
The alias is opaque to lxm — it is never parsed back into a reference.

`<remote>/<alias>[/vm]` is always a valid LXD local-alias name. This name is what lxm looks for in
the cache probe, what the fetch op creates, and what the create/rebuild payload uses as its
`Source.Alias` (§5.1).

### 4.3 Cache probe (local only, no network)

The reference is **cached** iff the canonical **type-qualified** local alias `ImageLocalRef(image,
instanceType)` exists in the local image store (probed via `ImageService.GetImageAliases`, §8). The
canonical alias is authoritative, deterministic, and greppable via `lxc image alias list`. Because
the type is encoded in the alias name, the probe key is the alias name alone.

Consequence (locked): an image the operator hand-copied **without** the canonical alias is treated
as a miss and re-fetched. This is safe — the fetch is idempotent (LXD adopts/refreshes the image and
re-tags the alias).

### 4.4 Type, architecture, variant (delegated)

The fetch is performed by the **LXD daemon** via a simplestreams pull (§8), so type is selected by
`source.image_type` (from the manifest's resolved `type`), architecture is the daemon's own, and
variant is the stream's default. v1 does **not** expose `image_arch:`/`image_variant:`.

### 4.5 Live-instance matching (recorded reference)

The existing `imageMatches` heuristic reconciles a reference against a live instance's recorded
`image.os`/`image.release`, but fails for every remote whose name is not the OS name (`images:debian/12`
parses `osPart = "images"` vs live `image.os = "debian"` → perpetual recreate). Fix (locked):

1. `buildInstancesPost` sets `post.Config["user.lxm.image"] = manifest.Image` (create **and**
   recreate, since both use `buildInstancesPost`).
2. `imageMatches` first returns `true` when `liveConfig["user.lxm.image"]` equals the desired
   reference (trimmed, case-insensitive), before any OS/release heuristic:

```go
func imageMatches(desired, live string, liveConfig map[string]string) bool {
    if desired == live { return true }
    if recorded := strings.ToLower(strings.TrimSpace(liveConfig["user.lxm.image"])); recorded != "" &&
        recorded == strings.ToLower(strings.TrimSpace(desired)) {
        return true
    }
    // ... existing fingerprint + os:release heuristics remain as fallback for
    // legacy instances created before user.lxm.image existed ...
}
```

The recorded value is not diffed (`computeDiffs` never diffs `user.lxm.*` keys), is preserved across
`update`, and is **kept authoritative on every path that changes the image identity**:

* **Create** (`buildInstancesPost`) records `manifest.Image`.
* **Recreate fallback** (delete+create) records it via the same create payload.
* **Rebuild** (non-fallback) re-records it: LXD's rebuild preserves `user.*` config but resets
  `image.*` to the new image (`lxd/instance/drivers/driver_common.go` `rebuildCommon`), and
  `InstanceRebuildPost` carries only `Source`. Without a refresh the record would stay stale and
  re-plan a perpetual, destructive recreate for non-OS remotes — and could even mask real drift (a
  stale record equal to a reverted manifest reference). The executor therefore issues a targeted
  config PUT after a successful rebuild (`refreshImageRecord`): it copies the live config
  (preserving the fresh `image.*` keys — an instance PUT is a full-replace, so a bare create-payload
  config would wipe them) and updates only `user.lxm.image`.
* **Update** (`buildInstancePut`) re-records `manifest.Image`, which also backfills legacy instances
  created before the key existed.

---

## 5. Reconciliation (`internal/plan`)

### 5.1 Resolved source in create/rebuild payloads

`buildInstancesPost` and the recreate `RebuildPost` use `config.ImageLocalRef(manifest.Image,
manifest.Type)` and map to the `api.InstanceSource` fields **explicitly**:

| `image` form | `Source.Fingerprint` | `Source.Alias` |
| :-- | :-- | :-- |
| fingerprint (12–64 hex) | `manifest.Image` | `""` |
| bare alias (`#LocalAlias`) | `""` | `manifest.Image` |
| `remote:alias` (`#RemoteAlias`) | `""` | `ImageLocalRef(image, type)` = `<remote>/<alias>[/vm]` (§4.2) |

Apply to: `buildInstancesPost` (create payload), `RebuildPost` (rebuild source), and the recreate
`InstancesPost`. This fixes the latent bug where a fingerprint manifest reference produced
`Alias: "<hex>"` and failed with `Image alias not found`. Additionally, `buildInstancesPost` records
`post.Config["user.lxm.image"] = manifest.Image` (§4.5).

This rewrite is deterministic and offline, and is what makes a post-fetch create succeed.

### 5.2 Reconciler signature

`Compute(manifest, live, volumes, imageAliases map[string]bool, imageRemotes map[string]string,
hasRebuildExt bool)`. `imageAliases` is the live local-alias inventory (a set of alias **names**
from `ImageService.GetImageAliases`, §8) and `imageRemotes` the effective registry compiled by the
command layer (built-ins ∪ declared union). Both keep `Compute` a pure, offline function; either may
be empty/nil for tests and non-image flows.

### 5.3 Fetch decision (in `Compute`)

For a `status: present` manifest (create and recreate paths only — not update/absent):

1. If `image` is **not** `remote:alias` → no image op (behaviour unchanged).
2. Resolve `remote` → URL against `imageRemotes`. Unknown remote → exit-3 error (§4.1).
3. Probe `imageAliases[config.ImageLocalRef(image, manifest.Type)]`. Present → no image op.
4. Absent → append an `ImageOp{fetch}` to the step (§5.4).

The plan never contacts the remote; the fetch op carries everything apply needs (including the
already-resolved URL).

### 5.4 Plan model: `ImageOp` + `Step.ImageOps`

```go
type ImageOp struct {
    Op         string `json:"op"`            // "fetch"
    Remote     string `json:"remote"`        // remote name (diagnostics)
    RemoteURL  string `json:"remote_url"`    // resolved simplestreams URL
    Alias      string `json:"alias"`         // alias on the remote
    LocalAlias string `json:"local_alias"`   // canonical TYPE-QUALIFIED local alias (§4.2)
    Type       string `json:"type"`          // "container" | "virtual-machine"
}

type Step struct {
    // ...
    ImageOps []ImageOp `json:"image_ops,omitempty"`
}
```

The image diff for a remote reference shows in the plan as it does today; the fetch is an additional
structured `image_ops` entry on the step, so `--format json` consumers see the exact fetch.

### 5.5 Diff rules (create/recreate only)

| Change | FieldDiff | ImageOp |
| :-- | :-- | :-- |
| new instance, `image` remote & uncached | `image` (old `∅`) | `fetch` |
| new instance, `image` remote & cached | `image` (old `∅`) | — |
| new instance, `image` local | `image` (old `∅`) | — |
| `image` change to uncached remote (recreate) | `image` (old→new, `RequiresRecreate`) | `fetch` |
| `image` change to cached remote / local (recreate) | `image` (old→new, `RequiresRecreate`) | — |
| `image` unchanged, `update`/`noop` | — | — |

Recreate with `RebuildFallback = true` follows the same rule: the fetch op is attached to the step
and runs before the delete+create.

---

## 6. CUE Schema Deltas (`internal/config/schemas/v2.cue`)

```cue
#ImageRemoteName: =~"^[a-zA-Z0-9_\\.\\-]+$"
#ImageRemoteNameInvalid: !~"^[a-zA-Z0-9_\\.\\-]+$"
```

Declared in `#LXM_AUTHORING` (as `image_remotes?: {[#ImageRemoteName]: string}`) and enforced in
`#LXM_RESOLVED`:

```cue
image_remotes?: {
	[#ImageRemoteName]: string
	[#ImageRemoteNameInvalid]: _|_
}
```

The positive key-pattern `{[#ImageRemoteName]: string}` alone is **not enforced** when the map is
nested inside `close({...})` — a CUE quirk (the same one that affects `vars`/`#EnvKey`, which is left
unchanged by this feature). Pairing it with the `[#ImageRemoteNameInvalid]: _|_` rejection (any key
that does not fully match the charset becomes `_|_`) makes the rule concrete in the resolved form.
`#LXM_AUTHORING` keeps the positive pattern as a tooling declaration; the authoring path relies on Go
for the diagnostic (below).

**Diagnostics (compensating control).** CUE's `_|_` error does not name the offending key (verified
with `cue.All()`), so the actionable message comes from Go: `validateImageRemoteNames` checks every
`image_remotes` key against `imageRemoteNameRe` and reports
`image_remotes: invalid remote name "<name>" (allowed characters: [a-zA-Z0-9_.-])`. It runs on every
production validation path:

| Path | Enforcement | Message |
| :-- | :-- | :-- |
| `lxm apply` / `plan` / `diff` (v2) | CUE `#LXM_RESOLVED` (`_|_`) + Go `ValidatePostMerge` | precise (Go fires first) |
| no-schema (v1-compat) manifests | Go `ValidatePostMerge` (CUE is skipped) | precise (Go is the sole enforcement) |
| `lxm compile` | CUE `#LXM_RESOLVED` + Go `MigrateManifest` | precise (Go runs before the CUE fallback) |
| external consumers of the resolved schema | CUE `#LXM_RESOLVED` (`_|_`) | generic CUE (contract/backstop) |

This is the "coarse CUE gate + Go rich validation" convention (mirroring `vswitch` `ipv4`). The URL
remains a bare `string` in CUE; rich URL validation (scheme, host, loopback-http rule) also runs
Go-side in `ValidatePostMerge`. `#ImageRef` is unchanged — the remote form is already accepted.

---

## 7. Locked Behavioral Rules

### 7.1 Local references never fetch
`#Fingerprint` and `#LocalAlias` references are local-only, always. A miss fails at apply with LXD's
`image not found` → exit 4 (unchanged).

### 7.2 URL validation & canonicalization (`ValidatePostMerge`, exit 3)
Each `image_remotes` value must parse via `url.Parse` with scheme `https` (or `http` only when the
host is loopback — `localhost`/`127.0.0.0/8`/`::1`), and a non-empty host. It is then canonicalized
per §2.3 and stored/compared in canonical form. Violation:
`image_remotes["<name>"]: invalid image remote URL "<url>"`.

### 7.3 Unknown remote (plan time, exit 3)
`unknown image remote "<remote>" (referenced by image "<remote>:<alias>" of instance "<name>");
declare it under image_remotes:` (§4.1).

### 7.4 Remote-resolution / fetch failure (apply time, exit 4)
Any failure resolving the alias on the remote, pulling the image, or tagging the alias surfaces as
`LXD_ERROR` (exit 4), retryable for transient network/connection errors. In addition to the errors
`ClassifyLXDError` marks retryable (ETag/412 conflicts), a fetch that times out
(`waitOpContext` deadline) is explicitly flagged retryable.

### 7.5 Global opt-out (`LXM_IMAGE_FETCH`)
The environment variable `LXM_IMAGE_FETCH` (default `"1"`) disables remote fetch when set to
`"0"`/`"false"`. When disabled, a `remote:alias` reference that is not cached is a **plan-time
config error (exit 3)**:
`image fetch is disabled (LXM_IMAGE_FETCH=0) but image "<remote>:<alias>" of instance "<name>" is
not cached locally`.

### 7.6 Never delete fetched images
lxm never deletes images or aliases, in any path. Fetched images persist for reuse.

### 7.7 Idempotency & concurrency
Fetch is idempotent. LXD's `CreateImage` pull adopts an existing identical image rather than
duplicating it; the executor treats `Alias already exists: <name>` from `CopyRemoteImage` as a
**success/no-op** (a concurrent apply already fetched and tagged the image), so Phase −1 converges
under parallel `--jobs`. All other errors abort the phase (exit 4, §9).

### 7.8 `status: absent` and base manifests
`status: absent` manifests need no `image` (unchanged) and never produce image ops. Base manifests
may not declare `image` (unchanged) but **may** declare `image_remotes` (a shared registry is a
legitimate base concern).

---

## 8. LXD Service Extensions (`internal/lxd`)

`ImageService` on the same connection, mirroring `NetworkService`/`StorageService`:

```go
type ImageService interface {
    GetImages() ([]api.Image, error)
    GetImageAliases() ([]api.ImageAliasesEntry, error)
    CopyRemoteImage(ctx context.Context, remoteURL, alias, imageType, localAlias string) error
}
```

`CopyRemoteImage` builds a simplestreams pull and awaits it:

```go
op, err := s.client.CreateImage(api.ImagesPost{
    Source: &api.ImagesPostSource{
        ImageSource: api.ImageSource{
            Protocol:  "simplestreams",
            Server:    remoteURL,
            Alias:     alias,
            ImageType: imageType, // "container" | "virtual-machine"
        },
        Mode: "pull",
        Type: api.SourceTypeImage,
    },
    Aliases: []api.ImageAlias{{Name: localAlias}},
}, nil)
if err != nil { return err }
return waitOpContext(ctx, op)
```

The LXD daemon performs the simplestreams download, arch/variant selection, and alias creation — the
same mechanism as `lxc image copy`. No client-side `ConnectSimpleStreams` is required in v1.

The fake server gains an in-memory image store (name → `api.Image`) and alias set. `CopyRemoteImage`
records the request and seeds the canonical alias (so idempotency tests assert a second apply
no-ops); a duplicate alias returns LXD's real `Alias already exists: <name>` error, which the
executor tolerates (§7.7).

The command layer type-asserts `svc` to `lxd.ImageService` to build the local-alias inventory
(`fetchImageAliases`), exactly as it does `lxd.StorageService` today. `ImageService` is **also** a
member of `apply.Services` (§9). A failed inventory probe is **fatal at apply** (exit 4, like
`fetchLiveSnapshots`) so it cannot silently turn every cached remote image into a redundant
simplestreams pull; `plan`/`diff` stay lenient (offline-capable) and degrade to an empty inventory.

---

## 9. Execution (`internal/apply`)

1. **Phase −1: image fetch.** Before Phase 0 (volume ops) and every instance step, the executor runs
   all `step.ImageOps` (deduplicated by `RemoteURL + Alias + Type` so a shared base image is fetched
   once across a fleet). A fetch failure aborts the apply with exit 4 (phase-abort semantics,
   matching the storage and network phases); `Alias already exists` is treated as success (§7.7).
   **Dry-run skips Phase −1 entirely** (no network, no mutation).
2. **Executor wiring.** `apply.Services` gains `lxd.ImageService`, and `defaultExecutor` gains an
   `imageSvc lxd.ImageService` field populated in `NewExecutor(svc Services)` alongside the existing
   `lxdSvc`/`netSvc`/`storageSvc`. `executeImageOp` calls `imageSvc.CopyRemoteImage` with the op's
   fields and classifies the error via `ClassifyLXDError`.
3. **Create/recreate ordering is already satisfied** by the payload rewrite (§5.1): the instance
   step's `Source.Alias`/`Source.Fingerprint` is the canonical local reference, which Phase −1
   guaranteed exists.

### Phase ordering (final)

| Phase | Step type | Guards |
| :-- | :-- | :-- |
| −1 | image fetch (`ImageOps`) | dry-run skip; phase-abort on failure (exit 4) |
| 0 | volume ops (`VolumeOps`) | unchanged (STORAGE-SPEC §10) |
| 1 | network steps | unchanged |
| 2 | instance steps | unchanged |

---

## 10. Result Envelope & Exit Codes

* **Plan/diff output**: fetch is a structured `image_ops` entry on the step (plus the usual `image`
  FieldDiff). No envelope change.
* **Exit codes**:
  * unknown remote / invalid URL / conflicting `image_remotes` / fetch-disabled miss ⇒ **3**
    (`CONFIG_ERROR`).
  * remote alias missing, no `<type>` image, network failure, LXD pull error ⇒ **4** (`LXD_ERROR`);
    transient network errors are marked retryable.
  * local-alias/fingerprint miss at create ⇒ **4** (unchanged LXD error).
  * No new exit codes.
