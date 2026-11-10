# HydraRoute Core — Observatory runtime and balancer candidate integration

Date: 2026-11-10

## Implemented in this pass

- Observatory still uses the managed Xray `ObservatoryService` and its real `GetOutboundStatus` measurements; no synthetic proxy latency is generated.
- `subjectSelector` remains restricted to the exact eligible outbound tags emitted into each profile's balancer. Excluded, filtered, and unselected subscription nodes are not probed.
- Added `RoutingService` to the private loopback-only Xray API service list. It is used to read `GetBalancerInfo` for `hr_bal_<profile>`.
- The status response now maps the last `principle_target.tag` returned by Xray back to the current profile's stable `ServerKey` and node name. Unknown or stale tags are ignored. The UI calls this a **balancer candidate**, not a guaranteed active socket; Round Robin may return multiple strategy candidates.
- Saving the Observatory interval now updates an already-prepared Xray config immediately. If Xray is running, Core restarts only its managed Xray process, validates the new config, checks for immediate process exit, and restores the previous config/runtime on failure. If there is no prepared Observatory config, the value is stored for the next build.
- The UI now reports whether the setting updated the prepared config, restarted the running Core Xray process, or is waiting for the next config build.

## Validation

- `GOMAXPROCS=1 go test -count=1 ./internal/api ./internal/compiler ./internal/xrayapi` — PASS.
- `gofmt` and `git diff --check` — PASS.
- GitHub Actions run #200 on the preceding DNS-hardening commit completed successfully, including Go tests/vet, dashboard JavaScript checks, HTTP/TLS/QUIC L7 integration, browser tests, and Linux ARM64/AMD64 builds. This is not validation of the Observatory changes in this document; a new CI run is required after commit.

## Known remaining work

- Complete fallback-tag parity in the balancer compiler: resolve the previous runtime candidate against the next profile generation, then apply the recovered last-alive / minimum-delay-alive / first-selected fallback priority. Keep all matching strictly profile-local. This is not claimed complete here.
- Add an end-to-end test against a real Xray binary that verifies `RoutingService.GetBalancerInfo` and candidate mapping for least-ping and round-robin strategies.
- Verify the full periodic probe cadence on ARM64. Xray exposes status retrieval but no force-probe RPC; the manual UI action waits for the next real configured observation cycle. ICMP echo remains a separate measurement from proxy delay.

No NanoPi deployment, Xray restart on NanoPi, host route/firewall/DNS change, or Keenetic/Entware modification was performed.