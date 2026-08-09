# Interactive Shell & SSH

This guide shows you how to get inside a container — with an interactive shell over LXD itself, or a hardened SSH session with automatic host-key registration.

## Prerequisites

* lxm installed ([Installation](../getting-started/installation.md))
* A running managed container

## 1. Open an interactive shell

`lxm shell` opens `/bin/bash -l` inside the container over the LXD websocket — no SSH server required inside the container:

```text
$ lxm shell dev-station
root@dev-station:~# exit
logout
```

By default it runs as `root`. Run as another user with `--run-as`:

```bash
lxm shell dev-station --run-as dev
```

The command is interactive: `--format json` is rejected with `exit 2` (the interactive carve-out), because there is no meaningful JSON envelope for an interactive session:

```text
$ lxm shell dev-station --format json
Error: interactive command shell rejects --format json
$ echo $?
2
```

## 2. SSH into a container

`lxm ssh` resolves the container's IP itself, passes it to OpenSSH as a routing hint, and keys host-key verification on the **container name**. On first connect it registers the container's host key into a tool-managed `known_hosts` file under an advisory lock, then runs `ssh` with `StrictHostKeyChecking=yes`.

```bash
lxm ssh dev-station
lxm ssh dev-station 'uname -a'            # run a single command
lxm ssh dev-station --user dev            # pick the SSH user
lxm ssh dev-station -i ~/.ssh/id_ed25519  # pick an identity
```

Preview the exact `ssh` invocation with `--dry-run`:

```text
$ lxm ssh glm --dry-run 'hostname'
Dry-run: ssh -o HostName=10.171.13.93 -o Port=22 -o UserKnownHostsFile=/home/tliang/.config/lxm/known_hosts -o StrictHostKeyChecking=yes -o HostKeyAlias=glm superuser@glm hostname
```

The user defaults to the manifest's `user` (via `user.lxm.user` metadata), then `root`. Because verification is keyed on the container name (not the IP), a DHCP address change does not trip "HOST IDENTIFICATION HAS CHANGED".

!!! warning

    lxm verifies host keys on every SSH connection (`StrictHostKeyChecking=yes`). Do **not** bypass this on containers you do not fully control. The `--insecure` flag and `-o StrictHostKeyChecking=no` / `-o UserKnownHostsFile=/dev/null` options disable verification and print a loud warning:

```text
$ lxm ssh glm --insecure --dry-run hostname
WARNING: Host key verification disabled by --insecure flag
Dry-run: ssh -o HostName=10.171.13.93 -o Port=22 -o UserKnownHostsFile=/dev/null -o StrictHostKeyChecking=no superuser@glm hostname
```

## 3. Control the known-hosts file

Host keys live in `~/.config/lxm/known_hosts` (a dedicated file, never your primary `~/.ssh/known_hosts`). Override the location with `LXM_KNOWN_HOSTS_FILE` — useful in CI runners and shared hosts where each job needs an isolated file:

```bash
LXM_KNOWN_HOSTS_FILE=/var/lib/ci/known_hosts lxm ssh dev-station
```

## When things go wrong

| Error | Cause | Fix |
|---|---|---|
| `container "X" not found` (exit 5) | The container name is wrong or the container does not exist | Check `lxm list`. |
| `container "X" has no IPv4 address / is not running` | The container is stopped or has no address | Start it (`lxm apply`) and wait for the network. |
| `host key registration failed` | `ssh-keyscan` could not reach the container | Confirm the container is running and reachable. |
| `Host key verification failed` | The container was rebuilt and its key changed | A rebuild purges the stale key on the same plan; if not, run `lxm ssh` again after a reconcile. |
| `interactive command ssh rejects --format json` | `--format json` on `ssh`/`shell` | Do not pass `--format` to interactive commands. |

## Next steps

* [Running Scripts](run-scripts.md) — execute scripts without an interactive session.
* [Provisioning with Recipes](provision-with-recipes.md) — the recipes that prepare the container before you shell in.
* [CLI Reference](../reference/cli.md#lxm-ssh) — the ssh/shell command reference.
