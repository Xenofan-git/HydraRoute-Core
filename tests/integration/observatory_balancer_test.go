package integration

import (
	"bufio"
	"context"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"hydraroute.local/core/internal/compiler"
	"hydraroute.local/core/internal/xrayapi"
)

const xrayAPIPort = 16285

type probeServer struct {
	http     *httptest.Server
	userHits atomic.Int64
}

func newProbeServer() *probeServer {
	p := &probeServer{}
	p.http = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/user" {
			p.userHits.Add(1)
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	return p
}

type socksUpstream struct {
	listener    net.Listener
	connections atomic.Int64
	userHits    atomic.Int64
	healthy     atomic.Bool
	delay       time.Duration
}

func startSOCKSUpstream(t *testing.T, delay time.Duration) *socksUpstream {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	s := &socksUpstream{listener: ln, delay: delay}
	s.healthy.Store(true)
	t.Cleanup(func() { _ = ln.Close() })
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			s.connections.Add(1)
			go s.handle(conn)
		}
	}()
	return s
}

func (s *socksUpstream) handle(conn net.Conn) {
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(8 * time.Second))
	var greeting [2]byte
	if _, err := io.ReadFull(conn, greeting[:]); err != nil || greeting[0] != 5 {
		return
	}
	methods := make([]byte, int(greeting[1]))
	if _, err := io.ReadFull(conn, methods); err != nil {
		return
	}
	if _, err := conn.Write([]byte{5, 0}); err != nil {
		return
	}
	var request [4]byte
	if _, err := io.ReadFull(conn, request[:]); err != nil || request[0] != 5 || request[1] != 1 {
		return
	}
	var host string
	switch request[3] {
	case 1:
		var raw [4]byte
		if _, err := io.ReadFull(conn, raw[:]); err != nil {
			return
		}
		host = net.IP(raw[:]).String()
	case 3:
		var size [1]byte
		if _, err := io.ReadFull(conn, size[:]); err != nil {
			return
		}
		raw := make([]byte, int(size[0]))
		if _, err := io.ReadFull(conn, raw); err != nil {
			return
		}
		host = string(raw)
	case 4:
		var raw [16]byte
		if _, err := io.ReadFull(conn, raw[:]); err != nil {
			return
		}
		host = net.IP(raw[:]).String()
	default:
		return
	}
	var rawPort [2]byte
	if _, err := io.ReadFull(conn, rawPort[:]); err != nil {
		return
	}
	if !s.healthy.Load() {
		_, _ = conn.Write([]byte{5, 5, 0, 1, 127, 0, 0, 1, 0, 0})
		return
	}
	if s.delay > 0 {
		time.Sleep(s.delay)
	}
	destination := net.JoinHostPort(host, fmt.Sprint(binary.BigEndian.Uint16(rawPort[:])))
	upstream, err := net.DialTimeout("tcp", destination, 4*time.Second)
	if err != nil {
		_, _ = conn.Write([]byte{5, 5, 0, 1, 127, 0, 0, 1, 0, 0})
		return
	}
	defer upstream.Close()
	if _, err := conn.Write([]byte{5, 0, 0, 1, 127, 0, 0, 1, 0, 0}); err != nil {
		return
	}
	_ = conn.SetDeadline(time.Time{})
	_ = upstream.SetDeadline(time.Time{})
	reader := bufio.NewReader(conn)
	requestLine, err := reader.ReadString('\n')
	if err != nil {
		return
	}
	if strings.Contains(requestLine, " /user ") {
		s.userHits.Add(1)
	}
	if _, err := io.WriteString(upstream, requestLine); err != nil {
		return
	}
	finished := make(chan struct{}, 2)
	go func() { _, _ = io.Copy(upstream, reader); finished <- struct{}{} }()
	go func() { _, _ = io.Copy(conn, upstream); finished <- struct{}{} }()
	<-finished
}

func freeTCPPort(t *testing.T) uint16 {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := uint16(ln.Addr().(*net.TCPAddr).Port)
	_ = ln.Close()
	return port
}

