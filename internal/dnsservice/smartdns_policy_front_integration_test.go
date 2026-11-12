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
	if blocked.Rcode != mdns.RcodeSuccess || len(blocked.Answer) != 0 || len(blocked.Ns) != 1 {
		t.Fatalf("Core policy front did not return an HRDNS-style SOA for child block under allow parent: rcode=%d answers=%v authority=%v", blocked.Rcode, blocked.Answer, blocked.Ns)
	}
	soa, ok := blocked.Ns[0].(*mdns.SOA)
	if !ok || soa.Hdr.Ttl != 30 || soa.Ns != "a.gtld-servers.net." || soa.Mbox != "nstld.verisign-grs.com." {
		t.Fatalf("Core policy front returned a non-HRDNS SOA: %+v", blocked.Ns[0])
	}

	// Validate HRDNS managed-list blocking on every supported RR type over
	// both client transports. TXT must not be intercepted by the blocklist.
	for _, network := range []string{"udp", "tcp"} {
		transportClient := &mdns.Client{Net: network, Timeout: 5 * time.Second}
		for _, rrType := range []uint16{mdns.TypeA, mdns.TypeAAAA, mdns.TypeSVCB, mdns.TypeHTTPS} {
			question := new(mdns.Msg)
			question.SetQuestion("blocked.allow-parent.example.test.", rrType)
			response, _, err := transportClient.Exchange(question, resolver)
			if err != nil {
				t.Fatalf("%s blocked %s query: %v", network, mdns.TypeToString[rrType], err)
			}
			if response.Rcode != mdns.RcodeSuccess || len(response.Answer) != 0 || len(response.Ns) != 1 {
				t.Fatalf("%s blocked %s: expected SOA-only response, got rcode=%d answers=%v authority=%v", network, mdns.TypeToString[rrType], response.Rcode, response.Answer, response.Ns)
			}
			if soa, ok := response.Ns[0].(*mdns.SOA); !ok || soa.Hdr.Ttl != 30 || soa.Ns != "a.gtld-servers.net." || soa.Mbox != "nstld.verisign-grs.com." {
				t.Fatalf("%s blocked %s: unexpected SOA %v", network, mdns.TypeToString[rrType], response.Ns[0])
			}
		}
	}

	// TCP clients must traverse the same Core policy front and profile-pinned
	// SmartDNS forwarding path as UDP clients.
	tcpClient := &mdns.Client{Net: "tcp", Timeout: 5 * time.Second}
	tcpQuestion := new(mdns.Msg)
	tcpQuestion.SetQuestion("policy.safe.ads.example.test.", mdns.TypeA)
	tcpAllowed, _, err := tcpClient.Exchange(tcpQuestion, resolver)
	if err != nil {
		t.Fatalf("TCP allowed child query: %v", err)
	}
	if got := answerA(tcpAllowed); got != "203.0.113.77" {
		t.Fatalf("TCP allowed child did not reach SmartDNS: answer=%q rcode=%d", got, tcpAllowed.Rcode)
	}

	allowed, err := queryA("policy.safe.ads.example.test")
	if err != nil {
		t.Fatalf("query more-specific allow under parent block: %v", err)
	}
	if got := answerA(allowed); got != "203.0.113.77" {
		t.Fatalf("child allow did not pass through to SmartDNS: answer=%q rcode=%d", got, allowed.Rcode)
	}

	for _, name := range []string{"policy-login.foreign.test", "policy-tracker.torrent.test"} {
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
