package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"golang.org/x/net/proxy"
	"io"
	"mime"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"hydraroute.local/core/internal/compiler"
	"hydraroute.local/core/internal/dnsservice"
	"hydraroute.local/core/internal/parser"
	"hydraroute.local/core/internal/profiles"
	"hydraroute.local/core/internal/routeconfig"
	hrruntime "hydraroute.local/core/internal/runtime"
	"hydraroute.local/core/internal/service"
	"hydraroute.local/core/internal/state"
	"hydraroute.local/core/internal/ui"
)

const maxRequestBytes = 8 << 20

type Options struct {
	RefreshInterval time.Duration
	RetryMin        time.Duration
	RetryMax        time.Duration
	AutoStart       bool
}

type RefreshStatus struct {
	Enabled             bool       `json:"enabled"`
	IntervalSeconds     int64      `json:"interval_seconds"`
	Running             bool       `json:"running"`
	LastAttempt         *time.Time `json:"last_attempt,omitempty"`
	LastSuccess         *time.Time `json:"last_success,omitempty"`
	NextAttempt         *time.Time `json:"next_attempt,omitempty"`
	LastError           string     `json:"last_error,omitempty"`
	ConsecutiveFailures int        `json:"consecutive_failures"`
}

type Server struct {
	supervisor   *hrruntime.Supervisor
	stateDir     string
	profileStore *profiles.Store
	routingStore *routeconfig.Store
	dns          *dnsservice.Manager
	opMu         sync.Mutex
	refreshMu    sync.Mutex
	refreshOpts  Options
	refreshState RefreshStatus
	mux          *http.ServeMux
}

func New(supervisor *hrruntime.Supervisor) *Server {
	return NewWithStateDir(supervisor, "")
}

func NewWithStateDir(supervisor *hrruntime.Supervisor, stateDir string) *Server {
	return NewWithOptions(supervisor, stateDir, Options{})
}

func NewWithOptions(supervisor *hrruntime.Supervisor, stateDir string, opts Options) *Server {
	if opts.RetryMin <= 0 {
		opts.RetryMin = 30 * time.Second
	}
	if opts.RetryMax <= 0 {
		opts.RetryMax = 15 * time.Minute
	}
	if opts.RetryMax < opts.RetryMin {
		opts.RetryMax = opts.RetryMin
	}
	s := &Server{supervisor: supervisor, stateDir: stateDir, refreshOpts: opts, refreshState: RefreshStatus{Enabled: opts.RefreshInterval > 0, IntervalSeconds: int64(opts.RefreshInterval.Seconds())}, mux: http.NewServeMux()}
	if stateDir != "" {
		s.profileStore = profiles.NewStore(filepath.Join(stateDir, "profiles"))
		s.routingStore = routeconfig.NewStore(stateDir)
		s.dns = dnsservice.New(filepath.Join(stateDir, "dns", "config.json"))
	}
	s.mux.HandleFunc("/healthz", s.health)
	s.mux.HandleFunc("/v1/config", s.config)
	s.mux.HandleFunc("/v1/subscriptions/compile", s.compileSubscription)
	s.mux.HandleFunc("/v1/selections", s.selections)
	s.mux.HandleFunc("/v1/profiles/refresh", s.refreshProfiles)
	s.mux.HandleFunc("/v1/profiles/preview", s.profilePreview)
	s.mux.HandleFunc("/v1/profiles/test", s.profileTest)
	s.mux.HandleFunc("/v1/refresh/status", s.refreshStatus)
	s.mux.HandleFunc("/v1/profiles", s.profileCollection)
	s.mux.HandleFunc("/v1/interfaces", s.interfaces)
	s.mux.HandleFunc("/v1/routing", s.routingConfig)
	s.mux.HandleFunc("/v1/runtime", s.runtime)
	s.mux.HandleFunc("/v1/dns", s.dnsConfig)
	s.mux.HandleFunc("/v1/dns/service", s.dnsService)
	s.mux.HandleFunc("/v1/dns/check", s.dnsCheck)
	s.mux.Handle("/", ui.Handler())
	return s
}

// ListenAndServe refuses non-loopback binds. Remote access should be provided
// by an authenticated transport/tunnel rather than exposing this control API.
func (s *Server) ListenAndServe(address string) error {
	return s.Serve(context.Background(), address)
}

