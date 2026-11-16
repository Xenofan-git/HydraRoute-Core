package dnsservice

import (
	mdns "github.com/miekg/dns"
	"path/filepath"
	"sync/atomic"
	"testing"
)

func TestDomainGroupUpstreamsAndPersistence(t *testing.T) {
	m := New(filepath.Join(t.TempDir(), "dns.json"))
	c := m.Config()
	c.Servers = []Server{{Name: "primary", Protocol: "Do53", Address: "127.0.0.1:5300", Enabled: true}, {Name: "backup", Protocol: "Do53", Address: "127.0.0.1:5301", Enabled: false}}
	c.DomainGroups = []DomainGroup{{Name: "Video", Domains: []string{"youtube.com", "full:exact.test"}, Servers: []string{"primary", "backup"}}}
	if err := m.Save(c); err != nil {
		t.Fatal(err)
	}
	reloaded := New(m.path)
	c = reloaded.Config()
	if len(c.DomainGroups) != 1 || len(c.DomainGroups[0].Servers) != 2 {
		t.Fatalf("groups lost: %+v", c.DomainGroups)
	}
	for _, tc := range []struct {
		host string
		want bool
	}{{"youtube.com", true}, {"m.youtube.com", true}, {"notyoutube.com", false}, {"exact.test", true}, {"sub.exact.test", false}} {
		q := new(mdns.Msg)
		q.SetQuestion(mdns.Fqdn(tc.host), mdns.TypeA)
		upstreams, matched := domainGroupUpstreams(q, c)
		if matched != tc.want {
			t.Errorf("%s: matched=%v want %v", tc.host, matched, tc.want)
		}
		if matched && (len(upstreams) != 1 || upstreams[0].Name != "primary") {
			t.Errorf("%s: unexpected upstreams %+v", tc.host, upstreams)
		}
	}
	c.DomainGroups[0].Servers = []string{"missing"}
	if err := m.Save(c); err == nil {
		t.Fatal("unknown group DNS server should be rejected")
	}
}

func TestDomainGroupForwardingWithSmartDNSPriority(t *testing.T) {
	var groupHits, defaultHits, smartHits atomic.Int32
	groupAddr := startFakeDNSUpstream(t, "203.0.113.10", &groupHits)
	defaultAddr := startFakeDNSUpstream(t, "203.0.113.20", &defaultHits)
	smartAddr := startFakeDNSUpstream(t, "203.0.113.30", &smartHits)
	m := New(filepath.Join(t.TempDir(), "dns.json"))
	c := m.Config()
	c.AutoProvider = false
	c.Servers = []Server{{Name: "group", Protocol: "Do53", Address: groupAddr, Enabled: true}, {Name: "default", Protocol: "Do53", Address: defaultAddr, Enabled: true}}
	c.DefaultProvider = "default"
	c.DomainGroups = []DomainGroup{{Name: "video", Domains: []string{"youtube.com"}, Servers: []string{"group"}}}
	if err := m.Save(c); err != nil {
		t.Fatal(err)
	}
	ask := func(host string, want string) {
		t.Helper()
		q := new(mdns.Msg)
		q.SetQuestion(mdns.Fqdn(host), mdns.TypeA)
		resp := new(mdns.Msg)
		resp.SetReply(q)
		if err := m.forward(q, resp); err != nil {
			t.Fatal(err)
		}
		if len(resp.Answer) != 1 || resp.Answer[0].(*mdns.A).A.String() != want {
			t.Fatalf("%s: got %+v, want %s", host, resp.Answer, want)
		}
	}
	ask("m.youtube.com", "203.0.113.10")
	ask("example.org", "203.0.113.20")
	if groupHits.Load() != 1 || defaultHits.Load() != 1 {
		t.Fatalf("unexpected upstream hits group=%d default=%d", groupHits.Load(), defaultHits.Load())
	}
	if err := m.SetSmartDNSForwarder(smartAddr); err != nil {
		t.Fatal(err)
	}
	ask("m.youtube.com", "203.0.113.30")
	if smartHits.Load() != 1 || groupHits.Load() != 1 {
		t.Fatalf("SmartDNS priority not preserved: smart=%d group=%d", smartHits.Load(), groupHits.Load())
	}
}

func TestDomainGroupFallbackAndFailClosed(t *testing.T) {
	var backupHits, defaultHits atomic.Int32
	backupAddr := startFakeDNSUpstream(t, "203.0.113.41", &backupHits)
	defaultAddr := startFakeDNSUpstream(t, "203.0.113.42", &defaultHits)
	m := New(filepath.Join(t.TempDir(), "dns.json"))
	c := m.Config()
	c.AutoProvider = false
	c.Servers = []Server{{Name: "broken", Protocol: "Do53", Address: "127.0.0.1:1", Enabled: true}, {Name: "backup", Protocol: "Do53", Address: backupAddr, Enabled: true}, {Name: "default", Protocol: "Do53", Address: defaultAddr, Enabled: true}}
	c.DefaultProvider = "default"
	c.DomainGroups = []DomainGroup{{Name: "video", Domains: []string{"youtube.com"}, Servers: []string{"broken", "backup"}}}
	if err := m.Save(c); err != nil {
		t.Fatal(err)
	}
	query := func() (*mdns.Msg, *mdns.Msg) {
		q := new(mdns.Msg)
		q.SetQuestion("youtube.com.", mdns.TypeA)
		r := new(mdns.Msg)
		r.SetReply(q)
		return q, r
	}
	q, r := query()
	if err := m.forward(q, r); err != nil {
		t.Fatal(err)
	}
	if len(r.Answer) != 1 || r.Answer[0].(*mdns.A).A.String() != "203.0.113.41" {
		t.Fatalf("backup not used: %+v", r.Answer)
	}
	if backupHits.Load() != 1 || defaultHits.Load() != 0 {
		t.Fatalf("unexpected fallback: backup=%d default=%d", backupHits.Load(), defaultHits.Load())
	}
	c.DomainGroups[0].Servers = []string{"broken"}
	if err := m.Save(c); err != nil {
		t.Fatal(err)
	}
	q, r = query()
	if err := m.forward(q, r); err == nil {
		t.Fatal("expected group failure")
	}
	if defaultHits.Load() != 0 {
		t.Fatal("group failure leaked to general DNS")
	}
	c.Servers[0].Enabled = false
	if err := m.Save(c); err != nil {
		t.Fatal(err)
	}
	q, r = query()
	if err := m.forward(q, r); err == nil {
		t.Fatal("disabled group DNS must fail closed")
	}
	if defaultHits.Load() != 0 {
		t.Fatal("disabled group DNS leaked to general DNS")
	}
}
