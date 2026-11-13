# HRweb DNS blocklist — parser and source lifecycle foundation

Date: 2026-11-10

Original HRweb 1.68.0-1 method inventory includes dnsBlockSave, dnsBlockToggle, dnsBlockCheckHandler, dnsBlockRemove, dnsBlockRestore, dnsBlockListSave, dnsBlockListToggle, dnsBlockListDelete, dnsBlockUpdate, dnsBlockCustomGet and dnsBlockCustomSave. Helpers include dnsBlockSource.add/parseLine/read, fetchDNSBlockSource, fetchDNSBlockExclusive, buildDNSBlockCustomLocked, DNSBlock.rebuild, openDNSBlockBin and writeDNSBlockBin.

The original DNSBlockList model contains ID, name, URL, enabled state, removed entries, count, allow count, dropped count, update timestamp and last error. Static disassembly confirms comment/metadata handling (#, !, [), Adblock-style @@ and || markers, and normalization of case, wildcard and trailing-dot forms.

Core branch integration/hrneo-dns-blocklist-parser-20261110 now contains:
- parse.go: hosts-file mappings, plain domains, common ||domain^ and @@||domain^ entries, normalized/deduplicated block and allow results, parse counters, 16 MiB input and 64 KiB line caps.
- fetch.go: public HTTP(S) fetch through the existing subscription transport, inheriting public-IP checks, DNS-rebinding protection, redirect validation, timeout and 8 MiB response cap.
- manager.go: persistent source CRUD, enable/disable, refresh, last-known-good retention on failed refresh, per-source removed-entry restore, status counters and merged enabled block/allow snapshots. State is atomically written as a private 0600 file; unsafe URL destinations and loose state permissions are rejected.

This is not yet connected to the HTTP API or DNS data plane. Allow-vs-block precedence is kept explicit as separate sets pending exact original behavior confirmation. Scheduled refresh and SmartDNS domain-set integration also remain open. No runtime deployment or live DNS changes.
