package compiler

import (
	"encoding/json"
	"strings"
)

// profileSupportsMux mirrors HRweb's compatibility gate: the selected pool
// must be non-empty, and no selected link may use the XTLS RPRX flow.
func profileSupportsMux(p Profile) bool {
	excluded := stringSet(p.Excluded)
	picked := stringSet(p.Picked)
	selected := 0
	for _, node := range p.Nodes {
		if excluded[node.Key] || ((p.ManualSelection || len(picked) > 0) && !picked[node.Key]) {
			continue
		}
		selected++
		if !SupportsMuxOutbound(node.Outbound) {
			return false
		}
	}
	return selected > 0
}

func SupportsMuxOutbound(raw json.RawMessage) bool {
	var value any
	if len(raw) == 0 || json.Unmarshal(raw, &value) != nil {
		return false
	}
	return !hasXTLSRPRXFlowValue(value)
}

func hasXTLSRPRXFlowValue(value any) bool {
	var walk func(any) bool
	walk = func(v any) bool {
		switch x := v.(type) {
		case map[string]any:
			for key, child := range x {
				if strings.EqualFold(key, "flow") {
					if flow, ok := child.(string); ok && strings.Contains(strings.ToLower(flow), "xtls-rprx") {
						return true
					}
				}
				if walk(child) {
					return true
				}
			}
		case []any:
			for _, child := range x {
				if walk(child) {
					return true
				}
			}
		}
		return false
	}
	return walk(value)
}

// HRweb uses concurrency=8 and xudpConcurrency=8. Its stripped binary carries
// an opaque static value for xudpProxyUDP443; use Xray's safe reject default
// until a live original config confirms a different value.
func applyMux(outbound map[string]any) {
	outbound["mux"] = map[string]any{
		"enabled":          true,
		"concurrency":      8,
		"xudpConcurrency":  8,
		"xudpProxyUDP443":  "reject",
	}
}
