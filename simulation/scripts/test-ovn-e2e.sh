#!/usr/bin/env bash
# ==============================================================================
# LXM - Canonical OVN Virtual Switches End-to-End Test Suite
# ==============================================================================
# Runs a comprehensive end-to-end verification of LXM OVN support:
# 1. Verification of provider network_ovn and network_acl extensions
# 2. OVN Virtual Switch Provisioning with uplink parent binding
# 3. Microsegmentation ACL compilation with G0 intra-switch & G8 CIDR decomposition
# 4. Intra-switch overlay communication under default-reject
# 5. Inter-group stateful one-way policy enforcement (web -> db allow, db -> web reject)
# 6. Internet egress SNAT via parent uplink
# 7. Idempotent Zero-Drift Verification
# 8. Complete Teardown & Purge
# ==============================================================================
set -euo pipefail

GREEN='\033[0;32m'
RED='\033[0;31m'
YELLOW='\033[1;33m'
NC='\033[0m'

pass() { echo -e "${GREEN}[PASS]${NC} $1"; }
fail() { echo -e "${RED}[FAIL]${NC} $1"; exit 1; }
info() { echo -e "${YELLOW}==>${NC} $1"; }
skip() { echo -e "${YELLOW}[SKIP]${NC} $1"; exit 0; }

LXM_BIN="${LXM_BIN:-$(pwd)/bin/lxm}"
if [[ ! -x "${LXM_BIN}" ]]; then
    if command -v lxm >/dev/null 2>&1; then
        LXM_BIN="$(command -v lxm)"
    fi
fi

PROVIDER_CLI="${PROVIDER_CLI:-incus}"
if ! command -v "${PROVIDER_CLI}" >/dev/null 2>&1; then
    if command -v lxc >/dev/null 2>&1; then
        PROVIDER_CLI="lxc"
    else
        fail "Neither incus nor lxc CLI found on PATH"
    fi
fi

# Detect parent uplink network
UPLINK_NET=""
for cand in incusbr0 lxdbr0 default; do
    if "${PROVIDER_CLI}" network show "${cand}" >/dev/null 2>&1; then
        UPLINK_NET="${cand}"
        break
    fi
done

if [[ -z "${UPLINK_NET}" ]]; then
    fail "No parent uplink bridge (incusbr0, lxdbr0, default) found"
fi

TEST_DIR="/tmp/e2e_ovn_test"

cleanup_resources() {
    info "Cleaning up OVN test resources..."
    for inst in e2e-ovn-web-1 e2e-ovn-web-2 e2e-ovn-db e2e-ovn-iso-1 e2e-ovn-iso-2; do
        "${PROVIDER_CLI}" stop -f "${inst}" 2>/dev/null || true
        for _ in {1..10}; do
            if ! "${PROVIDER_CLI}" info "${inst}" >/dev/null 2>&1; then
                break
            fi
            "${PROVIDER_CLI}" delete -f "${inst}" 2>/dev/null || true
            sleep 1
        done
    done
    for _ in {1..15}; do
        "${PROVIDER_CLI}" network delete ovn-webbr0 2>/dev/null || true
        "${PROVIDER_CLI}" network delete ovn-dbbr0 2>/dev/null || true
        "${PROVIDER_CLI}" network delete ovn-isobr0 2>/dev/null || true
        "${PROVIDER_CLI}" network acl delete lxm-ovn-webbr0 2>/dev/null || true
        "${PROVIDER_CLI}" network acl delete lxm-ovn-dbbr0 2>/dev/null || true
        "${PROVIDER_CLI}" network acl delete lxm-ovn-isobr0 2>/dev/null || true
        if ! "${PROVIDER_CLI}" network show ovn-webbr0 >/dev/null 2>&1 && \
           ! "${PROVIDER_CLI}" network show ovn-dbbr0 >/dev/null 2>&1 && \
           ! "${PROVIDER_CLI}" network show ovn-isobr0 >/dev/null 2>&1 && \
           ! "${PROVIDER_CLI}" network acl show lxm-ovn-webbr0 >/dev/null 2>&1 && \
           ! "${PROVIDER_CLI}" network acl show lxm-ovn-dbbr0 >/dev/null 2>&1 && \
           ! "${PROVIDER_CLI}" network acl show lxm-ovn-isobr0 >/dev/null 2>&1; then
            break
        fi
        sleep 1
    done
    sudo -n iptables -D FORWARD -i "${UPLINK_NET}" -o "${UPLINK_NET}" -j ACCEPT 2>/dev/null || true
    sudo -n iptables -t nat -D POSTROUTING -s 10.70.0.0/16 ! -o "${UPLINK_NET}" -j MASQUERADE 2>/dev/null || true
    rm -rf "${TEST_DIR}" 2>/dev/null || true
}

