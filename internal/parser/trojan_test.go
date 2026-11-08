package parser

import (
	"encoding/json"
	"testing"
)

func TestParseTrojanTLSWebSocket(t *testing.T) {
	servers, err := ParseTrojanLinks("src", []string{"trojan://p%40ss%3Aword@edge.example:443?security=tls&sni=front.example&type=ws&host=cdn.example&path=%2Fws&fp=chrome&alpn=h2%2Chttp%2F1.1#Trojan%20Node"})
	if err != nil {
		t.Fatal(err)
	}
	if len(servers) != 1 || servers[0].Name != "Trojan Node" {
		t.Fatalf("identity=%#v", servers)
	}
	cfg := decodeOutbound(t, servers[0].Outbound)
	if cfg["protocol"] != "trojan" {
		t.Fatalf("protocol=%v", cfg["protocol"])
	}
	server := cfg["settings"].(map[string]any)["servers"].([]any)[0].(map[string]any)
	if server["password"] != "p@ss:word" || server["address"] != "edge.example" {
		t.Fatalf("server=%v", server)
	}
	stream := cfg["streamSettings"].(map[string]any)
	ws := stream["wsSettings"].(map[string]any)
	if ws["host"] != "cdn.example" || ws["path"] != "/ws" {
		t.Fatalf("ws=%v", ws)
	}
	tls := stream["tlsSettings"].(map[string]any)
	if tls["serverName"] != "front.example" || tls["fingerprint"] != "chrome" || len(tls["alpn"].([]any)) != 2 {
		t.Fatalf("tls=%v", tls)
	}
}

func TestParseTrojanGRPCAndXHTTP(t *testing.T) {
	grpc, err := ParseTrojanLinks("src", []string{"trojan://pass@grpc.example:443?type=grpc&serviceName=svc&authority=front.example#grpc"})
	if err != nil {
		t.Fatal(err)
	}
	g := decodeOutbound(t, grpc[0].Outbound)["streamSettings"].(map[string]any)["grpcSettings"].(map[string]any)
	if g["serviceName"] != "svc" || g["authority"] != "front.example" {
		t.Fatalf("grpc=%v", g)
	}
	xhttp, err := ParseTrojanLinks("src", []string{"trojan://pass@x.example:443?type=xhttp&path=%2Fapi&host=front.example&mode=stream-one&extra=%7B%22scMaxEachPostBytes%22%3A1000%7D#xhttp"})
	if err != nil {
		t.Fatal(err)
	}
	x := decodeOutbound(t, xhttp[0].Outbound)["streamSettings"].(map[string]any)["xhttpSettings"].(map[string]any)
	if x["path"] != "/api" || x["host"] != "front.example" || x["mode"] != "stream-one" {
		t.Fatalf("xhttp=%v", x)
	}
	if x["extra"].(map[string]any)["scMaxEachPostBytes"] != float64(1000) {
		t.Fatalf("extra=%v", x["extra"])
	}
}

func TestParseTrojanRejectsInvalidAndKeepsPassword(t *testing.T) {
	bad := []string{"trojan://@host:443", "trojan://pass@host:0", "trojan://pass@bad_host:443", "trojan://pass@host:443?type=kcp", "trojan://pass@host:443?security=reality", "vless://pass@host:443"}
	for _, link := range bad {
		if _, err := ParseTrojanLinks("src", []string{link}); err == nil {
			t.Errorf("expected rejection for %s", link)
		}
	}
	raw := "trojan://pass@host.example:443#node"
	parsed, err := ParseTrojanLinks("src", []string{raw})
	if err != nil {
		t.Fatal(err)
	}
	var obj map[string]any
	if err := json.Unmarshal(parsed[0].Outbound, &obj); err != nil {
		t.Fatal(err)
	}
	if obj["protocol"] != "trojan" {
		t.Fatal(obj)
	}
	if _, err := ParseTrojanLinks("", nil); err == nil {
		t.Fatal("expected empty source error")
	}
}
