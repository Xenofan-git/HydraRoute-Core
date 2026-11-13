# HRWeb balancerActiveTag: exact protobuf extraction

Date: 2026-11-09

## Function locations

- `main.xrayAPICall`: `0x37fb60`
- `main.balancerActiveTag`: `0x380270`
- `main.protoWalk`: `0x380740`
- `main.protoField`: `0x380b00`
- `main.protoField.func1`: `0x380b80`
- `main.protoVarint`: `0x380c20`
- `main.observatoryStatus`: `0x3804d0`

Full AArch64 disassembly saved alongside this report as `balancerActiveTag.asm`, `protoField.asm`, `protoWalk.asm`, and `protoVarint.asm`.

## Request and response

`balancerActiveTag` constructs a request and calls `xrayAPICall` with operation ID **54** (`0x36`). It then invokes `protoField` sequentially with field numbers **1**, **6**, and **1**.

The gRPC method is `xray.app.router.command.RoutingService/GetBalancerInfo`.

The schema path is:

1. `GetBalancerInfoResponse.balancer` (field 1)
2. `BalancerMsg.principle_target` (field 6)
3. `PrincipleTargetInfo.tag` (repeated field 1)

The operation is different from `observatoryStatus`, which uses operation ID 70.

## Repeated-field behavior

`protoField` delegates to `protoWalk` with a callback. The callback at `0x380b80` compares the incoming field number with the requested number. On each match it overwrites the captured `(value pointer, value length)` pair with the current occurrence. `protoWalk` iterates the protobuf message in wire order and invokes the callback for each occurrence.

Therefore, when the innermost field `tag` occurs repeatedly, `balancerActiveTag` returns the **last** matching tag. It does not return the first. This is now reflected by `GetBalancerPrincipleTargetTag` and a two-element repeated-field regression test.

## Semantic caveat

Upstream Xray `RoutingService.GetBalancerInfo` obtains `BalancerPrincipleTarget.GetPrincipleTarget`. Xray's Round Robin strategy returns the entire candidate slice. Thus the returned scalar from HRWeb is the last strategy principle-target candidate, not necessarily proof of the outbound carrying an already established connection. Preserve this distinction in Core.

## Core implementation status

- Dynamic protobuf client for GetBalancerInfo exists in `internal/xrayapi/balancer.go`.
- `GetBalancerPrincipleTargetTags` returns the complete list.
- `GetBalancerPrincipleTargetTag` returns the last tag to match HRWeb scalar extraction.
- Wire-contract test includes two repeated tags and asserts the last-value semantics.
- Fallback URI matching/priority has not yet been implemented.

Validation: full Go tests, `go vet`, and `git diff --check` passed. No service restart, deployment, network changes, commit, or push.

## Follow-up: proxy.activeServers handler chain (2026-11-09)

Frontend evidence (`extracted/js/modules/connections/proxy-manager.js`):
- `updateActiveServers()` considers only interfaces where `poolCount > 1`.
- It calls `APIClient.call('proxy.activeServers')` and reads `result.servers`.
- It stores `servers[iface.name] || ''` into `proxy.activeServer.<name>`.
- UI renders this value as the active server label (around lines 587-592).

Handler registry:
- `proxy.activeServers` -> `main.proxyActiveServers` at `0x37c1b0`.
- Handler calls `main.activeBalancerServers` at `0x380f70` and serializes the result under `servers`.

Recovered helper symbols:
- `main.activeBalancerLinks` at `0x380cd0`.
- `main.activeBalancerServers` at `0x380f70`.
- `main.balancerActiveTag` at `0x380270`.

