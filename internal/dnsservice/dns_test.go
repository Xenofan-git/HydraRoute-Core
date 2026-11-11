package dnsservice

import (
	"net"
	"path/filepath"
	"strconv"
	"sync/atomic"
	"testing"
	"time"

	mdns "github.com/miekg/dns"
)

func TestDefaultConfigUsesSafeLoopbackAndKnownResolvers(t *testing.T) {
	c := DefaultConfig()
	if c.Listen != "127.0.0.1" || c.Port != 5353 {
		t.Fatalf("unsafe default listener: %s:%d", c.Listen, c.Port)
	}
	if !c.AutoProvider || len(c.Servers) < 3 {
		t.Fatalf("automatic provider discovery or resolver presets missing: %+v", c)
	}
}
func TestSaveRejectsNonLoopbackListener(t *testing.T) {
	m := New(filepath.Join(t.TempDir(), "dns", "config.json"))
	c := m.Config()
	c.Listen = "0.0.0.0"
	if err := m.Save(c); err == nil {
		t.Fatal("expected non-loopback listener to be rejected")
	}
}
func TestSavePersistsProviderAndHosts(t *testing.T) {
	m := New(filepath.Join(t.TempDir(), "dns", "config.json"))
	c := m.Config()
	c.Hosts = []Host{{Domain: "local.test", Address: "192.0.2.10"}}
	c.DefaultProvider = "Yandex DNS · Basic"
	if err := m.Save(c); err != nil {
		t.Fatal(err)
	}
	n := New(m.path)
	if n.Config().DefaultProvider != c.DefaultProvider || len(n.Config().Hosts) != 1 {
		t.Fatalf("configuration did not persist: %+v", n.Config())
	}
}

func TestParseDHCPDNSServersOnlyReadsDHCPDNSOption(t *testing.T) {
	got := parseDHCPDNSServers("" +
		"ip_address = 192.168.4.26\n" +
		"domain_name_servers = 192.168.4.1, 2001:db8::1\n" +
		"routers = 192.168.4.1\n")
	if len(got) != 2 || got[0] != "192.168.4.1" || got[1] != "2001:db8::1" {
		t.Fatalf("unexpected DHCP DNS parse: %#v", got)
	}
}

func TestParseDHCPDNSServersIgnoresInvalidAndDuplicates(t *testing.T) {
	got := parseDHCPDNSServers("domain_name_servers = 127.0.0.1, 192.0.2.53, 192.0.2.53, invalid")
	if len(got) != 1 || got[0] != "192.0.2.53" {
		t.Fatalf("unexpected DHCP DNS parse: %#v", got)
	}
}

func TestDefaultRouteInterfaceFromProcChoosesLowestMetric(t *testing.T) {
	data := "Iface Destination Gateway Flags RefCnt Use Metric Mask MTU Window IRTT\n" +
		"eth1 00000000 0104A8C0 0003 0 0 200 00000000 0 0 0\n" +
		"eth0 00000000 0104A8C0 0003 0 0 100 00000000 0 0 0\n" +
		"tailscale0 00000000 00000000 0001 0 0 50 00000000 0 0 0\n"
	if got := defaultRouteInterfaceFromProc(data); got != "eth0" {
		t.Fatalf("expected lowest-metric default route eth0, got %q", got)
	}
}

func TestDefaultConfigHasSafeLocalDNSListener(t *testing.T) {
	c := DefaultConfig()
	if c.Enabled {
		t.Fatal("DNS service must remain disabled by default")
	}
	if c.Listen != "127.0.0.1" || c.Port != 5353 {
		t.Fatalf("unexpected DNS bind: %s:%d", c.Listen, c.Port)
	}
}

