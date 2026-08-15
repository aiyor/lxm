# NETWORK-SPEC — Managed Virtual Switches & Network Segmentation

**Feature**: `vswitches:` and `network_policy:` — declarative management of LXD virtual bridges and
group-based traffic policy, compiled deterministically into LXD network ACLs.

**Status**: IMPLEMENTED and verified against a real LXD 6.9 daemon (nftables backend). This
document is the authoritative specification of the feature as shipped; the behavior described here
is pinned to LXD 6.9 at implementation time.

**Schema**: `lxm/config/v2`. Companion user documentation: `docs/howto/configure-networking.md`,
`docs/reference/manifest.md`, `docs/reference/results-and-exit-codes.md`,
`docs/reference/conntrack-lifecycle.md`.

---

## 1. Overview & Goals

`lxm` attaches NIC devices to bridges (`nictype: bridged`, parent defaulting to `lxdbr0`), but by
default does not manage the LXD network objects themselves. This feature adds:

1. **`vswitches:`** — declarative management of LXD *managed virtual switches* (bridges), so VM and
   container fleets can live on separate networks with controlled inter-network traffic.
2. **`network_policy:`** — a group-based traffic policy compiled deterministically into **LXD
   network ACLs**, supporting:
   - isolated networks (outbound internet only; cannot initiate traffic toward any internal
     network),
   - networks that communicate mutually,
   - network groups: intra-group allow, inter-group deny by default, with explicit selective
     allowances between groups.

### 1.1 Why "vswitches"

LXD manages five network types: `bridge`, `ovn`, `macvlan`, `physical`, `sriov`. Only `bridge` and
`ovn` are *managed switches* with LXD-run IPAM/DHCP; macvlan/physical/sriov are passthrough uplink
types that never carry a managed subnet. The umbrella term **vswitch** covers exactly — and only —
that object family. v1 manages `type: bridge`; `ovn` is a future additive relaxation that unlocks
the stronger OVN ACL feature set. The instance-level `networks:` field (NIC list) keeps its name;
NICs join a vswitch via the existing `parent:` key.

### 1.2 Locked design principles

1. **The vswitch subnet is explicit.** Every managed vswitch declares its IPv4 CIDR. All ACL
   compilation uses concrete CIDR subjects — never LXD selector syntax (`@internal`/`@external`),
   which is OVN-only and unsupported on bridges.
2. **Deny-by-default inside the policy world; untouched outside.** Only vswitches assigned a
   `group` receive ACLs. Ungrouped vswitches keep stock LXD behavior (fully open, NAT-routed).
3. **Additive plan schema.** Network reconciliation steps are separate step entries executed
   strictly before instance steps.
4. **Unmanaged-by-omission.** Removing a vswitch from manifests stops managing it; `lxm` never
   deletes networks or ACLs in v1.
5. **Isolation defaults conservative.** Where a trade-off exists between operator convenience and
   isolation, isolation wins; convenience is available via explicit opt-in.

## 2. Verified LXD constraints (these drive the design)

