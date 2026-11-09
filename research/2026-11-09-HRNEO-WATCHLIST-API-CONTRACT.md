# HRNeo Watchlist API contract — recovered from source

Date: 2026-11-09
Original source: `Ground-Zerro/HydraRoute`, commit `7fb556ec493adda131f3e8f97106226751b59528`, HRNeo 3.24.2-1.

## Evidence

- `Neo/docs/WATCHLIST_API.md`
- `Neo/docs/WATCHLIST_LIMITS.md`
- `Neo/source/src/watchlist_api.c`
- `Neo/source/src/watchlist.c`
- `Neo/source/include/watchlist.h`
- `Neo/source/include/watchlist_api.h`
- `Neo/source/tests/check_wlapi.c`
- `Neo/docs/CORE.md`, section 20.

This closes the earlier API contract recovery gap. The API is a local Unix socket protocol, not an HTTP/JSON endpoint.

## Transport contract

- Unix domain socket: `/var/run/hrneo.sock`, fixed path (not configurable).
- `AF_UNIX / SOCK_STREAM`.
- File permissions `0600`; owned by hrneo (root).
- Up to 4 concurrent clients; fifth receives `ERR busy\n` and is closed.
- Request line is ASCII terminated by LF; CRLF accepted.
- Input buffer 512 bytes; overlong line closes connection.
- Commands may be pipelined; responses preserve request order.
- Half-close via `shutdown(SHUT_WR)` is supported; complete buffered requests are answered; incomplete final line is ignored.
- Unknown command returns `ERR command\n` and connection remains open.
- Socket appears only after watchlist and rank data are fully loaded. It is removed at graceful shutdown; restart closes all clients.

## MATCH contract

Request: `MATCH <name>\n`

Responses:
- `OK <target> <rank> <key>\n` — match found.
- `NO\n` — no match.
- `ERR name\n` — empty name or length >253.
- `ERR command\n` — unknown command.

Semantics:
1. Remove one trailing dot.
2. Reject empty name or length >253 bytes.
3. Lowercase in place.
4. Check exact name and each parent suffix: `a.b.c` → `a.b.c`, `b.c`, `c`.
5. Among all matching keys, choose the target with the **lowest numeric rank**. Specificity/longest suffix does not win automatically.
6. Return the key that produced the winning target.
7. CNAME is not part of MATCH. The DNS data-plane path separately walks CNAME chains.

Ranks:
- Unique integer 0..N-1.
- PolicyOrder targets first, then remaining targets alphabetically.
- Targets may be policies or DirectRoute interface names.
- Targets containing only CIDRs can appear in T records but not K/MATCH.

## DUMP contract

Request: `DUMP\n`

Response:
- `T <rank> <target>\n` — targets in ascending rank.
- `K <key> <rank>\n` — each watchlist key and owning target rank; hash bucket order is unspecified.
- `END <n>\n` — successful completion; n counts emitted K lines.

A missing END or K-count mismatch means truncated output. Dump is streamed from the immutable in-memory hash table, without a full copy. Server output buffer is 8192 bytes; one bounded fill is performed per writable event so a slow DUMP reader does not stall MATCH requests from other clients. The same connection's later requests wait for DUMP completion.

K entries include explicit domain.conf entries and GeoSite Domain/Full entries, deduplicated by first insertion. GeoSite Plain/Regex patterns are not emitted as concrete keys.

## Lifecycle and security

- API is read-only and queries the same immutable in-memory table used by HRNeo routing.
- Separate event-loop thread; it does not wait for RCI/netfilter commits.
- No hot watchlist reload; restart closes clients and may change keys/ranks.
- Socket permissions are 0600; this is the access-control boundary.
- No credentials are expected in MATCH/DUMP responses.

## Core/Debian adaptation decisions

1. Preserve the line protocol and response records where practical.
2. Use a Core-owned path such as `/run/hydraroute-core/watchlist.sock`, never `/var/run/hrneo.sock` (avoid collision with existing HRNeo).
3. Socket should be created under the service-owned runtime directory with 0600 and only after the active routing model is loaded.
4. DUMP and MATCH must read the same immutable snapshot that produced the Xray config. Publish new snapshot only after successful config validation/apply; old connections close or finish against old snapshot.
5. Preserve lowest-rank-wins semantics, including exact + parent suffix candidates. Do not replace it with longest-suffix match.
6. For domain.conf-style Core rules, return targets as stable local action identifiers: profile ID, `DIRECT`, or `BLOCK`. This is a documented Debian mapping, not the original Keenetic target namespace.
7. Expand GeoSite Domain/Full entries from the configured geodata assets if full parity is required. Do not claim full DUMP parity while GeoSite is only passed through to Xray and not materialized into the snapshot.
8. GeoIP/IP/CIDR entries do not belong in this domain Watchlist MATCH/DUMP API; keep IP matching separate.
9. Keep protocol limits bounded and test 4-client limit, overlong lines, pipelining, half-close, slow DUMP concurrency, truncation detection, trailing dot, case-folding, suffix rank precedence and restart lifecycle.

## Status

Contract recovery: COMPLETE.
Core implementation: implemented in source; full CI validation pending. See research/2026-11-09-HRNEO-WATCHLIST-IMPLEMENTATION.md.
No NanoPi runtime or network changes were made during this research.
