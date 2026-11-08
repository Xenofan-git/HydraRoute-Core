package parser

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
)

type vmessShare struct {
	Name     string          `json:"ps"`
	Address  string          `json:"add"`
	Port     json.RawMessage `json:"port"`
	ID       string          `json:"id"`
	AlterID  json.RawMessage `json:"aid"`
	Security string          `json:"scy"`
	Network  string          `json:"net"`
	Header   string          `json:"type"`
	Host     string          `json:"host"`
	Path     string          `json:"path"`
	TLS      string          `json:"tls"`
	SNI      string          `json:"sni"`
	ALPN     string          `json:"alpn"`
	FP       string          `json:"fp"`
}

func ParseVMessLinks(source string, links []string) ([]ParsedServer, error) {
	if strings.TrimSpace(source) == "" {
		return nil, fmt.Errorf("subscription source is empty")
	}
	counts := map[string]int{}
	out := make([]ParsedServer, 0, len(links))
	for i, raw := range links {
		p, err := parseVMess(raw)
		if err != nil {
			return nil, fmt.Errorf("link %d: %w", i+1, err)
		}
		nth := counts[p.name]
		counts[p.name] = nth + 1
		out = append(out, ParsedServer{Key: ServerKey{Source: source, Name: p.name, Nth: nth}, Name: p.name, Outbound: p.outbound})
	}
	return out, nil
}

func parseVMess(raw string) (parsedVLESS, error) {
	raw = strings.TrimSpace(raw)
	if !strings.HasPrefix(strings.ToLower(raw), "vmess://") {
		return parsedVLESS{}, fmt.Errorf("invalid VMess URI")
	}
	payload := strings.TrimSpace(raw[len("vmess://"):])
	if payload == "" || strings.ContainsAny(payload, "/?#") {
		return parsedVLESS{}, fmt.Errorf("invalid VMess payload")
	}
	decoded, err := decodeVMessBase64(payload)
	if err != nil {
		return parsedVLESS{}, fmt.Errorf("VMess payload is not valid base64")
	}
	var s vmessShare
	if err = json.Unmarshal(decoded, &s); err != nil {
		return parsedVLESS{}, fmt.Errorf("VMess payload is not valid JSON")
	}
	if !validServerHost(s.Address) {
		return parsedVLESS{}, fmt.Errorf("server address is invalid")
	}
	port, err := rawJSONInt(s.Port)
	if err != nil || port < 1 || port > 65535 {
		return parsedVLESS{}, fmt.Errorf("server port must be between 1 and 65535")
	}
	if !validUUID(s.ID) {
		return parsedVLESS{}, fmt.Errorf("VMess UUID has invalid format")
	}
	aid := 0
	if len(s.AlterID) > 0 && string(s.AlterID) != "null" {
		aid, err = rawJSONInt(s.AlterID)
		if err != nil || aid < 0 || aid > 65535 {
			return parsedVLESS{}, fmt.Errorf("VMess alterId must be between 0 and 65535")
		}
	}
	cipher := strings.TrimSpace(s.Security)
	if cipher == "" {
		cipher = "auto"
	}
	switch cipher {
	case "auto", "none", "zero", "aes-128-gcm", "chacha20-poly1305":
	default:
		return parsedVLESS{}, fmt.Errorf("unsupported VMess cipher %q", cipher)
	}
	user := map[string]any{"id": s.ID, "alterId": aid, "security": cipher}
	outbound := map[string]any{"protocol": "vmess", "settings": map[string]any{"vnext": []any{map[string]any{"address": s.Address, "port": port, "users": []any{user}}}}}
	network := strings.ToLower(strings.TrimSpace(s.Network))
	if network == "" {
		network = "tcp"
	}
	stream := map[string]any{"network": network}
	switch network {
	case "tcp":
		header := strings.ToLower(strings.TrimSpace(s.Header))
		if header != "" && header != "none" {
			return parsedVLESS{}, fmt.Errorf("unsupported VMess TCP header type %q", header)
		}
		stream["tcpSettings"] = map[string]any{"header": map[string]any{"type": "none"}}
	case "ws":
		ws := map[string]any{}
		if s.Path != "" {
			ws["path"] = s.Path
		}
		if s.Host != "" {
			ws["host"] = s.Host
		}
		stream["wsSettings"] = ws
	case "grpc":
		grpc := map[string]any{}
		if s.Path != "" {
			grpc["serviceName"] = strings.TrimPrefix(s.Path, "/")
		}
		if s.Host != "" {
			grpc["authority"] = s.Host
		}
		stream["grpcSettings"] = grpc
	case "httpupgrade":
		x := map[string]any{}
		if s.Path != "" {
			x["path"] = s.Path
		}
		if s.Host != "" {
			x["host"] = s.Host
		}
		stream["httpupgradeSettings"] = x
	case "xhttp":
		x := map[string]any{}
		if s.Path != "" {
			x["path"] = s.Path
		}
		if s.Host != "" {
			x["host"] = s.Host
		}
		stream["xhttpSettings"] = x
	default:
		return parsedVLESS{}, fmt.Errorf("unsupported VMess transport %q", network)
	}
	tlsMode := strings.ToLower(strings.TrimSpace(s.TLS))
	switch tlsMode {
	case "", "none":
		stream["security"] = "none"
	case "tls":
		stream["security"] = "tls"
		name := s.SNI
		if name == "" {
			name = s.Host
		}
		if name == "" {
			name = s.Address
		}
		tls := map[string]any{"serverName": name}
		if s.FP != "" {
			tls["fingerprint"] = s.FP
		}
		if s.ALPN != "" {
			tls["alpn"] = splitCSV([]string{s.ALPN})
		}
		stream["tlsSettings"] = tls
	default:
		return parsedVLESS{}, fmt.Errorf("unsupported VMess TLS mode %q", tlsMode)
	}
	outbound["streamSettings"] = stream
	encoded, err := json.Marshal(outbound)
	if err != nil {
		return parsedVLESS{}, fmt.Errorf("cannot encode VMess outbound")
	}
	name := s.Name
	if name == "" {
		name = s.Address + ":" + strconv.Itoa(port)
	}
	return parsedVLESS{name: name, outbound: encoded}, nil
}

func decodeVMessBase64(value string) ([]byte, error) {
	for _, enc := range []*base64.Encoding{base64.RawStdEncoding, base64.StdEncoding, base64.RawURLEncoding, base64.URLEncoding} {
		if b, err := enc.DecodeString(value); err == nil {
			return b, nil
		}
	}
	return nil, fmt.Errorf("invalid base64")
}

func rawJSONInt(raw json.RawMessage) (int, error) {
	if len(raw) == 0 {
		return 0, fmt.Errorf("missing integer")
	}
	var n int
	if err := json.Unmarshal(raw, &n); err == nil {
		return n, nil
	}
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		return strconv.Atoi(s)
	}
	return 0, fmt.Errorf("invalid integer")
}
