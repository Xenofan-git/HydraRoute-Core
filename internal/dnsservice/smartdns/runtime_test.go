package smartdns

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

type fakeProcess struct {
	mu      sync.Mutex
	stopped bool
	wait    chan error
}

func newFakeProcess() *fakeProcess { return &fakeProcess{wait: make(chan error, 1)} }
func (p *fakeProcess) Wait() error { return <-p.wait }
func (p *fakeProcess) exit(err error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if !p.stopped {
		p.stopped = true
		p.wait <- err
	}
}
func (p *fakeProcess) Stop(context.Context) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if !p.stopped {
		p.stopped = true
		p.wait <- errors.New("stopped")
	}
	return nil
}

type fakeLauncher struct {
	mu         sync.Mutex
	processes  []*fakeProcess
	failStarts int
	attempts   int
}

func (l *fakeLauncher) Start(context.Context, string, string) (Process, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.attempts++
	if l.failStarts > 0 {
		l.failStarts--
		return nil, errors.New("simulated launch failure")
	}
	p := newFakeProcess()
	l.processes = append(l.processes, p)
	return p, nil
}

func TestRuntimeApplyStagesGenerationAndStartsOnlyAfterConfigIsWritten(t *testing.T) {
	root := t.TempDir()
	r, err := NewRuntime(RuntimeConfig{BinaryPath: "/usr/bin/smartdns", StateDir: filepath.Join(root, "state")})
	if err != nil {
		t.Fatal(err)
	}
	launcher := &fakeLauncher{}
	var probedAddress string
	probe := func(_ context.Context, address string) error { probedAddress = address; return nil }
	r.setTestHooks(launcher, probe)
	cfg := Config{Enabled: true, ListenPort: 15353, DataDir: filepath.Join(root, "ignored"), Upstreams: []Upstream{{
		ID: "u1", ProfileID: "p1", Protocol: "doh", Host: "dns.example.com", BootstrapIP: "1.1.1.1", Enabled: true,
	}}}
	if err := r.Apply(context.Background(), cfg, []ProfileRef{{ID: "p1", Enabled: true, Listen: "127.0.0.1", Port: 16001}}); err != nil {
		t.Fatal(err)
	}
	status := r.Status()
	if status.Running || status.Generation == "" || status.ConfigPath == "" {
		t.Fatalf("unexpected prepared-only status: %+v", status)
	}
	config, err := os.ReadFile(status.ConfigPath)
	if err != nil {
		t.Fatal(err)
	}
	if len(config) == 0 {
		t.Fatal("generated config is empty")
	}
	if len(launcher.processes) != 0 {
		t.Fatal("Apply started SmartDNS when no previous runtime was running")
	}
	if err := r.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !r.Status().Running || len(launcher.processes) != 1 {
		t.Fatal("SmartDNS did not start")
	}
	if probedAddress != "127.0.0.1:15353" {
		t.Fatalf("readiness probe used %q, want 127.0.0.1:15353", probedAddress)
	}
	if err := r.Stop(context.Background()); err != nil {
		t.Fatal(err)
	}
	if r.Status().Running {
		t.Fatal("SmartDNS did not stop")
	}
}

