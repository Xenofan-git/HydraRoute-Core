# HRweb 1.68.0 - API transport and DTO inventory

Date: 2026-11-05
Scope: static inspection of AArch64 binary type metadata, embedded frontend JavaScript and handler symbols. No binary execution or system changes.

## 1. API transport contract (frontend)

The frontend API client in extracted/js/core/api.js uses one JSON POST endpoint:
- URL: /api
- HTTP method: POST
- Content-Type: application/json
- CSRF header: X-CSRF, sourced from the csrf cookie
- Request envelope: { "m": "<method-name>", "p": <params-or-empty-object> }
- Default timeout: 120,000 ms
- Long timeout: 600,000 ms

Success handling expects a JSON object with ok === true and returns body.data. It also synchronizes body.ev when present. Failure handling reads body.error and body.code. HTTP 401 is treated as expired session.

Streaming uses GET /api/stream with query parameter m (method name), t (CSRF token), and optional flattened params. The frontend uses this for proxy.pingServers. Connection liveness polling calls system.ping every 3 seconds when idle; timeout is 5 seconds and the lost-state threshold is three failures.

## 2. Frontend method inventory and handler symbols

Observed APIClient methods in extracted frontend:
- system.ping
- proxy.check, proxy.checkRequirements, proxy.install, proxy.status, proxy.start, proxy.stop
- proxy.interfaces, proxy.activeServers
- proxy.createInterface, proxy.deleteInterface
- proxy.fetchSubscription, proxy.refreshSubscription, proxy.interfaceServers
- proxy.applyServerSelection, proxy.pickServers, proxy.serverPicker
- proxy.pingServers (stream)
- proxy.createPhobos, proxy.deletePhobos, proxy.phobosObf
- probe.run

Matching backend symbols include:
- proxy.check -> main.proxyCheck (0x379550)
- proxy.status -> main.proxyStatusMethod (0x3795e0)
- proxy.install -> main.proxyInstall (0x379660)
- proxy.checkRequirements -> main.proxyCheckRequirements (0x379730)
- proxy.createInterface -> main.proxyCreateInterface (0x379840)
- proxy.deleteInterface -> main.proxyDeleteInterface (0x379e40)
- proxy.activeServers -> main.proxyActiveServers (0x37c1b0)
- proxy.interfaces -> main.proxyInterfacesMethod (0x37c260)
- proxy.fetchSubscription -> main.proxyFetchSubscription (0x37c300)
- proxy.refreshSubscription -> main.proxyRefreshSubscription (0x37c690)
- proxy.interfaceServers -> main.proxyInterfaceServers (0x37c870)
- proxy.applyServerSelection -> main.proxyApplySelection (0x37c9a0)
- proxy.pickServers -> main.proxyPickServers (0x37ccc0)
- proxy.serverPicker -> main.proxyServerPicker (0x37cee0)
- proxy.pingServers stream -> frontend SSE call; backend stream dispatcher is registered by setupRoutes/apiStreamDispatch.
- proxy.start/stop are selected by UI state; their backend toggle methods are separate from proxy.status.
- probe.run -> frontend sends {kind, name}; backend probe subject methods exist, but complete dispatch mapping is not included in this inventory.

## 3. Request DTOs recovered from Go runtime type metadata

All listed DTO fields are pointer-typed. This is intentional for optional/partial JSON decoding, but the handler's exact validation still needs to be audited separately.

### ProxyCreateInterfaceRequest (96 bytes)
- name: *string
- subscriptionUrls: *[]string, omitempty
- allSubscriptionLinks: *map[string][]string, omitempty
- subscriptionHeaders: *map[string]string, omitempty
- directLinks: *[]string, omitempty
- transport: *string

Frontend payload includes name, transport, subscriptionUrls, allSubscriptionLinks, directLinks, and subscriptionHeaders only when non-empty.

### ProxyDeleteInterfaceRequest (16 bytes)
- name: *string

Frontend payload: {name}.

### ApplyServerSelectionRequest (72 bytes)
- name: *string
- excluded: *[]ServerKey
- refreshed: *bool
- sources: *map[string]SourceSettings
- autoUpdateEnabled: *bool
- autoUpdateInterval: *int

Frontend payload sends all six fields. The UI computes autoUpdateInterval as total minutes and clamps its lower bound to 5 minutes.

### FetchSubscriptionRequest (32 bytes)
- url: *string
- headers: *string, omitempty

Frontend payload: {url, headers}; an empty header value is sent as undefined/omitted.

### PickServersRequest (40 bytes)
- name: *string
- picked: *[]ServerKey

Frontend sends picked as objects {source, name, nth}; empty picked list means auto mode / entire pool according to UI.

### ServerKey (40 bytes)
- source: *string
- name: *string
- nth: *int, omitempty

### SourceSettings (48 bytes)
- headers: *string, omitempty
- serverFilter: *ServerFilter

### ServerFilter (32 bytes)
- keep: *string, omitempty
- exclude: *string, omitempty

### interfaceNameRequest (16 bytes)
- name: *string
Used by interface-scoped API calls such as interfaceServers, serverPicker and other named-interface operations where this DTO is the decoder target.