// Serve shuts down the HTTP listener and the managed Xray child when ctx ends.
func (s *Server) Serve(ctx context.Context, address string) error {
	host, _, err := net.SplitHostPort(address)
	if err != nil {
		return fmt.Errorf("invalid listen address: %w", err)
	}
	ip := net.ParseIP(host)
	if ip == nil || !ip.IsLoopback() {
		return fmt.Errorf("API listen address must be a literal loopback IP")
	}
	if s.dns != nil && s.dns.Config().Enabled {
		if err := s.dns.Start(); err != nil {
			return fmt.Errorf("start saved DNS service: %w", err)
		}
	}
	listener, err := net.Listen("tcp", address)
	if err != nil {
		return err
	}
	srv := &http.Server{Addr: address, Handler: s.mux, ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 15 * time.Second, WriteTimeout: 60 * time.Second, IdleTimeout: 60 * time.Second}
	serveErr := make(chan error, 1)
	go func() { serveErr <- srv.Serve(listener) }()
	schedulerCtx, schedulerCancel := context.WithCancel(ctx)
	schedulerDone := make(chan struct{})
	if s.refreshOpts.RefreshInterval > 0 {
		s.refreshMu.Lock()
		nextAttempt := time.Now().UTC().Add(s.refreshOpts.RefreshInterval)
		s.refreshState.NextAttempt = &nextAttempt
		s.refreshMu.Unlock()
		go func() { defer close(schedulerDone); s.runRefreshScheduler(schedulerCtx) }()
	} else {
		close(schedulerDone)
	}
	defer func() { schedulerCancel(); <-schedulerDone }()
	select {
	case err := <-serveErr:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	case <-ctx.Done():
		if s.dns != nil {
			_ = s.dns.Stop()
		}
		schedulerCancel()
		<-schedulerDone
		stopCtx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
		defer cancel()
		s.opMu.Lock()
		runtimeErr := s.supervisor.Stop(stopCtx)
		s.opMu.Unlock()
		shutdownErr := srv.Shutdown(stopCtx)
		if runtimeErr != nil {
			return fmt.Errorf("stop managed Xray: %w", runtimeErr)
		}
		if shutdownErr != nil {
			return fmt.Errorf("shutdown API: %w", shutdownErr)
		}
		return nil
	}
}

func (s *Server) health(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		methodNotAllowed(w)
		return
	}
	if !validLocalReadRequest(r) {
		writeError(w, http.StatusForbidden, "local request required")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"status": "ok", "runtime": s.supervisor.Status()})
}

func (s *Server) routingConfig(w http.ResponseWriter, r *http.Request) {
	if s.routingStore == nil {
		writeError(w, http.StatusServiceUnavailable, "routing config directory is not configured")
		return
	}
	switch r.Method {
	case http.MethodGet:
		if !validLocalReadRequest(r) {
			writeError(w, http.StatusForbidden, "local request required")
			return
		}
		cfg, found, err := s.routingStore.Load()
		if err != nil {
			writeError(w, http.StatusInternalServerError, "cannot read routing config")
			return
		}
		if !found {
			cfg = compiler.RoutingConfig{}
		}
		writeJSON(w, http.StatusOK, map[string]any{"routing": cfg, "configured": found})
	case http.MethodPut:
		if !validLocalJSONRequest(r) {
			writeError(w, http.StatusForbidden, "local same-origin JSON request required")
			return
		}
		s.opMu.Lock()
		defer s.opMu.Unlock()
		var cfg compiler.RoutingConfig
		if err := decodeStrictJSON(w, r, &cfg, 1<<20); err != nil {
			writeError(w, http.StatusBadRequest, "invalid routing config")
			return
		}
		previous, hadPrevious, err := s.routingStore.Load()
		if err != nil {
			writeError(w, http.StatusInternalServerError, "cannot read previous routing config")
			return
		}
		// Allow users to prepare dormant routing rules before adding their first
		// profile. Never leave a previously prepared runtime config stale.
		if !cfg.Enabled {
			stored, listErr := s.profileStore.ListProfiles()
			if listErr != nil {
				writeError(w, http.StatusInternalServerError, "cannot read stored profiles")
				return
			}
			if len(stored) == 0 {
				if s.supervisor.Status().Configured {
					writeError(w, http.StatusConflict, "cannot save disabled routing without stored profiles while a runtime config is prepared")
					return
				}
				if err := s.routingStore.Save(cfg); err != nil {
					writeError(w, http.StatusInternalServerError, "cannot save routing config")
					return
				}
				writeJSON(w, http.StatusOK, map[string]any{"routing": cfg, "status": "saved"})
				return
			}
		}
		if err := s.routingStore.Save(cfg); err != nil {
			writeError(w, http.StatusInternalServerError, "cannot save routing config")
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 90*time.Second)
		defer cancel()
		result, err := s.refreshProfilesLocked(ctx)
		if err != nil {
			var rollbackErr error
			if hadPrevious {
				rollbackErr = s.routingStore.Save(previous)
			} else {
				rollbackErr = s.routingStore.Delete()
			}
			if rollbackErr != nil {
				writeError(w, http.StatusInternalServerError, fmt.Sprintf("routing apply failed: %v; routing config rollback failed: %v", err, rollbackErr))
				return
			}
			writeError(w, http.StatusUnprocessableEntity, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"routing": cfg, "apply": result})
	default:
		methodNotAllowed(w)
	}
}

func routingReferencesProfile(cfg compiler.RoutingConfig, profileID string) bool {
	if cfg.DefaultAction == "profile" && cfg.DefaultProfileID == profileID {
		return true
	}
	for _, rule := range cfg.Rules {
		if rule.Enabled && rule.Action == "profile" && rule.ProfileID == profileID {
			return true
		}
	}
	return false
}

func (s *Server) config(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w)
		return
	}
	if !validLocalJSONRequest(r) {
		writeError(w, http.StatusForbidden, "local same-origin JSON request required")
		return
	}
	s.opMu.Lock()
	defer s.opMu.Unlock()
	r.Body = http.MaxBytesReader(w, r.Body, maxRequestBytes)
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	var req compiler.CompileRequest
	if err := dec.Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid compile request")
		return
	}
	var extra any
	if err := dec.Decode(&extra); err != io.EOF {
		writeError(w, http.StatusBadRequest, "request must contain one JSON value")
		return
	}
	req.DNS = s.coreDNSConfig()
	compiled, err := compiler.Compile(req)
	if err != nil {
		writeError(w, http.StatusUnprocessableEntity, err.Error())
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()
	if err := s.supervisor.Prepare(ctx, compiled); err != nil {
		writeError(w, http.StatusUnprocessableEntity, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"status": "prepared", "bytes": len(compiled), "runtime": s.supervisor.Status()})
}