func freeDNSPort(t *testing.T) int {
	t.Helper()
	pc, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := pc.LocalAddr().(*net.UDPAddr).Port
	_ = pc.Close()
	ln, err := net.Listen("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(port)))
	if err != nil {
		t.Fatal(err)
	}
	_ = ln.Close()
	return port
}

func startFakeDNSUpstream(t *testing.T, answerIP string, hits *atomic.Int32) string {
	t.Helper()
	pc, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	mux := mdns.NewServeMux()
	mux.HandleFunc(".", func(w mdns.ResponseWriter, req *mdns.Msg) {
		if hits != nil {
			hits.Add(1)
		}
		resp := new(mdns.Msg)
		resp.SetReply(req)
		if len(req.Question) > 0 {
			var record string
			switch req.Question[0].Qtype {
			case mdns.TypeA:
				record = req.Question[0].Name + " 30 IN A " + answerIP
			case mdns.TypeTXT:
				record = req.Question[0].Name + " 30 IN TXT \"passed-through\""
			}
			if record != "" {
				rr, parseErr := mdns.NewRR(record)
				if parseErr != nil {
					t.Errorf("build fake DNS answer: %v", parseErr)
					return
				}
				resp.Answer = append(resp.Answer, rr)
			}
		}
		_ = w.WriteMsg(resp)
	})
	server := &mdns.Server{PacketConn: pc, Handler: mux}
	go func() { _ = server.ActivateAndServe() }()
	t.Cleanup(func() { _ = server.Shutdown() })
	return pc.LocalAddr().String()
}

func TestDNSPolicyFrontBlocksBeforeSmartDNSAndForwardsAllowedQueries(t *testing.T) {
	var smartDNSHits atomic.Int32
	smartDNSAddr := startFakeDNSUpstream(t, "203.0.113.77", &smartDNSHits)
	m := New(filepath.Join(t.TempDir(), "dns", "config.json"))
	cfg := m.Config()
	cfg.Port = freeDNSPort(t)
	cfg.BlockingEnabled = true
	if err := m.Save(cfg); err != nil {
		t.Fatal(err)
	}
	m.SetBlocklistDomains(
		[]string{"blocked.allow-parent.test"},
		[]string{"allow-parent.test"},
	)
	if err := m.SetSmartDNSForwarder(smartDNSAddr); err != nil {
		t.Fatal(err)
	}
	if err := m.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = m.Stop() }()

	client := &mdns.Client{Net: "udp", Timeout: 2 * time.Second}
	blockedQuery := new(mdns.Msg)
	blockedQuery.SetQuestion("blocked.allow-parent.test.", mdns.TypeA)
	blockedResponse, _, err := client.Exchange(blockedQuery, net.JoinHostPort("127.0.0.1", strconv.Itoa(cfg.Port)))
	if err != nil {
		t.Fatalf("blocked query: %v", err)
	}
	if len(blockedResponse.Answer) != 1 || blockedResponse.Answer[0].(*mdns.A).A.String() != "0.0.0.0" {
		t.Fatalf("more-specific block was not enforced before SmartDNS: %+v", blockedResponse.Answer)
	}
	if smartDNSHits.Load() != 0 {
		t.Fatalf("blocked query reached SmartDNS %d times", smartDNSHits.Load())
	}

	allowedQuery := new(mdns.Msg)
	allowedQuery.SetQuestion("other.allow-parent.test.", mdns.TypeA)
	allowedResponse, _, err := client.Exchange(allowedQuery, net.JoinHostPort("127.0.0.1", strconv.Itoa(cfg.Port)))
	if err != nil {
		t.Fatalf("allowed query: %v", err)
	}
	if len(allowedResponse.Answer) != 1 || allowedResponse.Answer[0].(*mdns.A).A.String() != "203.0.113.77" {
		t.Fatalf("allow-parent query was not forwarded to SmartDNS: %+v", allowedResponse.Answer)
	}
	if smartDNSHits.Load() != 1 {
		t.Fatalf("SmartDNS received %d queries, want exactly one", smartDNSHits.Load())
	}

	// HRDNS applies managed blocklists to A/AAAA/SVCB/HTTPS records, not TXT.
	// A TXT lookup for the same blocked hostname must still reach SmartDNS.
	txtQuery := new(mdns.Msg)
	txtQuery.SetQuestion("blocked.allow-parent.test.", mdns.TypeTXT)
	txtResponse, _, err := client.Exchange(txtQuery, net.JoinHostPort("127.0.0.1", strconv.Itoa(cfg.Port)))
	if err != nil {
		t.Fatalf("TXT query for blocked host: %v", err)
	}
	if len(txtResponse.Answer) != 1 || txtResponse.Answer[0].Header().Rrtype != mdns.TypeTXT {
		t.Fatalf("TXT query was incorrectly blocked: %+v", txtResponse.Answer)
	}
	if smartDNSHits.Load() != 2 {
		t.Fatalf("SmartDNS received %d queries after TXT pass-through, want two", smartDNSHits.Load())
	}
}