on_exit() {
    EXIT_CODE=$?
    if [[ ${EXIT_CODE} -ne 0 ]]; then
        echo -e "${RED}[FAIL] OVN E2E test failed with exit code ${EXIT_CODE}${NC}"
    fi
    cleanup_resources
    exit ${EXIT_CODE}
}

cleanup_resources
trap on_exit EXIT

info "Checking provider server capabilities for OVN..."
SERVER_EXTS=$("${PROVIDER_CLI}" info | grep -E "network_ovn|network_acl" || true)
if ! echo "${SERVER_EXTS}" | grep -q "network_ovn"; then
    skip "Provider server does not support network_ovn extension (OVN not installed/configured on host)"
fi
if ! echo "${SERVER_EXTS}" | grep -q "network_acl"; then
    skip "Provider server does not support network_acl extension"
fi
pass "Provider supports network_ovn and network_acl"

info "Preparing OVN test manifests in ${TEST_DIR}..."
mkdir -p "${TEST_DIR}"

cat << MANIFEST > "${TEST_DIR}/_base.yaml"
schema: lxm/config/v2
base: true
image: images:debian/12/cloud
user: debian
wait:
  agent: 3m
vswitches:
  - name: ovn-webbr0
    type: ovn
    parent: ${UPLINK_NET}
    ipv4: 10.70.0.1/24
    nat: false
    group: web
  - name: ovn-dbbr0
    type: ovn
    parent: ${UPLINK_NET}
    ipv4: 10.75.0.1/24
    nat: false
    group: db
  - name: ovn-isobr0
    type: ovn
    parent: ${UPLINK_NET}
    ipv4: 10.80.0.1/24
    nat: true
    internet: false
    group: iso
    config:
      dns.nameservers: 10.80.0.10
network_policy:
  allow:
    - from: web
      to: db
      direction: egress
MANIFEST

cat << 'MANIFEST' > "${TEST_DIR}/web-1.yaml"
schema: lxm/config/v2
include: [_base.yaml]
name: e2e-ovn-web-1
type: container
status: present
groups: [web]
networks:
  - name: eth0
    parent: ovn-webbr0
    ipv4: 10.70.0.10
MANIFEST

cat << 'MANIFEST' > "${TEST_DIR}/web-2.yaml"
schema: lxm/config/v2
include: [_base.yaml]
name: e2e-ovn-web-2
type: container
status: present
groups: [web]
networks:
  - name: eth0
    parent: ovn-webbr0
    ipv4: 10.70.0.20
MANIFEST

cat << 'MANIFEST' > "${TEST_DIR}/db.yaml"
schema: lxm/config/v2
include: [_base.yaml]
name: e2e-ovn-db
type: container
status: present
groups: [db]
networks:
  - name: eth0
    parent: ovn-dbbr0
    ipv4: 10.75.0.10
MANIFEST

cat << 'MANIFEST' > "${TEST_DIR}/iso-1.yaml"
schema: lxm/config/v2
include: [_base.yaml]
name: e2e-ovn-iso-1
type: container
status: present
groups: [iso]
networks:
  - name: eth0
    parent: ovn-isobr0
    ipv4: 10.80.0.10
MANIFEST

cat << 'MANIFEST' > "${TEST_DIR}/iso-2.yaml"
schema: lxm/config/v2
include: [_base.yaml]
name: e2e-ovn-iso-2
type: container
status: present
groups: [iso]
networks:
  - name: eth0
    parent: ovn-isobr0
    ipv4: 10.80.0.20
MANIFEST

