# HRweb DNS blocklist remove/restore semantics

Date: 2026-11-11
Reference: original HRweb 1.68.0-1 embedded dashboard JavaScript and handler registry.

## Evidence from original UI

The original DNS blocklist checker builds rows with distinct collections:
- list row: `entries` (blocked matches), `removed` (user-removed blocked entries), and `allow` (source allow exceptions);
- custom row: separate `block` and `allow` collections.

The original `renderCheckRow` renders separate visual states for blocked entries, removed entries and allow exceptions. It offers “Убрать из списка” only when `row.entries.length > 0`; it offers “Вернуть” only when `row.removed.length > 0`. Allow exceptions are not passed to the remove action. The custom allow action separately edits `dns.blockCustomSave` and does not touch per-source removed entries.

The original API registry exposes distinct `dns.blockRemove` and `dns.blockRestore` handlers, and the persisted model includes `Removed []DNSBlockRemoved`. The UI confirms that this removed collection is a suppression list for blocked entries, not a general suppression list for both block and allow records.

## Core correction

The Core manager now:
- accepts RemoveEntry only for domains in a source's Blocked result;
- applies Removed only when merging Blocked entries into the active block set;
- never suppresses a source's Allowed exceptions because an entry was removed from its blocked collection;
- retains RestoreEntry as the inverse operation for the removed blocked entry.

A regression test proves that removing a blocked domain does not remove a source allow exception, and that allow-only domains cannot be passed to RemoveEntry.

## Still open

This establishes the remove/restore data semantics from original UI behavior. Exact cross-list precedence and custom allow precedence still require final verification against the original DNSBlock check/rebuild implementation; do not claim complete parity yet.
