# Configuring Networking

This guide shows you how to declare static networking for a container. `networks:` manifests apply cleanly: lxm creates each interface as an LXD `nic` device with `nictype: bridged` on the bridge you name.

## Prerequisites

* lxm installed ([Installation](../getting-started/installation.md))
* A working LXD host with at least one managed bridge (LXD's default `lxdbr0`)
* [Authoring Manifests](author-manifests.md)

## 1. Declare a network in the manifest

Add a `networks` list to the container manifest. Each entry is a NIC on the container:

```yaml
schema: lxm/config/v2
name: web-01
status: present
image: ubuntu:22.04
groups: [web]
networks:
  - name: eth0
    ipv4: 10.171.13.120
    parent: lxdbr0
```

* `name` — the interface name inside the container (default `eth0`).
* `ipv4` — the static address to assign (optional; must be free and within the bridge subnet).
* `parent` — the LXD bridge to attach to (default `lxdbr0`).

A complete example with both a mount and a network is [`dev-station.yaml`](../examples/dev-station.yaml).

!!! note

    A static address must be unique and inside the bridge's subnet. Check the subnet with `lxc network show lxdbr0` (e.g. `ipv4.address: 10.171.13.1/24` means usable host addresses `10.171.13.2`–`10.171.13.254`).

## 2. Preview the NIC device

`plan --format json` shows the network as an LXD `nic` device with `nictype: bridged` and the address set:

```text
$ lxm plan config/dev.yaml --format json
...
    "instances_post": {
      ...
      "devices": {
        "eth0": {
          "ipv4.address": "10.171.13.120",
          "name": "eth0",
          "nictype": "bridged",
          "parent": "lxdbr0",
          "type": "nic"
        }
      }
    }
```

LXD requires every `type: nic` device to declare a `nictype`; lxm sets `bridged` to match the `parent` bridge.

## 3. Apply and verify

```text
$ lxm apply config/dev.yaml
Applied 1 step(s) across 1 container(s)
```

The container is created with `eth0` attached to `lxdbr0` at the static address. Confirm the device on the container:

```text
$ lxc config show web-01
...
  eth0:
    ipv4.address: 10.171.13.120
    name: eth0
    nictype: bridged
    parent: lxdbr0
    type: nic
```

and the assigned address:

```text
$ lxm list --name web-01
NAME    STATUS   MANAGED  GROUPS  IMAGE   IP
web-01  Running  true     web     ubuntu  10.171.13.120
```

## When things go wrong

| Error | Cause | Fix |
|---|---|---|
| `network N: invalid IPv4 address "10.171.13.abc"` | `ipv4` is not a valid IPv4 address | Fix the address; it must parse as IPv4. |
| `duplicate network name "eth0"` | Two networks with the same `name` after merge | Give each interface a distinct `name`. |
| No IP assigned | The static address is outside the bridge subnet or already in use | Pick a free address inside `lxc network show <parent>`'s subnet. |

## Managed virtual switches & network segmentation

Beyond attaching NICs to the provider's default bridge, lxm can **create and own virtual switches (Linux bridges or multi-node OVN overlay switches)**
and enforce **group-based traffic policy** between them. This is the `vswitches:` and
`network_policy:` feature — declarative, deterministic network segmentation for container and VM
fleets.

The mental model:

* A **vswitch** is a provider-managed virtual switch that lxm creates, owns, and reconciles (`vswitches:`).
  * **Linux Bridge (`type: bridge`)**: Local bridge on a single host.
  * **OVN Overlay Switch (`type: ovn`)**: Distributed virtual switch spanning multiple nodes across a cluster via Geneve encapsulation.
* A **network group** is a set of vswitches that share policy (the `group:` field on a vswitch).
* A **network policy** (`network_policy:`) expresses which groups may talk to which other groups,
  mutually or one-way. lxm compiles it into **network ACLs** (one `lxm-<vswitch>` ACL per
  grouped vswitch) with prefix decomposition and applies them with a `reject` default.

Both blocks are **fleet-scoped**: they are usually declared once in a `_base.yaml` and inherited by
every leaf manifest (`include: [_base.yaml]`), then unioned across all loaded manifests.

```yaml
# _base.yaml
schema: lxm/config/v2
base: true

vswitches:
  # Local Linux bridge for single-host VM workloads
  - name: vmbr0
    type: bridge
    ipv4: 10.30.0.1/24
    group: vms

  # Local Linux bridge for container services
  - name: cbr0
    type: bridge
    ipv4: 10.40.0.1/24
    group: containers

  # Distributed OVN overlay virtual switch across cluster nodes
  - name: ovnbr0
    type: ovn
    parent: lxdbr0        # uplink parent bridge (e.g. lxdbr0 or incusbr0)
    ipv4: 10.60.0.1/24
    group: services
    mtu: 1442             # 1500 - 58 bytes Geneve tunnel overhead

  # Isolated quarantine network with outbound internet access only
  - name: labbr0
    type: bridge
    ipv4: 10.70.0.1/24
    group: quarantine

network_policy:
  allow:
    - from: vms
      to: services        # vms ⇄ services, fully mutual
    - from: containers
      to: services
      direction: egress   # containers may initiate; services may not
```

Instances join a vswitch with the existing `parent:` key:

```yaml
# web-a.yaml
schema: lxm/config/v2
include: [_base.yaml]
name: web-a
networks:
  - name: eth0
    parent: ovnbr0
    ipv4: 10.60.0.10
```

### OVN Virtual Switches & Multi-Node Cluster Overlays

Open Virtual Network (OVN) brings software-defined networking (SDN) and multi-node overlay capabilities to LXD and Incus:

* **Cross-Node Connectivity**: Instances placed on the same OVN vswitch (or permitted by `network_policy`) can communicate seamlessly across different physical cluster nodes using Geneve encapsulation tunnels without any manual physical network re-configuration.
* **Uplink Parent Network (`parent:`)**: Every `type: ovn` virtual switch requires an uplink parent bridge (such as `lxdbr0` or `incusbr0`). The uplink must be an existing, active managed bridge configured with an IPv4 address (`ipv4.address`) and NAT (`ipv4.nat: true`), which provides WAN egress routing and acts as the source for upstream DNS resolver derivation.
* **MTU Sizing (`mtu:`)**: Geneve tunnel encapsulation adds 58 bytes of encapsulation header. When running over a standard physical network with 1500 MTU, specify `mtu: 1442` on OVN virtual switches to prevent packet fragmentation. On networks with jumbo frames (9000 MTU), set `mtu: 8942`.

### What the policy means

* **Intra-vswitch**:
  * On a **Linux Bridge (`type: bridge`)**, instances on the same bridge talk at L2 with no filtering (bridge ACLs operate at the bridge boundary).
  * On an **OVN Virtual Switch (`type: ovn`)**, OVN evaluates ACLs at every logical switch port under a default-reject policy. `lxm` automatically generates **G0** intra-switch allow rules (`allow S_V → S_V`) so instances on the same OVN switch communicate freely.
* **Intra-group** — vswitches sharing a `group` may communicate freely, mutually.
* **Inter-group** — **denied (reject) by default** in both directions unless an `allow` matches.
* **`direction: both`** (default) — mutual communication.
* **`direction: egress`** — the `from` group may initiate toward `to`; the `to` group may not
  initiate back. Reply traffic of established flows is handled by stateful allows.
* **Isolated "internet-only" network** — a group with **no** `allow` entries referencing it keeps
  outbound internet (default `internet: true`) while every internal subnet is rejected.
* **Guest DNS Resolution & Host Gateway Protection**:
  * For `internet: true` networks, `lxm` generates **G8** `/32` resolver carve-out rules allowing UDP/TCP DNS queries (port 53) to reachable internal/RFC1918 upstream resolvers while sealing off all other internal/RFC1918 traffic (public resolvers like `1.1.1.1` pass via the internet wildcard).
  * IPv4 DNS resolvers are derived automatically in order: explicit `DNSResolvers` → vswitch `dns.nameservers` → parent `dns.nameservers` → parent `ipv4.address` gateway.
  * For **`internet: false`** OVN networks, `lxm` emits explicit port 53 (UDP & TCP) reject rules targeting derived upstream resolvers to block outbound DNS queries, while keeping in-subnet resolvers open for intra-subnet DNS. If an upstream resolver cannot be derived, `lxm plan` warns: `specify dns.nameservers to install port 53 reject rules`.
  * **G9** port guards explicitly block non-DNS host ports (SSH, API listeners) on the gateway IP.

### `internal_cidrs` — declaring more "internal" space

The isolation model treats RFC1918 space (`10/8`, `172.16/12`, `192.168/16`), `100.64/10`,
loopback, link-local, and all managed vswitch subnets as internal — unreachable from
`internet: true` groups. Anything else is reachable through the internet wildcard.

**This includes your host's own public/routable addresses.** A cloud host with a public IP (e.g.
`203.0.113.5`) is *not* in the default internal set, so a quarantine network could reach host
services bound to it. If your host exposes services on non-RFC1918 addresses, declare them:

```yaml
network_policy:
  internal_cidrs:
    - 203.0.113.0/24    # the host's public block → now rejected from internet-enabled groups
```

`internal_cidrs` is **additive** to the locked default set and applies to every grouped vswitch
with `internet: true`. Entries outside the defaults are the ones that matter: a
`192.168.77.0/24` declaration adds nothing, because the default `192.168.0.0/16` already covers it.

### Caveats

* **`nat: false` with `internet: true` drops egress.** If a grouped virtual switch disables NAT (`nat: false`) while keeping internet access (`internet: true`), `lxm plan` emits a warning: instances will send egress traffic without source NAT, and packets with RFC1918 source IPs will be dropped upstream by the host or router firewall.
* **Guest routing can bypass policy.** An instance with NICs on vswitches from two different groups
  can forward traffic between them if guest IP forwarding is enabled. `lxm plan` warns when it sees
  this (R10); it cannot prevent it.
* **Cross-bridge traffic is source-NAT'd at the destination bridge.** ACLs are evaluated *before*
  that NAT (verified on LXD 6.9), so the source-subnet rules match correctly — but the *guest on the
  receiving side* sees the destination bridge's gateway as the peer source, not the real source IP.
* **Tightening is not retroactive.** Removing an `allow` blocks new connections immediately, but
  already-established flows keep flowing until the kernel conntrack entry expires (up to 5 days for
  TCP). See [Conntrack lifecycle](../reference/conntrack-lifecycle.md).

## Next steps

* [Mounting Host Directories](mount-host-dirs.md) — combine mounts with networking.
* [Cloud-Init Bootstrapping](cloud-init-bootstrapping.md) — network configuration via cloud-init.
* [Manifest Reference](../reference/manifest.md#networks) — the networks field reference.
* [Conntrack lifecycle](../reference/conntrack-lifecycle.md) — what happens when you tighten policy
  with live traffic.
