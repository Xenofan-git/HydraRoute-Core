# HydraRoute Core — Observatory concurrency parity

Date: 2026-11-12

## Reference behavior

The captured live HRNeo configuration uses Xray Observatory with `probeInterval=2m` and `enableConcurrency=true`. The interval is the pause between complete observation cycles; Xray probes selected subjects concurrently. The selected subject set remains restricted to eligible outbound tags in the profile balancer pools.

## Core correction

- Emit `enableConcurrency=true` in the generated Xray config.
- Preserve the configured cycle interval in milliseconds; do not divide it by the number of selected subjects and do not impose a per-subject 100 ms floor.
- Use 120 seconds as the default/fallback interval.
- Apply the same semantics when settings are changed against a prepared Xray config.
- Make the manual profile-ping SSE wait use the full cycle duration.
- Update UI defaults/help text and regression tests.

## Validation

- Full `go test -count=1 ./...`: PASS in the isolated VPS worktree after adding integration coverage.
- `go vet ./...`: PASS.
- `git diff --check`: PASS.
- Static ARM64 integration test binary SHA-256: `cc3ab88d84a9055c11456eba2be602f43d16082ad9b7715e8b036202cdf107ce`.
- On NanoPi, both real-Xray integration tests passed against the installed Xray 26.10.31: four selected subjects reached max in-flight concurrency 4; balancer cadence and fail-closed fallback assertions also passed.
- The Xray test ran as a separate temporary process. API port 16285 was released after the test.

## Preservation boundary

No HydraRoute Core service deployment/restart, no change to the installed Core Xray binary/config, and no changes to Passwall2, Keenetic, original HRNeo/Entware, 3x-ui, DNS, host routes, firewall, iptables/nftables or Tailscale.

## Remaining

- Reconcile the older dirty VPS worktree file-by-file; it is not the current PR branch and has no Git remote.
- Browser tests and GitHub CI remain pending. Current PR #7 CI reruns failed before runner assignment (runner_id=0, no steps/logs); treat this as Actions provisioning failure, not a code-test result.
- Continue the outstanding HRNeo/HRweb/HRDNS parity audit, especially DNS/SmartDNS runtime acceptance and remaining lifecycle/fault/scale cases.
- No deployment is authorized by this report.
