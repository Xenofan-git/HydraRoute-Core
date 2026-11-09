
## 2026-11-09 — Full HRNeo functional parity scope
- Зафиксирована цель переноса всего функционала оригинального HydraRoute Neo в независимый Debian/Ubuntu ARM64 HydraRoute Core.
- Добавлена матрица функциональных групп: профили/подписки/балансировка, proxy lifecycle, Phobos/WireGuard, DNS, Geo/FQDN, health/events/system/speed, API/UI/files.
- Зафиксированы ограничения: никакого глобального маршрутизирования, Keenetic RCI/NDM, изменений DNS/firewall, вмешательства в 3x-ui/HydraRoute Neo/Direct/Reolink.
- Текущие незавершённые изменения остаются в рабочей копии VPS; GitHub/NanoPi не обновлялись.

## 2026-11-09 — Observatory integration work (VPS source, not deployed)

- Xray compiler now retains every parsed profile outbound for Observatory, while the profile balancer selector still contains only manually selected, non-excluded nodes. This allows excluded/offline nodes to be observed and recover without entering the pool automatically.
- Added deterministic per-profile node outbound tags and a loopback-only Xray Observatory gRPC API endpoint.
- Core reads real Xray `GetOutboundStatus` results, separates proxy delay from ICMP echo, and streams per-node results to the server picker.
- Added a private, atomic, mode-0600 persistent Observatory status cache; cached measurements remain visible when Xray is stopped or its API is temporarily unavailable.
- Server picker refreshes live status while open without re-rendering the modal or wiping unsaved form fields. Added clear unavailable-state messaging.
- Set a conservative 30-second Observatory cycle. The original UI exposes a configurable interval (5–3600 seconds); the Core settings control and runtime reconfiguration are still pending.
- Updated the manual-selection regression test to assert that excluded outbounds remain observable but are absent from the balancer.
- Validation: `go test ./...` and `go vet ./...` passed on VPS. Browser/Playwright checks were not run (Node.js is unavailable).
- Not deployed to NanoPi: its live Core runtime currently reports unconfigured/stopped, and the active Foreign subscription previously failed parsing. No live VPN, route, firewall, DNS, or service changes were made.

### Observatory parity — interval configuration (VPS source, not deployed)

- Added a global Observatory probe interval setting in the Core panel (5–3600 seconds), persisted separately under the private state directory with mode 0600.
- Added same-origin API `GET/PUT /v1/observatory/settings`, validation, and persistence/reload tests.
- The setting is injected into direct config builds and subscription refresh builds.
- Matched HRNeo's interval derivation: configured seconds are converted to milliseconds and divided by the number of all observed outbound subjects; the result has a 100 ms floor. The live HRNeo configuration corroborates this exactly: 1200 seconds / 104 subjects = 11538 ms.
- Matched the original probe URL: `https://www.google.com/generate_204`.
- The setting is used on the next Core config build; changing it does not forcibly restart a running Xray process.
- Tests cover interval distribution, the 100 ms floor, persistence, invalid values, and the original URL.
- No live HRNeo/Keenetic configuration or NanoPi runtime was changed. This source work remains uncommitted and undeployed.
- Adjusted the manual Observatory stream timeout to honor the configured global cycle (up to 3600 seconds plus startup margin), with SSE keepalives. The UI now aborts the stream when the picker closes or selection is saved.
- Serialized cache writes and bounded the in-memory/on-disk cache to the newest 4096 measurements to avoid stale concurrent snapshots and unbounded growth.

