# HRweb 1.68.0 — exact Mux defaults from ARM64 disassembly

Date: 2026-11-10  
Reference binary: original HRweb 1.68.0-1 ARM64 executable, BuildID `7b3e84a0b071cc221dfb76df58d42fd03f080d70`.  
Method: static disassembly and read-only inspection of the original binary; no HRweb process was started and no live router setting was changed.

## Result

The function `main.applyMuxToConfig` at virtual address `0x38ac40` constructs the Mux object. The three values are recovered from the map assignments in the function:

| JSON key | Value | Evidence |
|---|---:|---|
| `concurrency` | `8` | key literal at VA `0x4e834c`; integer value at VA `0x753a90` is 8 |
| `xudpConcurrency` | `1` | key literal at VA `0x4eabb7`; integer value at VA `0x7530d8` is 1 |
| `xudpProxyUDP443` | `"reject"` | key literal at VA `0x4eabc6`; string header at VA `0x754270` points to six-byte literal `reject` |

The map assignment sequence and value descriptors were checked against the original ARM64 disassembly. The integer descriptor at `0x43bb80` has size 8; the string descriptor at `0x43b940` has size 16. The string header resolves to VA `0x4e566d`, length 6, bytes `reject`.

## Core correction

The Core Mux implementation previously used `xudpConcurrency: 8`, which did not match HRweb. The PR #6 branch now uses:

```json
{
  "enabled": true,
  "concurrency": 8,
  "xudpConcurrency": 1,
  "xudpProxyUDP443": "reject"
}
```

Updated:
- `internal/compiler/mux.go`
- `internal/compiler/mux_test.go`

The regression test now requires the original `xudpConcurrency=1` value. The change is on `integration/hrneo-mux-parity-20261110`; CI must pass before this correction is considered validated.

## Scope and limitations

- This is static evidence from the original HRweb binary, not a live-router configuration dump.
- The per-profile Mux toggle and XTLS RPRX compatibility gate remain separate concerns; this report only closes the three Mux object defaults.
- No deployment, service restart, Keenetic/Entware configuration change, Xray runtime change, DNS, route, firewall, Tailscale or conntrack change was made.
