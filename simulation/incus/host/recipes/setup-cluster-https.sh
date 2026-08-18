#!/bin/bash
set -euo pipefail

echo "==> Generating Incus client trust token for host enrollment..."
TRUST_TOKEN=$(incus config trust add host-client --quiet)
echo "${TRUST_TOKEN}" > /root/incus-trust-token
chmod 0600 /root/incus-trust-token

echo "==> Trust token generated and saved to /root/incus-trust-token"
