package geodata

import (
	"context"
	"errors"
	"hydraroute.local/core/internal/xrayapi"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

// Opt-in test: HRCORE_TEST_XRAY and HRCORE_TEST_XRAY_ASSETS must point to
// a real AMD64 Xray binary and trusted GeoIP/GeoSite files. No system services
// or listeners are modified; all processes and assets are private to TempDir.
type isolatedXrayRuntime struct {
	binary, config, assets string
	cmd                    *exec.Cmd
}

func (r *isolatedXrayRuntime) IsConfigured() bool { return true }
func (r *isolatedXrayRuntime) IsRunning() bool    { return r.cmd != nil && r.cmd.ProcessState == nil }
func (r *isolatedXrayRuntime) Start(ctx context.Context) error {
	if r.IsRunning() {
		return errors.New("already running")
	}
	cmd := exec.Command(r.binary, "run", "-config", r.config)
	cmd.Env = append(os.Environ(), "XRAY_LOCATION_ASSET="+r.assets)
	if err := cmd.Start(); err != nil {
		return err
	}
	r.cmd = cmd
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-time.After(200 * time.Millisecond):
	}
	if err := cmd.Process.Signal(syscall.Signal(0)); err != nil {
		return err
	}
	return nil
}
func (r *isolatedXrayRuntime) Stop(ctx context.Context) error {
	if !r.IsRunning() {
		return errors.New("not running")
	}
	cmd := r.cmd
	if err := cmd.Process.Signal(os.Interrupt); err != nil {
		return err
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case <-ctx.Done():
		_ = cmd.Process.Kill()
		<-done
		return ctx.Err()
	case <-done:
		r.cmd = nil
		return nil
	}
}
func TestLiveXrayGeoDataActivationAndRollback(t *testing.T) {
	binary, assets := os.Getenv("HRCORE_TEST_XRAY"), os.Getenv("HRCORE_TEST_XRAY_ASSETS")
	if binary == "" || assets == "" {
		t.Skip("set HRCORE_TEST_XRAY and HRCORE_TEST_XRAY_ASSETS for isolated real-Xray integration")
	}
	root := t.TempDir()
	ip, err := os.ReadFile(filepath.Join(assets, "geoip.dat"))
	if err != nil {
		t.Fatal(err)
	}
	site, err := os.ReadFile(filepath.Join(assets, "geosite.dat"))
	if err != nil {
		t.Fatal(err)
	}
	config := filepath.Join(root, "config.json")
	cfg := `{"log":{"loglevel":"warning"},"inbounds":[],"outbounds":[{"protocol":"freedom","tag":"direct"}],"routing":{"rules":[{"type":"field","domain":["geosite:google"],"outboundTag":"direct"},{"type":"field","ip":["geoip:private"],"outboundTag":"direct"}]}}`
	if err := os.WriteFile(config, []byte(cfg), 0600); err != nil {
		t.Fatal(err)
	}
	store := &GenerationStore{Root: filepath.Join(root, "store")}
	if err := os.Mkdir(store.Root, 0700); err != nil {
		t.Fatal(err)
	}
	check := XrayCheck{Binary: binary, Config: config, Timeout: 10 * time.Second}.Check
	baseline, err := store.Publish(ip, site, check)
	if err != nil {
		t.Fatal(err)
	}
	runtime := &isolatedXrayRuntime{binary: binary, config: config, assets: filepath.Join(store.Root, "current")}
	if err := runtime.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer func() {
		if runtime.IsRunning() {
			_ = runtime.Stop(context.Background())
		}
	}()
	healthy := func(ctx context.Context) error {
		if !runtime.IsRunning() {
			return errors.New("Xray not running")
		}
		return ctx.Err()
	}
	next, err := store.Activate(context.Background(), ip, site, check, runtime, healthy)
	if err != nil || next == baseline {
		t.Fatalf("live activation: %s %v", next, err)
	}
	if err := CheckStartupReady(store.Root); err != nil {
		t.Fatalf("successful activation left journal: %v", err)
	}
	attempts := 0
	_, err = store.Activate(context.Background(), ip, site, check, runtime, func(ctx context.Context) error {
		attempts++
		if attempts == 1 {
			return errors.New("simulated control API failure")
		}
		return healthy(ctx)
	})
	if err == nil {
		t.Fatal("accepted failed post-switch health probe")
	}
	current, err := store.Current()
	if err != nil || current != next {
		t.Fatalf("rollback: current=%s expected=%s err=%v", current, next, err)
	}
	if !runtime.IsRunning() {
		t.Fatal("previous Xray not running after rollback")
	}
	if err := CheckStartupReady(store.Root); err != nil {
		t.Fatalf("rollback left journal: %v", err)
	}
	// Exercise the actual gRPC client against an unreachable control endpoint
	// while the real Xray process remains alive. A failed control API must not
	// be mistaken for a healthy activation.
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	unavailable := listener.Addr().String()
	_ = listener.Close()
	attempts = 0
	_, err = store.Activate(context.Background(), ip, site, check, runtime, func(ctx context.Context) error {
		attempts++
		if attempts > 1 {
			return healthy(ctx)
		} // previous generation recovery
		probeCtx, cancel := context.WithTimeout(ctx, 400*time.Millisecond)
		defer cancel()
		_, probeErr := xrayapi.GetOutboundStatus(probeCtx, unavailable)
		if probeErr == nil {
			return errors.New("unreachable control API unexpectedly healthy")
		}
		return probeErr
	})
	if err == nil || attempts != 2 {
		t.Fatalf("control API failure not rolled back: attempts=%d err=%v", attempts, err)
	}
	current, err = store.Current()
	if err != nil || current != next {
		t.Fatalf("control API rollback current=%s expected=%s err=%v", current, next, err)
	}
	if !runtime.IsRunning() {
		t.Fatal("Xray stopped after real gRPC control failure")
	}
	if err := CheckStartupReady(store.Root); err != nil {
		t.Fatalf("control API rollback left journal: %v", err)
	}
	t.Logf("real Xray activation and health-failure rollback passed; initial=%s updated=%s", baseline, next)
}
