#!/usr/bin/env bash
# ==============================================================================
# LXM - Unix-Domain Socket Proxy End-to-End Test Suite (`sockets:`)
# ==============================================================================
# Verifies issue #17 against a live LXD or Incus daemon:
# 1. bind=instance create: instance-side listener -> host-owned socket, with
#    uid/gid/mode ownership applied inside the instance
# 2. Per-instance isolation: two instances may use the same instance-side
#    listen path while proxying to different host sockets
# 3. Access control: mode 0600 owned by uid 1000 permits the owner and denies
#    another uid; broker absent is a clean client failure; broker restart
#    reconnects (per-connection relay)
# 4. bind=host: host-side listener -> instance-owned socket, host-side DAC
# 5. list/status surfaces (text + published_sockets JSON); idempotent re-plan
# 6. Validation matrix: duplicate host-side path exit 3, duplicate
#    instance-side listen path exit 3, VM rejection exit 3, unmarked device
#    collision exit 3
# 7. Parent directory missing: clean provider error at apply (exit 4)
# 8. Removal deletes the device; the documented stale instance-side socket file
#    remains but no longer relays and is re-used on re-add; bind=host listener
#    cleanup (where the daemon performs it); re-plan after removal is a noop
#
# Usage:
#   PROVIDER_CLI=lxc LXM_IMAGE=ubuntu:22.04 ./simulation/scripts/test-sockets-e2e.sh
#   PROVIDER_CLI=incus LXM_IMAGE=ubuntu:22.04 ./simulation/scripts/test-sockets-e2e.sh
#
# Prerequisites: a running LXD/Incus daemon reachable by the current user, the
# lxm binary (bin/lxm or $LXM_BIN), jq, and python3 on the host. The guest image
# must provide python3.
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
for tool in jq python3; do
  command -v "${tool}" >/dev/null 2>&1 || fail "'${tool}' is required"
done

PROVIDER_NAME="lxd"
if [[ "${PROVIDER_CLI}" == "incus" ]]; then PROVIDER_NAME="incus"; fi
LXM=( "${LXM_BIN}" --provider "${PROVIDER_NAME}" )
LXM_IMAGE="${LXM_IMAGE:-ubuntu:22.04}"

INST_A="lxm-e2e-sock-a"
INST_B="lxm-e2e-sock-b"
INST_C="lxm-e2e-sock-c"
TEST_DIR="$(mktemp -d /tmp/e2e_sockets_test.XXXXXX)"
HOST_UID="$(id -u)"
HOST_GID="$(id -g)"

BROKER_A_PID=""
BROKER_B_PID=""

cleanup_resources() {
  info "Cleaning up socket E2E resources..."
  for pid in "${BROKER_A_PID}" "${BROKER_B_PID}"; do
    if [[ -n "${pid}" ]]; then kill "${pid}" >/dev/null 2>&1 || true; fi
  done
  for inst in "${INST_A}" "${INST_B}" "${INST_C}"; do
    "${PROVIDER_CLI}" delete "${inst}" --force >/dev/null 2>&1 || true
  done
  rm -rf "${TEST_DIR}"
}
trap cleanup_resources EXIT

wait_running() {
  local name="$1"
  for _ in $(seq 1 90); do
    if "${PROVIDER_CLI}" list "^${name}$" --format json | jq -e '.[0].status == "Running"' >/dev/null 2>&1; then
      return 0
    fi
    sleep 1
  done
  fail "timed out waiting for ${name} to be Running"
}

wait_exec() {
  local name="$1"
  for _ in $(seq 1 120); do
    if "${PROVIDER_CLI}" exec "${name}" -- true >/dev/null 2>&1; then
      return 0
    fi
    sleep 1
  done
  fail "timed out waiting for exec on ${name}"
}

# start_host_broker <socket-path> <reply-prefix> -> echoes pid
start_host_broker() {
  local socket_path="$1" prefix="$2" script="${TEST_DIR}/broker.py"
  cat > "${script}" <<PYEOF
import os, socket
p = ${socket_path@Q}
try: os.unlink(p)
except FileNotFoundError: pass
s = socket.socket(socket.AF_UNIX, socket.SOCK_STREAM)
s.bind(p); s.listen(16)
while True:
    c, _ = s.accept()
    try:
        d = c.recv(100)
        c.sendall(${prefix@Q}.encode() + d)
    except OSError:
        pass
    finally:
        c.close()
PYEOF
  python3 "${script}" >"${TEST_DIR}/host-broker.log" 2>&1 &
  echo $!
}

