#!/usr/bin/env bash
# ==============================================================================
# LXM - Declarative Port Publishing End-to-End Test Suite
# ==============================================================================
# Verifies `ports:` against a live LXD or Incus daemon:
# 1. Container non-NAT create: native proxy device + host-side reachability
# 2. Update (listener move) without instance restart, idempotent re-plan
# 3. list/status output surfaces (text + published_ports JSON)
# 4. Fleet-wide duplicate-listener rejection (exit 3)
# 5. Loopback+NAT rejection (exit 3)
# 6. Unmarked proxy-device collision rejection (no adoption)
# 7. Listener-handoff warning and `-j 1` apply
# 8. Removal deletes the device
# 9. Optional VM DNAT (nat: true) stage: WITH_VM=1
#
# Usage:
#   PROVIDER_CLI=lxc ./simulation/scripts/test-ports-e2e.sh
#   PROVIDER_CLI=incus WITH_VM=1 ./simulation/scripts/test-ports-e2e.sh
# ==============================================================================
set -euo pipefail

GREEN='\033[0;32m'
RED='\033[0;31m'
YELLOW='\033[1;33m'
NC='\033[0m'

pass() { echo -e "${GREEN}[PASS]${NC} $1"; }
fail() { echo -e "${RED}[FAIL]${NC} $1"; exit 1; }
info() { echo -e "${YELLOW}==>${NC} $1"; }
skip() { echo -e "${YELLOW}[SKIP]${NC} $1"; }

LXM_BIN="${LXM_BIN:-$(pwd)/bin/lxm}"
if [[ ! -x "${LXM_BIN}" ]]; then
  if command -v lxm >/dev/null 2>&1; then
    LXM_BIN="$(command -v lxm)"
  fi
fi
[[ -x "${LXM_BIN}" ]] || fail "lxm binary not found (set LXM_BIN)"

PROVIDER_CLI="${PROVIDER_CLI:-}"
if [[ -z "${PROVIDER_CLI}" ]]; then
  if command -v incus >/dev/null 2>&1; then PROVIDER_CLI="incus"; else PROVIDER_CLI="lxc"; fi
fi
command -v "${PROVIDER_CLI}" >/dev/null 2>&1 || fail "provider CLI '${PROVIDER_CLI}' not found"
for tool in jq curl; do
  command -v "${tool}" >/dev/null 2>&1 || fail "'${tool}' is required"
done

PROVIDER_NAME="lxd"
if [[ "${PROVIDER_CLI}" == "incus" ]]; then PROVIDER_NAME="incus"; fi
PARENT_NET=""
for cand in lxdbr0 incusbr0 default; do
  if "${PROVIDER_CLI}" network show "${cand}" >/dev/null 2>&1; then PARENT_NET="${cand}"; break; fi
done
[[ -n "${PARENT_NET}" ]] || fail "no parent bridge (lxdbr0/incusbr0/default) found"

WEB="e2e-ports-web"
WEB2="e2e-ports-web2"
VM="e2e-ports-vm"
TEST_DIR="$(mktemp -d /tmp/e2e_ports_test.XXXXXX)"

choose_free_port() {
  local p
  for _ in $(seq 1 50); do
    p=$((20000 + RANDOM % 20000))
    if ! ss -H -ltn "sport = :${p}" 2>/dev/null | grep -q .; then
      echo "${p}"
      return 0
    fi
  done
  fail "could not find a free host port"
}

HOST_PORT="${HOST_PORT:-$(choose_free_port)}"
HOST_PORT2="$((HOST_PORT + 1))"
LISTEN_IP="127.0.0.1"
WITH_VM="${WITH_VM:-0}"

cleanup_resources() {
  info "Cleaning up port E2E resources..."
  for inst in "${WEB}" "${WEB2}" "${VM}"; do
    "${PROVIDER_CLI}" delete "${inst}" --force >/dev/null 2>&1 || true
  done
  rm -rf "${TEST_DIR}"
}
trap cleanup_resources EXIT