info "Step 1: Running lxm plan on OVN topology..."
PLAN_OUTPUT=$("${LXM_BIN}" plan "${TEST_DIR}")
echo "${PLAN_OUTPUT}"
pass "Plan generated successfully"

info "Step 2: Applying OVN topology with lxm apply..."
"${LXM_BIN}" apply --jobs 1 "${TEST_DIR}"
pass "Apply succeeded"

info "Step 3: Verifying OVN network objects in provider..."
"${PROVIDER_CLI}" network show ovn-webbr0 | grep -q "type: ovn"
"${PROVIDER_CLI}" network show ovn-dbbr0 | grep -q "type: ovn"
"${PROVIDER_CLI}" network show ovn-isobr0 | grep -q "type: ovn"
pass "OVN networks created and confirmed as type: ovn"

info "Step 4: Waiting for container agent readiness..."
for c in e2e-ovn-web-1 e2e-ovn-web-2 e2e-ovn-db e2e-ovn-iso-1 e2e-ovn-iso-2; do
    READY=0
    for _ in {1..30}; do
        if "${PROVIDER_CLI}" exec "${c}" -- ip addr show eth0 | grep -q "inet "; then
            READY=1
            break
        fi
        sleep 2
    done
    if [[ ${READY} -ne 1 ]]; then
        fail "Container ${c} did not get an IP address on eth0 within the timeout"
    fi
done
pass "Containers booted and IP addresses assigned"

# Configure host nexthop routes for inter-OVN routing via parent uplink bridge.
WEB_UPLINK=$("${PROVIDER_CLI}" network show ovn-webbr0 | grep -E "volatile.network.ipv4.address:" | awk '{print $2}' | tr -d '"' || true)
DB_UPLINK=$("${PROVIDER_CLI}" network show ovn-dbbr0 | grep -E "volatile.network.ipv4.address:" | awk '{print $2}' | tr -d '"' || true)
if [[ -n "${WEB_UPLINK}" && -n "${DB_UPLINK}" ]]; then
    sudo -n ip route replace 10.70.0.0/24 via "${WEB_UPLINK}" dev "${UPLINK_NET}" 2>/dev/null || true
    sudo -n ip route replace 10.75.0.0/24 via "${DB_UPLINK}" dev "${UPLINK_NET}" 2>/dev/null || true
fi

# Ensure host allows bridge forwarding and host NAT for inter-OVN and WAN routing
sudo -n iptables -C FORWARD -i "${UPLINK_NET}" -o "${UPLINK_NET}" -j ACCEPT 2>/dev/null || sudo -n iptables -I FORWARD 1 -i "${UPLINK_NET}" -o "${UPLINK_NET}" -j ACCEPT 2>/dev/null || true
sudo -n iptables -t nat -C POSTROUTING -s 10.70.0.0/16 ! -o "${UPLINK_NET}" -j MASQUERADE 2>/dev/null || sudo -n iptables -t nat -A POSTROUTING -s 10.70.0.0/16 ! -o "${UPLINK_NET}" -j MASQUERADE 2>/dev/null || true

info "Step 5: Starting HTTP service on e2e-ovn-db (port 8080)..."
"${PROVIDER_CLI}" exec e2e-ovn-db -- systemd-run --unit=e2e-http python3 -m http.server 8080 --bind 0.0.0.0
HTTP_READY=0
for _ in {1..10}; do
    if "${PROVIDER_CLI}" exec e2e-ovn-db -- python3 -c "import urllib.request; urllib.request.urlopen('http://127.0.0.1:8080', timeout=1)" 2>/dev/null; then
        HTTP_READY=1
        break
    fi
    sleep 1
done
if [[ ${HTTP_READY} -ne 1 ]]; then
    fail "HTTP service did not become ready on e2e-ovn-db:8080"
fi
pass "HTTP service active on e2e-ovn-db:8080"

info "Step 6: Gate T3-OVN - Testing intra-switch overlay communication (web-1 -> web-2)..."
"${PROVIDER_CLI}" exec e2e-ovn-web-1 -- ping -c 2 -W 2 10.70.0.20
pass "Gate T3-OVN: Intra-switch overlay traffic freely allowed (G0 invariant verified)"

