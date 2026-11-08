# HydraRoute Core

**ARM64-ready VPN routing and Xray load-balancing core for Debian and Ubuntu.**

HydraRoute Core is a Go-based service for managing multiple VPN subscription profiles and compiling them into an Xray configuration. It is designed for ARM64 devices such as NanoPi, as well as standard Linux systems.

The project focuses on per-profile isolation: each enabled profile receives its own loopback-only SOCKS5 endpoint and its own Xray balancer. Profiles are kept separate instead of merging unrelated subscriptions into a single pool.

> **Project status:** Active development / pre-production. The core, API, parser, configuration compiler, encrypted profile storage, and runtime supervisor are implemented and covered by automated tests. Production deployment and integration with a host's network policy remain separate steps.

## Key features

- **Multi-profile architecture** — manage independent subscription profiles without mixing their nodes.
- **Subscription parsing** — supports VLESS, VMess, Trojan, Shadowsocks, SOCKS5, and HTTP(S) proxy links.
- **Transport support** — TCP, WebSocket, gRPC, XHTTP, and HTTPUpgrade where supported by the protocol and Xray.
- **Xray configuration compiler** — generates profile-specific SOCKS5 inbounds, outbounds, balancers, routing rules, and observability settings.
- **Load balancing** — supports random, round-robin, and least-ping strategies.
- **Encrypted profile storage** — subscription source data is encrypted at rest using AES-256-GCM.
- **Selection persistence** — stores manual/automatic node selection and exclusions without persisting raw subscription credentials in selection state.
- **Local REST API** — loopback-only API for health, runtime status, profile management, refresh, and selection operations.
- **Runtime supervision** — validates candidate configurations with Xray before activation and preserves the last known-good configuration on refresh failures.
- **Scheduled refresh** — optional refresh scheduler with capped exponential retry; disabled unless configured.
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
       v
Xray runtime
       |
       v
Applications / router policy (configured separately)
```

Each profile has an independent loopback SOCKS5 endpoint. The generated configuration does not install system routes, modify firewall rules, change DNS settings, or reconfigure network interfaces.

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

## Current limitations

- Hysteria2 is not supported by the current Xray-based engine.
- IDN hostname normalization is not implemented.
- No web management UI is included in the current core.
- Host routing, firewall, DNS, and router-policy integration are intentionally outside the core's scope.
- Review dependency provenance and licensing before redistribution or public binary releases.

## License

No license has been added yet. Until a license is published, the source is not granted an open-source redistribution license. Check the provenance and licensing of bundled third-party components separately.

---

**HydraRoute Core** — independent profiles, controlled routing, and Xray-based load balancing.