func (s *Server) compileSubscription(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w)
		return
	}
	if !validLocalJSONRequest(r) {
		writeError(w, http.StatusForbidden, "local same-origin JSON request required")
		return
	}
	s.opMu.Lock()
	defer s.opMu.Unlock()
	if s.stateDir == "" {
		writeError(w, http.StatusServiceUnavailable, "selection state directory is not configured")
		return
	}
	var req service.SubscriptionRequest
	if err := decodeStrictJSON(w, r, &req, maxRequestBytes); err != nil {
		writeError(w, http.StatusBadRequest, "invalid subscription request")
		return
	}
	req.DNS = s.coreDNSConfig()
	if s.routingStore != nil {
		if routing, found, err := s.routingStore.Load(); err != nil {
			writeError(w, http.StatusInternalServerError, "cannot read routing config")
			return
		} else if found {
			req.Routing = &routing
		}
	}
	ctx, cancel := context.WithTimeout(r.Context(), 25*time.Second)
	defer cancel()
	result, err := service.CompileSubscriptions(ctx, req, s.stateDir)
	if err != nil {
		writeError(w, http.StatusUnprocessableEntity, err.Error())
		return
	}
	prepareCtx, prepareCancel := context.WithTimeout(r.Context(), 25*time.Second)
	defer prepareCancel()
	if err := s.supervisor.Prepare(prepareCtx, result.Config); err != nil {
		writeError(w, http.StatusUnprocessableEntity, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"status": "prepared", "profiles": result.ProfileCount, "nodes": result.NodeCount, "bytes": len(result.Config), "runtime": s.supervisor.Status()})
}

type selectionRequest struct {
	ID       string             `json:"id"`
	SourceID string             `json:"source_id"`
	PickMode string             `json:"pick_mode"`
	Picked   []parser.ServerKey `json:"picked,omitempty"`
	Excluded []parser.ServerKey `json:"excluded,omitempty"`
}

func (s *Server) selections(w http.ResponseWriter, r *http.Request) {
	if s.stateDir == "" {
		writeError(w, http.StatusServiceUnavailable, "selection state directory is not configured")
		return
	}
	switch r.Method {
	case http.MethodGet:
		if !validLocalReadRequest(r) {
			writeError(w, http.StatusForbidden, "local request required")
			return
		}
		profileID := r.URL.Query().Get("profile_id")
		profile, err := service.LoadSelection(s.stateDir, profileID)
		if err != nil {
			writeError(w, http.StatusNotFound, "selection state not found or invalid")
			return
		}
		writeJSON(w, http.StatusOK, profile)
	case http.MethodPut:
		if !validLocalJSONRequest(r) {
			writeError(w, http.StatusForbidden, "local same-origin JSON request required")
			return
		}
		s.opMu.Lock()
		defer s.opMu.Unlock()
		var req selectionRequest
		if err := decodeStrictJSON(w, r, &req, 1<<20); err != nil {
			writeError(w, http.StatusBadRequest, "invalid selection request")
			return
		}
		profile := state.Profile{ID: req.ID, SourceID: req.SourceID, PickMode: req.PickMode, Picked: req.Picked, Excluded: req.Excluded}
		if err := service.SaveSelection(s.stateDir, profile); err != nil {
			writeError(w, http.StatusUnprocessableEntity, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"status": "saved", "profile_id": profile.ID})
	default:
		methodNotAllowed(w)
	}
}

func decodeStrictJSON(w http.ResponseWriter, r *http.Request, dst any, limit int64) error {
	r.Body = http.MaxBytesReader(w, r.Body, limit)
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		return err
	}
	var extra any
	if err := dec.Decode(&extra); err != io.EOF {
		return fmt.Errorf("request must contain one JSON value")
	}
	return nil
}

func validLocalReadRequest(r *http.Request) bool {
	remoteHost, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil || net.ParseIP(remoteHost) == nil || !net.ParseIP(remoteHost).IsLoopback() {
		return false
	}
	host := r.Host
	if parsedHost, _, splitErr := net.SplitHostPort(host); splitErr == nil {
		host = parsedHost
	}
	if ip := net.ParseIP(host); ip == nil || !ip.IsLoopback() {
		if !strings.EqualFold(host, "localhost") {
			return false
		}
	}
	if !validLocalOrigin(r) {
		return false
	}
	return !strings.EqualFold(r.Header.Get("Sec-Fetch-Site"), "cross-site")
}

type networkInterfaceDTO struct {
	Name     string `json:"name"`
	Category string `json:"category"`
	Up       bool   `json:"up"`
	Loopback bool   `json:"loopback"`
}

