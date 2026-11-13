# GeoData: safe opt-in migration gate (2026-11-13)

The packaged service continues to use `XRAY_LOCATION_ASSET=/usr/local/libexec/hydraroute`. No deployment or systemd changes are authorized by this work.

## Before enabling a managed generation

1. Keep a verified backup of the complete legacy Xray asset directory, configuration, and current service unit. Never delete or overwrite the original GeoIP/GeoSite files.
2. Run `geodata.AuditLegacyAssets(source)` against the **absolute** legacy asset directory. It is read-only. If `Extra` is nonempty, **stop**. Custom `ext:` assets, other databases, symlinks, and hidden entries require a separate explicit preservation strategy; they are not copied by the two-file generation store.
3. Confirm the Xray configuration resolves its `geoip:` and `geosite:` references and any custom `ext:` references. Do not assume that a successful two-file preflight proves arbitrary custom assets will work.
4. Create the managed store under the application state directory with restricted ownership and permissions. Bootstrap it from the legacy directory using `GenerationStore.Bootstrap` with a real `XrayCheck`. Bootstrap now rejects extra legacy files rather than silently hiding them.
5. Validate the candidate configuration with real Xray and `XRAY_LOCATION_ASSET=<stateDir>/geodata/current` in isolation. Check `InspectStore` and `CheckStartupReady` before any service changes.
6. Only after an explicit operator-approved maintenance window, prepare a service override and an immediate rollback procedure to the original environment value. Do not change systemd or production routing as part of automated migration tests.
7. After restart, verify the process, Xray Control API, routing rules, and dependent services; on any failure revert the service override and restore the previous working configuration.

## Automated coverage

- `TestAuditLegacyAssetsPreservesAndReportsAdditionalFiles`
- `TestAuditLegacyAssetsRejectsUnsafeSource`
- `TestBootstrapRefusesToDropCustomAssets`
- `TestLiveXrayGeoDataActivationAndRollback` (opt-in real AMD64 Xray; separate process and temporary files)

Remaining: end-to-end service override test in an isolated environment, then a reviewed operator runbook. No live rollout has occurred.

## Isolated service-environment transition test

`TestLiveXrayServiceAssetPathMigrationAndRevert` exercises the actual Xray
binary with a temporary legacy directory, a bootstrapped managed store, and
three successive Xray subprocess launches: legacy, managed `current`, and
legacy again. It verifies that both original databases remain byte-identical,
that the managed generation remains ready, and that the final legacy process
is running. This is **not** a real systemd unit override test; systemd and
production services remain unchanged. The explicit systemd override and
service supervision path still require a separate isolated deployment test.

## Transient systemd service smoke test

`tests/integration/geodata_systemd_smoke.sh` uses `systemd-run` to create a uniquely
named temporary service on the development VPS. It verifies Xray 26.10.31 under
systemd, with `NoNewPrivileges`, `ProtectSystem=strict`, `ProtectHome`,
`PrivateTmp`, and `PrivateDevices`, using no inbound listeners. It tests
legacy -> managed -> legacy environment transitions, then a deliberately
corrupted managed database (service fails) and a successful legacy restart.
Original GeoIP/GeoSite files are compared byte-for-byte. The test cleans up
its temporary unit and `/run` directory. It does **not** install, reload, or
modify `hydraroute-core.service`, nor test the full hrapi application service.

The first attempt placed the config under `/tmp`; `PrivateTmp=yes` correctly
made that path invisible to the service. The test now uses a private `/run`
folder so the isolation property stays enabled. Production rollout is still
blocked pending a reviewed operator runbook and a full hrapi service test.

## Full hrapi + Xray isolated autostart smoke test

`tests/integration/geodata_hrapi_smoke.sh` builds the current `cmd/hrapi`,
starts it with `-restore-config=true -autostart` and real Xray 26.10.31,
then polls `/v1/runtime` for running/configured status. It performs three
separate launches: legacy asset directory, managed `geodata/current`, and
legacy directory again. The managed run also checks `/v1/geodata/status`
reports `update.ready=true`; original databases are byte-compared after the
revert. All paths, processes and API ports are isolated and removed at exit.
This is an application integration test, not an installed systemd service or
production rollout. The managed baseline in this smoke test is seeded from
trusted local assets; separate Bootstrap tests cover validated initialization.

The test host mounts `/run` with `noexec`, so the compiled test hrapi binary
is placed in a private `/var/tmp` directory. The restored config must be
`0600` because the supervisor intentionally rejects permissive files.

## GitHub Actions integration

The main `.github/workflows/ci.yml` test job now reuses the existing
checksum-verified Xray 26.10.31 archive to run the opt-in real-Xray GeoData
activation/rollback and asset migration tests, followed by the full `hrapi`
autostart smoke test. All tests use private temporary paths and loopback
listeners. The transient systemd test is intentionally **not** run on shared
GitHub-hosted runners because it requires privileged access to systemd; it
remains a separately executed development-VPS smoke test.

This CI change has been validated locally on the Moscow development VPS. It
must not be reported as green GitHub Actions until the branch is pushed and
GitHub completes the workflow.

## Managed-mode startup fail-closed audit

`CheckAssetStartupReady` distinguishes legacy asset paths from the exact
managed `<stateDir>/geodata/current` path. A missing store remains allowed
only for legacy installations; managed startup now rejects a missing store,
missing current generation, missing GeoIP/GeoSite, symlinks, and invalid
protobuf databases. Both `cmd/hrapi` pre-autostart and the API's secondary
startup guard call it; `RecoverStartup` uses it even when no journal exists.
The full hrapi smoke test deliberately corrupts managed GeoIP, confirms
managed autostart fails, then restarts with the untouched legacy assets.

Remaining review item: the guard reads generation assets before launching
Xray, but a separately privileged local actor with write access to the
private state directory could still modify files between check and launch.
The production state directory must remain writable only by the dedicated
service account. No live migration or production service changes occurred.
