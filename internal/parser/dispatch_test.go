package parser

import (
	"testing"
)

func TestParseLinksDispatchesMixedProtocolsWithSourceWideOrdinals(t *testing.T) {
	vless := "vless://" + uuid1 + "@one.example:443#same"
	vmess := vmessLink(t, map[string]any{"ps": "same", "add": "two.example", "port": "443", "id": uuid1})
	parsed, err := ParseLinks("source", []string{vless, vmess})
	if err != nil {
		t.Fatal(err)
	}
	if len(parsed) != 2 || parsed[0].Key.Nth != 0 || parsed[1].Key.Nth != 1 {
		t.Fatalf("unexpected mixed identities: %#v %#v", parsed[0].Key, parsed[1].Key)
	}
	if string(parsed[0].Outbound) == string(parsed[1].Outbound) {
		t.Fatal("protocol outputs unexpectedly match")
	}
}

func TestParseLinksRejectsUnknownScheme(t *testing.T) {
	if _, err := ParseLinks("source", []string{"ss://password@example:443"}); err == nil {
		t.Fatal("expected unsupported scheme error")
	}
}

func TestParseLinksDispatchesTrojan(t *testing.T) {
	parsed, err := ParseLinks("source", []string{"trojan://secret@trojan.example:443#trojan"})
	if err != nil {
		t.Fatal(err)
	}
	if len(parsed) != 1 || decodeOutbound(t, parsed[0].Outbound)["protocol"] != "trojan" {
		t.Fatalf("parsed=%#v", parsed)
	}
}

func TestParseLinksDispatchesShadowsocks(t *testing.T) {
	parsed, err := ParseLinks("source", []string{"ss://YWVzLTI1Ni1nY206cGFzcw==@ss.example:8388#ss"})
	if err != nil {
		t.Fatal(err)
	}
	if len(parsed) != 1 || decodeOutbound(t, parsed[0].Outbound)["protocol"] != "shadowsocks" {
		t.Fatalf("parsed=%#v", parsed)
	}
}

func TestParseLinksDispatchesSOCKSAndHTTP(t *testing.T) {
	parsed, err := ParseLinks("source", []string{"socks5://user:pass@proxy.example:1080#socks", "https://user:pass@proxy.example:443#https"})
	if err != nil {
		t.Fatal(err)
	}
	if len(parsed) != 2 || decodeOutbound(t, parsed[0].Outbound)["protocol"] != "socks" || decodeOutbound(t, parsed[1].Outbound)["protocol"] != "http" {
		t.Fatalf("parsed=%#v", parsed)
	}
}
