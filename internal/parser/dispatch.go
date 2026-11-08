package parser

import (
	"fmt"
	"strings"
)

// ParseLinks dispatches mixed share links and assigns source-wide duplicate
// ordinals, so names remain unique even when protocols are interleaved.
func ParseLinks(source string, links []string) ([]ParsedServer, error) {
	if strings.TrimSpace(source) == "" {
		return nil, fmt.Errorf("subscription source is empty")
	}
	counts := map[string]int{}
	out := make([]ParsedServer, 0, len(links))
	for i, raw := range links {
		var parsed []ParsedServer
		var err error
		trimmed := strings.TrimSpace(raw)
		switch {
		case strings.HasPrefix(strings.ToLower(trimmed), "vless://"):
			parsed, err = ParseVLESSLinks(source, []string{raw})
		case strings.HasPrefix(strings.ToLower(trimmed), "vmess://"):
			parsed, err = ParseVMessLinks(source, []string{raw})
		case strings.HasPrefix(strings.ToLower(trimmed), "trojan://"):
			parsed, err = ParseTrojanLinks(source, []string{raw})
		case strings.HasPrefix(strings.ToLower(trimmed), "ss://"):
			parsed, err = ParseShadowsocksLinks(source, []string{raw})
		case strings.HasPrefix(strings.ToLower(trimmed), "socks://"), strings.HasPrefix(strings.ToLower(trimmed), "socks5://"), strings.HasPrefix(strings.ToLower(trimmed), "socks5h://"), strings.HasPrefix(strings.ToLower(trimmed), "http://"), strings.HasPrefix(strings.ToLower(trimmed), "https://"):
			parsed, err = ParseProxyLinks(source, []string{raw})
		default:
			return nil, fmt.Errorf("link %d: unsupported share-link protocol", i+1)
		}
		if err != nil {
			return nil, fmt.Errorf("link %d: %w", i+1, err)
		}
		server := parsed[0]
		server.Key.Nth = counts[server.Name]
		counts[server.Name]++
		out = append(out, server)
	}
	return out, nil
}