Disassembly observations:
- `activeBalancerServers` calls `activeBalancerLinks` to obtain active balancer/interface records.
- It iterates these records, calls helper `0x38e1d0` to resolve an interface/balancer descriptor, then calls `balancerActiveTag` (`0x380270`) for a tag candidate.
- It also calls `0x37e4d0` in the candidate-validation path before accepting/storing the result. This is likely a health/state validation step; exact semantics still require disassembling `0x37e4d0` and following the data layout.
- `activeBalancerLinks` obtains a list through `0x38d280`, filters records using a flag at byte offset `+16`, and constructs a compact list. The exact flag meaning is not yet established.
- The response is therefore not simply the last `PrincipleTargetInfo.tag`: the handler first selects active links/interfaces, resolves per-link balancer metadata, validates candidate state, and only then builds the per-interface `servers` map.

Do not yet claim exact active connection semantics or implement fallback selection from this alone. Next reverse-engineering targets: `0x37e4d0`, `0x38e1d0`, `0x38d280`, and the relevant `activeBalancerLinks` data structure. Existing `balancerActiveTag` protobuf extraction remains the last repeated `PrincipleTargetInfo.tag`, which is only one stage in this larger handler chain.

## Follow-up: candidate validation and active-link enumeration

Additional disassembly recovered on 2026-11-09:

### `main` helper at `0x37e4d0`
- It accepts a byte/string-like value plus a length and uses bounded byte/string comparisons (`0x12a00`) against embedded constants.
- It then parses/extracts two structured values through helper `0x9e7b0` at offsets `+32` and `+48` of a parsed object.
- The final return includes a boolean-like third result. If the parsed fields are absent/invalid, it returns an empty/false result.
- This is consistent with validation/parsing of a structured endpoint/URI, but the exact field names and predicate are not proven yet. Do not label it as a health check until its callers/data layout are fully decoded.

### `activeBalancerLinks` at `0x38d280`
- Builds an initial list using an embedded query/key and a generic iterator/helper chain.
- Iterates the returned records and constructs a new list of records with a flag byte at offset `+16` set.
- For each record it resolves/constructs associated metadata via `0x2fb260`, and records a derived value from a nested object (`+8`) together with an index/count.
- The list therefore represents a filtered set of active-link records, not simply all configured interfaces. Exact query semantics and flag meaning remain open.

### `activeBalancerServers` at `0x380f70`
- Consumes the filtered link list.
- For each link, resolves its descriptor via `0x38e1d0` (which delegates through `0x38dbc0` and a lookup helper `0x3a11f0`).
- Calls `balancerActiveTag` and a separate helper `0x37e4d0` on candidate data before writing a per-interface result.
- It iterates additional per-candidate records and updates a map keyed by the active interface identity.

Still not proven: whether the candidate is selected by matching the current outbound's share URI, by a runtime endpoint tuple, or by another identity field. Do not implement the earlier hypothesized fallback priority until this is resolved. The parser's memory-only `ReferenceURI` is groundwork, not yet used by active-server logic.

## Follow-up: InterfaceMeta server/pool pipeline (2026-11-09)

Function table cross-check (`funcs-full.tsv`) corrected several earlier tentative interpretations:
- `0x37e4d0` is `main.parseOutboundTag`, not a health-check helper.
- `0x38d280` is `main.loadAllInterfaceMetas`, not `activeBalancerLinks`.
- `0x38dbc0` is `(*InterfaceMeta).servers`.
- `0x38e1d0` is `(*InterfaceMeta).pool`.
- `0x3a11f0` is the generic `slices.DeleteFunc` instantiation for `[]main.metaServer`.

Disassembly of `(*InterfaceMeta).pool` (`0x38e1d0`):
- Calls `(*InterfaceMeta).servers` (`0x38dbc0`) to obtain the initial `[]metaServer` slice.
- Passes that slice through `slices.DeleteFunc[[]main.metaServer]` at `0x3a11f0` with a closure/predicate.
- Returns the filtered slice. Thus HRNeo's pool is a filtered view over the interface's server records, not necessarily the raw list.