func TestBlocklistAppliesOnlyToHRDNSRecordTypes(t *testing.T) {
	m := &Manager{cfg: Config{BlockingEnabled: true}}
	m.SetBlocklistDomains([]string{"blocked.example.test"}, nil)
	cases := []struct {
		name  string
		qtype uint16
		want  bool
	}{
		{name: "A", qtype: mdns.TypeA, want: true},
		{name: "AAAA", qtype: mdns.TypeAAAA, want: true},
		{name: "SVCB", qtype: mdns.TypeSVCB, want: true},
		{name: "HTTPS", qtype: mdns.TypeHTTPS, want: true},
		{name: "TXT", qtype: mdns.TypeTXT, want: false},
		{name: "MX", qtype: mdns.TypeMX, want: false},
		{name: "CNAME", qtype: mdns.TypeCNAME, want: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := m.blockedHostForType("blocked.example.test", tc.qtype); got != tc.want {
				t.Fatalf("blockedHostForType(%s)=%v, want %v", tc.name, got, tc.want)
			}
		})
	}
}

func TestExplicitDNSRulesRemainIndependentOfBlocklistRecordTypes(t *testing.T) {
	m := &Manager{cfg: Config{
		BlockingEnabled: true,
		Rules:           []Rule{{Domain: "explicit.example.test", Action: "block"}},
	}}
	if !m.blockedHostForType("explicit.example.test", mdns.TypeTXT) {
		t.Fatal("explicit Core DNS rule unexpectedly inherited the blocklist record-type filter")
	}
}

func TestSmartDNSForwarderIsExclusiveAndSkipsConfiguredFallbacks(t *testing.T) {
	var smartDNSHits, fallbackHits atomic.Int32
	smartDNSAddr := startFakeDNSUpstream(t, "203.0.113.77", &smartDNSHits)
	fallbackAddr := startFakeDNSUpstream(t, "198.51.100.88", &fallbackHits)
	m := &Manager{cfg: Config{
		AutoProvider: true,
		Servers:      []Server{{Name: "Public fallback", Protocol: "Do53", Address: fallbackAddr, Enabled: true}},
	}}
	if err := m.SetSmartDNSForwarder(smartDNSAddr); err != nil {
		t.Fatal(err)
	}
	query := new(mdns.Msg)
	query.SetQuestion("routed.example.test.", mdns.TypeA)
	response := new(mdns.Msg)
	if err := m.forward(query, response); err != nil {
		t.Fatalf("forward through SmartDNS: %v", err)
	}
	if len(response.Answer) != 1 || response.Answer[0].(*mdns.A).A.String() != "203.0.113.77" {
		t.Fatalf("unexpected SmartDNS response: %+v", response.Answer)
	}
	if smartDNSHits.Load() != 1 || fallbackHits.Load() != 0 {
		t.Fatalf("SmartDNS/fallback hit counts = %d/%d, want 1/0", smartDNSHits.Load(), fallbackHits.Load())
	}
}

func TestSmartDNSForwarderFailureDoesNotFallBackToPublicDNS(t *testing.T) {
	var fallbackHits atomic.Int32
	fallbackAddr := startFakeDNSUpstream(t, "198.51.100.88", &fallbackHits)
	m := &Manager{cfg: Config{
		Servers: []Server{{Name: "Public fallback", Protocol: "Do53", Address: fallbackAddr, Enabled: true}},
	}}
	unusedAddr := net.JoinHostPort("127.0.0.1", strconv.Itoa(freeDNSPort(t)))
	if err := m.SetSmartDNSForwarder(unusedAddr); err != nil {
		t.Fatal(err)
	}
	query := new(mdns.Msg)
	query.SetQuestion("routed.example.test.", mdns.TypeA)
	if err := m.forward(query, new(mdns.Msg)); err == nil {
		t.Fatal("expected unavailable SmartDNS upstream to fail")
	}
	if got := fallbackHits.Load(); got != 0 {
		t.Fatalf("public fallback received %d queries after SmartDNS failure", got)
	}
}

