# HRDNS / HydraRoute Core: DNS priority comparison (2026-11-15)

## Evidence inspected
- Original HRDNS 2.0.0-1 package static research: `/srv/development/migrated-root/hrdns-research/2026-11-07-HRDNS-2.0.0-1-STATIC-ANALYSIS.md`, original package `/srv/development/migrated-root/hrdns-research/hrdns_2.0.0-1_aarch64-3.10.ipk`.
- Core: `internal/dnsservice/dns.go`, `internal/dnsservice/smartdns/compiler.go`, `internal/api/server.go` and corresponding tests.

## Confirmed about original
Original HRDNS is a standalone resolver based on SmartDNS 48.4, not the Core front DNS manager. Binary strings expose `nameserver`, `domain-rules`, `domain-set`, `server-https`, `server-tls`, `-group`, `-exclude-default-group`. Public SmartDNS 48.4 supports domain-to-upstream-group selection with `nameserver /domain/group` and domain sets. This is a **capability**, not proof of the exact precedence of original HRNeo's UI settings. Original HRDNS package was inspected statically only; no original runtime queries were captured. The four-step priority claimed for Core must **not** be attributed to HRNeo without a runtime trace or extracted original config.

## Confirmed about current Core
- `dns.go:forward`: when SmartDNS forwarder is configured it receives the query first, before Core rule DNS, domain group DNS and general DNS. This behavior is verified by `TestDomainGroupForwardingWithSmartDNSPriority`.
- With SmartDNS off: Core checks rule DNS first, then domain-group DNS, then general DNS. `TestDomainGroupUpstreamsAndPersistence` and `TestDomainGroupForwardingWithSmartDNSPriority` exercise group matching, persistence and actual DNS responses.
- Core SmartDNS compiler already emits `nameserver /domain-set:.../hr_dns_<profile>` and `nameserver /domain/hr_dns_<profile>` for its **profile-based** upstream groups. These are not the same thing as DNS-server selections in the DNS Service UI (`dnsservice.Config.DomainGroups[].Servers`). No automatic conversion between them currently exists.
- SmartDNS generated configuration deliberately excludes proxied upstreams from the default group. It must not silently fall back to arbitrary public resolvers.

## Decision and remaining work
Do **not** change Core's DNS precedence based on an unverified assumption about HRNeo. To claim exact original compatibility, collect an original HRDNS generated configuration and run controlled queries with conflicting domain/rule/general assignments on an isolated test environment. Then design mapping from DNS Service UI groups to SmartDNS native named upstream groups (including Do53/DoT/DoH, server enablement, conflicts, and fail-closed behavior). Do not expose global routing or modify Keenetic/NanoPi production DNS during this investigation.
