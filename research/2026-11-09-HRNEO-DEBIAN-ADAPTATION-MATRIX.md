# HydraRoute Core — HRNeo Debian/Ubuntu adaptation matrix
Date: 2026-11-09
Target: NanoPi R3S LTS / Debian 13 ARM64
Panel: http://192.168.4.26:17891
Backend API: 127.0.0.1:17890
GitHub: Xenofan-git/HydraRoute-Core, draft PR #2

## 1. Source-of-truth separation

- **HRNeo 3.24.2-1** is the original router-side daemon and the behavioral reference for policy, DNS capture, L7, interfaces, and system integration.
- **HRweb 1.68.0** is the web UI/reference for user workflows, profile selection, subscription handling, and presentation. The address `192.168.1.1:2000` is the HRweb panel, not the HRNeo daemon endpoint.
- **HRDNS 2.0.0-1** is a separate DNS package. It is not a Debian service package and must not be installed as an Entware IPK on NanoPi.

Do not copy HRweb binaries/assets. The Neo source is AGPL-3.0; this implementation is being developed independently from documented behavior and API observations. Any direct code reuse requires an explicit licensing decision.

## 2. Debian adaptation principles

The target is functional parity at the hrcore panel and proxy-control plane, not a Keenetic firmware port.

- One profile keeps its own loopback SOCKS5 inbound and Xray balancer.
- Policy rules are compiled into Xray routing for traffic that explicitly enters hrcore's SOCKS5 inbounds.
- Direct maps to an Xray freedom outbound; block maps to blackhole; profile actions map to that profile's balancer.
- Outgoing-interface selection is per outbound/profile through socket binding, not through Keenetic's interface registry.
- DNS is an optional local service. It must not replace system DNS, rewrite router DNS, or intercept unrelated device traffic.
- L7 inspection is limited to traffic explicitly entering the proxy and should use Xray inbound sniffing. No global NFLOG/iptables capture.
- No global `ip route`, `ip rule`, policy routing, iptables/nftables, conntrack flush, or Keenetic NDM/RCI integration.

## 3. HRNeo feature mapping

| Original HRNeo capability | hrcore/Debian implementation | Status |
|---|---|---|
| Domain rules / watchlist | Ordered Xray domain rules on shared SOCKS inbounds; Core-owned MATCH/DUMP socket and readiness API | Implemented foundation; partial GeoSite coverage |
| IP/CIDR rules | Ordered Xray IP/CIDR rules | Implemented foundation; syntax parity audit pending |
| Direct / block / profile routing | freedom / blackhole / profile balancer | Partial, rule UI exists |
| PolicyOrder | Preserve ordered first-match rule compilation | Partial |
| GeoIP / GeoSite databases | Safe Xray asset readiness, tag catalog and rule references | Partial; list/preset CRUD and managed updates pending |
| DNS capture / dynamic IP sets | Optional loopback-only Core DNS resolver consumed by Core Xray | Partial; no AF_PACKET capture, ipsets or transparent interception |
| L7 HTTP/TLS/QUIC classification | Opt-in Xray inbound sniffing on Core SOCKS5 only; no NFLOG/firewall capture | Partial; HTTP/TLS E2E on VPS+NanoPi, QUIC E2E on VPS; ARM64 QUIC E2E pending |
| DirectRoute / ForceInterface | Per-profile Xray `sockopt.interface` and read-only interface listing | Adapted / partial; no fwmark/ip-rule/table manager |
| GlobalRouting | No safe host-level equivalent under current constraints | Not ported |
| ConntrackFlush | Not applicable to independent SOCKS sessions | Not ported |
| Health/Observatory | Xray gRPC Observatory, cached status, manual ICMP/proxy SSE and Health Monitor | Implemented foundation; full parity/scale/failover audit pending |
| Watchlist MATCH/DUMP API | Core-owned private Unix socket MATCH/DUMP plus HTTP readiness | Implemented with partial GeoSite Plain/Regex coverage |
| Event/log monitoring | Bounded persistent structured event journal, local API, SSE and live UI | Implemented foundation; raw Xray stdout/stderr viewer pending |
| Phobos / WireGuard helpers | Optional isolated helper/service lifecycle | Pending |
| System/network metrics and speed tests | Read-only disk/RAM/load/uptime metrics in Health Monitor | Partial; network speed tests and full system diagnostics pending |
| Service/config management | hrcore-owned local services/config only | Partial |

## 4. Observatory policy

User requirement: probe only keys that are members of active balancer pools.

Current implementation rule:
- only eligible nodes are emitted as Xray outbounds;
- Xray `observatory.subjectSelector` is built from the same eligible tags as the balancer selectors;
- excluded, filtered, and unselected manual nodes are not probed;
- the manual SSE/ICMP stream uses the same eligible-node set;
- cached measurements are returned only for currently eligible nodes and marked stale when not refreshed.

Consequence: a node removed from the balancer is no longer monitored and cannot auto-recover through Observatory until it is re-included. This is intentional under the probe-only-balancer-members constraint.

## 5. HRDNS 2.0.0-1 static-analysis conclusions

Reference report: `research/2026-11-07-HRDNS-2.0.0-1-STATIC-ANALYSIS.md`
Expected SHA-256: `553e05c8d50de951c2acbeebf06960c1b5934833c0218065d9bce26077e1e521`

