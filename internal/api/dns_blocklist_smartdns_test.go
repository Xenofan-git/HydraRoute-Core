package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
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
	base := hrsmartdns.Config{Enabled: true, DomainSets: []hrsmartdns.DomainSet{
		{ID: "custom-block", Kind: "block", Domains: []string{"ads-parent.example.test", "blocked.allow-parent.example.test"}, Enabled: true},
		{ID: "custom-allow", Kind: "allow", Domains: []string{"safe.ads-parent.example.test", "allow-parent.example.test"}, Enabled: true},
		{ID: "custom-set", Kind: "route", ProfileID: "foreign", Domains: []string{"tracker.example.test"}, Enabled: true},
	}}
	server := &Server{blocklists: manager, dns: resolver, smartDNSConfig: base}
	server.setSmartDNSPolicyConfig(base)
	server.applyDNSBlocklists()
	for _, host := range []string{"ads.example.test", "blocked.allow-parent.example.test"} {
		if !resolver.IsBlockedHost(host) {
			t.Fatalf("Core DNS policy resolver did not block %s", host)
		}
	}
	for _, host := range []string{"safe.example.test", "safe.ads-parent.example.test", "allow-parent.example.test"} {
		if resolver.IsBlockedHost(host) {
			t.Fatalf("Core DNS policy resolver did not apply allow exception for %s", host)
		}
	}
	if len(server.smartDNSConfig.DomainSets) != 3 || server.smartDNSConfig.DomainSets[2].ID != "custom-set" {
		t.Fatalf("SmartDNS profile-routing domain sets unexpectedly changed: %+v", server.smartDNSConfig.DomainSets)
	}
	server.smartDNSConfig.Enabled = false
	server.setSmartDNSPolicyConfig(server.smartDNSConfig)
	server.applyDNSBlocklists()
	if resolver.IsBlockedHost("blocked.allow-parent.example.test") {
		t.Fatal("disabled SmartDNS configuration still applied its custom block set")
	}
	if !resolver.IsBlockedHost("ads.example.test") {
		t.Fatal("disabling SmartDNS unexpectedly disabled independently managed URL blocklists")
	}
}

func TestSmartDNSForwardAddressDefaultsToLoopbackPort(t *testing.T) {
	if got := smartDNSListenPort(hrsmartdns.Config{}); got != 5354 {
		t.Fatalf("default SmartDNS listen port = %d, want 5354", got)
	}
	if got := smartDNSForwardAddress(hrsmartdns.Config{}); got != "127.0.0.1:5354" {
		t.Fatalf("default SmartDNS forward address = %q, want 127.0.0.1:5354", got)
	}
	if got := smartDNSListenPort(hrsmartdns.Config{ListenPort: 5454}); got != 5454 {
		t.Fatalf("configured SmartDNS listen port = %d, want 5454", got)
	}
	if got := smartDNSForwardAddress(hrsmartdns.Config{ListenPort: 5454}); got != "127.0.0.1:5454" {
		t.Fatalf("configured SmartDNS forward address = %q, want 127.0.0.1:5454", got)
	}
}
func TestSmartDNSStartRequiresRunningCoreDNSPolicyResolver(t *testing.T) {
	stateDir := filepath.Join(t.TempDir(), "smartdns-state")
	if err := os.Mkdir(stateDir, 0700); err != nil {
		t.Fatal(err)
	}
	runtime, err := hrsmartdns.NewRuntime(hrsmartdns.RuntimeConfig{
		BinaryPath: "/bin/true",
		StateDir:   stateDir,
		ListenAddr: "127.0.0.1:5354",
	})
	if err != nil {
		t.Fatal(err)
	}
	server := &Server{
		dns:             dnsservice.New(filepath.Join(t.TempDir(), "dns", "config.json")),
		smartDNSRuntime: runtime,
		smartDNSConfig:  hrsmartdns.DefaultConfig(),
	}
	server.smartDNSConfig.Enabled = true

	req := httptest.NewRequest(http.MethodPost, "http://127.0.0.1:17891/v1/dns/smartdns/start", strings.NewReader("{}"))
	req.RemoteAddr = "127.0.0.1:12345"
	req.Header.Set("Origin", "http://127.0.0.1:17891")
	req.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()
	server.smartDNSAction(recorder, req)

	if recorder.Code != http.StatusConflict || !strings.Contains(recorder.Body.String(), "start the Core DNS policy resolver before SmartDNS") {
		t.Fatalf("SmartDNS start without Core DNS returned status=%d body=%s", recorder.Code, recorder.Body.String())
	}
}
