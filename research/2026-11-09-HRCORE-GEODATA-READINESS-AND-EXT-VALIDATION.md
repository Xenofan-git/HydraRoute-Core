# HydraRoute Core GeoIP/GeoSite readiness and rule validation — 2026-11-09

## Summary

The NanoPi deployment already contains Xray 26.10.31 ARM64 and both geodata assets. A read-only Xray `run -test` against the actual deployed binary and files successfully validated built-in and external GeoSite references plus GeoIP references.

This work adds source-level geodata readiness reporting to the Core API and Rules UI. It does not deploy or modify NanoPi runtime.

## NanoPi evidence

Service unit:
- `XRAY_LOCATION_ASSET=/usr/local/libexec/hydraroute`
- `WorkingDirectory=/usr/local/libexec/hydraroute`

Installed assets:
- `geoip.dat`: 3,312,784 bytes; SHA-256 `ce90dc02a81528109c99d769e1f8fe3071ded6b02a574f31e62d8f4eee312563`
- `geosite.dat`: 7,554,929 bytes; SHA-256 `9ee94acb06e2f795831fb1589c0dc7af9242b0fb8d9196afd981b915b1097ed3`

Actual ARM64 Xray validation passed with:
- `geosite:youtube`
- `ext:geosite.dat:youtube`
- `geoip:ru`
- `geoip:private`

Command used Xray `run -test -config <temporary-file>` with `XRAY_LOCATION_ASSET=/usr/local/libexec/hydraroute`. The temporary config was in `/tmp` and removed afterwards. No listener was started, no service restarted, and no persistent config was written.

## Compiler security correction

Previous `ext:` validation only checked that the reference had three colon-separated parts and non-empty filename/tag. It did not reject absolute paths or path traversal.

Validation now requires:
- exactly `ext:<basename>.dat:<tag>`;
- filename matches a restricted basename regex and ends in `.dat`;
- tag matches the existing geodata tag allowlist;
- path separators, absolute paths, `..`, malformed names and invalid tags are rejected.

Regression tests cover valid built-in GeoIP/GeoSite values and valid external file references, plus traversal/absolute-path/invalid-extension cases.

## New Core API/UI work

Added `GET /v1/geodata/status`:
- local-request-only GET endpoint;
- reads `XRAY_LOCATION_ASSET` from the service environment;
- checks fixed `geoip.dat` and `geosite.dat` filenames only;
- rejects symlink directories/files, non-regular files, empty files, and assets larger than 128 MiB;
- verifies the opened file still matches the previously inspected inode;
- returns readiness, size and SHA-256 without exposing the asset directory path.

Rules UI now displays GeoIP and GeoSite asset readiness. The rule editor documents domain/IP, built-in GeoSite/GeoIP and safe external GeoSite syntax. Xray's own config test remains authoritative for whether a specific tag exists in the selected database.

API tests cover ready assets, hash/size metadata, path non-disclosure, symlink rejection and missing asset directory. Browser test fixture and a UI readiness test were added.

## Validation

- `GOMAXPROCS=1 go test -count=1 ./...` — passed.
- `GOMAXPROCS=1 go vet ./...` — passed.
- `git diff --check` — passed.
- Actual NanoPi ARM64 Xray 26.10.31 config test with both installed assets — passed.
- Browser/Playwright test was added but not run on VPS because Node.js/Playwright are unavailable there. Do not claim browser validation until CI executes it.

## Safety boundary

No NanoPi runtime deployment, service restart, config replacement, port binding, system DNS change, route/firewall change, Keenetic change, x-ui/Xray change or Tailscale change occurred.