### 2026-11-09 — Observatory polling and ICMP resource-safety follow-up
- Corrected the SSE completion error so it no longer claims a fixed 40-second timeout; the stream deadline is based on the configured Observatory interval plus its grace period.
- Bounded ICMP/DNS probe concurrency to eight nodes per batch, avoiding an unbounded burst of ping processes on large subscriptions while retaining separate ICMP echo and Xray proxy-delay events.
- Added a cache-only profile preview path for Observatory status and manual probe streams. Periodic status polling no longer triggers remote subscription fetches when a cached subscription is empty; interactive subscription preview remains the fetch-capable path.
- Added a regression test proving the cache-only preview rejects an empty remote-subscription cache without attempting network access.
- Validation: `GOMAXPROCS=1 go test ./...`, `GOMAXPROCS=1 go vet ./...`, and `git diff --check` passed. No deployment, service restart, network/routing change, or commit was performed.
- VPS storage remains critically constrained (about 441 MiB free); race-instrumented builds remain deferred.
- Hardened the dashboard SSE reader: an unexpected EOF without the server's terminal `done` event is now reported as an interrupted probe rather than a false success; added a UI source regression assertion.

### 2026-11-09 — Observatory freshness and cache-generation audit
- Moved the manual probe freshness window to the start of the request, before the bounded ICMP phase. Fresh Xray observations completed while ICMP checks are running are no longer discarded and forced to wait for another cycle.
- Added a strict timestamp-boundary helper/test for Xray `LastTryTime` (Unix-second precision): same-second or older values are not mislabeled as fresh.
- Cache reads now verify that a stored status belongs to the current profile's generated outbound tag. Reusing a profile ID after changing its source/configuration can no longer display measurements from the previous outbound generation; stale entries are discarded during status collection.
- Validation: `GOMAXPROCS=1 go test ./...`, `GOMAXPROCS=1 go vet ./...`, and `git diff --check` passed.
- Source remains uncommitted and undeployed. No active service, network configuration, routes, DNS, firewall, or camera tunnel was changed.

### 2026-11-09 — Observatory cache write amplification fix
- `collectObservatoryStatus` no longer treats an unchanged Xray status with the same `LastTryTime` as a new cache update. Previously, `>=` could rewrite the status file on every poll even when the measurement was identical.
- Cache replacement now occurs for a newer probe timestamp, or for a same-second result only when one of the actual status fields changed. Added regression coverage for identical status and changed delay/last-seen.
- Targeted API/compiler/service tests and `git diff --check` passed. Full suite and vet had passed immediately before this optimization; rerun after the next implementation batch.

### 2026-11-09 — VPS disk cleanup
- Freed space without touching research, project artifacts, backups, archives, or camera tunnels: removed regenerable `/root/.cache/go-build` and abandoned `/tmp/go-build*` compiler temp directories; removed rotated system logs only (old auth/syslog/kern log rotations; active logs retained); vacuumed archived systemd journal to a 20 MiB target.
- Root filesystem increased from 431 MiB free / 97% used to approximately 975 MiB free / 93% used immediately after cleanup. Later Go test rebuilds used some cache space; latest check showed approximately 795 MiB free / 95% used.
- Preserved `/var/tmp/openwrt-podkop`, `/root/hrweb-research`, project source, backups, and all active logs. Reolink guard files on VPS and Keenetic were read before cleanup; no camera-related files/services were modified.

## 2026-11-09 — Observatory stale-state correctness
- Added explicit `stale` metadata to cached Observatory status responses. When Xray is stopped/unavailable, or a current outbound tag has no current Xray status and only a cached measurement is available, the retained measurement is marked stale.
- UI labels stale measurements as `устарело`, rather than presenting an old `alive=false` result as current `offline`.
- Stale measurements are excluded from offline auto-selection and are ranked as unknown for sorting. This prevents stale cache from driving pool exclusion decisions.
- Tightened `since` freshness checks to strict `LastTryTime > since`, consistent with Unix-second precision and the SSE contract.
- Added API/UI regression assertions. Targeted `go test` and `go vet` for api, ui, compiler and xrayapi passed; `git diff --check` passed.
- No runtime deployment, service restart, network change, or commit/push performed.

## 2026-11-09 — Observatory: original offline-selection semantics and stale guard

