package integration

import (
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"hydraroute.local/core/internal/compiler"
)

type observatoryConcurrencyProbe struct {
	server   *httptest.Server
	requests atomic.Int64
	active   atomic.Int64
	maximum  atomic.Int64
}

func newObservatoryConcurrencyProbe(delay time.Duration) *observatoryConcurrencyProbe {
	p := &observatoryConcurrencyProbe{}
	p.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/generate_204" {
			p.requests.Add(1)
			active := p.active.Add(1)
			for {
				maximum := p.maximum.Load()
				if active <= maximum || p.maximum.CompareAndSwap(maximum, active) {
					break
				}
			}
			time.Sleep(delay)
			p.active.Add(-1)
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	return p
}

func TestXrayObservatorySubjectsAreProbedConcurrently(t *testing.T) {
	if os.Getenv("HRCORE_XRAY_INTEGRATION") != "1" {
		t.Skip("set HRCORE_XRAY_INTEGRATION=1 to run the real Xray data-plane test")
	}
	xrayBinary := os.Getenv("XRAY_BINARY")
	if xrayBinary == "" {
		t.Fatal("XRAY_BINARY is required")
	}
	if _, err := os.Stat(xrayBinary); err != nil {
		t.Fatalf("Xray binary is unavailable: %v", err)
	}
	apiListener, err := net.Listen("tcp", net.JoinHostPort("127.0.0.1", fmt.Sprint(xrayAPIPort)))
	if err != nil {
		t.Skipf("Xray API test port %d is already in use: %v", xrayAPIPort, err)
	}
	_ = apiListener.Close()

	probe := newObservatoryConcurrencyProbe(700 * time.Millisecond)
	defer probe.server.Close()
	upstream := startSOCKSUpstream(t, 0)
	profilePort := freeTCPPort(t)
	upstreamPort := uint16(upstream.listener.Addr().(*net.TCPAddr).Port)
	nodes := make([]compiler.Node, 4)
	for i := range nodes {
		key := fmt.Sprintf("parallel-%d", i)
		outbound, marshalErr := json.Marshal(map[string]any{
			"protocol": "socks",
			"settings": map[string]any{"servers": []any{map[string]any{"address": "127.0.0.1", "port": upstreamPort}}},
		})
		if marshalErr != nil {
			t.Fatal(marshalErr)
		}
		nodes[i] = compiler.Node{Key: key, Outbound: outbound}
	}
	compiled, err := compiler.Compile(compiler.CompileRequest{
		ObservatoryProbeIntervalSeconds: 5,
		Profiles:                        []compiler.Profile{{ID: "parallel", Enabled: true, Port: profilePort, Nodes: nodes}},
	})
	if err != nil {
		t.Fatal(err)
	}
	var config map[string]any
	if err := json.Unmarshal(compiled, &config); err != nil {
		t.Fatal(err)
	}
	logDir := t.TempDir()
	config["log"] = map[string]any{
		"loglevel": "warning",
		"access":   filepath.Join(logDir, "access.log"),
		"error":    filepath.Join(logDir, "error.log"),
	}
	observatory := config["observatory"].(map[string]any)
	if observatory["enableConcurrency"] != true {
		t.Fatalf("compiler emitted enableConcurrency=%v, want true", observatory["enableConcurrency"])
	}
	if selector, ok := observatory["subjectSelector"].([]any); !ok || len(selector) != len(nodes) {
		t.Fatalf("compiler emitted unexpected Observatory subjects: %#v", observatory["subjectSelector"])
	}
	observatory["probeUrl"] = probe.server.URL + "/generate_204"
	observatory["probeInterval"] = "5s"
	configPath := filepath.Join(logDir, "xray-concurrent.json")
	data, err := json.MarshalIndent(config, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(configPath, data, 0600); err != nil {
		t.Fatal(err)
	}
	if output, err := exec.Command(xrayBinary, "run", "-test", "-config", configPath).CombinedOutput(); err != nil {
		t.Fatalf("Xray rejected concurrent Observatory config: %v\n%s", err, output)
	}
	logFile, err := os.OpenFile(filepath.Join(logDir, "error.log"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0600)
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(xrayBinary, "run", "-config", configPath)
	cmd.Stdout, cmd.Stderr = logFile, logFile
	if err := cmd.Start(); err != nil {
		_ = logFile.Close()
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		_ = logFile.Close()
	})

	deadline := time.Now().Add(12 * time.Second)
	for time.Now().Before(deadline) && probe.maximum.Load() < 2 {
		time.Sleep(50 * time.Millisecond)
	}
	if got := probe.maximum.Load(); got < 2 {
		t.Fatalf("Observatory probes were not concurrent: max in-flight=%d, total probes=%d", got, probe.requests.Load())
	}
	if got := probe.requests.Load(); got < 2 {
		t.Fatalf("too few Observatory probe requests observed: %d", got)
	}
	t.Logf("verified parallel Observatory probing: max in-flight=%d, probe requests=%d", probe.maximum.Load(), probe.requests.Load())
}
