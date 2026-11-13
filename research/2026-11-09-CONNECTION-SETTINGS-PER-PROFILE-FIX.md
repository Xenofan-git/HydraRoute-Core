# Per-profile connection settings — implementation audit and fix

Date: 2026-11-09
Working tree: `/root/hr-hydraroute-build/src/hydraroute-core/`
Branch: `integration/nanopi-ui-20261109`

## User-visible reference
The supplied HRNeo screenshots establish that the gear menu belongs to each individual connection card. The three settings are therefore profile-scoped: DPI bypass (noise traffic, TLS fragmentation, random TLS fingerprint), balancing strategy (least latency, random server, round robin), and outgoing interface (automatic/system routing or a selected Linux interface grouped as VPN / Internet / Other).

## Audit findings
1. NanoPi dashboard had real connection cards and the gear menu, but DPI and outgoing-interface clicks were toast-only placeholders.
2. Balancing opened a settings form and was already persisted as a profile property, but the gear-menu behavior was not consistent with the original inline menu.
3. Compiler `RoutingConfig.DPIBypass` applied the same DPI flags to every profile outbound. This was a scope bug: changing one connection could affect all connections.
4. `profiles.Profile` did not persist DPI settings; metadata returned to the UI did not expose per-profile DPI/interface values.
5. Outgoing interface had no complete profile-level API/UI contract in this source tree.

## Changes made
- Added `DPIBypass` to the persisted encrypted profile model and credential-free profile metadata.
- Added `DPIBypass` and `OutboundInterface` to compiler profiles and subscription compile inputs.
- Removed global DPI bypass from `RoutingConfig`; compiler now decorates only the outbound nodes belonging to the profile carrying the option.
- Added per-profile API update support for `dpi_bypass`, `strategy`, and `outbound_interface`. Changes are saved atomically through the existing encrypted profile store; when a prepared configuration exists, the existing refresh/rollback path is used.
- Added read-only `GET /v1/interfaces`: enumerates Linux network devices, excludes loopback, classifies VPN / Internet / Other, marks link state and detects the default IPv4 route interface. It does not alter routes, link state, firewall, or host DNS.
- Compiler writes selected interface to each eligible outbound's `streamSettings.sockopt.interface`; empty value means no socket bind and leaves system routing in control.
- Added validation for interface names and current interface existence on API update and profile refresh. A vanished selected interface fails closed instead of silently reverting to automatic routing.
- Implemented card-local DPI, balancing, and outgoing-interface panels in the dashboard; the interface picker has search, categories, link status and the explicit automatic/system-routing choice.
- Added regression tests proving profile A's DPI/interface settings do not leak into profile B, persisted metadata is returned without subscription secrets, and the saved profile settings reach generated Xray outbounds. Added browser interaction coverage for all three menu items.

## Validation
- `go test -count=1 ./...` — PASS.
- `go vet ./...` — PASS.
- `gofmt` and `git diff --check` — PASS.
- Compiler test verifies `sockopt.interface` and profile-isolated DPI output. API integration test performs profile update, encrypted persistence, metadata read, refresh/compile and inspects generated outbounds.
- Browser test source now covers DPI checkbox save, balancing radio selection and outgoing interface selection across two profiles. It has not yet been executed locally because Node.js is not installed on the VPS. A temporary Node runtime extraction was attempted but aborted on low disk space; all partial files were removed.

## Important limitations / next gates
- The selected interface is Xray socket binding (`sockopt.interface`), not a new Linux policy route or global route. No `ip route`, `ip rule`, firewall, NAT, or TUN changes are made.
- Linux interface binding was checked with an unprivileged socket test on the VPS; it succeeded without adding capabilities. Full Xray traffic-path validation on NanoPi remains a separate isolated test gate.
- No application binary was installed, no Core service was restarted, no live profile/runtime was changed, and no NanoPi/Keenetic/3x-ui/HydraRoute Neo/Reolink configuration was touched.
- The current edits are on the VPS-local integration branch. GitHub PR/CI has not yet been updated from this local branch; do not describe these local test results as a new green GitHub Actions run.
- VPS root filesystem was 99% full (about 245 MiB free) after the aborted Node extraction; temporary Node files were removed. Do not install large toolchains or delete existing artifacts/backups without reviewing disk usage and user approval.

## VPS storage cleanup — 2026-11-09
- Initial root filesystem: 14 GB, 99% used, 241 MB free.
- Inspected `/root`, `/var`, build workspace, temporary files, active processes and mounts. The 409 MB OpenWrt/Podkop image under `/var/tmp/openwrt-podkop` is retained as prior router research material; no active build/mount was found, but it was not deleted. HRweb disassembly, release archives, build artifacts, source snapshots and backups were also retained.
- Cleared only the reproducible Go build cache (`go clean -cache`, about 707 MB), apt downloaded package cache (16 KB) and old archived systemd journal files (48 MB; retained current journal). No application or project artifact was removed.
- Result: 1.1 GB free, 92% used. Inodes 14% used. Go build cache will regenerate on next build.
- Reolink protection files on VPS and Keenetic were read before cleanup. Four direct Reolink systemd services and `nanopi-via-keenetic.service` remained active. No camera/tunnel/network service changed.

## GitHub transfer status
- GitHub connector is available, but VPS has no authenticated `gh` CLI and no configured Git remote/credential helper.
- Inspected current GitHub feature branch source: it differs materially from VPS integration source. In particular, GitHub branch currently has global `RoutingConfig.DPIBypass` and global balancing override; its API has no profile PUT endpoint, while local integration implements encrypted per-profile settings. GitHub UI also still stores DPI/interface settings globally, whereas local integration uses the newer NanoPi card-based UI.
- A wholesale file overwrite would lose or mismatch current branch features; therefore no unsafe blind push/merge was made. GitHub PR remains unchanged. Next transfer must be a deliberate source-level integration of the VPS backend contract into a branch based on current GitHub feature head, then CI validation.

