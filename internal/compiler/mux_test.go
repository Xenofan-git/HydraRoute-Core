package compiler

import (
	"encoding/json"
	"testing"
)

func TestProfileSupportsMuxRejectsXTLSRPRX(t *testing.T) {
	p := Profile{Nodes: []Node{
		{Key: "vless", Outbound: json.RawMessage(`{"protocol":"vless","settings":{"vnext":[{"users":[{"flow":"xtls-rprx-vision"}]}]}}`)},
		{Key: "vmess", Outbound: json.RawMessage(`{"protocol":"vmess","settings":{}}`)},
	}}
	if profileSupportsMux(p) {
		t.Fatal("pool containing XTLS RPRX flow must be mux-incompatible")
	}
}

func TestProfileSupportsMuxUsesSelectedPool(t *testing.T) {
	p := Profile{
		ManualSelection: true,
		Picked:          []string{"safe"},
		Nodes: []Node{
			{Key: "safe", Outbound: json.RawMessage(`{"protocol":"vmess","settings":{}}`)},
			{Key: "vision", Outbound: json.RawMessage(`{"protocol":"vless","settings":{"vnext":[{"users":[{"flow":"xtls-rprx-vision"}]}]}}`)},
		},
	}
	if !profileSupportsMux(p) {
		t.Fatal("unselected XTLS node must not make the active pool incompatible")
	}
}

func TestApplyMuxUsesHRNeoProfileDefaults(t *testing.T) {
	outbound := map[string]any{"protocol": "vmess"}
	applyMux(outbound)
	mux, ok := outbound["mux"].(map[string]any)
	if !ok {
		t.Fatal("mux object was not added")
	}
	if mux["enabled"] != true || mux["concurrency"] != 8 || mux["xudpConcurrency"] != 1 || mux["xudpProxyUDP443"] != "reject" {
		t.Fatalf("unexpected mux config: %#v", mux)
	}
}

func TestProfileSupportsMuxRequiresSelectedNode(t *testing.T) {
	p := Profile{Nodes: []Node{{Key: "a", Outbound: json.RawMessage(`{"protocol":"vmess"}`)}}, Excluded: []string{"a"}}
	if profileSupportsMux(p) {
		t.Fatal("empty selected pool must be mux-incompatible")
	}
}

func TestCompileAppliesMuxOnlyForCompatibleProfilePool(t *testing.T) {
	cases := []struct {
		name     string
		outbound string
		wantMux  bool
	}{
		{name: "compatible", outbound: `{"protocol":"vmess","settings":{}}`, wantMux: true},
		{name: "xtls flow", outbound: `{"protocol":"vless","settings":{"vnext":[{"users":[{"flow":"xtls-rprx-vision"}]}]}}`, wantMux: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			raw, err := Compile(CompileRequest{Profiles: []Profile{{
				ID: "mux-test", Enabled: true, Port: 16091, Strategy: "leastPing",
				MuxEnabled: true,
				Nodes:      []Node{{Key: "node-1", Outbound: json.RawMessage(tc.outbound)}},
			}}})
			if err != nil {
				t.Fatalf("Compile() error = %v", err)
			}
			var config map[string]any
			if err := json.Unmarshal(raw, &config); err != nil {
				t.Fatalf("decode compiled config: %v", err)
			}
			outbounds, ok := config["outbounds"].([]any)
			if !ok {
				t.Fatal("compiled outbounds missing")
			}
			found := false
			for _, item := range outbounds {
				outbound, ok := item.(map[string]any)
				if !ok || outbound["tag"] != OutboundTag("mux-test", "node-1") {
					continue
				}
				_, found = outbound["mux"]
			}
			if found != tc.wantMux {
				t.Fatalf("mux present = %v, want %v", found, tc.wantMux)
			}
		})
	}
}
