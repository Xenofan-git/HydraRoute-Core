package parser

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net"
	"net/url"
	"strconv"
	"strings"
)

// ServerKey preserves the recovered HRweb identity tuple. Nth is zero-based
// and omitted for the first node with a given name in one source.
type ServerKey struct {
	Source string `json:"source"`
	Name   string `json:"name"`
	Nth    int    `json:"nth,omitempty"`
}

// ParsedServer separates stable UI identity from the Xray outbound payload.
type ParsedServer struct {
	Key      ServerKey       `json:"key"`
	Name     string          `json:"name"`
	Outbound json.RawMessage `json:"outbound"`
}

// CompilerKey encodes the public ServerKey tuple without exposing credentials.
func (s ParsedServer) CompilerKey() string {
	b, _ := json.Marshal(s.Key)
	return string(b)
}

// CompilerOutbound returns a detached copy suitable for compiler.Node.
func (s ParsedServer) CompilerOutbound() json.RawMessage {
	return append(json.RawMessage(nil), s.Outbound...)
}

// ParseVLESSLinks parses a list of vless:// share links belonging to one
// subscription source. Duplicate names receive source-local, zero-based Nth.
func ParseVLESSLinks(source string, links []string) ([]ParsedServer, error) {
	if strings.TrimSpace(source) == "" {
		return nil, fmt.Errorf("subscription source is empty")
	}
	counts := make(map[string]int)
	out := make([]ParsedServer, 0, len(links))
	for i, raw := range links {
		parsed, err := parseVLESS(raw)
		if err != nil {
			return nil, fmt.Errorf("link %d: %w", i+1, err)
		}
		nth := counts[parsed.name]
		counts[parsed.name] = nth + 1
		out = append(out, ParsedServer{
			Key:      ServerKey{Source: source, Name: parsed.name, Nth: nth},
			Name:     parsed.name,
			Outbound: parsed.outbound,
		})
	}
	return out, nil
}

type parsedVLESS struct {
	name     string
	outbound json.RawMessage
}

