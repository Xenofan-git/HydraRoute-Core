# HRweb active outbound index mapping — 2026-11-09

## Scope

Static-only analysis of the ARM64 HRweb 1.68.0 binary and its Go disassembly. The binary was not executed. This note closes the previously open question of how an Xray balancer principle-target tag is mapped back to a pool entry.

## Confirmed mapping

1. `main.outboundTag` is at `0x37e3c0`. Its format string is `out%d_%05d_%d_%d` (rodata at `0x4ec089`, 17 bytes).
2. `main.buildManagedConfig` is at `0x37e900`. Its caller at `0x381948` passes the current `*InterfaceMeta` and the interface ordinal as the first two arguments.
3. `buildManagedConfig` obtains the complete `InterfaceMeta.pool()` slice, then iterates that pool with a zero-based index. It separately checks picked membership while iterating; it does not renumber the pool to the picked subset.
4. Each generated outbound tag receives:
   - the interface ordinal as the third numeric component;
   - the current index in the full `pool()` slice as the fourth numeric component.
5. `main.parseOutboundTag` is at `0x37e4d0`. It takes the suffix after the last dot, splits it on underscores into exactly four parts, checks that the first part begins with `out`, parses parts 3 and 4 as integers, and returns those two indices with a validity flag.
6. `main.activeBalancerServers` (0x380f70) consumes the parsed values: it checks the interface ordinal against the current interface index, checks that the pool index is within the current pool length, then resolves the corresponding `metaServer` from `InterfaceMeta.pool()`. The result is associated with `InterfaceMeta.Name` and the resolved server link/name.
7. The first two tag components are not used for this reverse lookup; the final two components are the interface ordinal and full-pool index. The tag is not a persisted `ServerKey`.

## Important distinction

The tag mapping is index-based in HRweb. Core already has a more stable identity available: deterministic outbound tags derived from profile ID and the full `ServerKey` (`Source + Name + Nth`). Core should continue resolving Xray status through those stable tags rather than copying HRweb's ordinal-based identity scheme.

The Xray `GetBalancerInfo` response contains `principle_target.tag` candidates. HRweb's callback keeps the last repeated tag, but this is not proof of a currently active socket/connection: for Round Robin, Xray returns a candidate list. If Core exposes this value, label it as the selected/candidate target unless a separate connection-level signal is available.

## Remaining uncertainty

- `activeBalancerLinks` selects records whose internal `metaServer.Filtered` flag is true. That flag is distinct from `Excluded` and `Unsupported`; it must not be collapsed into health, manual selection, or balancer membership.
- `InterfaceMeta.pool()` removes unsupported records only when both `Filtered` and `Excluded` are false. Filtered/excluded records remain in the metadata pool for index stability.
- The exact construction and semantic polarity of `Filtered` remains a separate question. Do not assume that it means “offline”, “selected”, or “currently in balancer”.

## Implementation consequence for HydraRoute Core

- Keep stable `ServerKey`-derived outbound tags.
- Resolve a principle-target tag only against the current profile's eligible outbound-tag map.
- Reject unknown tags, stale profile generations, and tags not present in the current balancer selector.
- Do not present the last principle-target tag as a guaranteed active TCP connection.
- Keep manual exclusion, source filtering, selected pool membership, Xray health, and display state as separate fields.

## Evidence locations

- `outboundTag`: `0x37e3c0`
- `parseOutboundTag`: `0x37e4d0`
- `buildManagedConfig`: `0x37e900`
- `buildManagedConfig` caller/interface ordinal: `0x381948`
- `activeBalancerLinks`: `0x380cd0`
- `activeBalancerServers`: `0x380f70`
- Full disassembly: `/root/hrweb-research/hrweb.unpacked`
