package api

import (
	"context"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"hydraroute.local/core/internal/geodata"
)

// geoDataActivate is intentionally opt-in: it only operates on an already
// initialized store explicitly selected by Xray, never on legacy assets.
func (s *Server) geoDataActivate(w http.ResponseWriter, r *http.Request) {
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
		writeError(w, http.StatusBadRequest, "invalid geodata update request")
		return
	}
	if s.supervisor == nil || s.stateDir == "" {
		writeError(w, http.StatusServiceUnavailable, "managed runtime required")
		return
	}
	root := filepath.Join(s.stateDir, "geodata")
	if os.Getenv("XRAY_LOCATION_ASSET") != filepath.Join(root, "current") {
		writeError(w, http.StatusConflict, "Xray must explicitly use managed GeoData generation")
		return
	}
	s.opMu.Lock()
	defer s.opMu.Unlock()
	if err := geodata.CheckStartupReady(root); err != nil {
		writeError(w, http.StatusConflict, "GeoData recovery required")
		return
	}
	store := &geodata.GenerationStore{Root: root}
	current, err := store.Current()
	if err != nil || current == "" {
		writeError(w, http.StatusConflict, "GeoData generation must be bootstrapped first")
		return
	}
	if !s.supervisor.Status().Running {
		writeError(w, http.StatusConflict, "Xray must be running")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 120*time.Second)
	defer cancel()
	ip, err := geodata.Fetch(ctx, req.GeoIP)
	if err != nil {
		writeError(w, http.StatusUnprocessableEntity, "GeoIP download or digest failed")
		return
	}
	site, err := geodata.Fetch(ctx, req.GeoSite)
	if err != nil {
		writeError(w, http.StatusUnprocessableEntity, "GeoSite download or digest failed")
		return
	}
	binary, config := s.supervisor.Paths()
	// Require sustained process liveness rather than a transient Running
	// flag immediately after exec; a failed child often exits shortly after.
	probe := func(ctx context.Context) error {
		ticker := time.NewTicker(200 * time.Millisecond)
		defer ticker.Stop()
		stableSince := time.Time{}
		for {
			if s.supervisor.Status().Running {
				if stableSince.IsZero() {
					stableSince = time.Now()
				}
				if time.Since(stableSince) >= 2*time.Second {
					return nil
				}
			} else {
				stableSince = time.Time{}
			}
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-ticker.C:
			}
		}
	}
	generation, err := store.Activate(ctx, ip, site, (geodata.XrayCheck{Binary: binary, Config: config}).Check, geodata.SupervisorAdapter{Supervisor: s.supervisor}, probe)
	if err != nil {
		s.recordEvent("error", "geodata", "activation_failed", "GeoData activation failed; inspect recovery state")
		writeError(w, http.StatusConflict, "GeoData activation failed; inspect status and event journal")
		return
	}
	s.recordEvent("info", "geodata", "activated", "Validated GeoData generation activated")
	writeJSON(w, http.StatusOK, map[string]any{"activated": true, "generation": generation})
}