# wait_host_socket <path>
wait_host_socket() {
  local path="$1"
  for _ in $(seq 1 40); do
    [[ -S "${path}" ]] && return 0
    sleep 0.25
  done
  fail "timed out waiting for host socket ${path}"
}

# start_instance_server <instance> <socket-path> <reply-prefix>
start_instance_server() {
  local inst="$1" socket_path="$2" prefix="$3"
  cat > "${TEST_DIR}/instance_server.py" <<PYEOF
import os, socket
p = ${socket_path@Q}
try: os.unlink(p)
except FileNotFoundError: pass
s = socket.socket(socket.AF_UNIX, socket.SOCK_STREAM)
s.bind(p); s.listen(16)
while True:
    c, _ = s.accept()
    try:
        d = c.recv(100)
        c.sendall(${prefix@Q}.encode() + d)
    except OSError:
        pass
    finally:
        c.close()
PYEOF
  "${PROVIDER_CLI}" file push "${TEST_DIR}/instance_server.py" "${inst}${socket_path}" >/dev/null
  "${PROVIDER_CLI}" exec "${inst}" -- sh -c "nohup python3 ${socket_path} >/var/tmp/lxm-e2e-server.log 2>&1 &"
  for _ in $(seq 1 40); do
    if "${PROVIDER_CLI}" exec "${inst}" -- test -S "${socket_path}"; then return 0; fi
    sleep 0.25
  done
  fail "timed out waiting for instance socket ${socket_path} on ${inst}"
}

# guest_connect <instance> <socket-path> [run-as-user]
# Prints the reply; exits non-zero on connection/relay failure.
guest_connect() {
  local inst="$1" socket_path="$2" user="${3:-}"
  cat > "${TEST_DIR}/client.py" <<PYEOF
import socket, sys
s = socket.socket(socket.AF_UNIX, socket.SOCK_STREAM)
s.settimeout(5)
try:
    s.connect(${socket_path@Q})
    s.sendall(b"ping")
    reply = s.recv(100)
except OSError as exc:
    print("CLIENT-ERROR:" + str(exc))
    sys.exit(3)
if not reply:
    print("CLIENT-ERROR:connection closed without reply")
    sys.exit(4)
print(reply.decode())
PYEOF
  local remote_script="/var/tmp/lxm-e2e-client-${RANDOM}-${RANDOM}.py"
  if ! "${PROVIDER_CLI}" file push "${TEST_DIR}/client.py" "${inst}${remote_script}" >/dev/null 2>&1; then
    echo "SCRIPT-ERROR: could not push client script to ${inst}"
    return 5
  fi
  if [[ -n "${user}" ]]; then
    "${PROVIDER_CLI}" exec "${inst}" -- su -s /bin/sh "${user}" -c "python3 ${remote_script}"
  else
    "${PROVIDER_CLI}" exec "${inst}" -- python3 "${remote_script}"
  fi
}

host_connect() {
  local socket_path="$1"
  python3 - "${socket_path}" <<'PYEOF'
import socket, sys
s = socket.socket(socket.AF_UNIX, socket.SOCK_STREAM)
s.settimeout(5)
try:
    s.connect(sys.argv[1])
    s.sendall(b"ping")
    reply = s.recv(100)
except OSError as exc:
    print("CLIENT-ERROR:" + str(exc))
    sys.exit(3)
if not reply:
    print("CLIENT-ERROR:connection closed without reply")
    sys.exit(4)
print(reply.decode())
PYEOF
}

write_instance_a() {
  local sockets_block="$1"
  cat > "${TEST_DIR}/a.yaml" <<EOF
schema: lxm/config/v2
name: ${INST_A}
image: ${LXM_IMAGE}
type: container
EOF
  if [[ -n "${sockets_block}" ]]; then
    printf 'sockets:\n%s\n' "${sockets_block}" >> "${TEST_DIR}/a.yaml"
  fi
}

