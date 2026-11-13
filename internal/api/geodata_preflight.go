package api

import (
	"context"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"hydraroute.local/core/internal/geodata"
)

// geoDataPreflight checks both pinned downloads and the current Xray config.
// It does not stop Xray, publish a generation, or change active assets.
func (s *Server) geoDataPreflight(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w)
		return
	}
	if !validLocalJSONRequest(r) {
		writeError(w, http.StatusForbidden, "local same-origin JSON request required")
		return
	}
	var req struct {
		GeoIP   geodata.Source `json:"geoip"`
		GeoSite geodata.Source `json:"geosite"`
	}
	if err := decodeStrictJSON(w, r, &req, 4096); err != nil {
		writeError(w, http.StatusBadRequest, "invalid geodata request")
		return
	}
	if s.supervisor == nil || s.stateDir == "" {
		writeError(w, http.StatusServiceUnavailable, "managed Xray and state directory required")
		return
	}
	binary, config := s.supervisor.Paths()
	if _, err := os.Stat(config); err != nil {
		writeError(w, http.StatusConflict, "prepared Xray config required")
		return
	}
	root := filepath.Join(s.stateDir, "geodata")
	if err := geodata.CheckStartupReady(root); err != nil {
		writeError(w, http.StatusConflict, "GeoData store needs recovery")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 100*time.Second)
	defer cancel()
	geoip, err := geodata.Fetch(ctx, req.GeoIP)
	if err != nil {
		writeError(w, http.StatusUnprocessableEntity, "GeoIP download or digest verification failed")
		return
	}
	geosite, err := geodata.Fetch(ctx, req.GeoSite)
	if err != nil {
		writeError(w, http.StatusUnprocessableEntity, "GeoSite download or digest verification failed")
		return
	}
	// Stage only in a private temporary directory, never in the active store.
	dir, err := os.MkdirTemp("", "hrcore-geodata-preflight-*")
	if err != nil {
		writeError(w, http.StatusInternalServerError, "cannot stage candidate")
		return
	}
	defer os.RemoveAll(dir)
	if err := os.Chmod(dir, 0700); err != nil {
		writeError(w, http.StatusInternalServerError, "cannot protect candidate")
		return
	}
	for name, data := range map[string][]byte{"geoip.dat": geoip, "geosite.dat": geosite} {
		if err := geodata.Validate(name, data); err != nil {
			writeError(w, http.StatusUnprocessableEntity, "invalid "+name)
			return
		}
		if err := os.WriteFile(filepath.Join(dir, name), data, 0600); err != nil {
			writeError(w, http.StatusInternalServerError, "cannot stage candidate")
			return
		}
	}
	if err := (geodata.XrayCheck{Binary: binary, Config: config}).Check(dir); err != nil {
		writeError(w, http.StatusUnprocessableEntity, "Xray rejected candidate databases")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"valid": true, "activated": false, "geoip_bytes": len(geoip), "geosite_bytes": len(geosite)})
}
