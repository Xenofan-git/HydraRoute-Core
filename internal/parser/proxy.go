package parser

import (
	"encoding/json"
	"fmt"
	"net/url"
	"strconv"
	"strings"
)

func ParseProxyLinks(source string, links []string) ([]ParsedServer, error) {
	if strings.TrimSpace(source) == "" {
		return nil, fmt.Errorf("subscription source is empty")
	}
	counts := map[string]int{}
	out := make([]ParsedServer, 0, len(links))
	for i, raw := range links {
		p, err := parseProxyURI(raw)
		if err != nil {
			return nil, fmt.Errorf("link %d: %w", i+1, err)
		}
		nth := counts[p.name]
		counts[p.name] = nth + 1
		out = append(out, ParsedServer{Key: ServerKey{Source: source, Name: p.name, Nth: nth}, Name: p.name, Outbound: p.outbound, ReferenceURI: p.uri})
	}
	return out, nil
}

func parseProxyURI(raw string) (parsedVLESS, error) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return parsedVLESS{}, fmt.Errorf("invalid proxy URI")
	}
	scheme := strings.ToLower(u.Scheme)
	var protocol string
	switch scheme {
	case "socks", "socks5", "socks5h":
		protocol = "socks"
	case "http", "https":
		protocol = "http"
	default:
		return parsedVLESS{}, fmt.Errorf("unsupported proxy URI scheme %q", scheme)
	}
	host := u.Hostname()
	if !validServerHost(host) {
		return parsedVLESS{}, fmt.Errorf("server address is invalid")
	}
	port, err := strconv.Atoi(u.Port())
	if err != nil || port < 1 || port > 65535 {
		return parsedVLESS{}, fmt.Errorf("server port must be between 1 and 65535")
	}
	if u.Path != "" && u.Path != "/" {
		return parsedVLESS{}, fmt.Errorf("proxy URI path is not supported")
	}
	q, queryErr := url.ParseQuery(u.RawQuery)
	if queryErr != nil {
		return parsedVLESS{}, fmt.Errorf("query parameters are malformed")
	}
	for k := range q {
		allowed := protocol == "socks" && k == "udp" || scheme == "https" && (k == "sni" || k == "fp" || k == "allowInsecure")
		if !allowed {
			return parsedVLESS{}, fmt.Errorf("unsupported proxy URI parameter %q", k)
		}
	}
	server := map[string]any{"address": host, "port": port}
	if u.User != nil {
		username := u.User.Username()
		password, _ := u.User.Password()
		if username == "" {
			return parsedVLESS{}, fmt.Errorf("proxy username is empty")
		}
		server["users"] = []any{map[string]any{"user": username, "pass": password}}
	}
	outbound := map[string]any{"protocol": protocol, "settings": map[string]any{"servers": []any{server}}}
	if protocol == "socks" {
		server["version"] = "5"
		if v := q.Get("udp"); v != "" {
			b, e := strconv.ParseBool(v)
			if e != nil {
				return parsedVLESS{}, fmt.Errorf("udp must be a boolean")
			}
			server["udp"] = b
		}
	} else if scheme == "https" {
		sni := q.Get("sni")
		if sni == "" {
			sni = host
		}
		tls := map[string]any{"serverName": sni}
		if fp := q.Get("fp"); fp != "" {
			tls["fingerprint"] = fp
		}
		if v := q.Get("allowInsecure"); v != "" {
			b, e := strconv.ParseBool(v)
			if e != nil {
				return parsedVLESS{}, fmt.Errorf("allowInsecure must be a boolean")
			}
			// Removed in Xray 26.10+: preserve TLS certificate verification.
			_ = b
		}
		outbound["streamSettings"] = map[string]any{"security": "tls", "tlsSettings": tls}
	}
	encoded, err := json.Marshal(outbound)
	if err != nil {
		return parsedVLESS{}, fmt.Errorf("cannot encode proxy outbound")
	}
	name := u.Fragment
	if name == "" {
		name = host + ":" + strconv.Itoa(port)
	}
	return parsedVLESS{name: name, outbound: encoded, uri: referenceURI(raw)}, nil
}
