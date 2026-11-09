# L7 sniffing end-to-end validation — 2026-11-09

## Implemented behavior

- Optional L7 sniffing is configurable through the Core API/UI.
- Settings are persisted separately and applied only to HydraRoute Core user SOCKS inbounds.
- Selected destination overrides are HTTP, TLS and QUIC.
- Xray uses `routeOnly: true`; no transparent LAN interception, NFLOG, firewall or host routing is introduced.
- The private Xray API inbound does not receive user-traffic sniffing.

## Actual NanoPi ARM64 end-to-end test

Executed `tests/integration/l7_proxy_sniffing.py` against:
- Xray 26.10.31 ARM64 at `/usr/local/libexec/hydraroute/xray`.
- Existing assets at `/usr/local/libexec/hydraroute`.
- Temporary loopback-only SOCKS5 inbound and loopback target server.
- Temporary Xray config under `/tmp`, removed after test.

Result:
`L7_PROXY_SNIFFING_E2E_OK: HTTP Host and TLS SNI rules route blocked.example to block and allowed.example to direct`

Both protocols were tested with a blocked host and an allowed host. This confirms actual traffic classification and routing, not just config syntax.

No persistent Core/Xray process, systemd service, config, route, firewall or DNS setting was changed.

## QUIC coverage

A separate loopback QUIC test exists at `tests/integration/l7_quic_sni.py`, using pinned `aioquic==1.7.2` and SOCKS5 UDP ASSOCIATE. NanoPi currently has no pip/ensurepip/python3-venv, so no system packages were installed merely to run this optional test.

CI workflow was extended to:
1. Set up Python 3.12 on the ephemeral GitHub runner.
2. Install the pinned QUIC test dependency.
3. Run HTTP Host + TLS SNI E2E.
4. Run QUIC SNI E2E through Core SOCKS UDP.

CI #186 completed successfully: Go tests, generated Xray config validation, pinned aioquic QUIC E2E, HTTP/TLS E2E, go vet, Playwright dashboard tests, and static Linux ARM64/AMD64 builds all passed.

## Existing CI

Latest completed CI runs #181 and #182 passed:
- Go formatting/tests/vet.
- JavaScript syntax and Playwright dashboard tests.
- Xray generated routing config validation.
- Static Linux AMD64 and ARM64 builds.

## Safety boundary

No NanoPi runtime deployment/restart. No modifications to Keenetic, HRNeo/Entware, 3x-ui/Xray, Tailscale, system DNS, routes, firewall, iptables/nftables or conntrack.