wait_running() {
  local name="$1"
  for _ in $(seq 1 60); do
    if "${PROVIDER_CLI}" list "^${name}$" --format json | jq -e '.[0].status == "Running"' >/dev/null 2>&1; then
      return 0
    fi
    sleep 1
  done
  fail "timed out waiting for ${name} to be Running"
}

wait_exec() {
  local name="$1"
  for _ in $(seq 1 90); do
    if "${PROVIDER_CLI}" exec "${name}" -- true >/dev/null 2>&1; then
      return 0
    fi
    sleep 1
  done
  fail "timed out waiting for exec/agent on ${name}"
}

wait_http() {
  local url="$1" expect="$2" name="$3"
  for _ in $(seq 1 30); do
    if body="$(curl -fsS --max-time 2 "${url}" 2>/dev/null)" && [[ "${body}" == *"${expect}"* ]]; then
      return 0
    fi
    sleep 1
  done
  fail "expected ${name} at ${url} to serve '${expect}'"
}

http_refused() {
  ! curl -fsS --max-time 2 "$1" >/dev/null 2>&1
}

write_base() {
  cat > "${TEST_DIR}/_base.yaml" <<EOF
schema: lxm/config/v2
base: true
provider: ${PROVIDER_NAME}
image: ubuntu:22.04
user: ubuntu
EOF
}

write_web_manifest() {
  local port="${1:-}"
  {
    cat <<EOF
schema: lxm/config/v2
include: [_base.yaml]
name: ${WEB}
type: container
status: present
networks:
  - name: eth0
    parent: ${PARENT_NET}
EOF
    if [[ -n "${port}" ]]; then
      cat <<EOF
ports:
  - name: web
    listen: ${LISTEN_IP}:${port}
    connect: 127.0.0.1:80
EOF
    fi
  } > "${TEST_DIR}/web.yaml"
}

write_web2_manifest() {
  local port="${1:-}"
  {
    cat <<EOF
schema: lxm/config/v2
include: [_base.yaml]
name: ${WEB2}
type: container
status: present
networks:
  - name: eth0
    parent: ${PARENT_NET}
EOF
    if [[ -n "${port}" ]]; then
      cat <<EOF
ports:
  - name: web
    listen: ${LISTEN_IP}:${port}
    connect: 127.0.0.1:80
EOF
    fi
  } > "${TEST_DIR}/web2.yaml"
}

start_http_server() {
  local inst="$1"
  "${PROVIDER_CLI}" exec "${inst}" -- sh -c \
    "mkdir -p /srv/e2e && echo e2e-ok > /srv/e2e/index.html" >/dev/null 2>&1 \
    || fail "could not prepare HTTP document root inside ${inst}"
  # Prefer a transient systemd unit so the server survives the exec session; fall
  # back to nohup for images without systemd.
  if ! "${PROVIDER_CLI}" exec "${inst}" -- systemd-run --collect --unit=e2e-ports-http \
    --working-directory=/srv/e2e python3 -m http.server 80 >/dev/null 2>&1; then
    "${PROVIDER_CLI}" exec "${inst}" -- sh -c \
      "cd /srv/e2e && nohup python3 -m http.server 80 >/dev/null 2>&1 &" >/dev/null 2>&1 \
      || fail "could not start HTTP server inside ${inst}"
  fi
}

info "1. Creating a container with a loopback published port (${LISTEN_IP}:${HOST_PORT})..."
write_base
write_web_manifest "${HOST_PORT}"
"${LXM_BIN}" plan "${TEST_DIR}/web.yaml" >/dev/null
"${LXM_BIN}" apply "${TEST_DIR}/web.yaml"
wait_running "${WEB}"
start_http_server "${WEB}"
wait_http "http://${LISTEN_IP}:${HOST_PORT}/" "e2e-ok" "published port"
"${LXM_BIN}" plan "${TEST_DIR}/web.yaml" --format json | jq -e --arg inst "${WEB}" \
  '.plan.steps[] | select(.container == $inst) | .action == "noop"' >/dev/null \
  || fail "re-plan after create is not a noop (live idempotency failure)"
