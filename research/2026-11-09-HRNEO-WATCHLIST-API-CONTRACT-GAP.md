# HRNeo Watchlist MATCH/DUMP API — contract recovery gap

Date: 2026-11-09
Status: NOT IMPLEMENTED; original contract not yet proven.

## What is known
The Debian parity roadmap includes a local MATCH/DUMP equivalent for the original HRNeo Watchlist. The recovered HRweb 1.68.0-1 transport/DTO inventory from 2026-11-05 documents the JSON POST envelope `{m, p}`, streaming GET `/api/stream`, and methods including `system.ping`, `proxy.*`, `proxy.pingServers`, and `probe.run`. It does not document a Watchlist MATCH or DUMP method, nor any corresponding request/response DTO.

The current Core has ordered Xray domain/IP rules and a local rule compiler. That does not by itself establish the semantics of an original Watchlist API.

## Required evidence before coding
1. Original API method registration and exact method names.
2. Frontend call sites and request parameter shapes.
3. Handler decoder target and Go DTO field metadata.
4. Response DTO and error/status mapping.
5. MATCH semantics: domain normalization, wildcard/CIDR/Geo matching, order, disabled rules, default action, and tie-breaking.
6. DUMP semantics: source of truth, output format, limits, ordering, redaction and streaming behavior.
7. Authorization/session/CSRF boundary.

## Implementation guardrails
- Do not invent endpoint names or payloads and call them compatible.
- Any Core-local API must be explicitly versioned and loopback-only until original semantics are proven.
- Never expose credentials, subscription URLs, SOCKS passwords or raw proxy links in dumps/events.
- MATCH should be a pure read-only operation over the same compiled rule model used by the data plane, not a separate divergent evaluator.
- DUMP must be bounded, deterministic and redact secrets.
- Add tests for order, disabled rules, GeoIP/GeoSite tags, CIDR, default action, malformed inputs, size limits and redaction.

## Current conclusion
Watchlist MATCH/DUMP remains a parity research task. Continue with source/binary evidence recovery before API implementation. No runtime or network changes were made.
