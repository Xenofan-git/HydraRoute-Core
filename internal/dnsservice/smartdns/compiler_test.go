package smartdns

import (
	"strings"
	"testing"
)

func fixtures() (Config, []ProfileRef) {
	return Config{
		Enabled: true, ListenHost: "127.0.0.1", ListenPort: 16053, CacheSize: 4096,
		Upstreams: []Upstream{
			{ID: "foreign-doh", ProfileID: "foreign", Protocol: "doh", Host: "dns.example.net", Path: "/dns-query", BootstrapIP: "1.1.1.1", Enabled: true},
			{ID: "torrent-dot", ProfileID: "torrent", Protocol: "dot", Host: "dot.example.org", BootstrapIP: "9.9.9.9", Enabled: true},
		},
		Rules: []DomainRule{
			{ID: "suffix", MatchType: "suffix", Pattern: "example.com", ProfileID: "foreign", Enabled: true},
			{ID: "exact", MatchType: "exact", Pattern: "login.example.com", ProfileID: "torrent", Enabled: true},
		},
	}, []ProfileRef{
		{ID: "foreign", Enabled: true, Listen: "127.0.0.1", Port: 16081},
		{ID: "torrent", Enabled: true, Listen: "127.0.0.1", Port: 16082},
	}
}

func TestCompileCreatesOnePinnedProxyPerProfile(t *testing.T) {
	cfg, profiles := fixtures()
	out, err := Compile(cfg, profiles)
	if err != nil {
		t.Fatal(err)
	}
	if out.ProxyCount != 2 || out.UpstreamCount != 2 || out.RuleCount != 2 {
		t.Fatalf("unexpected counts: %+v", out)
	}
	for _, expected := range []string{
		"bind 127.0.0.1:16053", "bind-tcp 127.0.0.1:16053",
		"cache-persist no", "resolv-file /dev/null", "speed-check-mode none", "local-ptr-enable no", "mdns-lookup no", "expand-ptr-from-address no", "audit-enable no",
		"proxy-server socks5://127.0.0.1:16081 -name hr_proxy_foreign",
		"proxy-server socks5://127.0.0.1:16082 -name hr_proxy_torrent",
		"server-https https://dns.example.net:443/dns-query -group hr_dns_foreign -exclude-default-group -proxy hr_proxy_foreign",
		"server-tls dot.example.org:853 -group hr_dns_torrent -exclude-default-group -proxy hr_proxy_torrent",
		"-host-name dns.example.net -tls-host-verify dns.example.net -host-ip 1.1.1.1 -http-host dns.example.net",
		"-host-name dot.example.org -tls-host-verify dot.example.org -host-ip 9.9.9.9",
		"nameserver /-login.example.com/hr_dns_torrent",
		"nameserver /example.com/hr_dns_foreign",
	} {
		if !strings.Contains(out.Config, expected) {
			t.Errorf("generated config missing %q:\n%s", expected, out.Config)
		}
	}
	if strings.Index(out.Config, "nameserver /-login.example.com") > strings.Index(out.Config, "nameserver /example.com") {
		t.Fatal("exact rules must be emitted before suffix rules")
	}
}

func TestCompileFailsClosedAndNeverUsesDefaultOrUnsafeDirectives(t *testing.T) {
	cfg, profiles := fixtures()
	out, err := Compile(cfg, profiles)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.Config, "Default upstream group is intentionally empty") {
		t.Fatal("missing fail-closed declaration")
	}
	for _, unsafe := range []string{"server 1.1.1.1", "-no-check-certificate", "-subnet", "-set-mark", "-interface", "ipset", "nftset", "ip-rules", "route"} {
		if strings.Contains(out.Config, unsafe) {
			t.Errorf("unsafe directive %q emitted", unsafe)
		}
	}
	if strings.Count(out.Config, "-exclude-default-group") != 2 {
		t.Fatal("every upstream must be excluded from default group")
	}
}

func TestCompileRejectsNonLoopbackAndMissingOrDisabledProfiles(t *testing.T) {
	cfg, profiles := fixtures()
	cfg.ListenHost = "0.0.0.0"
	if _, err := Compile(cfg, profiles); err == nil {
		t.Fatal("expected non-loopback bind rejection")
	}
	cfg, profiles = fixtures()
	profiles[0].Enabled = false
	if _, err := Compile(cfg, profiles); err == nil {
		t.Fatal("expected disabled profile reference rejection")
	}
	cfg, profiles = fixtures()
	profiles[0].Listen = "0.0.0.0"
	if _, err := Compile(cfg, profiles); err == nil {
		t.Fatal("expected non-loopback SOCKS rejection")
	}
}

func TestCompileRejectsUnpinnedAndUnsafeUpstreams(t *testing.T) {
	cfg, profiles := fixtures()
	cfg.Upstreams[0].BootstrapIP = "192.0.2.1"
	if _, err := Compile(cfg, profiles); err == nil {
		t.Fatal("expected documentation bootstrap IP rejection")
	}
	cfg, profiles = fixtures()
	cfg.Upstreams[0].Protocol = "do53"
	if _, err := Compile(cfg, profiles); err == nil {
		t.Fatal("expected plaintext upstream rejection")
	}
	cfg, profiles = fixtures()
	cfg.Upstreams[0].Host = "127.0.0.1"
	if _, err := Compile(cfg, profiles); err == nil {
		t.Fatal("expected IP hostname rejection")
	}
}

