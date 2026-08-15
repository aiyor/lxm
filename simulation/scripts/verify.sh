#!/bin/bash
set -uo pipefail
# Runs INSIDE the VM as root (invoked from the host: `lxm run lxm-lab -- simulation/scripts/verify.sh`).
# Probes the inner fleet with the inner `lxc` client.
ip() { lxc exec "$1" -- hostname -I 2>/dev/null | awk '{print $1}'; }
WEB_A=$(ip web-a); WEB_B=$(ip web-b); APP_A=$(ip app-a); DB_A=$(ip db-a); SAND=$(ip sandbox-a)
echo "web-a=$WEB_A web-b=$WEB_B app-a=$APP_A db-a=$DB_A sandbox-a=$SAND"
pass() { echo "PASS: $1"; }
fail() { echo "FAIL: $1"; }

lxc exec web-a -- ping -c2 -W2 "$WEB_B" >/dev/null 2>&1 && pass "R2 intra-group" || fail "R2 intra-group"
lxc exec web-a -- ping -c2 -W2 "$APP_A" >/dev/null 2>&1 && fail "R3 separation (should be blocked)" || pass "R3 separation"
lxc exec web-a -- ping -c2 -W2 "$DB_A"  >/dev/null 2>&1 && lxc exec db-a -- ping -c2 -W2 "$WEB_A" >/dev/null 2>&1 \
  && pass "R4 mutual" || fail "R4 mutual"
lxc exec app-a -- ping -c2 -W2 "$DB_A"  >/dev/null 2>&1 && pass "R5 app-a->db-a" || fail "R5 app-a->db-a"
lxc exec db-a  -- ping -c2 -W2 "$APP_A" >/dev/null 2>&1 && fail "R5 db-a->app-a (should be blocked)" || pass "R5 db-a->app-a blocked"
lxc exec sandbox-a -- curl -m5 -s -o /dev/null https://example.com && pass "R6 quarantine internet" || fail "R6 quarantine internet"
lxc exec sandbox-a -- ping -c2 -W2 "$WEB_A" >/dev/null 2>&1 && fail "R7 quarantine->internal (should be blocked)" || pass "R7 quarantine->internal blocked"
