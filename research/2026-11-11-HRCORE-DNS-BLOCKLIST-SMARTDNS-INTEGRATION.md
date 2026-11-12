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
- flags accumulate across matching suffixes, so an allow child suppresses a matching parent block;
- an identical domain key cannot safely carry both block and allow address actions in SmartDNS's single domain-rule entry. The compiler removes exact Block/Allow duplicates from the generated block set, and also removes block children covered by an allow parent. It retains allow children beneath block parents so SmartDNS's suffix flag performs the override.

The Core compiler permits Block/Allow suffix overlaps, normalizes the exact/allow-parent cases above, and emits block sets before allow sets for deterministic configuration. Source metadata and parsed source results remain intact; only the generated SmartDNS block-set file is normalized.

## Tests added

- API-level test verifies that current manager Blocked/Allowed snapshots replace only the generated SmartDNS sets while preserving custom sets.
- Compiler test verifies exact overlap normalization, allow parent vs block child normalization, and preservation of allow child exceptions beneath a block parent.
- The actual SmartDNS integration harness now tests a blocked parent domain and a child Allow exception against live mock DoH/SOCKS upstreams on AMD64 and ARM64, in addition to TLS, profile isolation and fail-closed behavior.
- Existing SmartDNS actual-binary runtime integration tests continue to validate staged generation, process start, DNS readiness and stop on AMD64 and ARM64.

## Remaining DNS closeout gates

1. CI for this synchronization change must pass on the current PR head.
2. Add and validate periodic DNS blocklist refresh using the original HRweb scheduling contract; do not silently reuse the subscription refresh interval.
3. Complete end-to-end DNS query tests proving Block/Allow and per-profile routing through actual SmartDNS.
4. Verify Core DNS service and SmartDNS lifecycle recovery after API restart.
5. Controlled NanoPi installation/deployment remains separate and requires explicit approval. No host resolver, route, firewall, Keenetic, HRNeo or 3x-ui changes are part of this work.

## HRNeo daily refresh scheduler parity

The original HRWeb UI stores the global DNS block refresh settings through `dns.blockSave`:
- `autoUpdate` (boolean),
- `updateTime` (local `HH:MM`, UI fallback/default `04:30`),
- `iface` (optional system interface).

Static analysis of the original ARM64 HRWeb binary confirms that `main.dnsBlockJob.Next` uses the `hrweb/sched.Daily` scheduler and validates the configured time with `main.isValidTime`. The original DNSBlock first-initialization path sets the auto-update flag and uses the UI's `04:30` default. The job's Run path refreshes the enabled DNS block sources.

Core now persists these settings with the blocklist state (legacy state files without the settings field migrate to the HRNeo defaults), exposes local-only GET/PUT at `/v1/dns/blocklists/settings`, and runs a separate daily scheduler independent of profile/subscription refresh. Saving settings wakes the scheduler so the next daily deadline is recalculated immediately.

An optional interface name is validated and used for outbound HTTP(S) blocklist sockets through Linux `SO_BINDTODEVICE`. DNS hostname resolution remains through the system resolver to preserve compatibility with local resolver stubs such as 127.0.0.53; actual HTTP(S) connections are bound to the selected interface. An empty interface uses normal system routing.

Tests cover default settings, normalization, invalid times/interface names, persistence across restart, interface-fetcher selection, API GET/PUT, wake-up signaling and next-daily-run calculation.


## Runtime cancellation rollback hardening (2026-11-12)

A lifecycle review found that candidate generation startup can be canceled after the previous SmartDNS process has already been stopped. Reusing the canceled request context for rollback made restoration impossible and could leave the DNS sidecar unavailable.

Runtime rollback now uses a separate bounded context (2 * startupTimeout + 3s) for the previous generation. Candidate startup still honors the caller context and does not retry after cancellation; the independent rollback context is used only to restore the known-good generation. A regression test cancels the caller during candidate readiness and verifies that the previous generation returns to Running.

Commits:
- 4cab6a3fe6c9ebe4b7569f28fffeffa4a4934f8b — independent rollback context.
- 245d41b8434f2278ecfe363c4cbb9665d1d381b4 — cancellation/rollback regression test.
- 15c0317745b7b72465bd9229a2e68f404df11bec — full two-attempt rollback time budget.

The latest CI attempt on commit 245d41b8434f2278ecfe363c4cbb9665d1d381b4 failed almost immediately; GitHub returned no job steps or downloadable logs (BlobNotFound). This is not treated as a code-test result. A fresh CI run is required on the latest head. No NanoPi deployment or host DNS/network changes.


## Authoritative implementation correction (2026-11-12)

This section supersedes the earlier implementation description above wherever it says managed block/allow lists are converted to SmartDNS domain sets, that the compiler normalizes block/allow overlaps, or that SmartDNS native allow flags enforce precedence. Those statements describe an intermediate design and are no longer current.

Current branch behavior:
- HRDNS most-specific-suffix allow/block evaluation is implemented in the Core DNS policy resolver.
- Managed URL blocklists and custom SmartDNS block/allow sets are merged into the Core policy-front snapshot.
- SmartDNS compiles profile-routing domain sets only; it does not receive native block/allow address rules.
- Core DNS forwards exclusively to SmartDNS while the sidecar is active. A failed SmartDNS forward returns SERVFAIL; no public DNS fallback is used.
- The blocklist refresh scheduler is a separate opt-in daily local-time job (default 04:30), with optional interface-bound HTTP(S) fetches. No retry loop was observed in the original HRweb scheduled job, so none is invented here.

The current source confirms the optional interface is applied at the HTTP(S) socket layer through the existing subscription fetch transport, while DNS hostname resolution remains on the system resolver for local-stub compatibility.