pass "Container non-NAT proxy device serves host-side traffic; re-plan is a noop."

info "2. Verifying list/status output surfaces..."
"${LXM_BIN}" list --format json | jq -e --arg inst "${WEB}" \
  '.results[] | select(.container == $inst) | .published_ports[] | select(.device == "proxy-web" and .managed == true and .listen == "127.0.0.1:'"${HOST_PORT}"'")' >/dev/null \
  || fail "list JSON missing published_ports for ${WEB}"
"${LXM_BIN}" status "${WEB}" --format json | jq -e \
  '.results[0].published_ports[0] | select(.name == "web" and .proto == "tcp")' >/dev/null \
  || fail "status JSON missing published_ports"
"${LXM_BIN}" list | grep -q -- "127.0.0.1:${HOST_PORT}->127.0.0.1:80" \
  || fail "list text missing PORTS value"
"${LXM_BIN}" status "${WEB}" | grep -q -- "Published Ports:" \
  || fail "status text missing Published Ports block"
pass "list/status text and JSON expose published ports."

info "3. Moving the listener in place (no recreate)..."
write_web_manifest "${HOST_PORT2}"
"${LXM_BIN}" apply "${TEST_DIR}/web.yaml"
wait_http "http://${LISTEN_IP}:${HOST_PORT2}/" "e2e-ok" "moved published port"
http_refused "http://${LISTEN_IP}:${HOST_PORT}/" || fail "old listener still serving after update"
"${PROVIDER_CLI}" list "^${WEB}$" --format json | jq -e '.[0].status == "Running"' >/dev/null \
  || fail "instance was not kept Running across a port-only update"
pass "Listener update applied without recreation; old port released."

info "4. Verifying idempotency..."
"${LXM_BIN}" plan "${TEST_DIR}/web.yaml" --format json | jq -e --arg inst "${WEB}" \
  '.plan.steps[] | select(.container == $inst) | .action == "noop"' >/dev/null \
  || fail "re-plan is not a noop after apply"
pass "Re-plan is a noop (zero drift)."

info "5. Verifying fleet-wide duplicate-listener rejection..."
write_web2_manifest "${HOST_PORT2}"
set +e
dup_out="$("${LXM_BIN}" plan "${TEST_DIR}" 2>&1)"
dup_code=$?
set -e
[[ ${dup_code} -eq 3 ]] || fail "duplicate listener exit code = ${dup_code}, want 3 (${dup_out})"
echo "${dup_out}" | grep -q "published by both" || fail "missing duplicate-listener diagnostic: ${dup_out}"
pass "Duplicate listener rejected with exit 3."

info "6. Verifying loopback+NAT rejection..."
cat > "${TEST_DIR}/bad-nat.yaml" <<EOF
schema: lxm/config/v2
name: e2e-ports-badnat
image: ubuntu:22.04
networks:
  - name: eth0
    parent: ${PARENT_NET}
    ipv4: 10.0.0.5
ports:
  - name: web
    listen: 127.0.0.1:${HOST_PORT}
    connect: 10.0.0.5:80
    nat: true
EOF
set +e
nat_out="$("${LXM_BIN}" plan "${TEST_DIR}/bad-nat.yaml" 2>&1)"
nat_code=$?
set -e
[[ ${nat_code} -eq 3 ]] || fail "loopback+NAT exit code = ${nat_code}, want 3 (${nat_out})"
echo "${nat_out}" | grep -q "cannot listen on a loopback address" || fail "missing loopback+NAT diagnostic: ${nat_out}"
rm -f "${TEST_DIR}/bad-nat.yaml"
pass "Loopback+NAT rejected with exit 3."

