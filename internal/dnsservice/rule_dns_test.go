package dnsservice

import (
	mdns "github.com/miekg/dns"
	"hydraroute.local/core/internal/compiler"
	"testing"
)

func TestRuleDNSRespectsRoutingPriorityAndFallback(t *testing.T) {
	m := New(t.TempDir() + "/dns.json")
	cfg := m.Config()
	cfg.Servers = []Server{{Name: "one", Protocol: "Do53", Address: "127.0.0.1:53", Enabled: true}, {Name: "two", Protocol: "Do53", Address: "127.0.0.1:54", Enabled: true}}
	cfg.RuleDNS = map[string][]string{"first": {"two"}, "second": {"one"}}
	if err := m.Save(cfg); err != nil {
		t.Fatal(err)
	}
	m.SetRoutingRules([]compiler.RoutingRule{{ID: "first", Enabled: true, MatchType: "domain", Values: []string{"domain:example.com"}}, {ID: "second", Enabled: true, MatchType: "domain", Values: []string{"full:a.example.com"}}})
	req := new(mdns.Msg)
	req.SetQuestion("a.example.com.", mdns.TypeA)
	candidates, matched := m.ruleUpstreams(req, m.Config())
	if !matched || len(candidates) != 1 || candidates[0].Name != "two" {
		t.Fatalf("first matching rule must win: %v %v", matched, candidates)
	}
	req.SetQuestion("unrelated.test.", mdns.TypeA)
	_, matched = m.ruleUpstreams(req, m.Config())
	if matched {
		t.Fatal("unrelated domain must use general DNS")
	}
}
func TestRuleDNSRejectsUnknownServer(t *testing.T) {
	m := New(t.TempDir() + "/dns.json")
	cfg := m.Config()
	cfg.RuleDNS = map[string][]string{"r": {"missing"}}
	if err := m.Save(cfg); err == nil {
		t.Fatal("expected validation error")
	}
}
