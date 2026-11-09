# HydraRoute Core

**ARM64-ready VPN routing and Xray load-balancing core for Debian and Ubuntu.**

HydraRoute Core is a Go-based service for managing multiple VPN subscription profiles and compiling them into an Xray configuration. It is designed for ARM64 devices such as NanoPi, as well as standard Linux systems.

The project keeps subscription profiles isolated: each enabled profile receives its own loopback-only SOCKS5 endpoint and Xray balancer. An optional shared SOCKS5 inbound can apply ordered domain/IP rules and route traffic to a profile balancer, DIRECT, or BLOCK. The shared inbound is separate from the per-profile endpoints.

> **Project status:** Active development / pre-production. The core, API, parser, configuration compiler, encrypted profile storage, and runtime supervisor are implemented and covered by automated tests. Production deployment and integration with a host's network policy remain separate steps.

## Key features

- **Multi-profile architecture** — manage independent subscription profiles without mixing their nodes.
- **Subscription parsing** — supports VLESS, VMess, Trojan, Shadowsocks, SOCKS5, and HTTP(S) proxy links.
- **Transport support** — TCP, WebSocket, gRPC, XHTTP, and HTTPUpgrade where supported by the protocol and Xray.
- **Xray configuration compiler** — generates profile-specific SOCKS5 inbounds, outbounds, balancers, routing rules, and observability settings.
- **Load balancing** — supports random, round-robin, and least-ping strategies.
- **Encrypted profile storage** — subscription source data is encrypted at rest using AES-256-GCM.
- **HRNeo-style subscription manager** — per-profile server selection, select-all/clear-all, offline exclusion, keyword keep/exclude filters, automatic refresh interval, and Save/Cancel.
- **Live server measurements** — separate ICMP echo and per-outbound Xray Observatory delay/status, streamed to the dashboard; stale or unavailable measurements are never presented as fresh.
- **Per-profile refresh scheduling** — each URL subscription has its own auto-update toggle and interval; cached subscription bodies are encrypted with the profile and reused between due refreshes.
- **Selection persistence** — stores manual/automatic node selection and exclusions without persisting raw subscription credentials in selection state.
- **Embedded web dashboard** — responsive Midnight + Violet interface with dark/light themes, profile cards, shared SOCKS5 routing editor, and runtime controls.
- **Shared SOCKS5 routing** — optional dedicated inbound with ordered domain/IP rules, profile/DIRECT/BLOCK actions, and an explicit default route.
- **Local REST API** — loopback-only API for health, runtime status, profile management, routing configuration, refresh, selection operations, GeoIP/GeoSite readiness, and Watchlist status.
- **HRNeo-compatible Watchlist** — Core-owned Unix socket with MATCH/DUMP line protocol, rank-based suffix matching, bounded DUMP streaming, and a read-only status endpoint. GeoSite Domain/Full keys are expanded from the installed data file; non-enumerable patterns are reported as partial coverage.
- **Runtime supervision** — validates candidate configurations with Xray before activation and preserves the last known-good configuration on refresh failures.
- **Scheduled refresh** — independent per-profile schedules with capped exponential retry; disabled unless configured.
- **ARM64 and AMD64 builds** — cross-compilation for Linux ARM64/AArch64 and AMD64.

## Architecture

```text
Subscription source
       |
       v
Profile parser and selection state
       |
       v
HydraRoute configuration compiler
       |
       v
One profile = one SOCKS5 inbound + one balancer
       |
       +-------------------------------+
       |                               |
       v                               v
Per-profile SOCKS5              Optional shared SOCKS5
       |                               |
       v                               v
Profile balancer              Ordered domain/IP rules
                                       |
                                       v
                              Profile / DIRECT / BLOCK
       |                               |
       +---------------+---------------+
                       |
                       v
                  Xray runtime
       |
       v
Applications / router policy (configured separately)
```

Each profile has an independent loopback SOCKS5 endpoint. The optional shared inbound is also loopback-only and has an explicit default action so unmatched traffic cannot fall through to Xray's first outbound. The embedded dashboard is served by the same loopback API at `http://127.0.0.1:17890/`. The generated configuration does not install system routes, modify firewall rules, change DNS settings, or reconfigure network interfaces.

## Supported protocols

| Protocol | Supported link formats |
|---|---|
| VLESS | TCP, WebSocket, gRPC, XHTTP, HTTPUpgrade; TLS and REALITY where compatible |
| VMess | TCP, WebSocket, gRPC, XHTTP; TLS |
| Trojan | TCP, WebSocket, gRPC, XHTTP; TLS |
| Shadowsocks | SIP002 and legacy links |
| SOCKS5 | Proxy links |
| HTTP / HTTPS | Proxy links |

Protocol and transport combinations are validated against the bundled Xray version. REALITY is not accepted with WebSocket or HTTPUpgrade in the current implementation.

**Not currently supported:** Hysteria2 outbound (not supported by the Xray engine used by this project) and IDN hostname normalization.

## Components

- `hrapi` — local REST API and runtime supervisor.
- `hrcore` — core command-line application and configuration functionality.
- `hrparse` — subscription/link parsing utility.
- **Xray** — proxy engine used to run the compiled configuration.

## Security model

- API listens on `127.0.0.1` by default; non-loopback bind addresses are rejected.
- API requests validate the loopback client and Host authority. Browser Origin checks require an exact authority match, including the port.
- Mutating API requests require JSON and enforce local-origin protections.
- Subscription fetching applies size/time limits and SSRF protections.
- Profile source data is encrypted at rest. The encryption key is stored separately from encrypted profile data.
- Runtime configuration and state files use restrictive permissions.
- Xray validates a candidate configuration before it replaces the active configuration.
- The service does not require network administration capabilities and does not manage host routes or firewall rules.

