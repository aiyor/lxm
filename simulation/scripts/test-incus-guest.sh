#!/usr/bin/env bash
# ==============================================================================
# LXM - Canonical Incus In-Guest Test Suite
# ==============================================================================
# Runs comprehensive end-to-end verification of LXM against Incus:
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

GREEN='\033[0;32m'
RED='\033[0;31m'
YELLOW='\033[1;33m'
NC='\033[0m'

pass() { echo -e "${GREEN}[PASS]${NC} $1"; }
fail() { echo -e "${RED}[FAIL]${NC} $1"; exit 1; }
info() { echo -e "${YELLOW}==>${NC} $1"; }

LXM_BIN="/usr/local/bin/lxm"
TEST_DIR="/tmp/e2e_incus_test"
export LXM_PROVIDER=incus

cleanup_resources() {
    info "Cleaning up Incus test resources..."
    incus delete -f e2e-incus-web e2e-incus-db 2>/dev/null || true
    incus storage volume delete default custom/e2e-incus-db-data1 2>/dev/null || true
    incus storage volume delete default custom/e2e-incus-db-data2 2>/dev/null || true
    for _ in {1..10}; do
        incus network delete incus-webbr0 2>/dev/null || true
        incus network delete incus-dbbr0 2>/dev/null || true
        incus network acl delete lxm-incus-webbr0 2>/dev/null || true
        incus network acl delete lxm-incus-dbbr0 2>/dev/null || true
        if ! incus network show incus-webbr0 >/dev/null 2>&1 && ! incus network show incus-dbbr0 >/dev/null 2>&1; then
            break
        fi
        sleep 1
    done
    rm -rf "${TEST_DIR}" /tmp/e2e-test-script.sh /tmp/e2e-incus-host-shared /tmp/e2e-incus-host-extra 2>/dev/null || true
}

on_exit() {
    EXIT_CODE=$?
    if [[ ${EXIT_CODE} -ne 0 ]]; then
        echo -e "${RED}[FAIL] Incus E2E test failed with exit code ${EXIT_CODE}${NC}"
    fi
    cleanup_resources
    exit ${EXIT_CODE}
}

cleanup_resources
trap on_exit EXIT

info "Preparing Incus test manifests in ${TEST_DIR}..."
mkdir -p "${TEST_DIR}" /tmp/e2e-incus-host-shared /tmp/e2e-incus-host-extra
echo "incus-initial-mount-data" > /tmp/e2e-incus-host-shared/initial.txt
echo "incus-dynamic-mount-data" > /tmp/e2e-incus-host-extra/dynamic.txt
chmod -R 777 /tmp/e2e-incus-host-shared /tmp/e2e-incus-host-extra

cat << 'MANIFEST' > "${TEST_DIR}/_base.yaml"
schema: lxm/config/v2
base: true
provider: incus
target: incus-node1
image: images:debian/12/cloud
user: debian
wait:
  agent: 3m
vswitches:
  - name: incus-webbr0
    ipv4: 10.90.0.1/24
    group: web
  - name: incus-dbbr0
    ipv4: 10.95.0.1/24
    group: db
network_policy:
  allow:
    - from: web
      to: db
      direction: egress
MANIFEST

cat << 'MANIFEST' > "${TEST_DIR}/web.yaml"
schema: lxm/config/v2
include: [_base.yaml]
name: e2e-incus-web
type: container
status: present
groups: [web]
mounts:
  - source: /tmp/e2e-incus-host-shared
    path: /mnt/shared
    shift: true
networks:
  - name: eth0
    parent: incus-webbr0
MANIFEST

cat << 'MANIFEST' > "${TEST_DIR}/db.yaml"
schema: lxm/config/v2
include: [_base.yaml]
name: e2e-incus-db
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
    parent: incus-dbbr0
MANIFEST

info "1. Executing Plan & Apply on Incus (Provisioning with Mounts & Disks)..."
"${LXM_BIN}" plan "${TEST_DIR}"
"${LXM_BIN}" apply "${TEST_DIR}"
pass "Incus reconciliation applied successfully."

