package service

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"hydraroute.local/core/internal/compiler"
	"hydraroute.local/core/internal/parser"
	"hydraroute.local/core/internal/state"
)

func TestCompileSubscriptionsIsolatesProfiles(t *testing.T) {
	req := SubscriptionRequest{Profiles: []ProfileRequest{
		{Source: "foreign", Profile: ProfileInput{ID: "foreign", Enabled: true, Port: 16110}, Links: []string{"vless://00000000-0000-4000-8000-000000000001@foreign.example:443#foreign"}},
		{Source: "torrent", Profile: ProfileInput{ID: "torrent", Enabled: true, Port: 16111}, Links: []string{"socks5://user:pass@torrent.example:1080#torrent"}},
	}}
	result, err := CompileSubscriptions(context.Background(), req, filepath.Join(t.TempDir(), "state"))
	if err != nil {
		t.Fatal(err)
	}
	if result.ProfileCount != 2 || result.NodeCount != 2 {
		t.Fatalf("result=%+v", result)
	}
	var cfg map[string]any
	if err := json.Unmarshal(result.Config, &cfg); err != nil {
		t.Fatal(err)
	}
	if got := len(cfg["inbounds"].([]any)); got != 2 {
		t.Fatalf("inbounds=%d", got)
	}
}

func TestCompileSubscriptionsAppliesSharedRouting(t *testing.T) {
	req := SubscriptionRequest{
		Profiles: []ProfileRequest{{Source: "foreign", Profile: ProfileInput{ID: "foreign", Enabled: true, Port: 16140},
			Links: []string{"vless://00000000-0000-4000-8000-000000000001@foreign.example:443#foreign"}}},
		Routing: &compiler.RoutingConfig{Enabled: true, Port: 16141, DefaultAction: "block",
			Rules: []compiler.RoutingRule{{ID: "local", Name: "Local domains", Enabled: true, MatchType: "domain", Values: []string{"domain:local.example"}, Action: "direct"}}},
	}
	result, err := CompileSubscriptions(context.Background(), req, filepath.Join(t.TempDir(), "state"))
	if err != nil {
		t.Fatal(err)
	}
	var cfg map[string]any
	if err := json.Unmarshal(result.Config, &cfg); err != nil {
		t.Fatal(err)
	}
	if len(cfg["inbounds"].([]any)) != 2 {
		t.Fatalf("expected shared and profile inbounds, got %d", len(cfg["inbounds"].([]any)))
	}
	if len(cfg["routing"].(map[string]any)["rules"].([]any)) != 3 {
		t.Fatal("expected profile, user and fallback rules")
	}
}

func TestPersistedSelectionAppliedAndManualFailsClosed(t *testing.T) {
	links := []string{
		"vless://00000000-0000-4000-8000-000000000001@a.example:443#A",
		"vless://00000000-0000-4000-8000-000000000001@b.example:443#B",
	}
	servers, err := parser.ParseLinks("providerA", links)
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(t.TempDir(), "state")
	profile := state.Profile{ID: "foreign", SourceID: "providerA", PickMode: "manual", Picked: []parser.ServerKey{servers[0].Key}, Excluded: []parser.ServerKey{servers[1].Key}}
	if err := SaveSelection(dir, profile); err != nil {
		t.Fatal(err)
	}
	result, err := CompileSubscriptions(context.Background(), SubscriptionRequest{Source: "providerA", Profile: ProfileInput{ID: "foreign", Enabled: true, Port: 16112}, Links: links}, dir)
	if err != nil {
		t.Fatal(err)
	}
	var cfg map[string]any
	if err := json.Unmarshal(result.Config, &cfg); err != nil {
		t.Fatal(err)
	}
	vless := 0
	for _, item := range cfg["outbounds"].([]any) {
		if m, ok := item.(map[string]any); ok && m["protocol"] == "vless" {
			vless++
		}
	}
	if vless != 1 {
		t.Fatalf("expected one selected VLESS outbound, got %d", vless)
	}
	_, err = CompileSubscriptions(context.Background(), SubscriptionRequest{Source: "providerA", Profile: ProfileInput{ID: "foreign", Enabled: true, Port: 16112}, Links: []string{"vless://00000000-0000-4000-8000-000000000001@other.example:443#other"}}, dir)
	if err == nil {
		t.Fatal("manual selection must fail closed when selected node disappears")
	}
}

func TestSaveSelectionRejectsUnsafeDirectoryAndIdentity(t *testing.T) {
	parent := t.TempDir()
	openDir := filepath.Join(parent, "open")
	if err := os.Mkdir(openDir, 0755); err != nil {
		t.Fatal(err)
	}
	p := state.Profile{ID: "profile", SourceID: "source", PickMode: "auto"}
	if err := SaveSelection(openDir, p); err == nil {
		t.Fatal("expected permissive directory rejection")
	}
	if err := SaveSelection(filepath.Join(parent, "private"), state.Profile{ID: "../escape", SourceID: "source", PickMode: "auto"}); err == nil {
		t.Fatal("expected unsafe profile ID rejection")
	}
}

func TestCompileSubscriptionsRejectsUnsafeProfileIDBeforeStatePath(t *testing.T) {
	_, err := CompileSubscriptions(context.Background(), SubscriptionRequest{Source: "source", Profile: ProfileInput{ID: "../escape", Enabled: true, Port: 16113}, Links: []string{"vless://00000000-0000-4000-8000-000000000001@edge.example:443#edge"}}, t.TempDir())
	if err == nil {
		t.Fatal("expected invalid profile ID rejection")
	}
}

func TestCompileSubscriptionsReportsAllSourceFailuresByProfile(t *testing.T) {
	_, err := CompileSubscriptions(context.Background(), SubscriptionRequest{Profiles: []ProfileRequest{
		{Source: "providerA", Profile: ProfileInput{ID: "foreign", Enabled: true, Port: 16170}, Links: []string{"unknown://invalid"}},
		{Source: "providerB", Profile: ProfileInput{ID: "torrent", Enabled: true, Port: 16171}, Links: []string{"unknown://invalid"}},
	}}, filepath.Join(t.TempDir(), "state"))
	refreshErr, ok := err.(*RefreshError)
	if !ok {
		t.Fatalf("expected RefreshError, got %T %v", err, err)
	}
	if len(refreshErr.Failures) != 2 || refreshErr.Failures[0].ProfileID != "foreign" || refreshErr.Failures[1].ProfileID != "torrent" {
		t.Fatalf("failures=%+v", refreshErr.Failures)
	}
	if strings.Contains(err.Error(), "unknown://") {
		t.Fatal("source credentials/raw links must not appear in refresh errors")
	}
}
