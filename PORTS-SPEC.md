# lxm Ports Specification — Declarative Port Publishing (`ports:`)

> Status: v1 (issue #11). Scope: explicit host → instance port publishing for LXD and Incus,
> compiled to native `proxy` devices. This document is the feature-level contract; the manifest
> schema table is mirrored in [`SPEC_MANIFEST.md`](SPEC_MANIFEST.md) §3.11 and the machine
> interface is extended in [`SPEC_RESULT.md`](SPEC_RESULT.md).

## 1. Overview

`lxm` manages the **desired set** of published ports per instance. Each `ports:` entry compiles to
one native LXD/Incus `proxy` device named `proxy-<name>`, carrying the ownership marker
`user.lxm.managed: "true"`. The daemon's proxy device is the only portable, feature-flagged-free
publishing mechanism shared by LXD and Incus; `nat: true` enables its DNAT mode for VM targets.

Ports are reconciled exactly like mounts and data disks: presence is desired state, omission from
the manifest retires the device on the next apply, and edits are computed as field-level diffs.
The feature is **additive** to the manifest schema (no version bump) and to `lxm/result/v1`.

## 2. Device encoding

| Manifest | Provider device |
| :-- | :-- |
| `name: web` | device name `proxy-web` (`proxy-` namespace) |
| `listen: 127.0.0.1:8080` | `"listen": "tcp:127.0.0.1:8080"` |
| `connect: 127.0.0.1:80` | `"connect": "tcp:127.0.0.1:80"` |
| `proto: udp` | endpoint prefix `udp:` on both values |
| `nat: true` | `"nat": "true"` (omitted when false; the daemon default) |
| — | `"type": "proxy"`, `"user.lxm.managed": "true"` |

Ownership is **marker-based, not name-based**. A marker-carrying `proxy` device under any device
name is considered lxm-owned and is diffed/repaired/removed; an unmarked `proxy-<name>` is a
foreign device and is preserved verbatim. Devices with a different `type` are never touched.

For reconciliation identity lxm uses the canonical **device name** (`proxy-<name>`): a marked
proxy device under any other name can never satisfy a manifest port and is retired on the next
apply. A canonical-shaped but undeclared device (`proxy-foo` with no `foo` port) is removed with a
`ports[foo]` diff; an anomalous marker-carrying device whose name is not `proxy-<name>` is removed
with a `devices[<device>]` diff so it cannot collide with a real port's `ports[<name>]` field. Malformed marked `proxy-<name>` devices are repaired in
place (`ports[<name>]`, old raw device map → new `PortConfig`), never surfaced as a plan error.
Additional daemon properties on a managed device that lxm does not emit (`proxy_protocol`, `bind`,
`security.*`, extra `user.*`) are outside the declarative contract: the plan reports a repair diff
with a warning, and the rewrite drops them.

## 3. Endpoint grammar & canonicalization

Endpoints are explicit IP literals with a port. `internal/ports.ParseEndpoint` is authoritative:

* accepted: `127.0.0.1:8080`, `10.0.0.5:53`, `[::1]:80`, `[2001:db8::1]:443`;
* rejected: hostnames (`localhost:8080`), zones (`[fe80::1%eth0]:80`), missing host (`:8080`),
  port 0, 4-in-6 literals (`[::ffff:1.2.3.4]:80`), protocol prefixes (`tcp:127.0.0.1:80` in a
  manifest field), and unbracketed IPv6;
* port range 1–65535; protocol `tcp` (default) or `udp`; proto is materialized by the loader;
* canonical form is `netip.AddrPort.String()` — deterministic device strings and diff values.

`connect` must never be a wildcard. `listen` may be a wildcard only with `nat: false`.

## 4. Manifest schema

```yaml
schema: lxm/config/v2
name: web-a
image: ubuntu:24.04
ports:
  - name: web
    listen: 127.0.0.1:8080
    connect: 127.0.0.1:80
  - name: dns
    listen: 127.0.0.1:5353
    connect: 127.0.0.1:53
    proto: udp
```

| Field | Type | Default | Rules |
| :-- | :-- | :-- | :-- |
| `name` | string | — (required) | `^[a-z]([a-z0-9-]{0,29}[a-z0-9])?$` — starts *and ends* alphanumeric (Incus device-name rule), ≤31 chars, unique per instance and layer. Device key `proxy-<name>`. |
| `listen` | string | — (required) | Endpoint on the host. |
| `connect` | string | — (required) | Endpoint on the instance. Must be explicit (no wildcard). |
| `proto` | string | `"tcp"` | `tcp` \| `udp`. |
| `nat` | bool | `false` | `true` enables DNAT mode (required for VMs). |

`remove.ports: [name]` drops inherited entries (error if unmatched). `replace.ports` wholistically
replaces the inherited list; as with every list directive in this schema, an empty
`replace.ports: []` is a **no-op** (use `remove.ports` to clear inherited entries). When a manifest
declares both `ports:` and a non-empty `replace.ports`, the replace list wins and the local `ports:`
entries are ignored (the same precedence the other list directives use). Merge is
**name-identity, whole-entry replacement**: an overlay entry
with the same `name` replaces the base entry entirely (unspecified fields reset to their defaults —
in particular, omitting `nat` turns a previously-NAT port back to non-NAT). Duplicate names within
one source layer are rejected before merging, including in a layer that has an inherited base.

## 5. Validation layers

1. **CUE authoring** (`#LXM_AUTHORING.ports`): field shape, `name` regex, `proto` enum, endpoint
   regex coarse gate. Unknown fields are rejected (`close`).
2. **Load normalization** (`LoadConfig`): protocol materialization, endpoint parse + canonical
   form, `nat` materialization.
3. **Post-merge Go** (`ValidatePostMerge`): `name` required and regex-valid, unique within the
   instance; `remove.ports` targets resolve.
4. **Per-instance cross-field Go** (`validateCommon`/`validatePorts`):
   * a NIC device name equal to `proxy-<name>` is a hard error (the port would silently overwrite
     the NIC in the instance PUT device map);
   * same-instance listener duplicates (pair-wise wildcard-aware, see §6);
   * `nat: false`: any explicit non-loopback/wildcard listen is accepted (plan warning only);
   * `nat: true`: IPv4 only, explicit non-wildcard **non-loopback** listen (loopback+NAT is
     runtime-broken: the daemon installs DNAT-only rules and the host drops locally generated
     packets), a declared static IPv4 in `networks:`, and `connect` equal to that address;
   * `type: virtual-machine` requires `nat: true`; containers may use either mode;
   * base fragments (`base: true`) skip the cross-field rules; the resolved leaf is validated.
5. **Fleet-scoped** (`config.ValidateFleetPorts`): same protocol/port listeners must be unique
   across all loaded manifests (full loaded set, before selector filtering), exit 3 citing both
   files and instances. Wildcard listeners conflict with the other family; specific addresses do
   not.
6. **Network layer** (`network.CheckInstancesWithProviderAndNetworkTypes`): `nat: true` is a hard
   error when the NIC providing the `connect` address is not bridged/routed. That covers a declared
   OVN vswitch, and a live provider network of type `ovn`, `macvlan`, `physical`, `sriov`,
   `loopback` or `none` (the daemons' NAT setup only accepts bridged/routed NICs). The check
   follows the connect address to its specific NIC, so a multi-NIC instance with a bridged `connect`
   NIC is unaffected. An undeclared/unknown parent type produces a plan warning instead of a hard
   error, since it cannot be classified offline.

## 6. Listener conflict semantics

A listener is `(proto, addr, port)` plus NAT mode. Conflict analysis is shared by same-instance,
fleet and handoff detection:

* **wildcard** (`0.0.0.0` or `::`) non-NAT listeners are dual-stack in Go/LXD/Incus: a wildcard
  binds an AF_INET6 socket with `IPV6_V6ONLY=0`, so it conflicts with **every** same-port address of
  either family;
* after that, conflict requires **exact address equality**; `127.0.0.1` and `[::1]` are independent;
* NAT rules are family-specific, so two NAT wildcard listeners are not a kernel conflict — v1
  rejects wildcard NAT `listen` at authoring time, and treats the wildcard case conservatively;
* explicit NAT and non-NAT listeners on the same address conflict (one would intercept the other);
* protocol and port must match for any conflict.

Listener uniqueness is evaluated over the **loaded manifests for one daemon endpoint**. On a
cluster, lxm does not know which member an instance will be placed on, so duplicate declared
listeners are rejected conservatively even if the instances might land on different members.

## 7. NAT and virtual machines

Daemon behaviour (verified against LXD and Incus sources):

* `nat: false` (container/VM target): userspace forkproxy connects to a `connect` address
  reachable from the host; for VMs this means the VM's static IP and the guest service must be
  reachable — lxm **requires `nat: true` for VMs** so the supported path is always available.
* `nat: true`: the daemon writes DNAT/SNAT rules for the host listen address to the VM's static
  address; requires an explicit non-loopback host address and an explicit static IPv4 `connect`
  equal to a declared NIC address. This is the recommended VM mode.
* loopback listen + NAT is **rejected** (runtime-broken on both daemons).
* dynamic-DHCP NAT (`connect` equal to the daemon-assigned address) and NAT wildcard listens are
  excluded from v1; they require Incus `v7.2.0+` (backports `7.0.1`/`7.0.2`; absent on `7.0.0`,
  `7.1.0` and all 6.x).
* the static IPv4 must be declared in the manifest (`networks[].ipv4`); profile-provided static
  addresses are not considered in v1.
* LXD/Incus reject NAT in a non-default project that enables `features.networks`; the provider
  interface does not expose project feature flags, so lxm cannot gate this at plan time — it
  surfaces as a provider error at apply.

## 8. Plan, apply and ownership

* `buildInstancesPost` appends `proxy-*` devices (create and fallback-recreate payloads).
* `buildInstancePut` preserves foreign devices, skips all marker-carrying proxies in the preserve
  loop, then merges the desired port devices last; omission from the PUT deletes retired devices
  because the daemon replaces the local device set.
* `Compute` diffs only marker-carrying proxies, keyed by canonical device name. A malformed marked
  device is still owned: it produces a synthetic repair (present in manifest) or removal diff
  (absent) — never a plan error and never a silent noop. Out-of-band properties are repaired the
  same way (see §2), so a device rewrite never drops them silently.
* An unmarked `proxy-<name>` collision is a **plan-time error** (exit 3). lxm never adopts and
  never emits a marker-only update: Incus rejects marker-only device changes (`ErrCannotUpdate`),
  and silent remove+add causes a listener blip.
* No restart is scheduled for port-only changes (`proxy` devices are hotpluggable; a changed
  same-name device is remove+add by the daemon).
* Native daemon rebuild (`instances_rebuild`) does not PUT devices; a port change planned in the
  same step lands on the next apply. This is a documented limitation with a test asserting the
  second apply converges.
* Profile-provided proxy devices are not managed by lxm; a local managed port that shadows a
  profile device emits a plan warning.

### Concurrency and listener handoffs

Fleet validation prevents two instances from **declaring** one listener, but not a handoff: Phase-2
instance steps run concurrently with a bounded semaphore, so a listener released by instance A and
claimed by B in the same apply can race (non-NAT may fail with `EADDRINUSE`; NAT may transiently
duplicate DNAT rules and self-heal). forkproxy retries binds for ~5 s, which usually covers a fast
release. v1 emits a plan warning when it detects a handoff and documents `-j 1`; apply-phase
ordering is future work.

## 9. Output

* `lxm plan`/`lxm apply`: text warnings; JSON plan diffs `ports[<name>]` and
  `ports[<name>].listen|connect|proto|nat`; create/update payloads contain the proxy devices. An
  anomalous marker-carrying device under a non-`proxy-<name>` name is removed with a
  `devices[<device>]` diff (outside the port-field namespace).
* `lxm diff`: the same field diffs; port add/remove values render via `PortConfig.String()`
  (`tcp 127.0.0.1:8080 -> 127.0.0.1:80 (nat)`).
* `lxm list`: text gains a `PORTS` column (managed ports only); JSON `results[].published_ports[]`
  is additive and covers the effective proxy set (local + profile-provided devices, the latter as
  `managed: false`).
* `lxm status`: text block `Published Ports:` (effective local + profile devices, ownership and
  NAT annotated); JSON `results[].published_ports[]`.
* `published_ports` entries: `{device, name?, proto, listen, connect, nat, managed}`, sorted by
  device name. No `lxm/result/v1` bump; the field is omitted when empty.

## 10. Security posture

Publishing a port deliberately exposes guest services to host-local (and for non-loopback listens,
remote) networks. Unlike `network_policy`, proxy listening traffic does **not** traverse
`network_policy` ACLs — see the non-loopback/wildcard plan warning and document host-firewall
requirements. On shared hosts bound to `localhost`, lxm cannot distinguish host users; use
non-loopback addresses only deliberately.

Host-side listeners are bound by the daemon (forkproxy), so privileged ports (<1024) do not
require lxm-side privileges; no privileged-port warning is emitted.

Cluster placement is out of scope for listener validation (see §6); identical listeners are rejected
conservatively per daemon endpoint.

## 11. Version compatibility

* LXD and Incus support `proxy` devices across the supported matrix; `nat` exists on both.
* Incus ≥ 6.15 (and 6.0.5+/7.x) permits `user.*` device keys and excludes them from the
  add/remove comparison used to classify device changes; it also rejects marker-only device
  updates. lxm therefore always performs a full device put (never a marker-only patch): the marker
  is accepted on create and preserved on update, while an ownership change alone would not be a
  valid update.
* Incus requires device names to start and end alphanumeric; the `name` regex applies to both
  daemons.
* Older Incus did not return a stable update error for device key changes; this is why adoption
  is a hard error instead of an automatic conversion.

## 12. Out of scope (v1)

Unix-socket proxying; `bind: instance` reverse publishing; `proxy_protocol`; `security.uid`/`gid`/
`mode`; port ranges and comma lists; network forwards; `status: absent` on individual ports
(omission deletes; `remove.ports` covers inheritance); NAT with `vswitch`-declared OVN NICs;
dynamic-DHCP NAT; NAT wildcard listens; apply-phase handoff ordering.

## 13. Error catalogue

| Condition | Exit | Message (prefix) |
| :-- | :--: | :-- |
| bad endpoint / unknown field / name regex | 3 | CUE authoring or `invalid listen`/`invalid connect` |
| protocol prefix in a field | 3 | `do not prefix the protocol; use the proto field` |
| hostname / zone / 4-in-6 / port 0 | 3 | `must be an explicit IP:port address…` / `4-in-6 address literals are not supported` |
| duplicate port name in one layer / instance | 3 | `duplicate port name %q` |
| wildcard `connect` | 3 | `wildcard connect is not supported; use an explicit instance address` |
| same-instance listener duplicate | 3 | `port %q of instance %q: listen %s %s duplicates port %q` |
| fleet listener duplicate | 3 | `port %s %s is published by both instance %q (%s) and instance %q (%s)` |
| fleet wildcard overlap | 3 | `… overlaps listener %s %s of instance %q (dual-stack wildcard binds conflict with both address families)` |
| NIC name collides with `proxy-<name>` | 3 | `NIC %q would collide with device %q` |
| unmarked device collision | 3 | `device %q for instance %q exists but is not lxm-managed; remove it or rename the port` |
| `nat: true` wildcard/loopback/IPv6/no-static/mismatch | 3 | see §5 |
| VM without `nat: true` | 3 | `virtual machines require nat: true (daemon limitation)` |
| NAT on unsupported parent type | 3 | `nat: true cannot use connect address %s on %s network %q (NAT requires a bridged/routed NIC)` |
| NAT parent type unknown | warning | `cannot determine whether network %q supports NAT` |

## 14. Examples

Container, host-local HTTP + UDP DNS:

```yaml
schema: lxm/config/v2
name: web-a
image: ubuntu:24.04
ports:
  - {name: web, listen: 127.0.0.1:8080, connect: 127.0.0.1:80}
  - {name: dns, listen: 127.0.0.1:5353, connect: 127.0.0.1:53, proto: udp}
```

VM with host-exposed HTTPS via NAT (static address on a bridged NIC):

```yaml
schema: lxm/config/v2
name: web-vm
type: virtual-machine
image: ubuntu:24.04
networks:
  - {name: eth0, parent: lxdbr0, ipv4: 10.10.10.31}
ports:
  - {name: https, listen: 10.10.10.1:8443, connect: 10.10.10.31:443, nat: true}
```
