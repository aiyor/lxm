#!/bin/bash
set -euo pipefail
lxc delete -f web-a web-b app-a db-a sandbox-a 2>/dev/null || true
for n in vmbr0 vmbr1 cbr0 svcbr0 labbr0; do
  lxc network delete "$n" 2>/dev/null || true
done
for a in lxm-vmbr0 lxm-vmbr1 lxm-cbr0 lxm-svcbr0 lxm-labbr0; do
  lxc network acl delete "$a" 2>/dev/null || true
done