Disassembly of `slices.DeleteFunc` confirms the generic algorithm scans 64-byte `metaServer` records, invokes a supplied predicate for each, and compacts the slice by removing records for which the predicate returns true. The callback's exact field predicate still needs resolution; do not yet assert whether it removes filtered, excluded, unsupported, or another class.

Disassembly of `(*InterfaceMeta).servers` (`0x38dbc0`) shows it reads structured slices from the InterfaceMeta receiver, iterates a source collection, invokes a closure to construct per-server records, and appends 64-byte records to the result. A further helper at `0x38e230` appears to build/transform a related collection and includes a branch on receiver field offset `+184`; exact field names remain unresolved.

Important correction to previous note: earlier prose described `0x38d280` as active-link enumeration and `0x37e4d0` as candidate validation. Those assignments were wrong; the symbol table identifies them as `loadAllInterfaceMetas` and `parseOutboundTag`. Re-evaluate the prior call-chain interpretation using the corrected symbol addresses before implementing active-server behavior.

No Core behavior changed. No runtime/network configuration touched. Next: decode `loadAllInterfaceMetas`, `parseOutboundTag`, the `pool()` DeleteFunc closure, and `metaServer` field layout; then revisit `activeBalancerLinks` at its actual symbol address `0x380cd0`.

## Follow-up: loadAllInterfaceMetas disassembly (2026-11-09)

Disassembly of `main.loadAllInterfaceMetas` at `0x38d280`:
- Initializes two key/value-like query descriptors from embedded constants (one descriptor length 24, another length 11) and calls the generic iterator/store helper chain.
- Iterates the returned collection and processes each entry through helper `0x2fb260`; the result controls whether a metadata record is constructed/appended.
- For accepted entries it builds a structured record, invokes helper `0xe0bd0` with the current entry and associated metadata, and appends a derived nested value plus iteration index/count into a result collection.
- This is metadata loading/normalization, not active-link enumeration. The exact meanings of embedded keys, helper `0x2fb260`, and record fields remain unresolved.

The neighboring helper at `0x38d850` contains a literal byte comparison corresponding to a URL delimiter pattern (`://`) and delegates parsing/normalization to other helpers. This supports treating some metadata fields as URL-like values, but does not yet prove that they are the identity key used to map an active Xray tag to a server row.

Do not infer connection type, online state, or identity semantics from these offsets alone. Continue with the exact symbol table for helpers around `0x2fb260`, `0xe0bd0`, `0x38d850`, and closure targets, then decode the `pool()` DeleteFunc predicate.

## Confirmed metaServer flags and pool predicate (2026-11-09)

The Go type-instantiation symbol for `slices.DeleteFunc` exposes the exact anonymous struct shape:
`struct { Link string; Filtered bool; Excluded bool; Unsupported bool; main.ServerKey }`.
Thus the record is 64 bytes on this build, with the first string occupying 16 bytes followed by three independent boolean fields at offsets +16, +17, +18, and an embedded ServerKey.

The predicate is named `main.(*InterfaceMeta).pool.func1` at `0x39fe20`. Disassembly shows:
- If `Filtered` is true, return false (do not delete).
- Else if `Excluded` is true, return false (do not delete).
- Else return `Unsupported`.

Therefore `InterfaceMeta.pool()` removes exactly records that are **unsupported and neither filtered nor excluded**. Filtered and excluded records are deliberately retained in the pool view. This is not a health/offline filter. This distinction is essential: the UI's filtered/excluded metadata is retained for visibility/selection semantics, while unsupported records are removed from ordinary pool unless explicitly marked filtered or excluded.

`activeBalancerLinks()` at `0x380cd0` calls `loadAllInterfaceMetas()` at `0x38d280`, iterates each interface's `servers()` result, and tests the boolean at record offset +16 (`Filtered`). It skips records where that bit is false and builds its output only from records where `Filtered` is true. The output is then consumed by `activeBalancerServers()` at `0x380f70`, which iterates the resulting interface/server associations and uses each interface's `pool()` plus `balancerActiveTag()` and `parseOutboundTag()` when resolving the displayed server. This establishes that the `Filtered` flag is an intentional eligibility/selection dimension in the active-server relation, not a health result.

