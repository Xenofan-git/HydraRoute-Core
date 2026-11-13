package geodata

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// Tests the service environment transition with real Xray subprocesses.
// No systemd unit is created or modified; this models its asset-path override.
func TestLiveXrayServiceAssetPathMigrationAndRevert(t *testing.T) {
	binary, assets := os.Getenv("HRCORE_TEST_XRAY"), os.Getenv("HRCORE_TEST_XRAY_ASSETS")
	if binary == "" || assets == "" {
		t.Skip("requires real Xray and assets")
	}
	root := t.TempDir()
	legacy := filepath.Join(root, "legacy")
	if err := os.Mkdir(legacy, 0700); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"geoip.dat", "geosite.dat"} {
		data, err := os.ReadFile(filepath.Join(assets, name))
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(legacy, name), data, 0600); err != nil {
			t.Fatal(err)
		}
	}
	config := filepath.Join(root, "config.json")
	cfg := `{"log":{"loglevel":"warning"},"inbounds":[],"outbounds":[{"protocol":"freedom","tag":"direct"}],"routing":{"rules":[{"type":"field","domain":["geosite:google"],"outboundTag":"direct"},{"type":"field","ip":["geoip:private"],"outboundTag":"direct"}]}}`
	if err := os.WriteFile(config, []byte(cfg), 0600); err != nil {
		t.Fatal(err)
	}
	store := &GenerationStore{Root: filepath.Join(root, "state", "geodata")}
	if err := os.MkdirAll(store.Root, 0700); err != nil {
		t.Fatal(err)
	}
	check := XrayCheck{Binary: binary, Config: config, Timeout: 10 * time.Second}.Check
	audit, err := AuditLegacyAssets(legacy)
	if err != nil || len(audit.Extra) != 0 {
		t.Fatalf("legacy audit: %+v %v", audit, err)
	}
	initial, err := store.Bootstrap(legacy, check)
	if err != nil {
		t.Fatal(err)
	}
	if initial != "generation-0" {
		t.Fatalf("unexpected initial generation: %s", initial)
	}
	r := &isolatedXrayRuntime{binary: binary, config: config, assets: legacy}
	if err := r.Start(context.Background()); err != nil {
		t.Fatalf("legacy start: %v", err)
	}
	defer func() {
		if r.IsRunning() {
			_ = r.Stop(context.Background())
		}
	}()
	// Explicit operator-style stop, change asset path, start; then revert.
	if err := r.Stop(context.Background()); err != nil {
		t.Fatal(err)
	}
	r.assets = filepath.Join(store.Root, "current")
	if err := r.Start(context.Background()); err != nil {
		t.Fatalf("managed start: %v", err)
	}
	if err := r.Stop(context.Background()); err != nil {
		t.Fatal(err)
	}
	r.assets = legacy
	if err := r.Start(context.Background()); err != nil {
		t.Fatalf("legacy revert start: %v", err)
	}
	if !r.IsRunning() {
		t.Fatal("legacy Xray failed to recover")
	}
	for _, name := range []string{"geoip.dat", "geosite.dat"} {
		before, err := os.ReadFile(filepath.Join(assets, name))
		if err != nil {
			t.Fatal(err)
		}
		after, err := os.ReadFile(filepath.Join(legacy, name))
		if err != nil {
			t.Fatal(err)
		}
		if string(before) != string(after) {
			t.Fatalf("legacy %s changed", name)
		}
	}
	if err := CheckStartupReady(store.Root); err != nil {
		t.Fatalf("managed store not ready after revert: %v", err)
	}
	t.Log("real Xray: legacy -> managed -> legacy asset environment transition passed")
}
