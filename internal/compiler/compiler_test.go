package compiler

import (
	"encoding/json"
	"fmt"
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
	if len(ins) != 3 {
		t.Fatalf("want 2 profile inbounds plus private API inbound, got %d", len(ins))
	}
	routing := cfg["routing"].(map[string]any)
	rules := routing["rules"].([]any)
	bals := routing["balancers"].([]any)
	if len(rules) != 3 || len(bals) != 2 {
		t.Fatalf("want two independent rules/balancers, got %d/%d", len(rules), len(bals))
	}
	outbounds := cfg["outbounds"].([]any)
	if len(outbounds) != 6 {
		t.Fatalf("want direct+block+3 eligible pool nodes plus private API outbound, got %d", len(outbounds))
	}
	api := cfg["api"].(map[string]any)
	if api["tag"] != "hr_api" || api["services"].([]any)[0] != "ObservatoryService" {
		t.Fatalf("private Observatory API not enabled: %#v", api)
	}
	obs := cfg["observatory"].(map[string]any)
	if len(obs["subjectSelector"].([]any)) != 3 {
		t.Fatal("Observatory must probe exactly the eligible nodes in profile balancers")
	}
	balSelector := bals[0].(map[string]any)["selector"].([]any)
	if len(balSelector) != 1 {
		t.Fatalf("balancer must still contain only eligible nodes: %#v", balSelector)
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
	if len(cfg["outbounds"].([]any)) != 5 {
		t.Fatalf("only the 2 eligible pool nodes may be compiled as outbounds")
	}
	obs := cfg["observatory"].(map[string]any)
	if len(obs["subjectSelector"].([]any)) != 2 {
		t.Fatalf("Observatory subjects must match the automatic pool: %#v", obs)
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
	if len(cfg["outbounds"].([]any)) != 4 {
		t.Fatalf("only the picked node may be compiled as an outbound")
	}
	obs := cfg["observatory"].(map[string]any)
	if len(obs["subjectSelector"].([]any)) != 1 {
		t.Fatalf("Observatory subjects must match the manual pool: %#v", obs)
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

func TestCompileSharedSOCKSOrderedRulesAndExplicitDefault(t *testing.T) {
	got, err := Compile(CompileRequest{
		Profiles: []Profile{
			{ID: "youtube", Enabled: true, Port: 23011, Nodes: []Node{node("yt1", "vless")}},
			{ID: "foreign", Enabled: true, Port: 23012, Nodes: []Node{node("f1", "trojan")}},
		},
		Routing: &RoutingConfig{
			Enabled: true, Port: 23010, DefaultAction: "profile", DefaultProfileID: "foreign",
			Rules: []RoutingRule{
				{ID: "yt", Name: "YouTube", Enabled: true, MatchType: "domain", Values: []string{"geosite:youtube"}, Action: "profile", ProfileID: "youtube"},
				{ID: "ru", Name: "Russian services", Enabled: true, MatchType: "ip", Values: []string{"geoip:ru", "192.0.2.0/24"}, Action: "direct"},
				{ID: "ads", Name: "Block test", Enabled: true, MatchType: "domain", Values: []string{"domain:ads.example"}, Action: "block"},
			},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	cfg := decode(t, got)
	inbounds := cfg["inbounds"].([]any)
	if len(inbounds) != 4 {
		t.Fatalf("want 2 profile inbounds, shared inbound and private API inbound, got %d", len(inbounds))
	}
	routing := cfg["routing"].(map[string]any)
	if routing["domainStrategy"] != "IPIfNonMatch" {
		t.Fatalf("IP rules must enable IPIfNonMatch, got %v", routing["domainStrategy"])
	}
	rules := routing["rules"].([]any)
	if len(rules) != 7 {
		t.Fatalf("want API rule, 2 profile rules, 3 user rules and explicit fallback, got %d", len(rules))
	}
	first := rules[3].(map[string]any)
	if first["inboundTag"].([]any)[0] != "hr_shared_in" || first["balancerTag"] != "hr_bal_youtube" {
		t.Fatalf("first shared rule not bound to YouTube balancer: %#v", first)
	}
	second := rules[4].(map[string]any)
	if second["outboundTag"] != "direct" {
		t.Fatalf("direct action not compiled correctly: %#v", second)
	}
	third := rules[5].(map[string]any)
	if third["outboundTag"] != "block" {
		t.Fatalf("block action not compiled correctly: %#v", third)
	}
	fallback := rules[6].(map[string]any)
	if fallback["inboundTag"].([]any)[0] != "hr_shared_in" || fallback["balancerTag"] != "hr_bal_foreign" {
		t.Fatalf("explicit default profile fallback missing: %#v", fallback)
	}
}

func TestSharedSOCKSRejectsUnsafeFallbackAndInvalidTargets(t *testing.T) {
	base := CompileRequest{
		Profiles: []Profile{{ID: "ok", Enabled: true, Port: 23101, Nodes: []Node{node("n1", "vless")}}},
		Routing:  &RoutingConfig{Enabled: true, Port: 23100, DefaultAction: "profile", DefaultProfileID: "missing"},
	}
	if _, err := Compile(base); err == nil || !strings.Contains(err.Error(), "not enabled") {
		t.Fatalf("expected invalid default profile rejection, got %v", err)
	}
	base.Routing.DefaultAction = "direct"
	base.Routing.DefaultProfileID = ""
	base.Routing.Rules = []RoutingRule{{ID: "bad", Name: "bad cidr", Enabled: true, MatchType: "ip", Values: []string{"10.0.0.999/24"}, Action: "block"}}
	if _, err := Compile(base); err == nil || !strings.Contains(err.Error(), "invalid IP or CIDR") {
		t.Fatalf("expected invalid CIDR rejection, got %v", err)
	}
}

func TestSharedSOCKSRejectsPortCollisionAndNonLoopback(t *testing.T) {
	req := CompileRequest{
		Profiles: []Profile{{ID: "p", Enabled: true, Port: 23200, Nodes: []Node{node("n", "vless")}}},
		Routing:  &RoutingConfig{Enabled: true, Listen: "0.0.0.0", Port: 23200, DefaultAction: "block"},
	}
	if _, err := Compile(req); err == nil || !strings.Contains(err.Error(), "duplicate port") {
		t.Fatalf("expected duplicate port rejection, got %v", err)
	}
	req.Routing.Port = 23201
	if _, err := Compile(req); err == nil || !strings.Contains(err.Error(), "not loopback") {
		t.Fatalf("expected non-loopback rejection, got %v", err)
	}
}

func TestDisabledSharedRoutingPreservesDormantRules(t *testing.T) {
	req := CompileRequest{
		Profiles: []Profile{{ID: "p", Enabled: true, Port: 23300, Nodes: []Node{node("n", "vless")}}},
		Routing:  &RoutingConfig{Enabled: false, Rules: []RoutingRule{{ID: "r", Enabled: true}}},
	}
	got, err := Compile(req)
	if err != nil {
		t.Fatal(err)
	}
	cfg := decode(t, got)
	if len(cfg["inbounds"].([]any)) != 2 {
		t.Fatalf("disabled shared routing must not emit an inbound")
	}
	if len(cfg["routing"].(map[string]any)["rules"].([]any)) != 2 {
		t.Fatalf("disabled shared routing must not emit dormant rules")
	}
}

func TestCoreDNSIsInjectedOnlyWhenExplicitlyEnabled(t *testing.T) {
	base := CompileRequest{Profiles: []Profile{{ID: "dns", Enabled: true, Port: 23400, Nodes: []Node{node("n1", "vless")}}}}
	without, err := Compile(base)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := decode(t, without)["dns"]; ok {
		t.Fatal("DNS must be absent when Core DNS is disabled")
	}
	base.DNS = &DNSConfig{Enabled: true, Listen: "127.0.0.1", Port: 5353}
	with, err := Compile(base)
	if err != nil {
		t.Fatal(err)
	}
	dnsCfg, ok := decode(t, with)["dns"].(map[string]any)
	if !ok {
		t.Fatal("enabled Core DNS config missing")
	}
	servers := dnsCfg["servers"].([]any)
	server := servers[0].(map[string]any)
	if server["address"] != "127.0.0.1" || server["port"] != float64(5353) {
		t.Fatalf("unexpected Core DNS server: %#v", server)
	}
}

func TestCoreDNSRejectsNonLoopbackAndInvalidPort(t *testing.T) {
	base := CompileRequest{Profiles: []Profile{{ID: "dns", Enabled: true, Port: 23401, Nodes: []Node{node("n1", "vless")}}}, DNS: &DNSConfig{Enabled: true, Listen: "0.0.0.0", Port: 5353}}
	if _, err := Compile(base); err == nil || !strings.Contains(err.Error(), "not loopback") {
		t.Fatalf("expected loopback rejection, got %v", err)
	}
	base.DNS.Listen = "127.0.0.1"
	base.DNS.Port = 53
	if _, err := Compile(base); err == nil || !strings.Contains(err.Error(), "outside allowed range") {
		t.Fatalf("expected invalid port rejection, got %v", err)
	}
}

func TestCompileAppliesIndependentDPIBypassOptionsToProxyOutbounds(t *testing.T) {
	profiles := []Profile{{ID: "dpi", Enabled: true, Port: 23040, Nodes: []Node{
		{Key: "tls", Outbound: json.RawMessage(`{"protocol":"vless","settings":{},"streamSettings":{"security":"tls","tlsSettings":{"serverName":"tls.example","fingerprint":"chrome"}}}`)},
		{Key: "reality", Outbound: json.RawMessage(`{"protocol":"vless","settings":{},"streamSettings":{"security":"reality","realitySettings":{"serverName":"reality.example","fingerprint":"chrome"}}}`)},
	}}}
	profiles[0].DPIBypass = DPIBypass{NoiseTraffic: true, TLSFragmentation: true, RandomTLSFingerprint: true}
	got, err := Compile(CompileRequest{Profiles: profiles})
	if err != nil {
		t.Fatal(err)
	}
	cfg := decode(t, got)
	outbounds := cfg["outbounds"].([]any)
	for _, raw := range outbounds[2:4] {
		out := raw.(map[string]any)
		stream := out["streamSettings"].(map[string]any)
		fm := stream["finalmask"].(map[string]any)
		if len(fm["tcp"].([]any)) != 1 || len(fm["udp"].([]any)) != 1 {
			t.Fatalf("DPI masks missing: %#v", fm)
		}
		if tls, ok := stream["tlsSettings"].(map[string]any); ok && tls["fingerprint"] != "random" {
			t.Fatalf("TLS fingerprint not randomized: %#v", tls)
		}
		if reality, ok := stream["realitySettings"].(map[string]any); ok && reality["fingerprint"] != "random" {
			t.Fatalf("REALITY fingerprint not randomized: %#v", reality)
		}
	}
}

func TestCompileAppliesDPIAndOutboundInterfacePerProfileOnly(t *testing.T) {
	profiles := []Profile{
		{ID: "alpha", Enabled: true, Port: 23042, OutboundInterface: "eth0", DPIBypass: DPIBypass{TLSFragmentation: true}, Nodes: []Node{node("a", "vless")}},
		{ID: "beta", Enabled: true, Port: 23043, Nodes: []Node{node("b", "trojan")}},
	}
	got, err := Compile(CompileRequest{Profiles: profiles})
	if err != nil {
		t.Fatal(err)
	}
	cfg := decode(t, got)
	outs := cfg["outbounds"].([]any)
	a := outs[2].(map[string]any)
	b := outs[3].(map[string]any)
	astream := a["streamSettings"].(map[string]any)
	asock := astream["sockopt"].(map[string]any)
	if asock["interface"] != "eth0" {
		t.Fatalf("outbound interface missing: %#v", asock)
	}
	if _, ok := astream["finalmask"]; !ok {
		t.Fatal("per-profile TLS fragmentation missing")
	}
	if _, ok := b["streamSettings"]; ok {
		t.Fatalf("profile alpha settings leaked into beta: %#v", b)
	}
}

func TestCompileLeavesDPIOptionsOffByDefault(t *testing.T) {
	got, err := Compile(CompileRequest{Profiles: []Profile{{ID: "plain", Enabled: true, Port: 23041, Nodes: []Node{node("plain", "vless")}}}})
	if err != nil {
		t.Fatal(err)
	}
	cfg := decode(t, got)
	out := cfg["outbounds"].([]any)[2].(map[string]any)
	if _, ok := out["streamSettings"]; ok {
		t.Fatalf("DPI options changed default outbound: %#v", out)
	}
}

func TestManualEmptyPoolOmitsProfileWithoutCreatingEmptyBalancer(t *testing.T) {
	config, err := Compile(CompileRequest{Profiles: []Profile{{ID: "empty", Enabled: true, Port: 16200, Strategy: "leastPing", ManualSelection: true, Nodes: []Node{{Key: "node-a", Outbound: json.RawMessage(`{"protocol":"freedom"}`)}}}}})
	if err != nil {
		t.Fatal(err)
	}
	var cfg map[string]any
	if err := json.Unmarshal(config, &cfg); err != nil {
		t.Fatal(err)
	}
	if len(cfg["routing"].(map[string]any)["balancers"].([]any)) != 0 {
		t.Fatalf("empty manual pool must not create an empty balancer: %s", config)
	}
	if len(cfg["inbounds"].([]any)) != 0 || cfg["api"] != nil || cfg["observatory"] != nil {
		t.Fatalf("empty manual pool must not create Observatory or inbounds: %s", config)
	}
}

func TestManagedObservatoryAPIReservesLoopbackPort(t *testing.T) {
	_, err := Compile(CompileRequest{Profiles: []Profile{{ID: "conflict", Enabled: true, Port: 16285, Strategy: "leastPing", Nodes: []Node{{Key: "node", Outbound: json.RawMessage(`{"protocol":"freedom"}`)}}}}})
	if err == nil || !strings.Contains(err.Error(), "reserved Xray API port") {
		t.Fatalf("expected reserved Observatory API port error, got %v", err)
	}
}

func TestAllNodesExcludedAreNotCompiledOrProbed(t *testing.T) {
	got, err := Compile(CompileRequest{Profiles: []Profile{{
		ID: "offline", Enabled: true, Port: 23008,
		Nodes:    []Node{node("n1", "vless"), node("n2", "vmess")},
		Excluded: []string{"n1", "n2"}, ManualSelection: true,
	}}})
	if err != nil {
		t.Fatal(err)
	}
	cfg := decode(t, got)
	if cfg["observatory"] != nil || cfg["api"] != nil {
		t.Fatalf("excluded nodes must not create Observatory subjects: %#v", cfg)
	}
	if len(cfg["outbounds"].([]any)) != 2 || len(cfg["inbounds"].([]any)) != 0 {
		t.Fatalf("excluded nodes must not create Xray outbounds or inbounds: %#v", cfg)
	}
	if len(cfg["routing"].(map[string]any)["balancers"].([]any)) != 0 {
		t.Fatal("empty excluded pool must not create a balancer")
	}
}

func TestCompileUsesConfiguredObservatoryIntervalAndOriginalProbeURL(t *testing.T) {
	got, err := Compile(CompileRequest{ObservatoryProbeIntervalSeconds: 137, Profiles: []Profile{{ID: "obs", Enabled: true, Port: 23090, Nodes: []Node{node("n1", "vless")}}}})
	if err != nil {
		t.Fatal(err)
	}
	obs := decode(t, got)["observatory"].(map[string]any)
	if obs["probeInterval"] != "137000ms" {
		t.Fatalf("probeInterval=%v", obs["probeInterval"])
	}
	if obs["probeUrl"] != "https://www.google.com/generate_204" {
		t.Fatalf("probeUrl=%v", obs["probeUrl"])
	}
}

func TestCompileObservatoryIntervalFallsBackOutsideSupportedRange(t *testing.T) {
	for _, value := range []int{0, 4, 3601} {
		got, err := Compile(CompileRequest{ObservatoryProbeIntervalSeconds: value, Profiles: []Profile{{ID: "obs", Enabled: true, Port: 23091, Nodes: []Node{node("n1", "vless")}}}})
		if err != nil {
			t.Fatal(err)
		}
		obs := decode(t, got)["observatory"].(map[string]any)
		if obs["probeInterval"] != "30000ms" {
			t.Fatalf("value %d: probeInterval=%v", value, obs["probeInterval"])
		}
	}
}

func TestObservatoryIntervalIsDistributedAcrossSubjects(t *testing.T) {
	nodes := make([]Node, 104)
	for i := range nodes {
		nodes[i] = node(fmt.Sprintf("node-%03d", i), "vless")
	}
	got, err := Compile(CompileRequest{ObservatoryProbeIntervalSeconds: 1200, Profiles: []Profile{{ID: "many", Enabled: true, Port: 23092, Nodes: nodes}}})
	if err != nil {
		t.Fatal(err)
	}
	obs := decode(t, got)["observatory"].(map[string]any)
	if obs["probeInterval"] != "11538ms" {
		t.Fatalf("probeInterval=%v, want 11538ms", obs["probeInterval"])
	}
}

func TestObservatoryIntervalHasHundredMillisecondFloor(t *testing.T) {
	nodes := make([]Node, 51)
	for i := range nodes {
		nodes[i] = node(fmt.Sprintf("node-%05d", i), "vless")
	}
	got, err := Compile(CompileRequest{ObservatoryProbeIntervalSeconds: 5, Profiles: []Profile{{ID: "floor", Enabled: true, Port: 23093, Nodes: nodes}}})
	if err != nil {
		t.Fatal(err)
	}
	obs := decode(t, got)["observatory"].(map[string]any)
	if obs["probeInterval"] != "100ms" {
		t.Fatalf("probeInterval=%v, want 100ms", obs["probeInterval"])
	}
}