The active-tag path remains more specific than simply taking a tag: `balancerActiveTag` performs Xray API operation 54 and parses nested protobuf fields; `activeBalancerServers` resolves a metadata association, obtains the filtered pool, and checks a parsed tag against candidate records before mapping to the interface result. The exact URI/key comparison and ordering should still be decoded before implementing parity.

No runtime code changed. No subscription, profile, route, firewall, DNS, service, camera, Tailscale, or VPN configuration changed.

## Follow-up: xrayServerName and active association (2026-11-09)

Function table confirms `main.xrayServerName` at `0x381090`. Its disassembly:
1. Calls `loadAllInterfaceMetas()`.
2. Iterates metadata entries and compares the requested interface identifier against a string field at offset `+256` in each interface metadata record (length-aware byte/string compare).
3. For a match, calls `InterfaceMeta.pool()`.
4. If the pool length is exactly one, extracts the server name directly from that record's `Link` via `extractServerName()` (`0x38d850`).
5. Otherwise calls `activeBalancerServers()` (`0x380f70`) and retrieves the result associated with the matched interface key from a shared map.
6. Returns empty when no interface metadata match exists.

This confirms a distinct single-server fast path and multi-server active-balancer path. It also confirms the key used to locate an interface is a field in the interface metadata record, while the server display name comes from `Link` parsing.

Function table further identifies `main.serverOutbounds` at `0x37e640`, `main.parseShareURL` at `0x385630`, and `main.applyXRayOptionsToOutbound` at `0x38b050`. The active server resolution path invokes `parseOutboundTag` (`0x37e4d0`) on Xray's selected tag and uses `parseShareURL` during candidate comparison; this suggests matching is performed on parsed share identity rather than simply displaying the raw Xray tag. Exact field-by-field comparison and duplicate/order behavior still need confirmation.

No implementation changes. No runtime/network changes. Next decode `parseShareURL`, `serverOutbounds`, and the `activeBalancerServers` comparison loop to establish which components of the parsed URL are used as identity and how multiple matches are resolved.

## Follow-up: parseShareURL call shape (2026-11-09)

`main.parseShareURL` is at `0x385630`. It first calls `0x385460` to normalize/prepare the input and receives a multi-value result including a function/handler slot. If that slot is non-nil, it invokes the returned handler with the original input and option arguments. Otherwise it initializes a structured result and calls a generic URL parser with an embedded option key at `0x43b940`; it returns the parser's structured output/error tuple. This is a parser/normalizer wrapper, not a raw string equality function.

A separate nearby function at `0x385730` performs delimiter/substring boundary checks around embedded string constants and is not yet proven to be the exact equality predicate used by active-server matching. `activeBalancerServers` invokes `parseShareURL` with the candidate server record's `Link` and the parsed outbound tag's extracted data, then passes the result into outbound option application/record construction. Do not yet claim which URL fields (scheme, host, port, userinfo, fragment, transport options) participate in the identity key.

The xrayServerName fast path and multi-server path are now documented. Remaining reverse engineering is concentrated on the `parseOutboundTag` return fields, `parseShareURL` structured result, and `serverOutbounds` mapping. No runtime or Core behavior changed.

## Follow-up: parser internals and protocol dispatch (2026-11-09)

Further decoded `main.parseShareURL`'s helper `0x385460`:
- It obtains a registered parser/handler table from a global registry (`0xa41000 + 0x6a0`).
- It iterates records with a 16-byte stride; each record contains a string key and a function/handler value.
- It compares the requested scheme/key against the registered string and, on match, returns the handler plus associated values. No match yields an empty/zero result.
- `parseShareURL` (`0x385630`) invokes that handler when found; otherwise it falls back to the generic URL parser. Thus the parser is explicitly scheme-dispatched and can apply protocol-specific parsing before the generic fallback. This is stronger than the earlier generic wrapper description.

