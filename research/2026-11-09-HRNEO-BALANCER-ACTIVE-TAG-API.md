# HRNeo active balancer tag API recovery

Date: 2026-11-09

## Static evidence

Original HRweb function table identifies:
- `main.xrayAPICall` at `0x37fb60`
- `main.balancerActiveTag` at `0x380270`
- `main.observatoryStatus` at `0x3804d0`

The string table contains the exact method path:
`xray.app.router.command.RoutingService/GetBalancerInfo`.

Disassembly of `main.balancerActiveTag` shows it invokes that method and walks the response using protobuf field numbers 1 → 6 → 1:
- `GetBalancerInfoResponse.balancer` = field 1
- `BalancerMsg.principle_target` = field 6
- `PrincipleTargetInfo.tag` = field 1 (repeated string)

The response schema is corroborated by the XTLS/Xray-core `app/router/command/command.proto` definition: https://github.com/XTLS/Xray-core/blob/main/app/router/command/command.proto . `GetBalancerInfoRequest.tag` is field 1; response `balancer` is field 1; `BalancerMsg.override` is field 5 and `principle_target` field 6; `PrincipleTargetInfo.tag` is repeated field 1.

## Core change

Added `internal/xrayapi/balancer.go` using `dynamicpb` and a minimal descriptor for only the five required messages. This implements the exact method path and reads the principle-target tag list. The minimal schema avoids importing the full Xray module.

Added `internal/xrayapi/balancer_test.go` validating the protobuf request wire encoding and nested response decoding.

## Remaining work

- Reconstruct the remainder of original `balancerActiveTag` to understand how it turns the principle-target candidate list into its scalar result; do not equate the first strategy target with the currently active connection outbound.
- Resolve the previous active runtime outbound tag to its original URI before config replacement.
- Reproduce URI normalization (substring before `#`) and exact fallback priority: last alive matching node, minimum-delay alive matching node, first selected node.
- Integrate fallbackTag into compiler/refresh pipeline without cross-profile leakage and without changing any system routes.

## Validation and safety

`GOMAXPROCS=1 go test ./...`, `GOMAXPROCS=1 go vet ./...`, and `git diff --check` passed on VPS. No deployment, process restart, network change, commit, or push was performed.
