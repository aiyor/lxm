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

## Next steps

* [Mounting Host Directories](mount-host-dirs.md) — combine mounts with networking.
* [Cloud-Init Bootstrapping](cloud-init-bootstrapping.md) — network configuration via cloud-init.
* [Manifest Reference](../reference/manifest.md#networks) — the networks field reference.