`parseOutboundTag` (`0x37e4d0`) evidence:
- It performs a last-index search using a one-byte delimiter constant at `0x752490` (first byte `.`), then a bounded split/search using constants at `0x753aa0` (bytes include `_` and `\\`).
- It requires a four-part result at one stage (`cmp ... #4`), then extracts two string fields from offsets +32 and +48 of the parsed record and compares them using Go string comparison helpers. It returns a structured string plus a boolean-like validity result; malformed/short inputs return empty/false.
- Exact meaning/order of the four parts is not yet proven. The constants are recorded as raw bytes, not assigned semantic labels.

`serverOutbounds` (`0x37e640`) walks a collection of candidate entries and calls `parseShareURL` on candidate link strings. On successful parse, it passes parsed URL/option data plus flags from the server/outbound record to `applyXRayOptionsToOutbound` (`0x38b050`) and appends a resulting record. On the alternate branch it calls `0x37d800` with the record's option flags and similarly appends a result. This function is a builder/transformer of outbound records, not itself a simple tag-to-key lookup.

`activeBalancerServers` uses `parseOutboundTag` on the selected Xray tag and then compares against candidate server records, using `serverOutbounds`/parsed share URL processing. Do not implement a guessed string/URI equality rule. Still need type-layout confirmation for the four-part tag and the exact candidate comparison fields/order.

No Core code/runtime/network changes. This remains offline reverse engineering.

## Follow-up: concrete ServerKey layout and pickedIn (2026-11-09)

Recovered Go type metadata from `type_metadata.txt`:

`main.ServerKey` is a 40-byte struct:
- offset 0: `Source string` (`json:"source"`)
- offset 16: `Name string` (`json:"name"`)
- offset 32: `Nth int` (`json:"nth,omitempty"`)

This is the canonical persisted identity used in `InterfaceMeta.Excluded` and `InterfaceMeta.Picked`; `PickerServer.ServerKey` embeds/presents the same key. Do not substitute a URL string or display label for this identity.

`InterfaceMeta` layout confirms:
- `Excluded *[]ServerKey` at +160
- `Picked *[]ServerKey` at +184
- `Iface *string` at +256

`pickedIn()` (`0x38e230`) operates on the `Picked` key slice. It builds a set/map keyed by `ServerKey` (40-byte records) and filters a supplied list of `ServerKey` values against that membership. Its result is a selected-key subset, not a health or connectivity result. Empty/nil picked slice returns empty results. This supports keeping manual selection state independent from ping/observatory health.

`pickedIn.func1` performs map membership lookup and returns the inverse boolean expected by `slices.DeleteFunc`, so it retains the records found in the picked-key set. The selection identity is the full comparable `ServerKey` (Source + Name + Nth), not just Name.

No implementation changes. Further work: decode `parseOutboundTag` into the exact `ServerKey` triple and inspect `prunePicked`/`pick`/`pruneExcluded` to capture refresh and manual selection semantics before implementing active-server resolution.

## Follow-up: selection pruning lifecycle (2026-11-09)

Further disassembly of `InterfaceMeta.prunePicked` (`0x38e4f0`) and `pruneExcluded` (`0x38eae0`) establishes their common pattern:

- `prunePicked` constructs a membership set from the supplied current candidate `ServerKey` slice, then applies `slices.DeleteFunc` to the existing `Picked` slice using full `ServerKey` equality. The retained result is written back to `InterfaceMeta.Picked`.
- `pruneExcluded` obtains the current `InterfaceMeta.servers()` collection, constructs a membership set from its embedded `ServerKey` values, then prunes the existing `Excluded` slice against that set and writes the result back.
- These are referential-integrity operations after the available server inventory changes: stale keys that no longer exist are removed. They are not health-based cleanup and do not clear keys merely because a server is temporarily offline.
- Both use the same `ServerKey` (Source, Name, Nth) identity recovered above.

