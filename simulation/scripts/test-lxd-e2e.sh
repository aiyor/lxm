#!/usr/bin/env bash
# ==============================================================================
# LXM - Canonical LXD End-to-End Test Suite
# ==============================================================================
# Runs a comprehensive end-to-end verification of LXM against the host LXD daemon:
# 1. Container & VM Creation (UEFI nosecureboot VM + Container)
# 2. VSwitch Bridge Creation + Microsegmentation ACL Compilation
# 3. Policy Enforcement & Traffic Isolation (Inter-group allow vs block)
# 4. In-Guest Script Execution
# 5. Snapshot & Rollback
# 6. Idempotent Zero-Drift Verification
# 7. Complete Teardown & Purge
# ==============================================================================
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "${SCRIPT_DIR}/../.." && pwd)"
LXM_BIN="${REPO_ROOT}/bin/lxm"
TEST_DIR="${REPO_ROOT}/.scratch/e2e_lxd_test"

# Colors for output
GREEN='\033[0;32m'
RED='\033[0;31m'
YELLOW='\033[1;33m'
NC='\033[0m' # No Color

pass() { echo -e "${GREEN}[PASS]${NC} $1"; }
fail() { echo -e "${RED}[FAIL]${NC} $1"; exit 1; }
info() { echo -e "${YELLOW}==>${NC} $1"; }

cleanup() {
    info "Cleaning up test resources..."
    lxc delete -f e2e-lxd-web e2e-lxd-db 2>/dev/null || true
    lxc storage volume delete default custom/e2e-lxd-db-data1 2>/dev/null || true
    lxc storage volume delete default custom/e2e-lxd-db-data2 2>/dev/null || true
    for _ in {1..10}; do
        lxc network delete e2e-webbr0 2>/dev/null || true
        lxc network delete e2e-dbbr0 2>/dev/null || true
        lxc network acl delete lxm-e2e-webbr0 2>/dev/null || true
        lxc network acl delete lxm-e2e-dbbr0 2>/dev/null || true
        if ! lxc network show e2e-webbr0 >/dev/null 2>&1 && ! lxc network show e2e-dbbr0 >/dev/null 2>&1; then
            break
        fi
        sleep 1
    done
    rm -rf "${TEST_DIR}" /tmp/e2e-test-script.sh /tmp/e2e-host-shared /tmp/e2e-host-extra 2>/dev/null || true
}
trap cleanup EXIT
cleanup

info "Building latest lxm binary..."
(cd "${REPO_ROOT}" && go build -o "${LXM_BIN}" ./cmd/lxm)

info "Preparing test manifests in ${TEST_DIR}..."
mkdir -p "${TEST_DIR}" /tmp/e2e-host-shared /tmp/e2e-host-extra
echo "initial-mount-data" > /tmp/e2e-host-shared/initial.txt
echo "dynamic-mount-data" > /tmp/e2e-host-extra/dynamic.txt

cat << 'EOF' > "${TEST_DIR}/_base.yaml"
schema: lxm/config/v2
base: true
provider: lxd
image: images:debian/12/cloud
user: debian
wait:
  agent: 1m
vswitches:
  - name: e2e-webbr0
    ipv4: 10.90.0.1/24
    group: web
  - name: e2e-dbbr0
    ipv4: 10.95.0.1/24
    group: db
network_policy:
  allow:
    - from: web
      to: db
      direction: egress
EOF

cat << 'EOF' > "${TEST_DIR}/web.yaml"
schema: lxm/config/v2
include: [_base.yaml]
name: e2e-lxd-web
type: container
status: present
groups: [web]
mounts:
  - source: /tmp/e2e-host-shared
    path: /mnt/shared
networks:
  - name: eth0
    parent: e2e-webbr0
EOF

cat << 'EOF' > "${TEST_DIR}/db.yaml"
schema: lxm/config/v2
include: [_base.yaml]
name: e2e-lxd-db
type: virtual-machine
status: present
groups: [db]
vm:
  boot_mode: uefi-nosecureboot
limits:
  cpu: 2
  memory: 2GiB
  disk: 10GiB
disks:
  - name: data1
    size: 1GiB
    pool: default
    path: /mnt/data1
networks:
  - name: eth0
    parent: e2e-dbbr0
EOF

info "1. Executing Plan & Apply on LXD (Provisioning with Mounts & Disks)..."
"${LXM_BIN}" plan "${TEST_DIR}"
"${LXM_BIN}" apply "${TEST_DIR}"
pass "Reconciliation applied successfully."