Constraints were verified against the LXD 6.x reference documentation:
[How to configure network ACLs](https://documentation.ubuntu.com/lxd/en/latest/howto/network_acls/),
[Network ACLs reference](https://documentation.ubuntu.com/lxd/en/latest/reference/network_acls/),
[Bridge network reference](https://documentation.ubuntu.com/lxd/en/latest/reference/network_bridge/).

| # | Constraint (paraphrased from the docs) | Consequence for this feature |
| :- | :-- | :-- |
| C1 | Bridge ACLs apply only on the boundary between the bridge and the LXD host; they **cannot** filter traffic between instances on the same bridge. | **Segmentation granularity is per-vswitch.** Instances that must be isolated from each other MUST be on separate vswitches. No intra-vswitch rules are generated. |
| C2 | ACL groups and network selectors (`@internal`/`@external`) are **not supported on bridges** (OVN-only). | Compilation uses only concrete CIDR subjects. "Internet" is an `allow` to `0.0.0.0/0` plus higher-priority explicit `reject` rules for internal CIDRs (C3). |
| C3 | Rule priority is **action-based**, not list-order: `drop` > `reject` > `allow` > default action (default `reject`). | Explicit `reject` rules deterministically outrank any `allow` regardless of specificity. This is the enforcement backbone — and the reason subtraction must be done by **CIDR decomposition**, not list removal (§5.2). |
| C4 | LXD baseline network service rules are added **before** ACL rules and cannot be blocked by them. | DHCP/DNS from instances to the vswitch gateway (`.1`) is guaranteed. `lxm` generates no service rules and cannot break them. |
| C5 | `allow` rules are **stateful** (the `allow-stateless` variant is the explicit opt-out; documented in Incus, same lineage — the LXD docs do not state this explicitly). | One-way policies depend on this. Verified on real LXD 6.9 (gate T1, §13). |
| C6 | With the `iptables` firewall driver, IP-range subjects (a–b form) are unsupported. | Only CIDR subjects are generated. (The backend is chosen daemon-side and is not API-observable per network; CIDR-only rules make the backend choice irrelevant.) |
| C7 | ACLs are available for bridge and OVN networks (`network_acl` API extension, LXD ≥ 4.10). | Prerequisite gate + `lxm doctor` bridge-ACL probe (§8.4). |
| C8 | LXD validates `security.acls` references at network create/update time — referencing a missing ACL is an API error. | Execution ordering: ACLs must exist before the vswitches that reference them (§8.3). |

> **Version pinning.** C1–C8 were verified against LXD 6.9. The `network_acl` extension's 4.10
> floor reflects its **OVN** origin; **bridge** ACL support arrived in a later LXD release. The
> authoritative capability check is the `lxm doctor` probe, which creates and deletes a throwaway
> bridge ACL for real (§8.4).

## 3. Manifest Schema

Both new sections are **fleet-scoped** and typically live in `_base.yaml`; they are unioned across
all loaded manifests for an invocation (§7).

```yaml
schema: lxm/config/v2
base: true

vswitches:
  - name: vmbr0
    ipv4: 10.30.0.1/24        # required; gateway must be the first usable host
    group: vms                # optional; assigns to a network group

  - name: cbr0
    ipv4: 10.40.0.1/24
    group: containers

  - name: svcbr0
    ipv4: 10.50.0.1/24
    group: services

  # Quarantine: outbound internet only, cannot reach any internal network
  - name: labbr0
    ipv4: 10.60.0.1/24
    group: quarantine

network_policy:
  internal_cidrs:             # additive to the locked default internal set (§5.2)
    - 192.168.77.0/24         # e.g. the host's physical LAN
  allow:
    - from: vms
      to: services            # vms ⇄ services, fully mutual
    - from: containers
      to: services
      direction: egress       # one-way: containers may initiate; services may not
```

### 3.1 `vswitches` field reference

| Field | Type | Default | Rules |
| :-- | :-- | :-- | :-- |
| `name` | string | — (required) | `^[a-z][a-z0-9-]{0,30}$`. The LXD network name; referenced by instance `networks[].parent`. |
| `type` | string | `"bridge"` | v1: only `"bridge"` (CUE-restricted). `"ovn"` is a future additive relaxation. |
| `driver` | string | `"native"` | → `bridge.driver`. `"native" \| "openvswitch"`. **Immutable after create** (change ⇒ plan error, exit 3). |
| `ipv4` | string | — (required) | Coarse CUE regex, then Go validation: `net.ParseCIDR`, address must equal the **first usable host** (`network IP + 1`), prefix length ∈ **[8, 29]**. Rationale: `/8` is the largest sane bridge supernet; `/29` is the smallest practical for DHCP (8 addresses ⇒ network + gateway `.1` + broadcast leave 5 usable hosts). `10.10.50.1/16` is rejected — it is not the first host of `10.10.0.0/16`. |
| `ipv6` | string | `"none"` | v1: only `"none"` (IPv6 policy compilation is deferred — accepting a CIDR without compiling policy for it would be a silent security hole). |
| `nat` | bool | `true` | → `ipv4.nat`. |
| `group` | string | — | Optional group membership. Absence ⇒ managed for addressing only (no ACLs). Removing `group` later detaches the ACL (§8.2). |
| `internet` | bool | `true` | Only meaningful with `group`. `true` ⇒ egress to the internet allowed. `false` ⇒ fully internal group (internet-bound egress rejected). |

Fixed, non-configurable (locked for determinism): `ipv4.dhcp: true`, `dns.domain: lxd`, no
`ipv4.routes`.

`nat` and `internet` are independent but coupled: `nat: false` + `internet: true` emits a wildcard
egress with no source NAT (RFC1918 sources dropped upstream) — `lxm plan` warns on this
combination. `internet: false` does **not** imply `nat: false`; it blocks egress at the ACL,
leaving `ipv4.nat` under operator control. A fully internal group (`internet: false`) must use the
gateway as DNS (C4 baseline) or an internal resolver; external DNS is rejected by the egress
default.

### 3.2 `network_policy` field reference

```yaml
network_policy:
  internal_cidrs:              # optional; ADDITIVE to the locked default internal set (§5.2)
    - 192.168.77.0/24
  allow:
    - from: <group>            # required; a group with ≥1 vswitch
      to: <group>              # required; a group with ≥1 vswitch
      direction: both          # "both" (default, mutual) | "egress" (one-way initiation)
```

* `network_policy` is a **scalar object within a single manifest tree**: presence-wins
  whole-value replacement in inheritance (no field-level merge, no `remove`/`replace` directives).
  The "additive" language on `internal_cidrs` refers to the locked default internal set, not to
  inheritance. Across sibling manifests the `allow` list and `internal_cidrs` list are **unioned
  and deduplicated** (§7).
* `from == to` is legal but redundant (intra-group is already allowed); flagged by a plan warning.
* An entry referencing an unknown group ⇒ exit 3:
  `network_policy: group "<name>" has no vswitches assigned`.
* Duplicate `(from, to, direction)` entries: **identical** duplicates dedup silently; the same
  `(from, to)` pair with a **differing** `direction` ⇒ exit 3.
* `internal_cidrs` entries must be valid CIDRs; duplicates dedup silently.

## 4. Policy Semantics (R1–R10)

For traffic **crossing a vswitch boundary** (leaving one vswitch toward another network or the
outside; see C1 for the intra-vswitch caveat), with `allow` stateful (C5, verified):

| # | Rule | Semantic |
| :- | :-- | :-- |
| R1 | Intra-vswitch | Instances on the same vswitch communicate at L2 **without filtering** (C1). Segmentation is per-vswitch, full stop. |
| R2 | Intra-group | Instances on vswitches sharing a `group` may communicate unrestricted, mutually. |
| R3 | Inter-group | **Denied (reject) by default** in both directions unless an `allow` matches — enforced by explicit reject rules (when a wildcard internet allow exists, C3) and by the ACL default action (reject). |
| R4 | `direction: both` | Vswitches of `from` and `to` receive mirrored egress+ingress allows toward/from each other's subnets — full mutual communication. |
| R5 | `direction: egress` | Vswitches of `from` get egress-allow to `to` subnets; vswitches of `to` get ingress-allow from `from` subnets (so the flow is admitted at the destination boundary). `to` gets **no** egress-allow toward `from` — `to` cannot initiate toward `from`. Return traffic of `from`-initiated flows rides the stateful allow (C5). **Verified on real LXD 6.9 (gate T1(a)).** |
| R6 | Internet | `internet: true` (default): egress `allow` to `0.0.0.0/0` **plus** explicit `reject` rules covering every internal CIDR not decomposed out by a permitted allowance (C2+C3). `internet: false`: no wildcard allow; egress to non-internal destinations hits the reject default. |
| R7 | Isolation requirement | An "isolated, outbound-internet-only" network = a group with **no** `allow` entries referencing it and default `internet: true`: the wildcard allow is neutralized for all internal CIDRs by R6 rejects (including its own subnet and the host gateway — §5.2); no ingress allows exist, so nothing can initiate toward it. |
| R8 | Bridge services | DHCP/DNS from instances to the vswitch gateway is guaranteed by LXD baseline rules (C4). All other host-bound traffic on the gateway IP is rejected by the own-subnet reject (§5.2). |
| R9 | Ungrouped vswitches | No ACLs generated; routing/NAT per stock LXD. Grouped→ungrouped traffic follows stock LXD routing (allowed) — if a grouped network must not reach an ungrouped internal network, that network **must** be assigned to a group (the policy world is opt-in per vswitch). |
| R10 | Multi-NIC bypass (invariant + warning) | An instance with NICs on vswitches from ≥2 distinct groups can forward traffic between them if guest IP forwarding is enabled (the policy filters vswitch boundaries, not guest routing). `lxm plan` warns: `instance "<n>" NICs span network groups [<g1>, <g2>]; guest routing may bypass network_policy`. Preventing this is out of scope for v1. |

## 5. Compilation to LXD Network ACLs (deterministic)

For every grouped vswitch `V` (subnet `S_V`, group `G_V`):

1. Create/own exactly one ACL: **name `lxm-<vswitch-name>`**,
   `config: {"user.lxm.managed": "true"}`, description
   `"lxm managed policy for vswitch <V> (group <G_V>)"`.
2. Apply it to the network with:
   * `security.acls: lxm-<vswitch-name>`
   * `security.acls.default.ingress.action: reject`
   * `security.acls.default.egress.action: reject`
3. Rule set for `V` — **CIDR subjects only** (C2, C6), all rules `state: enabled`.

Compilation is a **pure function** of `(vswitches, network_policy)`: identical input yields
byte-identical ACLs. No `drop` rules are emitted in v1 (locked): `reject` (ICMP admin-prohibited)
matches LXD's own default-action semantics and gives fast-fail diagnostics.

### 5.1 Generator matrix (single vantage point)

All generators are formulated from the ACL being compiled — vswitch `V` in group `G_V` with subnet
`S_V`; `S_P` is the subnet of vswitch `P`.

| Gen | Direction | Action | Source | Destination | Trigger |
| :-- | :-- | :-- | :-- | :-- | :-- |
| G1 | egress | allow | `S_V` | `S_P` ∀ P ∈ G_V, P ≠ V | intra-group (R2) |
| G2 | ingress | allow | `S_P` ∀ P ∈ G_V, P ≠ V | `S_V` | intra-group (R2) |
| G3 | egress | allow | `S_V` | `S_P` ∀ P ∈ H | policy `from G_V → H`, `direction: both \| egress` (R4/R5) |
| G4 | egress | allow | `S_V` | `S_P` ∀ P ∈ F | policy `from F → G_V`, `direction: both` (reciprocal egress leg of R4) |
| G5 | ingress | allow | `S_P` ∀ P ∈ H | `S_V` | policy `from G_V → H`, `direction: both` (mirror ingress leg of R4) |
| G6 | ingress | allow | `S_P` ∀ P ∈ F | `S_V` | policy `from F → G_V`, `direction: both \| egress` — admits F-initiated flows at V's boundary (R4/R5) |
| G7 | egress | allow | `S_V` | `0.0.0.0/0` | only when `internet: true` on V (R6) |
| G8 | egress | **reject** | `S_V` | `Decompose(InternalSet, PermittedEgress(V)) ∪ {S_V}` | only when G7 exists (otherwise the reject default covers) |

`PermittedEgress(V)` = the union of destination subnets produced by G1, G3, G4 (all ≠ `S_V`). `S_V`
itself is excluded from `PermittedEgress` by construction, so it is never a carve-out candidate.

No other rules are generated; everything unmatched hits the reject defaults. Deterministic ordering
within each ACL: `(direction, action, source, destination)`, then deduplicated.

### 5.2 Internal set, decomposition & host protection

**Why decomposition is mandatory.** If G8 subtracted *list entries* from the internal set, LXD's
action-priority model (C3) would still evaluate `reject S_V → 10.0.0.0/8` before
`allow S_V → 10.50.0.0/24` — shadowing every inter-group allow inside RFC1918 space. The fix is
true **CIDR carve-out by prefix decomposition**:

> `SubtractCIDRs(supernet, carveOuts)` replaces each supernet with the maximal set of
> non-overlapping sub-prefixes excluding the carved-out ranges. `10.0.0.0/8 \ 10.50.0.0/24`
> produces exactly 16 prefixes — one sibling per level from `/9` to `/24`.

Compliance requirement (enforced by a property test): the emitted reject set contains **no CIDR
overlapping any G1/G3/G4 destination**.

**Default internal set (locked)** — managed vswitch subnets ∪ operator `internal_cidrs` ∪:

```
10.0.0.0/8, 172.16.0.0/12, 192.168.0.0/16, 100.64.0.0/10, 127.0.0.0/8, 169.254.0.0/16,
::1/128, fe80::/10, fc00::/7
```

**Canonicalization (locked)**: before decomposition, the internal set is canonicalized by removing
any CIDR subsumed by another member, regardless of provenance. E.g. an operator `192.168.77.0/24`
is already covered by the default `192.168.0.0/16` and contributes no extra reject rule; an
operator `10.0.0.0/8` merges with the identical default.

**Why the RFC1918 supernets stay.** Scoping the default set to "other managed vswitch subnets +
loopback/link-local" would silently weaken the isolation guarantee — a quarantined network could
initiate toward the host's physical LAN unless the operator remembered to declare it. Keeping the
supernets + decomposing costs a bounded, small rule set and preserves both LAN-isolation default
and host protection.

**Own-subnet host protection (locked).** `S_V` is **always** in the G8 reject set and is **never**
a carve-out candidate. Because the bridge gateway (e.g. `10.60.0.1`) is an IP alias on the LXD
host, this rejects instance→host traffic on the gateway IP (SSH, LXD API, exporters bound to
`0.0.0.0`) — with the sole exception of DHCP/DNS (C4, R8). Intra-vswitch instance↔instance traffic
does not cross the boundary (C1) and is unaffected.

**Host non-RFC1918 addresses (documented caveat).** The internal set covers RFC1918 space,
operator `internal_cidrs`, and managed vswitch subnets — but **not** the host's public/routable
addresses (e.g. a cloud host's public IP). Such addresses are reachable from `internet: true`
groups via the G7 wildcard. Operators whose hosts expose services on non-RFC1918 addresses must
declare them in `network_policy.internal_cidrs`; `lxm` cannot infer them. (The RFC1918 defaults
already cover `100.64.0.0/10` CGNAT space, loopback, and link-local.)

**Rule-count guard.** If the compiled reject set for any vswitch exceeds **256 reject rules**,
`lxm plan` emits a warning (fragmentation from many carve-outs); compilation still proceeds.

### 5.3 Worked example

Given the §3 manifest (`vms` 10.30.0.0/24, `containers` 10.40.0.0/24, `services` 10.50.0.0/24,
`quarantine` 10.60.0.0/24; default internal set):

* **VM/container separation** — VM NICs `parent: vmbr0`, container NICs `parent: cbr0`; different
  subnets, different vswitches; inter-group traffic denied by R3 (no `vms ↔ containers` allow).
* **Isolated internet-only (`quarantine`)** — its ACL contains exactly:
  * egress allow 10.60.0.0/24 → 0.0.0.0/0 (G7);
  * egress reject 10.60.0.0/24 → `10.0.0.0/8` (incl. own subnet — no carve-outs exist for
    quarantine), `172.16.0.0/12`, `192.168.0.0/16`, `100.64.0.0/10`, `127.0.0.0/8`,
    `169.254.0.0/16` (G8 — rejects outrank the G7 allow, C3);
  * ingress: no allow rules → default reject (nothing can initiate in);
  * DHCP/DNS to 10.60.0.1 still works (C4); everything else on 10.60.0.1 is rejected.
* **Mutual communication** — put vswitches in one group (R2/G1–G2), e.g. two vswitches both
  `group: vms`.
* **Group matrix** — `vms ⇄ services` mutual (R4: G3+G5 on vmbr0, G4+G6 on svcbr0);
  `containers → services` one-way (R5: G3 on cbr0, G6 on svcbr0, no reciprocal egress);
  `quarantine ↔ anything-internal` denied (R3/R6). For vmbr0, G8 carves 10.50.0.0/24 out of
  `10.0.0.0/8` (decomposition) so the G3 allow is not shadowed.

## 6. Instance NIC Integration

* `networks[].parent` (existing field, default `lxdbr0`) is the join key. No `NetworkConfig`
  change is required.
* **NIC subnet membership check (plan-time, exit 3)**: if an instance declares `networks[].ipv4`
  and the referenced parent is a declared vswitch, `lxm plan` asserts the IP falls within the
  vswitch's CIDR; violation ⇒ exit 3:
  `instance "<n>": NIC "<dev>" static IP <ip> is outside parent vswitch "<parent>" subnet <cidr>`.
* **Unknown-parent warning**: a NIC `parent` naming neither a live LXD network nor a declared
  vswitch ⇒ plan warning:
  `instance "<n>": NIC parent "<p>" is not a known LXD network or declared vswitch`.
  This check consults the **live** LXD network set (fetched for every plan), so a vswitch-less
  fleet using the stock `lxdbr0` produces no false warning.
* **Multi-NIC group-span warning (R10)**: NICs on vswitches from ≥2 distinct groups ⇒ plan
  warning.
* Recommended pattern: a VM `_base.yaml` sets `networks: [{name: eth0, parent: vmbr0}]`; a
  container base sets `parent: cbr0`.

## 7. Fleet union, inheritance & conflicts

* Desired vswitches = the union of `vswitches:` across **all** loaded manifests for the
  invocation.
* Two manifests declaring the same vswitch name with **different** resolved specs ⇒ exit 3:
  `vswitch "<name>" declared with conflicting specs in "<fileA>" and "<fileB>"`. **Identical**
  duplicate declarations dedup silently. A base-vs-leaf conflict cites the base path and the leaf
  path.
* **Base files are not standalone manifests**: YAML discovery skips `_`-prefixed files, so a
  `vswitches:`/`network_policy:` block in `_base.yaml` reaches the fleet only via `include` into
  each leaf, which carries a copy. Dedup-on-identical is what makes the recommended `_base.yaml`
  placement work. **Within a single merged tree**, identical redeclarations are likewise allowed
  (deduplicated); only the fleet union reports conflicts, with both file paths.
* **`network_policy` across sibling manifests**: `allow` entries and `internal_cidrs` are unioned
  and deduplicated. A conflicting `(from, to)` pair (differing `direction`) ⇒ exit 3:
  `network_policy: conflicting declarations for "<from>" → "<to>" (both vs egress) in "<fileA>" and "<fileB>"`.
* **Selector-scope invariant**: when `-g`/`--name` filters which instances are applied, `vswitches`
  and `network_policy` are still compiled against the **entire loaded manifest set**, not the
  filtered subset — otherwise peer vswitches would vanish from the group registry and produce
  false exit-3 errors. Network steps are always executed in full.
* **Single-file invocations** (`lxm plan config/x.yaml`) manage only that file's vswitches and
  policy.
* **Worked example.** `_base.yaml` declares `vswitches: [vmbr0]` and
  `network_policy: {allow: [{from: vms, to: services}]}`. Two leaves each `include: [../_base.yaml]`.
  The union deduplicates the two identical `vmbr0` declarations and the two identical `allow`
  entries, yielding one vswitch and one allow. Had a leaf overridden `vmbr0`'s `ipv4`, that is a
  base-vs-leaf spec conflict ⇒ exit 3.

## 8. Reconciliation & Execution Model

### 8.1 Plan model (additive)

`Plan` gains `NetworkSteps []NetworkStep` (`json:"network_steps"`), executed before instance
`Steps`. Schema string stays `lxm/plan/v1` (additive field):

```go
type NetworkStep struct {
    Kind      string             // "create_acl" | "update_acl" | "create_vswitch" | "update_vswitch"
    Name      string             // vswitch or ACL name
    Changed   bool
    Diff      []FieldDiff        // same FieldDiff shape as instance steps
    Tightened bool               // update_acl removes/narrows allows (§9)
    ACLPost   *api.NetworkACLsPost
    ACLPut    *api.NetworkACLPut
    NetPost   *api.NetworksPost
    NetPut    *api.NetworkPut
}
```

### 8.2 Reconciliation rules (per vswitch)

| Live state | Desired | Action |
| :-- | :-- | :-- |
| missing | present | `create_acl` (+ `create_vswitch`) — §8.3 ordering |
| exists, mutable drift | present | `update_vswitch` — mutable keys only: `ipv4.nat`, `security.acls*`, description |
| exists, immutable drift (`ipv4.address`, `driver`) | present | **Plan error, exit 3**; message tells the operator to migrate instances to a new vswitch name |
| exists | absent from manifests | **Unmanage only** — no deletion, no ACL detach; plan warning `vswitch "<name>" no longer declared; left unmanaged (lxm never deletes networks)` |
| exists, unmanaged by lxm | declared in manifests | Adopt: mutable keys reconciled; **adoption refuses if `security.acls` already contains a foreign ACL** (exit 3) to avoid clobbering hand-written policy |
| exists, grouped (ACL attached) | same vswitch, `group` removed | `update_vswitch` removing `lxm-<name>` from `security.acls` and clearing `security.acls.default.*`; the orphaned ACL is left unmanaged and its description is annotated `unattached (group removed)` |

ACL reconciliation: `lxm` owns exactly the ACLs named `lxm-<vswitch>` (marker
`user.lxm.managed=true`). Rules are diffed as an order-independent set — any drift ⇒ `update_acl`
with the fully recomputed rule list. Foreign ACLs on a managed vswitch are preserved verbatim
`security.acls` entries are compared **order-insensitively**, so a differently-ordered ACL list
never causes perpetual update churn. An existing `lxm-<name>` ACL **without** the lxm marker
(hand-written) is overwritten but first flagged by a plan warning. Ordering carries no precedence
(C3): a foreign ACL's `allow` cannot loosen `lxm-<vswitch>`'s compiled `reject` rules.

### 8.3 Execution ordering (driven by C8)

1. **`create/update_acl` steps** — standalone objects (`/1.0/network-acls`), no dependencies.
2. **`create/update_vswitch` steps** — can now safely reference the guaranteed-present `lxm-<name>`
   ACLs in `security.acls`.
3. **Instance steps** — NICs attach to fully configured vswitches.

Within the network phase, the first failing step **short-circuits** the remaining network steps
(avoiding cascading "ACL not found" errors); any network-phase failure **aborts the apply before
any instance step runs**. All steps fetch a fresh ETag immediately before their update.

### 8.4 Prerequisites gating & `lxm doctor`

* `HasExtension("network_acl")` is required when any **grouped** vswitch is declared; missing ⇒
  exit 4 with an explicit message.
* `lxm doctor` runs the authoritative **bridge-ACL probe**: it creates and deletes a throwaway
  bridge ACL. This is the functional gate, because the extension's 4.10 floor reflects its OVN
  origin and bridge ACL support arrived later.
* The firewall backend (nftables vs xtables/iptables) is chosen **daemon-side at startup** and is
  **not API-observable per network**, so there is no `lxm doctor` iptables check. Because the
  compiler emits CIDR-only subjects (never a–b ranges, C6), the backend choice cannot affect rule
  correctness.
* The ACL listing in `lxm plan/apply` is gated on `HasExtension("network_acl")`; the network
  listing (needed for the NIC unknown-parent check) runs for every plan whenever the service
  exposes the network surface. A live-state **listing failure is fatal** (exit 4) — planning
  against an empty live set would propose create steps for existing objects and silently skip the
  adoption-refusal/foreign-ACL checks.

### 8.5 Verified cross-bridge behavior (gate T2)

Inter-group policies need the destination bridge's source-subnet rules to match the **original**
source. Verified on LXD 6.9: inter-bridge traffic **is** source-NAT'd to the destination bridge's
gateway (destination guests observe `_gateway.lxd` as the peer source) — **but** LXD evaluates
bridge ACLs in the forward/filter path **before** per-bridge postrouting SNAT, so the source-subnet
rules match the pre-NAT source and R2/R4/R5 hold unchanged. The guarantee therefore rests on
**netfilter implementation ordering**, not an LXD contract, and is pinned to the nftables backend
on LXD 6.9 at implementation time. Operators whose host-side policy keys on real source IPs must
account for the gateway source address seen by destination guests. No `nat` default change or
compiler revision was required.

## 9. Conntrack lifecycle after policy tightening

ACL `update_acl` steps change the **rule set**, not the **flow table**. Established connections
ride the kernel's conntrack `established,related` accept path, which sits ahead of the ACL chains.

* **Old flows outlive tightened policy.** A connection permitted before a tightening keeps flowing
  until its conntrack entry expires (kernel default for established TCP:
  `nf_conntrack_tcp_timeout_established` = **432000 s ≈ 5 days**). New connections are rejected
  immediately. **Verified on real LXD 6.9.**
* **`lxm` never flushes conntrack in v1.** Flushing requires host-root `conntrack`/`nft` and has
  blast radius beyond lxm-managed objects (Docker, other bridges, host sockets share the table); it
  would violate the tool's least-privilege posture.
* **Plan-time visibility**: when an `update_acl` step **removes or narrows** allow rules
  (`Tightened`), `lxm apply` emits:
  `ACL "<name>" tightened; pre-existing connections may persist until conntrack expiry (see docs: conntrack lifecycle)`.
  A pure widening (only new allows added) does **not** warn.
* **Operational runbook** (manual, after tightening with live flows):
  1. Scoped delete: `sudo conntrack -D -d <newly-blocked-cidr>` (or `-s <src> -d <dst>`).
  2. Instance restart — destroys the veth pair and its conntrack entries (no host privileges).
  3. Full flush `sudo conntrack -F` — discouraged sledgehammer.

Full user documentation: `docs/reference/conntrack-lifecycle.md`.

## 10. Result Envelope & Exit Codes

* `lxm plan/apply --format json`: `network_steps` renders inside the `plan` object (additive);
  `apply` adds a top-level `network_results: [{name, kind, changed, ok, duration_ms}]` alongside
  `results`, so a network-phase failure is machine-distinguishable from an instance-phase failure.
  A failed network step also appears in `errors` with the failing object's name in the `name` field
  (instance errors use `container`).
* Exit codes: manifest/policy/union conflicts, IPAM violations, NIC-subnet violations ⇒ `3`
  (`CONFIG_ERROR`); LXD API/extension errors, including `create_acl`/`create_vswitch` failures and
  live-state listing failures ⇒ `4` (`LXD_ERROR`). No new codes.
* **Phase-abort**: a network-step LXD error aborts the apply before any instance step runs; the
  envelope's `network_results` records the failing step and its `LXD_ERROR` (exit 4) surfaces in
  the top-level `errors` array.

## 11. LXD Service Surface (`internal/lxd`)

```go
type NetworkService interface {
    GetNetworks() ([]api.Network, error)
    GetNetwork(name string) (*api.Network, string, error)
    CreateNetwork(net api.NetworksPost) error
    UpdateNetwork(name string, net api.NetworkPut, etag string) error
    GetNetworkACLs() ([]api.NetworkACL, error)
    GetNetworkACL(name string) (*api.NetworkACL, string, error)
    CreateNetworkACL(acl api.NetworkACLsPost) error
    UpdateNetworkACL(name string, acl api.NetworkACLPut, etag string) error
    DeleteNetworkACL(name string) error
}
```

In-memory fakes back the same interface for unit/integration tests.

## 12. Implementation Notes

**Go structs (`internal/config`)** — `VSwitchConfig` (`Name`, `Type`, `Driver`, `IPv4`, `IPv6`,
`NAT *bool`, `Group`, `Internet *bool`; `NAT`/`Internet` nil defaults to `true`),
`NetworkPolicyRule` (`From`, `To`, `Direction`; empty = `both`), `NetworkPolicy` (`InternalCIDRs`,
`Allow`), added to `Config` as `VSwitches []VSwitchConfig` and `NetworkPolicy *NetworkPolicy`.

**Merge semantics (`MergeConfigs`)** — `VSwitches`: list-concat within an include chain (like
`Mounts`/`Networks`); `NetworkPolicy`: whole-value presence-wins replacement within a tree;
`remove`/`replace` directives gain **no** `vswitches` fields (removal is fleet-scoped
"unmanage by omission"). Defaults (`type: bridge`, `driver: native`, `ipv6: none`, `nat`/`internet`
true, `direction: both`) are materialized at load so the strict resolved schema round-trips.

**CUE (`internal/config/schemas/v2.cue`)** — `#VSwitchObjAuthoring`/`#VSwitchObjResolved` and
`#PolicyRuleAuthoring`; `vswitches`/`network_policy` added to both `#LXM_AUTHORING` and
`#LXM_RESOLVED`. The authoring regex for `ipv4` is deliberately coarse; all numeric checks
(first-usable-host, `/8`–`/29`, overlap, group resolution) are Go-side post-merge.

**Go-side post-merge validation (exit 3)** — `ipv4` first-usable-host + mask bounds; duplicate
names (deduplicated identically, conflicts deferred to the fleet union with attribution);
overlapping subnets; `internet: false` without `group`; valid `internal_cidrs`.

## 13. Verification & Test Plan

Unit coverage: config parsing/defaults/validation; compiler golden rule sets (quarantine 7-rule
set, mutual mirror, one-way no-reciprocal-egress, determinism, CIDR-only subjects, `internet:
false`); G8 decomposition golden (`10.0.0.0/8 \ 10.50.0.0/24` → 16 disjoint prefixes) and the
property that no reject overlaps any G1/G3/G4 destination; internal-set canonicalization;
reconciler matrix (create/update/adopt/unmanage/detach, immutable refusal, foreign-ACL refusal,
order-insensitive `security.acls`, `Tightened` on narrowing only, orphaned-ACL annotation);
executor ordering/phase-abort/`network_results`; CLI end-to-end (envelope `network_steps`/
`network_results`, extension gate, NIC-subnet exit 3, live-state listing error exit 4,
vswitch-less no-false-warning). `task lint` clean; full suite race-clean.

Integration gates on a real LXD 6.9 daemon (in the `simulation/` nested-LXD harness):

| Gate | Result |
| :-- | :-- |
| R2 intra-group, R3 separation, R4 mutual, R5 one-way, R6/R7 quarantine | **PASS** (7/7 matrix) |
| **T1(a)** stateful one-way over TCP | **PASS** — forward flow + reply traffic complete; reverse-initiated connection rejected |
| **T1(c)** host-gateway exposure | **PASS** — gateway SSH refused in 2 ms (fast-fail reject); DHCP/DNS still work (C4 baseline) |
| **T2** cross-bridge source-NAT | See §8.5 — guests see the gateway source, but ACLs match pre-NAT; R2/R4/R5 hold |
| Conntrack lifecycle | **PASS** — established flow survives a tightening; new connection rejected; tightening warning emitted; kernel timeout 432000 s confirmed |
| Idempotency | Re-apply after a clean apply produces 0 network steps and 5 instance noops |

## 14. Locked Decisions & Open Questions

1. **Per-instance NIC ACLs: rejected for v1.** Network-level ACLs keep the model fleet-abstract;
   per-vswitch granularity (C1) is the documented invariant. Per-instance overrides may be
   revisited in a future v2.
2. **OVN phase keeps concrete CIDR subjects.** One deterministic compiler, identical golden output
   on bridge and OVN backends; `@internal`/`@external` selectors remain unused.
3. **Network forwards / load balancers: unmanaged in v1.** Inbound exposure is a distinct concern
   from internal segmentation.
4. **`reject` vs `drop`: v1 emits `reject` only** — matches LXD default-action semantics and gives
   fast-fail diagnostics; a per-group `drop` knob is a future additive option.
5. **Internal-set default: RFC1918 supernets retained with mandatory CIDR decomposition** —
   preserves LAN-isolation default and host protection.
6. **Conntrack is not flushed on policy tightening** — established flows persist until conntrack
   expiry; flushing is a host-admin runbook action, not automated.

**Mask bounds `/8`–`/29` (locked).** `/8` is the floor (a large supernet costs nothing
correctness-wise because the compiler decomposes CIDRs); `/29` is the ceiling. Caveat: `/8`
vswitches overlap every RFC1918 supernet and maximize decomposition output, so the >256-rule
warning fires more readily for `/8` fleets with dense allow matrices — a warning, not an error.

**R5 is never demoted to "best-effort".** If a future LXD version breaks stateful inter-bridge
allows (C5), R5 is re-specified (mirror ingress allow, or drop the one-way `egress`), not
documented away.
