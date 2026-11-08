package api

import (
	"context"
	"encoding/json"
	"hydraroute.local/core/internal/profiles"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	hrruntime "hydraroute.local/core/internal/runtime"
)

func localGet(path string) *http.Request {
	r := httptest.NewRequest(http.MethodGet, path, nil)
	r.Host = "127.0.0.1:17890"
	r.RemoteAddr = "127.0.0.1:12345"
	return r
}

func localPost(path, body string) *http.Request {
	r := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
	r.Host = "127.0.0.1:17890"
	r.RemoteAddr = "127.0.0.1:12345"
	r.Header.Set("Content-Type", "application/json")
	return r
}

func localPut(path, body string) *http.Request {
	r := httptest.NewRequest(http.MethodPut, path, strings.NewReader(body))
	r.Host = "127.0.0.1:17890"
	r.RemoteAddr = "127.0.0.1:12345"
	r.Header.Set("Content-Type", "application/json")
	return r
}

func apiSupervisor(t *testing.T) *hrruntime.Supervisor {
	t.Helper()
	bin := filepath.Join(t.TempDir(), "xray")
	script := "#!/bin/sh\nif [ \"$2\" = -test ]; then exit 0; fi\nsleep 20\n"
	if err := os.WriteFile(bin, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	s, err := hrruntime.NewSupervisor(bin, filepath.Join(t.TempDir(), "private", "config.json"))
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func TestHealthAndMethodHandling(t *testing.T) {
	h := New(apiSupervisor(t))
	rr := httptest.NewRecorder()
	h.mux.ServeHTTP(rr, localGet("/healthz"))
	if rr.Code != 200 {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
	}
	var result map[string]any
	if err := json.Unmarshal(rr.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if result["status"] != "ok" {
		t.Fatalf("result=%v", result)
	}
	rr = httptest.NewRecorder()
	h.mux.ServeHTTP(rr, httptest.NewRequest(http.MethodPost, "/healthz", nil))
	if rr.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status=%d", rr.Code)
	}
}

func TestConfigEndpointCompilesAndPreparesOnly(t *testing.T) {
	h := New(apiSupervisor(t))
	body := `{"profiles":[{"id":"foreign","enabled":true,"port":16100,"nodes":[{"key":"node-A","outbound":{"protocol":"freedom"}}]}]}`
	rr := httptest.NewRecorder()
	h.mux.ServeHTTP(rr, localPost("/v1/config", body))
	if rr.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
	}
	st := h.supervisor.Status()
	if !st.Configured || st.Running {
		t.Fatalf("status=%+v", st)
	}
}

func TestRoutingEndpointPersistsAndPreparesSharedSOCKS(t *testing.T) {
	stateDir := filepath.Join(t.TempDir(), "state")
	h := NewWithStateDir(apiSupervisor(t), stateDir)
	profile := profiles.Profile{ID: "foreign", SourceID: "providerA", Enabled: true, Port: 16120,
		Links: []string{"vless://00000000-0000-4000-8000-000000000001@foreign.example:443#foreign"}}
	if err := h.profileStore.Save(profile); err != nil {
		t.Fatal(err)
	}
	body := `{"enabled":true,"listen":"127.0.0.1","port":16121,"default_action":"block","rules":[{"id":"ru","name":"Russian services","enabled":true,"match_type":"domain","values":["geosite:category-ru"],"action":"direct"}]}`
	rr := httptest.NewRecorder()
	h.mux.ServeHTTP(rr, localPut("/v1/routing", body))
	if rr.Code != http.StatusOK {
		t.Fatalf("PUT status=%d body=%s", rr.Code, rr.Body.String())
	}
	config, err := h.supervisor.SnapshotConfig()
	if err != nil {
		t.Fatal(err)
	}
	var compiled map[string]any
	if err := json.Unmarshal(config, &compiled); err != nil {
		t.Fatal(err)
	}
	if got := len(compiled["inbounds"].([]any)); got != 2 {
		t.Fatalf("expected profile and shared inbounds, got %d", got)
	}
	rules := compiled["routing"].(map[string]any)["rules"].([]any)
	if got := len(rules); got != 3 {
		t.Fatalf("expected profile, custom and default rules, got %d", got)
	}
	rr = httptest.NewRecorder()
	h.mux.ServeHTTP(rr, localGet("/v1/routing"))
	if rr.Code != http.StatusOK || !strings.Contains(rr.Body.String(), `"configured":true`) {
		t.Fatalf("GET status=%d body=%s", rr.Code, rr.Body.String())
	}
}

func TestRoutingEndpointRollsBackInvalidConfig(t *testing.T) {
	stateDir := filepath.Join(t.TempDir(), "state")
	h := NewWithStateDir(apiSupervisor(t), stateDir)
	profile := profiles.Profile{ID: "foreign", SourceID: "providerA", Enabled: true, Port: 16130,
		Links: []string{"vless://00000000-0000-4000-8000-000000000001@foreign.example:443#foreign"}}
	if err := h.profileStore.Save(profile); err != nil {
		t.Fatal(err)
	}
	body := `{"enabled":true,"port":16131,"default_action":"profile","default_profile_id":"missing"}`
	rr := httptest.NewRecorder()
	h.mux.ServeHTTP(rr, localPut("/v1/routing", body))
	if rr.Code != http.StatusUnprocessableEntity {
		t.Fatalf("PUT status=%d body=%s", rr.Code, rr.Body.String())
	}
	rr = httptest.NewRecorder()
	h.mux.ServeHTTP(rr, localGet("/v1/routing"))
	if rr.Code != http.StatusOK || !strings.Contains(rr.Body.String(), `"configured":false`) {
		t.Fatalf("failed update was not rolled back: status=%d body=%s", rr.Code, rr.Body.String())
	}
	if h.supervisor.Status().Configured {
		t.Fatal("invalid routing config must not be prepared")
	}
}

func TestRuntimeEndpointRequiresPreparedConfig(t *testing.T) {
	h := New(apiSupervisor(t))
	rr := httptest.NewRecorder()
	h.mux.ServeHTTP(rr, localPost("/v1/runtime", `{"action":"start"}`))
	if rr.Code != http.StatusConflict {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
	}
}

func TestListenAddressMustBeLoopback(t *testing.T) {
	h := New(apiSupervisor(t))
	if err := h.ListenAndServe("0.0.0.0:17890"); err == nil {
		t.Fatal("expected non-loopback bind rejection")
	}
	if err := h.ListenAndServe(":17890"); err == nil {
		t.Fatal("expected wildcard bind rejection")
	}
}

func TestConfigRejectsInvalidCompileRequest(t *testing.T) {
	h := New(apiSupervisor(t))
	body := `{"profiles":[{"id":"bad","enabled":true,"port":16101,"listen":"0.0.0.0","nodes":[{"key":"x","outbound":{"protocol":"freedom"}}]}]}`
	rr := httptest.NewRecorder()
	h.mux.ServeHTTP(rr, localPost("/v1/config", body))
	if rr.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
	}
	if h.supervisor.Status().Configured {
		t.Fatal("invalid config must not be prepared")
	}
}