// interfaces exposes read-only Linux network-device metadata. It never changes routes or link state.
func (s *Server) interfaces(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		methodNotAllowed(w)
		return
	}
	if !validLocalReadRequest(r) {
		writeError(w, http.StatusForbidden, "local request required")
		return
	}
	items, err := net.Interfaces()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "cannot enumerate network interfaces")
		return
	}
	defaultIf := defaultRouteInterface()
	result := make([]networkInterfaceDTO, 0, len(items))
	for _, item := range items {
		if item.Name == "lo" || item.Flags&net.FlagLoopback != 0 {
			continue
		}
		iface := networkInterfaceDTO{Name: item.Name, Up: item.Flags&net.FlagUp != 0, Loopback: false, Category: "Остальное"}
		lower := strings.ToLower(item.Name)
		if item.Flags&net.FlagPointToPoint != 0 || strings.HasPrefix(lower, "tun") || strings.HasPrefix(lower, "tap") || strings.HasPrefix(lower, "wg") || strings.HasPrefix(lower, "tailscale") || strings.HasPrefix(lower, "ppp") || strings.HasPrefix(lower, "vpn") {
			iface.Category = "VPN"
		} else if item.Name == defaultIf || strings.HasPrefix(lower, "eth") || strings.HasPrefix(lower, "en") || strings.HasPrefix(lower, "wlan") || strings.HasPrefix(lower, "wlp") || strings.HasPrefix(lower, "wwan") || strings.HasPrefix(lower, "usb") {
			iface.Category = "Интернет"
		}
		result = append(result, iface)
	}
	writeJSON(w, http.StatusOK, map[string]any{"interfaces": result, "default_interface": defaultIf})
}

func defaultRouteInterface() string {
	data, err := os.ReadFile("/proc/net/route")
	if err != nil {
		return ""
	}
	for _, line := range strings.Split(string(data), "\n") {
		fields := strings.Fields(line)
		if len(fields) > 3 && fields[1] == "00000000" {
			flags, _ := strconv.ParseInt(fields[3], 16, 64)
			if flags&1 != 0 {
				return fields[0]
			}
		}
	}
	return ""
}

func validateOutboundInterface(name string) error {
	if name == "" {
		return nil
	}
	iface, err := net.InterfaceByName(name)
	if err != nil || iface.Flags&net.FlagLoopback != 0 {
		return fmt.Errorf("unknown or unsupported outbound interface")
	}
	return nil
}

func (s *Server) profileCollection(w http.ResponseWriter, r *http.Request) {
	if s.profileStore == nil {
		writeError(w, http.StatusServiceUnavailable, "profile store is not configured")
		return
	}
	switch r.Method {
	case http.MethodGet:
		if !validLocalReadRequest(r) {
			writeError(w, http.StatusForbidden, "local request required")
			return
		}
		items, err := s.profileStore.List()
		if err != nil {
			writeError(w, http.StatusInternalServerError, "cannot read profile metadata")
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"profiles": items})
	case http.MethodPost:
		if !validLocalJSONRequest(r) {
			writeError(w, http.StatusForbidden, "local same-origin JSON request required")
			return
		}
		s.opMu.Lock()
		defer s.opMu.Unlock()
		var p profiles.Profile
		if err := decodeStrictJSON(w, r, &p, maxRequestBytes); err != nil {
			writeError(w, http.StatusBadRequest, "invalid profile request")
			return
		}
		if err := validateOutboundInterface(p.OutboundInterface); err != nil {
			writeError(w, http.StatusUnprocessableEntity, err.Error())
			return
		}
		if err := s.profileStore.Save(p); err != nil {
			writeError(w, http.StatusUnprocessableEntity, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"status": "saved", "profile_id": p.ID})
	case http.MethodPut:
		if !validLocalJSONRequest(r) {
			writeError(w, http.StatusForbidden, "local same-origin JSON request required")
			return
		}
		s.opMu.Lock()
		defer s.opMu.Unlock()
		var req struct {
			ID                string              `json:"id"`
			Enabled           *bool               `json:"enabled,omitempty"`
			Port              *uint16             `json:"port,omitempty"`
			Strategy          *string             `json:"strategy,omitempty"`
			OutboundInterface *string             `json:"outbound_interface,omitempty"`
			DPIBypass         *profiles.DPIBypass `json:"dpi_bypass,omitempty"`
		}
		if err := decodeStrictJSON(w, r, &req, 4096); err != nil {
			writeError(w, http.StatusBadRequest, "invalid profile update")
			return
		}
		p, err := s.profileStore.Get(req.ID)
		if err != nil {
			writeError(w, http.StatusNotFound, "profile not found")
			return
		}
		previous := p
		if req.Enabled != nil {
			p.Enabled = *req.Enabled
		}
		if req.Port != nil {
			p.Port = *req.Port
		}
		if req.Strategy != nil {
			p.Strategy = *req.Strategy
		}
		if req.OutboundInterface != nil {
			p.OutboundInterface = strings.TrimSpace(*req.OutboundInterface)
		}
		if err := validateOutboundInterface(p.OutboundInterface); err != nil {
			writeError(w, http.StatusUnprocessableEntity, err.Error())
			return
		}
		if req.DPIBypass != nil {
			p.DPIBypass = *req.DPIBypass
		}
		if err := profiles.Validate(p); err != nil {
			writeError(w, http.StatusUnprocessableEntity, err.Error())
			return
		}
		if p.Port < 1024 || (p.Strategy != "" && p.Strategy != "leastPing" && p.Strategy != "roundRobin" && p.Strategy != "random") {
			writeError(w, http.StatusUnprocessableEntity, "invalid profile port or balancing strategy")
			return
		}
		if err := s.profileStore.Save(p); err != nil {
			writeError(w, http.StatusInternalServerError, "cannot save profile settings")
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 90*time.Second)
		defer cancel()
		if s.supervisor.Status().Configured {
			if _, err := s.refreshProfilesLocked(ctx); err != nil {
				_ = s.profileStore.Save(previous)
				writeError(w, http.StatusUnprocessableEntity, "profile update was rolled back: "+err.Error())
				return
			}
		}
		writeJSON(w, http.StatusOK, map[string]any{"status": "updated", "profile_id": p.ID, "enabled": p.Enabled, "port": p.Port, "strategy": p.Strategy, "outbound_interface": p.OutboundInterface, "dpi_bypass": p.DPIBypass, "runtime": s.supervisor.Status()})
	case http.MethodDelete:
		if !validLocalJSONRequest(r) {
			writeError(w, http.StatusForbidden, "local same-origin JSON request required")
			return
		}
		s.opMu.Lock()
		defer s.opMu.Unlock()
		var req struct {
			ID string `json:"id"`
		}
		if err := decodeStrictJSON(w, r, &req, 4096); err != nil {
			writeError(w, http.StatusBadRequest, "invalid profile delete request")
			return
		}
		if s.supervisor.Status().Running {
			writeError(w, http.StatusConflict, "stop runtime before deleting a profile")
			return
		}
		if s.routingStore != nil {
			if routing, found, loadErr := s.routingStore.Load(); loadErr != nil {
				writeError(w, http.StatusInternalServerError, "cannot inspect shared routing references")
				return
			} else if found && routingReferencesProfile(routing, req.ID) {
				writeError(w, http.StatusConflict, "remove routing rules referencing this profile before deleting it")
				return
			}
		}
		if err := service.DeleteSelection(s.stateDir, req.ID); err != nil {
			writeError(w, http.StatusUnprocessableEntity, err.Error())
			return
		}
		if err := s.profileStore.Delete(req.ID); err != nil {
			writeError(w, http.StatusUnprocessableEntity, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"status": "deleted", "profile_id": req.ID})
	default:
		methodNotAllowed(w)
	}
}

