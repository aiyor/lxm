SRC ?= /mnt/devel/tools/lxm
VM  ?= lxm-incus-lab
VM2 ?= lxm-incus-lab-2

# Prerequisites check: host requires 'lxc' (LXD client) and 'jq'

# 1. Provision the primary Incus Lab VM (incus-node1 Leader)
provision:
	lxm apply $(SRC)/simulation/incus/host/lxm-incus-lab.yaml

# 2. Rebuild in-VM lxm binary from live mounted source (hot iteration path)
rebuild:
	lxm run $(VM) -- $(SRC)/simulation/incus/host/recipes/build-lxm.sh

# 3. Enroll host client with Lab VM over HTTPS mTLS (Token Handoff)
remote-enroll:
	@echo "==> Fetching trust token from lab VM..."
	$(eval TOKEN := $(shell lxc exec $(VM) -- cat /root/incus-trust-token))
	$(eval IP := $(shell lxc list "^$(VM)$$" --format json | jq -r '.[0].state.network.eth0.addresses[] | select(.family=="inet") | .address'))
	@echo "==> Enrolling host remote 'lab-cluster' at https://$(IP):8443..."
	lxm remote add lab-cluster https://$(IP):8443 --token $(TOKEN) --provider incus

# 4. Provision and join Node 2 for a full 2-node cluster
cluster-2node:
	@echo "==> Provisioning Node 2 VM..."
	lxm apply $(SRC)/simulation/incus/host/lxm-incus-lab-2.yaml
	@echo "==> Minting cluster join token on Node 1..."
	$(eval JOIN_TOKEN := $(shell lxc exec $(VM) -- incus cluster add incus-node2 --quiet))
	$(eval LEADER_IP := $(shell lxc list "^$(VM)$$" --format json | jq -r '.[0].state.network.eth0.addresses[] | select(.family=="inet") | .address'))
	@echo "==> Joining Node 2 to cluster at $(LEADER_IP):8443..."
	lxc exec $(VM2) -- env LEADER_IP=$(LEADER_IP) JOIN_TOKEN=$(JOIN_TOKEN) /bin/bash /opt/lxm-src/simulation/incus/host/recipes/setup-member-join.sh

# 5. Test single-node Incus fleet locally inside VM via UNIX socket
test-local:
	lxc exec $(VM) -- su - lxm -c 'cd /opt/lxm-src/simulation/incus/vm/lab/single-node && lxm apply .'

# 6. Test multi-node cluster placement across nodes 1 and 2
test-cluster-placement:
	lxc exec $(VM) -- su - lxm -c 'cd /opt/lxm-src/simulation/incus/vm/lab/multi-node && lxm apply .'

# 7. Test Remote HTTPS Cluster API from host to Lab VM
test-remote:
	$(SRC)/simulation/incus/scripts/test-remote-https.sh $(VM)

# 8. Run full verification suite
verify:
	lxm run $(VM) -- $(SRC)/simulation/incus/scripts/verify-incus.sh

# 9. Reset inner Incus to pristine state
reset:
	lxm run $(VM) -- $(SRC)/simulation/incus/scripts/reset-inner-incus.sh

# 10. Snapshots and Rollbacks
snap-ready:
	lxm snapshot create $(VM) ready

rollback:
	lxm rollback $(VM) ready

# 11. Interactive shell into VM
shell:
	lxm shell $(VM) --run-as root