func TestRuntimeStopIsIdempotent(t *testing.T) {
	h := New(apiSupervisor(t))
	rr := httptest.NewRecorder()
	h.mux.ServeHTTP(rr, localPost("/v1/runtime", `{"action":"stop"}`))
	if rr.Code != http.StatusOK {
		t.Fatalf("status=%d", rr.Code)
	}
	_ = context.Background()
}

func TestServeContextShutdownStopsManagedRuntime(t *testing.T) {
	bin := filepath.Join(t.TempDir(), "xray")
	script := "#!/bin/sh\nif [ \"$2\" = -test ]; then exit 0; fi\ntrap 'exit 0' TERM\nwhile :; do sleep 0.1; done\n"
	if err := os.WriteFile(bin, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	supervisor, err := hrruntime.NewSupervisor(bin, filepath.Join(t.TempDir(), "private", "config.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := supervisor.Prepare(context.Background(), []byte(`{"inbounds":[],"outbounds":[]}`)); err != nil {
		t.Fatal(err)
	}
	if err := supervisor.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	h := New(supervisor)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- h.Serve(ctx, "127.0.0.1:0") }()
	time.Sleep(50 * time.Millisecond)
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("API did not shut down")
	}
	if supervisor.Status().Running {
		t.Fatal("managed Xray remains running after API shutdown")
	}
}

