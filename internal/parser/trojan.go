package parser

import (
	"encoding/json"
	"fmt"
	"net/url"
	"strconv"
	"strings"
)

func ParseTrojanLinks(source string, links []string) ([]ParsedServer, error) {
	if strings.TrimSpace(source) == "" {
		return nil, fmt.Errorf("subscription source is empty")
	}
	counts := map[string]int{}
	out := make([]ParsedServer, 0, len(links))
	for i, raw := range links {
		p, err := parseTrojan(raw)
		if err != nil {
			return nil, fmt.Errorf("link %d: %w", i+1, err)
		}
		nth := counts[p.name]
		counts[p.name] = nth + 1
		out = append(out, ParsedServer{Key: ServerKey{Source: source, Name: p.name, Nth: nth}, Name: p.name, Outbound: p.outbound, ReferenceURI: p.uri})
	}
	return out, nil
}

func parseTrojan(raw string) (parsedVLESS, error) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || !strings.EqualFold(u.Scheme, "trojan") {
		return parsedVLESS{}, fmt.Errorf("invalid Trojan URI")
	}
	if u.User == nil {
		return parsedVLESS{}, fmt.Errorf("Trojan password is missing")
	}
	password, ok := u.User.Password()
	if !ok {
		password = u.User.Username()
	}
	if password == "" {
		return parsedVLESS{}, fmt.Errorf("Trojan password is empty")
	}
	host := u.Hostname()
	if !validServerHost(host) {
		return parsedVLESS{}, fmt.Errorf("server address is invalid")
	}
	port, err := strconv.Atoi(u.Port())
	if err != nil || port < 1 || port > 65535 {
		return parsedVLESS{}, fmt.Errorf("server port must be between 1 and 65535")
	}
	q, err := url.ParseQuery(u.RawQuery)
	if err != nil {
		return parsedVLESS{}, fmt.Errorf("query parameters are malformed")
	}
	get := func(k string) string { return q.Get(k) }
	network := strings.ToLower(get("type"))
	if network == "" {
		network = "tcp"
	}
	stream := map[string]any{"network": network}
	path, hostHeader := get("path"), get("host")
	switch network {
	case "tcp":
	case "ws":
		ws := map[string]any{}
		if path != "" {
			ws["path"] = path
		}
		if hostHeader != "" {
			ws["host"] = hostHeader
		}
		stream["wsSettings"] = ws
	case "grpc":
		grpc := map[string]any{}
		service := get("serviceName")
		if service == "" {
			service = strings.TrimPrefix(path, "/")
		}
		if service != "" {
			grpc["serviceName"] = service
		}
		if authority := get("authority"); authority != "" {
			grpc["authority"] = authority
		} else if hostHeader != "" {
			grpc["authority"] = hostHeader
		}
		if get("mode") == "multi" {
			grpc["multiMode"] = true
		}
		stream["grpcSettings"] = grpc
	case "xhttp":
		x := map[string]any{}
		if path != "" {
			x["path"] = path
		}
		if hostHeader != "" {
			x["host"] = hostHeader
		}
		if mode := get("mode"); mode != "" {
			x["mode"] = mode
		}
		if extra := get("extra"); extra != "" {
			if !json.Valid([]byte(extra)) {
				return parsedVLESS{}, fmt.Errorf("XHTTP extra parameter is not valid JSON")
			}
			var v any
			if err := json.Unmarshal([]byte(extra), &v); err != nil {
				return parsedVLESS{}, fmt.Errorf("XHTTP extra parameter is invalid")
			}
			x["extra"] = v
		}
		stream["xhttpSettings"] = x
	case "httpupgrade":
		x := map[string]any{}
		if path != "" {
			x["path"] = path
		}
		if hostHeader != "" {
			x["host"] = hostHeader
		}
		stream["httpupgradeSettings"] = x
	default:
		return parsedVLESS{}, fmt.Errorf("unsupported Trojan transport %q", network)
	}
	security := strings.ToLower(get("security"))
	if security == "" {
		security = "tls"
	}
	switch security {
	case "none":
		stream["security"] = "none"
	case "tls":
		stream["security"] = "tls"
		sni := get("sni")
		if sni == "" {
			sni = get("peer")
		}
		if sni == "" {
			sni = host
		}
		tls := map[string]any{"serverName": sni}
		if fp := get("fp"); fp != "" {
			tls["fingerprint"] = fp
		}
		if alpn := splitCSV(q["alpn"]); len(alpn) > 0 {
			tls["alpn"] = alpn
		}
		if v := get("allowInsecure"); v != "" {
			b, e := strconv.ParseBool(v)
			if e != nil {
				return parsedVLESS{}, fmt.Errorf("allowInsecure must be a boolean")
			}
			tls["allowInsecure"] = b
		}
		stream["tlsSettings"] = tls
	default:
		return parsedVLESS{}, fmt.Errorf("unsupported Trojan security %q", security)
	}
	outbound := map[string]any{"protocol": "trojan", "settings": map[string]any{"servers": []any{map[string]any{"address": host, "port": port, "password": password}}}, "streamSettings": stream}
	encoded, err := json.Marshal(outbound)
	if err != nil {
		return parsedVLESS{}, fmt.Errorf("cannot encode Trojan outbound")
	}
	name := u.Fragment
	if name == "" {
		name = host + ":" + strconv.Itoa(port)
	}
	return parsedVLESS{name: name, outbound: encoded, uri: referenceURI(raw)}, nil
}