func TestSetSmartDNSForwarderRequiresLoopback(t *testing.T) {
	m := &Manager{}
	if err := m.SetSmartDNSForwarder("8.8.8.8:5354"); err == nil {
		t.Fatal("expected non-loopback SmartDNS forwarder to be rejected")
	}
	if err := m.SetSmartDNSForwarder("127.0.0.1:5354"); err != nil {
		t.Fatalf("valid loopback SmartDNS forwarder rejected: %v", err)
	}
	if err := m.SetSmartDNSForwarder("127.0.0.1:53"); err != nil {
		t.Fatalf("valid privileged loopback SmartDNS port rejected: %v", err)
	}
	if err := m.SetSmartDNSForwarder(""); err != nil {
		t.Fatalf("clear SmartDNS forwarder: %v", err)
	}
}

func TestStopClosesUDPAndTCPListenersAndAllowsRestart(t *testing.T) {
	m := New(filepath.Join(t.TempDir(), "dns", "config.json"))
	cfg := m.Config()
	cfg.Port = freeDNSPort(t)
	if err := m.Save(cfg); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		if err := m.Start(); err != nil {
			t.Fatalf("start %d: %v", i, err)
		}
		if !m.Status()["running"].(bool) {
			t.Fatalf("service did not report running on start %d", i)
		}
		if err := m.Stop(); err != nil {
			t.Fatalf("stop %d: %v", i, err)
		}
		if m.Status()["running"].(bool) {
			t.Fatalf("service still reports running after stop %d", i)
		}
		udp, err := net.ListenPacket("udp", net.JoinHostPort("127.0.0.1", strconv.Itoa(cfg.Port)))
		if err != nil {
			t.Fatalf("UDP listener leaked after stop %d: %v", i, err)
		}
		_ = udp.Close()
		tcp, err := net.Listen("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(cfg.Port)))
		if err != nil {
			t.Fatalf("TCP listener leaked after stop %d: %v", i, err)
		}
		_ = tcp.Close()
	}
}

