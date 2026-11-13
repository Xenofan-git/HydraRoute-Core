# HydraRoute Core — encrypted profiles and explicit refresh workflow

Date: 2026-11-08

## Delivered

- Added `internal/profiles`, an encrypted profile/source store using AES-256-GCM.
- Subscription URLs (including query tokens), inline subscription bodies and raw share links are encrypted at rest. The profile encryption key is generated locally and stored as a 32-byte mode-0600 file inside a mode-0700 directory.
- Profile records are atomically replaced, private, authenticated, and bound to their profile ID as AES-GCM additional authenticated data. Invalid IDs, ambiguous inputs, symlink/permissive directories, tampered ciphertext and invalid file modes are rejected.
- `GET /v1/profiles` returns metadata only; it does not return subscription URLs, tokens or raw links.
- `POST /v1/profiles` creates or updates a profile. Exactly one input source is accepted: links, inline subscription body, or subscription URL.
- `DELETE /v1/profiles` accepts a JSON profile ID, removes encrypted profile and selection state, and refuses deletion while the managed Xray runtime is running.
- `POST /v1/profiles/refresh` reads all encrypted profiles, fetches/parses each source, reconciles each profile's saved selection, compiles one aggregate config and validates/prepares it with Xray. It does not start Xray.
- No scheduled refresh/retry loop is enabled. Refresh remains an explicit API action until concurrency, backoff, provider rate limits and restart semantics are designed and tested.

## Verification

- `go test -count=1 ./...` passed.
- `go vet ./...` passed.
- 88 test functions across the module.
- Tests cover AES-GCM round-trip/tamper rejection, plaintext-secret absence, private file modes, metadata redaction, CRUD, refresh, selection cleanup and delete-while-running protection.

## Safety boundary

No production service, existing Xray/3x-ui process, route, firewall, DNS, interface, Keenetic setting, HydraRoute Neo/Entware configuration or Tailscale configuration was modified. The new profile store and refresh endpoint are not yet deployed to NanoPi; ARM64 build and isolated Xray/API verification follow.

## NanoPi ARM64 isolated end-to-end validation

After a timestamped source/artifact backup, copied the ARM64 build to NanoPi and ran it with staged Xray 26.10.31 inside `sudo unshare -n`:

- `POST /v1/profiles` stored a URL-backed profile; `GET /v1/profiles` returned metadata only.
- Inspected the encrypted profile file: neither the URL host nor test query value appeared in plaintext. Encryption key mode was 0600 and profile directory mode was 0700.
- Deleted the URL-backed test profile without making a network request.
- Stored a local VLESS-link profile and `POST /v1/profiles/refresh` returned 200; Xray 26.10.31 accepted the aggregate config.
- Explicit runtime start returned 200/running; deleting the profile while running returned 409; stop returned 200/stopped; delete after stop returned 200.
- Temporary state/config and isolated namespace were cleaned.

Production `x-ui.service`, `tailscaled.service`, and `tailscale-xray-proxy.service` remained active. No production Xray process, route, firewall, DNS, interface or Keenetic setting was modified.

## Final build hardening

Serialized mutating API operations (config preparation, subscription compilation, selection writes, profile CRUD/refresh and runtime start/stop) through one server operation mutex. This closes races such as a profile deletion checking runtime state while a concurrent start is in progress. Graceful API shutdown also serializes its runtime stop against active mutations.

Final hrapi SHA-256:
- AMD64: `fbc1c880e4ce6d2eca6b6b0fae00b90d7e8ed3a805f8e295f816b29ad41b938d`
- ARM64: `f948608e0bfdb09958ed31d6aa538a31e32731100fe45c702cfd066986f52b7f`

Full test suite and `go vet` passed after the concurrency change; 88 test functions.

## Final serialized ARM64 build retest

After rebuilding with the API operation mutex, recopied the final ARM64 binary to NanoPi and repeated the isolated namespace test. Profile URL save/list/delete, ciphertext/metadata redaction, local profile save, refresh-all, Xray 26.10.31 validation, runtime start, delete-while-running rejection (409), runtime stop and post-stop delete all passed. Final ARM64 hrapi hash verified on NanoPi: `f948608e0bfdb09958ed31d6aa538a31e32731100fe45c702cfd066986f52b7f`.


