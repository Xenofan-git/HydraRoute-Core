# GeoIP / GeoSite update development — 2026-11-13

Development checkout: /srv/development/isolated-checkouts/hrcore-geo-audit
Base: PR #7 branch integration/hrneo-dns-blocklist-parser-20261110, c129ae1.

Implemented internal/geodata candidate validation (protobuf structural checks), HTTPS fetch with SHA-256 verification, bounded response, no redirects/proxies and public-IP-only dial, and isolated staged pair with fsync. All Go tests pass on Moscow VPS.

NOT YET IMPLEMENTED: full semantic Xray schema validation; generation-based atomic activation; crash recovery and rollback; management API/UI; production deployment. StagePair intentionally does not change XRAY_LOCATION_ASSET. Existing NanoPi, Keenetic, 3x-ui and CSQTT are untouched. This code must not be advertised as a completed updater.

Next: implement crash-safe generation pointer with migration compatibility for existing non-symlink asset directory, rollback and restart validation, then API/UI and integration tests. Review DNS resolver and HTTPS trust policy before allowing configurable URLs in API.
