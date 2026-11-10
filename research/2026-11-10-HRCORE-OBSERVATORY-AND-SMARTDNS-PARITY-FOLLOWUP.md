# HydraRoute Core — Observatory parity follow-up

Date: 2026-11-10
Branch: `integration/dns-resolver-hardening-20261110`
Latest implementation commit at report time: `98981d499552161f5e11d99b938003b804ad5577`

## Source baseline

The behavioral reference is HRweb `1.68.0-1` static research, recorded in:
- `/root/hrweb-research/2026-11-07-HYDRAROUTE-ARM64-RESEARCH-CLOSEOUT-SPEC.md`, section 4.
- `/root/hrweb-research/2026-11-05-observatory-interval-provenance-followup.md`.

The recovered fallback priority in closeout spec §4 is:
1. Last alive outbound matching the active balancer's per-interface reference URI.
2. Lowest-delay alive outbound within that same URI family.
3. First selected outbound.
The reference URI is taken from the active outbound of the current balancer; comparison uses the URI prefix before `#`.

**Candidate caveat:** the recovered HRweb callback keeps the last repeated `principle_target.tag` from `GetBalancerInfo`. Static research confirms this is a strategy candidate list (not necessarily the outbound carrying a live connection, especially for Round Robin). Core therefore treats it as a candidate/reference hint only, not as proof of an active socket; unknown tags are ignored.

This is a static reverse-engineering result, not a live execution of HRweb.

## Change made

The Core implementation previously widened the minimum-delay fallback to the entire profile pool when no alive member of the reference URI family existed. That was not part of the recovered HRweb priority.

Commits:
- `9526a6ae8b24fd61736fd46e4e7a5d034fcb7824` — restrict fallback candidates to the active URI family.
- `eb91465ca3be864764a5b14d0e186dd88662198e` — replace the old global-pool test with URI-family tests; add no-widening and missing-reference cases.
- `98981d499552161f5e11d99b938003b804ad5577` — fix declaration ordering after the behavior change.

Current intended sequence:
- Retain the previous runtime candidate only if it remains selected, alive, URI-compatible, and belongs to the active reference family.
- Otherwise retain the last alive member of that family in original pool order.
- Otherwise choose the minimum-delay alive member of that family.
- Otherwise use the first selected member of the same profile.
- Never use another profile or global `direct` as fallback.

URI identity remains compared without the display fragment after `#`; stale Observatory status whose URI identity changed is rejected.

## Validation performed

A temporary test directory was created under `/tmp/hrcore-fallback-check`; the VPS worktree itself was not modified. The exact `fallback.go` and `fallback_test.go` contents were fetched from the GitHub branch and overlaid into that temporary copy.

Using Go 1.27.3:
- `go test -count=1 ./internal/service` — PASS.
- `go test -count=1 ./...` — PASS for the temporary test tree.
- `go vet ./...` — PASS.
- `HRCORE_XRAY_INTEGRATION=1 XRAY_BINARY=/root/hr-hydraroute-build/toolchain/xray-v26.10.31-amd64/xray go test -count=1 -v ./tests/integration` — PASS; actual Xray 26.10.31 Observatory/balancer/fail-closed and leastPing/roundRobin runtime tests passed.

**Validation caveat:** the VPS source worktree is still the preserved older `integration/nanopi-ui-20261109` worktree with local modifications. Only the exact current-branch fallback implementation and its tests were overlaid in the temporary copy. These local results are useful but do not replace GitHub CI on the current branch head.

## Observatory items still not proven at exact HRweb parity

The probe interval is still provisional. The current Core compiler normalizes invalid values to 30 seconds, divides the configured seconds by the total selected subject count, and applies a 100 ms floor. HRweb static analysis found a candidate interval arithmetic path at `main.rebuildXRayConfigs`, but the source field is a `*int` and the observed stack value is not yet proven to be the dereferenced configured value. The divisor appears to be the length of a dynamically grown string slice, but its exact semantic role was not conclusively established. Do not describe the current interval formula as exact HRweb parity until that dataflow is closed.

The strategy surface discovered in HRweb is `leastPing`, `random`, and `roundRobin`. Core has actual Xray runtime tests for leastPing and roundRobin; random still needs a deterministic/behavioral test strategy, plus update/restart/subscription-removal lifecycle parity checks.

## SmartDNS ARM64 runtime gate

Workflow run `42828062739` on commit `76a153dbd0b4b864bd7fffc353bd3866d1146b8c`:
- AMD64 SmartDNS build — PASS.
- ARM64 SmartDNS build — PASS.
- AMD64 isolation sandbox — PASS.
- ARM64 QEMU isolation sandbox — FAIL: SmartDNS exited early; the harness log was empty.

Diagnostic changes:
- `c8e76e5cb64d2f052de9e269ef97e0a878db7130` adds QEMU syscall tracing to the ARM64 version preflight and avoids wrapping QEMU with host `strace`.
- `76dad2e22d803e0d240b9c3b60afef56179b3a6f` adds QEMU syscall tracing to the runtime wrapper so the next run can capture the actual startup failure.

The QEMU root cause remains unresolved until a fresh job log is inspected. Do not claim ARM64 SmartDNS runtime parity from a successful cross-build.

## Original DNS-path boundary

Original HRNeo's DNS capture is a passive `AF_PACKET SOCK_DGRAM/ETH_P_ALL` + L3-BPF path that observes DNS answers on LAN/VPN/tunnel interfaces and adds addresses to ipset; `ConntrackFlush` optionally triggers asynchronous conntrack cleanup. The optional Core SmartDNS sidecar is a separate profile-pinned DoH/DoT and domain-set feature. It is not a replacement for HRNeo's passive DNS capture, ipset population, or conntrack behavior. Keep this distinction explicit in the compatibility matrix.

## Deployment boundary

No NanoPi deployment or service restart was performed. No Keenetic/Entware/HRNeo, 3x-ui/Xray, Tailscale, resolver, firewall, route, iptables/nftables, or conntrack state was changed.