## Follow-on: scheduler and restart recovery

- Added `Supervisor.Restore(ctx)`: validates the existing config file is a private regular file, validates JSON and runs the selected Xray `run -test`; marks it configured without starting Xray. Missing config is a clean first-run state.
- Added CLI flags `-restore-config` (default true), `-autostart` (default false; also starts Xray after successful refreshes when configured), `-refresh-interval` (default 0/disabled), `-retry-min` (default 30s), and `-retry-max` (default 15m). The CLI requires a refresh interval of at least 1m and retry minimum of at least 5s.
- Added background refresh scheduler. It runs after the configured interval, reports status at `GET /v1/refresh/status`, and retries failures with capped exponential backoff. Scheduler stops and joins cleanly during API shutdown.
- Subscription compilation now collects safe per-profile failure summaries. If any enabled source fails, the aggregate refresh aborts without replacing the last-good config. Raw URLs, tokens and share links are not included in scheduler errors.
- Manual and scheduled refresh preserve runtime state: if Xray is running, sources are fully parsed first, then Xray is stopped, the candidate config is validated/atomically prepared, and Xray is restarted. If validation fails, the previous config remains and the previous runtime is restarted.
- Added tests for restore validation, runtime-preserving refresh, last-good config retention, scheduler startup/shutdown, status, per-profile error reporting and retry backoff.
- 97 test functions; full test suite and `go vet` passed. Static Linux AMD64/ARM64 builds pass.

### Operational safety

The scheduler remains disabled unless `-refresh-interval` is explicitly set. `-autostart` is separately opt-in. A hardened systemd deployment template was added and syntax-verified in a staging root; no systemd unit was installed or enabled, and no production process/configuration was changed.

## NanoPi ARM64 scheduler and restart recovery validation

Final ARM64 hrapi was copied after a timestamped backup and verified on NanoPi:
- SHA-256: `241132f9ebce0e9feb0e9935dbefc3cfcef6550a78d978fd3e2a2391b13ab3a9`.

Isolated `unshare -n` tests with Xray 26.10.31 passed:
- First API start with no config reports configured=false; scheduler enabled status reports the requested 60-second interval and next attempt.
- Profile save followed by manual refresh prepares the config.
- Refresh while Xray is running stops/validates/restarts it; runtime remains running afterwards.
- API graceful shutdown stops the managed Xray child.
- Restart with default restore validates the existing config and reports configured=true/running=false; no implicit autostart.
- Restart with explicit `-autostart` restores and starts Xray; API stop succeeds.
- A separate 60-second scheduled cycle ran on NanoPi: status advanced from no `last_success` to a real `last_attempt`/`last_success`, Xray config became configured, and runtime remained stopped (scheduler does not auto-start Xray).

All test namespaces, temporary config/state directories and API processes were cleaned. No production listener or service was changed.


## Deployment packaging template

- Added `deploy/systemd/hydraroute-core.service` and `deploy/sysusers/hydraroute-core.conf` to the source tree. The unit uses a dedicated unprivileged user, private state directory, loopback-only API, systemd filesystem/kernel hardening and six-hour refresh. It explicitly opts into Xray autostart after config restore and successful refresh; on first install without a config it stays up and can accept profiles, then the first successful refresh starts Xray.
- Added `deploy/README.md` with review/install/rollback guidance. No installer was executed; no systemd unit was copied into `/etc/systemd/system`, enabled or started.
- Built a private ARM64 deployment bundle with hrapi/hrcore/hrparse, Xray 26.10.31 and its asset/license files. `systemd-analyze verify` passed against a staging root; `systemd-sysusers --dry-run` parsed the user declaration.
- HydraRoute Core itself still has no repository license declaration; bundle documentation explicitly restricts redistribution until licensing/provenance is resolved.
- Final suite: 97 test functions, all tests and `go vet` pass.

## ARM64 deployment bundle (template only)

