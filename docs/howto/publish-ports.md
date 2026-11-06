# Publishing Ports

This guide shows you how to publish a host port to a service inside a container or virtual
machine. `ports:` entries compile to native LXD/Incus `proxy` devices — no extra daemons, no
iptables scripts.

## Prerequisites

* lxm installed ([Installation](../getting-started/installation.md))
* A working LXD or Incus host and permission to manage instances
* [Authoring Manifests](author-manifests.md)

## 1. Declare a port

Add a `ports` list to the manifest. The example serves a container-local HTTP server on host
`127.0.0.1:8080`:

```yaml
schema: lxm/config/v2
name: web-a
status: present
image: ubuntu:22.04
networks:
  - name: eth0
    parent: lxdbr0
ports:
  - name: web
    listen: 127.0.0.1:8080   # host side
    connect: 127.0.0.1:80    # instance side
```

* `name` — names the device (`proxy-web`), must be unique for the instance.
* `listen` — the host endpoint; an explicit IP literal and port.
* `connect` — the instance endpoint; an explicit IP literal and port (wildcards are rejected).
* `proto` — optional, `tcp` (default) or `udp`.
* `nat` — optional DNAT mode; required for virtual machines (see §5).

!!! note

    A loopback `listen` exposes the port to the host only. To accept remote traffic, use a host
    address such as `0.0.0.0:8080` or the bridge address — and read the security note in §6.

## 2. Preview the plan

```bash
lxm plan web.yaml
```

The plan shows an `update`/`create` step with a `ports[web]` diff. With `--format json` the create
payload contains the generated device:

```json
{
  "proxy-web": {
    "type": "proxy",
    "listen": "tcp:127.0.0.1:8080",
    "connect": "tcp:127.0.0.1:80",
    "user.lxm.managed": "true"
  }
}
```

## 3. Apply and verify

```bash
lxm apply web.yaml
lxm status web-a        # "Published Ports:" block
lxm list                # PORTS column
curl http://127.0.0.1:8080/
```

The same data is available as JSON: `lxm list --format json` and `lxm status web-a --format json`
include an additive `published_ports` array per result.

## 4. Update or remove the port

Edit the entry and re-apply:

```yaml
ports:
  - name: web
    listen: 127.0.0.1:8081
    connect: 127.0.0.1:80
```

`lxm plan` now reports `ports[web].listen`; apply recreates the proxy device without restarting the
instance. Removing the entry from the manifest deletes the device on the next apply.

Inheritance works like other list fields: a base manifest can declare ports, `remove.ports: [name]`
drops inherited entries, and `replace.ports` replaces the inherited list. Same-named entries replace
wholesale — omitting `nat` in an override resets it to `false`.

## 5. Publish a VM service with NAT

Virtual machines must use `nat: true`. The VM needs a static address on a bridged NIC, and `connect`
must equal that address:

```yaml
schema: lxm/config/v2
name: web-vm
type: virtual-machine
image: ubuntu:24.04
networks:
  - name: eth0
    parent: lxdbr0
    ipv4: 10.10.10.31
ports:
  - name: https
    listen: 10.10.10.1:8443
    connect: 10.10.10.31:443
    nat: true
```

The daemon installs DNAT rules from `10.10.10.1:8443` to the VM's static address. Put the host
traffic on an address the daemon can see (usually the bridge/gateway IP); loopback `listen` is
rejected because the resulting rules cannot deliver host-local traffic.

## 6. Conflict and exposure rules

* **One listener, one instance.** Two manifests cannot declare the same protocol/address/port. A
  wildcard listener (`0.0.0.0` or `::`) conflicts with every same-port address of either family
  because Go and the daemons create a dual-stack socket. Violations fail at plan time with exit 3,
  before anything is mutated.
* **NAT + OVN is rejected.** `nat: true` requires a bridged/routed NIC; an OVN-attached NIC cannot
  use the daemon's NAT path.
* **Non-loopback listens bypass `network_policy`.** Proxy-listening traffic terminates on the host
  and does not traverse network ACLs; allow it in the host firewall deliberately. lxm emits a plan
  warning for every non-loopback or wildcard listen.
* **Profiles are not managed.** A proxy device provided by a profile is left untouched; if a
  manifest port shadows it, lxm warns.
* **Unmanaged devices are never adopted.** A pre-existing `proxy-<name>` device without the lxm
  marker is foreign; declaring the same name is a plan-time error. Remove it or rename the port.

## 7. Listener handoffs

Moving a listener from instance A to B in one plan produces a warning: instance steps run
concurrently, so the claim can race with the release. Apply with `--jobs 1` (or re-run apply) when a
listener moves between instances; the daemon's forkproxy retries binds for a few seconds, which
usually covers a fast release.

## Troubleshooting

| Symptom | Cause | Fix |
| :-- | :-- | :-- |
| `port "web" ... wildcard connect is not supported` | `connect` uses `0.0.0.0`/`::` | Use an explicit instance address. |
| `nat: true cannot listen on a loopback address` | NAT with `127.0.0.1` | Use a non-loopback host/bridge address; loopback NAT is unusable. |
| `nat: true requires a static ipv4 in networks:` | VM/NAT without a static NIC address | Add `networks[].ipv4` and point `connect` at it. |
| `is published by both instance ...` | Fleet-wide listener collision | Change one `listen` port/address, or remove the duplicate. |
| `device "proxy-web" ... is not lxm-managed` | Unmarked pre-existing proxy device | Inspect with `lxm status`/`lxc config device show`, remove it or rename the port. |
| Port works locally but not remotely | `listen` is `127.0.0.1` | Bind a non-loopback address (plus host firewall rule). |

See [`PORTS-SPEC.md`](https://github.com/aiyor/lxm/blob/main/PORTS-SPEC.md) for the full contract.