func TestMutatingEndpointsRejectCrossOriginAndNonJSON(t *testing.T) {
	h := New(apiSupervisor(t))
	req := localPost("/v1/runtime", `{"action":"stop"}`)
	req.Header.Set("Origin", "https://evil.example")
	rr := httptest.NewRecorder()
	h.mux.ServeHTTP(rr, req)
	if rr.Code != http.StatusForbidden {
		t.Fatalf("cross-origin status=%d", rr.Code)
	}
	req = localPost("/v1/runtime", `{"action":"stop"}`)
	req.Header.Set("Content-Type", "text/plain")
	rr = httptest.NewRecorder()
	h.mux.ServeHTTP(rr, req)
	if rr.Code != http.StatusForbidden {
		t.Fatalf("non-JSON status=%d", rr.Code)
	}
}

func TestMutatingEndpointsRejectNonLoopbackHostAndFetchSite(t *testing.T) {
	h := New(apiSupervisor(t))
	req := localPost("/v1/runtime", `{"action":"stop"}`)
	req.Host = "router.example:17890"
	rr := httptest.NewRecorder()
	h.mux.ServeHTTP(rr, req)
	if rr.Code != http.StatusForbidden {
		t.Fatalf("non-loopback host status=%d", rr.Code)
	}
	req = localPost("/v1/runtime", `{"action":"stop"}`)
	req.Header.Set("Sec-Fetch-Site", "cross-site")
	rr = httptest.NewRecorder()
	h.mux.ServeHTTP(rr, req)
	if rr.Code != http.StatusForbidden {
		t.Fatalf("fetch-site status=%d", rr.Code)
	}
}

func TestSubscriptionCompileAndSelectionEndpoints(t *testing.T) {
	stateDir := filepath.Join(t.TempDir(), "state")
	h := NewWithStateDir(apiSupervisor(t), stateDir)
	selection := `{"id":"foreign","source_id":"providerA","pick_mode":"manual","picked":[{"source":"providerA","name":"edge-A"}]}`
	req := localPost("/v1/selections", selection)
	req.Method = http.MethodPut
	rr := httptest.NewRecorder()
	h.mux.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("save status=%d body=%s", rr.Code, rr.Body.String())
	}
	get := httptest.NewRequest(http.MethodGet, "/v1/selections?profile_id=foreign", nil)
	get.Host = "127.0.0.1:17890"
	get.RemoteAddr = "127.0.0.1:12345"
	rr = httptest.NewRecorder()
	h.mux.ServeHTTP(rr, get)
	if rr.Code != http.StatusOK {
		t.Fatalf("load status=%d body=%s", rr.Code, rr.Body.String())
	}

	body := `{"source":"providerA","profile":{"id":"foreign","enabled":true,"port":16120},"links":["vless://00000000-0000-4000-8000-000000000001@edge-B.example:443#edge-B"]}`
	rr = httptest.NewRecorder()
	h.mux.ServeHTTP(rr, localPost("/v1/subscriptions/compile", body))
	if rr.Code != http.StatusUnprocessableEntity {
		t.Fatalf("missing selected identity should fail closed, status=%d body=%s", rr.Code, rr.Body.String())
	}
	if h.supervisor.Status().Configured {
		t.Fatal("failed subscription compile must not prepare config")
	}
}

func TestSubscriptionCompilePreparesConfigWithoutStartingXray(t *testing.T) {
	h := NewWithStateDir(apiSupervisor(t), filepath.Join(t.TempDir(), "state"))
	body := `{"source":"providerB","profile":{"id":"foreign","enabled":true,"port":16121},"links":["vless://00000000-0000-4000-8000-000000000001@edge.example:443#edge"]}`
	rr := httptest.NewRecorder()
	h.mux.ServeHTTP(rr, localPost("/v1/subscriptions/compile", body))
	if rr.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
	}
	if !h.supervisor.Status().Configured || h.supervisor.Status().Running {
		t.Fatalf("runtime=%+v", h.supervisor.Status())
	}
}