func socksGET(port uint16, target string) (int, error) {
	u, err := url.Parse(target)
	if err != nil {
		return 0, err
	}
	host, portText, err := net.SplitHostPort(u.Host)
	if err != nil {
		return 0, err
	}
	ip := net.ParseIP(host).To4()
	if ip == nil {
		return 0, fmt.Errorf("test target must use IPv4")
	}
	upstream, err := net.DialTimeout("tcp", net.JoinHostPort("127.0.0.1", fmt.Sprint(port)), 3*time.Second)
	if err != nil {
		return 0, err
	}
	defer upstream.Close()
	_ = upstream.SetDeadline(time.Now().Add(4 * time.Second))
	if _, err := upstream.Write([]byte{5, 1, 0}); err != nil {
		return 0, err
	}
	var greeting [2]byte
	if _, err := io.ReadFull(upstream, greeting[:]); err != nil {
		return 0, err
	}
	if greeting != [2]byte{5, 0} {
		return 0, fmt.Errorf("SOCKS method negotiation failed: %v", greeting)
	}
	portNum, err := strconv.Atoi(portText)
	if err != nil {
		return 0, err
	}
	request := []byte{5, 1, 0, 1, ip[0], ip[1], ip[2], ip[3], byte(portNum >> 8), byte(portNum)}
	if _, err := upstream.Write(request); err != nil {
		return 0, err
	}
	var reply [10]byte
	if _, err := io.ReadFull(upstream, reply[:]); err != nil {
		return 0, err
	}
	if reply[1] != 0 {
		return 0, fmt.Errorf("SOCKS connect rejected: code %d", reply[1])
	}
	path := u.RequestURI()
	if path == "" {
		path = "/"
	}
	if _, err := fmt.Fprintf(upstream, "GET %s HTTP/1.1\r\nHost: %s\r\nConnection: close\r\n\r\n", path, u.Host); err != nil {
		return 0, err
	}
	line, err := bufio.NewReader(upstream).ReadString('\n')
	if err != nil {
		return 0, err
	}
	var major, minor, code int
	if _, err := fmt.Sscanf(line, "HTTP/%d.%d %d", &major, &minor, &code); err != nil {
		return 0, fmt.Errorf("invalid HTTP response %q: %w", line, err)
	}
	return code, nil
}

func readOutboundStatuses() (map[string]xrayapi.OutboundStatus, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	items, err := xrayapi.GetOutboundStatus(ctx, "127.0.0.1:16285")
	if err != nil {
		return nil, err
	}
	out := make(map[string]xrayapi.OutboundStatus, len(items))
	for _, item := range items {
		out[item.Tag] = item
	}
	return out, nil
}

func waitForOutboundState(t *testing.T, tag string, alive bool, afterTry int64) xrayapi.OutboundStatus {
	t.Helper()
	deadline := time.Now().Add(8 * time.Second)
	var latest xrayapi.OutboundStatus
	for time.Now().Before(deadline) {
		statuses, err := readOutboundStatuses()
		if err == nil {
			latest = statuses[tag]
			if status, ok := statuses[tag]; ok && status.LastTryTime > afterTry && status.Alive == alive {
				return status
			}
		}
		time.Sleep(150 * time.Millisecond)
	}
	t.Fatalf("Observatory did not move %s to alive=%v after LastTryTime %d; latest=%+v", tag, alive, afterTry, latest)
	return xrayapi.OutboundStatus{}
}