write_instance_b() {
  local sockets_block="$1"
  cat > "${TEST_DIR}/b.yaml" <<EOF
schema: lxm/config/v2
name: ${INST_B}
image: ${LXM_IMAGE}
type: container
EOF
  if [[ -n "${sockets_block}" ]]; then
    printf 'sockets:\n%s\n' "${sockets_block}" >> "${TEST_DIR}/b.yaml"
  fi
}

A_HOST_BROKER="${TEST_DIR}/a-host-broker.sock"
B_HOST_BROKER="${TEST_DIR}/b-host-broker.sock"
SHARED_INSTANCE_SOCKET="/var/tmp/lxm-e2e-broker.sock"

info "1. Creating instance A with a bind=instance socket (uid 1000, mode 0600)..."
BROKER_A_PID="$(start_host_broker "${A_HOST_BROKER}" "HOST-A:")"
wait_host_socket "${A_HOST_BROKER}"
write_instance_a "  - name: broker
    listen: unix:${SHARED_INSTANCE_SOCKET}
    connect: unix:${A_HOST_BROKER}
    bind: instance
    uid: 1000
    gid: 1000
    mode: \"0600\""
"${LXM[@]}" plan "${TEST_DIR}/a.yaml" >/dev/null
"${LXM[@]}" apply "${TEST_DIR}/a.yaml" >/dev/null
wait_running "${INST_A}"
wait_exec "${INST_A}"
"${PROVIDER_CLI}" exec "${INST_A}" -- sh -c "test -S '${SHARED_INSTANCE_SOCKET}'" \
  || fail "instance-side listening socket ${SHARED_INSTANCE_SOCKET} was not created inside ${INST_A}"
owner="$("${PROVIDER_CLI}" exec "${INST_A}" -- stat -c '%u:%g %a' "${SHARED_INSTANCE_SOCKET}")"
[[ "${owner}" == "1000:1000 600" ]] || fail "instance-side socket ownership/mode = ${owner}, want '1000:1000 600'"
reply="$(guest_connect "${INST_A}" "${SHARED_INSTANCE_SOCKET}" ubuntu)" || fail "instance->host proxying failed: ${reply}"
[[ "${reply}" == "HOST-A:ping" ]] || fail "unexpected proxied reply: ${reply}"
pass "bind=instance socket forwards instance traffic to the host service with uid/gid/mode applied."

info "2. Verifying access control (owner allowed, other uid denied)..."
set +e
denied="$(guest_connect "${INST_A}" "${SHARED_INSTANCE_SOCKET}" nobody 2>&1)"
denied_code=$?
set -e
[[ ${denied_code} -ne 0 ]] || fail "a different uid was able to use a 0600 socket: ${denied}"
case "${denied}" in
  SCRIPT-ERROR:*) fail "client harness error, not an access denial: ${denied}" ;;
  *"CLIENT-ERROR:"*"permission denied"*|*"CLIENT-ERROR:"*"Permission denied"*|*"CLIENT-ERROR:"*"Errno 13"*) : ;;
  *) fail "unexpected denial result (exit ${denied_code}): ${denied}" ;;
esac
pass "0600 owned by uid 1000 denies another uid."

info "3. Verifying broker-absent failure and broker-restart reconnection..."
kill "${BROKER_A_PID}" >/dev/null 2>&1 || true
sleep 0.5
set +e
absent_reply="$(guest_connect "${INST_A}" "${SHARED_INSTANCE_SOCKET}" ubuntu 2>&1)"
absent_code=$?
set -e
[[ ${absent_code} -ne 0 ]] || fail "broker-absent connection unexpectedly succeeded: ${absent_reply}"
case "${absent_reply}" in
  SCRIPT-ERROR:*) fail "client harness error during broker-absent test: ${absent_reply}" ;;
esac
BROKER_A_PID="$(start_host_broker "${A_HOST_BROKER}" "HOST-A:")"
wait_host_socket "${A_HOST_BROKER}"
sleep 0.5
reply="$(guest_connect "${INST_A}" "${SHARED_INSTANCE_SOCKET}" ubuntu)" || fail "reconnection after broker restart failed: ${reply}"
[[ "${reply}" == "HOST-A:ping" ]] || fail "unexpected reconnect reply: ${reply}"
pass "Broker absent is a clean client failure; the proxy reconnects after a broker restart."

