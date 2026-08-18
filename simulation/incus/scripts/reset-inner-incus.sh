#!/bin/bash
set -euo pipefail

echo "==> Deleting inner Incus instances..."
incus delete -f incus-test-web incus-test-db incus-test-db-node2 2>/dev/null || true

echo "==> Deleting inner Incus vswitches..."
for n in incus-vmbr0 incus-cbr0; do
  incus network delete "$n" 2>/dev/null || true
done

for a in lxm-incus-vmbr0 lxm-incus-cbr0; do
  incus network acl delete "$a" 2>/dev/null || true
done

echo "==> Inner Incus reset complete."
