# Xray control API readiness recovery — 2026-11-20

## Problem
Process-only supervision recovers exited Xray, but a live Xray process can stop responding to the Observatory/control gRPC API. The existing Health Monitor reports this as failed but previously did not initiate a restart.

## Isolated development implementation
- `runXrayReadinessRecovery` runs only with AutoStart enabled and a managed Xray supervisor; probes the control API every 10s, with 2s timeout.
- Three consecutive failures for the same PID trigger an attempt to restart that exact PID. Success resets the streak; PID changes reset it too.
- A five-minute cooldown follows attempted restarts. The API operation lock serializes recovery with other managed operations.
- `Supervisor.RestartIfRunning` refuses to restart if the process is no longer running, its PID changed, it is not configured, or the operator has stopped it. Graceful TERM is followed by forced termination after 3s. The desired-running state is preserved.
- Events `xray_unresponsive_restarted` and `xray_unresponsive_restart_failed` record outcomes.
- Unit tests verify stale PID protection, restart and manual-stop preservation. This is not yet a live hang/fault-injection test.

## Caveats / follow-up
- A responsive gRPC control API does not prove end-to-end SOCKS5 forwarding. Add an independent, opt-in, local SOCKS5 handshake probe before claiming full data-plane recovery.
- A nonresponsive control API may be due to an API listener or port configuration issue rather than Xray itself. The three-failure threshold and cooldown reduce restart storms but do not diagnose this distinction.
- Production NanoPi has an older Core build and was not modified. Run isolated ARM64 fault injection before any production rollout.

## Simulated LTE outage (2026-11-20)
Added `TestSimulatedLTEOutageRecovery`: local HTTP upstream returns 204 when link-up and 503 when link-down; Core's existing recovery retry loop is run against this deterministic connectivity gate. Test confirms initial reachability, failure detection during simulated outage, and successful retry after restoration (~5s). This does **not** change the system's actual LTE interface, DNS, routes or Tailscale, and does **not** validate real Xray/SOCKS5 data-plane restoration. An isolated ARM64 VM network-namespace/packet-loss test with managed Xray remains necessary.

## ARM64 VM live fault injection (2026-11-20)
VM: existing isolated QEMU Debian aarch64 under `/srv/development/isolated-checkouts/hrcore-arm64-vm`, SSH hostfwd 127.0.0.1:22228. Deployed a test build of the current branch **only inside the VM**, preserving its former binary at `/tmp/hrapi-before-readiness-test`.

- Started with managed Xray PID 7544. Issued `SIGSTOP` to that PID to emulate a live but unresponsive process. After 55 seconds, `/v1/runtime` reported PID 7598 and `/v1/events` contained `xray_unresponsive_restarted`. Both Xray and SOCKS5 health checks initially became degraded and later recovered to healthy. This validates automatic recovery under real ARM64 process hang.
- Within the five-minute cooldown, issued a second `SIGSTOP` to PID 7598 for 42 seconds, then `SIGCONT`. PID remained 7598 and the number of `xray_unresponsive_restarted` events remained exactly one. This validates no immediate restart loop during cooldown.
- SOCKS5 Health Monitor now performs a local RFC1928 no-auth greeting for enabled loopback profile inbounds. It never calls a public test host; thus an LTE outage cannot itself mark the local SOCKS5 handshake as failed. It detects whether the local inbound accepts a greeting, **not** whether proxied outbound traffic reaches the Internet. A separate end-to-end probe must be informational and must not trigger restarts on WAN loss.
- No production NanoPi, Keenetic, MTS or Beeline configuration was changed.
