#!/usr/bin/env bash
# ==============================================================================
# LXM - Canonical Incus End-to-End Test Suite Launcher
# ==============================================================================
# Runs a comprehensive end-to-end verification of LXM against Incus:
# 1. Container & VM Creation (provider: incus)
# 2. Host Directory Mounts (Initial + Dynamic Add/Remove)
# 3. Storage Disks Lifecycle (Initial + Dynamic Add + Detach + Delete)
# 4. VSwitch Bridge Creation + Microsegmentation ACL Compilation
# 5. Policy Enforcement & Traffic Isolation (Inter-group allow vs block)
# 6. In-Guest Script Execution
# 7. Snapshot & Rollback
# 8. Idempotent Zero-Drift Verification
# 9. Complete Teardown & Purge
# ==============================================================================
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "${SCRIPT_DIR}/../.." && pwd)"
LAB_VM="lxm-incus-lab"

GREEN='\033[0;32m'
RED='\033[0;31m'
YELLOW='\033[1;33m'
NC='\033[0m'

info() { echo -e "${YELLOW}==>${NC} $1"; }
pass() { echo -e "${GREEN}[PASS]${NC} $1"; }
fail() { echo -e "${RED}[FAIL]${NC} $1"; exit 1; }

# 1. Ensure Incus Lab VM is running
info "Checking Incus Lab VM (${LAB_VM})..."
VM_STATE=$(lxc list "^${LAB_VM}$" --format json | jq -r '.[0].status // "Stopped"')
if [[ "${VM_STATE}" != "Running" ]]; then
    info "Starting ${LAB_VM}..."
    lxc start "${LAB_VM}"
    for _ in {1..30}; do
        if lxc exec "${LAB_VM}" -- incus info >/dev/null 2>&1; then
            break
        fi
        sleep 2
    done
fi

# Ensure cluster node 2 is also running if present
if lxc list "^lxm-incus-lab-2$" --format json | jq -r '.[0].status // "Stopped"' | grep -q "Stopped"; then
    info "Starting lxm-incus-lab-2 cluster member..."
    lxc start lxm-incus-lab-2 || true
fi

info "Building latest lxm binary inside Incus Lab VM..."
lxc exec "${LAB_VM}" -- /bin/bash -c "export PATH=\$PATH:/usr/local/go/bin; cd /opt/lxm-src && go build -buildvcs=false -o /usr/local/bin/lxm ./cmd/lxm"

info "Executing Comprehensive Incus End-to-End Suite inside ${LAB_VM}..."
chmod +x "${SCRIPT_DIR}/test-incus-guest.sh"
lxc exec "${LAB_VM}" -- /bin/bash /opt/lxm-src/simulation/scripts/test-incus-guest.sh

pass "Incus End-to-End Test Suite Complete."