info "2. Verifying instance states, network IPs, initial mounts, and data disks..."
WEB_IP=""
DB_IP=""
for i in {1..30}; do
    WEB_IP=$(lxc list "^e2e-lxd-web$" --format json | jq -r '.[0].state.network.eth0.addresses[]? | select(.family=="inet").address' || true)
    DB_IP=$(lxc list "^e2e-lxd-db$" --format json | jq -r '.[0].state.network.enp5s0.addresses[]? | select(.family=="inet").address' || true)
    if [[ -n "${WEB_IP}" && -n "${DB_IP}" ]]; then
        break
    fi
    sleep 1
done

if [[ -z "${WEB_IP}" || -z "${DB_IP}" ]]; then
    fail "Timed out waiting for IP addresses (web: ${WEB_IP}, db: ${DB_IP})"
fi

# Verify initial directory mount inside container
MOUNT_CONTENT=$(lxc exec e2e-lxd-web -- cat /mnt/shared/initial.txt 2>/dev/null || true)
if [[ "${MOUNT_CONTENT}" != "initial-mount-data" ]]; then
    fail "Directory mount verification failed: expected 'initial-mount-data', got '${MOUNT_CONTENT}'"
fi

# Verify initial data disk on VM
if ! lxc config show e2e-lxd-db --expanded | grep -q "disk-data1"; then
    fail "Data disk verification failed: disk-data1 not found on e2e-lxd-db"
fi
pass "Instances online with valid IPs (web: ${WEB_IP}, db: ${DB_IP}), mounts verified, and data disks attached."

info "3. Testing in-guest command execution..."
cat << 'EOF' > /tmp/e2e-test-script.sh
#!/bin/bash
echo "OK: $(hostname) [kernel: $(uname -r)]"
EOF
chmod +x /tmp/e2e-test-script.sh

"${LXM_BIN}" script e2e-lxd-web /tmp/e2e-test-script.sh >/dev/null
"${LXM_BIN}" script e2e-lxd-db /tmp/e2e-test-script.sh >/dev/null
pass "In-guest script execution succeeded on container and VM."

info "4. Testing Dynamic Mounts (Adding & Removing Host Directory Mounts)..."
# Add second mount to web.yaml
cat << 'EOF' > "${TEST_DIR}/web.yaml"
schema: lxm/config/v2
include: [_base.yaml]
name: e2e-lxd-web
type: container
status: present
groups: [web]
mounts:
  - source: /tmp/e2e-host-shared
    path: /mnt/shared
  - source: /tmp/e2e-host-extra
    path: /mnt/extra
networks:
  - name: eth0
    parent: e2e-webbr0
EOF
"${LXM_BIN}" apply "${TEST_DIR}"

EXTRA_MOUNT=$(lxc exec e2e-lxd-web -- cat /mnt/extra/dynamic.txt 2>/dev/null || true)
if [[ "${EXTRA_MOUNT}" != "dynamic-mount-data" ]]; then
    fail "Dynamic mount addition failed: /mnt/extra/dynamic.txt content mismatch (${EXTRA_MOUNT})"
fi
pass "Dynamic mount addition verified inside container."

# Remove second mount from web.yaml
cat << 'EOF' > "${TEST_DIR}/web.yaml"
schema: lxm/config/v2
include: [_base.yaml]
name: e2e-lxd-web
type: container
status: present
groups: [web]
mounts:
  - source: /tmp/e2e-host-shared
    path: /mnt/shared
networks:
  - name: eth0
    parent: e2e-webbr0
EOF
"${LXM_BIN}" apply "${TEST_DIR}"

if lxc config show e2e-lxd-web --expanded | grep -q "path: /mnt/extra"; then
    fail "Dynamic mount removal failed: /mnt/extra still present in instance config"
fi
pass "Dynamic mount removal verified."

info "5. Testing VM Disk Lifecycle (Add Disk, Detach Disk, Delete Volume)..."
# Add second disk (data2)
cat << 'EOF' > "${TEST_DIR}/db.yaml"
schema: lxm/config/v2
include: [_base.yaml]
name: e2e-lxd-db
type: virtual-machine
status: present
groups: [db]
vm:
  boot_mode: uefi-nosecureboot
limits:
  cpu: 2
  memory: 2GiB
  disk: 10GiB
disks:
  - name: data1
    size: 1GiB
    pool: default
    path: /mnt/data1
  - name: data2
    size: 1GiB
    pool: default
    path: /mnt/data2
networks:
  - name: eth0
    parent: e2e-dbbr0
EOF
"${LXM_BIN}" apply "${TEST_DIR}"