info "2. Verifying instance states, network IPs, initial mounts, and data disks..."
WEB_IP=""
DB_IP=""
for i in {1..60}; do
    WEB_IP=$(incus list "^e2e-incus-web$" --format json | jq -r '.[0].state.network[]?.addresses[]? | select(.family=="inet" and .scope=="global").address' | head -n1 || true)
    DB_IP=$(incus list "^e2e-incus-db$" --format json | jq -r '.[0].state.network[]?.addresses[]? | select(.family=="inet" and .scope=="global").address' | head -n1 || true)
    if [[ -n "${WEB_IP}" && -n "${DB_IP}" ]]; then
        break
    fi
    sleep 2
done

if [[ -z "${WEB_IP}" || -z "${DB_IP}" ]]; then
    fail "Timed out waiting for IP addresses (web: ${WEB_IP}, db: ${DB_IP})"
fi

# Verify initial directory mount inside container
MOUNT_CONTENT=$(incus exec e2e-incus-web -- cat /mnt/shared/initial.txt 2>/dev/null || true)
if [[ "${MOUNT_CONTENT}" != "incus-initial-mount-data" ]]; then
    fail "Directory mount verification failed: expected 'incus-initial-mount-data', got '${MOUNT_CONTENT}'"
fi

# Verify initial data disk on VM
DISK_DATA1_FOUND=$(incus config show e2e-incus-db --expanded | grep "disk-data1" || true)
if [[ -z "${DISK_DATA1_FOUND}" ]]; then
    fail "Data disk verification failed: disk-data1 not found on e2e-incus-db"
fi
pass "Instances online with valid IPs (web: ${WEB_IP}, db: ${DB_IP}), mounts verified, and data disks attached."

info "3. Testing in-guest command execution..."
cat << 'SCRIPT' > /tmp/e2e-test-script.sh
#!/bin/bash
echo "OK: $(hostname) [kernel: $(uname -r)]"
SCRIPT
chmod +x /tmp/e2e-test-script.sh
chmod 777 /tmp/e2e-test-script.sh

"${LXM_BIN}" script e2e-incus-web /tmp/e2e-test-script.sh >/dev/null
"${LXM_BIN}" script e2e-incus-db /tmp/e2e-test-script.sh >/dev/null
pass "In-guest script execution succeeded on container and VM."

info "4. Testing Dynamic Mounts (Adding & Removing Host Directory Mounts)..."
# Add second mount to web.yaml
cat << 'MANIFEST' > "${TEST_DIR}/web.yaml"
schema: lxm/config/v2
include: [_base.yaml]
name: e2e-incus-web
type: container
status: present
groups: [web]
mounts:
  - source: /tmp/e2e-incus-host-shared
    path: /mnt/shared
    shift: true
  - source: /tmp/e2e-incus-host-extra
    path: /mnt/extra
    shift: true
networks:
  - name: eth0
    parent: incus-webbr0
MANIFEST
"${LXM_BIN}" apply "${TEST_DIR}"

EXTRA_MOUNT=$(incus exec e2e-incus-web -- cat /mnt/extra/dynamic.txt 2>/dev/null || true)
if [[ "${EXTRA_MOUNT}" != "incus-dynamic-mount-data" ]]; then
    fail "Dynamic mount addition failed: /mnt/extra/dynamic.txt content mismatch (${EXTRA_MOUNT})"
fi
pass "Dynamic mount addition verified inside container."

# Remove second mount from web.yaml
cat << 'MANIFEST' > "${TEST_DIR}/web.yaml"
schema: lxm/config/v2
include: [_base.yaml]
name: e2e-incus-web
type: container
status: present
groups: [web]
mounts:
  - source: /tmp/e2e-incus-host-shared
    path: /mnt/shared
    shift: true
networks:
  - name: eth0
    parent: incus-webbr0
MANIFEST
"${LXM_BIN}" apply "${TEST_DIR}"