## GitHub integration transfer — 2026-11-09
- Created remote branch `integration/per-profile-connection-settings-20261109` from the exact feature head `801b4f75f430165123e72b6f73d6ec4459f07f95`; original feature branch and PR were not rewritten.
- Transferred the tested VPS integration files using GitHub Git Data API (blobs/tree/commit), including API/compiler/profile-store/service/UI/browser tests, DNS service and tests, and pinned `go.mod`/`go.sum`.
- Remote commit: `387ed4a3c918157254d8a12f151422906aab5c1f` (`Integrate per-profile connection settings and DNS service`). Branch ref update succeeded with expected-head check; no force update.
- GitHub Actions run #153 / run ID `42537249947` triggered by push. At journal time it was `in_progress`; setup Go was running, test/build steps had not yet reported results. Do not claim CI green until all steps and both architecture build jobs finish successfully.
- No PR was created yet, no merge performed, no binaries deployed to NanoPi. Next: inspect run #153 and fix any failures on this integration branch only.

## Subscription manager modal — 2026-11-09
- Replaced the separate server-picker page with an HRNeo-style modal opened from each connection card's `Серверы...` menu item. It overlays the current connections page and includes a scrollable outbound table, search, select-all, status columns, update/filter tabs, and Save/Cancel actions. Header and control styling follows the supplied references (compact system sans-serif, dark blue panel, amber accents).
- Reused the existing credential-free profile preview and selection APIs. The modal supports manual selection, keyword include-only and exclude filters, and preserves missing previously selected identities.
- Added persistent automatic subscription refresh settings (`refresh-settings.json`, mode 0600) and `PUT /v1/refresh/status`. Scheduler now runs while disabled, wakes when settings change, and uses bounded retry backoff after a failed refresh. The interval is global across stored subscriptions, not per-profile.
- Added intentional empty manual pool semantics: saving zero selected outbounds is valid; compiler omits that profile's inbound/balancer rather than emitting an invalid empty balancer. Existing missing selected identities still fail closed on compile.
- Added API/compiler regression tests and updated UI/browser tests for modal, select-all, search, refresh interval persistence and empty-pool behavior.
- VPS validation after changes: `go test -count=1 ./...`, `go vet ./...`, `gofmt`, `git diff --check` passed.
- GitHub integration branch advanced to `5826c33f92ff7addff2b643a5af30f87cb218296`; PR #2 remains draft. Actions #155/#156 are in progress at the time of this journal update.
- Known unfinished part: Xray Observatory's live per-outbound status and explicit forced-probe API are not yet integrated. The modal currently shows status placeholders; `Опросить серверы` reports that the Xray API integration is pending, and the `offline` checkbox does not yet remove failed nodes. Do not represent these two controls as operational until Xray's Observatory gRPC service is wired and tested.
- No NanoPi deployment, Xray restart, profile runtime change, network route, firewall, DNS, Tailscale, Keenetic, HydraRoute Neo, 3x-ui, CSQTT or Reolink change was performed.

## Browser CI correction — 2026-11-09
- Runs #155 and #156 confirmed all Go tests, Xray config validation and `go vet` passed, but browser tests found three assertion issues: stale expected navigation/card counts and the test not removing the preserved missing-picked identity from the new modal.
- Corrected the browser assertions to normalize icon-prefixed navigation labels, match the two-rule fixture, and explicitly forget the missing identity before checking saved selection. The empty-pool browser scenario passes in the previous run.
- Pushed test-only commit `b2ed8781004c301e9a2e11b290db628143393046`; GitHub Actions run #157 is validating it. Build matrix has not yet run because it depends on the test job.

## CI source-encoding correction — 2026-11-09
- Browser run #157 caught a transfer-wrapper issue: the test source was uploaded with the SSH status prefix included, so GitHub stored invalid bytes even though the local source was correct. This was isolated to `tests/browser/dashboard.test.cjs`; application source was unaffected.
- Re-uploaded the file from an explicitly delimited base64 payload and verified the GitHub-fetched UTF-8 source begins with the expected Node test imports and contains the subscription modal tests.
- Latest branch commit: `f7523df91f43d137a94abdd25f8dd2a6b94ffb21`. Actions #159/#160 are in progress; formatting and JavaScript syntax steps have passed, Go tests are running. No green CI claim yet.

## Latest CI result — 2026-11-09
- Corrected source transfer and latest head `f7523df91f43d137a94abdd25f8dd2a6b94ffb21` passed the complete `Format, test and vet` job, including browser interactions (Actions run #159 / ID `42538434576`).
- Both `Build Linux amd64` and `Build Linux arm64` jobs have started and were still running at the time of this update. Run #159 is not yet fully green until both builds complete.
- A second PR-triggered run #160 is also validating the same head.
- Observatory manual force-probe and offline filtering remain intentionally unfinished; the UI displays the controls but does not fake results. Auto-refresh currently applies globally to all stored subscriptions.

## Full GitHub Actions validation passed — 2026-11-09
- Actions run #159 (`42538434576`) completed with conclusion `success` for commit `f7523df91f43d137a94abdd25f8dd2a6b94ffb21`.
- Passed: formatting, dashboard JavaScript syntax, all Go tests, Xray generated-config validation, `go vet`, Playwright browser interaction tests, static AMD64 build, static ARM64 build, and artifact uploads.
- PR #2 is still draft and explicitly records remaining work: Xray Observatory gRPC status/forced probe and offline exclusion are not yet operational; refresh interval is global rather than per-profile.
- No merge or production deployment. No NanoPi/Xray process or network settings changed.
