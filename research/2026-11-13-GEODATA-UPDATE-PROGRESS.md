# GeoIP / GeoSite update development — 2026-11-13

Development checkout: /srv/development/isolated-checkouts/hrcore-geo-audit
Base: PR #7 branch integration/hrneo-dns-blocklist-parser-20261110, c129ae1.

Implemented internal/geodata candidate validation (protobuf structural checks), HTTPS fetch with SHA-256 verification, bounded response, no redirects/proxies and public-IP-only dial, and isolated staged pair with fsync. All Go tests pass on Moscow VPS.

NOT YET IMPLEMENTED: full semantic Xray schema validation; generation-based atomic activation; crash recovery and rollback; management API/UI; production deployment. StagePair intentionally does not change XRAY_LOCATION_ASSET. Existing NanoPi, Keenetic, 3x-ui and CSQTT are untouched. This code must not be advertised as a completed updater.

Next: implement crash-safe generation pointer with migration compatibility for existing non-symlink asset directory, rollback and restart validation, then API/UI and integration tests. Review DNS resolver and HTTPS trust policy before allowing configurable URLs in API.

## Second development pass

Added GenerationStore: a private generation directory containing both validated assets, one atomic `current` symlink rename, explicit callback for candidate runtime validation before publish, and rollback to a validated earlier generation. The module rejects unsafe generation names and symlink roots. Tests cover success, failed validation preserving current, rollback, path traversal, and unsafe links. `go test ./...` passes on Moscow VPS.

IMPORTANT LIMITATIONS: `current` is NOT wired to Xray's actual XRAY_LOCATION_ASSET; no live activation, post-switch Xray health check or crash-state reconciliation yet. A successful symlink rename is not proof that a running Xray instance uses the new data. The `check` callback must be supplied by a trusted Xray validator before production integration. Filesystem sync errors after rename need explicit recovery handling. This remains development-only.
