#!/bin/bash
set -uo pipefail

echo "==> Verifying Incus daemon status inside Lab VM..."
incus admin waitready --timeout=10 || { echo "FAIL: Incus not ready"; exit 1; }

echo "==> Incus cluster members:"
incus cluster list

echo "==> Incus instances:"
incus list

echo "==> Incus networks:"
incus network list

echo "PASS: Incus verification completed successfully."
