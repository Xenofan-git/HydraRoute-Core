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
var ruleIDPattern = regexp.MustCompile(`^[A-Za-z0-9_-]{1,48}$`)
var domainNamePattern = regexp.MustCompile(`^(?i:[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?)(?:\.(?i:[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?))*\.?$`)
var geoTagPattern = regexp.MustCompile(`^[A-Za-z0-9_.@!-]{1,96}$`)
var geoDataFilePattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,123}\.dat$`)
var allowedStrategies = map[string]bool{"random": true, "roundRobin": true, "leastPing": true}

// Compile builds one aggregate Xray configuration. Profile inbounds remain
// isolated; the optional shared inbound has its own ordered routing table.
// OutboundTag returns the deterministic Xray tag assigned to a profile node.
func OutboundTag(profileID, key string) string {
	h := sha256.Sum256([]byte(profileID + "\x00" + key))
	return "hr_" + profileID + "_" + hex.EncodeToString(h[:6])
}

func Compile(req CompileRequest) ([]byte, error) {
	if req.L7Sniffing.Enabled && !req.L7Sniffing.HTTP && !req.L7Sniffing.TLS && !req.L7Sniffing.QUIC {
		return nil, fmt.Errorf("L7 sniffing is enabled but no protocol is selected")
	}
	profiles := append([]Profile(nil), req.Profiles...)
	sort.Slice(profiles, func(i, j int) bool { return profiles[i].ID < profiles[j].ID })

	seenIDs := map[string]bool{}
	seenPorts := map[uint16]string{}
	enabledProfiles := map[string]bool{}
	inbounds := make([]any, 0)
	outbounds := []any{
		map[string]any{"tag": "direct", "protocol": "freedom"},
		map[string]any{"tag": "block", "protocol": "blackhole"},
	}
	rules := make([]any, 0)
	balancers := make([]any, 0)
	allOutboundTags := make([]string, 0)

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
			// Validate every parsed node, but emit only eligible pool members as
			// Xray outbounds and Observatory subjects.
			var outbound map[string]any
			if len(n.Outbound) == 0 || json.Unmarshal(n.Outbound, &outbound) != nil || outbound == nil {
				return nil, fmt.Errorf("profile %s node %q: outbound must be a JSON object", p.ID, n.Key)
			}
			if _, ok := outbound["protocol"]; !ok {
				return nil, fmt.Errorf("profile %s node %q: outbound protocol is missing", p.ID, n.Key)
			}
			applyDPIBypass(outbound, p.DPIBypass)
			if p.OutboundInterface != "" {
				if !regexp.MustCompile(`^[A-Za-z0-9_.:-]{1,64}$`).MatchString(p.OutboundInterface) {
					return nil, fmt.Errorf("profile %s: invalid outbound interface name", p.ID)
				}
				stream, _ := outbound["streamSettings"].(map[string]any)
				if stream == nil {
					stream = map[string]any{}
					outbound["streamSettings"] = stream
				}
				sockopt, _ := stream["sockopt"].(map[string]any)
				if sockopt == nil {
					sockopt = map[string]any{}
					stream["sockopt"] = sockopt
				}
				sockopt["interface"] = p.OutboundInterface
			}
			if excluded[n.Key] || ((p.ManualSelection || len(picked) > 0) && !picked[n.Key]) {
				continue
			}
			tag := OutboundTag(p.ID, n.Key)
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
		// Only eligible balancer members become Xray outbounds or Observatory
		// subjects. Excluded and unselected nodes must not be probed.
		outbounds = append(outbounds, eligible...)
		if len(eligible) == 0 {
			continue
		}
		enabledProfiles[p.ID] = true

		inTag := "hr_in_" + p.ID
		balTag := "hr_bal_" + p.ID
		profileInbound := map[string]any{
			"tag": inTag, "listen": listen, "port": p.Port, "protocol": "socks",
			"settings": map[string]any{"auth": "noauth", "udp": true},
		}
		applyL7Sniffing(profileInbound, req.L7Sniffing)
		inbounds = append(inbounds, profileInbound)
		rules = append(rules, map[string]any{
			"type": "field", "inboundTag": []string{inTag}, "balancerTag": balTag,
		})
		balancers = append(balancers, map[string]any{
			"tag": balTag, "selector": selectors, "strategy": map[string]any{"type": strategy},
		})
	}

	domainStrategy := "AsIs"
	if req.Routing != nil {
		routing := req.Routing
		if !routing.Enabled {
			// Keep dormant rules in persistent settings; disabled routing emits no shared inbound.
		} else {
			if len(enabledProfiles) == 0 {
				return nil, fmt.Errorf("shared SOCKS5 requires at least one enabled profile")
			}
			if routing.Port < 1024 {
				return nil, fmt.Errorf("shared SOCKS5 port %d is outside allowed range 1024-65535", routing.Port)
			}
			if other, ok := seenPorts[routing.Port]; ok {
				return nil, fmt.Errorf("shared SOCKS5 and profile %s use duplicate port %d", other, routing.Port)
			}
			listen := routing.Listen
			if listen == "" {
				listen = "127.0.0.1"
			}
			ip := net.ParseIP(listen)
			if ip == nil || !ip.IsLoopback() {
				return nil, fmt.Errorf("shared SOCKS5 listen address %q is not loopback", listen)
			}
			if err := validateRouteTarget(routing.DefaultAction, routing.DefaultProfileID, enabledProfiles); err != nil {
				return nil, fmt.Errorf("shared SOCKS5 default: %w", err)
			}
			if len(routing.Rules) > 256 {
				return nil, fmt.Errorf("shared routing exceeds 256 rules")
			}
			sharedTag := "hr_shared_in"
			sharedInbound := map[string]any{
				"tag": sharedTag, "listen": listen, "port": routing.Port, "protocol": "socks",
				"settings": map[string]any{"auth": "noauth", "udp": true},
			}
			applyL7Sniffing(sharedInbound, req.L7Sniffing)
			inbounds = append(inbounds, sharedInbound)
			seenRuleIDs := map[string]bool{}
			for i, rule := range routing.Rules {
				if !rule.Enabled {
					continue
				}
				if !ruleIDPattern.MatchString(rule.ID) || seenRuleIDs[rule.ID] {
					return nil, fmt.Errorf("shared routing rule %d: invalid or duplicate rule id %q", i+1, rule.ID)
				}
				seenRuleIDs[rule.ID] = true
				if strings.TrimSpace(rule.Name) == "" || len(rule.Name) > 96 {
					return nil, fmt.Errorf("shared routing rule %s: name must contain 1-96 characters", rule.ID)
				}
				if len(rule.Values) == 0 || len(rule.Values) > 512 {
					return nil, fmt.Errorf("shared routing rule %s: provide 1-512 match values", rule.ID)
				}
				field := ""
				switch rule.MatchType {
				case "domain":
					field = "domain"
				case "ip":
					field = "ip"
					domainStrategy = "IPIfNonMatch"
				default:
					return nil, fmt.Errorf("shared routing rule %s: match_type must be domain or ip", rule.ID)
				}
				values := make([]string, 0, len(rule.Values))
				seenValues := map[string]bool{}
				for _, value := range rule.Values {
					value = strings.TrimSpace(value)
					if err := validateMatchValue(rule.MatchType, value); err != nil {
						return nil, fmt.Errorf("shared routing rule %s: %w", rule.ID, err)
					}
					if seenValues[value] {
						return nil, fmt.Errorf("shared routing rule %s: duplicate match value %q", rule.ID, value)
					}
					seenValues[value] = true
					values = append(values, value)
				}
				entry := map[string]any{
					"type": "field", "inboundTag": []string{sharedTag}, field: values,
					"ruleTag": rule.Name,
				}
				if err := applyRouteTarget(entry, rule.Action, rule.ProfileID, enabledProfiles); err != nil {
					return nil, fmt.Errorf("shared routing rule %s: %w", rule.ID, err)
				}
				rules = append(rules, entry)
			}
			// Explicit catch-all prevents Xray's implicit first-outbound fallback
			// from sending unmatched shared traffic through the direct outbound.
			fallback := map[string]any{"type": "field", "inboundTag": []string{sharedTag}, "ruleTag": "shared-default"}
			if err := applyRouteTarget(fallback, routing.DefaultAction, routing.DefaultProfileID, enabledProfiles); err != nil {
				return nil, fmt.Errorf("shared SOCKS5 default: %w", err)
			}
			rules = append(rules, fallback)
		}
	}

	cfg := map[string]any{
		"log":       map[string]any{"loglevel": "warning"},
		"inbounds":  inbounds,
		"outbounds": outbounds,
		"routing":   map[string]any{"domainStrategy": domainStrategy, "rules": rules, "balancers": balancers},
	}
	if req.DNS != nil && req.DNS.Enabled {
		listen := req.DNS.Listen
		if listen == "" {
			listen = "127.0.0.1"
		}
		ip := net.ParseIP(listen)
		if ip == nil || !ip.IsLoopback() {
			return nil, fmt.Errorf("Core DNS listen address %q is not loopback", listen)
		}
		if req.DNS.Port < 1024 {
			return nil, fmt.Errorf("Core DNS port %d is outside allowed range 1024-65535", req.DNS.Port)
		}
		cfg["dns"] = map[string]any{
			"servers": []any{map[string]any{"address": listen, "port": req.DNS.Port}},
		}
	}
	if len(allOutboundTags) > 0 {
		// Private loopback-only gRPC endpoint for the managed process' Observatory.
		// It is not exposed on LAN and does not install routes or alter the host firewall.
		const apiPort = 16285
		if owner, exists := seenPorts[apiPort]; exists {
			return nil, fmt.Errorf("profile %s uses reserved Xray API port %d", owner, apiPort)
		}
		inbounds = append(inbounds, map[string]any{
			"tag": "hr_api_in", "listen": "127.0.0.1", "port": apiPort, "protocol": "dokodemo-door",
			"settings": map[string]any{"address": "127.0.0.1"},
		})
		outbounds = append(outbounds, map[string]any{"tag": "hr_api", "protocol": "freedom"})
		rules = append([]any{map[string]any{"type": "field", "inboundTag": []string{"hr_api_in"}, "outboundTag": "hr_api"}}, rules...)
		cfg["inbounds"] = inbounds
		cfg["outbounds"] = outbounds
		cfg["routing"] = map[string]any{"domainStrategy": domainStrategy, "rules": rules, "balancers": balancers}
		cfg["api"] = map[string]any{"tag": "hr_api", "services": []string{"ObservatoryService", "RoutingService"}}
		cfg["observatory"] = map[string]any{
			"subjectSelector": allOutboundTags,
			"probeUrl":        "https://www.google.com/generate_204",
			"probeInterval":   observatoryProbeInterval(req.ObservatoryProbeIntervalSeconds, len(allOutboundTags)),
		}
	}
	return json.MarshalIndent(cfg, "", "  ")
}

func applyL7Sniffing(inbound map[string]any, settings L7SniffingConfig) {
	if !settings.Enabled {
		return
	}
	destOverride := make([]string, 0, 3)
	if settings.HTTP {
		destOverride = append(destOverride, "http")
	}
	if settings.TLS {
		destOverride = append(destOverride, "tls")
	}
	if settings.QUIC {
		destOverride = append(destOverride, "quic")
	}
	if len(destOverride) == 0 {
		return
	}
	inbound["sniffing"] = map[string]any{
		"enabled": true, "destOverride": destOverride,
		"metadataOnly": false, "routeOnly": true,
	}
}

func ObservatoryProbeIntervalMS(seconds, subjects int) int {
	if seconds < 5 || seconds > 3600 {
		seconds = 30
	}
	if subjects < 1 {
		subjects = 1
	}
	intervalMS := (seconds * 1000) / subjects
	if intervalMS < 100 {
		intervalMS = 100
	}
	return intervalMS
}

func observatoryProbeInterval(seconds, subjects int) string {
	return fmt.Sprintf("%dms", ObservatoryProbeIntervalMS(seconds, subjects))
}

func validateRouteTarget(action, profileID string, enabled map[string]bool) error {
	entry := map[string]any{}
	return applyRouteTarget(entry, action, profileID, enabled)
}

func applyRouteTarget(entry map[string]any, action, profileID string, enabled map[string]bool) error {
	switch action {
	case "profile":
		if !profileIDPattern.MatchString(profileID) || !enabled[profileID] {
			return fmt.Errorf("target profile %q is not enabled", profileID)
		}
		entry["balancerTag"] = "hr_bal_" + profileID
	case "direct":
		if profileID != "" {
			return fmt.Errorf("direct action must not specify a profile")
		}
		entry["outboundTag"] = "direct"
	case "block":
		if profileID != "" {
			return fmt.Errorf("block action must not specify a profile")
		}
		entry["outboundTag"] = "block"
	default:
		return fmt.Errorf("action must be profile, direct, or block")
	}
	return nil
}

func validateMatchValue(matchType, value string) error {
	if value == "" || len(value) > 512 || strings.ContainsAny(value, " \t\r\n\x00") {
		return fmt.Errorf("invalid empty, oversized, or whitespace-containing match value")
	}
	if matchType == "domain" {
		switch {
		case strings.HasPrefix(value, "domain:") || strings.HasPrefix(value, "full:"):
			if !domainNamePattern.MatchString(strings.TrimPrefix(strings.TrimPrefix(value, "domain:"), "full:")) {
				return fmt.Errorf("invalid domain value %q", value)
			}
		case strings.HasPrefix(value, "keyword:"):
			if strings.TrimPrefix(value, "keyword:") == "" {
				return fmt.Errorf("empty keyword match")
			}
		case strings.HasPrefix(value, "regexp:"):
			if _, err := regexp.Compile(strings.TrimPrefix(value, "regexp:")); err != nil {
				return fmt.Errorf("invalid domain regexp: %w", err)
			}
		case strings.HasPrefix(value, "geosite:"):
			if !geoTagPattern.MatchString(strings.TrimPrefix(value, "geosite:")) {
				return fmt.Errorf("invalid geosite tag %q", value)
			}
		case strings.HasPrefix(value, "ext:"):
			parts := strings.Split(value, ":")
			if len(parts) != 3 || !geoDataFilePattern.MatchString(parts[1]) || !geoTagPattern.MatchString(parts[2]) {
				return fmt.Errorf("invalid external geosite reference %q: expected ext:<basename>.dat:<tag>", value)
			}
		default:
			return fmt.Errorf("domain values must use domain:, full:, keyword:, regexp:, geosite:, or ext: syntax")
		}
		return nil
	}
	if strings.HasPrefix(value, "geoip:") {
		if !geoTagPattern.MatchString(strings.TrimPrefix(value, "geoip:")) {
			return fmt.Errorf("invalid geoip tag %q", value)
		}
		return nil
	}
	if net.ParseIP(value) != nil {
		return nil
	}
	if _, _, err := net.ParseCIDR(value); err != nil {
		return fmt.Errorf("invalid IP or CIDR %q", value)
	}
	return nil
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

// applyDPIBypass decorates each eligible proxy's first-hop transport. It only
// touches the generated Xray object; it never changes host routes or firewall.
func applyDPIBypass(outbound map[string]any, options DPIBypass) {
	if !options.NoiseTraffic && !options.TLSFragmentation && !options.RandomTLSFingerprint {
		return
	}
	stream, ok := outbound["streamSettings"].(map[string]any)
	if !ok {
		stream = map[string]any{}
		outbound["streamSettings"] = stream
	}
	if options.RandomTLSFingerprint {
		if tls, ok := stream["tlsSettings"].(map[string]any); ok {
			tls["fingerprint"] = "random"
		}
		if reality, ok := stream["realitySettings"].(map[string]any); ok {
			reality["fingerprint"] = "random"
		}
	}
	if options.TLSFragmentation || options.NoiseTraffic {
		fm, ok := stream["finalmask"].(map[string]any)
		if !ok {
			fm = map[string]any{}
			stream["finalmask"] = fm
		}
		if options.TLSFragmentation {
			tcp, _ := fm["tcp"].([]any)
			tcp = append(tcp, map[string]any{"type": "fragment", "settings": map[string]any{"packets": "tlshello", "lengths": []string{"10-20"}, "delays": []string{"1-2"}, "maxSplit": "3-6"}})
			fm["tcp"] = tcp
		}
		if options.NoiseTraffic {
			udp, _ := fm["udp"].([]any)
			udp = append(udp, map[string]any{"type": "noise", "settings": map[string]any{"reset": "30-60", "noise": []any{map[string]any{"rand": "50-100", "randRange": "0-255", "delay": "10-20"}}}})
			fm["udp"] = udp
		}
	}
}