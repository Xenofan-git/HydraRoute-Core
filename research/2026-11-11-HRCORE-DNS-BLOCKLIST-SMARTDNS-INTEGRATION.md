# SmartDNS blocklist integration and allow precedence

Date: 2026-11-11
Branch: integration/hrneo-dns-blocklist-parser-20261110
PR: #7

## Core integration

- The persistent HRweb-style blocklist manager remains the source of truth for source metadata, last-known-good parsed results, enablement and per-source removed entries.
- The regular Core DNS resolver receives the merged Blocked and Allowed snapshots as before.
- When SmartDNS is enabled, the API derives two reserved domain sets from the same snapshot:
  - `hrcore-blocklist` (kind `block`)
  - `hrcore-allowlist` (kind `allow`)
- These generated sets replace only the reserved IDs; user-managed SmartDNS domain sets are retained.
- On blocklist create/update/toggle/delete/refresh/remove/restore, the API rebuilds the managed SmartDNS generation. If the sidecar is running, Runtime.Apply uses the normal readiness gate and previous-generation rollback. If it is stopped, the new generation is prepared but not started.
- SmartDNS configuration PUT also includes the current blocklist snapshot in the prepared runtime generation, while persisted user configuration remains free of generated sets.
- The SmartDNS dashboard text now describes this automatic synchronization.

## Allow precedence: source-level SmartDNS verification

SmartDNS source is retained at `/root/hrdns-research/d0/source`.

The configuration parser in `src/dns_conf/address.c` handles an address token beginning with `-` by setting `DOMAIN_FLAG_ADDR_IGN` on the domain rule. In `src/dns_server/rules.c`, `_dns_server_get_rules` accumulates flags from every matching suffix rule, and `_dns_server_update_rule_by_flags` clears both IPv4 and IPv6 address rules when `DOMAIN_FLAG_ADDR_IGN` is present.

Therefore `address /domain-set:<allow-set>/-` is a real allow exception, not a no-op response:
- allow entries suppress matching block address rules;
- flags accumulate across matching suffixes, so parent and child exceptions both work;
- exact overlap between a block and allow source is supported by SmartDNS itself.

The Core compiler now permits block/allow suffix overlaps and emits block sets before allow sets for deterministic configuration. It does not discard block source entries: the SmartDNS resolver applies the allow flag during query rule resolution.

## Tests added

- API-level test verifies that current manager Blocked/Allowed snapshots replace only the generated SmartDNS sets while preserving custom sets.
- Compiler test verifies exact overlap, allow parent vs block child, and allow child vs block parent all compile without losing source domains.
- Existing SmartDNS actual-binary runtime integration tests continue to validate staged generation, process start, DNS readiness and stop on AMD64 and ARM64.

## Remaining DNS closeout gates

1. CI for this synchronization change must pass on the current PR head.
2. Add and validate periodic DNS blocklist refresh using the original HRweb scheduling contract; do not silently reuse the subscription refresh interval.
3. Complete end-to-end DNS query tests proving Block/Allow and per-profile routing through actual SmartDNS.
4. Verify Core DNS service and SmartDNS lifecycle recovery after API restart.
5. Controlled NanoPi installation/deployment remains separate and requires explicit approval. No host resolver, route, firewall, Keenetic, HRNeo or 3x-ui changes are part of this work.