**Important:** Back up the profile encryption key together with the encrypted profile store. If the key is lost, encrypted profile data cannot be recovered. Keep the API bound to loopback; do not expose it directly to an untrusted network.

## Build

Requirements:

- Go toolchain (see `go.mod` for the module version).
- Linux, macOS, or Windows development environment; Linux is the primary deployment target.

Run tests and static analysis:

```sh
gofmt -w .
go test -count=1 ./...
go vet ./...
```

Build Linux binaries:

```sh
CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -o hrapi-linux-arm64 ./cmd/hrapi
CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -o hrcore-linux-arm64 ./cmd/hrcore
CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -o hrparse-linux-arm64 ./cmd/hrparse

CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -o hrapi-linux-amd64 ./cmd/hrapi
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -o hrcore-linux-amd64 ./cmd/hrcore
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -o hrparse-linux-amd64 ./cmd/hrparse
```

The repository CI workflow runs formatting checks, tests, `go vet`, and Linux ARM64/AMD64 builds.

## Runtime and deployment

A systemd unit template and deployment notes are provided under `deploy/`. Review the deployment instructions and adapt paths and runtime settings to the target host before installation.

The API defaults to `127.0.0.1:17890`. Xray executable and configuration paths must be supplied explicitly. Runtime auto-start and scheduled subscription refresh are opt-in settings.

HydraRoute Core is designed to coexist with other proxy clients. Integrating its SOCKS5 endpoint into a router policy is an explicit, separate network-administration task; the core does not create global routes or alter existing routing policies.

## Management API

The embedded dashboard uses the loopback-only API. The shared routing configuration is persisted in a private `routing.json` file under the configured state directory. Each profile card has a **Серверы** action: it opens the HRNeo-style subscription manager. Selection is stored separately from keep/exclude filters, so changing filters does not destroy manual picks. Direct/manual links bypass subscription filters. URL subscription bodies are cached inside the encrypted profile store; per-profile auto-refresh fetches only due sources and uses cached bodies for the rest. The original two measurement columns are kept distinct: ICMP echo is a host-level reachability test, while proxy delay and `offline` state come from the running Xray Observatory.

- GET /v1/geodata/status — inspect the fixed GeoIP/GeoSite assets, sizes and SHA-256.
- GET /v1/watchlist/status — inspect the local MATCH/DUMP socket and partial-coverage counters.
- The Core Watchlist socket is created only after a shared routing configuration has been successfully prepared. Its path is /var/lib/hydraroute/state/watchlist.sock (mode 0600); it never reuses HRNeo's /var/run/hrneo.sock.
- MATCH <name> removes one trailing dot, lowercases, checks exact and parent suffixes, and chooses the lowest numeric rank. DUMP streams T, K, and END records. Targets map to Core profile IDs, DIRECT, or BLOCK; IP-only targets may appear in T, but IP/CIDR entries are not domain K records.
- GeoSite Domain/Full records are expanded; GeoSite Plain/Regex and Xray keyword/regexp patterns are not enumerable concrete keys and are reported as partial coverage by /v1/watchlist/status.
- `GET /v1/profiles` — list profile metadata.
- `POST /v1/profiles` and `DELETE /v1/profiles` — create and delete profiles.
- `POST /v1/profiles/refresh` — compile stored subscriptions and prepare the Xray configuration.
- `POST /v1/profiles/preview` — prepare the credential-free server list, per-profile filters, selection state, and auto-refresh settings for the subscription manager.
- `PUT /v1/profiles` — update profile settings, filters, auto-refresh schedule, and manual Picked/Excluded selection together; the runtime configuration is validated before activation.
- `POST /v1/profiles/refresh` — refresh all subscriptions, or only the selected profile when `profile_id` is supplied.
- `GET /v1/observatory/status?profile_id=<id>` — read real Xray Observatory results for the selected profile.
- `GET /v1/profiles/ping?profile_id=<id>` — Server-Sent Events stream matching HRweb's `proxy.pingServers` interaction: `links`, separate per-node `echo` (ICMP) and `ping` (Xray proxy delay/alive) events, then `done`. The Xray Observatory API exposes status retrieval but no force-probe RPC, so this stream waits for the next real observation cycle; it does not fabricate results or restart Xray.
- `GET /v1/selections?profile_id=<id>` and `PUT /v1/selections` — retained for backwards compatibility. The dashboard now saves profile filters and selection together through `PUT /v1/profiles`. Missing server identities are retained for review; an empty manual pool is supported and omits that profile's balancer.
- `GET /v1/routing` and `PUT /v1/routing` — read, validate, persist, and apply shared SOCKS5 routing rules.
- `GET /v1/runtime` and `POST /v1/runtime` — inspect and control the managed Xray process.
- `GET /v1/refresh/status` — inspect scheduled refresh status.

Mutating endpoints require a same-origin JSON request. The dashboard does not expose the API beyond loopback.

## Current limitations

- Hysteria2 is not supported by the current Xray-based engine.
- IDN hostname normalization is not implemented.
- Host routing, firewall, DNS, and router-policy integration are intentionally outside the core's scope.
- Review dependency provenance and licensing before redistribution or public binary releases.

## License

No license has been added yet. Until a license is published, the source is not granted an open-source redistribution license. Check the provenance and licensing of bundled third-party components separately.

---

**HydraRoute Core** — independent profiles, controlled routing, and Xray-based load balancing.

### Dashboard browser tests

The CI test job runs Playwright against the embedded dashboard assets with a local API fixture. It exercises the subscription manager, streamed echo/proxy ping events, offline exclusion, keyword filtering without losing manual picks, per-profile auto-refresh settings, and empty manual-pool handling. The browser is Chromium headless; the test fixture does not start Xray or modify host networking.