func TestXrayObservatoryBalancerRuntimeAndFailClosedFallback(t *testing.T) {
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
	if ln, err := net.Listen("tcp", "127.0.0.1:16285"); err == nil {
		_ = ln.Close()
	} else {
		t.Skipf("Xray API test port %d is already in use: %v", xrayAPIPort, err)
	}

	probe := newProbeServer()
	defer probe.http.Close()
	live := startSOCKSUpstream(t, 0)
	rrPort, lpPort := freeTCPPort(t), freeTCPPort(t)
	for rrPort == lpPort {
		lpPort = freeTCPPort(t)
	}

	outbound := func(key string, port uint16) compiler.Node {
		data, _ := json.Marshal(map[string]any{"protocol": "socks", "settings": map[string]any{"servers": []any{map[string]any{"address": "127.0.0.1", "port": port}}}})
		return compiler.Node{Key: key, Outbound: data}
	}
	compiled, err := compiler.Compile(compiler.CompileRequest{ObservatoryProbeIntervalSeconds: 5, Profiles: []compiler.Profile{
		{ID: "rr", Enabled: true, Port: rrPort, Strategy: "roundRobin", FallbackKey: "rr-dead-a", Nodes: []compiler.Node{outbound("rr-dead-a", freeTCPPort(t)), outbound("rr-live", uint16(live.listener.Addr().(*net.TCPAddr).Port)), outbound("rr-dead-c", freeTCPPort(t))}},
		{ID: "lp", Enabled: true, Port: lpPort, Strategy: "leastPing", FallbackKey: "lp-dead-a", Nodes: []compiler.Node{outbound("lp-dead-a", freeTCPPort(t)), outbound("lp-dead-b", freeTCPPort(t))}},
	}})
	if err != nil {
		t.Fatal(err)
	}
	var config map[string]any
	if err := json.Unmarshal(compiled, &config); err != nil {
		t.Fatal(err)
	}
	routing := config["routing"].(map[string]any)
	balancers := routing["balancers"].([]any)
	fallbacks := map[string]string{"rr": compiler.OutboundTag("rr", "rr-dead-a"), "lp": compiler.OutboundTag("lp", "lp-dead-a")}
	for _, raw := range balancers {
		balancer := raw.(map[string]any)
		id := strings.TrimPrefix(balancer["tag"].(string), "hr_bal_")
		if balancer["fallbackTag"] != fallbacks[id] {
			t.Fatalf("profile %s has non-local fallbackTag %v", id, balancer["fallbackTag"])
		}
	}
	config["log"] = map[string]any{"loglevel": "warning", "access": filepath.Join(t.TempDir(), "access.log"), "error": filepath.Join(t.TempDir(), "error.log")}
	observatory := config["observatory"].(map[string]any)
	observatory["probeUrl"] = probe.http.URL + "/generate_204"
	observatory["probeInterval"] = "500ms"
	configDir := t.TempDir()
	configPath := filepath.Join(configDir, "xray.json")
	data, err := json.MarshalIndent(config, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(configPath, data, 0600); err != nil {
		t.Fatal(err)
	}
	if output, err := exec.Command(xrayBinary, "run", "-test", "-config", configPath).CombinedOutput(); err != nil {
		t.Fatalf("Xray rejected Core-generated fallback config: %v\n%s", err, output)
	}

	logPath := config["log"].(map[string]any)["error"].(string)
	logFile, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0600)
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(xrayBinary, "run", "-config", configPath)
	cmd.Stdout, cmd.Stderr = logFile, logFile
	if err := cmd.Start(); err != nil {
		_ = logFile.Close()
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cmd.Process.Kill(); _ = cmd.Wait(); _ = logFile.Close() })

	wantAlive := map[string]bool{
		compiler.OutboundTag("rr", "rr-dead-a"): false,
		compiler.OutboundTag("rr", "rr-live"):   true,
		compiler.OutboundTag("rr", "rr-dead-c"): false,
		compiler.OutboundTag("lp", "lp-dead-a"): false,
		compiler.OutboundTag("lp", "lp-dead-b"): false,
	}
	deadline := time.Now().Add(18 * time.Second)
	ready := false
	var initialStatuses map[string]xrayapi.OutboundStatus
	for time.Now().Before(deadline) {
		statuses, statusErr := readOutboundStatuses()
		if statusErr == nil {
			ready = true
			for tag, alive := range wantAlive {
				status, ok := statuses[tag]
				if !ok || status.LastTryTime == 0 || status.Alive != alive {
					ready = false
					break
				}
			}
			if ready {
				initialStatuses = statuses
				break
			}
		}
		time.Sleep(200 * time.Millisecond)
	}
	if !ready {
		t.Fatal("Observatory did not report the expected alive/dead state for all five profile outbounds")
	}

	// Every subject must be probed again on the configured cadence.
	time.Sleep(12 * time.Second)
	refreshed, err := readOutboundStatuses()
	if err != nil {
		t.Fatalf("cannot read refreshed Observatory statuses: %v", err)
	}
	for tag := range wantAlive {
		t.Logf("cadence %s: before=%d after=%d", tag, initialStatuses[tag].LastTryTime, refreshed[tag].LastTryTime)
		if refreshed[tag].LastTryTime <= initialStatuses[tag].LastTryTime {
			t.Fatalf("Observatory did not refresh %s: before=%d after=%d", tag, initialStatuses[tag].LastTryTime, refreshed[tag].LastTryTime)
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	targets, err := xrayapi.GetBalancerPrincipleTargetTags(ctx, "127.0.0.1:16285", "hr_bal_rr")
	cancel()
	if err != nil {
		t.Fatalf("GetBalancerInfo failed for roundRobin: %v", err)
	}
	wantTargets := map[string]bool{
		compiler.OutboundTag("rr", "rr-dead-a"): true,
		compiler.OutboundTag("rr", "rr-live"):   true,
		compiler.OutboundTag("rr", "rr-dead-c"): true,
	}
	if len(targets) != len(wantTargets) {
		t.Fatalf("GetBalancerInfo candidate set mismatch: %q", targets)
	}
	for _, tag := range targets {
		if !wantTargets[tag] {
			t.Fatalf("GetBalancerInfo returned foreign candidate %q", tag)
		}
	}

	before := live.connections.Load()
	for i := 0; i < 3; i++ {
		code, err := socksGET(rrPort, probe.http.URL+"/user")
		if err != nil || code != http.StatusNoContent {
			t.Fatalf("roundRobin request %d failed: status=%d err=%v", i+1, code, err)
		}
	}
	if live.connections.Load()-before < 3 {
		t.Fatal("roundRobin did not send all three user requests through its only healthy member")
	}
	if probe.userHits.Load() != 3 {
		t.Fatalf("roundRobin user request count mismatch: got %d want 3", probe.userHits.Load())
	}

	// A live outbound must transition to dead when its upstream fails, then
	// recover after the upstream becomes available again.
	liveTag := compiler.OutboundTag("rr", "rr-live")
	beforeStatuses, err := readOutboundStatuses()
	if err != nil {
		t.Fatal(err)
	}
	beforeTry := beforeStatuses[liveTag].LastTryTime
	live.healthy.Store(false)
	deadStatus := waitForOutboundState(t, liveTag, false, beforeTry)
	before = probe.userHits.Load()
	if _, err := socksGET(rrPort, probe.http.URL+"/user"); err == nil {
		t.Fatal("roundRobin unexpectedly succeeded while its only healthy member was down")
	}
	if got := probe.userHits.Load(); got != before {
		t.Fatalf("roundRobin sent traffic directly while its pool was down: user hits %d -> %d", before, got)
	}
	live.healthy.Store(true)
	_ = waitForOutboundState(t, liveTag, true, deadStatus.LastTryTime)
	if code, err := socksGET(rrPort, probe.http.URL+"/user"); err != nil || code != http.StatusNoContent {
		t.Fatalf("roundRobin did not recover after its member came back: status=%d err=%v", code, err)
	}

	before = probe.userHits.Load()
	if code, err := socksGET(lpPort, probe.http.URL+"/user"); err == nil {
		t.Fatalf("all-dead leastPing unexpectedly returned HTTP %d; a direct fallback leak is possible", code)
	}
	if got := probe.userHits.Load(); got != before {
		t.Fatalf("all-dead leastPing reached the direct HTTP endpoint: user hits %d -> %d", before, got)
	}
}

func TestXrayLeastPingAndRoundRobinDistribution(t *testing.T) {
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
	if ln, err := net.Listen("tcp", "127.0.0.1:16285"); err == nil {
		_ = ln.Close()
	} else {
		t.Skipf("Xray API test port %d is already in use: %v", xrayAPIPort, err)
	}

	probe := newProbeServer()
	defer probe.http.Close()
	slow := startSOCKSUpstream(t, 250*time.Millisecond)
	medium := startSOCKSUpstream(t, 90*time.Millisecond)
	fast := startSOCKSUpstream(t, 20*time.Millisecond)
	rrPort, lpPort := freeTCPPort(t), freeTCPPort(t)
	for rrPort == lpPort {
		lpPort = freeTCPPort(t)
	}
	outbound := func(key string, port uint16) compiler.Node {
		data, _ := json.Marshal(map[string]any{"protocol": "socks", "settings": map[string]any{"servers": []any{map[string]any{"address": "127.0.0.1", "port": port}}}})
		return compiler.Node{Key: key, Outbound: data}
	}
	compiled, err := compiler.Compile(compiler.CompileRequest{ObservatoryProbeIntervalSeconds: 5, Profiles: []compiler.Profile{
		{ID: "rrdist", Enabled: true, Port: rrPort, Strategy: "roundRobin", FallbackKey: "rr-slow", Nodes: []compiler.Node{outbound("rr-slow", uint16(slow.listener.Addr().(*net.TCPAddr).Port)), outbound("rr-medium", uint16(medium.listener.Addr().(*net.TCPAddr).Port)), outbound("rr-fast", uint16(fast.listener.Addr().(*net.TCPAddr).Port))}},
		{ID: "lpfast", Enabled: true, Port: lpPort, Strategy: "leastPing", FallbackKey: "lp-slow", Nodes: []compiler.Node{outbound("lp-slow", uint16(slow.listener.Addr().(*net.TCPAddr).Port)), outbound("lp-medium", uint16(medium.listener.Addr().(*net.TCPAddr).Port)), outbound("lp-fast", uint16(fast.listener.Addr().(*net.TCPAddr).Port))}},
	}})
	if err != nil {
		t.Fatal(err)
	}
	var config map[string]any
	if err := json.Unmarshal(compiled, &config); err != nil {
		t.Fatal(err)
	}
	config["log"] = map[string]any{"loglevel": "warning", "access": filepath.Join(t.TempDir(), "access.log"), "error": filepath.Join(t.TempDir(), "error.log")}
	observatory := config["observatory"].(map[string]any)
	observatory["probeUrl"] = probe.http.URL + "/generate_204"
	observatory["probeInterval"] = "500ms"
	configPath := filepath.Join(t.TempDir(), "xray.json")
	data, err := json.MarshalIndent(config, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(configPath, data, 0600); err != nil {
		t.Fatal(err)
	}
	if output, err := exec.Command(xrayBinary, "run", "-test", "-config", configPath).CombinedOutput(); err != nil {
		t.Fatalf("Xray rejected strategy config: %v\n%s", err, output)
	}
	logFile, err := os.OpenFile(config["log"].(map[string]any)["error"].(string), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0600)
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(xrayBinary, "run", "-config", configPath)
	cmd.Stdout, cmd.Stderr = logFile, logFile
	if err := cmd.Start(); err != nil {
		_ = logFile.Close()
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cmd.Process.Kill(); _ = cmd.Wait(); _ = logFile.Close() })

	wantTags := []string{
		compiler.OutboundTag("rrdist", "rr-slow"), compiler.OutboundTag("rrdist", "rr-medium"), compiler.OutboundTag("rrdist", "rr-fast"),
		compiler.OutboundTag("lpfast", "lp-slow"), compiler.OutboundTag("lpfast", "lp-medium"), compiler.OutboundTag("lpfast", "lp-fast"),
	}
	deadline := time.Now().Add(20 * time.Second)
	ready := false
	for time.Now().Before(deadline) {
		statuses, statusErr := readOutboundStatuses()
		if statusErr == nil {
			ready = true
			for _, tag := range wantTags {
				if status, ok := statuses[tag]; !ok || !status.Alive || status.LastTryTime == 0 {
					ready = false
					break
				}
			}
			if ready {
				break
			}
		}
		time.Sleep(200 * time.Millisecond)
	}
	if !ready {
		t.Fatal("Observatory did not mark all three test upstreams alive for both strategies")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	leastPingTargets, err := xrayapi.GetBalancerPrincipleTargetTags(ctx, "127.0.0.1:16285", "hr_bal_lpfast")
	cancel()
	if err != nil {
		t.Fatalf("GetBalancerInfo failed for leastPing: %v", err)
	}
	if len(leastPingTargets) != 1 || leastPingTargets[0] != compiler.OutboundTag("lpfast", "lp-fast") {
		t.Fatalf("leastPing did not choose the lowest-delay outbound: %q", leastPingTargets)
	}
	ctx, cancel = context.WithTimeout(context.Background(), 3*time.Second)
	roundRobinTargets, err := xrayapi.GetBalancerPrincipleTargetTags(ctx, "127.0.0.1:16285", "hr_bal_rrdist")
	cancel()
	if err != nil {
		t.Fatalf("GetBalancerInfo failed for roundRobin: %v", err)
	}
	if len(roundRobinTargets) != 3 {
		t.Fatalf("roundRobin candidate set mismatch: %q", roundRobinTargets)
	}

	slowBefore, mediumBefore, fastBefore := slow.userHits.Load(), medium.userHits.Load(), fast.userHits.Load()
	for i := 0; i < 6; i++ {
		code, err := socksGET(rrPort, probe.http.URL+"/user")
		if err != nil || code != http.StatusNoContent {
			t.Fatalf("roundRobin request %d failed: status=%d err=%v", i+1, code, err)
		}
	}
	slowDelta := slow.userHits.Load() - slowBefore
	mediumDelta := medium.userHits.Load() - mediumBefore
	fastDelta := fast.userHits.Load() - fastBefore
	if slowDelta == 0 || mediumDelta == 0 || fastDelta == 0 || slowDelta+mediumDelta+fastDelta != 6 {
		t.Fatalf("roundRobin did not distribute six requests across all three members: slow=%d medium=%d fast=%d", slowDelta, mediumDelta, fastDelta)
	}

	slowBefore, mediumBefore, fastBefore = slow.userHits.Load(), medium.userHits.Load(), fast.userHits.Load()
	for i := 0; i < 3; i++ {
		code, err := socksGET(lpPort, probe.http.URL+"/user")
		if err != nil || code != http.StatusNoContent {
			t.Fatalf("leastPing request %d failed: status=%d err=%v", i+1, code, err)
		}
	}
	if slow.userHits.Load() != slowBefore || medium.userHits.Load() != mediumBefore || fast.userHits.Load()-fastBefore != 3 {
		t.Fatalf("leastPing traffic did not stay on the fastest member: slow=%d medium=%d fast=%d", slow.userHits.Load()-slowBefore, medium.userHits.Load()-mediumBefore, fast.userHits.Load()-fastBefore)
	}
}
