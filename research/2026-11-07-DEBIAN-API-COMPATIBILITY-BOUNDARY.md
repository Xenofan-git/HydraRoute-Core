# Debian API compatibility boundary — HRweb 1.68.0

Date: 2026-11-07

## 1. Full HRweb dispatcher vs Debian product scope

The regular HRweb dispatcher contains 114 unique methods (complete registry in `research/2026-11-05-api-handler-registry-full.md`). Streaming methods use a separate registry. This is the whole HRweb/HRNeo web control plane, not a list of methods that all belong in the independent Debian proxy manager.

The independent Debian product should preserve the proxy-management concepts and user-visible workflows, not reproduce Entware/NDM-only endpoints that operate unrelated router services.

## 2. Core proxy API surface for Debian

Required concepts and candidate method family:

- lifecycle: `proxy.check`, `proxy.status`, `proxy.start`, `proxy.stop`;
- interface management: `proxy.interfaces`, `proxy.createInterface`, `proxy.deleteInterface`;
- subscriptions/pools: `proxy.fetchSubscription`, `proxy.refreshSubscription`, `proxy.interfaceServers`, `proxy.activeServers`;
- selection: `proxy.applyServerSelection`, `proxy.pickServers`, `proxy.serverPicker`;
- telemetry: `proxy.pingServers` SSE, `probe.run`, `health.status`;
- strategy/options: `proxy.changeStrategy`, `proxy.toggleOpt`, `proxy.toggleMux`;
- optional Phobos: `proxy.createPhobos`, `proxy.deletePhobos`, `proxy.phobosObf`, `proxy.togglePhobos`.

The Debian API may use cleaner method names and explicit typed DTOs; it should preserve the data model where compatibility is useful: interface name, source URL, source/name/nth server key, excluded membership, picked selection, per-source headers/filter, auto-update settings, and separate echo-vs-proxy-ping measurements.

`proxy.install` and `proxy.checkRequirements` are Entware/opkg installation workflows in HRweb. Debian should provide a Debian-native installer/status workflow rather than porting opkg assumptions or `opkgTunSupported` fields.

## 3. Router/NDM-specific API families — separate adapter boundary

These methods belong to the Keenetic/HRNeo control plane and must not be silently implemented as Linux host mutations:

- `config.*`, `policies.*`, `policy.connections.*`, `interfaces.*`;
- `hrneo.*`, `rci.*`;
- `dns.*`, `geo.*`, `fqdn.*`;
- `files.*`, `net.routerWeb`, `net.fetchProxy`;
- router update, metrics, event, and device-specific methods.

For the independent Debian proxy core, keep these behind a `KeeneticAdapter` boundary. The adapter should expose read/plan/apply/reconcile operations and be independently permissioned. Core Xray/profile lifecycle must work without an adapter and must never mutate Keenetic or Linux routes as an implicit side effect.

## 4. Proxy methods with RCI coupling

Several methods under the `proxy.*` namespace still perform router-side mutations:

- `proxy.toggleXrayIface` updates per-interface metadata and commits RCI/config operations.
- `proxy.toggleExtIface` follows a separate external-interface path and does not show the same commit sequence in the inspected handler.
- `proxy.setOutInterface` mutates an interface's selected outgoing interface through an RCI-backed helper.

These are not ordinary UI toggles. In Debian, represent them as optional adapter operations with explicit preview, permission, transaction ID, read-back, and rollback. Never translate them into global `ip route` changes.

## 5. Frontend compatibility defects and non-contracts

- `proxy.toggleMux` decoder expects `{name, iface}`, while the inspected frontend sends only `{name}`. The missing `iface` becomes an empty string. Do not assume empty means all interfaces or reproduce this ambiguous call. Debian should make the target explicit.
- `proxy.installed` is frontend state, not a registered API method.
- `proxy.pingServers` is a stream method, not part of the 114 regular methods.
- `proxy.start` and `proxy.stop` are generic service method closures (`Service.Enable`/`Disable`), not separate handler implementations.

## 6. Compatibility completion criteria

Before implementing the API:

1. Freeze the exact frontend workflows to support.
2. Define request/response DTOs for those workflows only.
3. Specify null/missing/empty semantics explicitly; do not inherit Go pointer behavior accidentally.
4. Define typed error codes and operation-state transitions.
5. Keep router-adapter methods optional and explicitly authorized.
6. Add contract tests for duplicate subscription entries (`source,name,nth`), empty picked list (auto mode), excluded-vs-picked distinction, per-source filters, and ping-vs-echo events.

## 7. Known remaining API research gaps

- Exact validation/error code mapping for all proxy handlers is not complete.
- Exact response DTOs for every mutation are not required for the first Debian API and remain untraced where the frontend does not consume them.
- Full per-method authorization mapping is not complete; Debian must implement its own explicit role/permission model rather than copying assumptions.

## Evidence

- Complete registry: `research/2026-11-05-api-handler-registry-full.md`.
- Request/response DTO inventory and handler decoder targets: `research/2026-11-05-api-transport-dto-inventory.md`.
- Toggle/RCI behavior: `research/2026-11-05-mux-option-persistence-followup.md`, `research/2026-11-05-phobos-service-toggle-handler-followup.md`, and `research/2026-11-05-selector-outbound-membership-audit.md`.

Static analysis only. No API call or router/network mutation was performed.