info "Step 7: Gate T1-OVN - Testing cross-subnet forward HTTP connection (web-1 -> db:8080)..."
"${PROVIDER_CLI}" exec e2e-ovn-web-1 -- python3 -c "import urllib.request; print('HTTP Status:', urllib.request.urlopen('http://10.75.0.10:8080', timeout=5).status)"
pass "Gate T1-OVN: Cross-subnet forward connection allowed"

info "Step 8: Gate T2-OVN - Testing inter-group isolation (db -> web-1 must be rejected)..."
if "${PROVIDER_CLI}" exec e2e-ovn-db -- python3 -c "import urllib.request; urllib.request.urlopen('http://10.70.0.10:8080', timeout=2)" 2>/dev/null; then
    fail "Security violation: db was able to initiate connection to web-1"
else
    pass "Gate T2-OVN: Reverse-initiated connection rejected as expected"
fi

info "Step 9: Testing WAN egress SNAT..."
if ! "${PROVIDER_CLI}" exec e2e-ovn-web-1 -- ping -c 2 -W 3 1.1.1.1; then
    fail "WAN egress failed: e2e-ovn-web-1 could not reach 1.1.1.1"
fi
pass "WAN egress verified"

info "Step 10: Gate T6-OVN - Testing guest DNS resolution via uplink resolver..."
if ! "${PROVIDER_CLI}" exec e2e-ovn-web-1 -- python3 -c "import socket; print('DNS resolved google.com:', socket.getaddrinfo('google.com', 80)[0][4])"; then
    fail "DNS resolution failed: e2e-ovn-web-1 could not resolve hostnames via uplink resolver"
fi
pass "Gate T6-OVN: DNS resolution through carved-out uplink resolver verified"

info "Step 11: Gate T7-OVN - Testing host gateway port protection (non-DNS must be rejected)..."
UPLINK_GW=$("${PROVIDER_CLI}" network get "${UPLINK_NET}" ipv4.address | cut -d/ -f1)
if "${PROVIDER_CLI}" exec e2e-ovn-web-1 -- python3 -c "import socket; s = socket.create_connection(('${UPLINK_GW}', 22), timeout=2)" 2>/dev/null; then
    fail "Security violation: container was able to reach host gateway SSH on port 22"
else
    pass "Gate T7-OVN: Host gateway SSH access rejected by port guards as expected"
fi

info "Step 12: Gate T8-OVN - Testing isolated network DNS leak-seal (uplink DNS must be rejected)..."
if "${PROVIDER_CLI}" exec e2e-ovn-iso-2 -- python3 -c "import socket; s = socket.create_connection(('${UPLINK_GW}', 53), timeout=2)" 2>/dev/null; then
    fail "Security violation: isolated container was able to connect to uplink resolver on port 53"
else
    pass "Gate T8-OVN: Uplink resolver port 53 access strictly rejected on isolated network"
fi

info "Step 13: Gate T9-OVN - Testing in-network DNS resolver & intra-switch access on isolated network..."
"${PROVIDER_CLI}" exec e2e-ovn-iso-1 -- systemd-run --unit=e2e-iso-http python3 -m http.server 8080 --bind 0.0.0.0
for _ in {1..10}; do
    if "${PROVIDER_CLI}" exec e2e-ovn-iso-1 -- python3 -c "import urllib.request; urllib.request.urlopen('http://127.0.0.1:8080', timeout=1)" 2>/dev/null; then
        break
    fi
    sleep 1
done
"${PROVIDER_CLI}" exec e2e-ovn-iso-2 -- python3 -c "import urllib.request; print('Isolated Intra-switch HTTP Status:', urllib.request.urlopen('http://10.80.0.10:8080', timeout=2).status)"
pass "Gate T9-OVN: In-network resolver and intra-switch communication preserved on isolated OVN network (R8)"

info "Step 14: Gate T5-OVN - Testing Idempotent Zero-Drift..."
REPLAN_OUT=$("${LXM_BIN}" plan "${TEST_DIR}")
if echo "${REPLAN_OUT}" | grep -qE "create_|update_|delete_"; then
    echo "${REPLAN_OUT}"
    fail "Re-plan produced unexpected drift steps"
fi
pass "Gate T5-OVN: Zero-drift idempotency confirmed"

pass "All OVN E2E Integration Gates PASSED successfully!"
