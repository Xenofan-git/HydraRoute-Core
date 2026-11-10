package api

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"google.golang.org/grpc"

	"hydraroute.local/core/internal/compiler"
	"hydraroute.local/core/internal/parser"
	"hydraroute.local/core/internal/profiles"
	hrruntime "hydraroute.local/core/internal/runtime"
	"hydraroute.local/core/internal/service"
	"hydraroute.local/core/internal/state"
	"hydraroute.local/core/internal/xrayapi"
	"hydraroute.local/core/internal/xrayapi/command"
	xrayobservatory "hydraroute.local/core/internal/xrayapi/observatory"
)

func TestSubscriptionRefreshReplacesPoolAndPreservesProfileEndpoint(t *testing.T) {
	stateDir := filepath.Join(t.TempDir(), "state")
	h := NewWithStateDir(apiSupervisor(t), stateDir)
	oldSubscription := "vless://00000000-0000-4000-8000-000000000001@old.example:443#old"
	newSubscription := strings.Join([]string{
		"vless://00000000-0000-4000-8000-000000000002@new-a.example:443#new-a",
		"vless://00000000-0000-4000-8000-000000000003@new-b.example:443#new-b",
	}, "\n")
	profile := profiles.Profile{
		ID: "subscription", SourceID: "providerA", Enabled: true, Port: 16311,
		SubscriptionURL: "https://provider.example/subscription",
		CachedSubscription: oldSubscription, LastSubscriptionUpdate: 100,
	}
	if err := h.profileStore.Save(profile); err != nil {
		t.Fatal(err)
	}
	var fetches int
	h.subscriptionFetcher = func(_ context.Context, rawURL string) (string, error) {
		fetches++
		if rawURL != profile.SubscriptionURL {
			t.Fatalf("fetch URL=%q, want %q", rawURL, profile.SubscriptionURL)
		}
		return newSubscription, nil
	}
	if _, err := h.refreshProfilesModeWithFallbacks(context.Background(), true, "", nil); err != nil {
		t.Fatalf("refresh subscription: %v", err)
	}
	if fetches != 1 {
		t.Fatalf("subscription fetches=%d, want 1", fetches)
	}
	saved, err := h.profileStore.Get(profile.ID)
	if err != nil {
		t.Fatal(err)
	}
	if saved.CachedSubscription != newSubscription || saved.LastSubscriptionUpdate <= profile.LastSubscriptionUpdate {
		t.Fatalf("subscription cache was not updated: last_update=%d body=%q", saved.LastSubscriptionUpdate, saved.CachedSubscription)
	}
	if saved.ID != profile.ID || saved.SourceID != profile.SourceID || saved.Port != profile.Port || saved.SubscriptionURL != profile.SubscriptionURL {
		t.Fatalf("profile identity/endpoint changed during refresh: %+v", saved)
	}
	config, err := h.supervisor.SnapshotConfig()
	if err != nil {
		t.Fatal(err)
	}
	text := string(config)
	if !strings.Contains(text, "new-a.example") || !strings.Contains(text, "new-b.example") || strings.Contains(text, "old.example") {
		t.Fatalf("compiled config does not contain exactly the refreshed pool: %s", text)
	}
	if !strings.Contains(text, "16311") || !strings.Contains(text, "hr_bal_subscription") {
		t.Fatalf("refreshed profile lost its SOCKS endpoint or balancer: %s", text)
	}
}

