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
