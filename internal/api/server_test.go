package api

import (
	"context"
	"encoding/json"
	"fmt"
	"hydraroute.local/core/internal/compiler"
	dnsblocklist "hydraroute.local/core/internal/dnsservice/blocklist"
	hrsmartdns "hydraroute.local/core/internal/dnsservice/smartdns"
	"hydraroute.local/core/internal/parser"
	"hydraroute.local/core/internal/profiles"
	"hydraroute.local/core/internal/service"
	"hydraroute.local/core/internal/state"
	"hydraroute.local/core/internal/xrayapi"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
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

type synchronizedStreamRecorder struct {
	mu      sync.Mutex
	inner   *httptest.ResponseRecorder
	flushed chan struct{}
	once    sync.Once
}

func newSynchronizedStreamRecorder() *synchronizedStreamRecorder {
	return &synchronizedStreamRecorder{inner: httptest.NewRecorder(), flushed: make(chan struct{})}
}
func (r *synchronizedStreamRecorder) Header() http.Header { return r.inner.Header() }
func (r *synchronizedStreamRecorder) WriteHeader(status int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.inner.WriteHeader(status)
}
func (r *synchronizedStreamRecorder) Write(data []byte) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.inner.Write(data)
}
func (r *synchronizedStreamRecorder) Flush() {
	r.mu.Lock()
	r.inner.Flush()
	r.mu.Unlock()
	r.once.Do(func() { close(r.flushed) })
}
func (r *synchronizedStreamRecorder) String() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.inner.Body.String()
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

func TestEmbeddedDashboardServesWithSecurityHeaders(t *testing.T) {
	h := New(apiSupervisor(t))
	rr := httptest.NewRecorder()
	h.mux.ServeHTTP(rr, localGet("/"))
	if rr.Code != http.StatusOK {
		t.Fatalf("dashboard status=%d body=%s", rr.Code, rr.Body.String())
	}
	if !strings.Contains(rr.Body.String(), "HYDRAROUTE") {
		t.Fatal("dashboard HTML was not served")
	}
	if rr.Header().Get("Content-Security-Policy") == "" || rr.Header().Get("X-Frame-Options") != "DENY" {
		t.Fatalf("missing dashboard security headers: %v", rr.Header())
	}
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

func TestGeoDataStatusReportsAssetReadinessAndHashes(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "geoip.dat"), []byte("geoip-test-data"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "geosite.dat"), []byte("geosite-test-data"), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("XRAY_LOCATION_ASSET", dir)
	h := NewWithStateDir(apiSupervisor(t), t.TempDir())
	rr := httptest.NewRecorder()
	h.mux.ServeHTTP(rr, localGet("/v1/geodata/status"))
	if rr.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
	}
	var got geoDataStatusResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if !got.Ready || len(got.Assets) != 2 {
		t.Fatalf("unexpected geodata readiness: %#v", got)
	}
	for _, asset := range got.Assets {
		if !asset.Available || asset.Size <= 0 || len(asset.SHA256) != 64 {
			t.Errorf("asset metadata incomplete: %#v", asset)
		}
	}
	if strings.Contains(rr.Body.String(), dir) {
		t.Fatal("geodata response leaked the asset directory path")
	}
}

