#!/bin/bash
set -euo pipefail

echo "==> Installing prerequisites..."
apt-get update -y
apt-get install -y curl gnupg tcpdump netcat-openbsd iputils-ping dnsutils jq

echo "==> Configuring Zabbly repository for Incus 7.x..."
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

echo "==> Waiting for Incus daemon readiness..."
incus admin waitready --timeout=60

echo "==> Detecting host-facing management IPv4 on eth0..."
VM_IP=$(ip -4 -o addr show dev eth0 | awk '{print $4}' | cut -d/ -f1)
if [ -z "${VM_IP}" ]; then
    echo "ERROR: Could not detect IPv4 on eth0" >&2
    exit 1
fi
echo "==> Configuring Incus cluster leader with core.https_address: ${VM_IP}:8443"

cat > /root/incus-preseed.yaml <<YAML
config:
  core.https_address: "${VM_IP}:8443"
cluster:
  server_name: "incus-node1"
  enabled: true
networks:
- name: incusbr0
  type: bridge
  config:
    ipv4.address: 10.200.0.1/24
    ipv4.nat: "true"
    ipv6.address: none
storage_pools:
- name: default
  driver: dir
profiles:
- name: default
  devices:
    eth0:
      name: eth0
      type: nic
      parent: incusbr0
      nictype: bridged
    root:
      path: /
      pool: default
      type: disk
YAML

cat /root/incus-preseed.yaml | incus admin init --preseed

echo "==> Creating operator user 'lxm' and granting incus-admin privileges..."
adduser --disabled-password --gecos "" lxm || true
usermod -aG incus-admin lxm

echo "==> Incus cluster leader setup complete."
