package compiler

import (
	"encoding/json"
	"strings"
	"testing"
)

func node(key, protocol string) Node {
	return Node{Key: key, Outbound: json.RawMessage(`{"protocol":"` + protocol + `","settings":{}}`)}
}

func decode(t *testing.T, data []byte) map[string]any {
	t.Helper()
	var v map[string]any
	if err := json.Unmarshal(data, &v); err != nil {
		t.Fatal(err)
	}
	return v
}

func TestCompileSeparatesProfilesAndPools(t *testing.T) {
	got, err := Compile(CompileRequest{Profiles: []Profile{
		{ID: "beta", Enabled: true, Port: 23002, Strategy: "random", Nodes: []Node{node("b1", "vmess"), node("b2", "trojan")}},
		{ID: "alpha", Enabled: true, Port: 23001, Strategy: "leastPing", Nodes: []Node{node("a1", "vless"), node("a2", "shadowsocks")}, Excluded: []string{"a2"}},
	}})
	if err != nil {
		t.Fatal(err)
	}
	if err := ValidateJSON(got); err != nil {
		t.Fatal(err)
	}
	cfg := decode(t, got)
	ins := cfg["inbounds"].([]any)
	if len(ins) != 2 {
		t.Fatalf("want 2 inbounds, got %d", len(ins))
	}
	routing := cfg["routing"].(map[string]any)
	rules := routing["rules"].([]any)
	bals := routing["balancers"].([]any)
	if len(rules) != 2 || len(bals) != 2 {
		t.Fatalf("want two independent rules/balancers, got %d/%d", len(rules), len(bals))
	}
	outbounds := cfg["outbounds"].([]any)
	if len(outbounds) != 5 {
		t.Fatalf("want direct+block+3 eligible nodes, got %d", len(outbounds))
	}
	obs := cfg["observatory"].(map[string]any)
	if len(obs["subjectSelector"].([]any)) != 3 {
		t.Fatal("observatory selector must include exactly eligible nodes")
	}
	for _, raw := range ins {
		in := raw.(map[string]any)
		if in["listen"] != "127.0.0.1" {
			t.Fatalf("non-loopback listener: %v", in["listen"])
		}
	}
}

func TestEmptyPickedMeansAutomaticPool(t *testing.T) {
	got, err := Compile(CompileRequest{Profiles: []Profile{{
		ID: "auto", Enabled: true, Port: 23003,
		Nodes:    []Node{node("n1", "vless"), node("n2", "vmess"), node("n3", "trojan")},
		Excluded: []string{"n2"},
	}}})
	if err != nil {
		t.Fatal(err)
	}
	cfg := decode(t, got)
	if len(cfg["outbounds"].([]any)) != 4 {
		t.Fatalf("automatic pool should contain 2 nodes plus direct/block")
	}
}

func TestPickedSubsetIsIndependentFromExcluded(t *testing.T) {
	got, err := Compile(CompileRequest{Profiles: []Profile{{
		ID: "pick", Enabled: true, Port: 23004,
		Nodes:  []Node{node("n1", "vless"), node("n2", "vmess"), node("n3", "trojan")},
		Picked: []string{"n3"},
	}}})
	if err != nil {
		t.Fatal(err)
	}
	cfg := decode(t, got)
	if len(cfg["outbounds"].([]any)) != 3 {
		t.Fatalf("picked subset should contain one node plus direct/block")
	}
}

func TestRejectsNonLoopbackListener(t *testing.T) {
	_, err := Compile(CompileRequest{Profiles: []Profile{{ID: "bad", Enabled: true, Listen: "0.0.0.0", Port: 23005, Nodes: []Node{node("n1", "vless")}}}})
	if err == nil || !strings.Contains(err.Error(), "not loopback") {
		t.Fatalf("expected loopback validation error, got %v", err)
	}
}

func TestRejectsUnknownPickedNode(t *testing.T) {
	_, err := Compile(CompileRequest{Profiles: []Profile{{ID: "bad", Enabled: true, Port: 23006, Nodes: []Node{node("n1", "vless")}, Picked: []string{"missing"}}}})
	if err == nil || !strings.Contains(err.Error(), "does not exist") {
		t.Fatalf("expected unknown picked error, got %v", err)
	}
}

func TestRejectsPickedAndExcludedOverlap(t *testing.T) {
	_, err := Compile(CompileRequest{Profiles: []Profile{{ID: "bad", Enabled: true, Port: 23007, Nodes: []Node{node("n1", "vless")}, Picked: []string{"n1"}, Excluded: []string{"n1"}}}})
	if err == nil || !strings.Contains(err.Error(), "both picked and excluded") {
		t.Fatalf("expected overlap error, got %v", err)
	}
}

func TestDeterministicOutput(t *testing.T) {
	req := CompileRequest{Profiles: []Profile{{ID: "stable", Enabled: true, Port: 23008, Nodes: []Node{node("node-1", "vless"), node("node-2", "trojan")}}}}
	a, err := Compile(req)
	if err != nil {
		t.Fatal(err)
	}
	b, err := Compile(req)
	if err != nil {
		t.Fatal(err)
	}
	if string(a) != string(b) {
		t.Fatal("compiler output is not deterministic")
	}
}