func TestCompileRejectsAmbiguousRulesAndUnknownProfile(t *testing.T) {
	cfg, profiles := fixtures()
	cfg.Rules = append(cfg.Rules, DomainRule{ID: "same", MatchType: "suffix", Pattern: "example.com", ProfileID: "torrent", Enabled: true})
	if _, err := Compile(cfg, profiles); err == nil {
		t.Fatal("expected ambiguous rule rejection")
	}
	cfg, profiles = fixtures()
	cfg.Rules[0].ProfileID = "missing"
	if _, err := Compile(cfg, profiles); err == nil {
		t.Fatal("expected unknown profile rejection")
	}
}

func TestDisabledConfigProducesNoListenerOrRuntimeConfig(t *testing.T) {
	cfg, profiles := fixtures()
	cfg.Enabled = false
	out, err := Compile(cfg, profiles)
	if err != nil {
		t.Fatal(err)
	}
	if out.Config != "" || out.ProxyCount != 0 {
		t.Fatalf("disabled subsystem emitted config: %+v", out)
	}
}

func TestCompileGeneratesValidatedDomainSetFiles(t *testing.T) {
	cfg, profiles := fixtures()
	cfg.DataDir = "/var/lib/hydraroute/state/dns/sets"
	cfg.DomainSets = []DomainSet{
		{ID: "ads", Kind: "block", Domains: []string{"z.ads.test", "a.ads.test", "a.ads.test"}, Enabled: true},
		{ID: "trusted", Kind: "allow", Domains: []string{"trusted.test"}, Enabled: true},
		{ID: "torrent-domains", Kind: "route", ProfileID: "torrent", Domains: []string{"tracker.test"}, Enabled: true},
	}
	out, err := Compile(cfg, profiles)
	if err != nil {
		t.Fatal(err)
	}
	if out.DomainSetCount != 3 || len(out.Files) != 3 {
		t.Fatalf("domain-set output=%+v files=%v", out, out.Files)
	}
	for _, expected := range []string{
		"domain-set -name hr_set_ads -type list -file /var/lib/hydraroute/state/dns/sets/domain-set-ads.txt",
		"address /domain-set:hr_set_ads/#",
		"address /domain-set:hr_set_trusted/-",
		"nameserver /domain-set:hr_set_torrent-domains/hr_dns_torrent",
	} {
		if !strings.Contains(out.Config, expected) {
			t.Errorf("missing domain-set directive %q:\n%s", expected, out.Config)
		}
	}
	if got := string(out.Files["domain-set-ads.txt"]); got != "a.ads.test\nz.ads.test\n" {
		t.Fatalf("domain-set file is not deduplicated/sorted: %q", got)
	}
}

func TestCompileAppliesAllowlistPrecedenceAcrossDomainSets(t *testing.T) {
	cfg, profiles := fixtures()
	cfg.DataDir = "/var/lib/hydraroute/state/dns/sets"
	cfg.DomainSets = []DomainSet{
		{ID: "block", Kind: "block", Domains: []string{"example.test", "ads.allow-parent.test"}, Enabled: true},
		{ID: "allow", Kind: "allow", Domains: []string{"safe.example.test", "allow-parent.test", "same.test"}, Enabled: true},
		{ID: "block-same", Kind: "block", Domains: []string{"same.test"}, Enabled: true},
	}
	out, err := Compile(cfg, profiles)
	if err != nil {
		t.Fatalf("allow exceptions must compile: %v", err)
	}
	blocked := string(out.Files["domain-set-block.txt"])
	for _, domain := range []string{"example.test", "ads.allow-parent.test"} {
		if !strings.Contains(blocked, domain) {
			t.Fatalf("block set lost source entry %q: %q", domain, blocked)
		}
	}
	if got := string(out.Files["domain-set-block-same.txt"]); got != "same.test\n" {
		t.Fatalf("exact overlapping block entry must be preserved for SmartDNS allow flags: %q", got)
	}
	allowed := string(out.Files["domain-set-allow.txt"])
	for _, domain := range []string{"safe.example.test", "allow-parent.test", "same.test"} {
		if !strings.Contains(allowed, domain) {
			t.Fatalf("allow set lost exception %q: %q", domain, allowed)
		}
	}
	blockRule := strings.Index(out.Config, "address /domain-set:hr_set_block/#")
	allowRule := strings.Index(out.Config, "address /domain-set:hr_set_allow/-")
	if blockRule < 0 || allowRule < 0 || allowRule < blockRule {
		t.Fatalf("allow rules must be emitted after block rules: %s", out.Config)
	}
}


func TestCompileRejectsUnsafeDomainSetPaths(t *testing.T) {
	cfg, profiles := fixtures()
	cfg.DataDir = "/var/lib/hydraroute/../etc"
	cfg.DomainSets = []DomainSet{{ID: "block", Kind: "block", Domains: []string{"ads.test"}, Enabled: true}}
	if _, err := Compile(cfg, profiles); err == nil {
		t.Fatal("expected unsafe data directory rejection")
	}
}

func TestCompileRejectsRouteDomainSetWithoutProfileUpstream(t *testing.T) {
	cfg, profiles := fixtures()
	cfg.DataDir = "/var/lib/hydraroute/state/dns/sets"
	cfg.DomainSets = []DomainSet{{ID: "route", Kind: "route", ProfileID: "missing", Domains: []string{"example.test"}, Enabled: true}}
	if _, err := Compile(cfg, profiles); err == nil {
		t.Fatal("expected unknown profile route set rejection")
	}
}
