#!/bin/bash
set -euo pipefail

LEADER_IP="${LEADER_IP:-10.171.13.50}"
JOIN_TOKEN="${JOIN_TOKEN:-}"

if [ -z "${JOIN_TOKEN}" ]; then
    echo "ERROR: JOIN_TOKEN environment variable required" >&2
    exit 1
fi

export DEBIAN_FRONTEND=noninteractive

echo "==> Installing prerequisites and Incus 7.x..."
apt-get update -y
apt-get install -y curl gnupg jq
mkdir -p /etc/apt/keyrings
curl -fsSL https://pkgs.zabbly.com/key.asc | gpg --dearmor --yes -o /etc/apt/keyrings/zabbly.gpg
cat <<EOF > /etc/apt/sources.list.d/zabbly-incus.sources
Enabled: yes
Types: deb
URIs: https://pkgs.zabbly.com/incus/stable
Suites: $(. /etc/os-release && echo ${VERSION_CODENAME})
Components: main
Architectures: $(dpkg --print-architecture)
Signed-By: /etc/apt/keyrings/zabbly.gpg
EOF
apt-get update -y
apt-get install -y incus incus-client
incus admin waitready --timeout=60

NODE2_IP=$(hostname -I | awk '{print $1}')
if [ -z "${NODE2_IP}" ]; then
    NODE2_IP=$(ip -4 -o addr show scope global | awk '{print $4}' | cut -d/ -f1 | head -n1)
fi
if [ -z "${NODE2_IP}" ]; then
    echo "ERROR: Could not detect host-facing IPv4 for Node 2" >&2
    exit 1
fi

# Note: server_address supplies listener binding and cluster endpoint for Node 2.
cat > /root/incus-join.yaml <<YAML
cluster:
  server_name: "incus-node2"
  server_address: "${NODE2_IP}:8443"
  enabled: true
  cluster_address: "${LEADER_IP}:8443"
  cluster_token: "${JOIN_TOKEN}"
YAML

cat /root/incus-join.yaml | incus admin init --preseed
echo "==> Node 2 successfully joined Incus cluster."
