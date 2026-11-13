package api

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestManualRuntimeStartBlockedByPendingGeoData(t *testing.T) {
	state := t.TempDir()
	root := filepath.Join(state, "geodata")
	if err := os.Mkdir(root, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "transaction.json"), []byte(`{"previous":"generation-123","phase":"stopped"}`), 0600); err != nil {
		t.Fatal(err)
	}
	h := NewWithStateDir(apiSupervisor(t), state)
	rr := httptest.NewRecorder()
	h.mux.ServeHTTP(rr, localPost("/v1/runtime", `{"action":"start"}`))
	if rr.Code != http.StatusConflict {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
	}
	if !strings.Contains(rr.Body.String(), "GeoData recovery required") {
		t.Fatalf("missing safe error: %s", rr.Body.String())
	}
	if h.supervisor.Status().Running {
		t.Fatal("runtime started despite pending recovery")
	}
}
func TestManualRuntimeStartBlockedByUnsafeGeoDataRoot(t *testing.T) {
	state := t.TempDir()
	root := filepath.Join(state, "geodata")
	if err := os.Mkdir(root, 0777); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(root, 0777); err != nil {
		t.Fatal(err)
	}
	h := NewWithStateDir(apiSupervisor(t), state)
	rr := httptest.NewRecorder()
	h.mux.ServeHTTP(rr, localPost("/v1/runtime", `{"action":"start"}`))
	if rr.Code != http.StatusConflict {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
	}
}
