# HydraRoute Core — HRNeo/HRweb functional compatibility audit

Date: 2026-11-10  
Reference: HRNeo 3.24.2-1 source commit `7fb556ec493adda131f3e8f97106226751b59528`; HRweb 1.68.0-1 reverse-engineered API inventory (114 regular methods plus streaming methods).  
Scope: current Core source and its local UI/API; no NanoPi runtime deployment.

## Executive result

**Full functional parity is not complete.** The Core control plane is a real independent implementation with profiles, subscription parsing/refresh, Xray compilation/runtime, per-profile balancers/SOCKS, routing rules, interface binding, DNS, GeoData readiness, Watchlist, Observatory, L7 sniffing, event journal and Health Monitor. However, the Core REST surface is a new local API (26 route patterns), not a 1:1 implementation of all 114 HRweb regular API methods. Several original features are intentionally excluded by the Debian safety boundary; others remain pending.

The correct acceptance target is behavioral parity where a safe Debian analogue exists, plus an explicit documented decision for every Keenetic-specific or unsafe feature. A green build alone is not a completion criterion.

## API and UI surface currently present

26 route patterns:

- Liveness/runtime/config: `/healthz`, `/v1/runtime`, `/v1/config`.
- Profiles/subscriptions/picker: `/v1/profiles`, `/v1/profiles/preview`, `/v1/profiles/refresh`, `/v1/profiles/test`, `/v1/profiles/ping`, `/v1/subscriptions/compile`, `/v1/selections`, `/v1/refresh/status`.
- Observatory: `/v1/observatory/status`, `/v1/observatory/settings`.
- Routing/interfaces: `/v1/routing`, `/v1/interfaces`.
- DNS: `/v1/dns`, `/v1/dns/service`, `/v1/dns/check`.
- Geo/Watchlist/L7: `/v1/geodata/status`, `/v1/watchlist/status`, `/v1/l7/settings`.
- Operations: `/v1/health/monitor`, `/v1/health/check`, `/v1/health/settings`, `/v1/events`, `/v1/events/stream`.

UI navigation currently exposes Rules, Network, DNS, Hydra, Info and Settings. It is not a clone of HRweb's tab/dialog layout; workflows are mapped to the independent Core model.

## Compatibility matrix

| HRNeo/HRweb capability | Core implementation | Classification | Remaining gap |
|---|---|---|---|
| Profile CRUD, enable/disable, per-profile SOCKS | `/v1/profiles`; independent loopback endpoints and profile state | Adapted / foundation implemented | Compare every original profile field, defaults, validation and edge cases |
| Subscription fetch/parse/cache/refresh | Parser for VMess/VLESS/Trojan/Shadowsocks; last-known-good cache; scheduled refresh | Adapted / implemented foundation | Full source-header/error/history parity and fault-injection matrix |
| Server picker and selection | Preview, Picked/Excluded, automatic/manual mode, filters and missing-node reconciliation | Adapted / foundation implemented | Exhaustive UI interaction and reconciliation parity |
| Balancing strategies | Xray balancer and stable outbound tags; selected eligible nodes only | Adapted / partial | Verify exact leastPing/random/roundRobin semantics, tie/fallback behavior, node removal, dead-node recovery and per-profile isolation |
| Observatory | Xray gRPC Observatory; subject selectors use only eligible balancer members; cached status; manual ICMP + proxy stream | Adapted / partial | No force-probe RPC; exact HRNeo ranking/failure/fallback parity and large-pool timing still need dedicated tests |
| Ordered domain/IP/CIDR rules | First-match ordered Xray routing; Direct/freedom, Block/blackhole and profile/balancer targets | Adapted / implemented foundation | Validate every original match syntax and rule ordering edge case |
| GeoIP/GeoSite | Safe asset readiness, tag catalog and Xray rule references | Adapted / partial | No full Geo list/preset CRUD, managed download/update, rollback or full tag catalog UI |
| Watchlist MATCH/DUMP | Core-owned Unix socket, MATCH/DUMP, bounded clients/frames and HTTP readiness status | Adapted / partial | Plain/Regex GeoSite entries are excluded; no IP/CIDR K records by design; coverage must remain explicit |
| Outbound interface / DirectRoute | Per-profile Xray `sockopt.interface`; read-only interface enumeration | Adapted / partial | Does not reproduce HRNeo fwmark/ip rule/table manager or Keenetic interface/policy categories |
| DNS servers / resolver | Optional loopback-only Core resolver; DoH/DoT/Do53, hosts, rules/blocklists, stats, start/stop/check | Adapted / partial | Full HRDNS feature comparison, upstream lifecycle/failover, list update semantics and exhaustive resolver E2E |
| DNS capture of all LAN/VPN clients | Not implemented | Intentionally omitted | HRNeo AF_PACKET/IP-set capture is outside the Core SOCKS boundary and would require host packet capture/transparent routing |
| L7 HTTP/TLS/QUIC | Xray sniffing on Core SOCKS inbounds only, `routeOnly`; private API inbound excluded | Adapted / partial | No NFLOG capture of direct LAN/VPN traffic; HTTP/TLS E2E on VPS and NanoPi ARM64; QUIC E2E on VPS only; no TLS MITM |
| Health Monitor | Periodic/manual checks, thresholds/hysteresis, Xray/DNS/GeoData/Watchlist and local disk/RAM/load/uptime | Adapted / implemented foundation | Expand readiness and fault-injection coverage; no full HRweb system diagnostics/update workflows |
| Events/logs | Bounded persistent structured event journal, local API, SSE, live UI | Adapted / partial | Does not yet expose raw Xray stdout/stderr or every original operation/progress event |
| Phobos/obfuscation | Not implemented | Pending | Parser, isolated process lifecycle, slots/ports, rollback and reconciliation |
| WireGuard helper | Not implemented | Pending | Native config/peer status/handshake/data-plane and isolated lifecycle |
| System/network metrics and speed tests | Basic read-only disk, memory, load and uptime metrics | Partial | Network throughput, speed catalog/results, system update and diagnostics workflows |
| File manager APIs | Not implemented | Pending | Must be confined to explicitly allowed Core workspace with path traversal protection |
| RCI/NDM policy creation and commits | Not implemented | Intentionally omitted | No Keenetic RCI/NDM calls or router policy mutation |
| GlobalRouting / ConntrackFlush / ipset / NFLOG / firewall | Not implemented | Intentionally omitted | Conflicts with the no-global-route/firewall/system-mutation boundary |
| systemd install/boot service management | Not complete as an installable package workflow | Pending | Debian service unit, install/upgrade/rollback and boot enablement need separate design |

