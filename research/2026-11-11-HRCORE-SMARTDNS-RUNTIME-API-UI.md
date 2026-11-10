# SmartDNS runtime, API and dashboard integration

Date: 2026-11-11
Branch: integration/hrneo-dns-blocklist-parser-20261110
PR: #7

## Implemented

### Runtime manager
- Runtime package: internal/dnsservice/smartdns/runtime.go.
- Binary path expected by the API: /usr/local/libexec/hydraroute/smartdns.
- Runtime state root: <stateDir>/dns/smartdns/runtime.
- Every compiled generation is immutable and stored under generations/<random-id>/ with smartdns.conf and private Domain Set files.
- Files are created with mode 0600 under private 0700 directories; candidate generation is staged then atomically renamed.
- Managed SmartDNS runs in foreground with -f -c <config> -p -.
- Readiness is established only after a DNS response is received from the configured 127.0.0.1 listener.
- Apply while running stops the previous generation, starts the candidate, and restores the previous generation if candidate startup/readiness fails.
- Unexpected process exit is reflected in runtime status and the PID/running fields are cleared.
- API Serve shutdown stops the managed SmartDNS process.
- The sidecar is not auto-started: disabled by default, configuration can be prepared separately, and start is an explicit API/UI action.

### Persistent configuration and API
- Settings file: <stateDir>/dns/smartdns/config.json, atomically written with mode 0600.
- GET/PUT /v1/dns/smartdns.
- POST /v1/dns/smartdns/start and /stop.
- Profile references are resolved from the existing profile store and compiled into loopback SOCKS5 upstreams; unknown or disabled profile references fail closed.
- Startup validates saved settings and prepares a fresh generation but does not start the sidecar.
- Saved config files must be private regular files; symlinks and group/world-readable files are rejected.

### Dashboard
- New DNS tab: SmartDNS.
- UI controls: enable/disable permission, listen port, cache size, profile-pinned DoH/DoT upstreams, domain routing rules, save/prepare, start and stop.
- Existing Go DNS service and SmartDNS sidecar remain separate engines and separate listeners.

## Validation

On commit c9197014274fedb63a8d08b487596fa8e9dcdc22:
- HydraRoute Core CI passed format, Go tests, vet, Xray config validation, HTTP/TLS/QUIC routing tests, Observatory/balancer runtime failover, dashboard JS checks, browser interaction tests, Linux AMD64 and ARM64 builds.
- SmartDNS sidecar workflow passed AMD64/ARM64 builds, actual managed process start/readiness/stop tests on both architectures, and profile-pinned SOCKS/TLS/fail-closed integration under QEMU 9.2.2.

## Still open

1. Controlled install/deploy of the pinned SmartDNS binary on NanoPi; no runtime deployment has been performed.
2. Blocklist source DomainSets integration into SmartDNS and exact HRweb allow/block precedence.
3. Scheduled blocklist refresh (only explicit refresh-one/refresh-all actions are wired).
4. Full DNS profile/data-plane parity and comparison against original HRweb DNS methods.
5. Health Monitor checks for the managed SmartDNS sidecar and recovery policy.

No system resolver, Keenetic, HRNeo, routes, firewall, systemd or 3x-ui changes.