info "4. Verifying idempotent re-plan..."
"${LXM[@]}" plan "${TEST_DIR}/a.yaml" --format json | jq -e --arg inst "${INST_A}" \
  '.plan.steps[] | select(.container == $inst) | .action == "noop"' >/dev/null \
  || fail "re-plan after apply is not a noop (live idempotency failure)"
pass "Re-plan is a noop (zero drift)."

info "5. Verifying list/status output surfaces..."
"${LXM[@]}" list --format json | jq -e --arg inst "${INST_A}" \
  '.results[] | select(.container == $inst) | .published_sockets[]
   | select(.device == "socket-broker" and .managed == true and .bind == "instance"
            and .mode == "0600" and .uid == 1000 and .gid == 1000)' >/dev/null \
  || fail "list JSON missing published_sockets for ${INST_A}"
"${LXM[@]}" status "${INST_A}" --format json | jq -e \
  '.results[0].published_sockets[0] | select(.name == "broker" and .listen == "unix:'"${SHARED_INSTANCE_SOCKET}"'")' >/dev/null \
  || fail "status JSON missing published_sockets"
"${LXM[@]}" list | grep -q -- "SOCKETS" || fail "list text missing SOCKETS column"
"${LXM[@]}" list | grep -q -- "unix:${A_HOST_BROKER}" || fail "list text missing socket value"
"${LXM[@]}" status "${INST_A}" | grep -q -- "Unix Sockets:" || fail "status text missing Unix Sockets block"
pass "list/status text and JSON expose socket proxies."

info "6. Verifying per-instance isolation (same instance-side path, different host targets)..."
BROKER_B_PID="$(start_host_broker "${B_HOST_BROKER}" "HOST-B:")"
wait_host_socket "${B_HOST_BROKER}"
write_instance_b "  - name: broker
    listen: unix:${SHARED_INSTANCE_SOCKET}
    connect: unix:${B_HOST_BROKER}
    bind: instance"
"${LXM[@]}" apply "${TEST_DIR}/b.yaml" >/dev/null
wait_running "${INST_B}"
wait_exec "${INST_B}"
reply_a="$(guest_connect "${INST_A}" "${SHARED_INSTANCE_SOCKET}" ubuntu)" || fail "instance A isolation connect failed: ${reply_a}"
reply_b="$(guest_connect "${INST_B}" "${SHARED_INSTANCE_SOCKET}")" || fail "instance B isolation connect failed: ${reply_b}"
[[ "${reply_a}" == "HOST-A:ping" ]] || fail "instance A reached the wrong host target: ${reply_a}"
[[ "${reply_b}" == "HOST-B:ping" ]] || fail "instance B reached the wrong host target: ${reply_b}"
pass "Identical instance-side paths are isolated per instance and reach distinct host sockets."

info "7. Verifying bind=host direction (host listener -> instance service)..."
start_instance_server "${INST_B}" "/var/tmp/lxm-e2e-agent.sock" "INSTANCE-B:"
B_HOST_LISTENER="${TEST_DIR}/b-host-listener.sock"
write_instance_b "  - name: agent
    listen: unix:${B_HOST_LISTENER}
    connect: unix:/var/tmp/lxm-e2e-agent.sock
    bind: host
    uid: ${HOST_UID}
    gid: ${HOST_GID}
    mode: \"0600\""
"${LXM[@]}" apply "${TEST_DIR}/b.yaml" >/dev/null
wait_host_socket "${B_HOST_LISTENER}"
host_owner="$(stat -c '%u:%g %a' "${B_HOST_LISTENER}")"
[[ "${host_owner}" == "${HOST_UID}:${HOST_GID} 600" ]] || fail "host-side socket ownership/mode = ${host_owner}, want '${HOST_UID}:${HOST_GID} 600'"
"${PROVIDER_CLI}" config device show "${INST_B}" | grep -q "bind: host" || fail "bind: host missing from device config"
reply="$(host_connect "${B_HOST_LISTENER}")" || fail "bind=host proxying failed: ${reply}"
[[ "${reply}" == "INSTANCE-B:ping" ]] || fail "unexpected bind=host reply: ${reply}"
pass "bind=host forwards host-local traffic to the instance service with host-side DAC."

