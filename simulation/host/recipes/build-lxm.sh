#!/bin/bash
set -euo pipefail
GOVERSION=go1.26.1
if ! command -v go >/dev/null 2>&1 || ! go version | grep -q 'go1.26'; then
  curl -fsSL "https://go.dev/dl/${GOVERSION}.linux-amd64.tar.gz" -o /tmp/go.tgz
  rm -rf /usr/local/go
  tar -C /usr/local -xzf /tmp/go.tgz
fi
export PATH=/usr/local/go/bin:$PATH
export GOFLAGS=-mod=readonly          # never touch the read-only mounted source
export GOCACHE=/var/cache/go-build
export GOMODCACHE=/var/cache/go-mod
mkdir -p "$GOCACHE" "$GOMODCACHE"
cd /opt/lxm-src
# -buildvcs=false: the mounted .git is owned by a host uid (dubious ownership),
# so version stamping would fail on the read-only mount.
go build -buildvcs=false -o /usr/local/bin/lxm ./cmd/lxm
chmod +x /usr/local/bin/lxm
