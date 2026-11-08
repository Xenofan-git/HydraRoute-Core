package parser

import (
	"encoding/base64"
	"strings"
	"testing"
)

func TestParseSubscriptionPlainMixedLinks(t *testing.T) {
	body := "\n# generated list\n" + "vless://" + uuid1 + "@edge.example:443#VLESS\r\n" + "socks5://user:pass@proxy.example:1080#SOCKS\n"
	servers, err := ParseSubscription("sub-A", body)
	if err != nil {
		t.Fatal(err)
	}
	if len(servers) != 2 || servers[0].Key.Source != "sub-A" || decodeOutbound(t, servers[1].Outbound)["protocol"] != "socks" {
		t.Fatalf("servers=%#v", servers)
	}
}

func TestParseSubscriptionBase64MixedLinks(t *testing.T) {
	body := "vless://" + uuid1 + "@edge.example:443#VLESS\n" + "https://user:pass@proxy.example:443#HTTPS\n"
	encoded := base64.StdEncoding.EncodeToString([]byte(body))
	servers, err := ParseSubscription("sub-B", encoded)
	if err != nil {
		t.Fatal(err)
	}
	if len(servers) != 2 || decodeOutbound(t, servers[1].Outbound)["protocol"] != "http" {
		t.Fatalf("servers=%#v", servers)
	}
}

func TestParseSubscriptionBase64WhitespaceAndErrors(t *testing.T) {
	body := "vless://" + uuid1 + "@edge.example:443#VLESS\n"
	encoded := base64.StdEncoding.EncodeToString([]byte(body))
	wrapped := strings.Join([]string{encoded[:12], encoded[12:]}, "\n")
	if _, err := ParseSubscription("sub", wrapped); err != nil {
		t.Fatal(err)
	}
	for _, input := range []string{"", "not-base64!", "a line without scheme", "###"} {
		if _, err := ParseSubscription("sub", input); err == nil {
			t.Errorf("expected error for %q", input)
		}
	}
	if _, err := ParseSubscription("", body); err == nil {
		t.Fatal("expected empty source error")
	}
}
