package parser

import (
	"encoding/json"
	"fmt"
	"net/url"
	"strconv"
	"strings"
)

func ParseShadowsocksLinks(source string, links []string) ([]ParsedServer, error) {
	if strings.TrimSpace(source) == "" {
		return nil, fmt.Errorf("subscription source is empty")
	}
	counts := map[string]int{}
	out := make([]ParsedServer, 0, len(links))
	for i, raw := range links {
		p, err := parseShadowsocks(raw)
		if err != nil {
			return nil, fmt.Errorf("link %d: %w", i+1, err)
		}
		nth := counts[p.name]
		counts[p.name] = nth + 1
		out = append(out, ParsedServer{Key: ServerKey{Source: source, Name: p.name, Nth: nth}, Name: p.name, Outbound: p.outbound})
	}
	return out, nil
}

func parseShadowsocks(raw string) (parsedVLESS, error) {
	raw = strings.TrimSpace(raw)
	if !strings.HasPrefix(strings.ToLower(raw), "ss://") {
		return parsedVLESS{}, fmt.Errorf("invalid Shadowsocks URI")
	}
	body := raw[len("ss://"):]
	if body == "" {
		return parsedVLESS{}, fmt.Errorf("Shadowsocks payload is empty")
	}
	u, err := url.Parse(raw)
	if err != nil {
		return parsedVLESS{}, fmt.Errorf("invalid Shadowsocks URI")
	}
	if u.Query().Get("plugin") != "" {
		return parsedVLESS{}, fmt.Errorf("Shadowsocks SIP003 plugins are not supported")
	}
	var method, password, host string
	var port int
	if u.User != nil && u.Hostname() != "" {
		user := u.User.Username()
		decoded, decErr := decodeVMessBase64(user)
		if decErr == nil && strings.Contains(string(decoded), ":") {
			method, password, _ = strings.Cut(string(decoded), ":")
		} else if pass, ok := u.User.Password(); ok {
			method, password = user, pass
		} else if strings.Contains(user, ":") {
			method, password, _ = strings.Cut(user, ":")
		} else {
			return parsedVLESS{}, fmt.Errorf("Shadowsocks userinfo is not valid method:password")
		}
		host = u.Hostname()
		port, err = strconv.Atoi(u.Port())
	} else {
		// Legacy form: the entire authority is Base64(method:password@host:port).
		encoded := strings.SplitN(body, "#", 2)[0]
		encoded = strings.SplitN(encoded, "?", 2)[0]
		decoded, decErr := decodeVMessBase64(encoded)
		if decErr != nil {
			return parsedVLESS{}, fmt.Errorf("Shadowsocks payload is not valid Base64")
		}
		credential, authority, ok := strings.Cut(string(decoded), "@")
		if !ok {
			return parsedVLESS{}, fmt.Errorf("Shadowsocks payload must contain method:password@host:port")
		}
		method, password, ok = strings.Cut(credential, ":")
		if !ok {
			return parsedVLESS{}, fmt.Errorf("Shadowsocks method or password is missing")
		}
		parsedAuthority, parseErr := url.Parse("ss://x@" + authority)
		if parseErr != nil {
			return parsedVLESS{}, fmt.Errorf("invalid Shadowsocks server authority")
		}
		host = parsedAuthority.Hostname()
		port, err = strconv.Atoi(parsedAuthority.Port())
	}
	if err != nil || port < 1 || port > 65535 {
		return parsedVLESS{}, fmt.Errorf("server port must be between 1 and 65535")
	}
	if !validServerHost(host) {
		return parsedVLESS{}, fmt.Errorf("server address is invalid")
	}
	method = strings.ToLower(strings.TrimSpace(method))
	switch method {
	case "aes-128-gcm", "aes-256-gcm", "chacha20-ietf-poly1305",
		"2022-blake3-aes-128-gcm", "2022-blake3-aes-256-gcm",
		"2022-blake3-chacha20-poly1305", "xchacha20-ietf-poly1305",
		"aes-128-ctr", "aes-192-ctr", "aes-256-ctr",
		"aes-128-cfb", "aes-192-cfb", "aes-256-cfb", "chacha20", "chacha20-ietf", "rc4-md5":
	default:
		return parsedVLESS{}, fmt.Errorf("unsupported Shadowsocks cipher %q", method)
	}
	if password == "" {
		return parsedVLESS{}, fmt.Errorf("Shadowsocks password is empty")
	}
	outbound := map[string]any{"protocol": "shadowsocks", "settings": map[string]any{"servers": []any{map[string]any{"address": host, "port": port, "method": method, "password": password}}}}
	encoded, err := jsonMarshalOutbound(outbound)
	if err != nil {
		return parsedVLESS{}, fmt.Errorf("cannot encode Shadowsocks outbound")
	}
	name := u.Fragment
	if name == "" {
		name = host + ":" + strconv.Itoa(port)
	}
	return parsedVLESS{name: name, outbound: encoded}, nil
}

func jsonMarshalOutbound(value any) ([]byte, error) {
	return json.Marshal(value)
}
