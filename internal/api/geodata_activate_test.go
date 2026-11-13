package api

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

func TestGeoDataActivateRejectsUnmanagedRuntimeWithoutDownload(t *testing.T) {
	state := t.TempDir()
	h := NewWithStateDir(apiSupervisor(t), state)
	request := `{"geoip":{"url":"https://example.com/ip","sha256":"bad"},"geosite":{"url":"https://example.com/site","sha256":"bad"}}`
	t.Setenv("XRAY_LOCATION_ASSET", filepath.Join(state, "legacy"))
	rr := httptest.NewRecorder()
	h.mux.ServeHTTP(rr, localPost("/v1/geodata/activate", request))
	if rr.Code != http.StatusConflict {
		t.Fatalf("unmanaged accepted: %d %s", rr.Code, rr.Body.String())
	}
	t.Setenv("XRAY_LOCATION_ASSET", filepath.Join(state, "geodata", "current"))
	rr = httptest.NewRecorder()
	h.mux.ServeHTTP(rr, localPost("/v1/geodata/activate", request))
	if rr.Code != http.StatusConflict {
		t.Fatalf("unbootstrapped accepted: %d %s", rr.Code, rr.Body.String())
	}
	if _, err := os.Stat(filepath.Join(state, "geodata")); !os.IsNotExist(err) {
		t.Fatalf("unexpected store mutation: %v", err)
	}
}
func TestGeoDataActivateRejectsUnsafeRequests(t *testing.T) {
	h := New(apiSupervisor(t))
	rr := httptest.NewRecorder()
	h.mux.ServeHTTP(rr, localGet("/v1/geodata/activate"))
	if rr.Code != http.StatusMethodNotAllowed {
		t.Fatalf("GET accepted: %d", rr.Code)
	}
	rr = httptest.NewRecorder()
	h.mux.ServeHTTP(rr, localPost("/v1/geodata/activate", `{"extra":true}`))
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("unknown JSON accepted: %d", rr.Code)
	}
	req := localPost("/v1/geodata/activate", `{}`)
	req.Header.Set("Origin", "https://attacker.example")
	rr = httptest.NewRecorder()
	h.mux.ServeHTTP(rr, req)
	if rr.Code != http.StatusForbidden {
		t.Fatalf("cross-origin accepted: %d", rr.Code)
	}
}
