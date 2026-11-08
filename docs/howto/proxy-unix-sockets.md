# Proxy Unix Sockets Between the Host and Containers

This guide shows you how to bridge a Unix-domain socket between the host and a container with the
`sockets:` block. Each entry compiles to a native LXD/Incus `proxy` device, so lxm keeps it in sync
— no hand-rolled `lxc config device add` drift. Use it for per-instance host IPC such as
a credential broker, ssh-agent forwarding, GUI/audio sockets, or a sidecar that talks to a host
service over a socket.

There are two directions, and you pick one per socket with `bind`:

| Direction | `bind` | Listening socket created | Reaches |
| --- | --- | --- | --- |
| Container → host service | `instance` | inside the container (`listen`) | host socket `connect` |
| Host client → container service | `host` (default) | on the host (`listen`) | container socket `connect` |

!!! warning

    A socket proxy is **host access**, not a harmless file mount. `bind: instance` lets any process
    in the container that can open the socket — including root — reach your host service, and
    `bind: host` lets host-local processes reach the container service. Proxy traffic does **not**
    traverse `network_policy` ACLs: the listening socket's `uid`/`gid`/`mode` are the only access
    control, and the daemon default mode is `0644`. Keep the host service authenticated per
    connection, protect the socket's parent directory, and do not share one host socket across
    containers unless that is the intended design.

## Prerequisites

* lxm installed ([Installation](../getting-started/installation.md))
* A working LXD (6.x+) or Incus (6.x+) host whose daemon exposes the `proxy_unix`
  extension (add `proxy_unix_dac_properties` for `uid`/`gid`/`mode`); a missing extension fails
  with exit `4` rather than silently degrading
* [Authoring Manifests](author-manifests.md)
* A host service that already listens on a Unix socket (for `bind: instance`), or a container
  service listening on one (for `bind: host`)

## 1. Declare the sockets

Add a `sockets:` list to the manifest. The complete, compile-checked manifest is
[`sockets-demo.yaml`](../examples/sockets-demo.yaml) — the socket declarations are:

```yaml
schema: lxm/config/v2
name: sockets-demo
image: ubuntu:24.04
networks:
  - {name: eth0, parent: lxdbr0}
sockets:
  - name: broker                                   # device socket-broker
    listen: unix:/var/tmp/sshigit-broker.sock      # instance side (created by the daemon)
    connect: unix:/run/sshigit-broker/sockets-demo.sock  # host side (must be listening)
    bind: instance
    uid: 1000
    gid: 1000
    mode: "0600"
  - name: agent                                    # device socket-agent
    listen: unix:/tmp/lxm-docs-demo/agent.sock     # host side (created by the daemon)
    connect: unix:/run/sshigit/agent.sock          # instance side (must be listening)
    bind: host
    uid: 1000
    gid: 1000
    mode: "0600"
```

* `name` — names the device (`socket-<name>`); unique per instance.
* `listen` / `connect` — `unix:<absolute path>`. With `bind: instance`, `listen` is created **inside
  the container** and `connect` is the **host** socket to relay to; with `bind: host` it is the
  reverse.
* `bind` — `host` (default) or `instance`.
* `uid` / `gid` / `mode` — the owner and permission bits of the **listening** socket (not
  `security.uid`/`security.gid`, which are forkproxy privilege-drop options and are not exposed).
  Omit them and the daemon creates the socket `root:root` with mode `0644`, so declare them
  explicitly whenever an unprivileged user must connect.

`sockets:` is **container-only**: proxy devices on virtual machines require `nat: true`, and NAT
cannot proxy Unix endpoints, so declaring a socket on a `type: virtual-machine` is a compile error.

