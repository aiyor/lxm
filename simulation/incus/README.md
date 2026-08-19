# Incus Simulation Lab (`simulation/incus/`)

This directory provides an isolated development and dogfooding harness for Incus 7.x support in `lxm`. It allows developers to run an Incus cluster inside a nested LXD Virtual Machine on a host with LXD already installed, avoiding all socket, network bridge, and firewall conflicts.

## Architecture

```
Host LXD (Workstation)
 ├── VM: lxm-incus-lab (Node 1 Leader: 10.171.13.50 on host lxdbr0)
 │    ├── Zabbly Incus 7.x (stable / lts-7.0)
 │    ├── HTTPS REST API: https://10.171.13.50:8443 (mTLS)
 │    ├── Inner Bridge: incusbr0 (10.200.0.1/24)
 │    ├── Cluster Member: incus-node1
 │    └── Source Mount: /opt/lxm-src (mounted from host)
 │
 └── VM: lxm-incus-lab-2 (Node 2 Member: 10.171.13.51 on host lxdbr0) [Optional Multi-Node]
      └── Cluster Member: incus-node2 (Joined to incus-node1)
```

## Quick Start

### 1. Provision the Incus Lab VM (Leader)
```bash
make -C simulation/incus provision
```

### 2. Enroll Host Remote via Trust Token
```bash
make -C simulation/incus remote-enroll
```

### 3. Test Local Incus Management (Inside VM)
```bash
make -C simulation/incus test-local
```

### 4. Test Remote HTTPS Cluster Management (Host to VM)
```bash
make -C simulation/incus test-remote
```

### 5. Multi-Node Cluster Setup & Placement Test
```bash
make -C simulation/incus cluster-2node
make -C simulation/incus test-cluster-placement
```

### 6. Clean Up / Reset Inner Fleet
```bash
make -C simulation/incus reset
```
