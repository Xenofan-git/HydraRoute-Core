package api

import (
	"context"
	"path/filepath"
	"testing"

	dnsservice "hydraroute.local/core/internal/dnsservice"
	dnsblocklist "hydraroute.local/core/internal/dnsservice/blocklist"
	hrsmartdns "hydraroute.local/core/internal/dnsservice/smartdns"
)

func TestBlocklistsStayInCorePolicyResolverAndDoNotMutateSmartDNSConfig(t *testing.T) {
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

	resolver := dnsservice.New(filepath.Join(t.TempDir(), "dns", "config.json"))
	resolverConfig := resolver.Config()
	resolverConfig.BlockingEnabled = true
	if err := resolver.Save(resolverConfig); err != nil {
		t.Fatal(err)
	}
	server := &Server{blocklists: manager, dns: resolver}
	server.applyDNSBlocklists()
	if !resolver.IsBlockedHost("ads.example.test") {
		t.Fatal("Core DNS policy resolver did not receive blocklist entries")
	}
	if resolver.IsBlockedHost("safe.example.test") {
		t.Fatal("Core DNS policy resolver did not receive allowlist exceptions")
	}

	base := hrsmartdns.Config{DomainSets: []hrsmartdns.DomainSet{
		{ID: "custom-set", Kind: "route", ProfileID: "foreign", Domains: []string{"tracker.example.test"}, Enabled: true},
	}}
	if len(base.DomainSets) != 1 || base.DomainSets[0].ID != "custom-set" {
		t.Fatalf("SmartDNS profile-routing domain sets unexpectedly changed: %+v", base.DomainSets)
	}
}

func TestSmartDNSForwardAddressDefaultsToLoopbackPort(t *testing.T) {
	if got := smartDNSForwardAddress(hrsmartdns.Config{}); got != "127.0.0.1:5354" {
		t.Fatalf("default SmartDNS forward address = %q, want 127.0.0.1:5354", got)
	}
	if got := smartDNSForwardAddress(hrsmartdns.Config{ListenPort: 5454}); got != "127.0.0.1:5454" {
		t.Fatalf("configured SmartDNS forward address = %q, want 127.0.0.1:5454", got)
	}
}
