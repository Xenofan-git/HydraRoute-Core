# HydraRoute Core — DNS lifecycle, event journal, Health Monitor and source audit

Date: 2026-11-10
Status: implementation in working tree; no deployment to NanoPi.

## DNS lifecycle completion

- Reworked `dnsservice.Manager.Start/Stop` around paired UDP/TCP listeners and a generation-scoped completion channel.
- Added an explicit readiness barrier using `miekg/dns.Server.NotifyStartedFunc`. This closes a race where `Stop()` could run before `ActivateAndServe()` marked the listener started, causing shutdown to return `server not started` while the goroutine remained blocked in UDP read/TCP accept.
- Partial listener failure now shuts down the peer and closes the underlying socket if the peer has not entered its started state.
- `Stop()` waits for both serving goroutines to exit before clearing listener state; a stopped service can be started again.
- Added tests for UDP/TCP release, repeated start/stop, and UDP failure closing TCP.
- DNS remains loopback-only and disabled by default. System resolver and route inspection helpers are read-only; no host DNS, routes, firewall, or interface mutation was introduced.

Validation:
- `go test -count=1 ./internal/dnsservice` — PASS.
- `go test -race -count=1 ./internal/dnsservice` — PASS.

## Source audit findings and fixes

- Full `go test -count=1 ./...` passed before the new event/health packages were introduced; focused tests for all subsequently changed packages passed after integration.
- `go vet ./...` passed before the event/health additions; rerun after integration is required.
- `git diff --check` passed.
- Review of `exec.Command` and system integration found only Xray process lifecycle/config validation, bounded ICMP echo (`ping`), and read-only discovery (`ip route show default`, `nmcli` queries). No `ip route add/del`, `ip rule`, firewall/NAT, `resolv.conf`, or host interface mutation was found.
- Corrected an Observatory ICMP batch boundary to clamp to the eligible node count rather than the unfiltered preview count.
- VPS disk space is critically constrained; no old artifacts, backups, archives or research files were deleted. Node.js is not installed on the VPS, so JS syntax and Playwright browser validation must run in CI.

## Structured event journal

- Added `internal/events` with monotonic IDs, UTC timestamps, level/component/type/message fields, bounded 500-event history, atomic private JSON persistence, and subscriber channels.
- State is stored under `<stateDir>/events/events.json` with directory mode 0700 and file mode 0600; journal file size is capped at 1 MiB.
- Added local-only `GET /v1/events`, `DELETE /v1/events`, and SSE `GET /v1/events/stream`.
- Integrated safe, non-secret events for API start, configuration prepare/rejection, profile create/update/delete, runtime start/stop, subscription refresh and DNS lifecycle.
- UI now renders a structured event history and explicitly does not pretend to show Xray stdout/stderr.

## Health Monitor

- Added `internal/health` with validated settings and fail/recovery hysteresis.
- Settings: enabled, interval 5–3600s, timeout 1–30s (less than interval), failure threshold 1–10 and recovery threshold 1–10.
- Checks are read-only: API, Xray process/control API, optional Core DNS, GeoIP/GeoSite assets, Watchlist, filesystem free space, available memory, load average and uptime.
- Exposes `/v1/health/monitor`, `/v1/health/check`, `/v1/health/settings`.
- Background checks run only within the Core API service lifecycle and stop before managed Xray shutdown.
- UI exposes monitor configuration, manual check, component state and basic resource metrics.
- State transitions are recorded in the event journal.

Validation after this phase:
- `go test -count=1 ./internal/health ./internal/events ./internal/api ./internal/ui ./internal/dnsservice` — PASS.
- `go vet ./...` — pending final rerun after these changes.
- Browser tests / JS syntax / ARM64 builds — pending GitHub CI; local VPS has no Node.js and only ~200 MiB free disk.

## Safety boundary

All changes remain source-only. No Core runtime deployment/restart on NanoPi, no changes to Keenetic/Entware HydraRoute Neo, 3x-ui/Xray, Tailscale, Direct rules, system DNS, host routes, firewall or NAT.