func parseVLESS(raw string) (parsedVLESS, error) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.Scheme != "vless" {
		return parsedVLESS{}, fmt.Errorf("invalid VLESS URI")
	}
	if u.User == nil || u.User.Username() == "" {
		return parsedVLESS{}, fmt.Errorf("VLESS UUID is missing")
	}
	if _, hasPassword := u.User.Password(); hasPassword {
		return parsedVLESS{}, fmt.Errorf("VLESS userinfo must contain only the UUID")
	}
	if !validUUID(u.User.Username()) {
		return parsedVLESS{}, fmt.Errorf("VLESS UUID has invalid format")
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
	get := func(key string) string { return q.Get(key) }

	encryption := get("encryption")
	if encryption == "" {
		encryption = "none"
	}
	user := map[string]any{"id": u.User.Username(), "encryption": encryption}
	if flow := get("flow"); flow != "" {
		user["flow"] = flow
	}
	outbound := map[string]any{
		"protocol": "vless",
		"settings": map[string]any{"vnext": []any{map[string]any{
			"address": host,
			"port":    port,
			"users":   []any{user},
		}}},
	}

	network := get("type")
	if network == "" {
		network = "tcp"
	}
	stream := map[string]any{"network": network}
	hostHeader := get("host")
	path := get("path")
	switch network {
	case "tcp":
		// TCP is the default stream; no extra transport settings are needed.
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
		serviceName := get("serviceName")
		if serviceName == "" {
			serviceName = strings.TrimPrefix(path, "/")
		}
		if serviceName != "" {
			grpc["serviceName"] = serviceName
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
		xhttp := map[string]any{}
		if path != "" {
			xhttp["path"] = path
		}
		if hostHeader != "" {
			xhttp["host"] = hostHeader
		}
		if mode := get("mode"); mode != "" {
			xhttp["mode"] = mode
		}
		if extra := get("extra"); extra != "" {
			var value any
			if !json.Valid([]byte(extra)) {
				return parsedVLESS{}, fmt.Errorf("XHTTP extra parameter is not valid JSON")
			}
			if err := json.Unmarshal([]byte(extra), &value); err != nil {
				return parsedVLESS{}, fmt.Errorf("XHTTP extra parameter is invalid")
			}
			xhttp["extra"] = value
		}
		stream["xhttpSettings"] = xhttp
	case "httpupgrade":
		upgrade := map[string]any{}
		if path != "" {
			upgrade["path"] = path
		}
		if hostHeader != "" {
			upgrade["host"] = hostHeader
		}
		stream["httpupgradeSettings"] = upgrade
	default:
		return parsedVLESS{}, fmt.Errorf("unsupported VLESS transport %q", network)
	}

	security := get("security")
	if security == "" {
		security = "none"
	}
	stream["security"] = security
	sni := get("sni")
	if sni == "" {
		sni = host
	}
	switch security {
	case "none":
	case "tls":
		tls := map[string]any{"serverName": sni}
		if fp := get("fp"); fp != "" {
			tls["fingerprint"] = fp
		}
		if alpn := splitCSV(q["alpn"]); len(alpn) > 0 {
			tls["alpn"] = alpn
		}
		if v := get("allowInsecure"); v != "" {
			b, err := strconv.ParseBool(v)
			if err != nil {
				return parsedVLESS{}, fmt.Errorf("allowInsecure must be a boolean")
			}
			tls["allowInsecure"] = b
		}
		stream["tlsSettings"] = tls
	case "reality":
		if network != "tcp" && network != "grpc" && network != "xhttp" {
			return parsedVLESS{}, fmt.Errorf("REALITY is supported only with TCP, gRPC or XHTTP transport")
		}
		publicKey, shortID := get("pbk"), get("sid")
		if publicKey == "" || shortID == "" {
			return parsedVLESS{}, fmt.Errorf("REALITY requires pbk and sid parameters")
		}
		reality := map[string]any{"serverName": sni, "publicKey": publicKey, "shortId": shortID}
		if fp := get("fp"); fp != "" {
			reality["fingerprint"] = fp
		}
		if spiderX := get("spx"); spiderX != "" {
			reality["spiderX"] = spiderX
		}
		if verify := get("mldsa65Verify"); verify != "" {
			reality["mldsa65Verify"] = verify
		}
		stream["realitySettings"] = reality
	default:
		return parsedVLESS{}, fmt.Errorf("unsupported VLESS security %q", security)
	}
	outbound["streamSettings"] = stream

	encoded, err := json.Marshal(outbound)
	if err != nil {
		return parsedVLESS{}, fmt.Errorf("cannot encode VLESS outbound")
	}
	name := u.Fragment
	if name == "" {
		name = net.JoinHostPort(host, strconv.Itoa(port))
	}
	return parsedVLESS{name: name, outbound: encoded}, nil
}

func validServerHost(host string) bool {
	if net.ParseIP(host) != nil {
		return true
	}
	if host == "" || len(host) > 253 || strings.ContainsAny(host, " \t\r\n/:@[]") {
		return false
	}
	trimmed := strings.TrimSuffix(host, ".")
	if trimmed == "" {
		return false
	}
	for _, label := range strings.Split(trimmed, ".") {
		if len(label) == 0 || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return false
		}
		for _, r := range label {
			if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-') {
				return false
			}
		}
	}
	return true
}

func validUUID(value string) bool {
	if len(value) != 36 {
		return false
	}
	for i, r := range value {
		if i == 8 || i == 13 || i == 18 || i == 23 {
			if r != '-' {
				return false
			}
			continue
		}
		if !(r >= '0' && r <= '9' || r >= 'a' && r <= 'f' || r >= 'A' && r <= 'F') {
			return false
		}
	}
	return true
}

func splitCSV(values []string) []string {
	var out []string
	for _, value := range values {
		for _, item := range strings.Split(value, ",") {
			if item = strings.TrimSpace(item); item != "" {
				out = append(out, item)
			}
		}
	}
	return out
}

// Fingerprint is an internal, credential-safe identifier for diagnostics and
// reconciliation. It is not a replacement for the public ServerKey.
func (s ParsedServer) Fingerprint() string {
	h := sha256.Sum256(s.Outbound)
	return hex.EncodeToString(h[:])
}
