package parser

import (
	"encoding/json"
	"fmt"
	"net"
	"net/url"
	"strconv"
	"strings"
)

// ParseHysteria2Links converts Hysteria2 share links into Xray 26.1.18+
// hysteria outbound configurations, retaining the source-wide identity.
func ParseHysteria2Links(source string, links []string) ([]ParsedServer, error) {
	if strings.TrimSpace(source) == "" {
		return nil, fmt.Errorf("subscription source is empty")
	}
	counts := map[string]int{}
	out := make([]ParsedServer, 0, len(links))
	for i, raw := range links {
		p, err := parseHysteria2(raw)
		if err != nil {
			return nil, fmt.Errorf("link %d: %w", i+1, err)
		}
		nth := counts[p.name]
		counts[p.name]++
		out = append(out, ParsedServer{Key: ServerKey{Source: source, Name: p.name, Nth: nth}, Name: p.name, Outbound: p.outbound, ReferenceURI: p.uri})
	}
	return out, nil
}

func parseHysteria2(raw string) (parsedVLESS, error) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || (u.Scheme != "hysteria2" && u.Scheme != "hy2") {
		return parsedVLESS{}, fmt.Errorf("invalid Hysteria2 URI")
	}
	if u.User == nil || u.User.Username() == "" {
		return parsedVLESS{}, fmt.Errorf("Hysteria2 auth is missing")
	}
	auth := u.User.Username()
	if pass, ok := u.User.Password(); ok {
		auth += ":" + pass
	}
	host := u.Hostname()
	if !validServerHost(host) {
		return parsedVLESS{}, fmt.Errorf("invalid Hysteria2 host")
	}
	port, err := strconv.Atoi(u.Port())
	if err != nil || port < 1 || port > 65535 {
		return parsedVLESS{}, fmt.Errorf("invalid Hysteria2 port")
	}
	q, err := url.ParseQuery(u.RawQuery)
	if err != nil {
		return parsedVLESS{}, fmt.Errorf("invalid Hysteria2 query")
	}
	for key := range q {
		switch strings.ToLower(key) {
		case "sni", "peer", "alpn", "insecure", "allowinsecure":
		default:
			return parsedVLESS{}, fmt.Errorf("unsupported Hysteria2 option %q", key)
		}
	}
	sni := q.Get("sni")
	if sni == "" {
		sni = q.Get("peer")
	}
	if sni == "" {
		sni = host
	}
	tls := map[string]any{"serverName": sni}
	if alpn := splitCSV(q["alpn"]); len(alpn) > 0 {
		tls["alpn"] = alpn
	}
	insecure := q.Get("insecure")
	if insecure == "" {
		insecure = q.Get("allowInsecure")
	}
	if insecure != "" {
		b, e := strconv.ParseBool(insecure)
		if e != nil {
			return parsedVLESS{}, fmt.Errorf("Hysteria2 insecure must be boolean")
		}
		// Xray 26.10+ removed allowInsecure; always retain TLS verification.
		_ = b
	}
	outbound := map[string]any{"protocol": "hysteria", "settings": map[string]any{"address": host, "port": port, "version": 2}, "streamSettings": map[string]any{"network": "hysteria", "security": "tls", "tlsSettings": tls, "hysteriaSettings": map[string]any{"version": 2, "auth": auth}}}
	data, err := json.Marshal(outbound)
	if err != nil {
		return parsedVLESS{}, fmt.Errorf("cannot encode Hysteria2 outbound")
	}
	name := u.Fragment
	if name == "" {
		name = net.JoinHostPort(host, strconv.Itoa(port))
	}
	return parsedVLESS{name: name, outbound: data, uri: referenceURI(raw)}, nil
}
