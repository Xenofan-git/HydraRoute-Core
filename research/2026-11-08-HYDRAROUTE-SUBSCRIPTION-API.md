# HydraRoute Core — subscription API and persisted selections

Date: 2026-11-08

## Delivered

- Extracted subscription/profile request orchestration into `internal/service` for reuse by CLI and HTTP API.
- Added `POST /v1/subscriptions/compile`: accepts a single profile or a list of independent profiles; each profile may provide direct links, an inline subscription body, or an HTTP(S) subscription URL.
- Reuses the existing parser and hardened subscription fetcher (public IP validation, redirect validation, HTTPS downgrade rejection, bounded download size and timeout).
- Added `PUT /v1/selections` and `GET /v1/selections?profile_id=...`.
- Selection files are named from validated profile IDs inside a private state directory; arbitrary state paths are not accepted over HTTP.
- Persisted manual selections are reconciled against current parsed server identities. Missing picked/excluded identities remain in state; a manual profile with no currently available picked node fails closed.
- Subscription compilation calls the supervisor's Prepare path, which validates via Xray `run -test`; it does not start Xray.
- Strict JSON decoding, request limits, loopback/same-origin checks and existing API timeouts apply.
- Updated CLI API entrypoint with `-state-dir` (default `/var/lib/hydraroute/state`).

## Verification

- `go test -count=1 ./...` passed.
- `go vet ./...` passed.
- 82 test functions across the module.
- Built static Linux AMD64 and ARM64 binaries for `hrapi`, `hrcore`, and `hrparse`.

## Artifact SHA-256

- `hrapi-linux-amd64`: `76b19748121fd84ba98a74c366e0ce66451647de6d55070d846a7e4e19ede267`
- `hrapi-linux-arm64`: `6d119c0b2e8d37076e4323d6ea89b337afd6a5debc8459fb28b47e09fd9d5a94`
- `hrcore-linux-amd64`: `6f11686776a0cb1947d2a3a40010590dca881c724626933788c2920c8e9fbff8`
- `hrcore-linux-arm64`: `7e1fed414e1e09cc4950e1a983302352cdd3bed2347d23d911542f25965675cb`
- `hrparse-linux-amd64`: `e5c2e02971fe811573cd861c84230004378ded8b299c3c79c92cf85e229d01e2`
- `hrparse-linux-arm64`: `822396db6fcb831a139beb9800ca4ef3e521cc424e3e483c440c597939fac00b`

## Safety boundary

No production service, existing Xray/3x-ui process, route, firewall, DNS, interface, Keenetic setting, HydraRoute Neo/Entware configuration, or Tailscale configuration was modified. Subscription API and state endpoints have not yet been deployed to the NanoPi production environment.

## Next

1. Copy source and ARM64 binaries to NanoPi after a timestamped backup.
2. Run hrapi and actual Xray only inside an isolated network namespace with temporary test paths and ports.
3. Verify selection save/load, subscription URL SSRF rejection, profile isolation, prepare/start/stop and that no production listener/routes change.
4. Update NanoPi research journal and build manifest.

## NanoPi ARM64 isolated end-to-end validation

Executed the newly copied ARM64 `hrapi` and staged Xray 26.10.31 inside `sudo unshare -n` with loopback enabled and temporary config/state paths:

- API `/healthz`: 200.
- `PUT /v1/selections`: 200; `GET /v1/selections`: 200; saved profile returned expected opaque source ID and server identity.
- `POST /v1/subscriptions/compile`: 200; one profile/one VLESS node compiled and Xray `run -test` accepted the generated config.
- Explicit runtime start: 200, running=true; stop: 200, running=false, last exit `signal: terminated`.
- Refresh with missing manually picked node: 422 (`manual selection has no currently available nodes`); no replacement config was prepared.
- Cross-origin mutation: 403.
- Test namespace cleaned; no hrapi process remained. `x-ui.service`, `tailscaled.service`, and `tailscale-xray-proxy.service` remained active.

No production Xray, routes, firewall, DNS, interfaces, services or Keenetic settings were changed.