!!! note

    The daemon creates the socket, never its parent directory. Create the container-side parent in
    the image, cloud-init or a recipe (`mkdir -p /run/sshigit`), and create the host-side parent
    before apply (`mkdir -p /tmp/lxm-docs-demo`). Place instance-side sockets in an early-stable
    directory such as `/run/<dir>` or `/var/tmp` — a path under a directory the guest remounts
    later (for example systemd's `/tmp` tmpfs) can be shadowed during boot.

## 2. Prepare the host side

For the `broker` socket (`bind: instance`), start your host service on the per-instance `connect`
path **before** a client connects; lxm never creates the host-side socket. A minimal broker that
answers every connection looks like this:

```python
# host-broker.py — run one instance per managed socket
import os, socket

path = "/run/sshigit-broker/sockets-demo.sock"
os.makedirs(os.path.dirname(path), exist_ok=True)
try:
    os.unlink(path)  # replace a stale socket from a previous run
except FileNotFoundError:
    pass
srv = socket.socket(socket.AF_UNIX, socket.SOCK_STREAM)
srv.bind(path)
srv.listen(16)
while True:
    conn, _ = srv.accept()
    request = conn.recv(4096)
    conn.sendall(b"OK:" + request)
    conn.close()
```

Keep it running for the lifetime of the fleet (`python3 host-broker.py &`). Every connection is
relayed independently, so restarting the broker does not require re-applying the manifest — the
next client connection reconnects.

For the `agent` socket (`bind: host`), the host-side listening socket needs a parent directory the
daemon can bind into:

```bash
mkdir -p /tmp/lxm-docs-demo
```

The container-side service for `agent.connect` (`/run/sshigit/agent.sock`) must be running inside
the container; add its parent directory to the image or cloud-init exactly like the `broker` case.

## 3. Preview the plan

```text
$ lxm plan docs/examples/sockets-demo.yaml
Plan: 1 to create, 0 to update, 0 to recreate, 0 to delete, 0 noop across 1 manifest(s)
Warning: socket "agent" of instance "sockets-demo" uses bind: host and creates a host-side unix socket at /tmp/lxm-docs-demo/agent.sock; host-local processes with access to the socket reach the instance service directly, bypassing network_policy
```

The warning is expected: `bind: host` exposes the container service to host-local processes. The
create payload contains the two devices (`--format json` shows them under
`plan.steps[0].instances_post.devices`):

```text
$ lxm plan docs/examples/sockets-demo.yaml --format json | jq '.plan.steps[0].instances_post.devices["socket-broker"]'
{
  "bind": "instance",
  "connect": "unix:/run/sshigit-broker/sockets-demo.sock",
  "gid": "1000",
  "listen": "unix:/var/tmp/sshigit-broker.sock",
  "mode": "0600",
  "type": "proxy",
  "uid": "1000",
  "user.lxm.kind": "socket",
  "user.lxm.managed": "true"
}
```

## 4. Apply it

```bash
lxm apply docs/examples/sockets-demo.yaml
```

The daemon creates the devices and their listening sockets. `apply` is idempotent: re-running it
with no manifest changes is a no-op, and a socket-only edit (for example a `mode` change) updates
the device without restarting the instance. `bind: host` emits the exposure warning above on every
plan until you remove the entry.

## 5. Use and change the sockets

From inside the container, connect to the `broker` listener like any other Unix socket — the
credential no longer lives in the container, it is minted by the host service:

```bash
lxm shell sockets-demo
# (inside the container)
python3 -c 'import socket; s = socket.socket(socket.AF_UNIX); \
  s.connect("/var/tmp/sshigit-broker.sock"); s.sendall(b"hello"); print(s.recv(4096))'
```

Editing an entry is a field-level diff (`sockets[broker].mode`, `sockets[broker].listen`, …);
removing it from the manifest retires the device on the next apply. `remove.sockets: [name]` drops
an inherited entry and `replace.sockets` replaces the inherited list — the same inheritance rules
as `ports:`.

## Verify

`lxm status` shows the effective socket devices with ownership and bind annotation:

```text
$ lxm status --provider lxd sockets-demo
Container:     sockets-demo
Status:        Running (Code 103)
Architecture:  x86_64
IP Address:    10.171.13.109
Managed:       true
Groups:

Published Ports:
  (none)

Unix Sockets:
  - agent: unix:/tmp/lxm-docs-demo/agent.sock -> unix:/run/sshigit/agent.sock (bind=host, uid=1000, gid=1000, mode=0600, managed)
  - broker: unix:/var/tmp/sshigit-broker.sock -> unix:/run/sshigit-broker/sockets-demo.sock (bind=instance, uid=1000, gid=1000, mode=0600, managed)

Recipe Hash Trail:
  (none)
```

`lxm list` adds the same data as a `SOCKETS` column (managed sockets only; `bind: instance`
annotated, `-` when none), and `--format json` adds the additive `published_sockets` array to each
result — identical for `lxm list` and `lxm status`:

```json
{
  "container": "sockets-demo",
  "published_sockets": [
    {
      "device": "socket-agent",
      "name": "agent",
      "listen": "unix:/tmp/lxm-docs-demo/agent.sock",
      "connect": "unix:/run/sshigit/agent.sock",
      "bind": "host",
      "uid": 1000,
      "gid": 1000,
      "mode": "0600",
      "managed": true
    },
    {
      "device": "socket-broker",
      "name": "broker",
      "listen": "unix:/var/tmp/sshigit-broker.sock",
      "connect": "unix:/run/sshigit-broker/sockets-demo.sock",
      "bind": "instance",
      "uid": 1000,
      "gid": 1000,
      "mode": "0600",
      "managed": true
    }
  ]
}
```

The transcript above was produced against LXD 6.10.1 LTS; the same output shape applies to Incus
(live Incus verification is still pending — see `TESTING.md` §2).

## When things go wrong

| Symptom | Cause | Fix |
| :-- | :-- | :-- |
| `host-side unix socket path "..." is declared by socket ... and socket ...` (exit 3) | Two manifests claim the same host-side path | Change one `listen`/`connect` path — one host socket can back only one managed socket. |
| `device "socket-broker" ... exists but is not owned by the sockets namespace` (exit 3) | An unmarked pre-existing device holds the name | Remove the device (`lxm status`/`lxc config device show`) or rename the manifest socket. |
| apply exits 4 with `no such file or directory` / `Failed listening` | The listening socket's parent directory does not exist | Create it: host side for `bind: host`, container side via cloud-init/recipe for `bind: instance`. |
| The client gets a reset/EOF but the device exists | The service on the other side is not listening | Start the host service (`bind: instance`) or the container service (`bind: host`); the relay reconnects on the next client connection — no re-apply. |
| `permission denied` when connecting | The listening socket's `uid`/`gid`/`mode` exclude the client (default `root:root` `0644`) | Declare `uid`/`gid`/`mode` matching the client. Root inside the container bypasses file permissions by design. |
| `Unix-socket proxies are container-only ...` (exit 3) | `sockets:` on a `type: virtual-machine` | Socket proxies are container-only; move the service into a container. |
| `provider server lacks the proxy_unix extension` (exit 4) | Daemon too old for Unix proxy devices | Upgrade LXD/Incus, or drop the `sockets:` block. |
| The socket disappears a while after the instance starts | The instance-side path sits under a directory the guest remounts during boot | Move it to an early-stable directory such as `/run/<dir>` or `/var/tmp`. |
| A stale socket file remains after removing the entry | The daemons do not remove the instance-side file, and Incus never removes a `bind: host` host socket | Harmless: nothing listens on it, and the next device start unlinks and replaces it. Delete it manually if you need the path back. |

See [Troubleshooting](../troubleshooting.md#socket-proxy-fails-validation-sockets) for the full
error catalogue.

## Next steps

* [Manifest Reference](../reference/manifest.md#unix-socket-proxies-sockets) — every `sockets:` field.
* [`SOCKETS-SPEC.md`](https://github.com/aiyor/lxm/blob/main/SOCKETS-SPEC.md) — the authoritative
  feature contract, daemon-verified semantics, and the live verification matrix.
* [Publishing Ports](publish-ports.md) — the TCP/UDP sibling feature (`ports:`).
* [Authoring Manifests](author-manifests.md) — inheritance, `remove` and `replace`.