func TestSubscriptionCompileRejectsUnknownFieldsAndCrossOrigin(t *testing.T) {
	h := NewWithStateDir(apiSupervisor(t), t.TempDir())
	req := localPost("/v1/subscriptions/compile", `{"source":"s","profile":{"id":"p","enabled":true,"port":16122},"links":["vless://00000000-0000-4000-8000-000000000001@edge.example:443#edge"],"state_file":"/etc/passwd"}`)
	rr := httptest.NewRecorder()
	h.mux.ServeHTTP(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("unknown field status=%d", rr.Code)
	}
	req = localPost("/v1/subscriptions/compile", `{"source":"s","profile":{"id":"p","enabled":true,"port":16122},"links":["vless://00000000-0000-4000-8000-000000000001@edge.example:443#edge"]}`)
	req.Header.Set("Origin", "https://evil.example")
	rr = httptest.NewRecorder()
	h.mux.ServeHTTP(rr, req)
	if rr.Code != http.StatusForbidden {
		t.Fatalf("cross-origin status=%d", rr.Code)
	}
}

func TestEncryptedProfileCRUDAndRefreshWorkflow(t *testing.T) {
	stateDir := filepath.Join(t.TempDir(), "state")
	h := NewWithStateDir(apiSupervisor(t), stateDir)
	profileBody := `{"id":"foreign","source_id":"providerA","enabled":true,"port":16150,"strategy":"leastPing","subscription_url":"https://provider.example/sub?token=SECRET"}`
	req := localPost("/v1/profiles", profileBody)
	rr := httptest.NewRecorder()
	h.mux.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("save profile status=%d body=%s", rr.Code, rr.Body.String())
	}
	profileFile := filepath.Join(stateDir, "profiles", "foreign.profile")
	ciphertext, err := os.ReadFile(profileFile)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(ciphertext), "SECRET") || strings.Contains(string(ciphertext), "provider.example") {
		t.Fatal("subscription URL was stored in plaintext")
	}
	get := httptest.NewRequest(http.MethodGet, "/v1/profiles", nil)
	get.Host = "127.0.0.1:17890"
	get.RemoteAddr = "127.0.0.1:12345"
	rr = httptest.NewRecorder()
	h.mux.ServeHTTP(rr, get)
	if rr.Code != http.StatusOK || strings.Contains(rr.Body.String(), "SECRET") || strings.Contains(rr.Body.String(), "provider.example") {
		t.Fatalf("metadata response status=%d body=%s", rr.Code, rr.Body.String())
	}

	// Persist a second, network-independent source for refresh.
	storeProfile := profiles.Profile{ID: "local", SourceID: "providerB", Enabled: true, Port: 16151, Links: []string{"vless://00000000-0000-4000-8000-000000000001@edge.example:443#edge"}}
	if err := h.profileStore.Save(storeProfile); err != nil {
		t.Fatal(err)
	}
	selectionReq := localPost("/v1/selections", `{"id":"foreign","source_id":"providerA","pick_mode":"auto"}`)
	selectionReq.Method = http.MethodPut
	rr = httptest.NewRecorder()
	h.mux.ServeHTTP(rr, selectionReq)
	if rr.Code != http.StatusOK {
		t.Fatalf("save selection status=%d body=%s", rr.Code, rr.Body.String())
	}
	deleteReq := localPost("/v1/profiles", `{"id":"foreign"}`)
	deleteReq.Method = http.MethodDelete
	rr = httptest.NewRecorder()
	h.mux.ServeHTTP(rr, deleteReq)
	if rr.Code != http.StatusOK {
		t.Fatalf("delete profile status=%d body=%s", rr.Code, rr.Body.String())
	}
	getSelection := httptest.NewRequest(http.MethodGet, "/v1/selections?profile_id=foreign", nil)
	getSelection.Host = "127.0.0.1:17890"
	getSelection.RemoteAddr = "127.0.0.1:12345"
	rr = httptest.NewRecorder()
	h.mux.ServeHTTP(rr, getSelection)
	if rr.Code != http.StatusNotFound {
		t.Fatalf("selection should be deleted with profile, status=%d", rr.Code)
	}
	refresh := localPost("/v1/profiles/refresh", `{}`)
	rr = httptest.NewRecorder()
	h.mux.ServeHTTP(rr, refresh)
	if rr.Code != http.StatusOK {
		t.Fatalf("refresh status=%d body=%s", rr.Code, rr.Body.String())
	}
	if !h.supervisor.Status().Configured || h.supervisor.Status().Running {
		t.Fatalf("runtime=%+v", h.supervisor.Status())
	}
}