### KeeneticLoginRequest (32 bytes)
- login: *string
- password: *string

## 4. Response DTOs and nested data

### ProxyInterfacesResponse (24 bytes)
- interfaces: *[]ProxyInterfaceInfo

### ProxyInterfaceInfo (240 bytes)
- name, description, enabled
- serverCount, poolCount, pickedCount
- hasSubscriptions, muxEnabled, muxCompatible
- strategy, opts
- optional type, phobosMode, phobosTarget, obfRunning, serverName, sysName, downByMonitor, transport, rciName

### InterfaceServersResponse (80 bytes)
- name: *string
- servers: *[]ServerInfo
- sources: *[]SourceInfo
- autoUpdateEnabled: *bool
- autoUpdateInterval: *int

### ServerInfo (96 bytes)
- link, protocol
- optional excluded, unsupported, picked
- optional ping, echo
- serverKey

### SourceInfo (96 bytes)
- url, lastFetchedAt
- optional fetchError
- sourceSettings

### ServerPickerResponse (40 bytes)
- strategy: *string
- servers: *[]PickerServer

### PickerServer (96 bytes)
- link, protocol
- optional picked, ping, echo
- serverKey

### ProxyStatusResponse (16 bytes)
- status: *ProxyStatus (string alias)

### ProxyCheckResponse (1 byte)
- installed: *bool

### InstallRequirementsResponse (56 bytes)
- canInstall: *bool
- proxyComponent: *bool
- enoughSpace: *bool
- availableSpaceMB: *int64
- optional errorMessage: *string
- opkgtunSupported: *bool
- optional opkgtunReason: *string

### KeeneticAuthResponse (24 bytes)
- optional success: *bool
- optional error: *string

### metricsResponse (88 bytes)
- cpu, memPercent, memUsedMB, memTotalMB
- cpuHistory, ramHistory
- optional DNS metrics object

## 5. Persisted config structures relevant to the UI

### InterfaceMeta (272 bytes)
Fields include:
- name, enabled, createdAt, strategy, opts
- autoUpdateEnabled, autoUpdateInterval, autoUpdateNext
- subscriptions, directLinks, excluded, picked
- port, socksUser, socksPass, muxEnabled, iface

### SubscriptionSource (120 bytes)
- url, lastFetchedAt, fetchError, links, sourceSettings

### HrWebConfig (208 bytes)
The full field table and pointer semantics are documented in hrweb-config-patch-save-and-install-contract.md and the GeoSource/CaptiveURL follow-up. Do not collapse nil and explicit zero values.

### HrNeoConfigResponse (624 bytes)
- config: *HrNeoConfig
- defaults: *HrNeoConfig
HrNeoConfig is a separate 312-byte configuration structure; its full field inventory is emitted by the type-metadata inventory and includes routing, ipset, policy, and L7 capture settings. This is not the per-interface Xray metadata structure.

## 6. Debian compatibility design implications

- Implement one authenticated API transport with explicit method registry, typed request/response schemas, CSRF protection, bounded timeouts, and a stable {ok,data,error,code} envelope.
- Use explicit request DTOs and validation. Do not expose internal Go pointer semantics as an accidental API contract; define nullable/optional behavior per field.
- Keep per-interface operations named and scoped. No method should mutate a global Linux route as a side effect.
- Keep configuration GET, mutation, validation, persistence, and runtime apply as distinct operations.
- Keep install requirements, binary installation, service status, and SOCKS readiness separate.
- Preserve the pool/server-key model (source, name, nth) so duplicate subscription entries remain addressable.
- Preserve the distinction between pool membership (excluded) and active selection (picked).
- Keep stream/ping events separate from ordinary request/response methods.
- Do not port Entware-only install fields such as opkgTunSupported as Linux package assumptions; translate capability checks to Debian-native facts.

## 7. Still open

- Full route-registration map and exact decoder DTO for every method, especially proxy.start/stop, probe.run, proxy.activeServers and Phobos methods.
- Exact handler validation/error mapping for create, apply selection, pick servers and fetch/refresh subscription.
- Exact response payloads for create/delete/apply operations beyond frontend-consumed message, poolCount and refreshError.
- Full HrNeoConfig runtime defaults and mutation/commit semantics.
- Full API auth/session and stream authorization boundary (login/CSRF/session middleware exists; complete method-specific authorization not mapped here).

## Evidence

- Frontend API client: extracted/js/core/api.js.
- Frontend call sites: extracted/js/modules/connections/proxy-manager.js, server-selection.js, server-picker.js, server-ping.js.
- Backend handlers: funcs-full.tsv entries from 0x379550 through 0x37cee0.
- Go type metadata: type_metadata.py; descriptors for ProxyCreateInterfaceRequest (0x4b88e0), ApplyServerSelectionRequest (0x4b87e0), FetchSubscriptionRequest (0x485080), PickServersRequest (0x485120), InterfaceMeta (0x4d39a0), ProxyInterfaceInfo (0x4d61e0), HrWebConfig (0x4d8020), HrNeoConfigResponse (0x4847c0), InstallRequirementsResponse (0x4be400).

