package api

import (
	"context"
	"path/filepath"
	"testing"

	dnsblocklist "hydraroute.local/core/internal/dnsservice/blocklist"
	hrsmartdns "hydraroute.local/core/internal/dnsservice/smartdns"
)

func TestSmartDNSConfigWithBlocklistsPreservesCustomSetsAndReplacesManagedSets(t *testing.T) {
	manager, err := dnsblocklist.NewManager(filepath.Join(t.TempDir(), "blocklists.json"), func(context.Context, string) (dnsblocklist.Result, error) {
		return dnsblocklist.Result{
			Blocked: []string{"ads.example.test"},
			Allowed: []string{"safe.example.test"},
		}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	source, err := manager.SaveSource(dnsblocklist.Source{Name: "Test", URL: "https://lists.example.test/domains", Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Refresh(context.Background(), source.ID); err != nil {
		t.Fatal(err)
	}

	server := &Server{blocklists: manager}
	base := hrsmartdns.Config{DomainSets: []hrsmartdns.DomainSet{
		{ID: "custom-set", Kind: "route", ProfileID: "foreign", Domains: []string{"tracker.example.test"}, Enabled: true},
		{ID: "hrcore-blocklist", Kind: "block", Domains: []string{"stale.example.test"}, Enabled: true},
		{ID: "hrcore-allowlist", Kind: "allow", Domains: []string{"stale-safe.example.test"}, Enabled: true},
	}}
	got := server.smartDNSConfigWithBlocklists(base)
	if len(got.DomainSets) != 3 {
		t.Fatalf("got %d domain sets, want custom + managed block + managed allow: %+v", len(got.DomainSets), got.DomainSets)
	}
	byID := make(map[string]hrsmartdns.DomainSet, len(got.DomainSets))
	for _, set := range got.DomainSets {
		byID[set.ID] = set
	}
	if _, ok := byID["custom-set"]; !ok {
		t.Fatal("custom SmartDNS domain set was lost")
	}
	if got := byID["hrcore-blocklist"].Domains; len(got) != 1 || got[0] != "ads.example.test" {
		t.Fatalf("managed blocklist set was not refreshed: %v", got)
	}
	if got := byID["hrcore-allowlist"].Domains; len(got) != 1 || got[0] != "safe.example.test" {
		t.Fatalf("managed allowlist set was not refreshed: %v", got)
	}
}