func TestRuntimeStartRetriesOnceAfterTransientLaunchFailure(t *testing.T) {
	root := t.TempDir()
	r, err := NewRuntime(RuntimeConfig{BinaryPath: "/usr/bin/smartdns", StateDir: filepath.Join(root, "state")})
	if err != nil {
		t.Fatal(err)
	}
	launcher := &fakeLauncher{failStarts: 1}
	r.setTestHooks(launcher, func(context.Context, string) error { return nil })
	cfg := Config{Enabled: true, ListenPort: 15353, Upstreams: []Upstream{{ID: "u1", ProfileID: "p1", Protocol: "doh", Host: "dns.example.com", BootstrapIP: "1.1.1.1", Enabled: true}}}
	if err := r.Apply(context.Background(), cfg, []ProfileRef{{ID: "p1", Enabled: true, Listen: "127.0.0.1", Port: 16001}}); err != nil {
		t.Fatal(err)
	}
	if err := r.Start(context.Background()); err != nil {
		t.Fatalf("SmartDNS did not recover on its single startup retry: %v", err)
	}
	if launcher.attempts != 2 || len(launcher.processes) != 1 || !r.Status().Running {
		t.Fatalf("startup retry attempts=%d processes=%d status=%+v", launcher.attempts, len(launcher.processes), r.Status())
	}
	if err := r.Stop(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestRuntimeApplyRollsBackToPreviousGenerationWhenCandidateFails(t *testing.T) {
	root := t.TempDir()
	r, err := NewRuntime(RuntimeConfig{BinaryPath: "/usr/bin/smartdns", StateDir: filepath.Join(root, "state")})
	if err != nil {
		t.Fatal(err)
	}
	launcher := &fakeLauncher{}
	probe := func(_ context.Context, _ string) error { return nil }
	r.setTestHooks(launcher, probe)
	profile := []ProfileRef{{ID: "p1", Enabled: true, Listen: "127.0.0.1", Port: 16001}}
	cfg := Config{Enabled: true, ListenPort: 15353, Upstreams: []Upstream{{ID: "u1", ProfileID: "p1", Protocol: "doh", Host: "dns.example.com", BootstrapIP: "1.1.1.1", Enabled: true}}}
	if err := r.Apply(context.Background(), cfg, profile); err != nil {
		t.Fatal(err)
	}
	if err := r.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	oldGeneration := r.Status().Generation
	launcher.mu.Lock()
	launcher.failStarts = 2
	launcher.mu.Unlock()
	cfg.CacheSize = 128
	if err := r.Apply(context.Background(), cfg, profile); err == nil {
		t.Fatal("expected candidate launch failure")
	}
	status := r.Status()
	if !status.Running || status.Generation != oldGeneration {
		t.Fatalf("previous SmartDNS generation was not restored: %+v", status)
	}
	if len(launcher.processes) != 2 {
		t.Fatalf("expected candidate failure and previous generation restart, got %d successful launches", len(launcher.processes))
	}
}

func TestRuntimeStatusDetectsUnexpectedProcessExit(t *testing.T) {
	root := t.TempDir()
	r, err := NewRuntime(RuntimeConfig{BinaryPath: "/usr/bin/smartdns", StateDir: filepath.Join(root, "state")})
	if err != nil {
		t.Fatal(err)
	}
	launcher := &fakeLauncher{}
	r.setTestHooks(launcher, func(context.Context, string) error { return nil })
	cfg := Config{Enabled: true, ListenPort: 15353, Upstreams: []Upstream{{ID: "u1", ProfileID: "p1", Protocol: "doh", Host: "dns.example.com", BootstrapIP: "1.1.1.1", Enabled: true}}}
	if err := r.Apply(context.Background(), cfg, []ProfileRef{{ID: "p1", Enabled: true, Listen: "127.0.0.1", Port: 16001}}); err != nil {
		t.Fatal(err)
	}
	if err := r.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	launcher.processes[0].exit(fmt.Errorf("simulated crash"))
	deadline := time.Now().Add(time.Second)
	for r.Status().Running && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	status := r.Status()
	if status.Running || status.LastError == "" {
		t.Fatalf("unexpected process exit was not reflected in status: %+v", status)
	}
}

func TestRuntimeApplyRestoresPreviousGenerationAfterCallerCancellation(t *testing.T) {
	root := t.TempDir()
	r, err := NewRuntime(RuntimeConfig{BinaryPath: "/usr/bin/smartdns", StateDir: filepath.Join(root, "state")})
	if err != nil {
		t.Fatal(err)
	}
	launcher := &fakeLauncher{}
	profile := []ProfileRef{{ID: "p1", Enabled: true, Listen: "127.0.0.1", Port: 16001}}
	cfg := Config{Enabled: true, ListenPort: 15353, Upstreams: []Upstream{{ID: "u1", ProfileID: "p1", Protocol: "doh", Host: "dns.example.com", BootstrapIP: "1.1.1.1", Enabled: true}}}
	var cancel context.CancelFunc
	probeCalls := 0
	probe := func(ctx context.Context, _ string) error {
		probeCalls++
		if probeCalls == 2 {
			cancel()
			return ctx.Err()
		}
		return nil
	}
	r.setTestHooks(launcher, probe)
	if err := r.Apply(context.Background(), cfg, profile); err != nil {
		t.Fatal(err)
	}
	if err := r.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	oldGeneration := r.Status().Generation
	ctx, stop := context.WithCancel(context.Background())
	cancel = stop
	cfg.CacheSize = 128
	if err := r.Apply(ctx, cfg, profile); err == nil {
		t.Fatal("expected candidate startup to fail after caller cancellation")
	}
	status := r.Status()
	if !status.Running || status.Generation != oldGeneration {
		t.Fatalf("previous generation was not restored after cancellation: %+v", status)
	}
	if launcher.attempts != 3 {
		t.Fatalf("startup attempts=%d, want initial + canceled candidate + independent rollback", launcher.attempts)
	}
}

func TestRuntimeRejectsNonLoopbackReadinessAddress(t *testing.T) {
	_, err := NewRuntime(RuntimeConfig{BinaryPath: "/usr/bin/smartdns", StateDir: filepath.Join(t.TempDir(), "state"), ListenAddr: "0.0.0.0:5353"})
	if err == nil {
		t.Fatal("non-loopback readiness address was accepted")
	}
}

func TestRuntimeRejectsRelativePaths(t *testing.T) {
	if _, err := NewRuntime(RuntimeConfig{BinaryPath: "smartdns", StateDir: "/tmp/state"}); err == nil {
		t.Fatal("relative binary path was accepted")
	}
	if _, err := NewRuntime(RuntimeConfig{BinaryPath: "/usr/bin/smartdns", StateDir: "state"}); err == nil {
		t.Fatal("relative state path was accepted")
	}
}

func TestRuntimeHonorsCanceledStartContext(t *testing.T) {
	root := t.TempDir()
	r, err := NewRuntime(RuntimeConfig{BinaryPath: "/usr/bin/smartdns", StateDir: filepath.Join(root, "state")})
	if err != nil {
		t.Fatal(err)
	}
	launcher := &fakeLauncher{}
	probe := func(ctx context.Context, _ string) error { return ctx.Err() }
	r.setTestHooks(launcher, probe)
	cfg := Config{Enabled: true, ListenPort: 15353, Upstreams: []Upstream{{ID: "u1", ProfileID: "p1", Protocol: "doh", Host: "dns.example.com", BootstrapIP: "1.1.1.1", Enabled: true}}}
	if err := r.Apply(context.Background(), cfg, []ProfileRef{{ID: "p1", Enabled: true, Listen: "127.0.0.1", Port: 16001}}); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := r.Start(ctx); err == nil {
		t.Fatal("canceled start context unexpectedly succeeded")
	}
	if launcher.attempts != 1 {
		t.Fatalf("canceled startup made %d attempts, want no retry after cancellation", launcher.attempts)
	}
}

func TestRuntimeApplyRequiresProfilePinnedUpstream(t *testing.T) {
	root := t.TempDir()
	r, err := NewRuntime(RuntimeConfig{BinaryPath: "/usr/bin/smartdns", StateDir: filepath.Join(root, "state")})
	if err != nil {
		t.Fatal(err)
	}
	cfg := Config{Enabled: true, ListenPort: 15353, Upstreams: []Upstream{{ID: "u1", ProfileID: "missing", Protocol: "doh", Host: "dns.example.com", BootstrapIP: "1.1.1.1", Enabled: true}}}
	if err := r.Apply(context.Background(), cfg, nil); err == nil {
		t.Fatal("upstream referencing missing profile was accepted")
	}
}

func TestRuntimeStopIsIdempotent(t *testing.T) {
	r, err := NewRuntime(RuntimeConfig{BinaryPath: "/usr/bin/smartdns", StateDir: filepath.Join(t.TempDir(), "state")})
	if err != nil {
		t.Fatal(err)
	}
	if err := r.Stop(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := r.Stop(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestRuntimeReadinessProbeRejectsNonDNSResponse(t *testing.T) {
	// A closed loopback UDP port must not be mistaken for a ready DNS service.
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	if err := probeDNS(ctx, "127.0.0.1:1"); err == nil {
		t.Fatal("closed DNS port passed readiness probe")
	}
}

func TestRuntimeRecoversUnexpectedExitButHonorsManualStop(t *testing.T) {
	root := t.TempDir()
	r, err := NewRuntime(RuntimeConfig{BinaryPath: "/usr/bin/smartdns", StateDir: filepath.Join(root, "state")})
	if err != nil {
		t.Fatal(err)
	}
	launcher := &fakeLauncher{}
	r.setTestHooks(launcher, func(context.Context, string) error { return nil })
	cfg := Config{Enabled: true, ListenPort: 15353, Upstreams: []Upstream{{ID: "u1", ProfileID: "p1", Protocol: "doh", Host: "dns.example.com", BootstrapIP: "1.1.1.1", Enabled: true}}}
	profiles := []ProfileRef{{ID: "p1", Enabled: true, Listen: "127.0.0.1", Port: 16001}}
	if err := r.Apply(context.Background(), cfg, profiles); err != nil {
		t.Fatal(err)
	}
	r.SetDesiredRunning(true)
	if err := r.Recover(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !r.Status().Running {
		t.Fatal("boot recovery did not start desired SmartDNS")
	}
	launcher.processes[0].exit(fmt.Errorf("simulated crash"))
	deadline := time.Now().Add(time.Second)
	for r.Status().Running && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if !r.Status().Desired {
		t.Fatal("unexpected exit cleared desired-running state")
	}
	if err := r.Recover(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !r.Status().Running || launcher.attempts != 2 {
		t.Fatalf("recovery failed: attempts=%d status=%+v", launcher.attempts, r.Status())
	}
	if err := r.Stop(context.Background()); err != nil {
		t.Fatal(err)
	}
	if r.Status().Desired {
		t.Fatal("manual Stop did not clear desired-running state")
	}
	if err := r.Recover(context.Background()); err != nil {
		t.Fatal(err)
	}
	if r.Status().Running || launcher.attempts != 2 {
		t.Fatalf("recovery overrode manual Stop: attempts=%d status=%+v", launcher.attempts, r.Status())
	}
}

func TestRuntimeRecoversUnresponsiveProcessAfterThreeChecks(t *testing.T) {
	root := t.TempDir()
	r, err := NewRuntime(RuntimeConfig{BinaryPath: "/usr/bin/smartdns", StateDir: filepath.Join(root, "state")})
	if err != nil {
		t.Fatal(err)
	}
	launcher := &fakeLauncher{}
	unresponsive := false
	r.setTestHooks(launcher, func(context.Context, string) error {
		if unresponsive {
			return errors.New("simulated DNS timeout")
		}
		return nil
	})
	cfg := Config{Enabled: true, ListenPort: 15353, Upstreams: []Upstream{{ID: "u1", ProfileID: "p1", Protocol: "doh", Host: "dns.example.com", BootstrapIP: "1.1.1.1", Enabled: true}}}
	if err := r.Apply(context.Background(), cfg, []ProfileRef{{ID: "p1", Enabled: true, Listen: "127.0.0.1", Port: 16001}}); err != nil {
		t.Fatal(err)
	}
	r.SetDesiredRunning(true)
	if err := r.Recover(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(launcher.processes) != 1 {
		t.Fatal("initial process missing")
	}
	unresponsive = true
	for i := 0; i < 2; i++ {
		if err := r.Recover(context.Background()); err == nil {
			t.Fatalf("probe %d should fail", i+1)
		}
		if len(launcher.processes) != 1 {
			t.Fatal("restarted before failure threshold")
		}
	}
	unresponsive = false
	if err := r.Recover(context.Background()); err != nil {
		t.Fatal(err)
	}
	unresponsive = true
	for i := 0; i < 2; i++ {
		_ = r.Recover(context.Background())
	}
	unresponsive = false
	if err := r.Recover(context.Background()); err != nil {
		t.Fatal(err)
	}
	// A third consecutive failure must restart the child without clearing desired state.
	unresponsive = true
	for i := 0; i < 2; i++ {
		_ = r.Recover(context.Background())
	}
	// Allow the replacement to become ready.
	probes := 0
	r.setTestHooks(nil, func(context.Context, string) error {
		probes++
		if probes <= 1 {
			return errors.New("simulated DNS timeout")
		}
		return nil
	})
	if err := r.Recover(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(launcher.processes) != 2 || !r.Status().Running || !r.Status().Desired {
		t.Fatalf("unresponsive SmartDNS was not restarted: processes=%d status=%+v", len(launcher.processes), r.Status())
	}
	if err := r.Stop(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := r.Recover(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(launcher.processes) != 2 {
		t.Fatal("operator stop was overridden")
	}
}