// profileTest measures TCP reachability through one enabled profile SOCKS5
// inbound. It never tests through the host's default route directly.
func (s *Server) profileTest(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w)
		return
	}
	if !validLocalJSONRequest(r) {
		writeError(w, http.StatusForbidden, "local same-origin JSON request required")
		return
	}
	var req struct {
		ID string `json:"id"`
	}
	if err := decodeStrictJSON(w, r, &req, 4096); err != nil {
		writeError(w, http.StatusBadRequest, "invalid profile test request")
		return
	}
	if !s.supervisor.Status().Running {
		writeError(w, http.StatusConflict, "start Xray before testing a connection")
		return
	}
	p, err := s.profileStore.Get(req.ID)
	if err != nil {
		writeError(w, http.StatusNotFound, "profile not found")
		return
	}
	if !p.Enabled {
		writeError(w, http.StatusConflict, "connection is disabled")
		return
	}
	address := net.JoinHostPort(p.Listen, fmt.Sprint(p.Port))
	if p.Listen == "" {
		address = net.JoinHostPort("127.0.0.1", fmt.Sprint(p.Port))
	}
	dialer, err := proxy.SOCKS5("tcp", address, nil, &net.Dialer{Timeout: 8 * time.Second})
	if err != nil {
		writeError(w, http.StatusBadGateway, "cannot initialize SOCKS5 test")
		return
	}
	started := time.Now()
	conn, err := dialer.Dial("tcp", "www.gstatic.com:443")
	elapsed := time.Since(started).Milliseconds()
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]any{"status": "unavailable", "latency_ms": nil, "error": "connection test through SOCKS5 failed"})
		return
	}
	_ = conn.Close()
	writeJSON(w, http.StatusOK, map[string]any{"status": "online", "latency_ms": elapsed, "target": "www.gstatic.com:443"})
}

func (s *Server) profilePreview(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w)
		return
	}
	if !validLocalJSONRequest(r) {
		writeError(w, http.StatusForbidden, "local same-origin JSON request required")
		return
	}
	s.opMu.Lock()
	defer s.opMu.Unlock()
	if s.profileStore == nil {
		writeError(w, http.StatusServiceUnavailable, "profile store is not configured")
		return
	}
	var req struct {
		ProfileID string `json:"profile_id"`
	}
	if err := decodeStrictJSON(w, r, &req, 4096); err != nil {
		writeError(w, http.StatusBadRequest, "invalid profile preview request")
		return
	}
	profile, err := s.profileStore.Get(req.ProfileID)
	if err != nil {
		writeError(w, http.StatusNotFound, "profile not found or invalid")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 90*time.Second)
	defer cancel()
	preview, err := service.PreviewProfile(ctx, profile, s.stateDir)
	if err != nil {
		writeError(w, http.StatusUnprocessableEntity, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, preview)
}

func (s *Server) refreshProfiles(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w)
		return
	}
	if !validLocalJSONRequest(r) {
		writeError(w, http.StatusForbidden, "local same-origin JSON request required")
		return
	}
	s.opMu.Lock()
	defer s.opMu.Unlock()
	var req struct{}
	if err := decodeStrictJSON(w, r, &req, 4096); err != nil {
		writeError(w, http.StatusBadRequest, "refresh request must be an empty JSON object")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 90*time.Second)
	defer cancel()
	result, err := s.refreshProfilesLocked(ctx)
	if err != nil {
		writeError(w, http.StatusUnprocessableEntity, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, result)
}

