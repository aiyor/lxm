#!/bin/bash
set -euo pipefail
apt-get update -y
apt-get install -y snapd tcpdump netcat-openbsd curl iputils-ping dnsutils

snap install lxd
lxd waitready

cat > /root/lxd-preseed.yaml <<'YAML'
config: {}
networks:
- name: lxdbr0
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
      parent: lxdbr0
      nictype: bridged
    root:
      path: /
      pool: default
      type: disk
YAML
cat /root/lxd-preseed.yaml | lxd init --preseed

adduser --disabled-password --gecos "" lxm || true
usermod -aG lxd lxm
