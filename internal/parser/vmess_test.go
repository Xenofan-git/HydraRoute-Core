package parser

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"testing"
)

func vmessLink(t *testing.T, p map[string]any) string {
	t.Helper()
	b, err := json.Marshal(p)
	if err != nil {
		t.Fatal(err)
	}
	return "vmess://" + base64.RawStdEncoding.EncodeToString(b)
}

func TestParseVMessBasicTLSWebSocket(t *testing.T) {
	link := vmessLink(t, map[string]any{"ps": "Frankfurt VMess", "add": "edge.example", "port": "443", "id": uuid1, "aid": "0", "scy": "auto", "net": "ws", "host": "cdn.example", "path": "/socket", "tls": "tls", "sni": "sni.example", "alpn": "h2,http/1.1", "fp": "chrome"})
	servers, err := ParseVMessLinks("source-A", []string{link})
	if err != nil {
		t.Fatal(err)
	}
	if len(servers) != 1 || servers[0].Name != "Frankfurt VMess" || servers[0].Key.Source != "source-A" {
		t.Fatalf("identity=%#v", servers)
	}
	cfg := decodeOutbound(t, servers[0].Outbound)
	if cfg["protocol"] != "vmess" {
		t.Fatalf("protocol=%v", cfg["protocol"])
	}
	settings := cfg["settings"].(map[string]any)["vnext"].([]any)[0].(map[string]any)
	user := settings["users"].([]any)[0].(map[string]any)
	if user["id"] != uuid1 || user["alterId"] != float64(0) || user["security"] != "auto" {
		t.Fatalf("user=%v", user)
	}
	stream := cfg["streamSettings"].(map[string]any)
	ws := stream["wsSettings"].(map[string]any)
	if ws["host"] != "cdn.example" || ws["path"] != "/socket" {
		t.Fatalf("ws=%v", ws)
	}
	tls := stream["tlsSettings"].(map[string]any)
	if tls["serverName"] != "sni.example" || tls["fingerprint"] != "chrome" {
		t.Fatalf("tls=%v", tls)
	}
	if len(tls["alpn"].([]any)) != 2 {
		t.Fatalf("alpn=%v", tls["alpn"])
	}
}

func TestParseVMessNumericPortAndGRPC(t *testing.T) {
	link := vmessLink(t, map[string]any{"ps": "grpc", "add": "grpc.example", "port": 443, "id": uuid1, "aid": 0, "net": "grpc", "path": "/service/name", "host": "authority.example"})
	servers, err := ParseVMessLinks("src", []string{link})
	if err != nil {
		t.Fatal(err)
	}
	stream := decodeOutbound(t, servers[0].Outbound)["streamSettings"].(map[string]any)
	grpc := stream["grpcSettings"].(map[string]any)
	if grpc["serviceName"] != "service/name" || grpc["authority"] != "authority.example" {
		t.Fatalf("grpc=%v", grpc)
	}
}

func TestParseVMessStableDuplicateNames(t *testing.T) {
	a := vmessLink(t, map[string]any{"ps": "same", "add": "one.example", "port": "443", "id": uuid1})
	b := vmessLink(t, map[string]any{"ps": "same", "add": "two.example", "port": "443", "id": uuid1})
	servers, err := ParseVMessLinks("src", []string{a, b})
	if err != nil {
		t.Fatal(err)
	}
	if servers[0].Key.Nth != 0 || servers[1].Key.Nth != 1 || servers[0].CompilerKey() == servers[1].CompilerKey() {
		t.Fatalf("keys=%#v %#v", servers[0].Key, servers[1].Key)
	}
}

func TestParseVMessRejectsInvalidLinks(t *testing.T) {
	bad := []string{"vless://" + uuid1 + "@host:443", "vmess://not-base64", "vmess://" + base64.RawStdEncoding.EncodeToString([]byte("not-json")), vmessLink(t, map[string]any{"add": "host", "port": "443", "id": "bad"}), vmessLink(t, map[string]any{"add": "host", "port": "70000", "id": uuid1}), vmessLink(t, map[string]any{"add": "host", "port": "443", "id": uuid1, "net": "kcp"}), vmessLink(t, map[string]any{"add": "host", "port": "443", "id": uuid1, "tls": "reality"})}
	for i, link := range bad {
		t.Run(fmt.Sprintf("case-%d", i), func(t *testing.T) {
			if _, err := ParseVMessLinks("src", []string{link}); err == nil {
				t.Fatal("expected error")
			}
		})
	}
	if _, err := ParseVMessLinks("", nil); err == nil {
		t.Fatal("expected empty source error")
	}
}
