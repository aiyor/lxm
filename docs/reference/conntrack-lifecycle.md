# Conntrack lifecycle on policy changes

When `lxm apply` **tightens** network policy — removes an `allow` entry, narrows a group's
permissions, or re-merges a carve-out into its reject prefix — it rewrites the LXD network ACL's
**rule set**. It does **not** touch the host kernel's **connection tracking (conntrack)** flow
table. This page explains what that means for your running traffic, and how to handle it.

## What happens

The kernel accepts packets that match an *established* conntrack entry ahead of the per-bridge ACL
chains. So after a tightening:

| Traffic | Result |
| :-- | :-- |
| **New connections** | Rejected immediately — the new `reject` rules apply. |
| **Pre-existing flows** | Keep flowing until their conntrack entry expires (default TCP timeout is `nf_conntrack_tcp_timeout_established` = **432000 s ≈ 5 days**). |

lxm surfaces this at plan/apply time with a warning on the affected ACL:

```text
Warning: ACL "lxm-vmbr0" tightened; pre-existing connections may persist until conntrack expiry (see docs: conntrack lifecycle)
```

This is a deliberate design decision: `lxm` **never flushes conntrack implicitly**. Flushing is a
host-admin action (`conntrack`/`nft` need root) with blast radius far beyond lxm-managed objects —
Docker, other bridges, and host sockets all share the table — and it would violate the tool's
least-privilege posture.

## Operator runbook

After tightening a policy that had live flows, choose one of:

1. **Scoped conntrack delete** (precise, needs host root + `conntrack-tools`):
   ```bash
   sudo conntrack -D -d <newly-blocked-cidr>
   # or scoped by both endpoints:
   sudo conntrack -D -s <source-subnet> -d <dest-subnet>
   ```
2. **Instance restart** (no host privileges): stop/start the instance — this destroys its veth pair
   and the associated conntrack entries.
3. **Full flush** (sledgehammer, discouraged): `sudo conntrack -F`.

Nothing about the new policy is wrong — new connections are already blocked. The above only clears
the pre-existing flows that predate the change.
