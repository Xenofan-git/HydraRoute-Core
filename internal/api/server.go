package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net"
	"net/http"
	"net/url"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"hydraroute.local/core/internal/compiler"
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
	}
	s.mux.HandleFunc("/healthz", s.health)
	s.mux.HandleFunc("/v1/config", s.config)
	s.mux.HandleFunc("/v1/subscriptions/compile", s.compileSubscription)
	s.mux.HandleFunc("/v1/selections", s.selections)
	s.mux.HandleFunc("/v1/profiles/refresh", s.refreshProfiles)
	s.mux.HandleFunc("/v1/refresh/status", s.refreshStatus)
	s.mux.HandleFunc("/v1/profiles", s.profileCollection)
	s.mux.HandleFunc("/v1/routing", s.routingConfig)
	s.mux.HandleFunc("/v1/runtime", s.runtime)
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
		if err := s.routingStore.Save(cfg); err != nil {
			writeError(w, http.StatusInternalServerError, "cannot save routing config")
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 90*time.Second)
		defer cancel()
		result, err := s.refreshProfilesLocked(ctx)
		if err != nil {
			if hadPrevious {
				_ = s.routingStore.Save(previous)
			} else {
				_ = s.routingStore.Delete()
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
		if err := s.profileStore.Save(p); err != nil {
			writeError(w, http.StatusUnprocessableEntity, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"status": "saved", "profile_id": p.ID})
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
		inputs = append(inputs, service.ProfileRequest{Source: p.SourceID, Profile: service.ProfileInput{ID: p.ID, Enabled: p.Enabled, Listen: p.Listen, Port: p.Port, Strategy: p.Strategy}, Links: p.Links, Subscription: p.Subscription, SubscriptionURL: p.SubscriptionURL})
	}
	var routing *compiler.RoutingConfig
	if s.routingStore != nil {
		if storedRouting, found, loadErr := s.routingStore.Load(); loadErr != nil {
			return nil, fmt.Errorf("cannot load shared routing config: %w", loadErr)
		} else if found {
			routing = &storedRouting
		}
	}
	result, err := service.CompileSubscriptions(ctx, service.SubscriptionRequest{Profiles: inputs, Routing: routing}, s.stateDir)
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