func (s *Server) refreshProfilesLocked(ctx context.Context) (map[string]any, error) {
	if s.profileStore == nil {
		return nil, fmt.Errorf("profile store is not configured")
	}
	stored, err := s.profileStore.ListProfiles()
	if err != nil {
		return nil, fmt.Errorf("cannot load encrypted profile sources")
	}
	if len(stored) == 0 {
		return nil, fmt.Errorf("no profiles are stored")
	}
	inputs := make([]service.ProfileRequest, 0, len(stored))
	for _, p := range stored {
		if err := validateOutboundInterface(p.OutboundInterface); err != nil {
			return nil, fmt.Errorf("profile %s: %w", p.ID, err)
		}
		inputs = append(inputs, service.ProfileRequest{Source: p.SourceID, Profile: service.ProfileInput{ID: p.ID, Enabled: p.Enabled, Listen: p.Listen, Port: p.Port, Strategy: p.Strategy, OutboundInterface: p.OutboundInterface, DPIBypass: compiler.DPIBypass{NoiseTraffic: p.DPIBypass.NoiseTraffic, TLSFragmentation: p.DPIBypass.TLSFragmentation, RandomTLSFingerprint: p.DPIBypass.RandomTLSFingerprint}}, Links: p.Links, Subscription: p.Subscription, SubscriptionURL: p.SubscriptionURL})
	}
	var routing *compiler.RoutingConfig
	if s.routingStore != nil {
		if storedRouting, found, loadErr := s.routingStore.Load(); loadErr != nil {
			return nil, fmt.Errorf("cannot load shared routing config: %w", loadErr)
		} else if found {
			routing = &storedRouting
		}
	}
	result, err := service.CompileSubscriptions(ctx, service.SubscriptionRequest{Profiles: inputs, Routing: routing, DNS: s.coreDNSConfig()}, s.stateDir)
	if err != nil {
		return nil, err
	}
	wasRunning := s.supervisor.Status().Running
	shouldRun := wasRunning || s.refreshOpts.AutoStart
	previousConfig, err := s.supervisor.SnapshotConfig()
	if err != nil {
		return nil, fmt.Errorf("cannot snapshot last-good config: %w", err)
	}
	if wasRunning {
		stopCtx, cancel := context.WithTimeout(ctx, 7*time.Second)
		stopErr := s.supervisor.Stop(stopCtx)
		cancel()
		if stopErr != nil {
			return nil, fmt.Errorf("cannot stop Xray for refresh: %w", stopErr)
		}
	}
	prepareErr := s.supervisor.Prepare(ctx, result.Config)
	if prepareErr != nil {
		if wasRunning {
			if restartErr := s.supervisor.Start(context.Background()); restartErr != nil {
				return nil, fmt.Errorf("refresh failed and previous Xray could not be restarted: %w", restartErr)
			}
		}
		return nil, fmt.Errorf("refresh config validation failed; previous config retained: %w", prepareErr)
	}
	if shouldRun {
		startErr := s.supervisor.Start(context.Background())
		if startErr == nil {
			// Starting the child does not prove that Xray accepted its ports.
			// Detect immediate startup exits before reporting refresh success.
			timer := time.NewTimer(350 * time.Millisecond)
			select {
			case <-ctx.Done():
				timer.Stop()
				startErr = ctx.Err()
			case <-timer.C:
				if !s.supervisor.Status().Running {
					startErr = fmt.Errorf("Xray exited immediately after startup")
				}
			}
		}
		if startErr != nil {
			if s.supervisor.Status().Running {
				stopCtx, stopCancel := context.WithTimeout(context.Background(), 7*time.Second)
				stopErr := s.supervisor.Stop(stopCtx)
				stopCancel()
				if stopErr != nil {
					return nil, fmt.Errorf("new Xray failed and cannot be stopped for rollback: %w", stopErr)
				}
			}
			if len(previousConfig) == 0 {
				return nil, fmt.Errorf("new Xray startup failed; no previous config to restore: %w", startErr)
			}
			restoreCtx, restoreCancel := context.WithTimeout(context.Background(), 20*time.Second)
			restoreErr := s.supervisor.Prepare(restoreCtx, previousConfig)
			restoreCancel()
			if restoreErr != nil {
				return nil, fmt.Errorf("new Xray startup failed; previous config restore failed: %w", restoreErr)
			}
			if wasRunning {
				if restartErr := s.supervisor.Start(context.Background()); restartErr != nil {
					return nil, fmt.Errorf("new Xray startup failed; previous config restored but restart failed: %w", restartErr)
				}
			}
			return nil, fmt.Errorf("new Xray startup failed; previous config restored: %w", startErr)
		}
	}
	return map[string]any{"status": "prepared", "profiles": result.ProfileCount, "nodes": result.NodeCount, "bytes": len(result.Config), "runtime": s.supervisor.Status()}, nil
}

func (s *Server) refreshStatus(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		methodNotAllowed(w)
		return
	}
	if !validLocalReadRequest(r) {
		writeError(w, http.StatusForbidden, "local request required")
		return
	}
	s.refreshMu.Lock()
	status := s.refreshState
	s.refreshMu.Unlock()
	writeJSON(w, http.StatusOK, status)
}

