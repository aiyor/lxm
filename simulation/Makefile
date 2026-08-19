SRC ?= /mnt/devel/tools/lxm
VM  ?= lxm-lab

# --- End-to-End Multi-Provider Test Targets ---
test-lxd:
	$(SRC)/simulation/scripts/test-lxd-e2e.sh

test-incus:
	$(SRC)/simulation/scripts/test-incus-e2e.sh

# OVN e2e against whichever provider CLI is on PATH (auto-detected)
test-ovn:
	$(SRC)/simulation/scripts/test-ovn-e2e.sh

# OVN e2e pinned to a specific provider — run BOTH to satisfy the matrix (§2.1)
test-ovn-lxd:
	PROVIDER_CLI=lxc $(SRC)/simulation/scripts/test-ovn-e2e.sh

test-ovn-incus:
	PROVIDER_CLI=incus $(SRC)/simulation/scripts/test-ovn-e2e.sh

test-all: test-lxd test-incus test-ovn

# --- Legacy LXD Lab Simulation Targets ---
provision:
	lxm apply $(SRC)/simulation/host/lxm-lab.yaml

# rebuild the in-VM lxm from the (mounted) live source — the iteration hot path
rebuild:
	lxm run $(VM) -- $(SRC)/simulation/host/recipes/build-lxm.sh

plan:
	lxc exec $(VM) -- su - lxm -c 'cd /opt/lxm-src/simulation/vm/lab && lxm plan .'

apply:
	lxc exec $(VM) -- su - lxm -c 'cd /opt/lxm-src/simulation/vm/lab && lxm apply .'

verify:
	lxm run $(VM) -- $(SRC)/simulation/scripts/verify.sh

reset:
	lxm run $(VM) -- $(SRC)/simulation/scripts/reset-inner.sh

snap-ready:
	lxm snapshot create $(VM) ready

rollback:
	lxm rollback $(VM) ready

shell:
	lxm shell $(VM) --run-as root
