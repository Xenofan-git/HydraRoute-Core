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
- Added local-only `GET /v1/events`, `DELETE /v1/events`, and SSE `GET /v1/events/stream` with `Last-Event-ID` reconnection, a 16-subscriber cap, heartbeat and write-deadline clearing. `after_id` pagination returns the next page rather than skipping to the newest page.
- Integrated safe, non-secret events for API start, configuration prepare/rejection, profile create/update/delete, runtime start/stop, subscription refresh and DNS lifecycle.
- UI now renders a structured event history and consumes the live SSE stream; it explicitly does not pretend to show Xray stdout/stderr.

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
- Full `go test -count=1 ./...` — PASS after the latest Observatory changes.
- `go vet ./...`, gofmt and `git diff --check` — PASS.
- GitHub Actions #196, #197 and #198 — PASS for Go, JS syntax, Playwright, Xray config, HTTP/TLS/QUIC E2E and AMD64/ARM64 builds. CI #198 validated the Observatory cache/write-deadline/ICMP-bounding patch.
- Local VPS has no Node.js and only ~190 MiB free disk; JS/browser/build validation is delegated to GitHub CI.

## Read-only NanoPi baseline check — 2026-11-10

A read-only Tailscale SSH inspection corrected an outdated deployment assumption in earlier notes:

- `hydraroute-core.service` was already enabled and active before this work (since 2026-11-08 23:59 MSK), running `/usr/local/libexec/hydraroute/hrapi` on `127.0.0.1:17890`.
- The installed service is an older baseline: `/v1/runtime` reports `configured=false`, `running=false`; `/v1/observatory/settings`, `/v1/health/monitor`, `/v1/events`, `/v1/watchlist/status` and `/v1/geodata/status` return 404.
- The installed service has not been replaced or restarted during this work. No new source changes have been deployed to NanoPi.
- `hydraroute-login.service`, nginx and `x-ui.service` are already active; no changes were made to them or to any Xray process.

## Safety boundary

This phase made source/CI changes only. The pre-existing NanoPi Core API service was inspected read-only and left untouched. No changes were made to Keenetic/Entware HydraRoute Neo, 3x-ui/Xray, Tailscale, Direct rules, system DNS, host routes, firewall or NAT.

## Additional Observatory audit findings

- The manual ping stream previously persisted the probe cache once per newly observed outbound. It now batches in-memory updates and persists once when the stream exits, preserving the complete Xray Observatory status including health-average/failure fields.
- The manual ping SSE clears the API server's global 60-second write deadline so configured Observatory cycles longer than one minute are not silently cut off.
- ICMP probing is bounded to 8 concurrent workers and a 20-second overall phase; remaining echo results are explicitly reported as unavailable rather than leaving nodes indefinitely pending.
- Disabled profiles now return stale cached Observatory data and do not wait for probe results that cannot arrive.

## Additional DNS correctness and lifecycle hardening — 2026-11-10

- Fixed blocked AAAA and non-A DNS query handling: blocked names now return an empty NOERROR answer for AAAA and do not fall through to upstream resolution. A records retain the explicit `0.0.0.0` sink response.
- Added a live UDP resolver integration test with a separate upstream: verifies blocked A/AAAA requests never reach upstream and an allowed AAAA request does.
- Do53 now retries over TCP when the upstream UDP response has the TC bit set; a paired UDP/TCP upstream test verifies the fallback.
- DoH endpoints must be HTTPS, have a valid host and no embedded userinfo/fragment; redirects are rejected if they downgrade from HTTPS.
- DNS configuration changes are transactional across the resolver config and Core Xray DNS settings. Failed listener startup restores the previous persisted configuration; DNS stop/start failures attempt to restore the previous service/runtime state.
- DNS service/check API calls now return 503 rather than dereferencing an absent DNS manager when no state directory is configured.
- API service startup now closes a DNS listener it started if the API listener cannot bind; unexpected API listener termination also stops the DNS service and managed Xray runtime.
- Corrected DNS check UI copy: the operation uses the configured Core upstreams, not the host system resolver.
- Added regression tests for failed DNS startup rollback and API bind-failure cleanup.

Latest local validation after these changes:
- `go test -count=1 ./...` — PASS.
- `go vet ./...` — PASS.
- `go test -race -count=1 ./internal/dnsservice ./internal/events ./internal/health ./internal/api` — PASS.
- `gofmt` and `git diff --check` — PASS.
- Node.js is not installed on the VPS; dashboard syntax and Playwright remain GitHub CI gates. CI #198 passed before the additional DNS hardening in this section; a new CI run is still required for these final changes.
- Root filesystem has only about 92 MiB free; no existing cache, archive, backup or research material was deleted.

## Compatibility audit expansion

- Added `2026-11-10-HRWEB-114-METHOD-COMPATIBILITY-MATRIX.md`: all 114 regular HRweb 1.68.0 methods are individually classified, with Core endpoint mapping, adaptation status, known gaps and explicit Keenetic/RCI exclusions. The separate `proxy.pingServers` stream is included as item 115.
- The matrix is conservative: no 1:1 compatibility is claimed from a similar endpoint name alone. Phobos, FQDN groups, speed tests, file APIs, system update workflow, full Geo list management and DNS blocklist lifecycle remain open.