## Original HRNeo source behavior that cannot be represented as a transparent proxy-only feature

The original HRNeo source uses AF_PACKET DNS response capture, NFLOG packet copies for HTTP/TLS/QUIC, ipset, iptables/nftables-style firewall integration, conntrack operations, fwmarks, policy routing tables, and Keenetic RCI/NDM. Those features operate on arbitrary router traffic, including clients that never use a SOCKS proxy. Core deliberately does not recreate them through hidden host-level mutation.

Core's current semantics are narrower and explicit:

- Rules and L7 sniffing apply only to traffic that enters a Core-managed SOCKS5 inbound.
- Direct means the Core Xray freedom outbound, not a Keenetic policy.
- Outbound interface binding is per Xray outbound via `sockopt.interface`, not a global route or `ip rule`.
- Core DNS listens on loopback and is consumed by Core Xray; it does not rewrite `/etc/resolv.conf`, router DNS or intercept unrelated clients.
- Watchlist uses its own private socket path and does not collide with `/var/run/hrneo.sock`.

## Observatory and balancer audit notes

- Eligible probe subjects are derived from the same filtered/picked/not-excluded node set used by the profile balancer.
- Cached entries are matched by stable profile/node-derived outbound tag; cache entries from a changed tag are not reused.
- Manual ping keeps ICMP echo separate from Xray proxy delay, refuses non-public probe destinations, bounds ICMP concurrency to 8 and the ICMP phase to 20 seconds.
- The stream no longer persists cache once per node; updates are batched and full Xray health fields are retained.
- SSE write deadline is cleared for long configured Observatory cycles; disabled profiles return stale cached measurements without waiting for impossible fresh results.
- Remaining tests: very large subscription/pool behavior, stale cache expiry, subscription refresh while probes are in flight, profile disable/delete while stream is open, Xray process/API failure, per-profile isolation under overlapping node keys, and exact strategy/fallback parity against HRNeo.

## Method-by-method audit expansion

The complete 114-method classification is now in `2026-11-10-HRWEB-114-METHOD-COMPATIBILITY-MATRIX.md`. It maps every regular method to a Core route or explicit pending/omitted disposition and separately records `proxy.pingServers`. This supersedes the earlier family-level-only coverage statement; it does not claim byte-for-byte API compatibility.

## Verification state

- VPS full Go suite: PASS after DNS/events/health changes.
- `go vet ./...`: PASS.
- `gofmt` and `git diff --check`: PASS.
- GitHub Actions #196: PASS — Go, formatting, vet, JS syntax, Playwright, Xray config validation, HTTP/TLS/QUIC E2E, AMD64 and ARM64 builds.
- GitHub Actions #197: PASS — SSE reconnection/subscriber cap, Go/JS/browser tests, Xray config, HTTP/TLS/QUIC E2E and AMD64/ARM64 builds. The subsequent Observatory cache/write-deadline/ICMP-bounding patch is queued for CI #198.
- Read-only NanoPi inspection found a pre-existing `hydraroute-core.service` (active since 2026-11-08) running an older `hrapi` binary. `/v1/runtime` reports `configured=false`, `running=false`; the new Health Monitor, Events, Watchlist and GeoData endpoints return 404. This phase did not replace or restart that service.
- No changes to Keenetic/Entware HydraRoute Neo, 3x-ui/Xray, Tailscale, Direct rules, system DNS, routes or firewall.

