SRC ?= /mnt/devel/tools/lxm
VM  ?= lxm-incus-lab
VM2 ?= lxm-incus-lab-2
LXM_BIN ?= $(SRC)/bin/lxm

$(LXM_BIN):
	@mkdir -p $(SRC)/bin
	go build -o $(LXM_BIN) $(SRC)/cmd/lxm

# Prerequisites check: host requires 'lxc' (LXD client) and 'jq'

# 1. Provision the primary Incus Lab VM (incus-node1 Leader)
provision: $(LXM_BIN)
	$(LXM_BIN) apply $(SRC)/simulation/incus/host/lxm-incus-lab.yaml

# 2. Rebuild in-VM lxm binary from live mounted source (hot iteration path)
rebuild: $(LXM_BIN)
	$(LXM_BIN) run $(VM) -- $(SRC)/simulation/incus/host/recipes/build-lxm.sh

# 3. Enroll host client with Lab VM over HTTPS mTLS (Token Handoff)
remote-enroll: $(LXM_BIN)
	@echo "==> Fetching trust token from lab VM..."
	$(eval TOKEN := $(shell lxc exec $(VM) -- cat /root/incus-trust-token))
	$(eval IP := $(shell lxc list "^$(VM)$$" --format json | jq -r '.[0].state.network[] | select(.type=="broadcast") | .addresses[] | select(.family=="inet" and .scope=="global") | .address' | head -n1))
	@echo "==> Enrolling host remote 'lab-cluster' at https://$(IP):8443..."
	$(LXM_BIN) remote add lab-cluster https://$(IP):8443 --token $(TOKEN) --provider incus --accept-certificate

# 4. Provision and join Node 2 for a full 2-node cluster
cluster-2node: $(LXM_BIN)
	@echo "==> Provisioning Node 2 VM..."
	$(LXM_BIN) apply $(SRC)/simulation/incus/host/lxm-incus-lab-2.yaml
	@echo "==> Minting cluster join token on Node 1..."
	$(eval JOIN_TOKEN := $(shell lxc exec $(VM) -- incus cluster add incus-node2 --quiet))
	$(eval LEADER_IP := $(shell lxc list "^$(VM)$$" --format json | jq -r '.[0].state.network[] | select(.type=="broadcast") | .addresses[] | select(.family=="inet" and .scope=="global") | .address' | head -n1))
	@echo "==> Joining Node 2 to cluster at $(LEADER_IP):8443..."
	lxc exec $(VM2) -- env LEADER_IP=$(LEADER_IP) JOIN_TOKEN=$(JOIN_TOKEN) /bin/bash /opt/lxm-src/simulation/incus/host/recipes/setup-member-join.sh

# 5. Test single-node Incus fleet locally inside VM via UNIX socket
test-local:
	lxc exec $(VM) -- su - lxm -c 'cd /opt/lxm-src/simulation/incus/vm/lab/single-node && lxm apply .'

# 6. Test multi-node cluster placement across nodes 1 and 2
test-cluster-placement:
	lxc exec $(VM) -- su - lxm -c 'cd /opt/lxm-src/simulation/incus/vm/lab/multi-node && lxm apply .'

# 7. Test Remote HTTPS Cluster API from host to Lab VM
test-remote: $(LXM_BIN)
	LXM_BIN="$(LXM_BIN)" $(SRC)/simulation/incus/scripts/test-remote-https.sh $(VM)

# 7b. OVN E2E suite inside the Lab VM: rebuild the in-VM lxm binary, then run
#     test-ovn-e2e.sh against the inner Incus daemon. Skips gracefully if the
#     inner Incus lacks the network_ovn extension.
test-ovn:
	lxc exec $(VM) -- /bin/bash -c "export PATH=\$$PATH:/usr/local/go/bin; cd /opt/lxm-src && go build -buildvcs=false -o /usr/local/bin/lxm ./cmd/lxm"
	lxc exec $(VM) -- /bin/bash /opt/lxm-src/simulation/scripts/test-ovn-e2e.sh

# 8. Run full verification suite
verify: $(LXM_BIN)
	$(LXM_BIN) run $(VM) -- $(SRC)/simulation/incus/scripts/verify-incus.sh

# 9. Reset inner Incus to pristine state
reset: $(LXM_BIN)
	$(LXM_BIN) run $(VM) -- $(SRC)/simulation/incus/scripts/reset-inner-incus.sh

# 10. Snapshots and Rollbacks
snap-ready: $(LXM_BIN)
	$(LXM_BIN) snapshot create $(VM) ready

rollback: $(LXM_BIN)
	$(LXM_BIN) rollback $(VM) ready

# 11. Interactive shell into VM
shell: $(LXM_BIN)
	$(LXM_BIN) shell $(VM) --run-as root