info "7. Creating the second instance and verifying unmarked-device collision rejection..."
write_web2_manifest ""
"${LXM_BIN}" apply "${TEST_DIR}/web2.yaml"
wait_running "${WEB2}"
start_http_server "${WEB2}"
"${PROVIDER_CLI}" config device add "${WEB2}" proxy-web proxy \
  listen="tcp:${LISTEN_IP}:${HOST_PORT2}" connect="tcp:127.0.0.1:80" >/dev/null
write_web2_manifest "${HOST_PORT2}"
set +e
unmarked_out="$("${LXM_BIN}" plan "${TEST_DIR}/web2.yaml" 2>&1)"
unmarked_code=$?
set -e
[[ ${unmarked_code} -eq 3 ]] || fail "unmarked collision exit code = ${unmarked_code}, want 3 (${unmarked_out})"
echo "${unmarked_out}" | grep -q "is not lxm-managed" || fail "missing ownership diagnostic: ${unmarked_out}"
"${PROVIDER_CLI}" config device remove "${WEB2}" proxy-web >/dev/null
pass "Unmarked proxy device collision rejected; no adoption."

info "8. Verifying listener-handoff warning and -j 1 apply..."
write_web_manifest ""
write_web2_manifest "${HOST_PORT2}"
handoff_out="$("${LXM_BIN}" plan "${TEST_DIR}" 2>&1)"
echo "${handoff_out}" | grep -q "moves from instance" || fail "missing listener-handoff warning: ${handoff_out}"
"${LXM_BIN}" apply "${TEST_DIR}" -j 1
wait_http "http://${LISTEN_IP}:${HOST_PORT2}/" "e2e-ok" "handoff published port"
if "${PROVIDER_CLI}" config device show "${WEB}" | grep -q "proxy-web"; then
  fail "web still has proxy-web after handoff"
fi
pass "Listener handoff warned and applied successfully with -j 1."

info "9. Verifying removal deletes the device..."
write_web2_manifest ""
"${LXM_BIN}" apply "${TEST_DIR}/web2.yaml"
if "${PROVIDER_CLI}" config device show "${WEB2}" | grep -q "proxy-web"; then
  fail "proxy-web still present after removal"
fi
http_refused "http://${LISTEN_IP}:${HOST_PORT2}/" || fail "removed listener still serving"
pass "Removal deleted the proxy device and closed the port."

if [[ "${WITH_VM}" == "1" ]]; then
  info "10. Verifying VM DNAT (nat: true)..."
  BRIDGE_CIDR="$("${PROVIDER_CLI}" network show "${PARENT_NET}" --format json | jq -r '.config."ipv4.address" // empty')"
  [[ -n "${BRIDGE_CIDR}" ]] || fail "cannot determine ${PARENT_NET} ipv4.address for VM NAT stage"
  GW_IP="${BRIDGE_CIDR%/*}"
  VM_STATIC_IP="${VM_STATIC_IP:-$(echo "${GW_IP}" | awk -F. '{printf "%s.%s.%s.240", $1, $2, $3}')}"
  cat > "${TEST_DIR}/vm.yaml" <<EOF
schema: lxm/config/v2
include: [_base.yaml]
name: ${VM}
type: virtual-machine
status: present
vm:
  boot_mode: uefi-nosecureboot
networks:
  - name: eth0
    parent: ${PARENT_NET}
    ipv4: ${VM_STATIC_IP}
wait:
  agent: 5m
  required: true
ports:
  - name: https
    listen: ${GW_IP}:${HOST_PORT2}
    connect: ${VM_STATIC_IP}:80
    nat: true
EOF
  "${LXM_BIN}" apply "${TEST_DIR}/vm.yaml"
  wait_running "${VM}"
  wait_exec "${VM}"
  start_http_server "${VM}"
  wait_http "http://${GW_IP}:${HOST_PORT2}/" "e2e-ok" "VM NAT published port"
  pass "VM DNAT proxy device serves host-side traffic via nat: true."
else
  skip "VM DNAT stage not run (set WITH_VM=1 to include it; requires a VM-capable host)."
fi

pass "All port publishing E2E checks passed."
