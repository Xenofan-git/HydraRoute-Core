package compiler

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"regexp"
	"sort"
	"strings"
)

var profileIDPattern = regexp.MustCompile(`^[A-Za-z0-9_-]{1,32}$`)
var allowedStrategies = map[string]bool{"random": true, "roundRobin": true, "leastPing": true}

// Compile builds one aggregate Xray configuration with strictly isolated
// per-profile pools, inbounds and balancers. It performs no I/O or host changes.
func Compile(req CompileRequest) ([]byte, error) {
	cfg := map[string]any{
		"log":      map[string]any{"loglevel": "warning"},
		"inbounds": []any{},
		"outbounds": []any{
			map[string]any{"tag": "direct", "protocol": "freedom"},
			map[string]any{"tag": "block", "protocol": "blackhole"},
		},
		"routing": map[string]any{"domainStrategy": "AsIs", "rules": []any{}, "balancers": []any{}},
	}

	profiles := append([]Profile(nil), req.Profiles...)
	sort.Slice(profiles, func(i, j int) bool { return profiles[i].ID < profiles[j].ID })
	seenIDs := map[string]bool{}
	seenPorts := map[uint16]string{}
	allOutboundTags := make([]string, 0)
	inbounds := make([]any, 0)
	outbounds := []any{
		map[string]any{"tag": "direct", "protocol": "freedom"},
		map[string]any{"tag": "block", "protocol": "blackhole"},
	}
	rules := make([]any, 0)
	balancers := make([]any, 0)

	for _, p := range profiles {
		if !p.Enabled {
			continue
		}
		if !profileIDPattern.MatchString(p.ID) {
			return nil, fmt.Errorf("profile id %q must match [A-Za-z0-9_-]{1,32}", p.ID)
		}
		if seenIDs[p.ID] {
			return nil, fmt.Errorf("duplicate profile id %q", p.ID)
		}
		seenIDs[p.ID] = true
		if p.Port < 1024 {
			return nil, fmt.Errorf("profile %s: port %d is outside allowed range 1024-65535", p.ID, p.Port)
		}
		if other, ok := seenPorts[p.Port]; ok {
			return nil, fmt.Errorf("profiles %s and %s use duplicate port %d", other, p.ID, p.Port)
		}
		seenPorts[p.Port] = p.ID
		listen := p.Listen
		if listen == "" {
			listen = "127.0.0.1"
		}
		ip := net.ParseIP(listen)
		if ip == nil || !ip.IsLoopback() {
			return nil, fmt.Errorf("profile %s: SOCKS listen address %q is not loopback", p.ID, listen)
		}
		strategy := p.Strategy
		if strategy == "" {
			strategy = "leastPing"
		}
		if !allowedStrategies[strategy] {
			return nil, fmt.Errorf("profile %s: unsupported balancer strategy %q", p.ID, strategy)
		}

		excluded := stringSet(p.Excluded)
		picked := stringSet(p.Picked)
		nodeKeys := map[string]bool{}
		eligible := make([]any, 0)
		selectors := make([]string, 0)
		for _, n := range p.Nodes {
			if strings.TrimSpace(n.Key) == "" {
				return nil, fmt.Errorf("profile %s: node has empty stable key", p.ID)
			}
			if nodeKeys[n.Key] {
				return nil, fmt.Errorf("profile %s: duplicate node key %q", p.ID, n.Key)
			}
			nodeKeys[n.Key] = true
			if excluded[n.Key] || (len(picked) > 0 && !picked[n.Key]) {
				continue
			}
			var outbound map[string]any
			if len(n.Outbound) == 0 || json.Unmarshal(n.Outbound, &outbound) != nil || outbound == nil {
				return nil, fmt.Errorf("profile %s node %q: outbound must be a JSON object", p.ID, n.Key)
			}
			if _, ok := outbound["protocol"]; !ok {
				return nil, fmt.Errorf("profile %s node %q: outbound protocol is missing", p.ID, n.Key)
			}
			h := sha256.Sum256([]byte(p.ID + "\x00" + n.Key))
			tag := "hr_" + p.ID + "_" + hex.EncodeToString(h[:6])
			outbound["tag"] = tag
			eligible = append(eligible, outbound)
			selectors = append(selectors, tag)
			allOutboundTags = append(allOutboundTags, tag)
		}
		for key := range picked {
			if !nodeKeys[key] {
				return nil, fmt.Errorf("profile %s: picked node %q does not exist", p.ID, key)
			}
			if excluded[key] {
				return nil, fmt.Errorf("profile %s: node %q cannot be both picked and excluded", p.ID, key)
			}
		}
		if len(eligible) == 0 {
			return nil, fmt.Errorf("profile %s: no eligible nodes after picked/excluded filtering", p.ID)
		}

		inTag := "hr_in_" + p.ID
		balTag := "hr_bal_" + p.ID
		inbounds = append(inbounds, map[string]any{
			"tag": inTag, "listen": listen, "port": p.Port, "protocol": "socks",
			"settings": map[string]any{"auth": "noauth", "udp": false},
		})
		outbounds = append(outbounds, eligible...)
		rules = append(rules, map[string]any{
			"type": "field", "inboundTag": []string{inTag}, "balancerTag": balTag,
		})
		balancers = append(balancers, map[string]any{
			"tag": balTag, "selector": selectors, "strategy": map[string]any{"type": strategy},
		})
	}

	if len(inbounds) > 0 {
		cfg["inbounds"] = inbounds
		cfg["outbounds"] = outbounds
		cfg["routing"] = map[string]any{
			"domainStrategy": "AsIs", "rules": rules, "balancers": balancers,
		}
		cfg["observatory"] = map[string]any{
			"subjectSelector": allOutboundTags,
			"probeUrl":        "https://www.gstatic.com/generate_204",
			"probeInterval":   "180s",
		}
	} else {
		cfg["outbounds"] = outbounds
	}
	return json.MarshalIndent(cfg, "", "  ")
}

func stringSet(items []string) map[string]bool {
	out := make(map[string]bool, len(items))
	for _, item := range items {
		if strings.TrimSpace(item) != "" {
			out[item] = true
		}
	}
	return out
}

// ValidateJSON checks that the compiler output remains valid JSON.
func ValidateJSON(data []byte) error {
	var v any
	if err := json.Unmarshal(data, &v); err != nil {
		return err
	}
	if v == nil {
		return errors.New("empty JSON document")
	}
	return nil
}