- Added source deployment templates: `deploy/systemd/hydraroute-core.service`, `deploy/sysusers/hydraroute-core.conf`, and `deploy/README.md`.
- Built `hydraroute-core-arm64-2026-11-08.tar.gz` containing static hrapi/hrcore/hrparse, Xray 26.10.31 ARM64, matching geoip/geosite assets, Xray license, systemd unit, sysusers declaration and install/rollback instructions.
- Unit defaults: dedicated unprivileged `hydraroute` user, loopback-only API, private state directory, systemd filesystem/kernel hardening, six-hour refresh, capped retries, and explicit `-autostart`. On a fresh install with no config, API remains available; first successful manual/scheduled refresh prepares and starts Xray. Remove `-autostart` if that behavior is not wanted.
- `systemd-analyze verify` passed against a staging root; `systemd-sysusers --dry-run` parsed the user declaration; all package file checksums passed.
- `systemd-analyze security --offline=yes` reported overall exposure level `3.2 OK` for the unit.
- Bundle SHA-256: `3953920034f8e5a2e087fefcb37e52484c78201bcfd927e10f7ff7bc873bc48c`.
- Final hrapi hashes: AMD64 `cb6ca09e369fca9bda18db949a6d6c2bfd2aa686ae0674a2d5fd2e280c3ace7e`; ARM64 `3aa5c3e800a5f5910e1724e9b4c99a59eff0381a26d315175e87196544e53ede`.
- Final validation: 97 test functions; full tests and `go vet` pass. No unit installed/enabled, no production service/process/network configuration modified.

## Final ARM64 binary retest with autostart enabled

The final ARM64 hrapi (SHA-256 `3aa5c3e800a5f5910e1724e9b4c99a59eff0381a26d315175e87196544e53ede`) passed a final isolated NanoPi test using actual Xray 26.10.31:

- Started with `-autostart` and no existing config: API remained healthy, configured=false/running=false (no restart loop).
- Saved a profile and invoked first refresh: config validated and Xray started automatically.
- Waited for the real 60-second scheduler tick: last_attempt/last_success advanced, Xray remained running after scheduled refresh.
- Sent SIGTERM to API: managed Xray stopped cleanly.
- Restarted API with the same state/config and `-autostart`: existing config restored and Xray started.
- Explicit API stop and shutdown passed; temporary namespace/state cleaned.

Final deployment bundle was rebuilt to include the final 97-test manifest and has SHA-256 `35798cdcef7d21819450fd9ffc168320fcbd75cc474e562562d4f8a6929aa249`.


## GitHub Actions preparation

- Added `.github/workflows/ci.yml`: gofmt check, full `go test`, `go vet`, then static Linux AMD64/ARM64 builds for hrapi/hrcore/hrparse and 14-day artifact upload.
- Added `.gitignore` and `CONTRIBUTING.md` with required checks and project safety invariants. YAML parses locally; local test/vet/build checks pass.
- Checked the connected GitHub account's accessible repositories; no dedicated HydraRoute Core repository exists. Existing visible repos are unrelated CSQTT projects, so none was repurposed. The workflow has not run in GitHub Actions; publishing requires a new dedicated repository or an explicit destination.


## Read API Host/Origin hardening follow-up

- Security review found that read-only endpoints did not consistently validate loopback Host/client, and Origin checks matched hostname but not port. This could expose health/runtime/refresh metadata to DNS-rebinding or a malicious page hosted on another localhost port.
- Updated `/healthz`, `/v1/runtime` GET, `/v1/refresh/status`, profiles and selections read paths to require loopback remote address and loopback Host.
- If Origin is supplied, its complete authority must exactly match request Host, including port. Origin scheme must be HTTP(S), and userinfo/path/query/fragment are rejected. Write endpoints use the same exact-origin authority rule.
- Added regression tests for attacker Host, cross-site fetch metadata, remote clients and `127.0.0.1:3000` Origin against API on `127.0.0.1:17890`.
- 100 Go test functions; API tests repeated 5x; full test suite and `go vet` pass.
- Final ARM64 hrapi tested on NanoPi inside isolated network namespace with actual HTTP requests: Host rebinding and cross-port Origin return 403; correct loopback health/status requests return 200.