## Next audit/implementation sequence

1. Close CI #197 and fix every failing gate before moving on.
2. Complete the detailed 114-method HRweb inventory mapping: request/response, side effects, dependencies, UI action, Core equivalent, and explicit status for each method/stream.
3. Finish balancer/Observatory fault and scale tests, including two-profile isolation and live membership changes.
4. Complete Geo list/preset management and safe asset update/rollback.
5. Finish DNS/HRDNS feature comparison and local resolver integration tests.
6. Decide and implement Phobos/WireGuard modules only after isolated lifecycle contracts.
7. Complete systemd packaging, file APIs and remaining system/speed workflows where useful and safe.
8. Run final ARM64 runtime tests on NanoPi only after explicit approval; do not deploy as part of CI.

## HRNeo `config_t` parameter-by-parameter mapping (30 fields)

Reference field names are from the original `Neo/docs/CORE.md` at the pinned source commit.

| Original field | Core equivalent / decision | Status |
|---|---|---|
| `auto_start` | Core API service has its own service/autostart lifecycle; runtime autostart is controlled by the Core service invocation | Adapted; installer/systemd lifecycle still pending |
| `watchlist_path` | Profile/routing store and generated immutable Watchlist snapshot | Adapted; no direct text-file path parity |
| `clear_ipset` | No ipset subsystem | Intentionally omitted |
| `cidr_enabled` | IP/CIDR rule matching in Core routing model | Adapted |
| `cidr_file_path` | No original CIDR-file parser contract exposed in UI yet | Pending import/export parity |
| `ipset_enable_timeout`, `ipset_timeout` | No kernel ipset expiry; Xray routing has different lifecycle | Intentionally omitted |
| `log_level`, `log_file_path` | Structured event levels and private journal; raw daemon/Xray log file routing not implemented | Partial |
| `direct_route_enabled` | Direct action is Core Xray freedom outbound; not a global route manager | Adapted with narrower scope |
| `interface_fwmark_start`, `interface_table_start` | No fwmark/table allocator | Intentionally omitted under no-host-routing boundary |
| `force_interfaces[]` | Per-profile `sockopt.interface` and read-only interface listing | Adapted; no RCI policy/interface classes |
| `global_routing` | No global host routing | Intentionally omitted |
| `conntrack_flush` | No conntrack mutation | Intentionally omitted |
| `ipset_maxelem` | No kernel ipset | Intentionally omitted |
| `geo_ip_files[]`, `geo_site_files[]` | Xray asset directory and tag catalog/readiness | Partial; managed asset list/update/rollback pending |
| `policy_order[]` | Ordered first-match routing rules | Adapted; exhaustive ordering parity tests pending |
| `l7_capture_enabled` | Opt-in Xray sniffing on Core SOCKS inbound | Adapted; no packet capture |
| `l7_nflog_group` | No NFLOG group | Intentionally omitted |
| `l7_enable_tls`, `l7_enable_http`, `l7_enable_quic` | Xray sniffing protocol flags | Adapted; QUIC ARM64 runtime gate pending |
| `l7_wan_interface` | No WAN/NFLOG capture interface; per-outbound interface binding is separate | Intentionally omitted as capture selector |
| `l7_connbytes_max` | No connbytes firewall match | Intentionally omitted |
| `l7_tcp_reasm_enabled`, `l7_tcp_reasm_max_entries`, `l7_tcp_reasm_ttl_sec` | TCP reassembly delegated to Xray sniffing implementation | Adapted; no equivalent Core-side reassembly table knobs |
| `rci_token` | No Keenetic RCI client/token | Intentionally omitted |

This table is a semantic mapping, not a promise of identical behavior. In particular, Core's SOCKS-only Direct/L7/DNS semantics are not equivalent to HRNeo's interception of arbitrary router traffic.

## HRweb 1.68 API inventory coverage by area

The recovered 114 regular API methods plus streaming methods have not yet been transcribed into a final method-by-method spreadsheet. The current Core route inventory maps only at the feature-family level:

- Profiles, subscription import/refresh, server preview/selection and manual testing: substantial Core coverage.
- Config generation, validation, Xray lifecycle and routing/interface selection: substantial Core coverage, with no NDM/RCI or global route mutation.
- DNS, GeoData, Watchlist, L7, Observatory: feature-specific adapted endpoints exist, but several management and diagnostics operations remain incomplete.
- Health and structured events: newly implemented Core-native endpoints; they are not assumed to be byte-compatible with HRweb methods.
- File management, Phobos, WireGuard, full system/update/speed diagnostics and raw process-log streaming: no complete Core equivalent yet.

The remaining method-by-method audit must record for each original method: name/path, verb, request shape, response shape, stream/event contract, side effects, dependencies, UI caller, Core endpoint/equivalent, test coverage and final disposition (native/adapted/omitted/pending). No method should be marked compatible from a similar UI label alone.