if ! lxc config show e2e-lxd-db --expanded | grep -q "disk-data2"; then
    fail "Dynamic disk addition failed: disk-data2 not found on e2e-lxd-db"
fi
pass "Dynamic disk addition verified on VM."

# Detach disk data2 (attach: false)
cat << 'EOF' > "${TEST_DIR}/db.yaml"
schema: lxm/config/v2
include: [_base.yaml]
name: e2e-lxd-db
type: virtual-machine
status: present
groups: [db]
vm:
  boot_mode: uefi-nosecureboot
limits:
  cpu: 2
  memory: 2GiB
  disk: 10GiB
disks:
  - name: data1
    size: 1GiB
    pool: default
    path: /mnt/data1
  - name: data2
    size: 1GiB
    pool: default
    path: /mnt/data2
    attach: false
networks:
  - name: eth0
    parent: e2e-dbbr0
EOF
"${LXM_BIN}" apply "${TEST_DIR}"

if lxc config show e2e-lxd-db --expanded | grep -q "disk-data2"; then
    fail "Disk detach failed: disk-data2 still attached to VM"
fi
if ! lxc storage volume show default custom/e2e-lxd-db-data2 >/dev/null 2>&1; then
    fail "Disk detach failed: underlying storage volume was incorrectly deleted"
fi
pass "Disk detachment verified (device detached, volume preserved in storage pool)."

# Delete disk data2 (status: absent)
cat << 'EOF' > "${TEST_DIR}/db.yaml"
schema: lxm/config/v2
include: [_base.yaml]
name: e2e-lxd-db
type: virtual-machine
status: present
groups: [db]
vm:
  boot_mode: uefi-nosecureboot
limits:
  cpu: 2
  memory: 2GiB
  disk: 10GiB
disks:
  - name: data1
    size: 1GiB
    pool: default
    path: /mnt/data1
  - name: data2
    status: absent
    pool: default
networks:
  - name: eth0
    parent: e2e-dbbr0
EOF
"${LXM_BIN}" apply "${TEST_DIR}"

if lxc storage volume show default custom/e2e-lxd-db-data2 >/dev/null 2>&1; then
    fail "Disk volume deletion failed: volume e2e-lxd-db-data2 still present in storage pool"
fi
pass "Disk volume deletion verified (volume destroyed from storage pool)."

info "6. Testing Network Policy Enforcement..."
# Allowed: Web -> DB (must succeed — policy allows web -> db egress)
if lxc exec e2e-lxd-web -- ping -c 2 -W 2 "${DB_IP}" >/dev/null 2>&1; then
    pass "Policy allowed: Web -> DB traffic permitted as configured"
else
    fail "Policy failure: Web -> DB ping failed (should be allowed)"
fi
# Blocked: DB -> Web (must fail/be rejected by ACL)
if lxc exec e2e-lxd-db -- ping -c 2 -W 2 "${WEB_IP}" >/dev/null 2>&1; then
    fail "Policy failure: DB was able to ping Web (should have been blocked)"
else
    pass "Policy enforced: DB -> Web traffic correctly blocked by ACL."
fi

info "7. Testing Snapshot & Rollback..."
"${LXM_BIN}" snapshot e2e-lxd-web e2e-snap
"${LXM_BIN}" rollback e2e-lxd-web e2e-snap
pass "Snapshot creation and rollback succeeded."

info "8. Testing Plan Idempotency (Zero-Drift)..."
# Clean up data2 absent declaration for clean zero-drift comparison
cat << 'EOF' > "${TEST_DIR}/db.yaml"
schema: lxm/config/v2
include: [_base.yaml]
name: e2e-lxd-db
type: virtual-machine
status: present
groups: [db]
vm:
  boot_mode: uefi-nosecureboot
limits:
  cpu: 2
  memory: 2GiB
  disk: 10GiB
disks:
  - name: data1
    size: 1GiB
    pool: default
    path: /mnt/data1
networks:
  - name: eth0
    parent: e2e-dbbr0
EOF
PLAN_OUT=$("${LXM_BIN}" plan "${TEST_DIR}")
if echo "${PLAN_OUT}" | grep -q "0 to create, 0 to update, 0 to recreate, 0 to delete"; then
    pass "Plan idempotency verified (zero drift)."
else
    fail "Plan idempotency failed: ${PLAN_OUT}"
fi

echo -e "\n${GREEN}===============================================${NC}"
echo -e "${GREEN}All Canonical LXD E2E tests passed cleanly!${NC}"
echo -e "${GREEN}===============================================${NC}"
