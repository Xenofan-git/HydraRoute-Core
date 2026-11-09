# HydraRoute Core Watchlist MATCH/DUMP implementation — 2026-11-09

## Contract basis
Original HRNeo 3.24.2-1 source at Ground-Zerro/HydraRoute commit 7fb556ec493adda131f3e8f97106226751b59528. See WATCHLIST_API.md, WATCHLIST_LIMITS.md, watchlist_api.c, watchlist.c and check_wlapi.c.

## Core implementation
- Go package: internal/watchlist.
- Core-owned Unix socket: /var/lib/hydraroute/state/watchlist.sock (private state directory, mode 0600). It deliberately does not reuse /var/run/hrneo.sock.
- Four concurrent clients maximum; fifth receives ERR busy.
- 512-byte line reader, 253-byte MATCH name limit, LF/CRLF, pipelining, half-close, incomplete final line ignored, unknown command ERR command while connection remains open.
- DUMP uses an 8192-byte buffered writer, emits T/K/END, sorted K output for deterministic Debian behavior, and is isolated per client so a slow DUMP does not block another client's MATCH.
- MATCH removes one trailing dot, lowercases, checks exact and each parent suffix, chooses lowest numeric rank (not longest suffix), and returns the matched key.
- Action target mapping: profile ID, DIRECT, BLOCK. Enabled rules define ranks in rule order; IP-only rules contribute T targets but never K keys.
- Snapshot is immutable during each request/DUMP and is updated only after successful Xray config prepare/apply. Socket closes when routing is disabled or Core API shuts down.
- GET /v1/watchlist/status is loopback-only and reports ready/path/target count/key count/partial/excluded pattern count/error.

## GeoSite expansion
- Parses the Xray geosite.dat protobuf directly using protowire.
- Reads fixed/validated .dat basenames only; rejects symlinks, non-regular files, empty or >128 MiB assets; opens with O_NOFOLLOW and verifies inode identity.
- Expands only GeoSite Domain and Full records, as original Watchlist does. Plain/Regex records are excluded from K entries.
- Keyword/regexp Xray rule patterns are not enumerable as concrete suffix keys and are excluded from DUMP.
- Partial coverage is explicitly exposed through the status API and Rules UI; this must not be presented as a complete data-plane evaluator.
- Duplicate domain keys retain the first inserted owner; target ranks use the first enabled rule occurrence. K output is deterministic lexical order (original HRNeo hash bucket order was unspecified).

## Actual NanoPi data validation
A temporary ARM64 helper built from the new Go package was copied to /tmp on NanoPi, executed against /usr/local/libexec/hydraroute/geosite.dat, then removed:
- geosite:youtube — 178 concrete Domain/Full keys, partial=false.
- geosite:category-ads-all — 904 keys, partial=true, 1 Plain/Regex pattern excluded.
- geosite:geolocation-!cn — 25,006 keys, partial=true, 158 Plain/Regex patterns excluded.
- ext:geosite.dat:youtube — 178 keys, partial=false.
This was a parser probe only; no Core/Xray runtime or systemd service was changed.

## Tests
- Snapshot tests: lowest-rank precedence, trailing dot/case normalization, parent suffixes, duplicate first owner, GeoSite Domain/Full filtering, attribute filtering, external files, traversal/symlink rejection, bounded names and DUMP terminator/count.
- Socket tests: protocol pipelining, CRLF, half-close, socket mode, four-client cap, fifth-client busy, overlong input close, slow DUMP does not block MATCH, socket cleanup.
- API test: routing apply creates Core-owned socket, MATCH/DUMP responses and status counts, disabling routing removes socket.
- VPS: full Go tests, go vet and diff check passed; watchlist race test passed. GitHub Actions #191/#192 will validate JS/Playwright and static ARM64/AMD64 builds.

## Safety
No NanoPi Core runtime deployment/restart. No changes to Keenetic, original HRNeo/Entware, 3x-ui/Xray, Tailscale, DNS, routes, firewall, iptables/nftables or conntrack.