func TestProfileDeleteBlockedWhileRuntimeRunning(t *testing.T) {
	stateDir := filepath.Join(t.TempDir(), "state")
	h := NewWithStateDir(apiSupervisor(t), stateDir)
	if err := h.profileStore.Save(profiles.Profile{ID: "foreign", SourceID: "sourceA", Enabled: true, Port: 16152, Links: []string{"vless://00000000-0000-4000-8000-000000000001@edge.example:443#edge"}}); err != nil {
		t.Fatal(err)
	}
	if err := h.supervisor.Prepare(context.Background(), []byte(`{"inbounds":[],"outbounds":[]}`)); err != nil {
		t.Fatal(err)
	}
	if err := h.supervisor.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer h.supervisor.Stop(context.Background())
	req := localPost("/v1/profiles", `{"id":"foreign"}`)
	req.Method = http.MethodDelete
	rr := httptest.NewRecorder()
	h.mux.ServeHTTP(rr, req)
	if rr.Code != http.StatusConflict {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
	}
}

func TestRefreshStatusDisabledByDefault(t *testing.T) {
	h := NewWithStateDir(apiSupervisor(t), filepath.Join(t.TempDir(), "state"))
	rr := httptest.NewRecorder()
	h.mux.ServeHTTP(rr, localGet("/v1/refresh/status"))
	if rr.Code != http.StatusOK || !strings.Contains(rr.Body.String(), `"enabled":false`) {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
	}
}

