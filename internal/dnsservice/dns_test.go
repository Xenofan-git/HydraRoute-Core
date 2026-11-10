package dnsservice

import (
	"net"
	"path/filepath"
	"strconv"
	"testing"
	"time"
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
