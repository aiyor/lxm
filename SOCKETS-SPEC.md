# lxm Sockets Specification — Declarative Unix-Domain Socket Proxying (`sockets:`)

> Status: v1 (issue #17). Scope: Unix-domain socket proxying between the host and an instance for
> LXD and Incus, compiled to native `proxy` devices — including the reverse direction
> (`bind: instance`) and listening-socket DAC ownership (`uid`/`gid`/`mode`). This document is the
> feature-level contract; the manifest schema table is mirrored in [`SPEC_MANIFEST.md`](SPEC_MANIFEST.md)
> §3.12 and the machine interface is extended in [`SPEC_RESULT.md`](SPEC_RESULT.md).
>
> Implementation status: **IMPLEMENTED; live-verified on LXD 6.10.1 LTS** via
> `simulation/scripts/test-sockets-e2e.sh` (11/11 scenarios PASS; Incus not exercised — pending, see
> [`TESTING.md`](TESTING.md) §2.1).

## 1. Overview

`lxm` manages the **desired set** of proxied Unix-domain sockets per instance. Each `sockets:` entry
compiles to one native LXD/Incus `proxy` device named `socket-<name>`, carrying the shared ownership
marker `user.lxm.managed: "true"` plus the namespace marker `user.lxm.kind: "socket"`. The daemons'
`proxy` device is the only portable, feature-flagged-free mechanism that can create a Unix socket on
one side of the container boundary and relay it to the other side; `bind: instance` selects the
reverse direction needed for **per-instance host IPC** (for example a host-owned credential broker
whose socket is exposed inside each container).

Sockets are reconciled exactly like mounts, data disks and published ports: presence is desired
state, omission from the manifest retires the device on the next apply, and edits are computed as
field-level diffs. The feature is **additive** to the manifest schema (no version bump) and to
`lxm/result/v1`.

The block is deliberately separate from `ports:` (issue #11): `ports:` is the explicit
`IP:port` TCP/UDP publishing contract, while `sockets:` is a Unix-domain socket relay contract with
a different direction model and socket-level DAC options. Both compile to `proxy` devices, so they
share one ownership scheme (§2) but never claim each other's devices.

## 2. Device encoding and ownership namespace

| Manifest | Provider device |
| :-- | :-- |
| `name: broker` | device name `socket-broker` (`socket-` namespace) |
| `listen: unix:/run/sshigit/broker.sock` | `"listen": "unix:/run/sshigit/broker.sock"` |
| `connect: unix:/run/sshigit-broker/lab1.sock` | `"connect": "unix:/run/sshigit-broker/lab1.sock"` |
| `bind: instance` | `"bind": "instance"` |
| `bind: host` (default) | `"bind": "host"` — **always emitted explicitly** |
| `uid: 1000` | `"uid": "1000"` (omitted when not declared) |
| `gid: 1000` | `"gid": "1000"` (omitted when not declared) |
| `mode: "0660"` | `"mode": "0660"` (omitted when not declared) |
| — | `"type": "proxy"`, `"user.lxm.managed": "true"`, `"user.lxm.kind": "socket"` |

`bind` is materialized to `host` by the loader and **always** written to the device, unlike
`ports:`' `nat` which is omitted when false. The explicit `bind: host` is what makes LXD remove the
host-side listening socket when the device is retired (see §7); an omitted `bind` would still mean
host on the daemon, but the cleanup contract would not be triggered.

Ownership is **marker-based, not name-based**, and the two `proxy` features share one namespace
scheme:

* `user.lxm.managed: "true"` marks a device as lxm-owned. The plan's device-preservation loop
  skips *every* marker-carrying `proxy` device and rebuilds proxies from the manifest, in the order
  ports then sockets.
* `user.lxm.kind` distinguishes the namespaces. It is absent on all port devices created before the
  sockets feature existed, so "managed `proxy` **without** a `kind`" is owned by the **ports**
  namespace (backward compatible); `user.lxm.kind: "socket"` is owned by the **sockets** namespace.
  Malformed or renamed socket-namespace devices stay claimed by sockets and are repaired or removed,
  never silently dropped by the ports diff.
* An **unmarked** device is foreign: preserved verbatim and never adopted. A declared socket whose
  `socket-<name>` device name is occupied by a foreign device is a **plan-time error** (exit 3).
* Devices with a different `type` are never touched.

For reconciliation identity lxm uses the canonical **device name** (`socket-<name>`): a
marker-carrying socket device under any other name can never satisfy a manifest socket and is
retired on the next apply. A canonical-shaped but undeclared device (`socket-foo` with no `foo`
socket) is removed with a `sockets[foo]` diff; an anomalous marker-carrying socket device whose name
is not `socket-<name>` is removed with a `devices[<device>]` diff so it cannot collide with a real
socket's `sockets[<name>]` field. Malformed marked `socket-<name>` devices are repaired in place
(`sockets[<name>]`, old raw device map → new `SocketConfig`), never surfaced as a plan error.
Additional daemon properties on a managed device that lxm does not emit (extra `user.*`, a legacy
`bind` alias, `security.*`) are outside the declarative contract: the plan reports a repair diff
with a warning, and the rewrite drops them.

## 3. Endpoint grammar & canonicalization

Endpoints in the **manifest** are `unix:` prefixed; the daemon-level device encoding uses exactly
the same string. `internal/sockets.ParseEndpoint` is authoritative:

* accepted: `unix:/run/app.sock`, `unix:/var/lib/service/ipc`;
* rejected: a missing `unix:` scheme, a non-`unix` scheme (`tcp:`/`udp:` are rejected with a pointer
  to `ports:`; anything else with "unsupported endpoint scheme"), empty paths, relative paths,
  non-clean paths (`.`/`..` segments, `//`, trailing `/`), control characters including NUL,
  abstract sockets (`unix:@name`), and paths longer than the Linux `sockaddr_un.sun_path` limit
  (**107 bytes**, i.e. 108 including the terminating NUL);
* canonical form is `unix:` + the clean absolute path, compared byte-for-byte (no percent decoding,
  no symlink resolution). Non-clean spellings are rejected rather than cleaned, so endpoint equality
  is exactly byte equality of the canonical form.

In a `lxm/config/v2` manifest the closed authoring schema is the first gate: a value that does not
start with `unix:` (including `tcp:`/`udp:`) fails there with a CUE bounds error. The targeted
messages above are what the loader and the plan/apply validators report when a value reaches them —
legacy v1 manifests (which skip the authoring gate) and programmatic configs — so an author who
fixes the CUE error sees the precise endpoint diagnostic next if the path itself is still wrong.

The listening path and the connect path are both absolute and opaque; lxm never creates, stats or
opens either of them on either side.

## 4. Manifest schema

```yaml
schema: lxm/config/v2
name: lab1
image: ubuntu:24.04
sockets:
  - name: broker                                  # device socket-broker
    listen: unix:/run/sshigit/broker.sock         # instance side with bind: instance
    connect: unix:/run/sshigit-broker/lab1.sock   # host side with bind: instance
    bind: instance                                # default host
    uid: 1000                                     # optional listening-socket owner
    gid: 1000                                     # optional listening-socket group
    mode: "0660"                                  # optional quoted octal mode
  - name: agent
    listen: unix:/run/sshigit/agent.sock
    connect: unix:/run/sshigit-agent/lab1.sock
    bind: instance
    mode: "0600"
```

| Field | Type | Default | Rules |
| :-- | :-- | :-- | :-- |
| `name` | string | — (required) | `^[a-z]([a-z0-9-]{0,29}[a-z0-9])?$` — starts *and ends* alphanumeric (Incus device-name rule), ≤31 chars, unique per instance and layer. Device key `socket-<name>`. |
| `listen` | string | — (required) | `unix:<absolute clean path>`. Instance side when `bind: instance` (the daemon creates it); host side when `bind: host`. |
| `connect` | string | — (required) | `unix:<absolute clean path>`. Host side when `bind: instance` (the service must already be listening); instance side when `bind: host`. |
| `bind` | string | `"host"` | `host` \| `instance`. `host`: listen on the host, forward into the instance. `instance`: listen inside the instance, forward to the host. |
| `uid` | int | daemon default `0` | Optional listening-socket owner uid, `0`–`2147483647`. Emitted only when declared. |
| `gid` | int | daemon default `0` | Optional listening-socket group gid, `0`–`2147483647`. Emitted only when declared. |
| `mode` | string | daemon default `"0644"` | Optional listening-socket permission bits as a quoted octal string (`"0660"`; unquoted YAML integers are accepted and canonicalized). Accepted range `"0000"`–`"0777"`, canonical form `0[0-7]{3}`. |

`remove.sockets: [name]` drops inherited entries (error if unmatched). `replace.sockets` wholistically
replaces the inherited list; as with every list directive in this schema, an empty
`replace.sockets: []` is a **no-op** (use `remove.sockets` to clear inherited entries). When a
manifest declares both `sockets:` and a non-empty `replace.sockets`, the replace list wins and the
local `sockets:` entries are ignored (the same precedence the other list directives use). Merge is
**name-identity, whole-entry replacement**: an overlay entry with the same `name` replaces the base
entry entirely (unspecified fields reset to their defaults — in particular, omitting `uid`/`gid`/
`mode` in an override resets them to the daemon defaults, and omitting `bind` resets it to `host`).
Duplicate names within one source layer are rejected before merging, including in a layer that has
an inherited base.

The object is **closed**: unknown fields (`nat`, `proto`, `proxy_protocol`, `security.uid`,
`security.gid`, `status`, …) are rejected at authoring time for `schema: lxm/config/v2` manifests by
CUE, and by an equivalent Go-side strict check for legacy schema-less/`lxm/config/v1` manifests
(which bypass CUE). There is no per-socket `status: absent`; removal from the manifest (or
`remove.sockets`) is the delete mechanism.

## 5. Validation layers

1. **CUE authoring** (`#LXM_AUTHORING.sockets`): field shape, closed object, `name` regex, `bind`
   enum, coarse `unix:` endpoint gate, non-negative `uid`/`gid`, `mode` string-or-integer.
2. **Load normalization** (`LoadConfig`): `bind` materialization and canonicalization, endpoint
   parse + canonical form, mode canonicalization, `uid`/`gid` range. Grammar errors are reported
   here even for legacy v1 manifests and base fragments.
3. **Post-merge Go** (`ValidatePostMerge`): `name` required and regex-valid, unique within the
   instance; per-layer duplicate detection; `remove.sockets` targets resolve.
4. **Per-instance cross-field Go** (`validateCommon`/`validateSockets`, root-only, after type
   normalization; skipped for `base: true` and `status: absent`, whose grammar/name rules still run
   in load/`ValidatePostMerge`):
   * `type: virtual-machine` is rejected — Unix-socket proxies are container-only (§7);
   * a NIC device name equal to `socket-<name>` is a hard error (the socket would silently overwrite
     the NIC in the instance PUT device map);
   * same-instance duplicate **host-side** paths (see §6) are rejected;
   * same-instance duplicate **instance-side listen** paths with `bind: instance` are rejected (two
     devices cannot create the same instance-side socket).
5. **Fleet-scoped** (`config.ValidateFleetSockets`): the host-side path of every managed socket must
   be unique across the full loaded manifest set (before selector filtering), exit 3 citing both
   files and instances. Manifests are ordered by file path so the diagnostic is deterministic.
   Same-instance duplicates are skipped here so the single-manifest error stays the precise one.
6. **Capability gate** (CLI-time, `checkSocketExtensions`): `proxy_unix` is required when sockets
   are declared; `proxy_unix_dac_properties` is additionally required when any `uid`/`gid`/`mode`
   is declared. A missing extension is exit 4 (§11). Base fragments and `status: absent` manifests
   are skipped by the gate.
7. **Apply-time daemon rules**: the only statically uncheckable rule is the existence (and
   writability) of the parent directory of the listening path, which surfaces as a daemon error at
   apply (exit 4, §7). Every other daemon rule that lxm can check is enforced at config/plan time
   (exit 3) rather than degrading into a raw apply error.

`lxm compile` runs the same endpoint/bind/mode/uid/gid normalization and validation on migrated
output (`internal/config.normalizeCompiledSockets`), for both the top-level `sockets:` list and a
`replace.sockets` list, so a compiled manifest never contains values that plan/apply would then
reject (for example `mode: "0o660"`, `uid: 3000000000`, `unix://run/x.sock`, or an unquoted integer
mode), and it emits canonical `unix:`/`bind`/`mode`/`uid`/`gid` spellings. Inline YAML comments on
rewritten socket fields are preserved.

An offline plan (no reachable provider) cannot detect an unmarked device collision; apply always
can, because it fetches live state before building the plan.

## 6. Path uniqueness & handoffs

Two different paths matter, and they have different scopes:

* **Host-side path.** The filesystem path the *host* side of the relay uses: `connect` for
  `bind: instance`, `listen` for `bind: host`. Host-side paths are host-global resources, so they
  must be **unique across the loaded fleet** for one daemon endpoint: two devices forwarding to the
  same host socket are a mistake, and two `bind: host` listeners on one path are a hard bind
  conflict. Fleet validation rejects duplicates with exit 3 citing both manifests (`host-side unix
  socket path %q is declared by socket %q of instance %q (...) and ...`). On a cluster, lxm does not
  know which member an instance will be placed on, so duplicates are rejected conservatively even if
  the instances might land on different members.
* **Instance-side listen path.** The listening path created inside the instance for
  `bind: instance` (`listen`). It is per-instance: two devices on one instance cannot create the
  same socket, but the **same path on two different instances is fine** — verified live: two
  containers both exposing `unix:/var/tmp/lxm-e2e-broker.sock` reached distinct host targets through
  distinct per-instance devices. The path is part of each instance's own filesystem namespace.

Path comparison is on the canonical endpoint string (byte-for-byte), so it is exact and
deterministic; there are no wildcards, so wildcard-overlap semantics do not apply.

**Handoffs.** Fleet validation prevents two instances from *declaring* one host-side path, but not a
handoff: Phase-2 instance steps run concurrently with a bounded semaphore, so a host-side listener
released by instance A and claimed by B in the same apply can race. v1 emits a plan warning when it
detects a `bind: host` host-side listen path moving between stepped instances and documents `-j 1`;
apply-phase ordering is future work. (`bind: instance` host paths are connector targets, not
listeners, so they do not produce handoff warnings.)

## 7. Direction, socket ownership and daemon-verified semantics

Both directions are native daemon `proxy` devices. The case that motivated the feature is
`bind: instance`; `bind: host` is the mirror image and completes the daemon's model.

| | `bind: host` (daemon default) | `bind: instance` |
| :-- | :-- | :-- |
| Listening side | **host**, at `listen` | **instance**, at `listen` |
| Owning side of `uid`/`gid`/`mode` | host-side socket | instance-side socket |
| Relay target | `connect` (instance side) | `connect` (host side) |
| Typical use | host-local client → service inside the instance | process inside the instance → host-owned service |
| Instance must be a container | yes | yes |

Daemon-verified behaviour (LXD 6.10.1; the same mechanics exist in Incus):

* **The daemon creates the listening socket; lxm does not.** The service on the *other* side must
  already exist — for `bind: instance` the host-side `connect` service must be listening before a
  connection can succeed. lxm never creates, binds or connects to a socket itself; it only writes
  the device.
* **Parent directories must pre-exist.** forkproxy creates the socket, not its parent directory. The
  instance-side parent (`/run/sshigit` above) must be created by the image, cloud-init or a recipe.
  A missing parent fails at apply with a daemon error (`no such file or directory` /
  `Failed listening`) and exit 4 — it cannot be fully validated offline. Place the listening path
  in a directory that is stable early in boot: a socket created under a mount the guest remounts
  later (for example systemd's `/tmp` tmpfs on a freshly started instance) can be shadowed and
  disappear from client view. `/run/<dir>` (created by cloud-init/recipe) and `/var/tmp` are the
  safe choices; the live E2E gate uses `/var/tmp`.
* **Default listening-socket mode is `0644`.** With `mode` omitted the daemon creates the socket
  owned by root (`uid`/`gid` `0`) with mode `0644` (`rw-r--r--`): world-readable, but connecting to
  a Unix stream socket requires **write** permission on the socket, so the default is effectively
  root-only. Declare `uid`/`gid`/`mode` explicitly whenever an unprivileged in-instance user (for
  `bind: instance`) or host user (for `bind: host`) must connect. Only permission bits are accepted
  (`0000`–`0777`); the daemons pass the value through `os.Chmod`, where setuid/setgid/sticky raw
  bits are silently dropped, so lxm rejects them.
* **`uid`/`gid` are the listening-socket owner options — not `security.uid`/`security.gid`.**
  The latter control the privilege the forkproxy process drops to and are out of scope. Using
  `security.uid`/`security.gid` instead produces a socket owned by `root` that the intended
  instance user cannot use (observed in the issue #17 spike); `sockets:` only exposes the
  socket-owner options.
* **An existing object at the listening path is unlinked by forkproxy before it binds.** A stale
  socket, a leftover file, or a socket from a previous daemon generation is replaced on the next
  device start, so restarting/recreating the device recovers the path. This also means **never point
  `listen` at an existing important file** (host side for `bind: host`, instance side for
  `bind: instance`): the daemon deletes whatever occupies that path before creating the socket.
* **Instance-side socket files are not removed when the device is removed.** Deleting the device
  retires the proxy but leaves the socket file inside the instance; the stale file is harmless (no
  process is listening on the relay anymore) and is unlinked/replaced the next time a device starts
  on that path. Clean it up in a recipe/cloud-init only if the image should not keep it.
* **Host-side cleanup is LXD-only, and `bind: host`-only.** On LXD, retiring a `bind: host` device
  removes the host-side listening socket it created; Incus never removes it, and LXD's removal is
  best-effort. A stale host-side socket file is harmless (forkproxy unlinks it before binding next
  time) but it can mask a missing service until then — when repointing a path, make sure no live
  process still listens on it.
* **The relay is per-connection.** Each accepted connection is relayed to a fresh connection on the
  `connect` endpoint; the daemon does not hold a long-lived upstream connection. Consequences,
  verified live: a broker that is absent is a **clean client failure** (the client's connection is
  reset/closed without a reply — the proxy itself keeps running and the device stays healthy), and a
  broker that restarts or re-creates its socket is **reconnected on the next client connection**
  with no apply and no device recreation.
* **Container-only.** Both daemons only support `proxy` devices on virtual machines with
  `nat: true`, and NAT cannot proxy Unix endpoints, so a Unix-socket proxy can never attach to a
  VM. `type: virtual-machine` with `sockets:` is a configuration error (exit 3).
* **No restart.** `proxy` devices are hotpluggable; socket-only changes are ordinary online updates.

## 8. Plan, apply and ownership

* `buildSocketDevices` renders one device map per socket (`sockets.BuildDevice`) and appends the
  `socket-*` devices to the create and fallback-recreate payloads, after the port devices.
* `buildInstancePut` skips **all** marker-carrying proxy devices in the preservation loop (ports and
  sockets), then rebuilds ports and finally sockets from the manifest. Omission from the PUT deletes
  retired devices because the daemon replaces the local device set.
* `Compute` diffs only marker-carrying socket devices (`user.lxm.kind: "socket"`), keyed by the
  canonical device name `socket-<name>`. Scalar fields diff as `sockets[<name>].listen`,
  `.connect`, `.bind`, `.uid`, `.gid`, `.mode`; add/remove diff as `sockets[<name>]`. A
  malformed marker-carrying device is still owned: it produces a synthetic repair (present in the
  manifest) or removal diff (absent) — never a plan error and never a silent noop.
* A **non-canonical representation** of a live socket device (extra properties, a legacy
  `guest`/`container` bind alias, an omitted `bind`) is repaired in place with a device-level
  `sockets[<name>]` diff plus a warning, so a later rewrite never drops properties silently. The
  diff also compares the full canonical device map, so scalar field diffs and device drift cannot
  disagree.
* An unmarked device under a desired `socket-<name>` name is a **plan-time error** (exit 3). lxm
  never adopts and never emits a marker-only update: Incus rejects marker-only device changes and a
  silent remove+add would briefly tear down a relay.
* Deterministic ordering: desired sockets by name, removals sorted by device name.
* Warnings (top-level `warnings`, same surface as ports): `bind: host` creates a host-side listener
  that host-local processes can use directly and that bypasses `network_policy`; non-canonical
  managed devices are rewritten on the next update; a manifest socket that shadows a
  profile-provided device is not managed by lxm; host-side listener handoffs (§6).
* No restart is scheduled for socket-only changes. Native daemon rebuild (`instances_rebuild`) does
  not PUT devices; a socket change planned in the same step lands on the next apply — the same
  documented daemon behaviour as `ports:` (see [`PORTS-SPEC.md`](PORTS-SPEC.md) §8).
* Profile-provided socket devices are not managed by lxm; the local device of the same name wins and
  the shadowing produces a plan warning. Foreign devices are preserved untouched.

## 9. Output

* `lxm plan`/`lxm apply`: text warnings (see §8); JSON plan diffs `sockets[<name>]` and
  `sockets[<name>].listen|connect|bind|uid|gid|mode`; create/update payloads contain the
  `socket-*` devices. An anomalous marker-carrying device under a non-`socket-<name>` name is
  removed with a `devices[<device>]` diff (outside the socket-field namespace).
* `lxm diff`: the same field diffs; add/remove values render via `SocketConfig.String()` —
  `unix:/run/a.sock -> unix:/run/b.sock (bind=instance, uid=1000, gid=1000, mode=0660)`.
* `lxm list`: text gains a `SOCKETS` column (managed sockets only). Each entry is
  `<listen>-><connect>`, with ` (instance)` appended when `bind: instance`; multiple entries are
  comma-separated and the column is `-` when empty. JSON `results[].published_sockets[]` is
  additive and covers the effective device set (local + profile-provided, the latter as
  `managed: false`).
* `lxm status`: text block `Unix Sockets:` (effective local + profile devices, ownership, bind and
  DAC annotated), or `(none)` when there are none; JSON `results[].published_sockets[]`.
* `published_sockets` entries: `{device, name?, listen, connect, bind, uid?, gid?, mode?,
  managed}`, sorted by device name. `name` is derived from a `socket-<name>` device name and omitted
  otherwise; `listen`/`connect` carry the canonical `unix:` form; `uid`/`gid` are numbers and
  `mode` is the canonical quoted octal string when declared. `uid`/`gid`/`mode` are **omitted when
  undeclared** (declarative output); consumers that need the effective posture apply the daemon
  defaults (`uid`/`gid` `0`, `mode` `0644`). Non-proxy, TCP/UDP proxy and malformed devices are
  skipped. No `lxm/result/v1` bump; the field is omitted when empty.

## 10. Security posture

A socket proxy is **host access**, deliberately. `bind: instance` gives the container a path to a
host-owned service that would otherwise be unreachable from inside; `bind: host` gives host-local
processes a path to a service inside the container. In both directions:

* **Proxy traffic does not traverse `network_policy` ACLs.** Like `ports:`, the relay terminates at
  the daemon on the host or in the instance and bypasses the fleet's network segmentation. The
  socket's file permissions (`uid`/`gid`/`mode`) are the only access control; there is no
  network-layer gate. `bind: host` therefore emits a plan warning and must be deliberate.
* **The default mode is `0644` with owner `root:root`.** Connecting to a Unix stream socket
  requires write permission on the socket, so an undeclared `uid`/`gid`/`mode` is effectively
  root-only; any user who can write to the socket can use the relay. Declare the tightest ownership
  the client needs; prefer `"0600"` or `"0660"` with the client's `uid`/`gid`.
* **Any process that can open the listening socket can use it — including root.** Root inside the
  container (and root on the host for `bind: host`) bypasses file permission checks, so `mode` is a
  boundary between *users*, not a containment boundary against a compromised container. The socket
  path is a **per-instance identity only because the device mapping is per-instance**: do not
  bind-mount a shared socket directory or point several instances at one host socket unless that is
  the intended shared-service design.
* **The host service must authenticate each connection.** The daemon relays bytes; it does not
  enforce peer identity, and `SO_PEERCRED` attribution is out of scope (the relay makes it
  unreliable). Authenticate inside the protocol (tokens, capability handshakes) and enforce
  authorization per connection.
* **Protect the parent directories.** The instance-side parent directory must not be writable by
  users who should not be able to replace the socket path; the daemon unlinks whatever is at the
  listening path before binding, so a writable parent lets another process race the path. Use
  root-owned `0755` directories such as `/run`.
* **`uid`/`gid`/`mode` are not a privilege boundary for the daemon.** They set the listening
  socket's owner; `security.uid`/`security.gid` (the forkproxy privilege-drop options) are
  deliberately out of scope and never emitted.
* **Stale paths.** A stale instance-side or host-side socket (after device removal) is harmless as
  long as nothing listens on it; re-creating a device on that path unlinks and replaces it. Treat
  "socket exists" as "a relay used to be here", not "a service is listening". Because the daemon
  unlinks the path before binding, never choose a listening path that currently holds a valuable
  regular file: it will be deleted on the next device start.

## 11. Version compatibility & capability gating

* Both daemons support `unix` ↔ `unix` proxy devices with `bind`, `uid`, `gid` and `mode` across the
  supported daemon matrix (LXD 6.x+, Incus 6.x+); the feature was spiked on LXD 5.21.9, where the
  same device mechanics already exist, and both server extensions below predate the supported
  matrix.
* The CLI probes the daemon's extension set before planning/applying/diffing sockets:
  * `proxy_unix` — required for any declared socket (`unix` ↔ `unix` proxy devices);
  * `proxy_unix_dac_properties` — additionally required when any `uid`/`gid`/`mode` is declared
    (the listening-socket DAC options).
  A missing extension fails with exit 4 and a clear message instead of silently degrading on an
  unusual server. The gate is CLI-time, after provider resolution; offline plans skip it.
* `bind` accepts the canonical `host`/`instance` in manifests. When reading existing device state,
  the daemons' legacy `guest`/`container` aliases are recognized and canonicalized to `instance`
  (they produce a non-canonical-device repair warning; see §8). Manifest authoring is restricted to
  `host`/`instance` by the CUE schema.
* `user.*` device keys: Incus ≥ 6.15 (and 6.0.5+/7.x) permits `user.*` device keys and rejects
  marker-only device updates, exactly as described in [`PORTS-SPEC.md`](PORTS-SPEC.md) §11. lxm
  therefore always performs a full device put, never a marker-only patch.
* Device names must start and end alphanumeric on Incus; the `name` regex applies to both daemons.
* Container-only: VMs require `nat: true` on every proxy device and NAT cannot proxy Unix
  endpoints (see §7).
* `sun_path` is a Linux ABI limit (`108` bytes incl. NUL). Manifest paths are validated at load
  time, so the daemon never rejects an overlong *manifest* path at apply. Snap-packaged LXD
  additionally rewrites host-side non-abstract paths through `/var/lib/snapd/hostfs`; if that
  effective path crosses the limit, the daemon bind fails and apply exits 4 (see
  [Troubleshooting](docs/troubleshooting.md) — shorten the host-side path).
* The ownership marker pair is shared with `ports:`: both features write full device maps, rely on
  the same `user.*` daemon semantics, and are reconciled by the same PUT/ETag discipline.

## 12. Verification & test plan

Unit coverage: endpoint parsing/canonicalization (relative, non-clean, NUL/control, abstract,
overlong, wrong scheme, `tcp:`/`udp:` pointers); device encode/decode round-trips; ownership
namespace partition (ports vs sockets, malformed and renamed marker devices, foreign preservation);
mode/uid/gid validation and canonicalization; config load/merge/remove/replace and per-layer duplicate
detection; per-instance and fleet-wide path uniqueness; VM rejection; NIC collision; plan diff
field names, drift repair, repair/removal of malformed devices, ownership-conflict errors, warning
and handoff detection; `published_sockets` projection, ordering and text renderers; the CLI extension
gate and the fake-driver lifecycle. Suites live in `internal/sockets`, `internal/proxydevice`,
`internal/config`, `internal/plan`, `internal/fleet`, `internal/output` and `cmd/lxm`; `task lint`
and the full race-enabled suite must stay clean.

Acceptance deviation, recorded explicitly: issue #17's checklist says removal should clean "the
device **and the instance-side socket**". The device is removed, but neither daemon removes the
`bind: instance` socket file when the device is retired (§7); the stale file is harmless and is
replaced on the next device start. lxm cannot remove it without an out-of-model guest `exec`, so the
documented daemon behaviour is the accepted contract, and the live gate pins it (scenario 11 asserts
the stale file remains, that it no longer relays, and that re-adding a socket on that path works).

Live gate — `simulation/scripts/test-sockets-e2e.sh` on **LXD 6.10.1 LTS** (client 6.10.1,
server 6.10.1; unprivileged user via `./bin/lxm --provider lxd`; run 2026-11-08, **exit 0, 11/11
scenarios PASS**):

| Gate | Result |
| :-- | :-- |
| 1. `bind: instance` create; instance-side listener → host-owned socket with `uid`/`gid`/`mode` applied | PASS |
| 2. Access control: socket owned by uid 1000 with mode `0600` admits the owner and denies another uid | PASS |
| 3. Relay failure and recovery: broker absent is a clean client failure; broker restart reconnects without re-apply | PASS |
| 4. Idempotency: re-plan after apply is a noop (zero drift) | PASS |
| 5. Output surfaces: `lxm list` `SOCKETS` column, `lxm status` `Unix Sockets:` block, additive `published_sockets` JSON | PASS |
| 6. Per-instance isolation: identical instance-side listen paths on two instances reach distinct host sockets | PASS |
| 7. `bind: host`: host-side listener with host DAC relays to the instance-side service | PASS |
| 8. Missing instance-side parent directory fails cleanly at apply (exit 4), not as a plan-time error | PASS |
| 9. Validation matrix: duplicate host-side path, duplicate instance-side listen path and VM sockets are exit 3 | PASS |
| 10. Unmarked `socket-<name>` device collision is exit 3 and is never adopted | PASS |
| 11. Removal retires the device; the documented stale instance-side socket remains but no longer relays and is re-used on re-add; re-plan is a noop; host-side cleanup is daemon-specific | PASS |

Build/quality gates for the feature: `mise x -- go build ./...`, `go vet ./...`, `gofmt -l .`
(empty) and `mise x -- go test ./...` (all packages) pass.

Incus is **not exercised**. Incus was not reachable in the verification environment (the local
Incus daemon socket requires membership of the `incus-admin` group); Incus coverage is pending the
multi-provider pre-release gate. The gate script is provider-parameterized (`PROVIDER_CLI=incus`)
and is wired into the Incus pre-release step.

## 13. Out of scope (v1)

TCP/UDP endpoints in `sockets:` (use [`PORTS-SPEC.md`](PORTS-SPEC.md)); cross-protocol proxying
(`tcp` ↔ `unix`, `unix` ↔ `tcp`); `nat`, `proto` and `proxy_protocol` (meaningless or rejected for
Unix endpoints); abstract `@` sockets; `security.uid`/`security.gid` and forkproxy privilege-drop
options; host-side socket creation (the host service owns that); systemd socket activation;
`SO_PEERCRED`-based attribution; Windows named pipes; multi-hop/chained proxies; per-socket
`status: absent` (omission/`remove.sockets` covers lifecycle); automatic creation of parent
directories; symlink/ownership validation of parent directories; ACL interaction (proxy traffic
never traverses `network_policy`); Unix sockets on virtual machines; apply-phase handoff ordering.

## 14. Error catalogue

| Condition | Exit | Message (prefix) |
| :-- | :-- | :-- |
| unknown field in the block (`nat`, `proto`, `proxy_protocol`, `security.*`, `status`) | 3 | CUE authoring (`close`) for v2 manifests; `sockets[%d]: unknown field %q (supported fields: name, listen, connect, bind, uid, gid, mode)` for legacy schema-less/v1 manifests |
| missing/non-`unix:` scheme, empty/relative/non-clean path, control char, abstract socket, overlong `sun_path` | 3 | `socket %q of instance %q: invalid listen %q: …` / `… invalid connect %q: …` |
| `tcp:`/`udp:` endpoint in the block | 3 | `protocol %q is not supported for sockets; use ports: instead` |
| unknown endpoint scheme | 3 | `unsupported endpoint scheme %q (must be unix:)` |
| overlong path | 3 | `socket path %q is %d bytes; the Linux sun_path limit is 107 bytes` |
| duplicate socket name in one layer / instance | 3 | `duplicate socket name %q (socket %d)` |
| missing or invalid name | 3 | `socket %d: name is required` / `socket %q: name must match …` |
| invalid `bind` | 3 | `invalid bind %q (must be host or instance)` |
| `uid`/`gid` out of range | 3 | `uid %d is out of range (0-2147483647)` / `gid …` |
| invalid `mode` | 3 | `mode %q must be a quoted octal file mode between "0000" and "0777" (for example "0660")` |
| NIC name collides with `socket-<name>` | 3 | `NIC %q would collide with device %q` |
| same-instance duplicate host-side path | 3 | `socket %q of instance %q: host-side unix socket path %q is already used by socket %q` |
| same-instance duplicate instance-side listen | 3 | `socket %q of instance %q: instance-side listen socket %q is already created by socket %q` |
| fleet-wide duplicate host-side path | 3 | `host-side unix socket path %q is declared by socket %q of instance %q (%s) and socket %q of instance %q (%s)` |
| `remove.sockets` matches nothing | 3 | `remove.sockets: name %q matched no socket` |
| `sockets:` on a virtual machine | 3 | `Unix-socket proxies are container-only (virtual machines require nat: true, which cannot proxy unix endpoints)` |
| unmarked/foreign device collision | 3 | `device %q for instance %q exists but is not owned by the sockets namespace; remove it or rename the socket` |
| daemon lacks `proxy_unix` capability | 4 | `provider server lacks the proxy_unix extension; Unix-domain socket proxies cannot be created` |
| daemon lacks `proxy_unix_dac_properties` capability | 4 | `provider server lacks the proxy_unix_dac_properties extension; uid/gid/mode socket ownership options require a newer provider` |
| missing/unwritable listening parent directory at apply | 4 | daemon error (`no such file or directory` / `Failed listening …`) |
| non-canonical managed device representation | warning | `socket %q of instance %q: proxy device %q has non-canonical device properties; the next update rewrites them to the managed form` |
| `bind: host` host-local exposure | warning | `socket %q of instance %q uses bind: host and creates a host-side unix socket at %s; …` |
| profile-shadowed socket device | warning | `socket %q of instance %q shadows a profile-provided device %q; profile devices are not managed by lxm` |
| host-side listener handoff within one plan | warning | `unix socket path %q moves from instance %q to instance %q in this plan; … (or use -j 1)` |

## 15. Examples

Per-instance host broker (`bind: instance`) — the issue #17 use case; the container process owns
no credential and the host service sees only its own per-instance socket:

```yaml
schema: lxm/config/v2
name: lab1
image: ubuntu:24.04
sockets:
  - name: broker
    listen: unix:/run/sshigit/broker.sock
    connect: unix:/run/sshigit-broker/lab1.sock
    bind: instance
    uid: 1000
    gid: 1000
    mode: "0660"
```

The compiled device (as visible in the plan payload) is:

```yaml
socket-broker:
  type: proxy
  listen: unix:/run/sshigit/broker.sock
  connect: unix:/run/sshigit-broker/lab1.sock
  bind: instance
  uid: "1000"
  gid: "1000"
  mode: "0660"
  user.lxm.managed: "true"
  user.lxm.kind: "socket"
```

Host-local client into an instance service (`bind: host`):

```yaml
schema: lxm/config/v2
name: agent1
image: ubuntu:24.04
sockets:
  - name: agent
    listen: unix:/run/lxm/agent.sock     # host side (bind: host makes the host listen)
    connect: unix:/run/agent/agent.sock  # instance side
    bind: host
    mode: "0600"
```

The complete, compile-gated example is
[`docs/examples/sockets-demo.yaml`](docs/examples/sockets-demo.yaml); the task-focused guide is
[`docs/howto/proxy-unix-sockets.md`](docs/howto/proxy-unix-sockets.md).
