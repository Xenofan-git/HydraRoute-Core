package parser

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestHysteria2ShareLinks(t *testing.T) {
	links := []string{"hysteria2://secret@edge.example:443?sni=front.example&alpn=h3&insecure=1#same", "hy2://secret@edge.example:443#same"}
	got, err := ParseLinks("quattro", links)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[1].Key.Nth != 1 {
		t.Fatalf("wrong identities: %#v", got)
	}
	var out map[string]any
	if err := json.Unmarshal(got[0].Outbound, &out); err != nil {
		t.Fatal(err)
	}
	if out["protocol"] != "hysteria" {
		t.Fatalf("wrong protocol: %v", out["protocol"])
	}
	stream := out["streamSettings"].(map[string]any)
	if stream["hysteriaSettings"].(map[string]any)["auth"] != "secret" {
		t.Fatal("auth lost")
	}
	if _, exists := stream["tlsSettings"].(map[string]any)["allowInsecure"]; exists {
		t.Fatal("removed Xray allowInsecure field emitted")
	}
	if strings.Contains(got[0].ReferenceURI, "#same") {
		t.Fatal("fragment leaked")
	}
}
func TestHysteria2RejectsUnsupportedAndInvalid(t *testing.T) {
	for _, s := range []string{"hysteria2://@edge.example:443", "hysteria2://secret@edge.example:0", "hysteria2://secret@edge.example:443?insecure=maybe", "hysteria2://secret@edge.example:443?obfs=salamander"} {
		if _, err := ParseLinks("test", []string{s}); err == nil {
			t.Fatal("accepted invalid link")
		}
	}
}
