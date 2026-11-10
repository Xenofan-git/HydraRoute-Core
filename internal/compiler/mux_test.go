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
		Picked: []string{"safe"},
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
	if mux["enabled"] != true || mux["concurrency"] != 8 || mux["xudpConcurrency"] != 8 || mux["xudpProxyUDP443"] != "reject" {
		t.Fatalf("unexpected mux config: %#v", mux)
	}
}

func TestProfileSupportsMuxRequiresSelectedNode(t *testing.T) {
	p := Profile{Nodes: []Node{{Key: "a", Outbound: json.RawMessage(`{"protocol":"vmess"}`)}}, Excluded: []string{"a"}}
	if profileSupportsMux(p) {
		t.Fatal("empty selected pool must be mux-incompatible")
	}
}