func TestProfileDeleteRebuildsConfigAndClearsObservatoryCache(t *testing.T) {
	stateDir := filepath.Join(t.TempDir(), "state")
	h := NewWithStateDir(apiSupervisor(t), stateDir)
	alpha := profiles.Profile{ID: "alpha", SourceID: "providerA", Enabled: true, Port: 16321, Links: []string{
		"vless://00000000-0000-4000-8000-000000000011@alpha.example:443#alpha",
	}}
	beta := profiles.Profile{ID: "beta", SourceID: "providerB", Enabled: true, Port: 16322, Links: []string{
		"vless://00000000-0000-4000-8000-000000000012@beta.example:443#beta",
	}}
	for _, profile := range []profiles.Profile{alpha, beta} {
		if err := h.profileStore.Save(profile); err != nil {
			t.Fatal(err)
		}
	}
	if err := service.SaveSelection(stateDir, state.Profile{ID: "alpha", SourceID: "providerA", PickMode: "auto"}); err != nil {
		t.Fatal(err)
	}
	if _, err := h.refreshProfilesCachedLocked(context.Background()); err != nil {
		t.Fatalf("initial compile: %v", err)
	}
	h.probeCache["alpha"] = map[string]xrayapi.OutboundStatus{
		"alpha-key": {Tag: "hr_out_alpha", Alive: true, DelayMS: 35, LastTryTime: 1762800000},
	}
	h.probeCache["beta"] = map[string]xrayapi.OutboundStatus{
		"beta-key": {Tag: "hr_out_beta", Alive: true, DelayMS: 55, LastTryTime: 1762800001},
	}
	if err := h.saveProbeCache(); err != nil {
		t.Fatal(err)
	}

	deleteProfile := func(id string) {
		t.Helper()
		req := localPost("/v1/profiles", `{"id":"`+id+`"}`)
		req.Method = "DELETE"
		rr := httptest.NewRecorder()
		h.mux.ServeHTTP(rr, req)
		if rr.Code != 200 {
			t.Fatalf("delete %s status=%d body=%s", id, rr.Code, rr.Body.String())
		}
	}
	deleteProfile("alpha")
	if _, err := h.profileStore.Get("alpha"); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("deleted profile still exists: err=%v", err)
	}
	if _, err := service.LoadSelection(stateDir, "alpha"); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("deleted profile selection still exists: err=%v", err)
	}
	config, err := h.supervisor.SnapshotConfig()
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(config), "hr_bal_alpha") || !strings.Contains(string(config), "hr_bal_beta") || !strings.Contains(string(config), "beta.example") {
		t.Fatalf("profile deletion did not rebuild the aggregate config: %s", config)
	}
	if _, ok := h.probeCache["alpha"]; ok {
		t.Fatal("deleted profile Observatory cache remains in memory")
	}
	cacheData, err := os.ReadFile(filepath.Join(stateDir, "observatory", "status.json"))
	if err != nil {
		t.Fatal(err)
	}
	var persisted probeCacheFile
	if err := json.Unmarshal(cacheData, &persisted); err != nil {
		t.Fatal(err)
	}
	if _, ok := persisted.Profiles["alpha"]; ok {
		t.Fatal("deleted profile Observatory cache remains persisted")
	}
	if _, ok := persisted.Profiles["beta"]; !ok {
		t.Fatal("deleting alpha also removed beta Observatory cache")
	}

	deleteProfile("beta")
	config, err = h.supervisor.SnapshotConfig()
	if err != nil {
		t.Fatal(err)
	}
	var empty map[string]any
	if err := json.Unmarshal(config, &empty); err != nil {
		t.Fatal(err)
	}
	inbounds, ok := empty["inbounds"].([]any)
	if !ok || len(inbounds) != 0 {
		t.Fatalf("deleting the last profile left stale SOCKS inbounds: %#v", empty["inbounds"])
	}
	if len(h.probeCache) != 0 {
		t.Fatalf("last profile Observatory cache remains: %#v", h.probeCache)
	}
}

type lifecycleObservatoryServer struct {
	command.UnimplementedObservatoryServiceServer
	mu     sync.RWMutex
	status *xrayobservatory.OutboundStatus
}

func (s *lifecycleObservatoryServer) setStatus(tag string, delay, lastTry int64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.status = &xrayobservatory.OutboundStatus{
		OutboundTag: tag, Alive: true, Delay: delay,
		LastTryTime: lastTry, LastSeenTime: lastTry,
	}
}

func (s *lifecycleObservatoryServer) GetOutboundStatus(context.Context, *command.GetOutboundStatusRequest) (*command.GetOutboundStatusResponse, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.status == nil {
		return &command.GetOutboundStatusResponse{Status: &xrayobservatory.ObservationResult{}}, nil
	}
	copy := *s.status
	return &command.GetOutboundStatusResponse{Status: &xrayobservatory.ObservationResult{Status: []*xrayobservatory.OutboundStatus{&copy}}}, nil
}

