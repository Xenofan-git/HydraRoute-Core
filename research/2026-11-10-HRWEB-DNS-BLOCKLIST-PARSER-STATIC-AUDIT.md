# HRweb DNS blocklist — parser audit and Core foundation

Date: 2026-11-10
Reference: original HRweb 1.68.0-1 ARM64 binary and its Go function table.

## Original method inventory

The original HRweb binary contains these DNS blocklist methods and helpers:

- dnsBlockSave, dnsBlockToggle, dnsBlockCheckHandler, dnsBlockRemove, dnsBlockRestore
- dnsBlockListSave, dnsBlockListToggle, dnsBlockListDelete, dnsBlockUpdate
- dnsBlockCustomGet, dnsBlockCustomSave
- dnsBlockSource.add, dnsBlockSource.parseLine, dnsBlockSource.read
- fetchDNSBlockSource, fetchDNSBlockExclusive, buildDNSBlockCustomLocked
- DNSBlock.rebuild, openDNSBlockBin, writeDNSBlockBin

The DNSBlockList persisted model includes ID, name, URL, enabled state, removed entries, count, allow count, dropped count, update timestamp and last error. Therefore the original feature is more than a list of URL strings: it has source lifecycle, parsed/compiled data, allow/removed bookkeeping, status and update operations.

## Parser observations from static disassembly

The original dnsBlockSource.parseLine and .add functions show handling for:
- blank/comment/metadata lines beginning with #, ! or [;
- Adblock-style exception marker @@ and domain anchor ||;
- domain normalization, lowercasing and wildcard/trailing-dot cleanup in the add path;
- host/domain-oriented parsing rather than accepting arbitrary URL paths as domain names.

The original URL save path checks HTTP/HTTPS URL schemes. The Core implementation will retain stricter outbound-safety checks when fetching remote lists (public destinations, bounded redirects/body size and timeouts); it will not expose arbitrary raw SmartDNS config or permit local/private-network fetch targets.

## Core parser foundation in this branch

Added internal/dnsservice/blocklist/parse.go and tests. The parser:
- accepts hosts-file mappings, plain domains and common ||domain^ / @@||domain^ rules;
- normalizes to lowercase ASCII hostnames, strips a leading *. or ., and deduplicates;
- returns separate blocked and allowed domain sets plus line/parsed/ignored/dropped counters;
- ignores non-domain URL/path filters instead of widening them into a host match;
- caps input at 16 MiB and individual lines at 64 KiB.

This is deliberately a parser foundation only. It does not yet implement URL download, source CRUD/toggle/delete/update, removed-entry restore, persistence, scheduled refresh, status API, or wiring into the SmartDNS compiler/Go resolver. No 1:1 parity claim is made.

## Next implementation gates

1. Add a bounded HTTPS fetcher with public-address and redirect validation, timeout, response-size limit and content-type-independent plain-text parsing.
2. Persist each source's last-known-good parsed result and update metadata atomically; failed refresh must not erase working data.
3. Implement source add/update/toggle/delete/refresh and removed-entry restore with stable IDs and explicit per-source status.
4. Resolve allow-vs-block precedence against the original HRweb rebuild path before wiring parsed data into SmartDNS block/allow DomainSets.
5. Add API tests and isolated end-to-end DNS query tests; then update the 114-method compatibility matrix conservatively.

No runtime deployment or live DNS change was made.
