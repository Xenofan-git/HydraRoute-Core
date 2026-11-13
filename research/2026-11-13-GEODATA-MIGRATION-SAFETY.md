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