`pick` (`0x38e730`) starts by obtaining `pool()` and constructs a map keyed by the supplied ServerKey values. It compares candidate keys against the requested set and builds a resulting selected-key slice; the empty-input path clears `Picked`. The exact user-facing error and edge semantics are still being traced, so this is not yet a complete behavioral specification.

`outboundTag` (`0x37e3c0`) constructs a JSON-shaped tag from two string inputs plus flags/optional numeric data; its encoding has bounded numeric handling (upper bound 99999) and explicit optional-field behavior. `parseOutboundTag` parses the corresponding four-part intermediate representation and returns two string fields plus a validity result. The string-to-ServerKey mapping is not yet fully proven; do not use this partial inverse as a substitute for the canonical ServerKey identity.

No Core code changed in this step. No live services, routes, DNS, firewall, or VPN configuration touched.

## Follow-up: tag encoder call path and active-balancer consumer (2026-11-09)

Additional caller tracing:

- `main.outboundTag` is called from the per-server outbound construction path inside `main.buildManagedConfig` (`0x37e900`). It receives two strings extracted from the prepared outbound/server record, along with status/optional numeric arguments; the returned tag is stored on the generated outbound entry. This confirms tags are generated during Xray config compilation, not stored as the canonical persisted `ServerKey` itself.
- `main.parseOutboundTag` has call sites in the balancer-active-tag and active-server-resolution paths. The parsed pair is then compared/used with metadata candidates; it is a runtime tag decoder, not the canonical identity type.
- `main.activeBalancerServers` (`0x380f70`) obtains the active-link relation from `activeBalancerLinks`, iterates the relation records, calls `extractServerName` on one side, and resolves the active tag for the interface via `balancerActiveTag`. It constructs a map keyed by interface name. The exact server-candidate comparison and duplicate resolution still require the lower-level relation layout and parser semantics.
- `balancerActiveTag` parses the Xray protobuf response; `parseOutboundTag` is called only after a tag value is retrieved. Do not assume a tag uniquely identifies a persisted ServerKey without applying the metadata relation.

Important distinction: `ServerKey{Source,Name,Nth}` is persisted selection identity; generated Xray outbound tags are derived runtime identifiers. Core must preserve this two-layer model and explicitly resolve runtime tag back to candidate metadata.

No code changes made. Existing tests and vet passed in the previous step; no live configuration changes.

## Follow-up: active-link relation and candidate ordering (2026-11-09)

Further analysis of `activeBalancerLinks` (`0x380cd0`) and `InterfaceMeta.servers` (`0x38dbc0`) confirms:

- The relation starts from `loadAllInterfaceMetas()` and retains only server records whose `Filtered` flag is true. This is the active balancer's filtered candidate relation, not every server in the subscription.
- For each interface metadata record, the function groups/collects the filtered server records and then uses the corresponding interface's pool/candidate list for resolution. The server records are represented as 64-byte values during this stage, while `metaServer` itself is 64 bytes.
- `activeBalancerServers` walks the relation in its stored order. For each interface it calls `balancerActiveTag`, parses the returned tag, then attempts candidate resolution. It does not simply take the first server or compare display names.
- `InterfaceMeta.servers()` builds server records from persisted interface metadata, including the current `Excluded` slice at offset +160. The server record carries the full `ServerKey` identity plus flags; the separate `pool()` operation then removes unsupported records only under its own predicate.
- The filtered relation and pool are different views. A server can be in the full server inventory, be filtered for balancer consideration, and still be manually excluded; these dimensions must not be collapsed into one boolean.

The remaining gap is exact decoding of the four tag components and their comparison against the 64-byte candidate records, including duplicate-key order. The available disassembly has not yet proven the semantic labels for each tag component. No guessed matching rule should be introduced.
