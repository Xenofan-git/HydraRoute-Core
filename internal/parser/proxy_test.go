package parser

import "testing"

func TestParseSOCKSProxyURI(t *testing.T) {
	servers, err := ParseProxyLinks("src", []string{"socks5://user:p%40ss@proxy.example:1080#SOCKS"})
	if err != nil {
		t.Fatal(err)
	}
	cfg := decodeOutbound(t, servers[0].Outbound)
	if cfg["protocol"] != "socks" {
		t.Fatalf("protocol=%v", cfg["protocol"])
	}
	server := cfg["settings"].(map[string]any)["servers"].([]any)[0].(map[string]any)
	if server["address"] != "proxy.example" || server["version"] != "5" {
		t.Fatalf("server=%v", server)
	}
	user := server["users"].([]any)[0].(map[string]any)
	if user["user"] != "user" || user["pass"] != "p@ss" {
		t.Fatalf("user=%v", user)
	}
}

func TestParseHTTPAndHTTPSProxyURI(t *testing.T) {
	plain, err := ParseProxyLinks("src", []string{"http://alice:secret@proxy.example:8080#HTTP"})
	if err != nil {
		t.Fatal(err)
	}
	if decodeOutbound(t, plain[0].Outbound)["protocol"] != "http" {
		t.Fatal("expected http outbound")
	}
	secure, err := ParseProxyLinks("src", []string{"https://alice:secret@secure.example:8443?sni=front.example&fp=chrome#HTTPS"})
	if err != nil {
		t.Fatal(err)
	}
	cfg := decodeOutbound(t, secure[0].Outbound)
	stream := cfg["streamSettings"].(map[string]any)
	tls := stream["tlsSettings"].(map[string]any)
	if stream["security"] != "tls" || tls["serverName"] != "front.example" || tls["fingerprint"] != "chrome" {
		t.Fatalf("stream=%v", stream)
	}
}

func TestParseProxyRejectsUnsupportedInputs(t *testing.T) {
	bad := []string{"socks5://host:0", "socks5://bad_host:1080", "https://host:443?plugin=x", "http://host:80/path", "https://host:443?allowInsecure=maybe", "hysteria2://pass@host:443"}
	for _, link := range bad {
		if _, err := ParseProxyLinks("src", []string{link}); err == nil {
			t.Errorf("expected rejection for %s", link)
		}
	}
	if _, err := ParseProxyLinks("", nil); err == nil {
		t.Fatal("expected empty source error")
	}
}

func TestParseSOCKSUDPFlag(t *testing.T) {
	servers, err := ParseProxyLinks("src", []string{"socks5://proxy.example:1080?udp=true"})
	if err != nil {
		t.Fatal(err)
	}
	server := decodeOutbound(t, servers[0].Outbound)["settings"].(map[string]any)["servers"].([]any)[0].(map[string]any)
	if server["udp"] != true {
		t.Fatalf("server=%v", server)
	}
}
