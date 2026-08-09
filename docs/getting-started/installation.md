# Installation

This page covers what you need to run lxm, how to install the binary, and how to verify the install works against your LXD host.

## Requirements

* **Linux** (Ubuntu 22.04+ recommended). macOS is supported for the CLI; LXD itself must run on Linux. A **Linux kernel 5.12+** is recommended for idmapped host mounts.
* **LXD 5.0 or later**, installed and running.
* Your **user account must be a member of the `lxd` group** so lxm can talk to the LXD daemon through its local socket.
* **Go 1.26 or later** — only if you build lxm from source.

To confirm LXD is reachable and your user has access, run:

```bash
lxc list
```

An empty table is fine — the point is that LXD answers without a permission error. If it complains about the `lxd` group, add your user to it and log in again:

```bash
sudo usermod -aG lxd "$USER"
```

---

## Option 1: One-line installer

The quickest way is to pipe the installer script directly to `sh`. It detects your OS and architecture, downloads the latest release, verifies the SHA-256 checksum, and installs the binary to `/usr/local/bin`:

```bash
curl -fsSL https://raw.githubusercontent.com/aiyor/lxm/main/install.sh | sh
```

The installer is a plain POSIX `sh` script, so it needs nothing beyond `curl` and `tar`. You can pin a specific version or choose a different install prefix:

```bash
# pin a specific release
curl -fsSL https://raw.githubusercontent.com/aiyor/lxm/main/install.sh | LXM_VERSION=1.0.0 sh

# install under $HOME instead of /usr/local
curl -fsSL https://raw.githubusercontent.com/aiyor/lxm/main/install.sh | PREFIX="$HOME/.local" sh
```

With `PREFIX` set to a user-local directory, make sure `<prefix>/bin` is on your `PATH`.

---

## Option 2: Install with mise

If you manage toolchains with [mise](https://mise.jdx.dev/), lxm can be installed directly from its GitHub releases and pinned per project:

```bash
mise use -g github:aiyor/lxm
```

This installs the latest release binary (no Go toolchain needed) and makes `lxm` available on `PATH`. To use a specific version in a project, add it to that project's `mise.toml`:

```toml
[tools]
"github:aiyor/lxm" = "1.0.0"
```

Then run `mise install` to fetch it and `mise use` to select it. See the [mise GitHub backend docs](https://mise.jdx.dev/dev-tools/backends/github.html) for pinning, asset selection, and checksum options.

---

## Option 3: Prebuilt release binaries

Release builds for `linux` and `darwin` on `amd64` and `arm64` are published on the [GitHub Releases page](https://github.com/aiyor/lxm/releases).

Each release provides:

* `lxm_<version>_<os>_<arch>.tar.gz` — the binary plus the README and spec documents
* `checksums.txt` — SHA-256 checksums for every archive in the release

For example, on Linux (amd64), with `<version>` replaced by the release tag you downloaded:

```bash
LXM_VERSION=<version>

curl -sLO "https://github.com/aiyor/lxm/releases/download/v${LXM_VERSION}/lxm_${LXM_VERSION}_linux_amd64.tar.gz"
curl -sLO "https://github.com/aiyor/lxm/releases/download/v${LXM_VERSION}/checksums.txt"

# verify the download against the published checksum
grep "lxm_${LXM_VERSION}_linux_amd64.tar.gz" checksums.txt | sha256sum -c -

tar -xzf "lxm_${LXM_VERSION}_linux_amd64.tar.gz"
sudo install -m 0755 lxm /usr/local/bin/
```

Repeat with `darwin` / `arm64` as appropriate for other platforms.

---

## Option 4: Build from source

The only build dependency is Go 1.26+:

```bash
git clone https://github.com/aiyor/lxm.git
cd lxm
go build -o lxm ./cmd/lxm
sudo install -m 0755 lxm /usr/local/bin/
```

The produced binary is self-contained and has no runtime C dependencies (CGO is disabled), so the single `lxm` file is all you need to move between machines.

---

## Verify the install

Check the version:

```bash
$ lxm --version
lxm version dev (commit: none, built: unknown)
```

A build from a tagged release prints the release version, commit, and build date instead of `dev`.

Then confirm lxm can reach your LXD host:

```bash
$ lxm doctor
Running lxm doctor diagnostic checks...
[OK] LXD socket reachable
[OK] lxd group membership
[OK] Kernel idmapped mounts support
$ echo $?
0
```

`lxm doctor` checks that the LXD daemon socket is reachable, that your user is in the `lxd` group, and that your kernel supports idmapped mounts. Every line reads `[OK]` when the environment is healthy, and the command exits `0`.

!!! note

    `lxm doctor` also scans the current directory for manifest files and warns about any that are not yet migrated to the `lxm/config/v2` schema. Those warnings do not affect the exit code — the environment checks above are what determine success. See [Diagnosing with doctor](../howto/diagnose-with-doctor.md) for details.

---

## Next steps

Your environment is ready. Run your first managed container in the [Quick Start](quickstart.md), or read [Concepts](concepts.md) to understand what lxm does under the hood.
