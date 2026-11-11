package dnsservice

import (
	"net"
	"os"
	"strconv"
	"testing"
	"time"

	mdns "github.com/miekg/dns"
)

// TestPolicyFrontAgainstRunningSmartDNS is invoked by the pinned SmartDNS
// integration harness while its real SmartDNS process and SOCKS/DoH fixtures
// are running. It is skipped in ordinary Core CI.
func TestPolicyFrontAgainstRunningSmartDNS(t *testing.T) {
	upstream := os.Getenv("HRCORE_SMARTDNS_TEST_ADDR")
	if upstream == "" {
		t.Skip("HRCORE_SMARTDNS_TEST_ADDR is only set by the pinned SmartDNS integration harness")
	}

	manager := New(t.TempDir() + "/dns.json")
	cfg := manager.Config()
	cfg.Enabled = true
	cfg.Listen = "127.0.0.1"
	cfg.Port = freeDNSPort(t)
	cfg.BlockingEnabled = true
	if err := manager.Save(cfg); err != nil {
		t.Fatal(err)
	}
	manager.SetBlocklistDomains(
		[]string{"ads.example.test", "blocked.allow-parent.example.test"},
		[]string{"safe.ads.example.test", "allow-parent.example.test"},
	)
	if err := manager.SetSmartDNSForwarder(upstream); err != nil {
		t.Fatal(err)
	}
	if err := manager.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = manager.Stop() }()

	client := &mdns.Client{Net: "udp", Timeout: 5 * time.Second}
	resolver := net.JoinHostPort("127.0.0.1", strconv.Itoa(cfg.Port))
	queryA := func(name string) (*mdns.Msg, error) {
		query := new(mdns.Msg)
		query.SetQuestion(mdns.Fqdn(name), mdns.TypeA)
		response, _, err := client.Exchange(query, resolver)
		return response, err
	}
	answerA := func(response *mdns.Msg) string {
		if response == nil {
			return ""
		}
		for _, answer := range response.Answer {
			if record, ok := answer.(*mdns.A); ok {
				return record.A.String()
			}
		}
		return ""
	}

	blocked, err := queryA("blocked.allow-parent.example.test")
	if err != nil {
		t.Fatalf("query more-specific block under parent allow: %v", err)
	}
	if got := answerA(blocked); got != "0.0.0.0" {
		t.Fatalf("Core policy front did not block child under allow parent: answer=%q rcode=%d", got, blocked.Rcode)
	}

	allowed, err := queryA("safe.ads.example.test")
	if err != nil {
		t.Fatalf("query more-specific allow under parent block: %v", err)
	}
	if got := answerA(allowed); got != "203.0.113.77" {
		t.Fatalf("child allow did not pass through to SmartDNS: answer=%q rcode=%d", got, allowed.Rcode)
	}

	for _, name := range []string{"login.foreign.test", "tracker.torrent.test"} {
		response, err := queryA(name)
		if err != nil {
			t.Fatalf("profile-routed query %s: %v", name, err)
		}
		if got := answerA(response); got != "203.0.113.77" {
			t.Fatalf("profile-routed query %s returned %q (rcode=%d)", name, got, response.Rcode)
		}
	}

	unmatched, err := queryA("unmatched.test")
	if err != nil {
		t.Fatalf("unmatched fail-closed query: %v", err)
	}
	if got := answerA(unmatched); got == "203.0.113.77" {
		t.Fatal("unmatched query escaped SmartDNS default fail-closed group")
	}
}
