package dnsservice

import (
	mdns "github.com/miekg/dns"
	"path/filepath"
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
