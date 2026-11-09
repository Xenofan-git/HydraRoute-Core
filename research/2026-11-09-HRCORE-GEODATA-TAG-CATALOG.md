# NanoPi Xray GeoIP/GeoSite tag compatibility — 2026-11-09

## Environment

- Xray 26.10.31, ARM64, commit `a4a3dff`.
- Asset directory: `/usr/local/libexec/hydraroute`.
- `XRAY_LOCATION_ASSET` and systemd WorkingDirectory both point to that directory.
- Existing geodata SHA-256:
  - geoip.dat: `ce90dc02a81528109c99d769e1f8fe3071ded6b02a574f31e62d8f4eee312563`
  - geosite.dat: `9ee94acb06e2f795831fb1589c0dc7af9242b0fb8d9196afd981b915b1097ed3`

## Tags verified by actual ARM64 Xray config test

The following values were tested together in a temporary Xray configuration using the installed NanoPi binary and actual asset directory. `xray run -test -config` returned `Configuration OK`.

### GeoSite
- `geosite:youtube`
- `geosite:google`
- `geosite:category-ads-all`
- `geosite:category-ru`
- `geosite:geolocation-!cn`
- `geosite:netflix`
- `geosite:telegram`
- `geosite:discord`

### GeoIP
- `geoip:ru`
- `geoip:cn`
- `geoip:private`
- `geoip:!cn`

### External GeoSite
- `ext:geosite.dat:youtube`

## Negative test / syntax distinction

- `geosite:!cn` failed with Xray error `failed to check code !CN from geosite.dat`. It is not a valid negation form in this database.
- `geosite:geolocation-!cn` passed.
- `geoip:!cn` passed.

Therefore the compiler's tag-character allowlist must include `!`, but the final authority for a tag's semantic validity is the pinned Xray binary's config test. Do not interpret arbitrary `!` placement as a valid negative geosite expression.

## Compiler and UI changes

- Added `!` to the permitted character set for geodata tags so verified forms such as `geosite:geolocation-!cn` and `geoip:!cn` are not rejected before Xray validation.
- The compiler still restricts `ext:` file names to safe `.dat` basenames; external file paths cannot contain directory separators or traversal components.
- Rules UI syntax hint includes GeoSite/GeoIP and external-file examples.
- The Rules page shows the availability and SHA-256/size-derived status of the two fixed geodata assets via `GET /v1/geodata/status`.

## Safety

All tests used `run -test` with temporary config files. No Xray service was started/restarted; no listener was opened; no persistent configuration or host network state was changed.