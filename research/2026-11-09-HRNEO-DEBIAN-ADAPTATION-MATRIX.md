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
| Domain rules / watchlist | Ordered Xray domain rules on shared SOCKS inbounds | Partial |
| IP/CIDR rules | Ordered Xray IP/CIDR rules | Partial |
| Direct / block / profile routing | freedom / blackhole / profile balancer | Partial, rule UI exists |
| PolicyOrder | Preserve ordered first-match rule compilation | Partial |
| GeoIP / GeoSite databases | Managed Xray geodata assets and UI rule types | Pending |
| DNS capture / dynamic IP sets | Optional SmartDNS sidecar and local resolver groups | Pending |
| L7 HTTP/TLS/QUIC classification | Opt-in Xray inbound sniffing on Core SOCKS5 only; no NFLOG/firewall capture | Partial; API/UI/compiler wired, functional traffic tests pending |
| DirectRoute / ForceInterface | Per-profile outbound socket interface selection | Partial |
| GlobalRouting | No safe host-level equivalent under current constraints | Not ported |
| ConntrackFlush | Not applicable to independent SOCKS sessions | Not ported |
| Health/Observatory | Xray gRPC Observatory, cached status and manual SSE probe | Implemented foundation |
| Watchlist MATCH/DUMP API | Local rule match/dump API | Pending |
| Event/log monitoring | Local structured events and log viewer | Partial |
| Phobos / WireGuard helpers | Optional isolated helper/service lifecycle | Pending |
| System/network metrics and speed tests | Read-only local metrics and isolated test module | Pending |
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

- The hrcore panel on NanoPi responds at port 17891 and redirects unauthenticated requests to its login page. No production config was changed.
- Backend API is local on port 17890.
- Per-profile DPI bypass, balancing strategy, outgoing interface, and subscription refresh settings are implemented in the VPS source branch.
- Observatory settings are persisted; status polling and manual probe stream are wired.
- The compiler and probe stream now restrict checks to actual balancer members.
- Local VPS validation: `go test -count=1 ./...`, `go vet ./...`, and `git diff --check` passed.
- GitHub Actions run #169: formatting, JS syntax, Go tests, generated Xray routing config, vet, and Playwright browser tests passed; AMD64 and ARM64 static build jobs were still running at the time of this note.
- All source changes remain in a draft PR; nothing has been deployed to NanoPi or changed on Keenetic.

## 7. Next implementation sequence

1. Close AMD64/ARM64 CI build jobs and confirm the PR workflow.
2. Finish active balancer target semantics without claiming that `principle_target` is a live TCP connection.
3. Complete ordered domain/IP/CIDR rules, then add GeoIP/GeoSite data and rule import/export.
4. Add Xray sniffing for HTTP/TLS/QUIC on hrcore SOCKS inbounds only.
5. Implement the optional SmartDNS sidecar from the HRDNS analysis, with loopback-only defaults.
6. Complete logs/events, status, metrics, and local MATCH/DUMP APIs.
7. Add optional Phobos/WireGuard helpers only after isolated lifecycle and failure tests.
8. Run final NanoPi tests only after explicit approval; keep the existing Keenetic HRNeo, HydraRoute Neo/Entware, NanoPi 3x-ui, Tailscale, and existing routing policies untouched.

## 8. Active server reverse-engineering note

See `research/2026-11-09-HRWEB-ACTIVE-OUTBOUND-INDEX-MAPPING.md`. HRweb's active-target reverse mapping uses the final two numbers in its outbound tag as interface ordinal and full-pool index. Core should retain stable ServerKey-derived outbound tags instead of copying that fragile index scheme. The last `principle_target.tag` is a balancer candidate, not proof of a live socket.
