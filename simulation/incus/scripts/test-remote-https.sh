#!/bin/bash
set -euo pipefail

VM="${1:-lxm-incus-lab}"
echo "==> Resolving management IP for VM: ${VM}..."
VM_IP=$(lxc list "^${VM}\$" --format json | jq -r '.[0].state.network.eth0.addresses[] | select(.family=="inet") | .address')

if [ -z "${VM_IP}" ]; then
    echo "ERROR: Could not resolve IPv4 for ${VM}" >&2
    exit 1
fi

echo "==> Testing HTTPS REST connectivity to Incus on https://${VM_IP}:8443..."
if curl -k -s -m 5 "https://${VM_IP}:8443/1.0" | grep -q "1.0"; then
    echo "PASS: Incus HTTPS REST API reachable at https://${VM_IP}:8443"
else
    echo "FAIL: Could not reach Incus HTTPS REST API at https://${VM_IP}:8443" >&2
    exit 1
fi

echo "==> Running lxm remote list to verify enrolled remotes..."
lxm remote list || true