Static analysis only. HRweb binary was not executed; no system or network state was changed.


## 8. Handler-level JSON decoder targets confirmed

The request type mapping was cross-checked against the concrete JSON decoder type descriptors loaded by the backend handlers (not inferred from DTO names alone):

| API method | Handler | Pointer type descriptor | Decoded Go type |
|---|---|---:|---|
| proxy.createInterface | main.proxyCreateInterface (0x379840) | 0x4284e0 | *main.ProxyCreateInterfaceRequest |
| proxy.applyServerSelection | main.proxyApplySelection (0x37c9a0) | 0x4283e0 | *main.ApplyServerSelectionRequest |
| proxy.fetchSubscription | main.proxyFetchSubscription (0x37c300) | 0x428420 | *main.FetchSubscriptionRequest |
| proxy.refreshSubscription | main.proxyRefreshSubscription (0x37c690) | 0x428660 | *main.interfaceNameRequest |
| proxy.pickServers | main.proxyPickServers (0x37ccc0) | 0x4284a0 | *main.PickServersRequest |

The pointer descriptor names were independently decoded through the Go runtime type metadata reader. This confirms the five mappings above.

The other methods in the frontend inventory are not yet all mapped to their exact decode target. In particular, proxy.interfaceServers and proxy.serverPicker use helper/dispatch paths that were not shown to call json.Unmarshal directly in their top-level handlers; do not assign their decoder DTO solely from the similar {name} payload without tracing the dispatcher.


## 9. interfaceServers and serverPicker DTO mapping closed

The shared helper main.proxyGetInterfaceName (0x37c5a0) decodes the request parameter into pointer descriptor 0x428660, independently resolved as *main.interfaceNameRequest. Both top-level handlers call this helper:
- proxy.interfaceServers -> main.proxyInterfaceServers (0x37c870), request *main.interfaceNameRequest; response pointer descriptor 0x428460 = *main.InterfaceServersResponse.
- proxy.serverPicker -> main.proxyServerPicker (0x37cee0), request *main.interfaceNameRequest; response pointer descriptor 0x4285a0 = *main.ServerPickerResponse.

This closes the two request mappings previously left open. Their response descriptor names were independently decoded from Go runtime type metadata.


## 10. Remaining probe, Phobos and per-interface toggle request shapes

Additional handlers decode anonymous Go request structs. Their pointer type descriptors were decoded independently:

| API method | Handler | Decoder descriptor | Decoded request fields |
|---|---|---:|---|
| probe.run | main.probeRun (0x378a60) | 0x4320e0 | kind string, name string |
| proxy.createPhobos | main.proxyCreatePhobos (0x3755b0) | 0x4322e0 | name string, link string |
| proxy.deletePhobos | main.proxyDeletePhobos (0x375790) | 0x432220 | name string |
| proxy.phobosObf | main.proxyPhobosObf (0x3759a0) | 0x432220 | name string |
| proxy.togglePhobos | main.proxyTogglePhobos (0x375bc0) | 0x432220 | name string |
| proxy.toggleXrayIface | main.proxyToggleXrayIface (0x37a8b0) | 0x432220 | name string |
| proxy.toggleExtIface | main.proxyToggleExtIface (0x37b040) | 0x432220 | name string |
| proxy.toggleOpt | main.proxyToggleOpt (0x37a050) | 0x432320 | name string, opt string |
| proxy.toggleMux | main.proxyToggleMux (0x37a3e0) | 0x432260 | name string, iface string |
| proxy.changeStrategy | main.proxyChangeStrategy (0x37a590) | 0x432360 | name string, strategy string |

The UI call sites confirm:
- probe.run sends kind and name.
- createPhobos sends name and link.
- deletePhobos, phobosObf and interface toggle methods send name.
- toggleOpt sends name and opt.
- changeStrategy sends name and strategy.
- toggleMux currently sends name only; the decoder struct also contains iface, which remains empty when omitted by this frontend call.

These anonymous request structs use plain string fields rather than pointers, so missing/null values decode to empty strings rather than preserving pointer-level nil distinctions. Handler-side validation remains a separate audit item.


## 11. proxy.pingServers SSE contract

Frontend call sites:
- Server picker: proxy.pingServers?name=<interface>
- Subscription selection modal: same method with name, all=1, and optional refreshed=1.

APIClient.stream sends GET /api/stream with query fields m=proxy.pingServers, t=<CSRF token>, plus flattened name/all/refreshed values.

Observed SSE JSON event shapes consumed by the frontend:
- Initial target announcement: {links:[...]} — initializes pending state and total count.
- ICMP echo result: {echo:<result>, link:<link>}.
- Proxy/Xray ping result: {ping:<result>, link:<link>}.
- Completion: {done:true, error?:<message>}.

The frontend stores echo and proxy ping separately. ICMP echo is explicitly a router-to-server reachability measurement without Xray; proxy ping is the server delay measured through the managed proxy path. These measurements must not be conflated in Debian metrics or balancer scoring.