func (s *Server) runRefreshScheduler(ctx context.Context) {
	interval := s.refreshOpts.RefreshInterval
	timer := time.NewTimer(interval)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
		}
		now := time.Now().UTC()
		s.refreshMu.Lock()
		s.refreshState.Running = true
		s.refreshState.LastAttempt = &now
		s.refreshMu.Unlock()
		refreshCtx, cancel := context.WithTimeout(ctx, 90*time.Second)
		s.opMu.Lock()
		_, err := s.refreshProfilesLocked(refreshCtx)
		s.opMu.Unlock()
		cancel()
		next := interval
		s.refreshMu.Lock()
		s.refreshState.Running = false
		if err == nil {
			lastSuccess := time.Now().UTC()
			s.refreshState.LastSuccess = &lastSuccess
			s.refreshState.LastError = ""
			s.refreshState.ConsecutiveFailures = 0
		} else {
			s.refreshState.LastError = err.Error()
			s.refreshState.ConsecutiveFailures++
			next = s.refreshOpts.RetryMin
			for i := 1; i < s.refreshState.ConsecutiveFailures && next < s.refreshOpts.RetryMax; i++ {
				if next > s.refreshOpts.RetryMax/2 {
					next = s.refreshOpts.RetryMax
					break
				}
				next *= 2
			}
			if next > s.refreshOpts.RetryMax {
				next = s.refreshOpts.RetryMax
			}
		}
		nextAttempt := time.Now().UTC().Add(next)
		s.refreshState.NextAttempt = &nextAttempt
		s.refreshMu.Unlock()
		timer.Reset(next)
	}
}

func (s *Server) runtime(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		if !validLocalReadRequest(r) {
			writeError(w, http.StatusForbidden, "local request required")
			return
		}
		writeJSON(w, http.StatusOK, s.supervisor.Status())
	case http.MethodPost:
		if !validLocalJSONRequest(r) {
			writeError(w, http.StatusForbidden, "local same-origin JSON request required")
			return
		}
		s.opMu.Lock()
		defer s.opMu.Unlock()
		var body struct {
			Action string `json:"action"`
		}
		if err := decodeStrictJSON(w, r, &body, 4096); err != nil {
			writeError(w, http.StatusBadRequest, "invalid runtime action")
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 8*time.Second)
		defer cancel()
		switch strings.ToLower(body.Action) {
		case "start":
			if err := s.supervisor.Start(ctx); err != nil {
				writeError(w, http.StatusConflict, err.Error())
				return
			}
		case "stop":
			if err := s.supervisor.Stop(ctx); err != nil {
				writeError(w, http.StatusConflict, err.Error())
				return
			}
		default:
			writeError(w, http.StatusBadRequest, "action must be start or stop")
			return
		}
		writeJSON(w, http.StatusOK, s.supervisor.Status())
	default:
		methodNotAllowed(w)
	}
}

func validLocalOrigin(r *http.Request) bool {
	origin := r.Header.Get("Origin")
	if origin == "" {
		return true
	}
	u, err := url.Parse(origin)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.User != nil || u.Host == "" || u.Path != "" || u.RawQuery != "" || u.Fragment != "" {
		return false
	}
	// Compare the complete authority, including port. Matching only localhost
	// would allow a page on localhost:3000 to pass a request to localhost:17890.
	return strings.EqualFold(u.Host, r.Host)
}

func validLocalJSONRequest(r *http.Request) bool {
	remoteHost, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil || net.ParseIP(remoteHost) == nil || !net.ParseIP(remoteHost).IsLoopback() {
		return false
	}
	host := r.Host
	if parsedHost, _, splitErr := net.SplitHostPort(host); splitErr == nil {
		host = parsedHost
	}
	if ip := net.ParseIP(host); ip == nil || !ip.IsLoopback() {
		if !strings.EqualFold(host, "localhost") {
			return false
		}
	}
	if !validLocalOrigin(r) {
		return false
	}
	if strings.EqualFold(r.Header.Get("Sec-Fetch-Site"), "cross-site") {
		return false
	}
	mediaType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	return err == nil && mediaType == "application/json"
}

func methodNotAllowed(w http.ResponseWriter) {
	writeError(w, http.StatusMethodNotAllowed, "method not allowed")
}
func writeError(w http.ResponseWriter, status int, message string) {
	writeJSON(w, status, map[string]string{"error": message})
}
func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func (s *Server) coreDNSConfig() *compiler.DNSConfig {
	if s.dns == nil {
		return nil
	}
	c := s.dns.Config()
	if !c.Enabled {
		return nil
	}
	return &compiler.DNSConfig{Enabled: true, Listen: c.Listen, Port: uint16(c.Port)}
}