func TestUDPServeFailureStopsTCPListener(t *testing.T) {
	m := New(filepath.Join(t.TempDir(), "dns", "config.json"))
	cfg := m.Config()
	cfg.Port = freeDNSPort(t)
	if err := m.Save(cfg); err != nil {
		t.Fatal(err)
	}
	if err := m.Start(); err != nil {
		t.Fatal(err)
	}
	m.mu.RLock()
	pc := m.udp.PacketConn
	m.mu.RUnlock()
	if err := pc.Close(); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if !m.Status()["running"].(bool) {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if m.Status()["running"].(bool) {
		t.Fatal("service remained running after UDP listener failure")
	}
	if err := m.Stop(); err != nil {
		t.Fatal(err)
	}
	tcp, err := net.Listen("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(cfg.Port)))
	if err != nil {
		t.Fatalf("TCP listener leaked after UDP failure: %v", err)
	}
	_ = tcp.Close()
}

func TestBlockedDomainNeverFallsThroughToUpstreamForAOrAAAA(t *testing.T) {
	upstreamPacket, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	var upstreamQueries atomic.Int32
	upstream := &mdns.Server{PacketConn: upstreamPacket, Handler: mdns.HandlerFunc(func(w mdns.ResponseWriter, req *mdns.Msg) {
		upstreamQueries.Add(1)
		reply := new(mdns.Msg)
		reply.SetReply(req)
		for _, question := range req.Question {
			if question.Qtype == mdns.TypeAAAA {
				rr, _ := mdns.NewRR(question.Name + " 60 IN AAAA 2001:db8::10")
				reply.Answer = append(reply.Answer, rr)
			}
		}
		_ = w.WriteMsg(reply)
	})}
	upstreamDone := make(chan struct{})
	go func() { _ = upstream.ActivateAndServe(); close(upstreamDone) }()
	defer func() {
		_ = upstream.Shutdown()
		select {
		case <-upstreamDone:
		case <-time.After(time.Second):
			t.Error("upstream DNS server did not stop")
		}
	}()

	m := New(filepath.Join(t.TempDir(), "dns", "config.json"))
	cfg := m.Config()
	cfg.Port = freeDNSPort(t)
	cfg.AutoProvider = false
	cfg.Servers = []Server{{Name: "test-upstream", Protocol: "Do53", Address: upstreamPacket.LocalAddr().String(), Enabled: true}}
	cfg.BlockingEnabled = true
	cfg.Rules = []Rule{{Domain: "ads.test", Action: "block"}}
	if err := m.Save(cfg); err != nil {
		t.Fatal(err)
	}
	if err := m.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := m.Stop(); err != nil {
			t.Error(err)
		}
	}()

	client := &mdns.Client{Net: "udp", Timeout: time.Second}
	query := func(name string, qtype uint16) *mdns.Msg {
		t.Helper()
		msg := new(mdns.Msg)
		msg.SetQuestion(mdns.Fqdn(name), qtype)
		response, _, err := client.Exchange(msg, net.JoinHostPort("127.0.0.1", strconv.Itoa(cfg.Port)))
		if err != nil {
			t.Fatalf("DNS query %s type %d: %v", name, qtype, err)
		}
		return response
	}

	a := query("ads.test", mdns.TypeA)
	if len(a.Answer) != 1 || a.Answer[0].Header().Rrtype != mdns.TypeA || a.Answer[0].String() != "ads.test.\t60\tIN\tA\t0.0.0.0" {
		t.Fatalf("blocked A response=%v", a.Answer)
	}
	aaaa := query("ads.test", mdns.TypeAAAA)
	if aaaa.Rcode != mdns.RcodeSuccess || len(aaaa.Answer) != 0 {
		t.Fatalf("blocked AAAA response code=%d answers=%v", aaaa.Rcode, aaaa.Answer)
	}
	if got := upstreamQueries.Load(); got != 0 {
		t.Fatalf("blocked queries leaked to upstream: %d", got)
	}

	allowed := query("allowed.test", mdns.TypeAAAA)
	if len(allowed.Answer) != 1 || allowed.Answer[0].Header().Rrtype != mdns.TypeAAAA {
		t.Fatalf("allowed AAAA response=%v", allowed.Answer)
	}
	if got := upstreamQueries.Load(); got != 1 {
		t.Fatalf("allowed query upstream count=%d, want 1", got)
	}
}

func TestSaveRejectsInsecureDoHEndpoints(t *testing.T) {
	m := New(filepath.Join(t.TempDir(), "dns", "config.json"))
	cfg := m.Config()
	cfg.Servers = []Server{{Name: "insecure", Protocol: "DoH", Address: "http://dns.example/dns-query", Enabled: true}}
	if err := m.Save(cfg); err == nil {
		t.Fatal("expected non-HTTPS DoH endpoint to be rejected")
	}
	cfg.Servers[0].Address = "https://dns.example/dns-query"
	if err := m.Save(cfg); err != nil {
		t.Fatalf("valid HTTPS DoH endpoint rejected: %v", err)
	}
}

func TestDo53RetriesOverTCPWhenUDPResponseIsTruncated(t *testing.T) {
	tcpListener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := tcpListener.Addr().(*net.TCPAddr).Port
	udpPacket, err := net.ListenPacket("udp", net.JoinHostPort("127.0.0.1", strconv.Itoa(port)))
	if err != nil {
		_ = tcpListener.Close()
		t.Fatal(err)
	}
	var udpCount, tcpCount atomic.Int32
	handler := mdns.HandlerFunc(func(w mdns.ResponseWriter, req *mdns.Msg) {
		reply := new(mdns.Msg)
		reply.SetReply(req)
		if _, ok := w.RemoteAddr().(*net.UDPAddr); ok {
			udpCount.Add(1)
			reply.Truncated = true
		} else {
			tcpCount.Add(1)
			for _, question := range req.Question {
				if question.Qtype == mdns.TypeAAAA {
					rr, _ := mdns.NewRR(question.Name + " 60 IN AAAA 2001:db8::20")
					reply.Answer = append(reply.Answer, rr)
				}
			}
		}
		_ = w.WriteMsg(reply)
	})
	udpServer := &mdns.Server{PacketConn: udpPacket, Handler: handler}
	tcpServer := &mdns.Server{Listener: tcpListener, Handler: handler}
	udpDone, tcpDone := make(chan struct{}), make(chan struct{})
	go func() { _ = udpServer.ActivateAndServe(); close(udpDone) }()
	go func() { _ = tcpServer.ActivateAndServe(); close(tcpDone) }()
	defer func() {
		_ = udpServer.Shutdown()
		_ = tcpServer.Shutdown()
		select {
		case <-udpDone:
		case <-time.After(time.Second):
			t.Error("UDP upstream did not stop")
		}
		select {
		case <-tcpDone:
		case <-time.After(time.Second):
			t.Error("TCP upstream did not stop")
		}
	}()

	m := New(filepath.Join(t.TempDir(), "dns", "config.json"))
	cfg := m.Config()
	cfg.AutoProvider = false
	cfg.Servers = []Server{{Name: "truncating-upstream", Protocol: "Do53", Address: net.JoinHostPort("127.0.0.1", strconv.Itoa(port)), Enabled: true}}
	if err := m.Save(cfg); err != nil {
		t.Fatal(err)
	}
	resp := new(mdns.Msg)
	query := new(mdns.Msg)
	query.SetQuestion("tcp-fallback.test.", mdns.TypeAAAA)
	if err := m.exchange(query, resp, cfg.Servers[0]); err != nil {
		t.Fatal(err)
	}
	if resp.Truncated || len(resp.Answer) != 1 || resp.Answer[0].Header().Rrtype != mdns.TypeAAAA {
		t.Fatalf("TCP fallback response truncated=%v answers=%v", resp.Truncated, resp.Answer)
	}
	if udpCount.Load() != 1 || tcpCount.Load() != 1 {
		t.Fatalf("expected one UDP then one TCP request, got UDP=%d TCP=%d", udpCount.Load(), tcpCount.Load())
	}
}

func TestBlocklistDomainSetsMatchSubdomainsAndAllowExceptions(t *testing.T) {
	m := &Manager{cfg: Config{BlockingEnabled: true}}
	m.SetBlocklistDomains([]string{"Ads.Example.com."}, []string{"safe.ads.example.com"})
	if !m.blockedHost("tracker.ads.example.com") {
		t.Fatal("blocklist parent domain did not match subdomain")
	}
	if m.blockedHost("safe.ads.example.com") || m.blockedHost("child.safe.ads.example.com") {
		t.Fatal("allow exception did not override matching blocklist domain")
	}
}

func TestBlocklistLongestSuffixPrecedenceMatchesHRDNS(t *testing.T) {
	m := &Manager{cfg: Config{BlockingEnabled: true}}
	m.SetBlocklistDomains(
		[]string{"blocked.allow-parent.test", "example.test", "same.test"},
		[]string{"allow-parent.test", "safe.example.test", "same.test"},
	)

	cases := []struct {
		name    string
		blocked bool
	}{
		{name: "more-specific block beats broader allow", blocked: true},
		{name: "more-specific block also matches descendants", blocked: true},
		{name: "more-specific allow beats broader block", blocked: false},
		{name: "more-specific allow also matches descendants", blocked: false},
		{name: "same-suffix allow beats block", blocked: false},
		{name: "unmatched domain is not blocked", blocked: false},
	}
	domains := []string{
		"blocked.allow-parent.test",
		"child.blocked.allow-parent.test",
		"safe.example.test",
		"child.safe.example.test",
		"same.test",
		"unmatched.test",
	}
	for i, domain := range domains {
		t.Run(cases[i].name, func(t *testing.T) {
			if got := m.blockedHost(domain); got != cases[i].blocked {
				t.Fatalf("blockedHost(%q)=%v, want %v", domain, got, cases[i].blocked)
			}
		})
	}
}

func TestExplicitDNSRulesOverrideBlocklistDomainSets(t *testing.T) {
	m := &Manager{cfg: Config{BlockingEnabled: true, Rules: []Rule{
		{Domain: "ads.example.com", Action: "allow"},
		{Domain: "safe.example.com", Action: "block"},
	}}}
	m.SetBlocklistDomains([]string{"ads.example.com", "safe.example.com"}, []string{"safe.example.com"})
	if m.blockedHost("ads.example.com") {
		t.Fatal("explicit allow rule did not override blocklist")
	}
	if !m.blockedHost("safe.example.com") {
		t.Fatal("explicit block rule did not override allow exception")
	}
}

func TestBlocklistDomainSetsRespectBlockingEnabled(t *testing.T) {
	m := &Manager{cfg: Config{BlockingEnabled: false}}
	m.SetBlocklistDomains([]string{"ads.example.com"}, nil)
	if m.blockedHost("ads.example.com") {
		t.Fatal("blocklist should be inactive when blocking is disabled")
	}
}