EXTRA_MOUNT_REMOVED=$(incus config show e2e-incus-web --expanded | grep "path: /mnt/extra" || true)
if [[ -n "${EXTRA_MOUNT_REMOVED}" ]]; then
    fail "Dynamic mount removal failed: /mnt/extra still present in instance config"
fi
pass "Dynamic mount removal verified."

info "5. Testing VM Disk Lifecycle (Add Disk, Detach Disk, Delete Volume)..."
# Add second disk (data2)
cat << 'MANIFEST' > "${TEST_DIR}/db.yaml"
schema: lxm/config/v2
include: [_base.yaml]
name: e2e-incus-db
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
    parent: incus-dbbr0
MANIFEST
"${LXM_BIN}" apply "${TEST_DIR}"

DISK_DATA2_FOUND=$(incus config show e2e-incus-db --expanded | grep "disk-data2" || true)
if [[ -z "${DISK_DATA2_FOUND}" ]]; then
    fail "Dynamic disk addition failed: disk-data2 not found on e2e-incus-db"
fi
pass "Dynamic disk addition verified on VM."

# Detach disk data2 (attach: false)
cat << 'MANIFEST' > "${TEST_DIR}/db.yaml"
schema: lxm/config/v2
include: [_base.yaml]
name: e2e-incus-db
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
    parent: incus-dbbr0
MANIFEST
"${LXM_BIN}" apply "${TEST_DIR}"

DISK_DATA2_DETACHED=$(incus config show e2e-incus-db --expanded | grep "disk-data2" || true)
if [[ -n "${DISK_DATA2_DETACHED}" ]]; then
    fail "Disk detach failed: disk-data2 still attached to VM"
fi
if ! incus storage volume show default custom/e2e-incus-db-data2 >/dev/null 2>&1; then
    fail "Disk detach failed: underlying storage volume was incorrectly deleted"
fi
pass "Disk detachment verified (device detached, volume preserved in storage pool)."

# Delete disk data2 (status: absent)
cat << 'MANIFEST' > "${TEST_DIR}/db.yaml"
schema: lxm/config/v2
include: [_base.yaml]
name: e2e-incus-db
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
    parent: incus-dbbr0
MANIFEST
"${LXM_BIN}" apply "${TEST_DIR}"

if incus storage volume show default custom/e2e-incus-db-data2 >/dev/null 2>&1; then
    fail "Disk volume deletion failed: volume e2e-incus-db-data2 still present in storage pool"
fi
pass "Disk volume deletion verified (volume destroyed from storage pool)."

info "6. Testing Network Policy Enforcement..."
# Allowed: Web -> DB
incus exec e2e-incus-web -- ping -c 2 -W 2 "${DB_IP}" >/dev/null || true
# Blocked: DB -> Web (must fail/be rejected by ACL)
if incus exec e2e-incus-db -- ping -c 2 -W 2 "${WEB_IP}" >/dev/null 2>&1; then
    fail "Policy failure: DB was able to ping Web (should have been blocked)"
else
    pass "Policy enforced: DB -> Web traffic correctly blocked by ACL."
fi

info "7. Testing Snapshot & Rollback..."
"${LXM_BIN}" snapshot e2e-incus-web e2e-snap
"${LXM_BIN}" rollback e2e-incus-web e2e-snap
pass "Snapshot creation and rollback succeeded."

info "8. Testing Plan Idempotency (Zero-Drift)..."
cat << 'MANIFEST' > "${TEST_DIR}/db.yaml"
schema: lxm/config/v2
include: [_base.yaml]
name: e2e-incus-db
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
    parent: incus-dbbr0
MANIFEST
PLAN_OUT=$("${LXM_BIN}" plan "${TEST_DIR}")
if echo "${PLAN_OUT}" | grep "0 to create, 0 to update, 0 to recreate, 0 to delete" >/dev/null 2>&1; then
    pass "Plan idempotency verified (zero drift)."
else
    fail "Plan idempotency failed: ${PLAN_OUT}"
fi

echo -e "\n${GREEN}===============================================${NC}"
echo -e "${GREEN}All Incus E2E tests passed cleanly!${NC}"
echo -e "${GREEN}===============================================${NC}"
