package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"hydraroute.local/core/internal/profiles"
)

func TestProfileMuxSettingPersistsAndCompilesAcrossRestart(t *testing.T) {
	stateDir := filepath.Join(t.TempDir(), "state")
	profile := profiles.Profile{
		ID: "muxprofile", SourceID: "providerA", Enabled: true, Port: 16351,
		Links: []string{"vless://00000000-0000-4000-8000-000000000051@mux.example:443#mux"},
	}
	assertMuxCompiled := func(t *testing.T, h *Server) {
		t.Helper()
		config, err := h.supervisor.SnapshotConfig()
		if err != nil {
			t.Fatal(err)
		}
		var root map[string]any
		if err := json.Unmarshal(config, &root); err != nil {
			t.Fatal(err)
		}
		outbounds, ok := root["outbounds"].([]any)
		if !ok {
			t.Fatalf("compiled outbounds missing: %s", config)
		}
		for _, item := range outbounds {
			outbound, ok := item.(map[string]any)
			if !ok || outbound["protocol"] == "freedom" {
				continue
			}
			mux, ok := outbound["mux"].(map[string]any)
			if ok && mux["enabled"] == true && mux["concurrency"] == float64(8) && mux["xudpConcurrency"] == float64(8) {
				return
			}
		}
		t.Fatalf("enabled Mux settings not present in compiled Xray config: %s", config)
	}
	h := NewWithStateDir(apiSupervisor(t), stateDir)
	if err := h.profileStore.Save(profile); err != nil {
		t.Fatal(err)
	}

	update := localPut("/v1/profiles", `{"id":"muxprofile","mux_enabled":true}`)
	rr := httptest.NewRecorder()
	h.mux.ServeHTTP(rr, update)
	if rr.Code != http.StatusOK {
		t.Fatalf("enable Mux status=%d body=%s", rr.Code, rr.Body.String())
	}
	saved, err := h.profileStore.Get(profile.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !saved.MuxEnabled {
		t.Fatal("Mux setting was not persisted in the profile store")
	}

	rr = httptest.NewRecorder()
	h.mux.ServeHTTP(rr, localGet("/v1/profiles"))
	if rr.Code != http.StatusOK {
		t.Fatalf("list profiles status=%d body=%s", rr.Code, rr.Body.String())
	}
	var listing struct {
		Profiles []profiles.Metadata `json:"profiles"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &listing); err != nil {
		t.Fatal(err)
	}
	if len(listing.Profiles) != 1 || !listing.Profiles[0].MuxEnabled || !listing.Profiles[0].MuxCompatible {
		t.Fatalf("profile Mux metadata=%+v", listing.Profiles)
	}

	rr = httptest.NewRecorder()
	h.mux.ServeHTTP(rr, localPost("/v1/profiles/refresh", `{}`))
	if rr.Code != http.StatusOK {
		t.Fatalf("compile profile with Mux status=%d body=%s", rr.Code, rr.Body.String())
	}
	assertMuxCompiled(t, h)

	// Recreate the API server against the same persistent state directory,
	// simulating a Core process restart, then compile from the restored profile.
	restarted := NewWithStateDir(apiSupervisor(t), stateDir)
	restored, err := restarted.profileStore.Get(profile.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !restored.MuxEnabled {
		t.Fatal("Mux setting did not survive API/Core restart")
	}
	rr = httptest.NewRecorder()
	restarted.mux.ServeHTTP(rr, localPost("/v1/profiles/refresh", `{}`))
	if rr.Code != http.StatusOK {
		t.Fatalf("recompile after restart status=%d body=%s", rr.Code, rr.Body.String())
	}
	assertMuxCompiled(t, restarted)
}