func TestRefreshProfilesRestartsRunningRuntime(t *testing.T) {
	bin := filepath.Join(t.TempDir(), "xray")
	script := "#!/bin/sh\nif [ \"$2\" = -test ]; then exit 0; fi\ntrap 'exit 0' TERM\nwhile :; do sleep 0.1; done\n"
	if err := os.WriteFile(bin, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	stateDir := filepath.Join(t.TempDir(), "state")
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
	h := NewWithStateDir(sup, stateDir)
	if err := h.profileStore.Save(profiles.Profile{ID: "foreign", SourceID: "providerA", Enabled: true, Port: 16172, Links: []string{"vless://00000000-0000-4000-8000-000000000001@edge.example:443#edge"}}); err != nil {
		t.Fatal(err)
	}
	rr := httptest.NewRecorder()
	h.mux.ServeHTTP(rr, localPost("/v1/profiles/refresh", `{}`))
	if rr.Code != http.StatusOK {
		t.Fatalf("refresh status=%d body=%s", rr.Code, rr.Body.String())
	}
	if !sup.Status().Running {
		t.Fatalf("runtime should have been restarted: %+v", sup.Status())
	}
}

func TestRefreshStartupFailureRollsBackPreviousConfig(t *testing.T) {
	bin := filepath.Join(t.TempDir(), "xray")
	script := "#!/bin/sh\nif [ \"$2\" = -test ]; then exit 0; fi\nif grep -q hr_in_ \"$3\"; then exit 1; fi\ntrap 'exit 0' TERM\nwhile :; do sleep 0.1; done\n"
	if err := os.WriteFile(bin, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	configPath := filepath.Join(t.TempDir(), "private", "config.json")
	sup, err := hrruntime.NewSupervisor(bin, configPath)
	if err != nil {
		t.Fatal(err)
	}
	previous := []byte(`{"inbounds":[],"outbounds":[]}`)
	if err := sup.Prepare(context.Background(), previous); err != nil {
		t.Fatal(err)
	}
	if err := sup.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer sup.Stop(context.Background())
	h := NewWithStateDir(sup, filepath.Join(t.TempDir(), "state"))
	if err := h.profileStore.Save(profiles.Profile{ID: "foreign", SourceID: "providerA", Enabled: true, Port: 16174, Links: []string{"vless://00000000-0000-4000-8000-000000000001@edge.example:443#edge"}}); err != nil {
		t.Fatal(err)
	}
	rr := httptest.NewRecorder()
	h.mux.ServeHTTP(rr, localPost("/v1/profiles/refresh", `{}`))
	if rr.Code != http.StatusUnprocessableEntity {
		t.Fatalf("expected failed refresh, got %d: %s", rr.Code, rr.Body.String())
	}
	got, err := os.ReadFile(configPath)
	if err != nil || string(got) != string(previous) {
		t.Fatalf("last-good config not restored: %s, err=%v", got, err)
	}
	if !sup.Status().Running {
		t.Fatal("previous Xray runtime was not restarted")
	}
}

func TestRefreshFailurePreservesRunningConfig(t *testing.T) {
	bin := filepath.Join(t.TempDir(), "xray")
	script := "#!/bin/sh\nif [ \"$2\" = -test ]; then exit 0; fi\ntrap 'exit 0' TERM\nwhile :; do sleep 0.1; done\n"
	if err := os.WriteFile(bin, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	configPath := filepath.Join(t.TempDir(), "private", "config.json")
	sup, err := hrruntime.NewSupervisor(bin, configPath)
	if err != nil {
		t.Fatal(err)
	}
	previous := []byte(`{"inbounds":[],"outbounds":[]}`)
	if err := sup.Prepare(context.Background(), previous); err != nil {
		t.Fatal(err)
	}
	if err := sup.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer sup.Stop(context.Background())
	h := NewWithStateDir(sup, filepath.Join(t.TempDir(), "state"))
	if err := h.profileStore.Save(profiles.Profile{ID: "broken", SourceID: "providerA", Enabled: true, Port: 16173, Links: []string{"not-a-supported-link"}}); err != nil {
		t.Fatal(err)
	}
	rr := httptest.NewRecorder()
	h.mux.ServeHTTP(rr, localPost("/v1/profiles/refresh", `{}`))
	if rr.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
	}
	if !sup.Status().Running {
		t.Fatal("failed refresh stopped the current runtime")
	}
	got, err := os.ReadFile(configPath)
	if err != nil || string(got) != string(previous) {
		t.Fatalf("previous config changed: %s err=%v", got, err)
	}
}

func TestRefreshSchedulerRunsAndStopsWithServer(t *testing.T) {
	bin := filepath.Join(t.TempDir(), "xray")
	script := "#!/bin/sh\nif [ \"$2\" = -test ]; then exit 0; fi\ntrap 'exit 0' TERM\nwhile :; do sleep 0.1; done\n"
	if err := os.WriteFile(bin, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	sup, err := hrruntime.NewSupervisor(bin, filepath.Join(t.TempDir(), "private", "config.json"))
	if err != nil {
		t.Fatal(err)
	}
	stateDir := filepath.Join(t.TempDir(), "state")
	h := NewWithOptions(sup, stateDir, Options{RefreshInterval: 30 * time.Millisecond, RetryMin: 20 * time.Millisecond, RetryMax: 80 * time.Millisecond})
	if err := h.profileStore.Save(profiles.Profile{ID: "foreign", SourceID: "providerA", Enabled: true, Port: 16174, Links: []string{"vless://00000000-0000-4000-8000-000000000001@edge.example:443#edge"}}); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- h.Serve(ctx, "127.0.0.1:0") }()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		h.refreshMu.Lock()
		success := h.refreshState.LastSuccess != nil
		h.refreshMu.Unlock()
		if success {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	h.refreshMu.Lock()
	lastSuccess := h.refreshState.LastSuccess
	h.refreshMu.Unlock()
	if lastSuccess == nil {
		cancel()
		<-done
		t.Fatal("scheduler did not refresh stored profile")
	}
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("server/scheduler did not stop")
	}
	if h.supervisor.Status().Running {
		t.Fatal("scheduler/server shutdown left Xray running")
	}
}

func TestRefreshSchedulerExponentialBackoffReportsSourceFailures(t *testing.T) {
	bin := filepath.Join(t.TempDir(), "xray")
	script := "#!/bin/sh\nif [ \"$2\" = -test ]; then exit 0; fi\ntrap 'exit 0' TERM\nwhile :; do sleep 0.1; done\n"
	if err := os.WriteFile(bin, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	sup, err := hrruntime.NewSupervisor(bin, filepath.Join(t.TempDir(), "private", "config.json"))
	if err != nil {
		t.Fatal(err)
	}
	h := NewWithOptions(sup, filepath.Join(t.TempDir(), "state"), Options{RefreshInterval: time.Second, RetryMin: 100 * time.Millisecond, RetryMax: 400 * time.Millisecond})
	if err := h.profileStore.Save(profiles.Profile{ID: "broken", SourceID: "providerA", Enabled: true, Port: 16175, Links: []string{"not-a-supported-link"}}); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- h.Serve(ctx, "127.0.0.1:0") }()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		h.refreshMu.Lock()
		failures := h.refreshState.ConsecutiveFailures
		next := h.refreshState.NextAttempt
		lastErr := h.refreshState.LastError
		h.refreshMu.Unlock()
		if failures > 0 {
			if !strings.Contains(lastErr, "broken") || !strings.Contains(lastErr, "parsing failed") {
				cancel()
				<-done
				t.Fatalf("unexpected safe error report: %s", lastErr)
			}
			if next == nil || next.Before(time.Now().Add(50*time.Millisecond)) {
				cancel()
				<-done
				t.Fatalf("retry did not apply backoff: %s", next)
			}
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	h.refreshMu.Lock()
	failures := h.refreshState.ConsecutiveFailures
	h.refreshMu.Unlock()
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if failures == 0 {
		t.Fatal("scheduler did not record failed source refresh")
	}
	if sup.Status().Configured {
		t.Fatal("failed refresh must not prepare a config")
	}
}

func TestAutoStartStartsAfterFirstSuccessfulRefresh(t *testing.T) {
	bin := filepath.Join(t.TempDir(), "xray")
	script := "#!/bin/sh\nif [ \"$2\" = -test ]; then exit 0; fi\ntrap 'exit 0' TERM\nwhile :; do sleep 0.1; done\n"
	if err := os.WriteFile(bin, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	sup, err := hrruntime.NewSupervisor(bin, filepath.Join(t.TempDir(), "private", "config.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := sup.Restore(context.Background()); err != nil {
		t.Fatal(err)
	}
	if sup.Status().Configured {
		t.Fatal("first-run should have no config")
	}
	h := NewWithOptions(sup, filepath.Join(t.TempDir(), "state"), Options{AutoStart: true})
	if err := h.profileStore.Save(profiles.Profile{ID: "first", SourceID: "providerA", Enabled: true, Port: 16176, Links: []string{"vless://00000000-0000-4000-8000-000000000001@edge.example:443#edge"}}); err != nil {
		t.Fatal(err)
	}
	rr := httptest.NewRecorder()
	h.mux.ServeHTTP(rr, localPost("/v1/profiles/refresh", `{}`))
	if rr.Code != http.StatusOK {
		t.Fatalf("refresh status=%d body=%s", rr.Code, rr.Body.String())
	}
	if !sup.Status().Running {
		t.Fatal("autostart option should start Xray after first successful refresh")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := sup.Stop(ctx); err != nil {
		t.Fatal(err)
	}
}

func TestReadEndpointsRejectDNSRebindingAndCrossSiteOrigin(t *testing.T) {
	h := NewWithStateDir(apiSupervisor(t), filepath.Join(t.TempDir(), "state"))
	for _, path := range []string{"/healthz", "/v1/runtime", "/v1/refresh/status", "/v1/profiles", "/v1/selections?profile_id=foreign"} {
		for _, mutate := range []func(*http.Request){
			func(r *http.Request) { r.Host = "attacker.example" },
			func(r *http.Request) { r.Header.Set("Origin", "https://attacker.example") },
			func(r *http.Request) { r.Header.Set("Origin", "http://127.0.0.1:3000") },
			func(r *http.Request) { r.Header.Set("Sec-Fetch-Site", "cross-site") },
		} {
			req := localGet(path)
			mutate(req)
			rr := httptest.NewRecorder()
			h.mux.ServeHTTP(rr, req)
			if rr.Code != http.StatusForbidden {
				t.Fatalf("path=%s status=%d body=%s", path, rr.Code, rr.Body.String())
			}
		}
	}
}

func TestReadEndpointsRequireLoopbackRemoteAddress(t *testing.T) {
	h := NewWithStateDir(apiSupervisor(t), filepath.Join(t.TempDir(), "state"))
	for _, path := range []string{"/healthz", "/v1/runtime", "/v1/refresh/status", "/v1/profiles"} {
		req := localGet(path)
		req.RemoteAddr = "203.0.113.10:12345"
		rr := httptest.NewRecorder()
		h.mux.ServeHTTP(rr, req)
		if rr.Code != http.StatusForbidden {
			t.Fatalf("path=%s status=%d", path, rr.Code)
		}
	}
}

func TestMutationEndpointsRejectDifferentLocalhostOriginPort(t *testing.T) {
	h := New(apiSupervisor(t))
	req := localPost("/v1/runtime", `{"action":"stop"}`)
	req.Header.Set("Origin", "http://127.0.0.1:3000")
	rr := httptest.NewRecorder()
	h.mux.ServeHTTP(rr, req)
	if rr.Code != http.StatusForbidden {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
	}
}
