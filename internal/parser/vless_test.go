package parser

import (
	"encoding/json"
	"strings"
	"testing"

	"hydraroute.local/core/internal/compiler"
)

const uuid1 = "00000000-0000-4000-8000-000000000001"

func decodeOutbound(t *testing.T, raw json.RawMessage) map[string]any {
	t.Helper()
	var value map[string]any
	if err := json.Unmarshal(raw, &value); err != nil {
		t.Fatalf("decode outbound: %v", err)
	}
	return value
}

func TestParseVLESSBasicTLSAndName(t *testing.T) {
	servers, err := ParseVLESSLinks("https://sub.example/list", []string{
		"vless://" + uuid1 + "@edge.example:443?encryption=none&security=tls&sni=cdn.example&fp=chrome&alpn=h2%2Chttp%2F1.1#Frankfurt%20A",
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(servers) != 1 {
		t.Fatalf("got %d servers", len(servers))
	}
	s := servers[0]
	if s.Name != "Frankfurt A" || s.Key.Name != "Frankfurt A" || s.Key.Source != "https://sub.example/list" || s.Key.Nth != 0 {
		t.Fatalf("unexpected identity: %#v", s.Key)
	}
	cfg := decodeOutbound(t, s.Outbound)
	if cfg["protocol"] != "vless" {
		t.Fatalf("protocol=%v", cfg["protocol"])
	}
	stream := cfg["streamSettings"].(map[string]any)
	if stream["network"] != "tcp" || stream["security"] != "tls" {
		t.Fatalf("stream=%v", stream)
	}
	tls := stream["tlsSettings"].(map[string]any)
	if tls["serverName"] != "cdn.example" || tls["fingerprint"] != "chrome" {
		t.Fatalf("tls=%v", tls)
	}
	if got := tls["alpn"].([]any); len(got) != 2 || got[0] != "h2" || got[1] != "http/1.1" {
		t.Fatalf("alpn=%v", got)
	}
}

func TestParseVLESSRealityAndGRPC(t *testing.T) {
	uri := "vless://" + uuid1 + "@[2001:db8::1]:8443?type=grpc&security=reality&sni=front.example&serviceName=grpc-svc&pbk=public-key&sid=abcd&fp=chrome&spx=%2F#Node"
	servers, err := ParseVLESSLinks("source-A", []string{uri})
	if err != nil {
		t.Fatal(err)
	}
	cfg := decodeOutbound(t, servers[0].Outbound)
	stream := cfg["streamSettings"].(map[string]any)
	if stream["network"] != "grpc" || stream["security"] != "reality" {
		t.Fatalf("stream=%v", stream)
	}
	grpc := stream["grpcSettings"].(map[string]any)
	if grpc["serviceName"] != "grpc-svc" {
		t.Fatalf("grpc=%v", grpc)
	}
	reality := stream["realitySettings"].(map[string]any)
	if reality["publicKey"] != "public-key" || reality["shortId"] != "abcd" || reality["spiderX"] != "/" {
		t.Fatalf("reality=%v", reality)
	}
}

func TestParseVLESSGRPCAndXHTTP(t *testing.T) {
	grpc, err := ParseVLESSLinks("src", []string{"vless://" + uuid1 + "@grpc.example:443?type=grpc&serviceName=svc%2Fname&path=ignored&authority=authority.example&mode=multi#grpc"})
	if err != nil {
		t.Fatal(err)
	}
	g := decodeOutbound(t, grpc[0].Outbound)["streamSettings"].(map[string]any)["grpcSettings"].(map[string]any)
	if g["serviceName"] != "svc/name" || g["authority"] != "authority.example" || g["multiMode"] != true {
		t.Fatalf("grpc=%v", g)
	}

	xhttp, err := ParseVLESSLinks("src", []string{"vless://" + uuid1 + "@xhttp.example:443?type=xhttp&path=%2Fapi&host=front.example&mode=stream-one&extra=%7B%22scMaxEachPostBytes%22%3A1000%7D#xhttp"})
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

func TestParseVLESSDuplicateNamesUseSourceLocalOrdinal(t *testing.T) {
	links := []string{
		"vless://" + uuid1 + "@one.example:443#same",
		"vless://" + uuid1 + "@two.example:443#same",
		"vless://" + uuid1 + "@three.example:443#other",
	}
	servers, err := ParseVLESSLinks("source-A", links)
	if err != nil {
		t.Fatal(err)
	}
	if servers[0].Key.Nth != 0 || servers[1].Key.Nth != 1 || servers[2].Key.Nth != 0 {
		t.Fatalf("unexpected ordinals: %#v", servers)
	}
	if servers[0].CompilerKey() == servers[1].CompilerKey() {
		t.Fatal("duplicate-name keys collided")
	}
	other, err := ParseVLESSLinks("source-B", links[:1])
	if err != nil {
		t.Fatal(err)
	}
	if other[0].CompilerKey() == servers[0].CompilerKey() {
		t.Fatal("source was not included in key")
	}
}

func TestParseVLESSRejectsMalformedAndUnsupported(t *testing.T) {
	cases := []string{
		"vless://user@host:443#x",
		"vless://" + uuid1 + "@host:0#x",
		"vless://" + uuid1 + "@host:443?type=kcp#x",
		"vless://" + uuid1 + "@host:443?security=reality&pbk=only#x",
		"vless://" + uuid1 + "@host:443?type=xhttp&extra=not-json#x",
		"vless://" + uuid1 + "@bad_host:443#x",
		"vless://" + uuid1 + "@host:443?type=ws&security=reality&pbk=key&sid=id#x",
		"trojan://password@host:443#x",
	}
	for _, input := range cases {
		t.Run(input[:strings.Index(input, "@")], func(t *testing.T) {
			if _, err := ParseVLESSLinks("source", []string{input}); err == nil {
				t.Fatalf("expected rejection")
			}
		})
	}
}

func TestParseVLESSRequiresSourceAndHandlesEmptyList(t *testing.T) {
	if _, err := ParseVLESSLinks("", nil); err == nil {
		t.Fatal("expected empty source error")
	}
	servers, err := ParseVLESSLinks("source", nil)
	if err != nil || len(servers) != 0 {
		t.Fatalf("servers=%v err=%v", servers, err)
	}
}

func TestParsedVLESSFeedsProfileCompiler(t *testing.T) {
	servers, err := ParseVLESSLinks("source", []string{"vless://" + uuid1 + "@edge.example:443?security=tls&sni=edge.example#edge"})
	if err != nil {
		t.Fatal(err)
	}
	request := compiler.CompileRequest{Profiles: []compiler.Profile{{ID: "foreign", Enabled: true, Port: 16080, Nodes: []compiler.Node{{Key: servers[0].CompilerKey(), Outbound: servers[0].CompilerOutbound()}}}}}
	config, err := compiler.Compile(request)
	if err != nil {
		t.Fatal(err)
	}
	if err := compiler.ValidateJSON(config); err != nil {
		t.Fatal(err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(config, &decoded); err != nil {
		t.Fatal(err)
	}
	outbounds := decoded["outbounds"].([]any)
	if len(outbounds) != 3 {
		t.Fatalf("expected direct, block and one VLESS outbound; got %d", len(outbounds))
	}
	if outbounds[2].(map[string]any)["protocol"] != "vless" {
		t.Fatalf("unexpected outbound: %v", outbounds[2])
	}
}

func TestParseVLESSWebSocketAndHTTPUpgradeUseIndependentHost(t *testing.T) {
	ws, err := ParseVLESSLinks("source", []string{"vless://" + uuid1 + "@ws.example:443?type=ws&security=tls&host=front.example&path=%2Fws#ws"})
	if err != nil {
		t.Fatal(err)
	}
	wsSettings := decodeOutbound(t, ws[0].Outbound)["streamSettings"].(map[string]any)["wsSettings"].(map[string]any)
	if wsSettings["host"] != "front.example" {
		t.Fatalf("ws settings=%v", wsSettings)
	}
	if _, exists := wsSettings["headers"]; exists {
		t.Fatalf("legacy headers field should not be emitted: %v", wsSettings)
	}

	httpUpgrade, err := ParseVLESSLinks("source", []string{"vless://" + uuid1 + "@upgrade.example:443?type=httpupgrade&security=tls&host=front.example&path=%2Fupgrade#upgrade"})
	if err != nil {
		t.Fatal(err)
	}
	upgrade := decodeOutbound(t, httpUpgrade[0].Outbound)["streamSettings"].(map[string]any)["httpupgradeSettings"].(map[string]any)
	if upgrade["host"] != "front.example" {
		t.Fatalf("httpupgrade settings=%v", upgrade)
	}
	if _, exists := upgrade["headers"]; exists {
		t.Fatalf("headers field must not be emitted: %v", upgrade)
	}
}

func TestParseVLESSMissingNameUsesCredentialSafeHostFallback(t *testing.T) {
	servers, err := ParseVLESSLinks("source", []string{"vless://" + uuid1 + "@edge.example:443?security=tls"})
	if err != nil {
		t.Fatal(err)
	}
	if servers[0].Name != "edge.example:443" {
		t.Fatalf("fallback name=%q", servers[0].Name)
	}
	if strings.Contains(servers[0].Name, uuid1) {
		t.Fatal("fallback name leaked UUID")
	}
}
