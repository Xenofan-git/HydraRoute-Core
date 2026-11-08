package main

import (
	"encoding/json"
	"path/filepath"

	"hydraroute.local/core/internal/parser"
	"hydraroute.local/core/internal/state"
	"testing"
)

func TestCompileRequestMultipleProfilesStayIsolated(t *testing.T) {
	req := request{Profiles: []profileRequest{
		{Source: "subscription-A", Profile: profileInput{ID: "foreign", Enabled: true, Port: 16090}, Links: []string{"vless://00000000-0000-4000-8000-000000000001@foreign.example:443#foreign"}},
		{Source: "subscription-B", Profile: profileInput{ID: "torrent", Enabled: true, Port: 16091}, Links: []string{"socks5://user:pass@torrent.example:1080#torrent"}},
	}}
	raw, err := compileRequest(req)
	if err != nil {
		t.Fatal(err)
	}
	var cfg map[string]any
	if err := json.Unmarshal(raw, &cfg); err != nil {
		t.Fatal(err)
	}
	inbounds := cfg["inbounds"].([]any)
	if len(inbounds) != 2 {
		t.Fatalf("inbounds=%v", inbounds)
	}
	if inbounds[0].(map[string]any)["port"] == inbounds[1].(map[string]any)["port"] {
		t.Fatal("profiles share a port")
	}
	routing := cfg["routing"].(map[string]any)
	balancers := routing["balancers"].([]any)
	rules := routing["rules"].([]any)
	if len(balancers) != 2 || len(rules) != 2 {
		t.Fatalf("balancers=%v rules=%v", balancers, rules)
	}
	outbounds := cfg["outbounds"].([]any)
	found := map[string]bool{}
	for _, item := range outbounds {
		if obj, ok := item.(map[string]any); ok {
			if protocol, ok := obj["protocol"].(string); ok {
				found[protocol] = true
			}
		}
	}
	if !found["vless"] || !found["socks"] {
		t.Fatalf("protocol outbounds=%v", found)
	}
}

func TestCompileRequestRejectsMixedLegacyAndProfiles(t *testing.T) {
	_, err := compileRequest(request{Source: "old", Profiles: []profileRequest{{Source: "new", Profile: profileInput{ID: "new", Enabled: true, Port: 16092}, Links: []string{"vless://00000000-0000-4000-8000-000000000001@edge.example:443#node"}}}})
	if err == nil {
		t.Fatal("expected mixed request rejection")
	}
}

func TestCompileRequestSupportsLegacySingleProfile(t *testing.T) {
	req := request{Source: "legacy", Profile: profileInput{ID: "legacy", Enabled: true, Port: 16093}, Links: []string{"vless://00000000-0000-4000-8000-000000000001@edge.example:443#node"}}
	raw, err := compileRequest(req)
	if err != nil {
		t.Fatal(err)
	}
	if len(raw) == 0 {
		t.Fatal("empty config")
	}
}

func TestCompileRequestRejectsUnsafeSubscriptionURL(t *testing.T) {
	req := request{Profiles: []profileRequest{{Source: "unsafe", Profile: profileInput{ID: "unsafe", Enabled: true, Port: 16094}, SubscriptionURL: "http://127.0.0.1/sub"}}}
	if _, err := compileRequest(req); err == nil {
		t.Fatal("expected private subscription URL rejection")
	}
}

func TestCompileRequestRejectsMultipleSubscriptionInputs(t *testing.T) {
	req := request{Profiles: []profileRequest{{Source: "duplicate", Profile: profileInput{ID: "duplicate", Enabled: true, Port: 16095}, Subscription: "abc", SubscriptionURL: "https://provider.example/sub"}}}
	if _, err := compileRequest(req); err == nil {
		t.Fatal("expected duplicate subscription input rejection")
	}
}

func TestCompileRequestAppliesPersistedManualSelection(t *testing.T) {
	links := []string{
		"vless://00000000-0000-4000-8000-000000000001@a.example:443#A",
		"vless://00000000-0000-4000-8000-000000000001@b.example:443#B",
	}
	parsed, err := parser.ParseLinks("src", links)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	statePath := filepath.Join(dir, "private", "state.json")
	saved := state.State{Version: state.CurrentVersion, Profiles: []state.Profile{{ID: "foreign", SourceID: "src", PickMode: "manual", Picked: []parser.ServerKey{parsed[0].Key}, Excluded: []parser.ServerKey{parsed[1].Key}}}}
	if err := state.Save(statePath, saved); err != nil {
		t.Fatal(err)
	}
	req := request{Profiles: []profileRequest{{Source: "src", Profile: profileInput{ID: "foreign", Enabled: true, Port: 16096}, Links: links, StateFile: statePath}}}
	raw, err := compileRequest(req)
	if err != nil {
		t.Fatal(err)
	}
	var cfg map[string]any
	if err := json.Unmarshal(raw, &cfg); err != nil {
		t.Fatal(err)
	}
	outbounds := cfg["outbounds"].([]any)
	vlessCount := 0
	for _, item := range outbounds {
		if obj, ok := item.(map[string]any); ok && obj["protocol"] == "vless" {
			vlessCount++
		}
	}
	if vlessCount != 1 {
		t.Fatalf("expected one selected VLESS outbound, got %d", vlessCount)
	}
}

func TestCompileRequestFailsClosedWhenManualSelectionDisappears(t *testing.T) {
	dir := t.TempDir()
	statePath := filepath.Join(dir, "private", "state.json")
	saved := state.State{Version: state.CurrentVersion, Profiles: []state.Profile{{ID: "foreign", SourceID: "src", PickMode: "manual", Picked: []parser.ServerKey{{Source: "src", Name: "old-node"}}}}}
	if err := state.Save(statePath, saved); err != nil {
		t.Fatal(err)
	}
	req := request{Profiles: []profileRequest{{Source: "src", Profile: profileInput{ID: "foreign", Enabled: true, Port: 16097}, Links: []string{"vless://00000000-0000-4000-8000-000000000001@new.example:443#new"}, StateFile: statePath}}}
	if _, err := compileRequest(req); err == nil {
		t.Fatal("expected stale manual selection to fail closed")
	}
}

func TestCompileRequestRejectsInlineSelectionWithStateFile(t *testing.T) {
	req := request{Profiles: []profileRequest{{Source: "src", Profile: profileInput{ID: "foreign", Enabled: true, Port: 16098, Picked: []string{"inline"}}, Links: []string{"vless://00000000-0000-4000-8000-000000000001@new.example:443#new"}, StateFile: "/tmp/state"}}}
	if _, err := compileRequest(req); err == nil {
		t.Fatal("expected conflicting selection input rejection")
	}
}
