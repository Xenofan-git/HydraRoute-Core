package dnsservice

import (
	"path/filepath"
	"testing"
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
