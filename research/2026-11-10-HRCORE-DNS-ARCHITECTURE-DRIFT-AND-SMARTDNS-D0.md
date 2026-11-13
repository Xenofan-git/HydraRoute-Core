# DNS architecture drift audit and SmartDNS D0 status

Date: 2026-11-10

## Finding

The current `internal/dnsservice.Manager` is a self-contained Go DNS server using `miekg/dns`. It provides a loopback listener, Do53/DoT/DoH upstreams, custom hosts, simple domain block rules and statistics. Recent fixes make its lifecycle, A/AAAA blocking, Do53 TCP fallback, DoH HTTPS policy and API rollback safer.

This implementation is **not** the target architecture in `/root/hrdns-research/2026-11-07-HYDRAROUTE-DNS-SUBSYSTEM-SPEC.md`. It does not pin a DNS upstream group to a particular Core profile's SOCKS5 endpoint, does not generate per-interface SmartDNS proxy/group definitions, does not implement bootstrap isolation, and does not download/parse/manage HRDNS blocklist sets. Therefore it must not be described as full DNS integration or deployed as a substitute for the planned sidecar.

## Target architecture reaffirmed

- Optional SmartDNS Release48.4 sidecar, disabled by default.
- One SmartDNS proxy definition per enabled Core profile, using only that profile's loopback SOCKS5 inbound.
- DoH/DoT upstreams grouped by stable `interface_id`; no cross-profile/direct fallback.
- Typed domain rules and domain-set block/allow lists; no arbitrary raw config injection.
- Explicit bootstrap with TLS hostname verification; no `-no-check-certificate`.
- Loopback high port only; no port 53, no LAN bind, no system DNS, route, firewall, netlink, ipset or nftset mutation.
- Atomic candidate config generation and last-known-good rollback.
- Separate systemd/runtime identity and bounded logs; deployment requires a separate user approval.

## D0 source/artifact status

- Upstream: SmartDNS `Release48.4`, commit `21c940edc65520849ba03544c5cf8d9cf326e680`.
- Source: `/root/hrdns-research/d0/source`.
- ARM64 artifact: `/root/hrdns-research/d0/artifacts/smartdns-48.4-aarch64/smartdns`.
- SHA-256: `79bc9b5037f6ac68be7ee8bcd4a5040421ebbfefae17e4b629de7a20a80b22c8`.
- ELF: AArch64, PIE, dynamically linked; GLIBC >= 2.38 and OpenSSL >= 3.3.0 symbols observed; zlib and system runtime dependencies must be checked on the target.
- Build manifest records `WITH_UI=0 WITH_ZLIB=1 VER=48.4`, GCC cross compiler 15.2.0, GNU Make 4.4.1 and extracted target development libraries. Build tools/dependencies were staged in `/dev/shm`, not installed into the VPS host.
- No SmartDNS process has been run on NanoPi or VPS; no service, system DNS, network, route or firewall configuration has been changed.
- SmartDNS CLI has no dedicated config-only validation mode. Candidate validation therefore requires strict typed compiler tests plus startup/query tests in a disposable sandbox.

## D1 next implementation slice

1. Add typed DNS config models for upstreams, profile references, domain rules, domain sets, hosts and global listener/cache settings.
2. Build a deterministic SmartDNS config compiler from the active Core profile registry. Validate each profile's loopback SOCKS endpoint and reject unresolved/disabled references.
3. Emit only safe directives: `bind 127.0.0.1:<high-port>`, `proxy-server socks5://127.0.0.1:<profile-port>`, `server-https`/`server-tls` with `-proxy`, `-group`, `-exclude-default-group`, `nameserver`, `domain-set`, and local hosts/blocking rules.
4. Reject all directives/fields capable of changing host networking or leaking across groups: `-no-check-certificate`, ECS/subnet, `-set-mark`, `-interface`, ipset/nftset, route and arbitrary include/config text.
5. Add golden compiler tests, overlap/precedence tests, secret redaction tests and two-profile isolation tests before implementing service lifecycle.
6. Add a pinned ARM64 CI build and disposable runtime tests for SOCKS5/DoH/DoT, bootstrap, invalid certificate, profile-down fail-closed and no cross-profile fallback.

## Decision

Keep the current Go resolver as an interim feature branch until the SmartDNS D1/D2 implementation is proven. The final product path must not silently conflate the interim resolver with the SmartDNS sidecar. No NanoPi deployment is authorized by this audit.

## D1 typed SmartDNS config compiler — implementation started

Package: `internal/dnsservice/smartdns`.

Implemented and unit-tested:
- typed profile references, DoH/DoT upstreams, exact/suffix domain rules and block/allow/route domain sets;
- one generated SOCKS5 proxy per Core profile, shared by that profile's upstreams only;
- one isolated SmartDNS group per profile; every upstream uses `-exclude-default-group`, so unmatched names have no direct/default fallback;
- explicit public literal bootstrap IP, TLS hostname verification, SNI/HTTP Host, HTTPS-only DoH, and no embedded credentials/raw endpoint fragments;
- exact rule syntax uses SmartDNS root-only `/-domain/group`; suffix rules use `/domain/group`. This behavior was verified against the pinned Release48.4 source parser (`_config_setup_domain_key`), not yet runtime-tested;
- deterministic domain-set files, block/allow/route directives, suffix-overlap rejection for conflicting block/allow sets, and rejection of route-set overlap with explicit rules;
- loopback-only high-port UDP/TCP listeners, `/dev/null` resolv-file, disabled cache persistence/local PTR/mDNS/audit, and no generated host-network directives;
- compiler output is in-memory only; atomic file writing and sidecar lifecycle are not yet integrated.

Current compiler tests: 9 passing. This is D1 compiler progress only; no claim of SmartDNS runtime or DNS data-plane integration.

## Netlink source audit and required upstream patch

The unmodified SmartDNS Release48.4 source opens `NETLINK_ROUTE` sockets for local-PTR address tracking by default (`local_ptr_enable=1`) and seeds/monitors the neighbor cache even when no MAC client rules exist. This conflicts with the strict Core isolation boundary.

Mitigation prepared:
- generated config sets `local-ptr-enable no`, `mdns-lookup no`, `resolv-file /dev/null`, `cache-persist no`, `expand-ptr-from-address no`;
- `third_party/smartdns/patches/0001-disable-netlink-without-mac.patch` makes neighbor-cache initialization return before opening/reading NETLINK_ROUTE when MAC-based client rules are absent. Core does not emit MAC client rules.
- patch applicability was checked against the pinned Release48.4 source checkout with `git apply --check`.
- `ipset`/`nftset` directives are never emitted; their code paths remain dormant.

The existing ARM64 artifact recorded in D0 is an **unpatched baseline** and must not be used as the final Core sidecar. A new reproducible build from the pinned source plus the patch is required, followed by `strace`/sandbox verification that no AF_NETLINK socket is opened and no host resolver file is read.
