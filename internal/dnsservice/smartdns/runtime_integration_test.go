package smartdns

import (
	"context"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestRuntimeWithSmartDNSBinary(t *testing.T) {
	binary := os.Getenv("SMARTDNS_BINARY")
	if binary == "" {
		t.Skip("SMARTDNS_BINARY is not set; run in the pinned SmartDNS sandbox job")
	}
	if !filepath.IsAbs(binary) {
		t.Fatalf("SMARTDNS_BINARY must be absolute: %q", binary)
	}
	port := freeLoopbackPort(t)
	runtime, err := NewRuntime(RuntimeConfig{
		BinaryPath: binary,
		StateDir:   filepath.Join(t.TempDir(), "runtime"),
		ListenAddr: fmt.Sprintf("127.0.0.1:%d", port),
	})
	if err != nil {
		t.Fatal(err)
	}
	cfg := Config{
		Enabled:    true,
		ListenHost: "127.0.0.1",
		ListenPort: port,
		CacheSize:  32,
		CAPath:     "/etc/ssl/certs",
		Upstreams: []Upstream{{
			ID: "upstream1", ProfileID: "profile1", Protocol: "doh",
			Host: "dns.example.com", BootstrapIP: "1.1.1.1", Enabled: true,
		}},
	}
	profiles := []ProfileRef{{ID: "profile1", Enabled: true, Listen: "127.0.0.1", Port: 16001}}
	if err := runtime.Apply(context.Background(), cfg, profiles); err != nil {
		t.Fatalf("prepare SmartDNS generation: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := runtime.Start(ctx); err != nil {
		t.Fatalf("start pinned SmartDNS binary: %v", err)
	}
	if status := runtime.Status(); !status.Running || status.Generation == "" {
		t.Fatalf("SmartDNS did not report a ready generation: %+v", status)
	}
	if err := runtime.Stop(context.Background()); err != nil {
		t.Fatalf("stop SmartDNS: %v", err)
	}
	if status := runtime.Status(); status.Running {
		t.Fatalf("SmartDNS remained running after stop: %+v", status)
	}
}

func freeLoopbackPort(t *testing.T) int {
	t.Helper()
	for i := 0; i < 10; i++ {
		tcp, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		port := tcp.Addr().(*net.TCPAddr).Port
		udp, err := net.ListenPacket("udp", fmt.Sprintf("127.0.0.1:%d", port))
		if err == nil {
			_ = udp.Close()
			_ = tcp.Close()
			return port
		}
		_ = tcp.Close()
	}
	t.Fatal("could not allocate a free loopback TCP/UDP port")
	return 0
}