- Re-read original HRNeo `server-selection.js`: `isOffline` is based on a completed manual proxy-ping result (`ping` exists and is not pending and `alive=false`); ICMP echo is a separate rank/diagnostic. The bulk `offline` checkbox only applies to the currently visible non-filtered rows and calls `setExcluded` for those offline keys. The UI does not demonstrate automatic server reinclusion on recovery; excluded membership is persisted separately from health.
- Confirmed original UI strategy choices are exactly `leastPing`, `random`, `roundRobin`. Default interface strategy is `leastPing` (also confirmed in `interface-meta-mutation-audit.md`).
- The research closeout records a recovered original outbound-tag format with ping state 0=alive, 1=no record, 2=dead; delay is encoded only for alive and capped at 99,999ms. It also records original fallbackTag priority: last alive outbound matching the interface reference URI; then minimum-delay alive URI match; then first selected outbound. This is a significant parity item not yet implemented/verified in Core.
- Core stale-state fix was completed: cached Observatory status is explicitly `stale=true`; UI displays `устарело`, excludes stale records from offline sorting/selection, and the bulk offline checkbox now also refuses to change membership based on stale measurements.
- Added UI regression assertion for the bulk stale guard. Full `GOMAXPROCS=1 go test ./...`, `go vet ./...`, `git diff --check` pass.
- No runtime deployment, restart, commit or push.

## 2026-11-09 — HRNeo active-balancer API contract recovered

- Disassembled original HRweb `main.balancerActiveTag` and confirmed its gRPC method is `xray.app.router.command.RoutingService/GetBalancerInfo` (not ObservatoryService).
- Recovered the protobuf response path consumed by the original function: `GetBalancerInfoResponse.balancer` (field 1) → `BalancerMsg.principle_target` (field 6) → `PrincipleTargetInfo.tag` (field 1). The original function specifically reads the principle-target tag list; it does not infer the active tag from ICMP or Observatory delay.
- Added a minimal dynamic protobuf descriptor/client for this exact method in `internal/xrayapi/balancer.go`; this avoids importing the entire Xray source tree and keeps dependencies within the existing gRPC/protobuf modules.
- Added wire-contract regression coverage for request tag encoding and nested response decoding.
- `GOMAXPROCS=1 go test ./...`, `GOMAXPROCS=1 go vet ./...`, and `git diff --check` pass.
- This is an API client foundation only: it is not yet wired into config refresh/fallbackTag calculation. The original fallback candidate ordering still requires integration and end-to-end verification. No Xray process was started/restarted, no NanoPi deployment, no route/network changes, no commit/push.

### 2026-11-09 — principle-target semantic correction

- Inspected upstream Xray `app/router/command/command.go` and `app/router/balancing.go`: `GetBalancerInfo` obtains `BalancerPrincipleTarget.GetPrincipleTarget`; Round Robin returns the entire candidate slice. Therefore `principle_target.tag` must not be mistaken for the actual connection-active outbound.
- Renamed Core client methods to `GetBalancerPrincipleTargetTags` / `GetBalancerPrincipleTargetTag`; comments explicitly state the semantic boundary.
- Next research step: disassemble the complete original HRweb scalar-selection logic and establish how it chooses a single URI reference from this candidate list (or from override/other state) before implementing fallbackTag.

### 2026-11-09 — exact HRWeb scalar protobuf extraction semantics

- Disassembled the full `main.balancerActiveTag` function at `0x380270` and its helper `main.protoField.func1` at `0x380b80` using AArch64 objdump.
- Confirmed call chain: `xrayAPICall` operation ID 54 → protobuf field 1 → field 6 → field 1. The request operation is distinct from Observatory status (operation ID 70).
- Crucial detail: `protoField.func1` writes the matching field's value into its captured result each time the callback sees that field number. Since `PrincipleTargetInfo.tag` is repeated, subsequent occurrences overwrite earlier ones. HRWeb's scalar `balancerActiveTag` therefore resolves to the **last repeated tag**, not the first.
- Updated `GetBalancerPrincipleTargetTag` to return the last tag; added a repeated-tag regression fixture proving the scalar last-value behavior.
- Full `GOMAXPROCS=1 go test ./...`, `GOMAXPROCS=1 go vet ./...`, and `git diff --check` pass.
- Still not a confirmed actual connection-active outbound: upstream Xray exposes principle-target candidates; HRWeb's scalar helper returns the last candidate. Core keeps that distinction explicit. Fallback URI resolution and priority are not wired yet.