- HRDNS is an Entware/Keenetic package built around SmartDNS 48.4, with additional UI/API and management integration.
- Do not copy or install the IPK on Debian.
- Reuse upstream SmartDNS as an optional sidecar, with hrcore-owned config and lifecycle.
- Keep listeners loopback-only by default.
- DNS-over-TLS certificate verification must fail closed.
- No ECS by default; no system resolver changes; no transparent DNS interception.
- Upstream selection must be explicit and independently testable.

## 6. Current implementation and validation

- The hrcore panel on NanoPi was previously confirmed at port 17891 and redirects unauthenticated requests to its login page. No production config was changed.
- Backend API is local on port 17890.
- Per-profile DPI bypass, balancing strategy, outgoing interface, and subscription refresh settings are implemented in the VPS source branch.
- Observatory settings are persisted; status polling and manual probe stream are wired.
- The compiler and probe stream now restrict checks to actual balancer members.
- Local VPS validation after DNS/events/health integration: full `go test ./...`, `go vet ./...`, gofmt and `git diff --check` passed.
- GitHub Actions #196 passed Go tests, JS syntax, Playwright, Xray config validation, HTTP/TLS/QUIC E2E and static AMD64/ARM64 builds. #197 is checking the subsequent SSE/Observatory hardening.
- Current API has 26 route patterns; the original HRweb inventory has 114 regular API methods plus streams. Exact method-by-method parity remains an explicit audit task.
- All source changes remain source/CI only; nothing has been deployed to NanoPi or changed on Keenetic.

## 7. Next implementation sequence

1. Close CI #197 and fix every failing gate.
2. Complete method-by-method mapping of all 114 HRweb API methods plus streams.
3. Finish balancer/Observatory scale, failure, fallback and two-profile isolation tests.
4. Complete Geo list/preset management and safe data update/rollback.
5. Finish DNS/HRDNS parity review and loopback-only resolver tests.
6. Implement optional Phobos/WireGuard helpers only after isolated lifecycle and failure tests.
7. Complete systemd packaging, file APIs and remaining system/speed workflows where safe.
8. Run final NanoPi tests only after explicit approval; keep the existing Keenetic HRNeo, HydraRoute Neo/Entware, NanoPi 3x-ui, Tailscale and routing policies untouched.

## 8. Active server reverse-engineering note

See `research/2026-11-09-HRWEB-ACTIVE-OUTBOUND-INDEX-MAPPING.md`. HRweb's active-target reverse mapping uses the final two numbers in its outbound tag as interface ordinal and full-pool index. Core should retain stable ServerKey-derived outbound tags instead of copying that fragile index scheme. The last `principle_target.tag` is a balancer candidate, not proof of a live socket.

## Watchlist MATCH/DUMP contract and Core implementation update — 2026-11-09

Original contract recovered from Ground-Zerro/HydraRoute commit 7fb556ec493adda131f3e8f97106226751b59528. Core source now implements a Core-owned Unix socket under the private state directory, MATCH/DUMP line protocol, lowest-rank-wins suffix matching, four-client cap, bounded input/output, pipelining, half-close, DUMP streaming, GeoSite Domain/Full expansion, and loopback-only status API. The Rules UI reports readiness and partial coverage.

The implementation uses /var/lib/hydraroute/state/watchlist.sock (0600), never /var/run/hrneo.sock. GeoSite Plain/Regex and Xray keyword/regexp patterns are excluded from concrete K keys and counted in the partial-coverage status. See research/2026-11-09-HRNEO-WATCHLIST-API-CONTRACT.md and research/2026-11-09-HRNEO-WATCHLIST-IMPLEMENTATION.md.

## 2026-11-13 — CI-confirmed parity audit checkpoint

This is a scope checkpoint, not a claim that all HRweb methods are implemented.

- The latest Health Monitor threshold reconfiguration regression (`14a9384`) passed both HydraRoute Core CI and SmartDNS sidecar CI.
- The Core implementation now distinguishes first failed probe (`checking`/`warning`), repeated failures (`degraded`), consecutive recovery threshold, intermittent failures while degraded, and resets of observation counters on configuration changes.
- The event journal now records both automatic recovery retries and the first success after failure; health component transition events classify `checking` as a warning.
- These source-level and CI checks do **not** establish live NanoPi readiness or HRweb's full 114-method API parity.

### Remaining functional gaps to prioritize

1. Build an exact **114-method HRweb API inventory** and map each method to a Core endpoint, deliberate Debian-only substitute, or explicit non-applicable Keenetic operation. Include HTTP verb, input/output behavior, and an executable test or a documented gap. The current feature-group matrix is not method-level evidence.
2. Verify **Observatory and balancer isolation** with two independent profiles, a dead outbound, failover, stale measurement rejection, and candidate-target versus actual connection reporting. Keep probing restricted to active balancer members.
3. Audit **GeoIP/GeoSite lifecycle** (catalog, import/update, validation, atomic rollback, UI). Existing asset readiness and rule references are not equivalent to full HRweb management.
4. Audit **raw Xray logs, system diagnostics, speed tests, optional Phobos/WireGuard helpers**, and Debian service packaging. Preserve Core's loopback-only proxy scope.
5. Keep host routing, firewall, system DNS, Keenetic NDM/RCI, and other live services out of scope unless explicitly authorized.

Do not repeat existing passing DNS/Health/Observatory tests solely to increase coverage counts; implement tests against newly identified behavior gaps. All findings here are based on the checked-in matrix, README, and recent CI, not a new runtime deployment.