func TestGeoDataStatusRejectsSymlinkAssetsAndReportsMissingDirectory(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(t.TempDir(), "geoip-target.dat")
	if err := os.WriteFile(target, []byte("geoip"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, filepath.Join(dir, "geoip.dat")); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "geosite.dat"), []byte("geosite"), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("XRAY_LOCATION_ASSET", dir)
	h := NewWithStateDir(apiSupervisor(t), t.TempDir())
	rr := httptest.NewRecorder()
	h.mux.ServeHTTP(rr, localGet("/v1/geodata/status"))
	var got geoDataStatusResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.Ready || got.Assets[0].Available || !got.Assets[1].Available {
		t.Fatalf("symlinked asset must not be accepted: %#v", got)
	}
	t.Setenv("XRAY_LOCATION_ASSET", "")
	rr = httptest.NewRecorder()
	h.mux.ServeHTTP(rr, localGet("/v1/geodata/status"))
	got = geoDataStatusResponse{}
	if err := json.Unmarshal(rr.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.Ready || got.Error == "" {
		t.Fatalf("missing asset directory must report not-ready: %#v", got)
	}
}

func TestRoutingEndpointPersistsAndPreparesSharedSOCKS(t *testing.T) {
	stateDir := filepath.Join(t.TempDir(), "state")
	h := NewWithStateDir(apiSupervisor(t), stateDir)
	profile := profiles.Profile{ID: "foreign", SourceID: "providerA", Enabled: true, Port: 16120,
		Links: []string{"vless://00000000-0000-4000-8000-000000000001@foreign.example:443#foreign"}, DPIBypass: profiles.DPIBypass{NoiseTraffic: true, TLSFragmentation: true, RandomTLSFingerprint: true}}
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
	if got := len(compiled["inbounds"].([]any)); got != 3 {
		t.Fatalf("expected profile and shared inbounds, got %d", got)
	}
	proxy := compiled["outbounds"].([]any)[2].(map[string]any)
	stream := proxy["streamSettings"].(map[string]any)
	mask := stream["finalmask"].(map[string]any)
	if len(mask["tcp"].([]any)) != 1 || len(mask["udp"].([]any)) != 1 {
		t.Fatalf("DPI options were not applied to compiled proxy: %#v", mask)
	}
	rules := compiled["routing"].(map[string]any)["rules"].([]any)
	if got := len(rules); got != 4 {
		t.Fatalf("expected profile, custom and default rules, got %d", got)
	}
	rr = httptest.NewRecorder()
	h.mux.ServeHTTP(rr, localGet("/v1/routing"))
	if rr.Code != http.StatusOK || !strings.Contains(rr.Body.String(), `"configured":true`) {
		t.Fatalf("GET status=%d body=%s", rr.Code, rr.Body.String())
	}
}

func TestRoutingEndpointCanSaveDisabledConfigBeforeProfiles(t *testing.T) {
	stateDir := filepath.Join(t.TempDir(), "state")
	h := NewWithStateDir(apiSupervisor(t), stateDir)
	body := `{"enabled":false,"listen":"127.0.0.1","port":16122,"default_action":"block","rules":[{"id":"youtube","name":"YouTube","enabled":false,"match_type":"domain","values":["geosite:youtube"],"action":"profile","profile_id":"not-added-yet"}]}`
	rr := httptest.NewRecorder()
	h.mux.ServeHTTP(rr, localPut("/v1/routing", body))
	if rr.Code != http.StatusOK {
		t.Fatalf("PUT status=%d body=%s", rr.Code, rr.Body.String())
	}
	var result map[string]any
	if err := json.Unmarshal(rr.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if result["status"] != "saved" {
		t.Fatalf("expected deferred save status, got %v", result)
	}
	if h.supervisor.Status().Configured {
		t.Fatal("saving dormant routing before profiles must not prepare an Xray config")
	}
	rr = httptest.NewRecorder()
	h.mux.ServeHTTP(rr, localGet("/v1/routing"))
	if rr.Code != http.StatusOK || !strings.Contains(rr.Body.String(), `"configured":true`) || !strings.Contains(rr.Body.String(), `"not-added-yet"`) {
		t.Fatalf("dormant routing config was not preserved: status=%d body=%s", rr.Code, rr.Body.String())
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

func TestProfilePreviewReturnsCredentialFreeNodesAndSelection(t *testing.T) {
	stateDir := filepath.Join(t.TempDir(), "state")
	h := NewWithStateDir(apiSupervisor(t), stateDir)
	links := []string{
		"vless://00000000-0000-4000-8000-000000000001@a.example:443#Alpha",
		"vless://00000000-0000-4000-8000-000000000002@b.example:443#Beta",
	}
	profile := profiles.Profile{ID: "foreign", SourceID: "providerA", Enabled: true, Port: 16160, Links: links}
	if err := h.profileStore.Save(profile); err != nil {
		t.Fatal(err)
	}
	servers, err := parser.ParseLinks("providerA", links)
	if err != nil {
		t.Fatal(err)
	}
	selectionBody, err := json.Marshal(selectionRequest{ID: "foreign", SourceID: "providerA", PickMode: "manual", Picked: []parser.ServerKey{servers[0].Key}, Excluded: []parser.ServerKey{servers[1].Key}})
	if err != nil {
		t.Fatal(err)
	}
	selectionReq := localPost("/v1/selections", string(selectionBody))
	selectionReq.Method = http.MethodPut
	rr := httptest.NewRecorder()
	h.mux.ServeHTTP(rr, selectionReq)
	if rr.Code != http.StatusOK {
		t.Fatalf("save selection status=%d body=%s", rr.Code, rr.Body.String())
	}
	previewReq := localPost("/v1/profiles/preview", `{"profile_id":"foreign"}`)
	rr = httptest.NewRecorder()
	h.mux.ServeHTTP(rr, previewReq)
	if rr.Code != http.StatusOK {
		t.Fatalf("preview status=%d body=%s", rr.Code, rr.Body.String())
	}
	var got struct {
		ProfileID string `json:"profile_id"`
		SourceID  string `json:"source_id"`
		PickMode  string `json:"pick_mode"`
		Nodes     []struct {
			Name     string           `json:"name"`
			Picked   bool             `json:"picked"`
			Excluded bool             `json:"excluded"`
			Key      parser.ServerKey `json:"key"`
			Outbound json.RawMessage  `json:"outbound"`
		} `json:"nodes"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.ProfileID != "foreign" || got.SourceID != "providerA" || got.PickMode != "manual" || len(got.Nodes) != 2 {
		t.Fatalf("unexpected preview metadata: %+v", got)
	}
	if got.Nodes[0].Name != "Alpha" || !got.Nodes[0].Picked || got.Nodes[0].Excluded {
		t.Fatalf("unexpected first node: %+v", got.Nodes[0])
	}
	if got.Nodes[1].Name != "Beta" || got.Nodes[1].Picked || !got.Nodes[1].Excluded {
		t.Fatalf("unexpected second node: %+v", got.Nodes[1])
	}
	if strings.Contains(rr.Body.String(), "00000000-0000-4000-8000-000000000001") || strings.Contains(rr.Body.String(), "outbound") {
		t.Fatalf("preview leaked outbound credentials or raw payload: %s", rr.Body.String())
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
	storeProfile := profiles.Profile{ID: "local", SourceID: "providerB", Enabled: true, Port: 16151, Links: []string{"vless://00000000-0000-4000-8000-000000000001@edge.example:443#edge", "vless://00000000-0000-4000-8000-000000000002@edge2.example:443#edge2"}}
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

func TestProfileSettingsArePersistedPerProfileAndExposedWithoutSecrets(t *testing.T) {
	stateDir := filepath.Join(t.TempDir(), "state")
	h := NewWithStateDir(apiSupervisor(t), stateDir)
	for i, id := range []string{"alpha", "beta"} {
		if err := h.profileStore.Save(profiles.Profile{ID: id, SourceID: "providerA", Enabled: true, Port: uint16(16180 + i), Links: []string{"vless://00000000-0000-4000-8000-000000000001@edge.example:443#edge", "vless://00000000-0000-4000-8000-000000000002@edge2.example:443#edge2"}}); err != nil {
			t.Fatal(err)
		}
	}
	available, err := net.Interfaces()
	if err != nil {
		t.Fatal(err)
	}
	ifaceName := ""
	for _, candidate := range available {
		if candidate.Flags&net.FlagLoopback == 0 {
			ifaceName = candidate.Name
			break
		}
	}
	if ifaceName == "" {
		t.Skip("no selectable network interface in test environment")
	}
	body := fmt.Sprintf(`{"id":"alpha","strategy":"roundRobin","outbound_interface":%q,"dpi_bypass":{"noise_traffic":true,"tls_fragmentation":false,"random_tls_fingerprint":true}}`, ifaceName)
	rr := httptest.NewRecorder()
	h.mux.ServeHTTP(rr, localPut("/v1/profiles", body))
	if rr.Code != http.StatusOK {
		t.Fatalf("PUT status=%d body=%s", rr.Code, rr.Body.String())
	}
	a, err := h.profileStore.Get("alpha")
	if err != nil {
		t.Fatal(err)
	}
	b, err := h.profileStore.Get("beta")
	if err != nil {
		t.Fatal(err)
	}
	if a.Strategy != "roundRobin" || a.OutboundInterface != ifaceName || !a.DPIBypass.NoiseTraffic || !a.DPIBypass.RandomTLSFingerprint {
		t.Fatalf("alpha settings not saved: %+v", a)
	}
	if b.Strategy != "" || b.OutboundInterface != "" || b.DPIBypass != (profiles.DPIBypass{}) {
		t.Fatalf("settings leaked into beta: %+v", b)
	}
	rr = httptest.NewRecorder()
	h.mux.ServeHTTP(rr, localGet("/v1/profiles"))
	if rr.Code != http.StatusOK || !strings.Contains(rr.Body.String(), `"outbound_interface":"`+ifaceName+`"`) || !strings.Contains(rr.Body.String(), `"noise_traffic":true`) {
		t.Fatalf("profile metadata did not expose settings: %s", rr.Body.String())
	}
	if _, err := h.refreshProfilesLocked(context.Background()); err != nil {
		t.Fatalf("profile settings failed to compile through refresh: %v", err)
	}
	config, err := h.supervisor.SnapshotConfig()
	if err != nil {
		t.Fatal(err)
	}
	var compiled map[string]any
	if err := json.Unmarshal(config, &compiled); err != nil {
		t.Fatal(err)
	}
	for _, raw := range compiled["outbounds"].([]any) {
		out := raw.(map[string]any)
		tag, _ := out["tag"].(string)
		stream, _ := out["streamSettings"].(map[string]any)
		if strings.HasPrefix(tag, "hr_alpha_") {
			sock, _ := stream["sockopt"].(map[string]any)
			fm, _ := stream["finalmask"].(map[string]any)
			if sock["interface"] != ifaceName || fm["udp"] == nil {
				t.Fatalf("alpha options missing from compiled Xray outbound: %#v", out)
			}
		}
		if strings.HasPrefix(tag, "hr_beta_") {
			if _, ok := stream["finalmask"]; ok {
				t.Fatalf("alpha DPI settings leaked into beta outbound: %#v", out)
			}
		}
	}
}

func TestInterfaceListIsReadOnlyAndLoopbackIsExcluded(t *testing.T) {
	h := New(apiSupervisor(t))
	rr := httptest.NewRecorder()
	h.mux.ServeHTTP(rr, localGet("/v1/interfaces"))
	if rr.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
	}
	if strings.Contains(rr.Body.String(), `"name":"lo"`) {
		t.Fatalf("loopback should not be selectable: %s", rr.Body.String())
	}
}

func TestRefreshSettingsPersistAndUpdateRuntimeSchedule(t *testing.T) {
	stateDir := filepath.Join(t.TempDir(), "state")
	h := NewWithStateDir(apiSupervisor(t), stateDir)
	rr := httptest.NewRecorder()
	h.mux.ServeHTTP(rr, localPut("/v1/refresh/status", `{"enabled":true,"interval_seconds":86400}`))
	if rr.Code != http.StatusOK {
		t.Fatalf("PUT status=%d body=%s", rr.Code, rr.Body.String())
	}
	var got RefreshStatus
	rr = httptest.NewRecorder()
	h.mux.ServeHTTP(rr, localGet("/v1/refresh/status"))
	if rr.Code != http.StatusOK || json.Unmarshal(rr.Body.Bytes(), &got) != nil {
		t.Fatalf("GET status=%d body=%s", rr.Code, rr.Body.String())
	}
	if !got.Enabled || got.IntervalSeconds != 86400 {
		t.Fatalf("refresh status=%+v", got)
	}
	info, err := os.Stat(filepath.Join(stateDir, "refresh-settings.json"))
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatalf("settings file mode=%v err=%v", info, err)
	}
	restarted := NewWithStateDir(apiSupervisor(t), stateDir)
	if !restarted.refreshState.Enabled || restarted.refreshState.IntervalSeconds != 86400 || restarted.refreshOpts.RefreshInterval != 24*time.Hour {
		t.Fatalf("settings were not restored: %+v", restarted.refreshState)
	}
}

func TestEligibleObservatoryNodesOnlyIncludesBalancerMembers(t *testing.T) {
	preview := service.ProfilePreview{Nodes: []service.PreviewNode{
		{Key: parser.ServerKey{Source: "providerA", Name: "selected"}, Picked: true},
		{Key: parser.ServerKey{Source: "providerA", Name: "excluded"}, Picked: true, Excluded: true},
		{Key: parser.ServerKey{Source: "providerA", Name: "filtered"}, Picked: true, Filtered: true},
		{Key: parser.ServerKey{Source: "providerA", Name: "unpicked"}},
	}}
	got := eligibleObservatoryNodes(preview)
	if len(got) != 1 || got[0].Key.Name != "selected" {
		t.Fatalf("Observatory members=%#v, want only the selected balancer member", got)
	}
}

func TestResolveBalancerCandidateUsesLastKnownEligibleStrategyTag(t *testing.T) {
	profileID := "candidate"
	first := service.PreviewNode{Key: parser.ServerKey{Source: "providerA", Name: "first"}, Name: "First"}
	last := service.PreviewNode{Key: parser.ServerKey{Source: "providerA", Name: "last"}, Name: "Last"}
	firstKey, _ := json.Marshal(first.Key)
	lastKey, _ := json.Marshal(last.Key)
	firstTag := compiler.OutboundTag(profileID, string(firstKey))
	lastTag := compiler.OutboundTag(profileID, string(lastKey))
	key, name := resolveBalancerCandidate(profileID, []service.PreviewNode{first, last}, []string{firstTag, lastTag})
	if key != string(lastKey) || name != "Last" {
		t.Fatalf("candidate=(%q,%q), want last strategy target (%q,Last)", key, name, lastKey)
	}
	if key, name = resolveBalancerCandidate(profileID, []service.PreviewNode{first}, []string{"unknown-outbound-tag"}); key != "" || name != "" {
		t.Fatalf("unknown/stale candidate tag must not leak: (%q,%q)", key, name)
	}
}

func TestBuildProfileFallbackSnapshotMapsFullPreviewAndCurrentTags(t *testing.T) {
	profileID := "fallback-snapshot"
	selected := service.PreviewNode{Key: parser.ServerKey{Source: "providerA", Name: "selected"}, Name: "Selected", ReferenceURI: "vless://shared@example.test:443#selected", Picked: true}
	removedCandidate := service.PreviewNode{Key: parser.ServerKey{Source: "providerA", Name: "removed"}, Name: "Removed", ReferenceURI: "vless://shared@example.test:443#removed", Excluded: true}
	stale := service.PreviewNode{Key: parser.ServerKey{Source: "providerA", Name: "stale"}, Name: "Stale", ReferenceURI: "vless://stale@example.test:443#stale", Filtered: true}
	keyOf := func(node service.PreviewNode) string {
		raw, _ := json.Marshal(node.Key)
		return string(raw)
	}
	selectedKey, removedKey, staleKey := keyOf(selected), keyOf(removedCandidate), keyOf(stale)
	selectedTag := compiler.OutboundTag(profileID, selectedKey)
	removedTag := compiler.OutboundTag(profileID, removedKey)
	staleTag := compiler.OutboundTag(profileID, staleKey)

	cached := map[string]xrayapi.OutboundStatus{
		selectedKey: {Tag: selectedTag, Alive: true, DelayMS: 90, LastTryTime: 10},
		staleKey:    {Tag: "outbound-from-old-profile-generation", Alive: true, DelayMS: 1, LastTryTime: 12},
	}
	live := []xrayapi.OutboundStatus{
		{Tag: selectedTag, Alive: false, DelayMS: 0, LastTryTime: 20},
		{Tag: removedTag, Alive: true, DelayMS: 25, LastTryTime: 21},
		{Tag: staleTag, Alive: true, DelayMS: 5, LastTryTime: 0},
	}
	got := buildProfileFallbackSnapshot(profileID, []service.PreviewNode{selected, removedCandidate, stale}, cached, live, []string{selectedTag, removedTag})

	if got.CandidateKey != removedKey || got.ReferenceURI != removedCandidate.ReferenceURI {
		t.Fatalf("candidate snapshot=(%q,%q), want full-preview candidate (%q,%q)", got.CandidateKey, got.ReferenceURI, removedKey, removedCandidate.ReferenceURI)
	}
	if got.ReferenceURIs[staleKey] != stale.ReferenceURI {
		t.Fatalf("full preview URI map lost filtered node: %#v", got.ReferenceURIs)
	}
	if status := got.Statuses[selectedKey]; status.Alive || status.LastTryTime != 20 {
		t.Fatalf("live status did not replace cached status: %+v", status)
	}
	if status := got.Statuses[removedKey]; !status.Alive || status.LastTryTime != 21 {
		t.Fatalf("removed candidate live status missing: %+v", status)
	}
	if _, ok := got.Statuses[staleKey]; ok {
		t.Fatalf("stale cached tag or zero-timestamp live status was accepted: %+v", got.Statuses[staleKey])
	}

	unknown := buildProfileFallbackSnapshot(profileID, []service.PreviewNode{selected}, cached, nil, []string{"unknown-outbound-tag"})
	if unknown.CandidateKey != "" || unknown.ReferenceURI != "" {
		t.Fatalf("unknown candidate tag leaked into snapshot: %+v", unknown)
	}
}

func TestObservatoryCachePersistsAcrossCoreRestart(t *testing.T) {
	stateDir := filepath.Join(t.TempDir(), "state")
	h := NewWithStateDir(apiSupervisor(t), stateDir)
	h.probeCache["obs"] = map[string]xrayapi.OutboundStatus{
		`{"source":"providerA","name":"edge"}`: {Tag: "hr_obs_test", Alive: true, DelayMS: 42, LastTryTime: 1762680000},
	}
	if err := h.saveProbeCache(); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(filepath.Join(stateDir, "observatory", "status.json"))
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatalf("cache file mode=%v err=%v", info, err)
	}
	restarted := NewWithStateDir(apiSupervisor(t), stateDir)
	got := restarted.probeCache["obs"]
	if len(got) != 1 || got[`{"source":"providerA","name":"edge"}`].DelayMS != 42 {
		t.Fatalf("probe cache not restored: %#v", got)
	}
}

func TestObservatoryStatusIsReadOnlyAndReportsUnavailableWhenRuntimeStopped(t *testing.T) {
	h := NewWithStateDir(apiSupervisor(t), filepath.Join(t.TempDir(), "state"))
	p := profiles.Profile{ID: "obs", SourceID: "providerA", Enabled: true, Port: 16200, Links: []string{"vless://00000000-0000-4000-8000-000000000001@edge.example:443#edge"}}
	if err := h.profileStore.Save(p); err != nil {
		t.Fatal(err)
	}
	rr := httptest.NewRecorder()
	h.mux.ServeHTTP(rr, localGet("/v1/observatory/status?profile_id=obs"))
	if rr.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
	}
	var got map[string]any
	if err := json.Unmarshal(rr.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got["available"] != false {
		t.Fatalf("stopped runtime must not fabricate Observatory status: %#v", got)
	}
}

func TestProfileSettingsSaveFiltersAutoUpdateAndSelectionTogether(t *testing.T) {
	stateDir := filepath.Join(t.TempDir(), "state")
	h := NewWithStateDir(apiSupervisor(t), stateDir)
	p := profiles.Profile{ID: "settings", SourceID: "providerA", Enabled: true, Port: 16210, SubscriptionURL: "https://provider.example/sub"}
	if err := h.profileStore.Save(p); err != nil {
		t.Fatal(err)
	}
	body := `{"id":"settings","filter_keep":"LTE\nMoscow","filter_exclude":"trial","auto_update_enabled":true,"auto_update_interval":1440,"pick_mode":"manual","picked":[{"source":"providerA","name":"Node A"}],"excluded":[]}`
	rr := httptest.NewRecorder()
	h.mux.ServeHTTP(rr, localPut("/v1/profiles", body))
	if rr.Code != http.StatusOK {
		t.Fatalf("PUT status=%d body=%s", rr.Code, rr.Body.String())
	}
	got, err := h.profileStore.Get("settings")
	if err != nil {
		t.Fatal(err)
	}
	if got.FilterKeep != "LTE\nMoscow" || got.FilterExclude != "trial" || !got.AutoUpdateEnabled || got.AutoUpdateInterval != 1440 {
		t.Fatalf("profile settings=%+v", got)
	}
	selection, err := service.LoadSelection(stateDir, "settings")
	if err != nil {
		t.Fatal(err)
	}
	if selection.PickMode != "manual" || len(selection.Picked) != 1 || selection.Picked[0].Name != "Node A" {
		t.Fatalf("selection=%+v", selection)
	}
}

func TestScheduledRefreshUsesEncryptedCachedSubscriptionUntilItsIntervalIsDue(t *testing.T) {
	stateDir := filepath.Join(t.TempDir(), "state")
	h := NewWithStateDir(apiSupervisor(t), stateDir)
	p := profiles.Profile{ID: "scheduled", SourceID: "providerA", Enabled: true, Port: 16211, SubscriptionURL: "https://provider.example/sub", CachedSubscription: "vless://00000000-0000-4000-8000-000000000001@edge.example:443#Cached", AutoUpdateEnabled: true, AutoUpdateInterval: 1440, LastSubscriptionUpdate: time.Now().Unix()}
	if err := h.profileStore.Save(p); err != nil {
		t.Fatal(err)
	}
	delay, ok := h.profileRefreshDelay()
	if !ok || delay <= 0 || delay > 24*time.Hour {
		t.Fatalf("scheduled delay=%v enabled=%v", delay, ok)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	result, err := h.refreshProfilesScheduledLocked(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if result["nodes"] != 1 {
		t.Fatalf("scheduled cached compile=%v", result)
	}
	cached, err := h.profileStore.Get("scheduled")
	if err != nil {
		t.Fatal(err)
	}
	if cached.CachedSubscription != p.CachedSubscription || cached.LastSubscriptionUpdate != p.LastSubscriptionUpdate {
		t.Fatalf("not-due source was fetched/changed: %+v", cached)
	}
}

func TestProfilePingStreamKeepsICMPEchoAndProxyPingSeparate(t *testing.T) {
	stateDir := filepath.Join(t.TempDir(), "state")
	h := NewWithStateDir(apiSupervisor(t), stateDir)
	p := profiles.Profile{ID: "ping", SourceID: "providerA", Enabled: true, Port: 16212, Links: []string{"vless://00000000-0000-4000-8000-000000000001@127.0.0.1:443#Loopback"}}
	if err := h.profileStore.Save(p); err != nil {
		t.Fatal(err)
	}
	r := localGet("/v1/profiles/ping?profile_id=ping")
	rr := httptest.NewRecorder()
	h.mux.ServeHTTP(rr, r)
	if rr.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
	}
	if rr.Header().Get("Content-Type") != "text/event-stream" {
		t.Fatalf("content-type=%q", rr.Header().Get("Content-Type"))
	}
	body := rr.Body.String()
	for _, part := range []string{`"links"`, `"echo":{}`, `"done":true`, `Xray не запущен`} {
		if !strings.Contains(body, part) {
			t.Fatalf("SSE response missing %q: %s", part, body)
		}
	}
	if strings.Contains(body, `"echo":{"alive"`) || strings.Contains(body, `"ping":{"delay"`) && strings.Contains(body, `"echo":{"delay"`) {
		t.Fatalf("ICMP echo and Xray ping were conflated: %s", body)
	}
}

func TestObservatorySettingsPersistAndValidate(t *testing.T) {
	dir := t.TempDir()
	h := NewWithStateDir(apiSupervisor(t), dir)
	rr := httptest.NewRecorder()
	h.mux.ServeHTTP(rr, localPut("/v1/observatory/settings", `{"probe_interval_seconds":137}`))
	if rr.Code != http.StatusOK {
		t.Fatalf("save status=%d body=%s", rr.Code, rr.Body.String())
	}
	info, err := os.Stat(filepath.Join(dir, "observatory", "settings.json"))
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0600 {
		t.Fatalf("settings mode=%o", info.Mode().Perm())
	}
	reloaded := NewWithStateDir(apiSupervisor(t), dir)
	rr = httptest.NewRecorder()
	reloaded.mux.ServeHTTP(rr, localGet("/v1/observatory/settings"))
	if rr.Code != http.StatusOK || !strings.Contains(rr.Body.String(), `"probe_interval_seconds":137`) {
		t.Fatalf("reload status=%d body=%s", rr.Code, rr.Body.String())
	}
	rr = httptest.NewRecorder()
	h.mux.ServeHTTP(rr, localPut("/v1/observatory/settings", `{"probe_interval_seconds":3601}`))
	if rr.Code != http.StatusUnprocessableEntity {
		t.Fatalf("invalid interval status=%d body=%s", rr.Code, rr.Body.String())
	}
}

func TestObservatorySettingsImmediatelyUpdatePreparedXrayConfig(t *testing.T) {
	dir := t.TempDir()
	h := NewWithStateDir(apiSupervisor(t), dir)
	node := func(key string) compiler.Node {
		return compiler.Node{Key: key, Outbound: json.RawMessage(`{"protocol":"freedom"}`)}
	}
	config, err := compiler.Compile(compiler.CompileRequest{Profiles: []compiler.Profile{{
		ID: "obs", Enabled: true, Port: 16242, Nodes: []compiler.Node{node("one"), node("two")},
	}}})
	if err != nil {
		t.Fatal(err)
	}
	if err := h.supervisor.Prepare(context.Background(), config); err != nil {
		t.Fatalf("prepare Xray config: %v", err)
	}
	rr := httptest.NewRecorder()
	h.mux.ServeHTTP(rr, localPut("/v1/observatory/settings", `{"probe_interval_seconds":137}`))
	if rr.Code != http.StatusOK {
		t.Fatalf("save status=%d body=%s", rr.Code, rr.Body.String())
	}
	if !strings.Contains(rr.Body.String(), `"applies":"prepared Xray configuration updated"`) {
		t.Fatalf("response did not report immediate prepared-config update: %s", rr.Body.String())
	}
	updated, err := h.supervisor.SnapshotConfig()
	if err != nil {
		t.Fatal(err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(updated, &decoded); err != nil {
		t.Fatal(err)
	}
	observatory := decoded["observatory"].(map[string]any)
	want := fmt.Sprintf("%dms", compiler.ObservatoryProbeIntervalMS(137, 2))
	if observatory["probeInterval"] != want {
		t.Fatalf("probeInterval=%v want %s", observatory["probeInterval"], want)
	}
}

func TestL7SettingsPersistAndApplyOnlyToCoreInbounds(t *testing.T) {
	dir := t.TempDir()
	h := NewWithStateDir(apiSupervisor(t), dir)
	rr := httptest.NewRecorder()
	h.mux.ServeHTTP(rr, localPut("/v1/l7/settings", `{"enabled":true,"http":false,"tls":true,"quic":true}`))
	if rr.Code != http.StatusOK {
		t.Fatalf("save status=%d body=%s", rr.Code, rr.Body.String())
	}
	info, err := os.Stat(filepath.Join(dir, "l7", "settings.json"))
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0600 {
		t.Fatalf("settings mode=%o", info.Mode().Perm())
	}
	reloaded := NewWithStateDir(apiSupervisor(t), dir)
	rr = httptest.NewRecorder()
	reloaded.mux.ServeHTTP(rr, localGet("/v1/l7/settings"))
	if rr.Code != http.StatusOK || !strings.Contains(rr.Body.String(), `"enabled":true`) || !strings.Contains(rr.Body.String(), `"tls":true`) {
		t.Fatalf("reload status=%d body=%s", rr.Code, rr.Body.String())
	}
	request := `{"profiles":[{"id":"l7","enabled":true,"port":16241,"nodes":[{"key":"node","outbound":{"protocol":"freedom"}}]}]}`
	rr = httptest.NewRecorder()
	reloaded.mux.ServeHTTP(rr, localPost("/v1/config", request))
	if rr.Code != http.StatusOK {
		t.Fatalf("config status=%d body=%s", rr.Code, rr.Body.String())
	}
	config, err := reloaded.supervisor.SnapshotConfig()
	if err != nil {
		t.Fatal(err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(config, &decoded); err != nil {
		t.Fatal(err)
	}
	inbounds := decoded["inbounds"].([]any)
	sniffing := inbounds[0].(map[string]any)["sniffing"].(map[string]any)
	selected := sniffing["destOverride"].([]any)
	if len(selected) != 2 || selected[0] != "tls" || selected[1] != "quic" || sniffing["routeOnly"] != true {
		t.Fatalf("persisted L7 selection was not applied: %#v", sniffing)
	}
	invalid := `{"enabled":true,"http":false,"tls":false,"quic":false}`
	rr = httptest.NewRecorder()
	reloaded.mux.ServeHTTP(rr, localPut("/v1/l7/settings", invalid))
	if rr.Code != http.StatusUnprocessableEntity {
		t.Fatalf("empty enabled L7 selection status=%d body=%s", rr.Code, rr.Body.String())
	}
}

func TestIsFreshObservatoryProbeUsesStrictTimestampBoundary(t *testing.T) {
	if isFreshObservatoryProbe(1762680000, 1762680000) {
		t.Fatal("same-second/old result must not be treated as fresh")
	}
	if isFreshObservatoryProbe(1762679999, 1762680000) {
		t.Fatal("older result must not be treated as fresh")
	}
	if !isFreshObservatoryProbe(1762680001, 1762680000) {
		t.Fatal("newer result should be treated as fresh")
	}
}

func TestCachedProbeStatusesRejectsDifferentOutboundGeneration(t *testing.T) {
	h := NewWithStateDir(apiSupervisor(t), t.TempDir())
	key := `{"source":"providerA","name":"edge"}`
	h.probeCache["foreign"] = map[string]xrayapi.OutboundStatus{
		key: {Tag: "old-generation-tag", Alive: true, DelayMS: 12, LastTryTime: 1762680000},
	}
	node := service.PreviewNode{}
	if err := json.Unmarshal([]byte(key), &node.Key); err != nil {
		t.Fatal(err)
	}
	got := h.cachedProbeStatuses("foreign", []service.PreviewNode{node})
	if len(got) != 0 {
		t.Fatalf("stale generation status leaked into current profile: %#v", got)
	}
	tag := compiler.OutboundTag("foreign", key)
	h.probeCache["foreign"][key] = xrayapi.OutboundStatus{Tag: tag, Alive: false, LastTryTime: 1762680000}
	got = h.cachedProbeStatuses("foreign", []service.PreviewNode{node})
	if status, ok := got[key]; !ok || !status.Stale {
		t.Fatalf("cached measurement must be explicitly stale: %#v", got)
	}
}

func TestSameObservatoryStatusDetectsOnlyActualMeasurementChanges(t *testing.T) {
	a := xrayapi.OutboundStatus{Tag: "hr_test", Alive: true, DelayMS: 23, LastTryTime: 1762680000, LastSeenTime: 1762680000}
	if !sameObservatoryStatus(a, a) {
		t.Fatal("identical status should compare equal")
	}
	b := a
	b.DelayMS++
	if sameObservatoryStatus(a, b) {
		t.Fatal("changed delay should compare different")
	}
	b = a
	b.LastSeenTime++
	if sameObservatoryStatus(a, b) {
		t.Fatal("changed last-seen time should compare different")
	}
}

func TestEligiblePreviewNodeCountMatchesSelectionAndFilters(t *testing.T) {
	key1 := parser.ServerKey{Source: "providerA", Name: "one"}
	key2 := parser.ServerKey{Source: "providerA", Name: "two"}
	key3 := parser.ServerKey{Source: "providerA", Name: "three"}
	preview := service.ProfilePreview{Nodes: []service.PreviewNode{
		{Key: key1, Picked: true},
		{Key: key2, Picked: true, Excluded: true},
		{Key: key3, Picked: true, Filtered: true},
	}}
	if got := eligiblePreviewNodeCount(preview, nil); got != 1 {
		t.Fatalf("auto pool count=%d, want 1", got)
	}
	selection := &state.Profile{PickMode: "manual", Picked: []parser.ServerKey{key1, key2, key3}, Excluded: []parser.ServerKey{key2}}
	if got := eligiblePreviewNodeCount(preview, selection); got != 1 {
		t.Fatalf("manual pool count=%d, want 1", got)
	}
	selection = &state.Profile{PickMode: "manual", Picked: []parser.ServerKey{key1, key2}, Excluded: []parser.ServerKey{key2}}
	if got := eligiblePreviewNodeCount(preview, selection); got != 1 {
		t.Fatalf("manual subset pool count=%d, want 1", got)
	}
}

func TestRoutingPublishesCoreOwnedWatchlistSocketAndStatus(t *testing.T) {
	stateDir := filepath.Join(t.TempDir(), "state")
	h := NewWithStateDir(apiSupervisor(t), stateDir)
	profile := profiles.Profile{ID: "foreign", SourceID: "providerA", Enabled: true, Port: 16220,
		Links: []string{"vless://00000000-0000-4000-8000-000000000001@foreign.example:443#foreign"}}
	if err := h.profileStore.Save(profile); err != nil {
		t.Fatal(err)
	}
	body := `{"enabled":true,"listen":"127.0.0.1","port":16221,"default_action":"block","rules":[{"id":"ru","name":"RU","enabled":true,"match_type":"domain","values":["domain:ru"],"action":"profile","profile_id":"foreign"},{"id":"telegram","name":"Telegram","enabled":true,"match_type":"domain","values":["domain:telegram.org"],"action":"direct"},{"id":"geoip","name":"GeoIP RU","enabled":true,"match_type":"ip","values":["geoip:ru"],"action":"block"}]}`
	rr := httptest.NewRecorder()
	h.mux.ServeHTTP(rr, localPut("/v1/routing", body))
	if rr.Code != http.StatusOK {
		t.Fatalf("routing PUT status=%d body=%s", rr.Code, rr.Body.String())
	}
	path := filepath.Join(stateDir, "watchlist.sock")
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("watchlist socket was not created: %v", err)
	}
	if info.Mode().Perm() != 0600 || info.Mode()&os.ModeSocket == 0 {
		t.Fatalf("unsafe watchlist socket mode/type: %v", info.Mode())
	}
	conn, err := net.DialTimeout("unix", path, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := conn.Write([]byte("MATCH Mail.Google.RU.\nMATCH api.telegram.org\nDUMP\n")); err != nil {
		t.Fatal(err)
	}
	_ = conn.(*net.UnixConn).CloseWrite()
	response, err := io.ReadAll(conn)
	_ = conn.Close()
	if err != nil {
		t.Fatal(err)
	}
	got := string(response)
	if !strings.Contains(got, "OK foreign 0 ru\n") || !strings.Contains(got, "OK DIRECT 1 telegram.org\n") ||
		!strings.Contains(got, "T 2 BLOCK\n") || !strings.Contains(got, "END 2\n") {
		t.Fatalf("unexpected watchlist protocol response:\n%s", got)
	}
	rr = httptest.NewRecorder()
	h.mux.ServeHTTP(rr, localGet("/v1/watchlist/status"))
	if rr.Code != http.StatusOK || !strings.Contains(rr.Body.String(), `"ready":true`) ||
		!strings.Contains(rr.Body.String(), `"targets":3`) || !strings.Contains(rr.Body.String(), `"keys":2`) {
		t.Fatalf("watchlist status=%d body=%s", rr.Code, rr.Body.String())
	}

	disabled := `{"enabled":false,"listen":"127.0.0.1","port":16221,"default_action":"block","rules":[]}`
	rr = httptest.NewRecorder()
	h.mux.ServeHTTP(rr, localPut("/v1/routing", disabled))
	if rr.Code != http.StatusOK {
		t.Fatalf("disable routing status=%d body=%s", rr.Code, rr.Body.String())
	}
	if _, err := os.Lstat(path); !os.IsNotExist(err) {
		t.Fatalf("watchlist socket remains after routing disabled: %v", err)
	}
}

func TestEventJournalAPIListsClearsAndStreamsStructuredEvents(t *testing.T) {
	h := NewWithStateDir(apiSupervisor(t), t.TempDir())
	h.recordEvent("info", "runtime", "started", "Xray process started")
	h.recordEvent("warning", "runtime", "stopped", "Xray process stopped")

	rr := httptest.NewRecorder()
	h.mux.ServeHTTP(rr, localGet("/v1/events?limit=10"))
	if rr.Code != http.StatusOK || !strings.Contains(rr.Body.String(), `"type":"started"`) {
		t.Fatalf("list status=%d body=%s", rr.Code, rr.Body.String())
	}

	ctx, cancel := context.WithCancel(context.Background())
	streamReq := localGet("/v1/events/stream")
	streamReq.Header.Set("Last-Event-ID", "1")
	streamReq = streamReq.WithContext(ctx)
	streamRR := newSynchronizedStreamRecorder()
	finished := make(chan struct{})
	go func() {
		h.mux.ServeHTTP(streamRR, streamReq)
		close(finished)
	}()
	select {
	case <-streamRR.flushed:
	case <-time.After(2 * time.Second):
		cancel()
		<-finished
		t.Fatalf("stream did not emit initial event: %s", streamRR.String())
	}
	cancel()
	select {
	case <-finished:
	case <-time.After(time.Second):
		t.Fatal("event stream did not stop after request cancellation")
	}
	streamBody := streamRR.String()
	if !strings.Contains(streamBody, `"message":"Xray process stopped"`) || strings.Contains(streamBody, `"message":"Xray process started"`) {
		t.Fatalf("Last-Event-ID was not respected: %s", streamBody)
	}

	deleteReq := httptest.NewRequest(http.MethodDelete, "/v1/events", nil)
	deleteReq.Host = "127.0.0.1:17890"
	deleteReq.RemoteAddr = "127.0.0.1:12345"
	deleteReq.Header.Set("Content-Type", "application/json")
	deleteRR := httptest.NewRecorder()
	h.mux.ServeHTTP(deleteRR, deleteReq)
	if deleteRR.Code != http.StatusOK {
		t.Fatalf("clear status=%d body=%s", deleteRR.Code, deleteRR.Body.String())
	}
	rr = httptest.NewRecorder()
	h.mux.ServeHTTP(rr, localGet("/v1/events"))
	if rr.Code != http.StatusOK || !strings.Contains(rr.Body.String(), `"events":[]`) {
		t.Fatalf("after clear status=%d body=%s", rr.Code, rr.Body.String())
	}
}

func TestHealthMonitorStatusAndSettingsPersist(t *testing.T) {
	dir := t.TempDir()
	h := NewWithStateDir(apiSupervisor(t), dir)
	rr := httptest.NewRecorder()
	h.mux.ServeHTTP(rr, localGet("/v1/health/monitor"))
	if rr.Code != http.StatusOK || !strings.Contains(rr.Body.String(), `"checked_at"`) || !strings.Contains(rr.Body.String(), `"checks"`) {
		t.Fatalf("monitor status=%d body=%s", rr.Code, rr.Body.String())
	}

	settings := `{"enabled":true,"interval_seconds":45,"timeout_seconds":3,"fail_threshold":3,"recover_threshold":2}`
	rr = httptest.NewRecorder()
	h.mux.ServeHTTP(rr, localPut("/v1/health/settings", settings))
	if rr.Code != http.StatusOK {
		t.Fatalf("settings save=%d body=%s", rr.Code, rr.Body.String())
	}
	info, err := os.Stat(filepath.Join(dir, "health", "settings.json"))
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0600 {
		t.Fatalf("settings mode=%o", info.Mode().Perm())
	}
	reloaded := NewWithStateDir(apiSupervisor(t), dir)
	rr = httptest.NewRecorder()
	reloaded.mux.ServeHTTP(rr, localGet("/v1/health/settings"))
	if rr.Code != http.StatusOK || !strings.Contains(rr.Body.String(), `"interval_seconds":45`) || !strings.Contains(rr.Body.String(), `"fail_threshold":3`) {
		t.Fatalf("settings reload=%d body=%s", rr.Code, rr.Body.String())
	}

	rr = httptest.NewRecorder()
	h.mux.ServeHTTP(rr, localPut("/v1/health/settings", `{"enabled":true,"interval_seconds":5,"timeout_seconds":5,"fail_threshold":2,"recover_threshold":2}`))
	if rr.Code != http.StatusUnprocessableEntity {
		t.Fatalf("invalid settings status=%d body=%s", rr.Code, rr.Body.String())
	}

	rr = httptest.NewRecorder()
	h.mux.ServeHTTP(rr, localPost("/v1/health/check", `{}`))
	if rr.Code != http.StatusOK || !strings.Contains(rr.Body.String(), `"status"`) {
		t.Fatalf("manual check=%d body=%s", rr.Code, rr.Body.String())
	}
}

func TestDNSRoutesReturnUnavailableWithoutStateDirectory(t *testing.T) {
	h := New(apiSupervisor(t))
	for _, tc := range []struct{ method, path, body string }{
		{http.MethodGet, "/v1/dns", ""},
		{http.MethodPost, "/v1/dns/service", `{"action":"start"}`},
		{http.MethodPost, "/v1/dns/check", `{"domain":"example.com"}`},
	} {
		var req *http.Request
		if tc.method == http.MethodGet {
			req = localGet(tc.path)
		} else {
			req = localPost(tc.path, tc.body)
		}
		rr := httptest.NewRecorder()
		h.mux.ServeHTTP(rr, req)
		if rr.Code != http.StatusServiceUnavailable {
			t.Errorf("%s %s status=%d body=%s", tc.method, tc.path, rr.Code, rr.Body.String())
		}
	}
}

func TestDNSConfigStartFailureRestoresPreviousConfiguration(t *testing.T) {
	h := NewWithStateDir(apiSupervisor(t), t.TempDir())
	cfg := h.dns.Config()
	reservation, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	cfg.Port = reservation.LocalAddr().(*net.UDPAddr).Port
	if err := h.dns.Save(cfg); err != nil {
		t.Fatal(err)
	}
	cfg.Enabled = true
	body, err := json.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	rr := httptest.NewRecorder()
	h.mux.ServeHTTP(rr, localPut("/v1/dns", string(body)))
	_ = reservation.Close()
	if rr.Code != http.StatusInternalServerError {
		t.Fatalf("expected DNS bind failure, status=%d body=%s", rr.Code, rr.Body.String())
	}
	got := h.dns.Config()
	if got.Enabled || h.dns.Status()["running"].(bool) {
		t.Fatalf("failed DNS transaction left service enabled/running: enabled=%v status=%v", got.Enabled, h.dns.Status())
	}
	reloaded := h.dns.Config()
	if reloaded.Port != cfg.Port {
		t.Fatalf("rollback lost previous port: got=%d want=%d", reloaded.Port, cfg.Port)
	}
}

func TestServeBindFailureStopsDNSStartedForThatService(t *testing.T) {
	h := NewWithStateDir(apiSupervisor(t), t.TempDir())
	udp, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := udp.LocalAddr().(*net.UDPAddr).Port
	_ = udp.Close()
	cfg := h.dns.Config()
	cfg.Port = port
	cfg.Enabled = true
	if err := h.dns.Save(cfg); err != nil {
		t.Fatal(err)
	}
	apiListener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer apiListener.Close()
	if err := h.Serve(context.Background(), apiListener.Addr().String()); err == nil {
		t.Fatal("expected API listener bind failure")
	}
	if h.dns.Status()["running"].(bool) {
		t.Fatal("DNS service leaked after API listener bind failure")
	}
}

func TestPublicProbeAddressRejectsReservedAndCarrierGradeRanges(t *testing.T) {
	for _, address := range []string{"100.64.0.1", "192.0.2.1", "198.18.0.1", "2001:db8::1", "127.0.0.1", "10.0.0.1"} {
		if _, ok := publicProbeAddress(context.Background(), address); ok {
			t.Errorf("probe accepted non-public address %s", address)
		}
	}
	if got, ok := publicProbeAddress(context.Background(), "1.1.1.1"); !ok || got != "1.1.1.1" {
		t.Fatalf("public address rejected: %q %v", got, ok)
	}
}

func TestSmartDNSAPIStoresConfigAndRebuildsPreparedGeneration(t *testing.T) {
	stateDir := t.TempDir()
	h := NewWithStateDir(apiSupervisor(t), stateDir)
	profile := profiles.Profile{
		ID: "profile1", SourceID: "source1", Enabled: true, Listen: "127.0.0.1", Port: 16001,
		Links: []string{"vless://example.invalid"},
	}
	if err := h.profileStore.Save(profile); err != nil {
		t.Fatal(err)
	}
	dnsConfig := h.dns.Config()
	dnsConfig.BlockingEnabled = true
	if err := h.dns.Save(dnsConfig); err != nil {
		t.Fatal(err)
	}
	cfg := hrsmartdns.Config{
		Enabled: true, ListenHost: "127.0.0.1", ListenPort: 15354, CacheSize: 32,
		DomainSets: []hrsmartdns.DomainSet{
			{ID: "block-child", Kind: "block", Domains: []string{"ads.allow-parent.test"}, Enabled: true},
			{ID: "allow-parent", Kind: "allow", Domains: []string{"allow-parent.test"}, Enabled: true},
		},
		Upstreams: []hrsmartdns.Upstream{{
			ID: "upstream1", ProfileID: "profile1", Protocol: "doh",
			Host: "dns.example.com", BootstrapIP: "1.1.1.1", Enabled: true,
		}},
	}
	body, err := json.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	rr := httptest.NewRecorder()
	h.mux.ServeHTTP(rr, localPut("/v1/dns/smartdns", string(body)))
	if rr.Code != http.StatusOK {
		t.Fatalf("prepare SmartDNS config status=%d body=%s", rr.Code, rr.Body.String())
	}
	if h.smartDNSRuntime == nil || h.smartDNSRuntime.Status().Running {
		t.Fatal("SmartDNS should be prepared but not auto-started")
	}
	if h.smartDNSRuntime.Status().ConfigPath == "" {
		t.Fatalf("prepared SmartDNS config path missing: %+v", h.smartDNSRuntime.Status())
	}
	if !h.dns.IsBlockedHost("ads.allow-parent.test") {
		t.Fatal("SmartDNS block domain set was not applied to the Core DNS policy front")
	}
	if h.dns.IsBlockedHost("allow-parent.test") {
		t.Fatal("SmartDNS allow domain set was not applied to the Core DNS policy front")
	}
	if _, err := os.Stat(h.smartDNSConfigPath); err != nil {
		t.Fatalf("persisted SmartDNS settings missing: %v", err)
	}
	info, err := os.Stat(h.smartDNSConfigPath)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0600 {
		t.Fatalf("SmartDNS settings mode=%o, want 600", info.Mode().Perm())
	}
	rr = httptest.NewRecorder()
	h.mux.ServeHTTP(rr, localGet("/v1/dns/smartdns"))
	if rr.Code != http.StatusOK || !strings.Contains(rr.Body.String(), `"profile_id":"profile1"`) {
		t.Fatalf("SmartDNS GET status=%d body=%s", rr.Code, rr.Body.String())
	}
	reloaded := NewWithStateDir(apiSupervisor(t), stateDir)
	if !reloaded.smartDNSConfig.Enabled || reloaded.smartDNSErr != "" || reloaded.smartDNSRuntime.Status().Running {
		t.Fatalf("SmartDNS settings were not safely reconstructed: config=%+v err=%q status=%+v", reloaded.smartDNSConfig, reloaded.smartDNSErr, reloaded.smartDNSRuntime.Status())
	}
	if reloaded.smartDNSRuntime.Status().ConfigPath == "" {
		t.Fatal("reloaded SmartDNS generation was not prepared")
	}
	rr = httptest.NewRecorder()
	reloaded.mux.ServeHTTP(rr, localPost("/v1/dns/smartdns/stop", `{}`))
	if rr.Code != http.StatusOK {
		t.Fatalf("SmartDNS stop idempotence status=%d body=%s", rr.Code, rr.Body.String())
	}
}

func TestSmartDNSAPIRejectsProfileReferencesThatCannotBePinned(t *testing.T) {
	h := NewWithStateDir(apiSupervisor(t), t.TempDir())
	cfg := hrsmartdns.Config{
		Enabled: true, ListenHost: "127.0.0.1", ListenPort: 15354,
		Upstreams: []hrsmartdns.Upstream{{
			ID: "upstream1", ProfileID: "missing", Protocol: "doh",
			Host: "dns.example.com", BootstrapIP: "1.1.1.1", Enabled: true,
		}},
	}
	body, err := json.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	rr := httptest.NewRecorder()
	h.mux.ServeHTTP(rr, localPut("/v1/dns/smartdns", string(body)))
	if rr.Code != http.StatusUnprocessableEntity {
		t.Fatalf("missing profile reference status=%d body=%s", rr.Code, rr.Body.String())
	}
	if h.smartDNSConfig.Enabled {
		t.Fatal("rejected SmartDNS config became active in memory")
	}
}

func TestDNSBlocklistAPIManagesSourcesAndFeedsCoreResolver(t *testing.T) {
	stateDir := t.TempDir()
	h := NewWithStateDir(apiSupervisor(t), stateDir)
	fetcher := func(context.Context, string) (dnsblocklist.Result, error) {
		return dnsblocklist.Result{Blocked: []string{"ads.example.com"}, Allowed: []string{"safe.example.com"}}, nil
	}
	manager, err := dnsblocklist.NewManager(filepath.Join(stateDir, "dns", "blocklists", "state.json"), fetcher)
	if err != nil {
		t.Fatal(err)
	}
	h.blocklists = manager
	h.applyDNSBlocklists()
	cfg := h.dns.Config()
	cfg.BlockingEnabled = true
	if err := h.dns.Save(cfg); err != nil {
		t.Fatal(err)
	}

	rr := httptest.NewRecorder()
	h.mux.ServeHTTP(rr, localPost("/v1/dns/blocklists", `{"name":"Ads","url":"https://lists.example/ads.txt","enabled":true}`))
	if rr.Code != http.StatusCreated {
		t.Fatalf("create source status=%d body=%s", rr.Code, rr.Body.String())
	}
	var created struct {
		Source dnsblocklist.Source `json:"source"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &created); err != nil {
		t.Fatal(err)
	}
	if created.Source.ID == "" {
		t.Fatalf("source ID missing: %s", rr.Body.String())
	}

	rr = httptest.NewRecorder()
	h.mux.ServeHTTP(rr, localPost("/v1/dns/blocklists/"+created.Source.ID+"/refresh", `{}`))
	if rr.Code != http.StatusOK {
		t.Fatalf("refresh source status=%d body=%s", rr.Code, rr.Body.String())
	}
	if !h.dns.IsBlockedHost("tracker.ads.example.com") {
		t.Fatal("refreshed blocklist did not affect Core DNS resolver")
	}
	if h.dns.IsBlockedHost("safe.example.com") {
		t.Fatal("blocklist allow exception did not reach Core DNS resolver")
	}

	reloaded := NewWithStateDir(apiSupervisor(t), stateDir)
	if !reloaded.dns.IsBlockedHost("tracker.ads.example.com") {
		t.Fatal("persisted blocklist was not restored into Core DNS resolver")
	}

	rr = httptest.NewRecorder()
	h.mux.ServeHTTP(rr, localPost("/v1/dns/blocklists/"+created.Source.ID+"/remove", `{"domain":"ads.example.com"}`))
	if rr.Code != http.StatusOK || h.dns.IsBlockedHost("tracker.ads.example.com") {
		t.Fatalf("remove entry failed: status=%d body=%s", rr.Code, rr.Body.String())
	}

	rr = httptest.NewRecorder()
	h.mux.ServeHTTP(rr, localPost("/v1/dns/blocklists/"+created.Source.ID+"/restore", `{"domain":"ads.example.com"}`))
	if rr.Code != http.StatusOK || !h.dns.IsBlockedHost("tracker.ads.example.com") {
		t.Fatalf("restore entry failed: status=%d body=%s", rr.Code, rr.Body.String())
	}

	patch := localPost("/v1/dns/blocklists/"+created.Source.ID, `{"enabled":false}`)
	patch.Method = http.MethodPatch
	rr = httptest.NewRecorder()
	h.mux.ServeHTTP(rr, patch)
	if rr.Code != http.StatusOK || h.dns.IsBlockedHost("tracker.ads.example.com") {
		t.Fatalf("disable source failed: status=%d body=%s", rr.Code, rr.Body.String())
	}

	del := localGet("/v1/dns/blocklists/" + created.Source.ID)
	del.Method = http.MethodDelete
	rr = httptest.NewRecorder()
	h.mux.ServeHTTP(rr, del)
	if rr.Code != http.StatusOK || len(h.blocklists.Snapshot().Sources) != 0 {
		t.Fatalf("delete source failed: status=%d body=%s", rr.Code, rr.Body.String())
	}
}

func TestDNSBlocklistCatalogAPIRequiresLocalReadAndReturnsPresets(t *testing.T) {
	server := NewWithStateDir(apiSupervisor(t), t.TempDir())
	response := httptest.NewRecorder()
	server.mux.ServeHTTP(response, localGet("/v1/dns/blocklists/catalog"))
	if response.Code != http.StatusOK {
		t.Fatalf("catalog GET status=%d body=%s", response.Code, response.Body.String())
	}
	var payload struct {
		Presets []dnsblocklist.Preset `json:"presets"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	if len(payload.Presets) != 7 {
		t.Fatalf("catalog returned %d presets, want 7: %+v", len(payload.Presets), payload.Presets)
	}
	remote := httptest.NewRequest(http.MethodGet, "/v1/dns/blocklists/catalog", nil)
	remote.RemoteAddr = "192.0.2.10:12345"
	remote.Host = "127.0.0.1:17891"
	response = httptest.NewRecorder()
	server.mux.ServeHTTP(response, remote)
	if response.Code != http.StatusForbidden {
		t.Fatalf("remote catalog request status=%d, want forbidden", response.Code)
	}
}

func TestDNSBlocklistRefreshEndpointsRequireSameOriginJSON(t *testing.T) {
	stateDir := t.TempDir()
	server := NewWithStateDir(apiSupervisor(t), stateDir)
	manager, err := dnsblocklist.NewManager(filepath.Join(stateDir, "dns", "blocklists", "state.json"), nil)
	if err != nil {
		t.Fatal(err)
	}
	source, err := manager.SaveSource(dnsblocklist.Source{Name: "Ads", URL: "https://lists.example/ads.txt", Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	server.blocklists = manager

	for _, path := range []string{
		"/v1/dns/blocklists/_refresh",
		"/v1/dns/blocklists/" + source.ID + "/refresh",
	} {
		req := httptest.NewRequest(http.MethodPost, path, strings.NewReader("{}"))
		req.RemoteAddr = "127.0.0.1:12345"
		req.Host = "127.0.0.1:17891"
		req.Header.Set("Origin", "http://127.0.0.1:3000")
		req.Header.Set("Content-Type", "application/json")
		response := httptest.NewRecorder()
		server.mux.ServeHTTP(response, req)
		if response.Code != http.StatusForbidden {
			t.Fatalf("cross-origin refresh %s status=%d body=%s", path, response.Code, response.Body.String())
		}
	}
}

func TestDNSBlocklistCustomAPIUpdatesCorePolicyAndPersists(t *testing.T) {
	stateDir := t.TempDir()
	server := NewWithStateDir(apiSupervisor(t), stateDir)
	dnsConfig := server.dns.Config()
	dnsConfig.BlockingEnabled = true
	if err := server.dns.Save(dnsConfig); err != nil {
		t.Fatal(err)
	}

	response := httptest.NewRecorder()
	server.mux.ServeHTTP(response, localPut("/v1/dns/blocklists/custom", `{"block":"ads.parent.test\\nblocked.allow-parent.test","allow":"allow-parent.test\\nsafe.parent.test"}`))
	if response.Code != http.StatusOK {
		t.Fatalf("custom list PUT status=%d body=%s", response.Code, response.Body.String())
	}
	for _, host := range []string{"ads.parent.test", "blocked.allow-parent.test"} {
		if !server.dns.IsBlockedHost(host) {
			t.Fatalf("custom block list did not block %s", host)
		}
	}
	for _, host := range []string{"allow-parent.test", "safe.parent.test"} {
		if server.dns.IsBlockedHost(host) {
			t.Fatalf("custom allow list did not allow %s", host)
		}
	}

	response = httptest.NewRecorder()
	server.mux.ServeHTTP(response, localGet("/v1/dns/blocklists/custom"))
	if response.Code != http.StatusOK {
		t.Fatalf("custom list GET status=%d body=%s", response.Code, response.Body.String())
	}
	var lists map[string]string
	if err := json.Unmarshal(response.Body.Bytes(), &lists); err != nil {
		t.Fatal(err)
	}
	if lists["block"] != "ads.parent.test\nblocked.allow-parent.test" || lists["allow"] != "allow-parent.test\nsafe.parent.test" {
		t.Fatalf("custom lists did not round-trip: %+v", lists)
	}

	response = httptest.NewRecorder()
	server.mux.ServeHTTP(response, localPut("/v1/dns/blocklists/custom", `{"block":"bad..domain","allow":""}`))
	if response.Code != http.StatusUnprocessableEntity {
		t.Fatalf("invalid custom list status=%d body=%s", response.Code, response.Body.String())
	}
	if !server.dns.IsBlockedHost("ads.parent.test") {
		t.Fatal("invalid custom update replaced the last known-good policy")
	}
}

func TestDNSBlocklistSettingsAPIAndDailySchedule(t *testing.T) {
	stateDir := t.TempDir()
	server := NewWithStateDir(apiSupervisor(t), stateDir)
	get := httptest.NewRecorder()
	server.mux.ServeHTTP(get, localGet("/v1/dns/blocklists/settings"))
	if get.Code != http.StatusOK {
		t.Fatalf("settings GET status=%d body=%s", get.Code, get.Body.String())
	}
	var initial struct {
		Settings dnsblocklist.RefreshSettings `json:"settings"`
	}
	if err := json.Unmarshal(get.Body.Bytes(), &initial); err != nil {
		t.Fatal(err)
	}
	if initial.Settings.AutoUpdate || initial.Settings.UpdateTime != "04:30" {
		t.Fatalf("HRNeo default daily refresh settings mismatch: %+v", initial.Settings)
	}

	put := httptest.NewRecorder()
	server.mux.ServeHTTP(put, localPut("/v1/dns/blocklists/settings", `{"autoUpdate":false,"updateTime":"05:15","iface":"eth1"}`))
	if put.Code != http.StatusOK {
		t.Fatalf("settings PUT status=%d body=%s", put.Code, put.Body.String())
	}
	if got := server.blocklists.Settings(); got.AutoUpdate || got.UpdateTime != "05:15" || got.Iface != "eth1" {
		t.Fatalf("settings PUT not persisted: %+v", got)
	}
	select {
	case <-server.blocklistWake:
	default:
		t.Fatal("saving schedule did not wake the daily scheduler")
	}

	loc := time.FixedZone("test", 3*60*60)
	now := time.Date(2026, 11, 11, 4, 30, 0, 0, loc)
	next := nextDailyBlocklistUpdate(now, "04:30")
	if !next.Equal(time.Date(2026, 11, 12, 4, 30, 0, 0, loc)) {
		t.Fatalf("daily schedule next run=%s", next)
	}
}