## 2026-11-09 — HRNeo active server API research

- Traced frontend `proxy.activeServers` usage and handler `main.proxyActiveServers`.
- Identified separate `activeBalancerLinks` and `activeBalancerServers` helpers; the active-server map is built after active-link enumeration, balancer tag lookup, and candidate validation. It is not equivalent to returning the last Xray `PrincipleTargetInfo.tag` directly.
- Kept the URI reference field memory-only; no raw subscription URI is exposed through API or persistence.
- No runtime, routing, service, or network changes made.

## 2026-11-09 — HRNeo active-server reverse engineering follow-up

- Disassembled helper `0x37e4d0`: bounded string/byte checks followed by structured-field extraction and a boolean-like validation result; exact semantic field names remain unproven.
- Disassembled `activeBalancerLinks` (`0x38d280`): builds a filtered active-link collection, setting a record flag and attaching derived metadata.
- Confirmed `activeBalancerServers` resolves each link, queries balancer target tags, and validates candidate records before producing per-interface active-server state.
- Deliberately did not implement a speculative URI fallback algorithm.

- 2026-11-09: HRNeo balancer reverse engineering corrected Go symbol assignments and documented `InterfaceMeta.servers()` → `InterfaceMeta.pool()` → `slices.DeleteFunc` filtering pipeline. No runtime changes.
- 2026-11-09: Continued offline disassembly of `loadAllInterfaceMetas`; documented embedded metadata query descriptors, per-record helper/normalization flow, and URL-delimiter parser evidence. Semantics remain provisional; no runtime changes.
- 2026-11-09: Confirmed `metaServer` field layout and `pool.func1`: pool removes only Unsupported records when neither Filtered nor Excluded; this is not health filtering. `activeBalancerLinks` selects records whose Filtered flag is set. Documented corrected active-server pipeline.
- 2026-11-09: Disassembled `xrayServerName`: resolves interface metadata by its identifier field, uses direct Link-name extraction for one-server pools, and delegates multi-server pools to `activeBalancerServers`. Active resolution parses Xray tags and candidate share URLs; exact identity fields remain under investigation.
- 2026-11-09: Continued decoding `parseShareURL`; established it is a structured URL parser/normalizer wrapper with optional custom handler, not raw string equality. Exact identity fields remain unproven.
- 2026-11-09: Deepened HRNeo active-server disassembly: `parseShareURL` dispatches by registered scheme handler with generic fallback; `parseOutboundTag` performs delimiter-based structured parsing (four-part stage) and string-field comparisons; `serverOutbounds` transforms candidate links/options into outbound records. Exact identity field semantics remain unproven.
- 2026-11-09: Recovered exact `ServerKey` type: `{Source string, Name string, Nth int}` (40 bytes); `InterfaceMeta.Picked` and `Excluded` are slices of this key. `pickedIn` is a full-key membership filter, independent of health state.
- 2026-11-09: Disassembled `prunePicked` and `pruneExcluded`: both prune persisted full `ServerKey` selections/exclusions against the current candidate inventory, removing stale identities rather than offline servers. `pick` builds selection from pool candidates and clears Picked for empty input. Tag encode/decode still needs exact semantic mapping.
- 2026-11-09: Caller tracing confirms `outboundTag` is generated during `buildManagedConfig` for Xray outbound entries, while `ServerKey` remains the persisted selection identity. Runtime tag decoding must resolve through metadata candidates; do not conflate tags with ServerKey.
- 2026-11-09: `activeBalancerLinks` retains only `Filtered=true` records and forms the balancer candidate relation; `activeBalancerServers` traverses this relation in stored order and resolves runtime tags against candidate metadata. Full inventory, filtered relation, pool, and manual exclusions are distinct views.
