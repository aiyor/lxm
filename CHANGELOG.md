# Changelog

All notable changes to this project will be documented in this file.

## [1.5.0] - 2026-08-20

### Features
- **network**: support OVN virtual switches and multi-node cluster overlays (#7)
  - Add first-class support for Open Virtual Network (OVN) distributed virtual switches and ACL microsegmentation across LXD and Incus providers.
  - Manifest & Config: add `type: ovn`, `parent` uplink, and `mtu` settings with strict CUE/Go validation and provider capability gating (`network_ovn`).
  - ACL Compiler: generate G0 intra-switch rules for default-reject OVN networks, multi-source DNS resolver derivation, G9 host gateway port guards, and isolated network DNS leak-sealing while preserving in-network resolvers.
  - Driver & Clustering: implement fail-closed network creation polling, robust two-phase cluster member staging with probe-based idempotency, and fail-closed cluster topology detection across LXD and Incus drivers.
  - Diagnostics: add unique, leak-free OVN capability probe to `lxm doctor`.
  - Testing & Docs: add 10 stateful mock cluster daemon unit tests, a 14-gate live OVN E2E simulation suite, and full architecture specs in NETWORK-SPEC.md.

### Documentation
- **network**: document OVN virtual switches, multi-node overlays, and CLI diagnostics (#8)
  - Comprehensively document Open Virtual Network (OVN) support across architecture, specifications, how-to guides, reference manuals, and troubleshooting:
  - ARCHITECTURE.md: add internal/network to package topology and document Section 4.6 (OVN Virtual Switching & Distributed Datapath Architecture). Clarify 4-step IPv4 DNS resolver derivation, G8 RFC1918 carveout scope, member staging for Linux bridge clusters vs fail-closed pending polling for OVN, and provider mapping of reject (400) > allow (300) > baseline (200) on OVN.
  - NETWORK-SPEC.md, SPEC_MANIFEST.md & docs/reference/manifest.md: align parent field rule to required/immutable on type: ovn and optional/ignored on type: bridge. Document nat: false + internet: true warning and DNS leak-sealing.
  - docs/howto/configure-networking.md: document OVN distributed virtual switches, Geneve MTU sizing (1442/8942), explicit uplink parent bridge prerequisites (active IPv4 address and NAT), G0 intra-switch rules, G8 DNS resolver derivation, and nat: false caveat.
  - docs/howto/diagnose-with-doctor.md & cli.md: document provider capability checks and conditional leak-free throwaway OVN switch probe (when IPv4 uplink bridge is present). Update command reference for lxm disk, vswitch, and remote.
  - docs/troubleshooting.md: add entry for provider lacking network_ovn_acl when applying grouped OVN virtual switches.
  - docs/examples/ovn-network-demo.yaml & README.md: provide verified, warning-free multi-group network policy example.

### Maintenance
- **release**: v1.5.0

---

## [1.4.0] - 2026-08-19

### Refactoring
- Unified Multi-Provider Architecture (Add Incus support) (#6)
  - Abstract LXD and Incus SDKs behind unified `provider.Driver` interface
  - Add Incus 7.x native client driver and in-memory mock test driver
  - Add `lxm remote` CLI subcommands with TLS client auth and TOFU verification
  - Add `--provider`, `--remote`, and `--target` CLI flags for cluster targeting
  - Implement full storage disk lifecycle: dynamic hotplug, detach, and volume deletion
  - Implement host directory mount shifting and dynamic in-place mutation
  - Implement two-phase clustered bridge network creation protocol
  - Add comprehensive multi-provider test strategy and automated E2E test suites (TESTING.md)

---

## [1.3.2] - 2026-08-16

### Features
- declarative resource deletion & lifecycle management
  - Adds status: absent on disks:/vswitches: for explicit detach-and-delete, an orthogonal attach: false axis, post-instance delete phases, and marker-based fail-closed lxm disk/vswitch gc.
  - Deletion is gated on the user.lxm.managed ownership marker so external/foreign resources are never destroyed; omission remains detach-only.
  - Verified race-clean and lint-clean across config/plan/apply/CLI.
  - Includes ARCHITECTURE.md §1.10/§4.5.

---

## [1.3.1] - 2026-08-16

### Features
- resolve and fetch uncached cloud images via image_remotes (#4)
  - Automatic remote simplestreams image resolution and fetch for the image: remote:alias form (ubuntu:22.04, images:debian/12, custom image_remotes: declarations).
  - Features type-qualified canonical local aliases (<remote>/<alias>[/vm]) complying with LXD's UNIQUE(project_id, name) constraint, plan-time ImageOp fetch decision with offline probe, Phase -1 deduplicated execution before volume/network/instance phases, URL canonicalization with conflict detection, and user.lxm.image metadata recording to prevent perpetual recreate loops across multi-OS remotes.
  - Local aliases and fingerprints remain local-only (fingerprint mapping to Source.Fingerprint fixes latent alias resolution bug).
  - Includes IMAGE-SPEC.md, SPEC_MANIFEST.md §3.10

---

## [1.3.0] - 2026-08-16

### Features
- declarative VM data disks via disks: section (#3)
  - Adds additional storage volumes to virtual machines in filesystem (guest mount) or block (raw device) mode, each managed by lxm or referencing an external custom volume.
  - Managed volumes are provisioned via a new LXD StorageService (Phase-0 VolumeOps), grown online, and never deleted on removal; shrink and mode switches are rejected at plan time.
  - LXD-correct device encoding (source on every device, size only on the volume, default io.bus omitted) and getLiveMounts prefix partitioning fix the pre-existing conflation of non-root disk devices with mounts.
  - Includes STORAGE-SPEC.md, SPEC_MANIFEST.md §3.9

---

## [1.2.0] - 2026-08-15

### Features
- managed virtual switches and network segmentation (#2)
  - Declarative vswitches:/network_policy: compiled to LXD network ACLs - G1-G8 generator matrix with CIDR decomposition, fleet-scoped union with base-file dedup, ACL->vswitch->instance ordering with phase-abort, NIC integrity checks, doctor bridge-ACL probe, and NETWORK-SPEC.md.
  - Verified on LXD 6.9 (T1(a)/T1(c) pass; T2 pre-NAT ACL matching confirmed; conntrack lifecycle empirically verified).
  - Additive; no change for existing fleets.

---

## [1.1.0] - 2026-08-14

### Features
- add native support for LXD virtual machines and hardware limits (#1)
  - Add `type: virtual-machine` with QEMU/KVM execution and CUE schema validation
  - Add `limits` (cpu, memory, disk) and `vm` (boot_mode, hugepages, raw_qemu) configs
  - Implement guest `lxd-agent` readiness polling loop and timeout handling
  - Support mount `shift: true/false` across containers and VMs
  - Add `TYPE` column to `lxm list` and `/dev/kvm` diagnostic check to `lxm doctor`
  - Update manifest specifications and documentation

---

## [1.0.0] - 2026-08-09

### Initial Release
- Initial GA release of LXM declarative container orchestrator
  - Declarative container management for LXD with CUE schema validation
  - Deterministic plan/apply reconciliation workflow
  - Interactive shell, command execution, snapshot and rollback support

---

