SRC ?= /mnt/devel/tools/lxm
VM  ?= lxm-lab

provision:
	lxm apply $(SRC)/simulation/host/lxm-lab.yaml

# rebuild the in-VM lxm from the (mounted) live source — the iteration hot path
rebuild:
	lxm run $(VM) -- $(SRC)/simulation/host/recipes/build-lxm.sh

plan:
	lxc exec $(VM) -- su - lxm -c 'cd /opt/lxm-src/simulation/vm/lab && lxm plan . --format json'

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
