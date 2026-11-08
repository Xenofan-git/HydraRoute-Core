package parser

import (
	"encoding/base64"
	"encoding/json"
	"testing"
)

func TestParseShadowsocksSIP002(t *testing.T) {
	auth := base64.RawStdEncoding.EncodeToString([]byte("chacha20-ietf-poly1305:p@ss:word"))
	link := "ss://" + auth + "@ss.example:8388#Frankfurt%20SS"
	servers, err := ParseShadowsocksLinks("src", []string{link})
	if err != nil {
		t.Fatal(err)
	}
	if len(servers) != 1 || servers[0].Name != "Frankfurt SS" {
		t.Fatalf("identity=%#v", servers)
	}
	cfg := decodeOutbound(t, servers[0].Outbound)
	if cfg["protocol"] != "shadowsocks" {
		t.Fatalf("protocol=%v", cfg["protocol"])
	}
	server := cfg["settings"].(map[string]any)["servers"].([]any)[0].(map[string]any)
	if server["method"] != "chacha20-ietf-poly1305" || server["password"] != "p@ss:word" || server["port"] != float64(8388) {
		t.Fatalf("server=%v", server)
	}
}

func TestParseShadowsocksLegacyWholePayload(t *testing.T) {
	raw := base64.StdEncoding.EncodeToString([]byte("aes-256-gcm:secret@legacy.example:443"))
	servers, err := ParseShadowsocksLinks("src", []string{"ss://" + raw + "#legacy"})
	if err != nil {
		t.Fatal(err)
	}
	if servers[0].Name != "legacy" {
		t.Fatalf("name=%q", servers[0].Name)
	}
	server := decodeOutbound(t, servers[0].Outbound)["settings"].(map[string]any)["servers"].([]any)[0].(map[string]any)
	if server["address"] != "legacy.example" || server["method"] != "aes-256-gcm" || server["password"] != "secret" {
		t.Fatalf("server=%v", server)
	}
}

func TestParseShadowsocksRejectsInvalidLinks(t *testing.T) {
	bad := []string{"vless://user@host:443", "ss://", "ss://bad-base64", "ss://" + base64.RawStdEncoding.EncodeToString([]byte("unknown:pass")) + "@host:443", "ss://" + base64.RawStdEncoding.EncodeToString([]byte("aes-256-gcm:pass")) + "@host:70000", "ss://" + base64.RawStdEncoding.EncodeToString([]byte("aes-256-gcm:pass")) + "@bad_host:443", "ss://" + base64.RawStdEncoding.EncodeToString([]byte("aes-256-gcm:pass")) + "@host:443?plugin=obfs-local"}
	for _, link := range bad {
		if _, err := ParseShadowsocksLinks("src", []string{link}); err == nil {
			t.Errorf("expected rejection for %s", link)
		}
	}
	if _, err := ParseShadowsocksLinks("", nil); err == nil {
		t.Fatal("expected empty source error")
	}
}

func TestParseShadowsocksPlainUserInfo(t *testing.T) {
	servers, err := ParseShadowsocksLinks("src", []string{"ss://aes-128-gcm:secret@plain.example:8443#plain"})
	if err != nil {
		t.Fatal(err)
	}
	var cfg map[string]any
	if err := json.Unmarshal(servers[0].Outbound, &cfg); err != nil {
		t.Fatal(err)
	}
	if cfg["protocol"] != "shadowsocks" {
		t.Fatal(cfg)
	}
}