// applyCoreDNSRuntime updates only the managed Xray config. The host resolver,
// NetworkManager, routes and firewall are never touched. If Xray is running,
// it is restarted with the new per-Core DNS setting and rolled back on error.
func (s *Server) applyCoreDNSRuntime(enabled bool) error {
	previous, err := s.supervisor.SnapshotConfig()
	if err != nil || len(previous) == 0 {
		return err
	}
	var cfg map[string]any
	if err := json.Unmarshal(previous, &cfg); err != nil {
		return fmt.Errorf("cannot read prepared Xray config: %w", err)
	}
	if enabled {
		d := s.coreDNSConfig()
		if d == nil {
			return fmt.Errorf("Core DNS settings are not enabled")
		}
		cfg["dns"] = map[string]any{"servers": []any{map[string]any{"address": d.Listen, "port": d.Port}}}
	} else {
		delete(cfg, "dns")
	}
	next, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	wasRunning := s.supervisor.Status().Running
	if wasRunning {
		ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
		err = s.supervisor.Stop(ctx)
		cancel()
		if err != nil {
			return fmt.Errorf("cannot stop Xray to apply Core DNS: %w", err)
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	err = s.supervisor.Prepare(ctx, next)
	if err == nil && wasRunning {
		err = s.supervisor.Start(ctx)
	}
	if err != nil {
		rollbackErr := s.supervisor.Prepare(ctx, previous)
		if rollbackErr == nil && wasRunning {
			rollbackErr = s.supervisor.Start(ctx)
		}
		cancel()
		if rollbackErr != nil {
			return fmt.Errorf("apply Core DNS failed: %v; rollback failed: %w", err, rollbackErr)
		}
		return fmt.Errorf("apply Core DNS failed; previous Xray config restored: %w", err)
	}
	cancel()
	return nil
}

func (s *Server) dnsConfig(w http.ResponseWriter, r *http.Request) {
	if s.dns == nil {
		writeError(w, 503, "DNS state directory is not configured")
		return
	}
	switch r.Method {
	case http.MethodGet:
		if !validLocalReadRequest(r) {
			writeError(w, 403, "local request required")
			return
		}
		c := s.dns.Config()
		det := s.dns.Detect()
		if c.AutoProvider {
			c.ProviderName = det["provider_name"].(string)
			c.ProviderServers = det["provider_servers"].([]string)
		}
		status := s.dns.Status()
		status["provider_name"] = c.ProviderName
		status["provider_servers"] = c.ProviderServers
		stats := s.dns.Stats()
		stats.Provider = c.ProviderName
		writeJSON(w, 200, map[string]any{"config": c, "status": status, "stats": stats})
	case http.MethodPut:
		if !validLocalJSONRequest(r) {
			writeError(w, 403, "local same-origin JSON request required")
			return
		}
		var c dnsservice.Config
		if err := decodeStrictJSON(w, r, &c, 1<<20); err != nil {
			writeError(w, 400, "invalid DNS configuration")
			return
		}
		was := s.dns.Status()["running"].(bool)
		if was {
			if err := s.dns.Stop(); err != nil {
				writeError(w, 500, "cannot stop DNS service for configuration update")
				return
			}
		}
		if err := s.dns.Save(c); err != nil {
			if was {
				_ = s.dns.Start()
			}
			writeError(w, 422, err.Error())
			return
		}
		if c.Enabled {
			if err := s.dns.Start(); err != nil {
				writeError(w, 500, "DNS settings saved but service restart failed: "+err.Error())
				return
			}
		}
		if err := s.applyCoreDNSRuntime(c.Enabled); err != nil {
			writeError(w, 500, "DNS settings saved but Core Xray update failed: "+err.Error())
			return
		}
		writeJSON(w, 200, map[string]any{"config": s.dns.Config(), "status": s.dns.Status(), "stats": s.dns.Stats()})
	default:
		methodNotAllowed(w)
	}
}
func (s *Server) dnsService(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w)
		return
	}
	if !validLocalJSONRequest(r) {
		writeError(w, 403, "local same-origin JSON request required")
		return
	}
	var req struct{ Action string }
	if err := decodeStrictJSON(w, r, &req, 4096); err != nil {
		writeError(w, 400, "invalid DNS service action")
		return
	}
	c := s.dns.Config()
	var err error
	switch req.Action {
	case "start":
		c.Enabled = true
		if err = s.dns.Save(c); err == nil {
			err = s.dns.Start()
		}
		if err == nil {
			err = s.applyCoreDNSRuntime(true)
		}
		if err != nil {
			_ = s.dns.Stop()
			c.Enabled = false
			_ = s.dns.Save(c)
		}
	case "stop":
		// First detach Xray from the listener; only then close the DNS socket.
		if err = s.applyCoreDNSRuntime(false); err == nil {
			err = s.dns.Stop()
		}
		if err == nil {
			c.Enabled = false
			err = s.dns.Save(c)
		}
	default:
		writeError(w, 400, "action must be start or stop")
		return
	}
	if err != nil {
		writeError(w, 409, err.Error())
		return
	}
	writeJSON(w, 200, map[string]any{"status": s.dns.Status(), "config": s.dns.Config()})
}
func (s *Server) dnsCheck(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w)
		return
	}
	if !validLocalJSONRequest(r) {
		writeError(w, 403, "local same-origin JSON request required")
		return
	}
	var req struct{ Domain string }
	if err := decodeStrictJSON(w, r, &req, 4096); err != nil {
		writeError(w, 400, "invalid domain check")
		return
	}
	req.Domain = strings.TrimSpace(req.Domain)
	if req.Domain == "" || len(req.Domain) > 253 || strings.ContainsAny(req.Domain, "/\\\r\n ") {
		writeError(w, 400, "invalid domain")
		return
	}
	ips, err := s.dns.Check(req.Domain)
	if err != nil {
		writeJSON(w, 200, map[string]any{"status": "Нет ответа: " + err.Error(), "addresses": []string{}})
		return
	}
	writeJSON(w, 200, map[string]any{"status": "Разрешён", "addresses": ips})
}