info "8. Verifying parent-directory failure is a clean provider error..."
cat > "${TEST_DIR}/c.yaml" <<EOF
schema: lxm/config/v2
name: ${INST_C}
image: ${LXM_IMAGE}
type: container
sockets:
  - name: broker
    listen: unix:/nonexistent-lxm-e2e/broker.sock
    connect: unix:${A_HOST_BROKER}
    bind: instance
EOF
set +e
missing_out="$("${LXM[@]}" apply "${TEST_DIR}/c.yaml" 2>&1)"
missing_code=$?
set -e
[[ ${missing_code} -eq 4 ]] || fail "missing-parent apply exit code = ${missing_code}, want 4 (${missing_out})"
echo "${missing_out}" | grep -q -E "no such file or directory|Failed listening" \
  || fail "missing-parent diagnostic lacks daemon bind failure: ${missing_out}"
rm -f "${TEST_DIR}/c.yaml"
"${PROVIDER_CLI}" delete "${INST_C}" --force >/dev/null 2>&1 || true
pass "A missing instance-side parent directory fails cleanly at apply (exit 4)."

info "9. Verifying the validation matrix (exit 3)..."
write_instance_a ""
write_instance_b "" # drop both live sockets first so the fleet is clean
"${LXM[@]}" apply "${TEST_DIR}/a.yaml" >/dev/null
"${LXM[@]}" apply "${TEST_DIR}/b.yaml" >/dev/null

# 9a. duplicate host-side path across instances
write_instance_a "  - {name: broker, listen: \"unix:/var/tmp/lxm-e2e-a.sock\", connect: \"unix:${A_HOST_BROKER}\", bind: instance}"
write_instance_b "  - {name: broker, listen: \"unix:/var/tmp/lxm-e2e-b.sock\", connect: \"unix:${A_HOST_BROKER}\", bind: instance}"
set +e
dup_out="$("${LXM[@]}" plan "${TEST_DIR}" 2>&1)"
dup_code=$?
set -e
[[ ${dup_code} -eq 3 ]] || fail "duplicate host-side path exit code = ${dup_code}, want 3 (${dup_out})"
echo "${dup_out}" | grep -q "host-side unix socket path" || fail "missing duplicate host-side path diagnostic: ${dup_out}"

