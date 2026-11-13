# GeoIP / GeoSite update development — 2026-11-13

Development checkout: /srv/development/isolated-checkouts/hrcore-geo-audit
Base: PR #7 branch integration/hrneo-dns-blocklist-parser-20261110, c129ae1.

Implemented internal/geodata candidate validation (protobuf structural checks), HTTPS fetch with SHA-256 verification, bounded response, no redirects/proxies and public-IP-only dial, and isolated staged pair with fsync. All Go tests pass on Moscow VPS.

NOT YET IMPLEMENTED: full semantic Xray schema validation; generation-based atomic activation; crash recovery and rollback; management API/UI; production deployment. StagePair intentionally does not change XRAY_LOCATION_ASSET. Existing NanoPi, Keenetic, 3x-ui and CSQTT are untouched. This code must not be advertised as a completed updater.

Next: implement crash-safe generation pointer with migration compatibility for existing non-symlink asset directory, rollback and restart validation, then API/UI and integration tests. Review DNS resolver and HTTPS trust policy before allowing configurable URLs in API.

## Second development pass

Added GenerationStore: a private generation directory containing both validated assets, one atomic `current` symlink rename, explicit callback for candidate runtime validation before publish, and rollback to a validated earlier generation. The module rejects unsafe generation names and symlink roots. Tests cover success, failed validation preserving current, rollback, path traversal, and unsafe links. `go test ./...` passes on Moscow VPS.

IMPORTANT LIMITATIONS: `current` is NOT wired to Xray's actual XRAY_LOCATION_ASSET; no live activation, post-switch Xray health check or crash-state reconciliation yet. A successful symlink rename is not proof that a running Xray instance uses the new data. The `check` callback must be supplied by a trusted Xray validator before production integration. Filesystem sync errors after rename need explicit recovery handling. This remains development-only.

## Third development pass — Xray preflight

Added `XrayCheck` with bounded execution of the configured Xray binary's `run -test -config` command, using a candidate-only XRAY_LOCATION_ASSET environment. Added tests for rejecting Xray preflight failures without changing the current generation, invalid GeoSite, environment isolation and dangling generation links. Full Go test suite passes on Moscow VPS.

Deployment compatibility finding: current systemd unit sets `XRAY_LOCATION_ASSET=/usr/local/libexec/hydraroute`, which is also `WorkingDirectory` and contains the Xray executable. It is protected by `ProtectSystem=strict`; only `/var/lib/hydraroute` is writable. Therefore no updater can safely overwrite the existing asset directory in-place. Integration requires explicit migration of the asset location into a private generation store and coordinated Xray restart/health check with rollback. Never rewrite the live asset files or unit without a tested migration and user-authorized deployment.

Remaining security work: schema-level semantic checks for GeoIP/GeoSite, safe DNS/public IP handling and TLS trust review, generation crash recovery and post-switch rollback, cross-process locking, API authorization, UI and integration tests. No production change or GitHub push yet.