func TestObservatoryRecoversAfterManagedXrayRestart(t *testing.T) {
	bin := filepath.Join(t.TempDir(), "xray")
	script := "#!/bin/sh\nif [ \"$2\" = -test ]; then exit 0; fi\ntrap 'exit 0' TERM\nwhile :; do sleep 0.1; done\n"
	if err := os.WriteFile(bin, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	sup, err := hrruntime.NewSupervisor(bin, filepath.Join(t.TempDir(), "private", "config.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := sup.Prepare(context.Background(), []byte(`{"inbounds":[],"outbounds":[]}`)); err != nil {
		t.Fatal(err)
	}
	if err := sup.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer sup.Stop(context.Background())

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	grpcServer := grpc.NewServer()
	observatoryServer := &lifecycleObservatoryServer{}
	command.RegisterObservatoryServiceServer(grpcServer, observatoryServer)
	go func() { _ = grpcServer.Serve(listener) }()
	t.Cleanup(func() {
		grpcServer.Stop()
		_ = listener.Close()
	})

	stateDir := filepath.Join(t.TempDir(), "state")
	h := NewWithStateDir(sup, stateDir)
	h.observatoryAddress = listener.Addr().String()
	profile := profiles.Profile{ID: "obs", SourceID: "providerA", Enabled: true, Port: 16331, Links: []string{
		"vless://00000000-0000-4000-8000-000000000021@observed.example:443#observed",
	}}
	if err := h.profileStore.Save(profile); err != nil {
		t.Fatal(err)
	}
	preview, err := service.PreviewProfileCached(context.Background(), profile, stateDir)
	if err != nil || len(preview.Nodes) != 1 {
		t.Fatalf("preview nodes=%d err=%v", len(preview.Nodes), err)
	}
	keyBytes, err := json.Marshal(preview.Nodes[0].Key)
	if err != nil {
		t.Fatal(err)
	}
	tag := compiler.OutboundTag(profile.ID, string(keyBytes))
	observatoryServer.setStatus(tag, 80, 1762800100)

	first, err := h.collectObservatoryStatus(context.Background(), profile.ID, 0)
	if err != nil || !first.Available {
		t.Fatalf("initial Observatory status available=%v err=%v", first.Available, err)
	}
	key := string(keyBytes)
	if got := first.Statuses[key]; got.DelayMS != 80 || got.LastTryTime != 1762800100 || got.Stale {
		t.Fatalf("initial Observatory measurement=%+v", got)
	}
	if err := h.saveProbeCache(); err != nil {
		t.Fatal(err)
	}

	stopCtx, stopCancel := context.WithTimeout(context.Background(), 3*time.Second)
	if err := sup.Stop(stopCtx); err != nil {
		stopCancel()
		t.Fatalf("stop managed Xray: %v", err)
	}
	stopCancel()
	stopped, err := h.collectObservatoryStatus(context.Background(), profile.ID, 0)
	if err != nil || stopped.Available {
		t.Fatalf("stopped Observatory available=%v err=%v", stopped.Available, err)
	}
	if got := stopped.Statuses[key]; got.DelayMS != 80 || !got.Stale {
		t.Fatalf("last-known Observatory result was not retained while Xray stopped: %+v", got)
	}

	observatoryServer.setStatus(tag, 25, 1762800200)
	if err := sup.Start(context.Background()); err != nil {
		t.Fatalf("restart managed Xray: %v", err)
	}
	recovered, err := h.collectObservatoryStatus(context.Background(), profile.ID, 0)
	if err != nil || !recovered.Available {
		t.Fatalf("Observatory did not recover after Xray restart: available=%v err=%v", recovered.Available, err)
	}
	if got := recovered.Statuses[key]; got.DelayMS != 25 || got.LastTryTime != 1762800200 || got.Stale {
		t.Fatalf("recovered Observatory measurement=%+v", got)
	}
	if err := h.saveProbeCache(); err != nil {
		t.Fatal(err)
	}
	reloaded := NewWithStateDir(apiSupervisor(t), stateDir)
	if got := reloaded.probeCache[profile.ID][key]; got.DelayMS != 25 || got.LastTryTime != 1762800200 {
		t.Fatalf("latest Observatory measurement was not persisted: %+v", got)
	}
}

func TestRefreshProfilesRestartsManagedXrayProcess(t *testing.T) {
	bin := filepath.Join(t.TempDir(), "xray")
	script := "#!/bin/sh\nif [ \"$2\" = -test ]; then exit 0; fi\ntrap 'exit 0' TERM\nwhile :; do sleep 0.1; done\n"
	if err := os.WriteFile(bin, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	sup, err := hrruntime.NewSupervisor(bin, filepath.Join(t.TempDir(), "private", "config.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := sup.Prepare(context.Background(), []byte(`{"inbounds":[],"outbounds":[]}`)); err != nil {
		t.Fatal(err)
	}
	if err := sup.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer sup.Stop(context.Background())
	before := sup.Status().PID
	h := NewWithStateDir(sup, filepath.Join(t.TempDir(), "state"))
	if err := h.profileStore.Save(profiles.Profile{ID: "restart", SourceID: "providerA", Enabled: true, Port: 16341, Links: []string{
		"vless://00000000-0000-4000-8000-000000000031@restart.example:443#restart",
	}}); err != nil {
		t.Fatal(err)
	}
	rr := httptest.NewRecorder()
	h.mux.ServeHTTP(rr, localPost("/v1/profiles/refresh", `{}`))
	if rr.Code != 200 {
		t.Fatalf("refresh status=%d body=%s", rr.Code, rr.Body.String())
	}
	after := sup.Status()
	if !after.Running || after.PID == 0 || after.PID == before {
		t.Fatalf("refresh did not replace the managed Xray process: before=%d after=%+v", before, after)
	}
}