# 9b. duplicate instance-side listen path within one instance
write_instance_b "  - {name: one, listen: \"unix:/var/tmp/lxm-e2e-shared.sock\", connect: \"unix:${B_HOST_BROKER}\", bind: instance}
  - {name: two, listen: \"unix:/var/tmp/lxm-e2e-shared.sock\", connect: \"unix:${TEST_DIR}/other.sock\", bind: instance}"
set +e
listen_out="$("${LXM[@]}" plan "${TEST_DIR}/b.yaml" 2>&1)"
listen_code=$?
set -e
[[ ${listen_code} -eq 3 ]] || fail "duplicate instance-side listen exit code = ${listen_code}, want 3 (${listen_out})"
echo "${listen_out}" | grep -q "instance-side listen socket" || fail "missing duplicate instance-side listen diagnostic: ${listen_out}"

# 9c. virtual machines cannot use unix proxies
cat > "${TEST_DIR}/vm.yaml" <<EOF
schema: lxm/config/v2
name: lxm-e2e-sock-vm
type: virtual-machine
image: ${LXM_IMAGE}
vm: {boot_mode: uefi-nosecureboot}
sockets:
  - {name: broker, listen: "unix:/var/tmp/x.sock", connect: "unix:/var/tmp/y.sock"}
EOF
set +e
vm_out="$("${LXM[@]}" plan "${TEST_DIR}/vm.yaml" 2>&1)"
vm_code=$?
set -e
[[ ${vm_code} -eq 3 ]] || fail "VM socket exit code = ${vm_code}, want 3 (${vm_out})"
echo "${vm_out}" | grep -q "container-only" || fail "missing container-only diagnostic: ${vm_out}"
rm -f "${TEST_DIR}/vm.yaml"
pass "Validation matrix rejects duplicate paths, duplicate instance listeners and VM sockets with exit 3."

info "10. Verifying unmarked device collision is never adopted..."
write_instance_a "  - {name: broker, listen: \"unix:/var/tmp/lxm-e2e-a.sock\", connect: \"unix:${A_HOST_BROKER}\", bind: instance}"
"${PROVIDER_CLI}" config device add "${INST_A}" socket-broker proxy \
  bind=instance listen="unix:/var/tmp/lxm-e2e-a.sock" connect="unix:${A_HOST_BROKER}" >/dev/null
set +e
unmarked_out="$("${LXM[@]}" plan "${TEST_DIR}/a.yaml" 2>&1)"
unmarked_code=$?
set -e
[[ ${unmarked_code} -eq 3 ]] || fail "unmarked collision exit code = ${unmarked_code}, want 3 (${unmarked_out})"
echo "${unmarked_out}" | grep -q "is not owned by the sockets namespace" || fail "missing ownership diagnostic: ${unmarked_out}"
"${PROVIDER_CLI}" config device remove "${INST_A}" socket-broker >/dev/null
pass "Unmarked device collision rejected; no adoption."

info "11. Verifying removal deletes the device and retires listeners..."
write_instance_a ""
write_instance_b ""
"${LXM[@]}" apply "${TEST_DIR}" >/dev/null
if "${PROVIDER_CLI}" config device show "${INST_A}" | grep -q "socket-"; then
  fail "socket device still present on ${INST_A} after removal"
fi
if "${PROVIDER_CLI}" config device show "${INST_B}" | grep -q "socket-"; then
  fail "socket device still present on ${INST_B} after removal"
fi
if [[ -S "${B_HOST_LISTENER}" ]]; then
  echo "note: host listener ${B_HOST_LISTENER} remains (daemon-specific cleanup); harmless stale socket"
fi
# Documented daemon limitation (SOCKETS-SPEC §7): removing the device retires
# the relay but the daemons leave the bind=instance socket file inside the
# instance. The stale file is harmless (nothing listens on it) and is
# unlinked/replaced the next time a device starts on that path. Pin the actual
# behaviour rather than the issue's unachievable "removes the socket" wording.
if ! "${PROVIDER_CLI}" exec "${INST_A}" -- test -S "${SHARED_INSTANCE_SOCKET}"; then
  fail "expected the documented stale instance-side socket ${SHARED_INSTANCE_SOCKET} to remain after device removal"
fi
set +e
stale_reply="$(guest_connect "${INST_A}" "${SHARED_INSTANCE_SOCKET}" ubuntu 2>&1)"
stale_code=$?
set -e
[[ ${stale_code} -ne 0 ]] || fail "stale socket without a relay unexpectedly answered: ${stale_reply}"
case "${stale_reply}" in
  SCRIPT-ERROR:*) fail "client harness error during stale-socket check: ${stale_reply}" ;;
esac

# Re-adding a socket on the stale path replaces the file and relays again.
write_instance_a "  - {name: broker, listen: \"unix:${SHARED_INSTANCE_SOCKET}\", connect: \"unix:${A_HOST_BROKER}\", bind: instance, uid: 1000, gid: 1000, mode: \"0600\"}"
"${LXM[@]}" apply "${TEST_DIR}/a.yaml" >/dev/null
reply="$(guest_connect "${INST_A}" "${SHARED_INSTANCE_SOCKET}" ubuntu)" || fail "re-added socket on the stale path did not work: ${reply}"
[[ "${reply}" == "HOST-A:ping" ]] || fail "unexpected re-add reply: ${reply}"
write_instance_a ""
"${LXM[@]}" apply "${TEST_DIR}/a.yaml" >/dev/null

for inst in "${INST_A}" "${INST_B}"; do
  "${LXM[@]}" plan "${TEST_DIR}/${inst##*-}.yaml" --format json | jq -e --arg inst "${inst}" \
    '.plan.steps[] | select(.container == $inst) | .action == "noop"' >/dev/null \
    || fail "re-plan after removal is not a noop for ${inst}"
done
pass "Removal deletes socket devices; the stale instance-side file is documented and re-used on re-add; re-plan is a noop."

pass "All Unix-domain socket proxy E2E checks passed."
