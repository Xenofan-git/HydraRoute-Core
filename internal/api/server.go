package api

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
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
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"hydraroute.local/core/internal/compiler"
	"hydraroute.local/core/internal/dnsservice"
	dnsblocklist "hydraroute.local/core/internal/dnsservice/blocklist"
	hrsmartdns "hydraroute.local/core/internal/dnsservice/smartdns"
	"hydraroute.local/core/internal/events"
	"hydraroute.local/core/internal/health"
	"hydraroute.local/core/internal/parser"
	"hydraroute.local/core/internal/profiles"
	"hydraroute.local/core/internal/routeconfig"
	hrruntime "hydraroute.local/core/internal/runtime"
	"hydraroute.local/core/internal/service"
	"hydraroute.local/core/internal/state"
	"hydraroute.local/core/internal/ui"
	hrwatchlist "hydraroute.local/core/internal/watchlist"
	"hydraroute.local/core/internal/xrayapi"
)

const maxRequestBytes = 8 << 20
const maxGeoDataAssetBytes = 128 << 20
const smartDNSBinaryPath = "/usr/local/libexec/hydraroute/smartdns"

type geoDataAssetStatus struct {
	Name      string `json:"name"`
	Available bool   `json:"available"`
	Size      int64  `json:"size,omitempty"`
	SHA256    string `json:"sha256,omitempty"`
}

type geoDataStatusResponse struct {
	Ready  bool                 `json:"ready"`
	Assets []geoDataAssetStatus `json:"assets"`
	Error  string               `json:"error,omitempty"`
}

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
	supervisor          *hrruntime.Supervisor
	stateDir            string
	profileStore        *profiles.Store
	routingStore        *routeconfig.Store
	dns                 *dnsservice.Manager
	dnsPolicyMu         sync.RWMutex
	smartDNSPolicyOn    bool
	smartDNSDomainSets  []hrsmartdns.DomainSet
	blocklists          *dnsblocklist.Manager
	blocklistsErr       string
	smartDNSRuntime     *hrsmartdns.Runtime
	smartDNSConfig      hrsmartdns.Config
	smartDNSConfigPath  string
	smartDNSErr         string
	opMu                sync.Mutex
	refreshMu           sync.Mutex
	refreshOpts         Options
	refreshState        RefreshStatus
	refreshWake         chan struct{}
	blocklistWake       chan struct{}
	probeMu             sync.RWMutex
	probeSaveMu         sync.Mutex
	probeCache          map[string]map[string]xrayapi.OutboundStatus
	events              *events.Store
	healthMonitor       *health.Monitor
	healthCheckMu       sync.Mutex
	healthWake          chan struct{}
	watchlistMu         sync.Mutex
	watchlist           *hrwatchlist.Server
	watchlistPath       string
	watchlistErr        string
	watchlistTargets    int
	watchlistKeys       int
	watchlistPartial    bool
	watchlistExcluded   int
	observatoryInterval int
	observatoryAddress  string
	subscriptionFetcher func(context.Context, string) (string, error)
	l7Settings          compiler.L7SniffingConfig
	mux                 *http.ServeMux
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
	s := &Server{supervisor: supervisor, stateDir: stateDir, refreshOpts: opts, refreshState: RefreshStatus{Enabled: opts.RefreshInterval > 0, IntervalSeconds: int64(opts.RefreshInterval.Seconds())}, refreshWake: make(chan struct{}, 1), blocklistWake: make(chan struct{}, 1), probeCache: make(map[string]map[string]xrayapi.OutboundStatus), observatoryInterval: 30, subscriptionFetcher: parser.FetchSubscription, l7Settings: compiler.L7SniffingConfig{HTTP: true, TLS: true, QUIC: true}, mux: http.NewServeMux()}
	s.events = events.New("")
	healthSettings := health.DefaultSettings()
	if stateDir != "" {
		s.events = events.New(filepath.Join(stateDir, "events", "events.json"))
		healthSettings = loadHealthSettings(filepath.Join(stateDir, "health", "settings.json"))
		s.loadProbeCache()
		s.loadObservatorySettings()
		s.loadL7Settings()
		s.profileStore = profiles.NewStore(filepath.Join(stateDir, "profiles"))
		s.routingStore = routeconfig.NewStore(stateDir)
		s.dns = dnsservice.New(filepath.Join(stateDir, "dns", "config.json"))
		s.smartDNSConfig = hrsmartdns.DefaultConfig()
		s.smartDNSConfigPath = filepath.Join(stateDir, "dns", "smartdns", "config.json")
		if runtime, err := hrsmartdns.NewRuntime(hrsmartdns.RuntimeConfig{
			BinaryPath: smartDNSBinaryPath,
			StateDir:   filepath.Join(stateDir, "dns", "smartdns", "runtime"),
			ListenAddr: net.JoinHostPort("127.0.0.1", strconv.Itoa(s.smartDNSConfig.ListenPort)),
		}); err != nil {
			s.smartDNSErr = err.Error()
		} else {
			s.smartDNSRuntime = runtime
		}
		if info, err := os.Lstat(s.smartDNSConfigPath); err == nil {
			if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm()&0077 != 0 {
				s.smartDNSErr = "saved SmartDNS configuration must be a private regular file"
			} else if data, readErr := os.ReadFile(s.smartDNSConfigPath); readErr != nil {
				s.smartDNSErr = "saved SmartDNS configuration could not be read"
			} else if err := json.Unmarshal(data, &s.smartDNSConfig); err != nil {
				s.smartDNSErr = "saved SmartDNS configuration is invalid"
			}
		} else if !os.IsNotExist(err) {
			s.smartDNSErr = "saved SmartDNS configuration could not be inspected"
		}
		s.setSmartDNSPolicyConfig(s.smartDNSConfig)
		if s.smartDNSRuntime != nil {
			if err := s.smartDNSRuntime.Apply(context.Background(), s.smartDNSConfig, s.smartDNSProfileRefs()); err != nil {
				s.smartDNSErr = err.Error()
			}
		}
		if manager, err := dnsblocklist.NewManager(filepath.Join(stateDir, "dns", "blocklists", "state.json"), nil); err != nil {
			s.blocklistsErr = err.Error()
		} else {
			s.blocklists = manager
		}
		s.applyDNSBlocklists()
		if data, err := os.ReadFile(filepath.Join(stateDir, "refresh-settings.json")); err == nil {
			var saved struct {
				Enabled         bool  `json:"enabled"`
				IntervalSeconds int64 `json:"interval_seconds"`
			}
			if json.Unmarshal(data, &saved) == nil && saved.IntervalSeconds >= 3600 && saved.IntervalSeconds <= int64((30*24*time.Hour).Seconds()) {
				s.refreshOpts.RefreshInterval = 0
				if saved.Enabled {
					s.refreshOpts.RefreshInterval = time.Duration(saved.IntervalSeconds) * time.Second
				}
				s.refreshState.Enabled = saved.Enabled
				s.refreshState.IntervalSeconds = saved.IntervalSeconds
			}
		} else if !os.IsNotExist(err) { /* retain constructor defaults; status reports no saved override */
		}
	}
	s.healthMonitor = health.New(healthSettings)
	s.healthWake = make(chan struct{}, 1)
	s.mux.HandleFunc("/healthz", s.health)
	s.mux.HandleFunc("/v1/health/monitor", s.healthMonitorStatus)
	s.mux.HandleFunc("/v1/health/check", s.healthMonitorCheck)
	s.mux.HandleFunc("/v1/health/settings", s.healthMonitorSettings)
	s.mux.HandleFunc("/v1/config", s.config)
	s.mux.HandleFunc("/v1/subscriptions/compile", s.compileSubscription)
	s.mux.HandleFunc("/v1/selections", s.selections)
	s.mux.HandleFunc("/v1/profiles/refresh", s.refreshProfiles)
	s.mux.HandleFunc("/v1/profiles/preview", s.profilePreview)
	s.mux.HandleFunc("/v1/profiles/test", s.profileTest)
	s.mux.HandleFunc("/v1/refresh/status", s.refreshStatus)
	s.mux.HandleFunc("/v1/observatory/status", s.observatoryStatus)
	s.mux.HandleFunc("/v1/observatory/settings", s.observatorySettings)
	s.mux.HandleFunc("/v1/profiles/ping", s.profilePingStream)
	s.mux.HandleFunc("/v1/profiles", s.profileCollection)
	s.mux.HandleFunc("/v1/interfaces", s.interfaces)
	s.mux.HandleFunc("/v1/routing", s.routingConfig)
	s.mux.HandleFunc("/v1/geodata/status", s.geoDataStatus)
	s.mux.HandleFunc("/v1/watchlist/status", s.watchlistStatus)
	s.mux.HandleFunc("/v1/events", s.eventCollection)
	s.mux.HandleFunc("/v1/events/stream", s.eventStream)
	s.mux.HandleFunc("/v1/l7/settings", s.l7SettingsHandler)
	s.mux.HandleFunc("/v1/runtime", s.runtime)
	s.mux.HandleFunc("/v1/dns", s.dnsConfig)
	s.mux.HandleFunc("/v1/dns/service", s.dnsService)
	s.mux.HandleFunc("/v1/dns/check", s.dnsCheck)
	s.mux.HandleFunc("/v1/dns/smartdns", s.smartDNSHandler)
	s.mux.HandleFunc("/v1/dns/smartdns/", s.smartDNSAction)
	s.mux.HandleFunc("/v1/dns/blocklists", s.dnsBlocklists)
	s.mux.HandleFunc("/v1/dns/blocklists/catalog", s.dnsBlocklistCatalog)
	s.mux.HandleFunc("/v1/dns/blocklists/custom", s.dnsBlocklistCustom)
	s.mux.HandleFunc("/v1/dns/blocklists/settings", s.dnsBlocklistSettings)
	s.mux.HandleFunc("/v1/dns/blocklists/", s.dnsBlocklistItem)
	s.mux.Handle("/", ui.Handler())
	if s.supervisor != nil && s.supervisor.Status().Configured {
		s.syncWatchlist()
	}
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
	defer s.closeWatchlist()
	dnsStarted := false
	if s.dns != nil && s.dns.Config().Enabled {
		if err := s.dns.Start(); err != nil {
			return fmt.Errorf("start saved DNS service: %w", err)
		}
		dnsStarted = true
	}
	listener, err := net.Listen("tcp", address)
	if err != nil {
		if dnsStarted {
			_ = s.dns.Stop()
		}
		return err
	}
	s.recordEvent("info", "api", "started", "HydraRoute Core API started")
	srv := &http.Server{Addr: address, Handler: s.mux, ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 15 * time.Second, WriteTimeout: 60 * time.Second, IdleTimeout: 60 * time.Second}
	serveErr := make(chan error, 1)
	go func() { serveErr <- srv.Serve(listener) }()
	monitorCtx, monitorCancel := context.WithCancel(ctx)
	monitorDone := make(chan struct{})
	go func() { defer close(monitorDone); s.runHealthMonitor(monitorCtx) }()
	defer func() { monitorCancel(); <-monitorDone }()
	schedulerCtx, schedulerCancel := context.WithCancel(ctx)
	schedulerDone := make(chan struct{})
	if s.refreshOpts.RefreshInterval > 0 {
		s.refreshMu.Lock()
		nextAttempt := time.Now().UTC().Add(s.refreshOpts.RefreshInterval)
		s.refreshState.NextAttempt = &nextAttempt
		s.refreshMu.Unlock()
	}
	go func() {
		defer close(schedulerDone)
		var wg sync.WaitGroup
		wg.Add(2)
		go func() { defer wg.Done(); s.runRefreshScheduler(schedulerCtx) }()
		go func() { defer wg.Done(); s.runBlocklistScheduler(schedulerCtx) }()
		wg.Wait()
	}()
	defer func() { schedulerCancel(); <-schedulerDone }()
	select {
	case err := <-serveErr:
		if s.dns != nil {
			_ = s.dns.Stop()
		}
		schedulerCancel()
		<-schedulerDone
		stopCtx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
		defer cancel()
		s.opMu.Lock()
		var smartDNSErr error
		if s.smartDNSRuntime != nil {
			smartDNSErr = s.smartDNSRuntime.Stop(stopCtx)
		}
		runtimeErr := s.supervisor.Stop(stopCtx)
		s.opMu.Unlock()
		if smartDNSErr != nil {
			return fmt.Errorf("API stopped unexpectedly (%v); stop SmartDNS: %w", err, smartDNSErr)
		}
		if runtimeErr != nil {
			return fmt.Errorf("API stopped unexpectedly (%v); stop managed Xray: %w", err, runtimeErr)
		}
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	case <-ctx.Done():
		monitorCancel()
		<-monitorDone
		s.closeWatchlist()
		if s.dns != nil {
			_ = s.dns.Stop()
		}
		schedulerCancel()
		<-schedulerDone
		stopCtx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
		defer cancel()
		s.opMu.Lock()
		var smartDNSErr error
		if s.smartDNSRuntime != nil {
			smartDNSErr = s.smartDNSRuntime.Stop(stopCtx)
		}
		runtimeErr := s.supervisor.Stop(stopCtx)
		s.opMu.Unlock()
		shutdownErr := srv.Shutdown(stopCtx)
		if smartDNSErr != nil {
			return fmt.Errorf("stop SmartDNS: %w", smartDNSErr)
		}
		if runtimeErr != nil {
			return fmt.Errorf("stop managed Xray: %w", runtimeErr)
		}
		if shutdownErr != nil {
			return fmt.Errorf("shutdown API: %w", shutdownErr)
		}
		return nil
	}
}

func (s *Server) recordEvent(level, component, eventType, message string) {
	if s.events != nil {
		_, _ = s.events.Append(level, component, eventType, message)
	}
}

func (s *Server) eventCollection(w http.ResponseWriter, r *http.Request) {
	if s.events == nil {
		writeError(w, http.StatusServiceUnavailable, "event journal is unavailable")
		return
	}
	switch r.Method {
	case http.MethodGet:
		if !validLocalReadRequest(r) {
			writeError(w, http.StatusForbidden, "local request required")
			return
		}
		limit := 100
		if raw := r.URL.Query().Get("limit"); raw != "" {
			value, err := strconv.Atoi(raw)
			if err != nil || value < 1 || value > 500 {
				writeError(w, http.StatusBadRequest, "limit must be between 1 and 500")
				return
			}
			limit = value
		}
		var after uint64
		if raw := r.URL.Query().Get("after_id"); raw != "" {
			value, err := strconv.ParseUint(raw, 10, 64)
			if err != nil {
				writeError(w, http.StatusBadRequest, "after_id must be an unsigned integer")
				return
			}
			after = value
		}
		if err := s.events.Error(); err != nil {
			writeError(w, http.StatusServiceUnavailable, "event journal is not readable")
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"events": s.events.List(limit, after), "limit": limit})
	case http.MethodDelete:
		if !validLocalJSONRequest(r) {
			writeError(w, http.StatusForbidden, "local same-origin request required")
			return
		}
		if err := s.events.Clear(); err != nil {
			writeError(w, http.StatusInternalServerError, "cannot clear event journal")
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"status": "cleared"})
	default:
		methodNotAllowed(w)
	}
}

func (s *Server) eventStream(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		methodNotAllowed(w)
		return
	}
	if !validLocalReadRequest(r) {
		writeError(w, http.StatusForbidden, "local request required")
		return
	}
	if s.events == nil || s.events.Error() != nil {
		writeError(w, http.StatusServiceUnavailable, "event journal is unavailable")
		return
	}
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeError(w, http.StatusInternalServerError, "event streaming is unavailable")
		return
	}
	var after uint64
	lastEventID := r.Header.Get("Last-Event-ID")
	if lastEventID == "" {
		lastEventID = r.URL.Query().Get("after_id")
	}
	if raw := lastEventID; raw != "" {
		value, err := strconv.ParseUint(raw, 10, 64)
		if err != nil {
			writeError(w, http.StatusBadRequest, "after_id must be an unsigned integer")
			return
		}
		after = value
	}
	ch, cancel := s.events.Subscribe()
	if ch == nil {
		writeError(w, http.StatusServiceUnavailable, "event stream subscriber limit reached")
		return
	}
	defer cancel()
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache, no-transform")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")
	if controller := http.NewResponseController(w); controller != nil {
		_ = controller.SetWriteDeadline(time.Time{})
	}
	send := func(event events.Event) bool {
		data, err := json.Marshal(event)
		if err != nil {
			return false
		}
		if _, err := fmt.Fprintf(w, "id: %d\nevent: message\ndata: %s\n\n", event.ID, data); err != nil {
			return false
		}
		flusher.Flush()
		return true
	}
	for _, event := range s.events.List(500, after) {
		if event.ID > after {
			if !send(event) {
				return
			}
			after = event.ID
		}
	}
	heartbeat := time.NewTicker(15 * time.Second)
	defer heartbeat.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case event, open := <-ch:
			if !open {
				return
			}
			if event.ID <= after {
				continue
			}
			if !send(event) {
				return
			}
			after = event.ID
		case <-heartbeat.C:
			if _, err := fmt.Fprint(w, ": keepalive\n\n"); err != nil {
				return
			}
			flusher.Flush()
		}
	}
}

type healthSettingsFile struct {
	Settings health.Settings `json:"settings"`
}

func loadHealthSettings(path string) health.Settings {
	settings := health.DefaultSettings()
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm()&0077 != 0 || info.Size() > 4096 {
		return settings
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return settings
	}
	var saved healthSettingsFile
	if json.Unmarshal(data, &saved) == nil && saved.Settings.Validate() == nil {
		return saved.Settings
	}
	return settings
}

func (s *Server) saveHealthSettings(settings health.Settings) error {
	if s.stateDir == "" {
		return fmt.Errorf("state directory is not configured")
	}
	dir := filepath.Join(s.stateDir, "health")
	if err := os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	info, err := os.Lstat(dir)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm()&0077 != 0 {
		return fmt.Errorf("health settings directory must be private")
	}
	data, err := json.Marshal(healthSettingsFile{Settings: settings})
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".settings-*.json")
	if err != nil {
		return err
	}
	name := tmp.Name()
	defer os.Remove(name)
	if err := tmp.Chmod(0600); err != nil {
		tmp.Close()
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(name, filepath.Join(dir, "settings.json"))
}

func (s *Server) healthMonitorSettings(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		if !validLocalReadRequest(r) {
			writeError(w, http.StatusForbidden, "local request required")
			return
		}
		writeJSON(w, http.StatusOK, s.healthMonitor.Settings())
	case http.MethodPut:
		if !validLocalJSONRequest(r) {
			writeError(w, http.StatusForbidden, "local same-origin JSON request required")
			return
		}
		var settings health.Settings
		if err := decodeStrictJSON(w, r, &settings, 4096); err != nil {
			writeError(w, http.StatusBadRequest, "invalid health monitor settings")
			return
		}
		if err := settings.Validate(); err != nil {
			writeError(w, http.StatusUnprocessableEntity, err.Error())
			return
		}
		if err := s.saveHealthSettings(settings); err != nil {
			writeError(w, http.StatusInternalServerError, "cannot save health monitor settings")
			return
		}
		if err := s.healthMonitor.Configure(settings); err != nil {
			writeError(w, http.StatusUnprocessableEntity, err.Error())
			return
		}
		select {
		case s.healthWake <- struct{}{}:
		default:
		}
		s.recordEvent("info", "health", "settings_updated", "Health monitor settings updated")
		writeJSON(w, http.StatusOK, settings)
	default:
		methodNotAllowed(w)
	}
}

func (s *Server) healthMonitorStatus(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		methodNotAllowed(w)
		return
	}
	if !validLocalReadRequest(r) {
		writeError(w, http.StatusForbidden, "local request required")
		return
	}
	snapshot := s.healthMonitor.Snapshot()
	if snapshot.CheckedAt.IsZero() {
		snapshot = s.runHealthCheck(r.Context())
	}
	writeJSON(w, http.StatusOK, snapshot)
}

func (s *Server) healthMonitorCheck(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w)
		return
	}
	if !validLocalJSONRequest(r) {
		writeError(w, http.StatusForbidden, "local same-origin JSON request required")
		return
	}
	snapshot := s.runHealthCheck(r.Context())
	writeJSON(w, http.StatusOK, snapshot)
}

func (s *Server) runHealthMonitor(ctx context.Context) {
	for {
		settings := s.healthMonitor.Settings()
		if settings.Enabled {
			checkCtx, cancel := context.WithTimeout(ctx, time.Duration(settings.TimeoutSeconds)*time.Second)
			s.runHealthCheck(checkCtx)
			cancel()
		}
		timer := time.NewTimer(time.Duration(settings.IntervalSeconds) * time.Second)
		select {
		case <-ctx.Done():
			if !timer.Stop() {
				<-timer.C
			}
			return
		case <-s.healthWake:
			if !timer.Stop() {
				select {
				case <-timer.C:
				default:
				}
			}
		case <-timer.C:
		}
	}
}

func (s *Server) runHealthCheck(ctx context.Context) health.Snapshot {
	s.healthCheckMu.Lock()
	defer s.healthCheckMu.Unlock()
	checks := make([]health.Check, 0, 8)
	add := func(name, state, message string) {
		checks = append(checks, health.Check{Name: name, State: state, Message: message})
	}
	add("api", "healthy", "Local API is responding")
	if s.supervisor == nil {
		add("xray", "failed", "Xray supervisor is unavailable")
	} else {
		status := s.supervisor.Status()
		switch {
		case !status.Configured:
			add("xray", "not_configured", "No validated Xray configuration")
		case !status.Running:
			add("xray", "warning", "Xray process is stopped")
		default:
			probeCtx, cancel := context.WithTimeout(ctx, time.Second)
			_, err := xrayapi.GetOutboundStatus(probeCtx, s.observatoryAPIAddress())
			cancel()
			if err != nil {
				add("xray", "failed", "Xray control API did not respond")
			} else {
				add("xray", "healthy", "Xray process and control API are responding")
			}
		}
	}
	if s.dns == nil || !s.dns.Config().Enabled {
		add("dns", "disabled", "Core DNS resolver is disabled")
	} else if running, _ := s.dns.Status()["running"].(bool); running {
		add("dns", "healthy", "Core DNS resolver is running")
	} else {
		add("dns", "failed", "Core DNS resolver is enabled but stopped")
	}
	assetDir := strings.TrimSpace(os.Getenv("XRAY_LOCATION_ASSET"))
	geoState := "healthy"
	geoMessage := "GeoIP and GeoSite assets are available"
	if assetDir == "" || !filepath.IsAbs(assetDir) {
		geoState, geoMessage = "warning", "Xray asset directory is not configured"
	} else {
		for _, name := range []string{"geoip.dat", "geosite.dat"} {
			info, err := os.Lstat(filepath.Join(assetDir, name))
			if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 || info.Size() <= 0 || info.Size() > maxGeoDataAssetBytes {
				geoState, geoMessage = "warning", "One or more GeoIP/GeoSite assets are unavailable"
				break
			}
		}
	}
	add("geodata", geoState, geoMessage)
	watchState, watchMessage := "disabled", "Watchlist is not active"
	s.watchlistMu.Lock()
	if s.watchlistErr != "" {
		watchState, watchMessage = "failed", "Watchlist could not be synchronized"
	} else if s.watchlist != nil && s.watchlistPath != "" {
		watchState, watchMessage = "healthy", "Watchlist snapshot and local socket are active"
	}
	s.watchlistMu.Unlock()
	add("watchlist", watchState, watchMessage)

	metrics := health.Metrics{}
	if s.stateDir != "" {
		var stat syscall.Statfs_t
		if err := syscall.Statfs(s.stateDir, &stat); err == nil {
			metrics.DiskFreeBytes = stat.Bavail * uint64(stat.Bsize)
			metrics.DiskTotalBytes = stat.Blocks * uint64(stat.Bsize)
			if metrics.DiskTotalBytes > 0 && (metrics.DiskFreeBytes < 64<<20 || metrics.DiskFreeBytes*100/metrics.DiskTotalBytes < 1) {
				add("disk", "failed", "Filesystem free space is critically low")
			} else if metrics.DiskTotalBytes > 0 && (metrics.DiskFreeBytes < 256<<20 || metrics.DiskFreeBytes*100/metrics.DiskTotalBytes < 5) {
				add("disk", "warning", "Filesystem free space is low")
			} else {
				add("disk", "healthy", "Filesystem free space is sufficient")
			}
		} else {
			add("disk", "warning", "Filesystem metrics are unavailable")
		}
	}
	if data, err := os.ReadFile("/proc/meminfo"); err == nil {
		var total, available uint64
		for _, line := range strings.Split(string(data), "\n") {
			fields := strings.Fields(line)
			if len(fields) < 2 {
				continue
			}
			value, parseErr := strconv.ParseUint(fields[1], 10, 64)
			if parseErr != nil {
				continue
			}
			switch strings.TrimSuffix(fields[0], ":") {
			case "MemTotal":
				total = value * 1024
			case "MemAvailable":
				available = value * 1024
			}
		}
		metrics.MemoryTotalBytes, metrics.MemoryFreeBytes = total, available
		if total > 0 && available*100/total < 5 {
			add("memory", "failed", "Available system memory is critically low")
		} else if total > 0 && available*100/total < 10 {
			add("memory", "warning", "Available system memory is low")
		} else if total > 0 {
			add("memory", "healthy", "Available system memory is sufficient")
		}
	}
	if data, err := os.ReadFile("/proc/loadavg"); err == nil {
		fields := strings.Fields(string(data))
		if len(fields) > 0 {
			metrics.Load1, _ = strconv.ParseFloat(fields[0], 64)
		}
	}
	if data, err := os.ReadFile("/proc/uptime"); err == nil {
		fields := strings.Fields(string(data))
		if len(fields) > 0 {
			metrics.UptimeSeconds, _ = strconv.ParseFloat(fields[0], 64)
		}
	}

	previous := s.healthMonitor.Snapshot()
	result := s.healthMonitor.Apply(checks, metrics, time.Now())
	previousStates := make(map[string]string, len(previous.Checks))
	for _, check := range previous.Checks {
		previousStates[check.Name] = check.State
	}
	for _, check := range result.Checks {
		if old, ok := previousStates[check.Name]; ok && old != check.State {
			level := "info"
			if check.State == "degraded" || check.State == "warning" {
				level = "warning"
			}
			s.recordEvent(level, "health", "component_state_changed", check.Name+" health state changed: "+old+" -> "+check.State)
		}
	}
	if !previous.CheckedAt.IsZero() && previous.Status != result.Status {
		level := "info"
		if result.Status == "degraded" {
			level = "error"
		} else if result.Status == "warning" {
			level = "warning"
		}
		s.recordEvent(level, "health", "overall_state_changed", "Health status changed: "+previous.Status+" -> "+result.Status)
	}
	return result
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

func (s *Server) geoDataStatus(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		methodNotAllowed(w)
		return
	}
	if !validLocalReadRequest(r) {
		writeError(w, http.StatusForbidden, "local request required")
		return
	}
	result := geoDataStatusResponse{Assets: []geoDataAssetStatus{{Name: "geoip.dat"}, {Name: "geosite.dat"}}}
	assetDir := strings.TrimSpace(os.Getenv("XRAY_LOCATION_ASSET"))
	if assetDir == "" || !filepath.IsAbs(assetDir) {
		result.Error = "Xray asset directory is not configured"
		writeJSON(w, http.StatusOK, result)
		return
	}
	dirInfo, err := os.Lstat(assetDir)
	if err != nil || !dirInfo.IsDir() || dirInfo.Mode()&os.ModeSymlink != 0 {
		result.Error = "Xray asset directory is unavailable or unsafe"
		writeJSON(w, http.StatusOK, result)
		return
	}
	result.Ready = true
	for i := range result.Assets {
		asset := &result.Assets[i]
		path := filepath.Join(assetDir, asset.Name)
		info, statErr := os.Lstat(path)
		if statErr != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 || info.Size() <= 0 || info.Size() > maxGeoDataAssetBytes {
			result.Ready = false
			continue
		}
		f, openErr := os.Open(path)
		if openErr != nil {
			result.Ready = false
			continue
		}
		openedInfo, fstatErr := f.Stat()
		if fstatErr != nil || !os.SameFile(info, openedInfo) || !openedInfo.Mode().IsRegular() {
			_ = f.Close()
			result.Ready = false
			continue
		}
		data, readErr := io.ReadAll(io.LimitReader(f, maxGeoDataAssetBytes+1))
		_ = f.Close()
		if readErr != nil || len(data) == 0 || int64(len(data)) > maxGeoDataAssetBytes {
			result.Ready = false
			continue
		}
		sum := sha256.Sum256(data)
		asset.Available = true
		asset.Size = int64(len(data))
		asset.SHA256 = hex.EncodeToString(sum[:])
	}
	if !result.Ready && result.Error == "" {
		result.Error = "One or more required Xray geodata assets are missing or invalid"
	}
	writeJSON(w, http.StatusOK, result)
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
				s.syncWatchlist()
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
		result, err := s.refreshProfilesCachedLocked(ctx)
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
	req.L7Sniffing = s.l7Settings
	req.ObservatoryProbeIntervalSeconds = s.observatoryInterval
	compiled, err := compiler.Compile(req)
	if err != nil {
		writeError(w, http.StatusUnprocessableEntity, err.Error())
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()
	if err := s.supervisor.Prepare(ctx, compiled); err != nil {
		s.recordEvent("error", "runtime", "config_rejected", "Xray configuration validation failed")
		writeError(w, http.StatusUnprocessableEntity, err.Error())
		return
	}
	s.recordEvent("info", "runtime", "config_prepared", "Xray configuration validated and prepared")
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
	req.L7Sniffing = s.l7Settings
	req.ObservatoryProbeIntervalSeconds = s.observatoryInterval
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
		s.recordEvent("error", "runtime", "config_rejected", "Subscription configuration validation failed")
		writeError(w, http.StatusUnprocessableEntity, err.Error())
		return
	}
	s.recordEvent("info", "runtime", "config_prepared", "Subscription configuration validated and prepared")
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

func eligiblePreviewNodeCount(preview service.ProfilePreview, selection *state.Profile) int {
	picked := make(map[string]bool)
	excluded := make(map[string]bool)
	if selection != nil {
		for _, key := range selection.Picked {
			b, _ := json.Marshal(key)
			picked[string(b)] = true
		}
		for _, key := range selection.Excluded {
			b, _ := json.Marshal(key)
			excluded[string(b)] = true
		}
	}
	count := 0
	for _, node := range preview.Nodes {
		if node.Filtered {
			continue
		}
		b, _ := json.Marshal(node.Key)
		key := string(b)
		if selection == nil {
			if node.Picked && !node.Excluded {
				count++
			}
			continue
		}
		if excluded[key] {
			continue
		}
		if selection.PickMode == "manual" && !picked[key] {
			continue
		}
		count++
	}
	return count
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
		fullProfiles, err := s.profileStore.ListProfiles()
		if err != nil {
			writeError(w, http.StatusInternalServerError, "cannot read profile settings")
			return
		}
		byID := make(map[string]profiles.Profile, len(fullProfiles))
		for _, profile := range fullProfiles {
			byID[profile.ID] = profile
		}
		for i := range items {
			if profile, ok := byID[items[i].ID]; ok {
				preview, previewErr := service.PreviewProfileCached(r.Context(), profile, s.stateDir)
				items[i].MuxCompatible = previewErr == nil && preview.MuxCompatible
			}
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
		s.recordEvent("info", "profiles", "profile_saved", "Profile saved: "+p.ID)
		writeJSON(w, http.StatusOK, map[string]any{"status": "saved", "profile_id": p.ID})
	case http.MethodPut:
		if !validLocalJSONRequest(r) {
			writeError(w, http.StatusForbidden, "local same-origin JSON request required")
			return
		}
		s.opMu.Lock()
		defer s.opMu.Unlock()
		var req struct {
			ID                 string              `json:"id"`
			Enabled            *bool               `json:"enabled,omitempty"`
			Port               *uint16             `json:"port,omitempty"`
			Strategy           *string             `json:"strategy,omitempty"`
			OutboundInterface  *string             `json:"outbound_interface,omitempty"`
			MuxEnabled         *bool               `json:"mux_enabled,omitempty"`
			DPIBypass          *profiles.DPIBypass `json:"dpi_bypass,omitempty"`
			FilterKeep         *string             `json:"filter_keep,omitempty"`
			FilterExclude      *string             `json:"filter_exclude,omitempty"`
			PickMode           *string             `json:"pick_mode,omitempty"`
			Picked             *[]parser.ServerKey `json:"picked,omitempty"`
			Excluded           *[]parser.ServerKey `json:"excluded,omitempty"`
			AutoUpdateEnabled  *bool               `json:"auto_update_enabled,omitempty"`
			AutoUpdateInterval *int                `json:"auto_update_interval,omitempty"`
		}
		if err := decodeStrictJSON(w, r, &req, 1<<20); err != nil {
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
		if req.MuxEnabled != nil {
			p.MuxEnabled = *req.MuxEnabled
		}
		if err := validateOutboundInterface(p.OutboundInterface); err != nil {
			writeError(w, http.StatusUnprocessableEntity, err.Error())
			return
		}
		if req.DPIBypass != nil {
			p.DPIBypass = *req.DPIBypass
		}
		if req.FilterKeep != nil {
			p.FilterKeep = *req.FilterKeep
		}
		if req.FilterExclude != nil {
			p.FilterExclude = *req.FilterExclude
		}
		if req.AutoUpdateEnabled != nil {
			p.AutoUpdateEnabled = *req.AutoUpdateEnabled
		}
		if req.AutoUpdateInterval != nil {
			p.AutoUpdateInterval = *req.AutoUpdateInterval
		}
		if err := profiles.Validate(p); err != nil {
			writeError(w, http.StatusUnprocessableEntity, err.Error())
			return
		}
		if p.Port < 1024 || (p.Strategy != "" && p.Strategy != "leastPing" && p.Strategy != "roundRobin" && p.Strategy != "random") {
			writeError(w, http.StatusUnprocessableEntity, "invalid profile port or balancing strategy")
			return
		}
		selectionChanged := req.PickMode != nil || req.Picked != nil || req.Excluded != nil
		var oldSelection state.Profile
		var newSelection state.Profile
		if selectionChanged {
			oldSelection = state.Profile{ID: p.ID, SourceID: p.SourceID, PickMode: "auto"}
			if saved, loadErr := service.LoadSelection(s.stateDir, p.ID); loadErr == nil {
				oldSelection = saved
			} else if !os.IsNotExist(loadErr) {
				writeError(w, http.StatusInternalServerError, "cannot read previous selection")
				return
			}
			newSelection = oldSelection
			if req.PickMode != nil {
				newSelection.PickMode = *req.PickMode
			}
			if req.Picked != nil {
				newSelection.Picked = *req.Picked
			}
			if req.Excluded != nil {
				newSelection.Excluded = *req.Excluded
			}
			if newSelection.PickMode == "" {
				newSelection.PickMode = "auto"
			}
			if err := state.Validate(state.State{Version: state.CurrentVersion, Profiles: []state.Profile{newSelection}}); err != nil {
				writeError(w, http.StatusUnprocessableEntity, "invalid server selection")
				return
			}
		}
		if req.Strategy != nil && *req.Strategy != previous.Strategy {
			previewCtx, previewCancel := context.WithTimeout(r.Context(), 15*time.Second)
			preview, previewErr := service.PreviewProfileCached(previewCtx, p, s.stateDir)
			previewCancel()
			if previewErr != nil {
				writeError(w, http.StatusUnprocessableEntity, "refresh the profile before changing balancing strategy")
				return
			}
			var selectionOverride *state.Profile
			if selectionChanged {
				selectionOverride = &newSelection
			}
			if eligiblePreviewNodeCount(preview, selectionOverride) < 2 {
				writeError(w, http.StatusUnprocessableEntity, "balancing strategy can be changed only with at least two servers in the pool")
				return
			}
		}
		fallbackCtx, fallbackCancel := context.WithTimeout(r.Context(), 6*time.Second)
		previousFallback := s.profileFallbackSnapshot(fallbackCtx, previous)
		fallbackCancel()
		if err := s.profileStore.Save(p); err != nil {
			writeError(w, http.StatusInternalServerError, "cannot save profile settings")
			return
		}
		if selectionChanged {
			if err := service.SaveSelection(s.stateDir, newSelection); err != nil {
				_ = s.profileStore.Save(previous)
				writeError(w, http.StatusUnprocessableEntity, "cannot save server selection")
				return
			}
		}
		ctx, cancel := context.WithTimeout(r.Context(), 90*time.Second)
		defer cancel()
		if s.supervisor.Status().Configured {
			if _, err := s.refreshProfilesModeWithFallbacks(ctx, false, "", map[string]service.ProfileFallback{p.ID: previousFallback}); err != nil {
				_ = s.profileStore.Save(previous)
				if selectionChanged {
					_ = service.SaveSelection(s.stateDir, oldSelection)
				}
				writeError(w, http.StatusUnprocessableEntity, "profile update was rolled back: "+err.Error())
				return
			}
		}
		s.recordEvent("info", "profiles", "profile_updated", "Profile updated: "+p.ID)
		select {
		case s.refreshWake <- struct{}{}:
		default:
		}
		writeJSON(w, http.StatusOK, map[string]any{"status": "updated", "profile_id": p.ID, "enabled": p.Enabled, "port": p.Port, "strategy": p.Strategy, "outbound_interface": p.OutboundInterface, "mux_enabled": p.MuxEnabled, "dpi_bypass": p.DPIBypass, "runtime": s.supervisor.Status()})
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
		previous, err := s.profileStore.Get(req.ID)
		if err != nil {
			writeError(w, http.StatusNotFound, "profile not found")
			return
		}
		oldSelection, selectionErr := service.LoadSelection(s.stateDir, req.ID)
		hadSelection := selectionErr == nil
		if selectionErr != nil && !os.IsNotExist(selectionErr) {
			writeError(w, http.StatusInternalServerError, "cannot read previous server selection")
			return
		}
		var previousConfig []byte
		if s.supervisor != nil {
			previousConfig, err = s.supervisor.SnapshotConfig()
			if err != nil {
				writeError(w, http.StatusInternalServerError, "cannot snapshot current Xray configuration")
				return
			}
		}
		if err := s.profileStore.Delete(req.ID); err != nil {
			writeError(w, http.StatusUnprocessableEntity, err.Error())
			return
		}
		if err := service.DeleteSelection(s.stateDir, req.ID); err != nil {
			_ = s.profileStore.Save(previous)
			writeError(w, http.StatusUnprocessableEntity, err.Error())
			return
		}
		rollbackDelete := func() {
			_ = s.profileStore.Save(previous)
			if hadSelection {
				_ = service.SaveSelection(s.stateDir, oldSelection)
			}
			if s.supervisor != nil && len(previousConfig) > 0 {
				_ = s.supervisor.Prepare(context.Background(), previousConfig)
			}
		}
		if s.supervisor != nil && s.supervisor.Status().Configured {
			remaining, listErr := s.profileStore.ListProfiles()
			if listErr == nil {
				if len(remaining) == 0 {
					emptyConfig, compileErr := compiler.Compile(compiler.CompileRequest{})
					if compileErr != nil {
						listErr = compileErr
					} else {
						prepareCtx, prepareCancel := context.WithTimeout(r.Context(), 20*time.Second)
						listErr = s.supervisor.Prepare(prepareCtx, emptyConfig)
						prepareCancel()
					}
				} else {
					refreshCtx, refreshCancel := context.WithTimeout(r.Context(), 90*time.Second)
					_, listErr = s.refreshProfilesCachedLocked(refreshCtx)
					refreshCancel()
				}
			}
			if listErr != nil {
				rollbackDelete()
				writeError(w, http.StatusUnprocessableEntity, "profile deletion could not rebuild the remaining Xray configuration: "+listErr.Error())
				return
			}
		}
		if err := s.deleteProbeCache(req.ID); err != nil {
			rollbackDelete()
			writeError(w, http.StatusInternalServerError, "profile deleted but Observatory cache cleanup failed; previous profile/configuration restored")
			return
		}
		s.syncWatchlist()
		s.recordEvent("warning", "profiles", "profile_deleted", "Profile deleted: "+req.ID)
		writeJSON(w, http.StatusOK, map[string]any{"status": "deleted", "profile_id": req.ID})
	default:
		methodNotAllowed(w)
	}
}

type probeCacheFile struct {
	Version  int                                          `json:"version"`
	Profiles map[string]map[string]xrayapi.OutboundStatus `json:"profiles"`
}

func (s *Server) loadProbeCache() {
	if s.stateDir == "" {
		return
	}
	path := filepath.Join(s.stateDir, "observatory", "status.json")
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 {
		return
	}
	data, err := os.ReadFile(path)
	if err != nil || len(data) > 4<<20 {
		return
	}
	var saved probeCacheFile
	if json.Unmarshal(data, &saved) != nil || saved.Version != 1 || saved.Profiles == nil {
		return
	}
	for profileID, entries := range saved.Profiles {
		if !profileIDPatternForCache(profileID) {
			continue
		}
		clean := make(map[string]xrayapi.OutboundStatus)
		for key, status := range entries {
			if len(key) > 4096 || status.Tag == "" || status.LastTryTime <= 0 {
				continue
			}
			clean[key] = status
		}
		if len(clean) > 0 {
			s.probeCache[profileID] = clean
		}
	}
}

func profileIDPatternForCache(id string) bool {
	if id == "" || len(id) > 32 {
		return false
	}
	for _, r := range id {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '_' || r == '-') {
			return false
		}
	}
	return true
}

func (s *Server) saveProbeCache() error {
	s.probeSaveMu.Lock()
	defer s.probeSaveMu.Unlock()
	return s.saveProbeCacheLocked()
}

func (s *Server) deleteProbeCache(profileID string) error {
	s.probeSaveMu.Lock()
	defer s.probeSaveMu.Unlock()
	s.probeMu.Lock()
	previous, existed := s.probeCache[profileID]
	if existed {
		copied := make(map[string]xrayapi.OutboundStatus, len(previous))
		for key, status := range previous {
			copied[key] = status
		}
		previous = copied
	}
	delete(s.probeCache, profileID)
	s.probeMu.Unlock()
	if err := s.saveProbeCacheLocked(); err != nil {
		if existed {
			s.probeMu.Lock()
			s.probeCache[profileID] = previous
			s.probeMu.Unlock()
		}
		return err
	}
	return nil
}

func (s *Server) saveProbeCacheLocked() error {
	if s.stateDir == "" {
		return nil
	}
	type cacheEntry struct {
		profile, key string
		at           int64
	}
	s.probeMu.Lock()
	entries := make([]cacheEntry, 0)
	for profile, values := range s.probeCache {
		for key, status := range values {
			entries = append(entries, cacheEntry{profile, key, status.LastTryTime})
		}
	}
	if len(entries) > 4096 {
		sort.Slice(entries, func(i, j int) bool { return entries[i].at > entries[j].at })
		keep := make(map[string]map[string]bool)
		for _, entry := range entries[:4096] {
			if keep[entry.profile] == nil {
				keep[entry.profile] = make(map[string]bool)
			}
			keep[entry.profile][entry.key] = true
		}
		for profile, values := range s.probeCache {
			for key := range values {
				if !keep[profile][key] {
					delete(values, key)
				}
			}
			if len(values) == 0 {
				delete(s.probeCache, profile)
			}
		}
	}
	profilesCopy := make(map[string]map[string]xrayapi.OutboundStatus, len(s.probeCache))
	for id, values := range s.probeCache {
		copied := make(map[string]xrayapi.OutboundStatus, len(values))
		for key, status := range values {
			copied[key] = status
		}
		profilesCopy[id] = copied
	}
	s.probeMu.Unlock()
	dir := filepath.Join(s.stateDir, "observatory")
	if err := os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	if err := os.Chmod(dir, 0700); err != nil {
		return err
	}
	data, err := json.Marshal(probeCacheFile{Version: 1, Profiles: profilesCopy})
	if err != nil {
		return err
	}
	if len(data) > 4<<20 {
		return fmt.Errorf("Observatory cache exceeds safe size limit")
	}
	tmp, err := os.CreateTemp(dir, ".status-*.json")
	if err != nil {
		return err
	}
	name := tmp.Name()
	defer os.Remove(name)
	if err := tmp.Chmod(0600); err != nil {
		tmp.Close()
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(name, filepath.Join(dir, "status.json"))
}

type observatoryStatusResponse struct {
	ProfileID             string                            `json:"profile_id"`
	Available             bool                              `json:"available"`
	Refreshed             bool                              `json:"refreshed"`
	BalancerCandidateKey  string                            `json:"balancer_candidate_key,omitempty"`
	BalancerCandidateName string                            `json:"balancer_candidate_name,omitempty"`
	Statuses              map[string]xrayapi.OutboundStatus `json:"statuses"`
	Error                 string                            `json:"error,omitempty"`
}

func (s *Server) cachedProbeStatuses(profileID string, nodes []service.PreviewNode) map[string]xrayapi.OutboundStatus {
	result := make(map[string]xrayapi.OutboundStatus)
	s.probeMu.RLock()
	defer s.probeMu.RUnlock()
	for _, node := range nodes {
		keyBytes, _ := json.Marshal(node.Key)
		key := string(keyBytes)
		if status, ok := s.probeCache[profileID][key]; ok && status.Tag == compiler.OutboundTag(profileID, key) {
			status.Stale = true
			result[key] = status
		}
	}
	return result
}

func sameObservatoryStatus(a, b xrayapi.OutboundStatus) bool {
	return a.Tag == b.Tag && a.Alive == b.Alive && a.DelayMS == b.DelayMS &&
		a.LastError == b.LastError && a.LastTryTime == b.LastTryTime &&
		a.LastSeenTime == b.LastSeenTime && a.HealthAverage == b.HealthAverage &&
		a.HealthFail == b.HealthFail
}

func eligibleObservatoryNodes(preview service.ProfilePreview) []service.PreviewNode {
	nodes := make([]service.PreviewNode, 0, len(preview.Nodes))
	for _, node := range preview.Nodes {
		if node.Filtered || node.Excluded || !node.Picked {
			continue
		}
		nodes = append(nodes, node)
	}
	return nodes
}

// resolveBalancerCandidate maps Xray's last principle-target candidate back
// to the stable node identity used by this profile. Unknown/stale tags are
// deliberately ignored; a strategy candidate is not proof of an active socket.
func resolveBalancerCandidate(profileID string, nodes []service.PreviewNode, tags []string) (string, string) {
	if len(tags) == 0 {
		return "", ""
	}
	candidateTag := tags[len(tags)-1]
	for _, node := range nodes {
		keyBytes, _ := json.Marshal(node.Key)
		key := string(keyBytes)
		if compiler.OutboundTag(profileID, key) == candidateTag {
			return key, node.Name
		}
	}
	return "", ""
}

func (s *Server) observatoryAPIAddress() string {
	if address := strings.TrimSpace(s.observatoryAddress); address != "" {
		return address
	}
	return xrayapi.DefaultAddress
}

func (s *Server) collectObservatoryStatus(ctx context.Context, profileID string, since int64) (observatoryStatusResponse, error) {
	if s.profileStore == nil {
		return observatoryStatusResponse{}, fmt.Errorf("profile store is not configured")
	}
	profile, err := s.profileStore.Get(profileID)
	if err != nil {
		return observatoryStatusResponse{}, fmt.Errorf("profile not found")
	}
	preview, err := service.PreviewProfileCached(ctx, profile, s.stateDir)
	if err != nil {
		return observatoryStatusResponse{}, err
	}
	eligibleNodes := eligibleObservatoryNodes(preview)
	if !profile.Enabled {
		return observatoryStatusResponse{ProfileID: profileID, Available: false, Statuses: s.cachedProbeStatuses(profileID, eligibleNodes), Error: "Profile is disabled; showing last known measurements"}, nil
	}
	if !s.supervisor.Status().Running {
		return observatoryStatusResponse{ProfileID: profileID, Available: false, Statuses: s.cachedProbeStatuses(profileID, eligibleNodes), Error: "Xray is stopped; showing last known measurements"}, nil
	}
	items, err := xrayapi.GetOutboundStatus(ctx, s.observatoryAPIAddress())
	if err != nil {
		return observatoryStatusResponse{ProfileID: profileID, Available: false, Statuses: s.cachedProbeStatuses(profileID, eligibleNodes), Error: "Xray Observatory is unavailable; showing last known measurements"}, nil
	}
	byTag := make(map[string]xrayapi.OutboundStatus, len(items))
	for _, item := range items {
		byTag[item.Tag] = item
	}
	result := observatoryStatusResponse{ProfileID: profileID, Available: true, Refreshed: true, Statuses: make(map[string]xrayapi.OutboundStatus)}
	// RoutingService exposes the balancer's principle-target candidates. Keep
	// Observatory measurements available even if this optional API is absent.
	if tags, candidateErr := xrayapi.GetBalancerPrincipleTargetTags(ctx, s.observatoryAPIAddress(), "hr_bal_"+profileID); candidateErr == nil {
		result.BalancerCandidateKey, result.BalancerCandidateName = resolveBalancerCandidate(profileID, eligibleNodes, tags)
	}
	s.probeMu.Lock()
	cache := s.probeCache[profileID]
	if cache == nil {
		cache = make(map[string]xrayapi.OutboundStatus)
		s.probeCache[profileID] = cache
	}
	cacheChanged := false
	for _, node := range eligibleNodes {
		keyBytes, _ := json.Marshal(node.Key)
		key := string(keyBytes)
		tag := compiler.OutboundTag(profileID, key)
		item, ok := byTag[tag]
		if ok && item.LastTryTime > 0 {
			if old, exists := cache[key]; !exists || item.LastTryTime > old.LastTryTime ||
				(item.LastTryTime == old.LastTryTime && !sameObservatoryStatus(item, old)) {
				cache[key] = item
				cacheChanged = true
			}
			result.Statuses[key] = cache[key]
			if since > 0 && !isFreshObservatoryProbe(cache[key].LastTryTime, since) {
				result.Refreshed = false
			}
			continue
		}
		if cached, exists := cache[key]; exists {
			if cached.Tag == tag {
				cached.Stale = true
				result.Statuses[key] = cached
				if since > 0 && !isFreshObservatoryProbe(cached.LastTryTime, since) {
					result.Refreshed = false
				}
				continue
			}
			// Profile IDs can be reused with a different source/configuration. A
			// cache entry from the old outbound tag must never leak into the new one.
			delete(cache, key)
			cacheChanged = true
		}
		if since > 0 {
			result.Refreshed = false
		}
	}
	s.probeMu.Unlock()
	if cacheChanged {
		_ = s.saveProbeCache()
	}
	return result, nil
}

func (s *Server) observatoryStatus(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		methodNotAllowed(w)
		return
	}
	if !validLocalReadRequest(r) {
		writeError(w, http.StatusForbidden, "local request required")
		return
	}
	profileID := r.URL.Query().Get("profile_id")
	ctx, cancel := context.WithTimeout(r.Context(), 8*time.Second)
	defer cancel()
	result, err := s.collectObservatoryStatus(ctx, profileID, 0)
	if err != nil {
		writeError(w, http.StatusUnprocessableEntity, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, result)
}

type pingStreamEvent struct {
	Links []string       `json:"links,omitempty"`
	Link  string         `json:"link,omitempty"`
	Echo  map[string]any `json:"echo"`
	Ping  map[string]any `json:"ping"`
	Done  bool           `json:"done,omitempty"`
	Error string         `json:"error,omitempty"`
}

var icmpTimePattern = regexp.MustCompile(`time[=<]([0-9]+(?:\.[0-9]+)?)\s*ms`)

func publicProbeAddress(ctx context.Context, host string) (string, bool) {
	if ip := net.ParseIP(host); ip != nil {
		return ip.String(), parser.IsPublicDialIP(ip)
	}
	ips, err := net.DefaultResolver.LookupIPAddr(ctx, host)
	if err != nil || len(ips) == 0 {
		return "", false
	}
	for _, item := range ips {
		if !parser.IsPublicDialIP(item.IP) {
			return "", false
		}
	}
	return ips[0].IP.String(), true
}

func isFreshObservatoryProbe(lastTryTime, startedAt int64) bool {
	// Xray exposes LastTryTime as Unix seconds, so same-second updates cannot
	// be distinguished from the pre-request value. Require a strictly newer
	// timestamp rather than ever presenting an old measurement as fresh.
	return lastTryTime > startedAt
}

func probeICMPEcho(ctx context.Context, host string) map[string]any {
	binary, err := exec.LookPath("ping")
	if err != nil {
		return map[string]any{}
	}
	checkCtx, cancel := context.WithTimeout(ctx, 2500*time.Millisecond)
	defer cancel()
	target, ok := publicProbeAddress(checkCtx, host)
	if !ok {
		return map[string]any{}
	}
	out, err := exec.CommandContext(checkCtx, binary, "-n", "-c", "1", "-W", "1", target).CombinedOutput()
	if err != nil {
		return map[string]any{}
	}
	match := icmpTimePattern.FindSubmatch(out)
	if len(match) < 2 {
		return map[string]any{}
	}
	ms, err := strconv.ParseFloat(string(match[1]), 64)
	if err != nil {
		return map[string]any{}
	}
	return map[string]any{"delay": int64(ms + 0.5)}
}

// profilePingStream mirrors HRweb's proxy.pingServers event contract: ICMP
// echo (host-to-server, outside Xray) and proxy delay (Xray Observatory) are
// separate events and separate measurements. Observatory has no force-probe
// RPC, so the stream waits for the next real configured observation cycle.
func (s *Server) observatorySubjectCount(ctx context.Context, currentProfileID string, currentNodes []service.PreviewNode) (int, error) {
	profiles, err := s.profileStore.ListProfiles()
	if err != nil {
		return 0, err
	}
	total := 0
	for _, profile := range profiles {
		if !profile.Enabled {
			continue
		}
		if profile.ID == currentProfileID {
			total += len(currentNodes)
			continue
		}
		preview, previewErr := service.PreviewProfileCached(ctx, profile, s.stateDir)
		if previewErr != nil {
			continue
		}
		total += len(eligibleObservatoryNodes(preview))
	}
	if total < 1 {
		total = 1
	}
	return total, nil
}

func (s *Server) profilePingStream(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		methodNotAllowed(w)
		return
	}
	if !validLocalReadRequest(r) {
		writeError(w, http.StatusForbidden, "local request required")
		return
	}
	if s.profileStore == nil {
		writeError(w, http.StatusServiceUnavailable, "profile store is not configured")
		return
	}
	profileID := r.URL.Query().Get("profile_id")
	profile, err := s.profileStore.Get(profileID)
	if err != nil {
		writeError(w, http.StatusNotFound, "profile not found")
		return
	}
	ctx := r.Context()
	preview, err := service.PreviewProfileCached(ctx, profile, s.stateDir)
	if err != nil {
		writeError(w, http.StatusUnprocessableEntity, err.Error())
		return
	}
	eligibleNodes := eligibleObservatoryNodes(preview)
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeError(w, http.StatusInternalServerError, "streaming is unavailable")
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache, no-transform")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")
	if controller := http.NewResponseController(w); controller != nil {
		_ = controller.SetWriteDeadline(time.Time{})
	}
	cacheChanged := false
	defer func() {
		if cacheChanged {
			_ = s.saveProbeCache()
		}
	}()
	send := func(event pingStreamEvent) bool {
		data, e := json.Marshal(event)
		if e != nil {
			return false
		}
		if _, e = fmt.Fprintf(w, "data: %s\n\n", data); e != nil {
			return false
		}
		flusher.Flush()
		return true
	}
	// Start the freshness window before ICMP work. On large subscriptions the
	// bounded ICMP phase can take several seconds; starting afterward would
	// discard perfectly fresh Xray probes completed while ICMP was running.
	started := time.Now().Unix()
	keys := make([]string, 0, len(eligibleNodes))
	tags := make(map[string]string, len(eligibleNodes))
	for _, node := range eligibleNodes {
		keyBytes, _ := json.Marshal(node.Key)
		key := string(keyBytes)
		keys = append(keys, key)
		tags[key] = compiler.OutboundTag(profileID, key)
	}
	if !send(pingStreamEvent{Links: keys}) {
		return
	}
	if len(keys) == 0 {
		send(pingStreamEvent{Done: true})
		return
	}
	// ICMP echo is independent of Xray and may be unavailable without ping/CAP_NET_RAW.
	// Bound concurrent DNS lookups and ping processes: large subscriptions must not
	// turn one UI action into an unbounded process burst.
	type echoResult struct {
		key  string
		echo map[string]any
	}
	const maxConcurrentICMP = 8
	icmpCtx, cancelICMP := context.WithTimeout(ctx, 20*time.Second)
	defer cancelICMP()
	echoSent := make(map[string]bool, len(eligibleNodes))
	for start := 0; start < len(eligibleNodes); start += maxConcurrentICMP {
		if icmpCtx.Err() != nil {
			break
		}
		end := start + maxConcurrentICMP
		if end > len(eligibleNodes) {
			end = len(eligibleNodes)
		}
		echoes := make(chan echoResult, end-start)
		var wg sync.WaitGroup
		for _, node := range eligibleNodes[start:end] {
			keyBytes, _ := json.Marshal(node.Key)
			key, host := string(keyBytes), node.Host
			wg.Add(1)
			go func() {
				defer wg.Done()
				echoes <- echoResult{key, probeICMPEcho(icmpCtx, host)}
			}()
		}
		wg.Wait()
		close(echoes)
		for result := range echoes {
			if ctx.Err() != nil {
				return
			}
			echoSent[result.key] = true
			if !send(pingStreamEvent{Link: result.key, Echo: result.echo}) {
				return
			}
		}
	}
	cancelICMP()
	if ctx.Err() != nil {
		return
	}
	for _, node := range eligibleNodes {
		keyBytes, _ := json.Marshal(node.Key)
		key := string(keyBytes)
		if !echoSent[key] && !send(pingStreamEvent{Link: key, Echo: map[string]any{}}) {
			return
		}
	}
	if !profile.Enabled {
		send(pingStreamEvent{Done: true, Error: "Профиль выключен: прокси-задержка недоступна"})
		return
	}
	if !s.supervisor.Status().Running {
		send(pingStreamEvent{Done: true, Error: "Xray не запущен: прокси-задержка недоступна"})
		return
	}
	// Xray has no force-probe RPC. Its interval is distributed across all
	// subjects, so a full configured cycle may take up to the panel interval.
	// Keep the stream alive and let the caller cancel it if the wait is unwanted.
	s.opMu.Lock()
	cycleSeconds := s.observatoryInterval
	s.opMu.Unlock()
	if cycleSeconds < 5 || cycleSeconds > 3600 {
		cycleSeconds = 30
	}
	subjectCount, countErr := s.observatorySubjectCount(ctx, profileID, eligibleNodes)
	if countErr != nil {
		send(pingStreamEvent{Done: true, Error: "Не удалось определить размер пула Observatory"})
		return
	}
	probeIntervalMS := compiler.ObservatoryProbeIntervalMS(cycleSeconds, subjectCount)
	cycleDuration := time.Duration(probeIntervalMS*subjectCount) * time.Millisecond
	deadline := time.Now().Add(cycleDuration + 10*time.Second)
	sent := map[string]bool{}
	poll := time.NewTicker(500 * time.Millisecond)
	heartbeat := time.NewTicker(10 * time.Second)
	defer poll.Stop()
	defer heartbeat.Stop()
	for time.Now().Before(deadline) && len(sent) < len(tags) {
		select {
		case <-ctx.Done():
			return
		case <-heartbeat.C:
			if _, e := fmt.Fprint(w, ": keepalive\n\n"); e != nil {
				return
			}
			flusher.Flush()
		case <-poll.C:
		}
		probeCtx, cancel := context.WithTimeout(ctx, 4*time.Second)
		items, probeErr := xrayapi.GetOutboundStatus(probeCtx, s.observatoryAPIAddress())
		cancel()
		if probeErr != nil {
			continue
		}
		byTag := make(map[string]xrayapi.OutboundStatus, len(items))
		for _, item := range items {
			byTag[item.Tag] = item
		}
		pendingEvents := make([]pingStreamEvent, 0)
		for key, tag := range tags {
			if sent[key] {
				continue
			}
			item, found := byTag[tag]
			if !found || !isFreshObservatoryProbe(item.LastTryTime, started) {
				continue
			}
			sent[key] = true
			pendingEvents = append(pendingEvents, pingStreamEvent{Link: key, Ping: map[string]any{"alive": item.Alive, "delay": item.DelayMS, "at": item.LastTryTime}})
			cachedStatus := item
			cachedStatus.Stale = false
			s.probeMu.Lock()
			if s.probeCache[profileID] == nil {
				s.probeCache[profileID] = make(map[string]xrayapi.OutboundStatus)
			}
			s.probeCache[profileID][key] = cachedStatus
			s.probeMu.Unlock()
			cacheChanged = true
		}
		for _, event := range pendingEvents {
			if !send(event) {
				return
			}
		}
	}
	if len(sent) < len(tags) {
		send(pingStreamEvent{Done: true, Error: "Не все узлы получили новый результат Observatory за отведённое время; показаны только свежие измерения"})
		return
	}
	send(pingStreamEvent{Done: true})
}

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
	var req struct {
		ProfileID string `json:"profile_id"`
	}
	if err := decodeStrictJSON(w, r, &req, 4096); err != nil {
		writeError(w, http.StatusBadRequest, "invalid refresh request")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 90*time.Second)
	defer cancel()
	var result map[string]any
	var err error
	if req.ProfileID != "" {
		result, err = s.refreshProfileLocked(ctx, req.ProfileID)
	} else {
		result, err = s.refreshProfilesLocked(ctx)
	}
	if err != nil {
		s.recordEvent("error", "subscriptions", "refresh_failed", "Subscription refresh failed")
		writeError(w, http.StatusUnprocessableEntity, err.Error())
		return
	}
	s.recordEvent("info", "subscriptions", "refreshed", "Subscription profiles refreshed and configuration prepared")
	writeJSON(w, http.StatusOK, result)
}

func (s *Server) refreshProfilesLocked(ctx context.Context) (map[string]any, error) {
	return s.refreshProfilesMode(ctx, true, "")
}
func (s *Server) refreshProfilesCachedLocked(ctx context.Context) (map[string]any, error) {
	return s.refreshProfilesMode(ctx, false, "")
}
func (s *Server) refreshProfilesScheduledLocked(ctx context.Context) (map[string]any, error) {
	return s.refreshProfilesMode(ctx, false, "")
}
func (s *Server) refreshProfileLocked(ctx context.Context, profileID string) (map[string]any, error) {
	return s.refreshProfilesMode(ctx, false, profileID)
}

func buildProfileFallbackSnapshot(profileID string, nodes []service.PreviewNode, cached map[string]xrayapi.OutboundStatus, live []xrayapi.OutboundStatus, candidateTags []string) service.ProfileFallback {
	fallback := service.ProfileFallback{Statuses: make(map[string]service.FallbackStatus), ReferenceURIs: make(map[string]string)}
	statusValue := func(status xrayapi.OutboundStatus) service.FallbackStatus {
		return service.FallbackStatus{Alive: status.Alive, DelayMS: status.DelayMS, LastTryTime: status.LastTryTime, LastSeenTime: status.LastSeenTime}
	}
	liveByTag := make(map[string]xrayapi.OutboundStatus, len(live))
	for _, status := range live {
		liveByTag[status.Tag] = status
	}
	for _, node := range nodes {
		keyBytes, _ := json.Marshal(node.Key)
		key := string(keyBytes)
		tag := compiler.OutboundTag(profileID, key)
		fallback.ReferenceURIs[key] = node.ReferenceURI
		if status, ok := cached[key]; ok && status.Tag == tag {
			fallback.Statuses[key] = statusValue(status)
		}
		if status, ok := liveByTag[tag]; ok && status.LastTryTime > 0 {
			fallback.Statuses[key] = statusValue(status)
		}
	}
	// HRweb takes the last repeated principle_target tag. Resolve it against
	// the complete source preview, including nodes no longer selected, so its
	// URI can still guide the next pool compilation.
	if len(candidateTags) > 0 {
		candidateTag := candidateTags[len(candidateTags)-1]
		for _, node := range nodes {
			keyBytes, _ := json.Marshal(node.Key)
			key := string(keyBytes)
			if compiler.OutboundTag(profileID, key) == candidateTag {
				fallback.CandidateKey = key
				fallback.ReferenceURI = node.ReferenceURI
				break
			}
		}
	}
	return fallback
}

func (s *Server) profileFallbackSnapshot(ctx context.Context, profile profiles.Profile) service.ProfileFallback {
	preview, err := service.PreviewProfileCached(ctx, profile, s.stateDir)
	if err != nil {
		return service.ProfileFallback{}
	}
	cached := make(map[string]xrayapi.OutboundStatus)
	s.probeMu.RLock()
	for key, status := range s.probeCache[profile.ID] {
		cached[key] = status
	}
	s.probeMu.RUnlock()
	if !s.supervisor.Status().Running {
		return buildProfileFallbackSnapshot(profile.ID, preview.Nodes, cached, nil, nil)
	}
	probeCtx, cancel := context.WithTimeout(ctx, 4*time.Second)
	live, probeErr := xrayapi.GetOutboundStatus(probeCtx, s.observatoryAPIAddress())
	cancel()
	if probeErr != nil {
		live = nil
	}
	tagCtx, tagCancel := context.WithTimeout(ctx, 3*time.Second)
	candidateTags, tagErr := xrayapi.GetBalancerPrincipleTargetTags(tagCtx, s.observatoryAPIAddress(), "hr_bal_"+profile.ID)
	tagCancel()
	if tagErr != nil {
		candidateTags = nil
	}
	return buildProfileFallbackSnapshot(profile.ID, preview.Nodes, cached, live, candidateTags)
}

func (s *Server) refreshProfilesMode(ctx context.Context, forceFetch bool, targetProfile string) (map[string]any, error) {
	return s.refreshProfilesModeWithFallbacks(ctx, forceFetch, targetProfile, nil)
}

func (s *Server) refreshProfilesModeWithFallbacks(ctx context.Context, forceFetch bool, targetProfile string, suppliedFallbacks map[string]service.ProfileFallback) (map[string]any, error) {
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
	fallbacks := make(map[string]service.ProfileFallback, len(stored))
	for _, p := range stored {
		if supplied, ok := suppliedFallbacks[p.ID]; ok {
			fallbacks[p.ID] = supplied
			continue
		}
		fallbackCtx, fallbackCancel := context.WithTimeout(ctx, 6*time.Second)
		fallbacks[p.ID] = s.profileFallbackSnapshot(fallbackCtx, p)
		fallbackCancel()
	}
	inputs := make([]service.ProfileRequest, 0, len(stored))
	nowUnix := time.Now().Unix()
	for _, p := range stored {
		if err := validateOutboundInterface(p.OutboundInterface); err != nil {
			return nil, fmt.Errorf("profile %s: %w", p.ID, err)
		}
		subscription := p.Subscription
		if p.SubscriptionURL != "" {
			due := forceFetch || p.ID == targetProfile || p.CachedSubscription == "" || (p.AutoUpdateEnabled && (p.LastSubscriptionUpdate == 0 || nowUnix-p.LastSubscriptionUpdate >= int64(p.AutoUpdateInterval*60)))
			if due {
				fetcher := s.subscriptionFetcher
				if fetcher == nil {
					fetcher = parser.FetchSubscription
				}
				body, fetchErr := fetcher(ctx, p.SubscriptionURL)
				if fetchErr != nil {
					return nil, fmt.Errorf("profile %s subscription refresh failed: %w", p.ID, fetchErr)
				}
				p.CachedSubscription, p.LastSubscriptionUpdate = body, nowUnix
				if saveErr := s.profileStore.Save(p); saveErr != nil {
					return nil, fmt.Errorf("profile %s subscription cache could not be saved", p.ID)
				}
			}
			subscription = p.CachedSubscription
		}
		inputs = append(inputs, service.ProfileRequest{Source: p.SourceID, Profile: service.ProfileInput{ID: p.ID, Enabled: p.Enabled, Listen: p.Listen, Port: p.Port, Strategy: p.Strategy, OutboundInterface: p.OutboundInterface, MuxEnabled: p.MuxEnabled, DPIBypass: compiler.DPIBypass{NoiseTraffic: p.DPIBypass.NoiseTraffic, TLSFragmentation: p.DPIBypass.TLSFragmentation, RandomTLSFingerprint: p.DPIBypass.RandomTLSFingerprint}, FilterKeep: p.FilterKeep, FilterExclude: p.FilterExclude, AutoUpdateEnabled: p.AutoUpdateEnabled, AutoUpdateInterval: p.AutoUpdateInterval, Fallback: fallbacks[p.ID]}, Links: p.Links, Subscription: subscription, SubscriptionURL: ""})
	}
	var routing *compiler.RoutingConfig
	if s.routingStore != nil {
		if storedRouting, found, loadErr := s.routingStore.Load(); loadErr != nil {
			return nil, fmt.Errorf("cannot load shared routing config: %w", loadErr)
		} else if found {
			routing = &storedRouting
		}
	}
	result, err := service.CompileSubscriptions(ctx, service.SubscriptionRequest{Profiles: inputs, Routing: routing, DNS: s.coreDNSConfig(), L7Sniffing: s.l7Settings, ObservatoryProbeIntervalSeconds: s.observatoryInterval}, s.stateDir)
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
	s.syncWatchlist()
	s.recordEvent("info", "subscriptions", "configuration_refreshed", "Subscription configuration refreshed")
	return map[string]any{"status": "prepared", "profiles": result.ProfileCount, "nodes": result.NodeCount, "bytes": len(result.Config), "runtime": s.supervisor.Status()}, nil
}

func (s *Server) refreshStatus(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		if !validLocalReadRequest(r) {
			writeError(w, http.StatusForbidden, "local request required")
			return
		}
		s.refreshMu.Lock()
		status := s.refreshState
		s.refreshMu.Unlock()
		if s.profileStore != nil {
			if items, err := s.profileStore.ListProfiles(); err == nil {
				for _, p := range items {
					if p.AutoUpdateEnabled {
						status.Enabled = true
						seconds := int64(p.AutoUpdateInterval * 60)
						if status.IntervalSeconds == 0 || seconds < status.IntervalSeconds {
							status.IntervalSeconds = seconds
						}
					}
				}
			}
		}
		writeJSON(w, http.StatusOK, status)
	case http.MethodPut:
		if !validLocalJSONRequest(r) {
			writeError(w, http.StatusForbidden, "local same-origin JSON request required")
			return
		}
		var req struct {
			Enabled         bool  `json:"enabled"`
			IntervalSeconds int64 `json:"interval_seconds"`
		}
		if err := decodeStrictJSON(w, r, &req, 4096); err != nil {
			writeError(w, http.StatusBadRequest, "invalid refresh settings")
			return
		}
		if req.IntervalSeconds < 3600 || req.IntervalSeconds > int64((30*24*time.Hour).Seconds()) {
			writeError(w, http.StatusUnprocessableEntity, "refresh interval must be between 1 hour and 30 days")
			return
		}
		if s.stateDir == "" {
			writeError(w, http.StatusServiceUnavailable, "persistent state directory is not configured")
			return
		}
		data, _ := json.Marshal(struct {
			Enabled         bool  `json:"enabled"`
			IntervalSeconds int64 `json:"interval_seconds"`
		}{req.Enabled, req.IntervalSeconds})
		path := filepath.Join(s.stateDir, "refresh-settings.json")
		tmp, err := os.CreateTemp(s.stateDir, ".refresh-settings-*")
		if err != nil {
			writeError(w, http.StatusInternalServerError, "cannot persist refresh settings")
			return
		}
		name := tmp.Name()
		_ = tmp.Chmod(0600)
		_, err = tmp.Write(append(data, '\n'))
		if err == nil {
			err = tmp.Sync()
		}
		closeErr := tmp.Close()
		if err == nil {
			err = closeErr
		}
		if err == nil {
			err = os.Rename(name, path)
		}
		_ = os.Remove(name)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "cannot persist refresh settings")
			return
		}
		s.refreshMu.Lock()
		s.refreshState.Enabled = req.Enabled
		s.refreshState.IntervalSeconds = req.IntervalSeconds
		s.refreshOpts.RefreshInterval = 0
		if req.Enabled {
			s.refreshOpts.RefreshInterval = time.Duration(req.IntervalSeconds) * time.Second
			next := time.Now().UTC().Add(s.refreshOpts.RefreshInterval)
			s.refreshState.NextAttempt = &next
		} else {
			s.refreshState.NextAttempt = nil
		}
		s.refreshMu.Unlock()
		select {
		case s.refreshWake <- struct{}{}:
		default:
		}
		writeJSON(w, http.StatusOK, map[string]any{"status": "saved", "enabled": req.Enabled, "interval_seconds": req.IntervalSeconds})
	default:
		methodNotAllowed(w)
	}
}

func (s *Server) profileRefreshDelay() (time.Duration, bool) {
	if s.profileStore == nil {
		return 0, false
	}
	items, err := s.profileStore.ListProfiles()
	if err != nil {
		return 0, false
	}
	var earliest time.Duration
	found := false
	for _, p := range items {
		if !p.AutoUpdateEnabled || p.SubscriptionURL == "" {
			continue
		}
		interval := time.Duration(p.AutoUpdateInterval) * time.Minute
		if interval < 5*time.Minute {
			interval = 24 * time.Hour
		}
		delay := time.Duration(0)
		if p.LastSubscriptionUpdate > 0 {
			delay = time.Until(time.Unix(p.LastSubscriptionUpdate, 0).Add(interval))
			if delay < 0 {
				delay = 0
			}
		}
		if !found || delay < earliest {
			earliest, found = delay, true
		}
	}
	return earliest, found
}

func nextDailyBlocklistUpdate(now time.Time, updateTime string) time.Time {
	parts := strings.Split(updateTime, ":")
	if len(parts) != 2 {
		return now.Add(24 * time.Hour)
	}
	hour, errHour := strconv.Atoi(parts[0])
	minute, errMinute := strconv.Atoi(parts[1])
	if errHour != nil || errMinute != nil || hour < 0 || hour > 23 || minute < 0 || minute > 59 {
		return now.Add(24 * time.Hour)
	}
	next := time.Date(now.Year(), now.Month(), now.Day(), hour, minute, 0, 0, now.Location())
	if !next.After(now) {
		next = next.AddDate(0, 0, 1)
	}
	return next
}

// runBlocklistScheduler mirrors HRNeo's daily DNS blocklist job. Its schedule
// is independent of subscription refresh and uses the saved local wall-clock
// time (default 04:30) and optional interface-bound downloader.
func (s *Server) runBlocklistScheduler(ctx context.Context) {
	for {
		if s.blocklists == nil {
			select {
			case <-ctx.Done():
				return
			case <-s.blocklistWake:
				continue
			}
		}
		settings := s.blocklists.Settings()
		if !settings.AutoUpdate {
			select {
			case <-ctx.Done():
				return
			case <-s.blocklistWake:
				continue
			}
		}
		next := nextDailyBlocklistUpdate(time.Now(), settings.UpdateTime)
		timer := time.NewTimer(time.Until(next))
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-s.blocklistWake:
			timer.Stop()
			continue
		case <-timer.C:
		}
		refreshCtx, cancel := context.WithTimeout(ctx, 60*time.Minute)
		failures := s.blocklists.RefreshAll(refreshCtx)
		cancel()
		if ctx.Err() != nil {
			return
		}
		s.applyDNSBlocklists()
		if len(failures) > 0 {
			messages := make([]string, 0, len(failures))
			for _, err := range failures {
				messages = append(messages, err.Error())
			}
			s.recordEvent("warning", "dns", "blocklist_scheduled_refresh_partial", strings.Join(messages, "; "))
		} else {
			s.recordEvent("info", "dns", "blocklist_scheduled_refresh", "Scheduled DNS blocklist refresh completed")
		}
	}
}

func (s *Server) wakeBlocklistScheduler() {
	if s.blocklistWake == nil {
		return
	}
	select {
	case s.blocklistWake <- struct{}{}:
	default:
	}
}

func (s *Server) runRefreshScheduler(ctx context.Context) {
	retryDelay := time.Duration(0)
	for {
		profileDelay, profileScheduled := s.profileRefreshDelay()
		s.refreshMu.Lock()
		legacyInterval := s.refreshOpts.RefreshInterval
		s.refreshMu.Unlock()
		interval := legacyInterval
		if profileScheduled {
			interval = profileDelay
			if interval < time.Second {
				interval = time.Second
			}
		}
		if interval <= 0 {
			select {
			case <-ctx.Done():
				return
			case <-s.refreshWake:
				continue
			}
		}
		delay := interval
		if retryDelay > 0 {
			delay = retryDelay
		}
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-s.refreshWake:
			timer.Stop()
			continue
		case <-timer.C:
		}
		now := time.Now().UTC()
		s.refreshMu.Lock()
		s.refreshState.Running = true
		s.refreshState.LastAttempt = &now
		s.refreshMu.Unlock()
		refreshCtx, cancel := context.WithTimeout(ctx, 90*time.Second)
		s.opMu.Lock()
		var refreshErr error
		if profileScheduled {
			_, refreshErr = s.refreshProfilesScheduledLocked(refreshCtx)
		} else {
			_, refreshErr = s.refreshProfilesLocked(refreshCtx)
		}
		s.opMu.Unlock()
		cancel()
		next := interval
		s.refreshMu.Lock()
		s.refreshState.Running = false
		if refreshErr == nil {
			retryDelay = 0
			lastSuccess := time.Now().UTC()
			s.refreshState.LastSuccess = &lastSuccess
			s.refreshState.LastError = ""
			s.refreshState.ConsecutiveFailures = 0
		} else {
			s.refreshState.LastError = refreshErr.Error()
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
		if refreshErr != nil {
			retryDelay = next
		}

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
				s.recordEvent("error", "runtime", "start_failed", "Xray failed to start")
				writeError(w, http.StatusConflict, err.Error())
				return
			}
			s.recordEvent("info", "runtime", "started", "Xray process started")
		case "stop":
			if err := s.supervisor.Stop(ctx); err != nil {
				s.recordEvent("error", "runtime", "stop_failed", "Xray failed to stop cleanly")
				writeError(w, http.StatusConflict, err.Error())
				return
			}
			s.recordEvent("warning", "runtime", "stopped", "Xray process stopped")
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

func (s *Server) restoreDNSConfig(previous dnsservice.Config, wasRunning bool) error {
	var failures []string
	if running, _ := s.dns.Status()["running"].(bool); running {
		if err := s.dns.Stop(); err != nil {
			failures = append(failures, "stop new DNS service: "+err.Error())
		}
	}
	if err := s.dns.Save(previous); err != nil {
		failures = append(failures, "restore DNS settings: "+err.Error())
	}
	if wasRunning && previous.Enabled {
		if err := s.dns.Start(); err != nil {
			failures = append(failures, "restart previous DNS service: "+err.Error())
		}
	}
	if len(failures) > 0 {
		return fmt.Errorf("%s", strings.Join(failures, "; "))
	}
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
		previous := s.dns.Config()
		wasRunning, _ := s.dns.Status()["running"].(bool)
		if !c.Enabled && s.smartDNSRuntime != nil && s.smartDNSRuntime.Status().Running {
			writeError(w, http.StatusConflict, "stop SmartDNS before disabling the Core DNS policy resolver")
			return
		}
		if c.Enabled && s.smartDNSConfig.Enabled && c.Port == smartDNSListenPort(s.smartDNSConfig) {
			writeError(w, http.StatusConflict, "SmartDNS and Core DNS must use different loopback ports")
			return
		}
		if wasRunning {
			if err := s.dns.Stop(); err != nil {
				writeError(w, 500, "cannot stop DNS service for configuration update: "+err.Error())
				return
			}
		}
		if err := s.dns.Save(c); err != nil {
			if wasRunning && previous.Enabled {
				_ = s.dns.Start()
			}
			writeError(w, 422, err.Error())
			return
		}
		if c.Enabled {
			if err := s.dns.Start(); err != nil {
				rollbackErr := s.restoreDNSConfig(previous, wasRunning)
				if rollbackErr != nil {
					writeError(w, 500, "DNS restart failed: "+err.Error()+"; rollback failed: "+rollbackErr.Error())
					return
				}
				writeError(w, 500, "DNS restart failed; previous DNS settings restored: "+err.Error())
				return
			}
		}
		if err := s.applyCoreDNSRuntime(c.Enabled); err != nil {
			rollbackErr := s.restoreDNSConfig(previous, wasRunning)
			if rollbackErr != nil {
				writeError(w, 500, "Core DNS apply failed: "+err.Error()+"; DNS rollback failed: "+rollbackErr.Error())
				return
			}
			writeError(w, 500, "Core DNS apply failed; previous DNS settings restored: "+err.Error())
			return
		}
		if !c.Enabled {
			_ = s.dns.SetSmartDNSForwarder("")
		}
		s.recordEvent("info", "dns", "configuration_updated", "Core DNS configuration updated")
		writeJSON(w, 200, map[string]any{"config": s.dns.Config(), "status": s.dns.Status(), "stats": s.dns.Stats()})
	default:
		methodNotAllowed(w)
	}
}

func (s *Server) dnsService(w http.ResponseWriter, r *http.Request) {
	if s.dns == nil {
		writeError(w, http.StatusServiceUnavailable, "DNS state directory is not configured")
		return
	}
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
	previous := s.dns.Config()
	wasRunning, _ := s.dns.Status()["running"].(bool)
	var err error
	switch req.Action {
	case "start":
		next := previous
		next.Enabled = true
		if err = s.dns.Save(next); err == nil {
			err = s.dns.Start()
		}
		if err == nil {
			err = s.applyCoreDNSRuntime(true)
		}
		if err != nil {
			rollbackErr := s.restoreDNSConfig(previous, wasRunning)
			if rollbackErr != nil {
				err = fmt.Errorf("start DNS failed: %v; rollback failed: %w", err, rollbackErr)
			} else {
				err = fmt.Errorf("start DNS failed; previous DNS settings restored: %w", err)
			}
		}
	case "stop":
		if s.smartDNSRuntime != nil && s.smartDNSRuntime.Status().Running {
			writeError(w, http.StatusConflict, "stop SmartDNS before stopping the Core DNS policy resolver")
			return
		}
		if err = s.applyCoreDNSRuntime(false); err != nil {
			break
		}
		if err = s.dns.Stop(); err != nil {
			if previous.Enabled {
				if rollbackErr := s.applyCoreDNSRuntime(true); rollbackErr != nil {
					err = fmt.Errorf("stop DNS failed: %v; Xray DNS rollback failed: %w", err, rollbackErr)
				}
			}
			break
		}
		next := previous
		next.Enabled = false
		if err = s.dns.Save(next); err != nil {
			rollbackErr := s.restoreDNSConfig(previous, wasRunning)
			if previous.Enabled {
				if xrayErr := s.applyCoreDNSRuntime(true); xrayErr != nil {
					rollbackErr = errors.Join(rollbackErr, xrayErr)
				}
			}
			if rollbackErr != nil {
				err = fmt.Errorf("save DNS stop state failed: %v; rollback failed: %w", err, rollbackErr)
			}
		} else {
			_ = s.dns.SetSmartDNSForwarder("")
		}
	default:
		writeError(w, 400, "action must be start or stop")
		return
	}
	if err != nil {
		s.recordEvent("error", "dns", "service_action_failed", "DNS service action failed")
		writeError(w, 409, err.Error())
		return
	}
	if req.Action == "start" {
		s.recordEvent("info", "dns", "started", "Core DNS resolver started")
	} else {
		s.recordEvent("warning", "dns", "stopped", "Core DNS resolver stopped")
	}
	writeJSON(w, 200, map[string]any{"status": s.dns.Status(), "config": s.dns.Config()})
}

func (s *Server) dnsCheck(w http.ResponseWriter, r *http.Request) {
	if s.dns == nil {
		writeError(w, http.StatusServiceUnavailable, "DNS state directory is not configured")
		return
	}
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

func (s *Server) loadL7Settings() {
	path := filepath.Join(s.stateDir, "l7", "settings.json")
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 || info.Size() > 4096 {
		return
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return
	}
	var saved compiler.L7SniffingConfig
	if json.Unmarshal(data, &saved) != nil || (saved.Enabled && !saved.HTTP && !saved.TLS && !saved.QUIC) {
		return
	}
	s.l7Settings = saved
}

func (s *Server) l7SettingsHandler(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		if !validLocalReadRequest(r) {
			writeError(w, http.StatusForbidden, "local request required")
			return
		}
		s.opMu.Lock()
		settings := s.l7Settings
		s.opMu.Unlock()
		writeJSON(w, http.StatusOK, settings)
	case http.MethodPut:
		if !validLocalJSONRequest(r) {
			writeError(w, http.StatusForbidden, "local same-origin JSON request required")
			return
		}
		var req compiler.L7SniffingConfig
		if err := decodeStrictJSON(w, r, &req, 4096); err != nil {
			writeError(w, http.StatusBadRequest, "invalid L7 settings")
			return
		}
		if req.Enabled && !req.HTTP && !req.TLS && !req.QUIC {
			writeError(w, http.StatusUnprocessableEntity, "select at least one L7 protocol")
			return
		}
		s.opMu.Lock()
		defer s.opMu.Unlock()
		if s.stateDir == "" {
			writeError(w, http.StatusServiceUnavailable, "state directory is not configured")
			return
		}
		dir := filepath.Join(s.stateDir, "l7")
		if err := os.MkdirAll(dir, 0700); err != nil {
			writeError(w, http.StatusInternalServerError, "cannot create L7 state directory")
			return
		}
		if err := os.Chmod(dir, 0700); err != nil {
			writeError(w, http.StatusInternalServerError, "cannot secure L7 state directory")
			return
		}
		data, _ := json.Marshal(req)
		tmp, err := os.CreateTemp(dir, ".settings-*.json")
		if err != nil {
			writeError(w, http.StatusInternalServerError, "cannot save L7 settings")
			return
		}
		name := tmp.Name()
		defer os.Remove(name)
		if err = tmp.Chmod(0600); err == nil {
			_, err = tmp.Write(data)
		}
		if err == nil {
			err = tmp.Sync()
		}
		closeErr := tmp.Close()
		if err == nil {
			err = closeErr
		}
		if err == nil {
			err = os.Rename(name, filepath.Join(dir, "settings.json"))
		}
		if err != nil {
			writeError(w, http.StatusInternalServerError, "cannot persist L7 settings")
			return
		}
		s.l7Settings = req
		writeJSON(w, http.StatusOK, map[string]any{"status": "saved", "settings": req, "applies": "next configuration build", "scope": "Core SOCKS5 inbounds only"})
	default:
		methodNotAllowed(w)
	}
}

type observatorySettings struct {
	ProbeIntervalSeconds int `json:"probe_interval_seconds"`
}

func (s *Server) loadObservatorySettings() {
	path := filepath.Join(s.stateDir, "observatory", "settings.json")
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 || info.Size() > 4096 {
		return
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return
	}
	var saved observatorySettings
	if json.Unmarshal(data, &saved) == nil && saved.ProbeIntervalSeconds >= 5 && saved.ProbeIntervalSeconds <= 3600 {
		s.observatoryInterval = saved.ProbeIntervalSeconds
	}
}

// applyObservatoryInterval updates only the managed Xray Observatory interval.
// If Xray is running it is restarted transactionally; on failure the previous
// validated config is restored and the previous runtime state is recovered.
func (s *Server) applyObservatoryInterval(seconds int) (bool, error) {
	previous, err := s.supervisor.SnapshotConfig()
	if err != nil || len(previous) == 0 {
		return false, err
	}
	var cfg map[string]any
	if err := json.Unmarshal(previous, &cfg); err != nil {
		return false, fmt.Errorf("cannot read prepared Xray config: %w", err)
	}
	observatory, ok := cfg["observatory"].(map[string]any)
	if !ok {
		return false, nil
	}
	subjects := 1
	if selector, ok := observatory["subjectSelector"].([]any); ok && len(selector) > 0 {
		subjects = len(selector)
	}
	observatory["probeInterval"] = fmt.Sprintf("%dms", compiler.ObservatoryProbeIntervalMS(seconds, subjects))
	next, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return false, err
	}
	wasRunning := s.supervisor.Status().Running
	if wasRunning {
		stopCtx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
		err = s.supervisor.Stop(stopCtx)
		cancel()
		if err != nil {
			return false, fmt.Errorf("cannot stop Xray to apply Observatory interval: %w", err)
		}
	}
	applyCtx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	err = s.supervisor.Prepare(applyCtx, next)
	if err == nil && wasRunning {
		err = s.supervisor.Start(applyCtx)
		if err == nil {
			time.Sleep(350 * time.Millisecond)
			if !s.supervisor.Status().Running {
				err = fmt.Errorf("Xray exited immediately after applying Observatory settings")
			}
		}
	}
	cancel()
	if err == nil {
		return true, nil
	}
	if wasRunning && s.supervisor.Status().Running {
		stopCtx, stopCancel := context.WithTimeout(context.Background(), 8*time.Second)
		stopErr := s.supervisor.Stop(stopCtx)
		stopCancel()
		if stopErr != nil {
			return false, fmt.Errorf("apply Observatory interval failed: %v; cannot stop failed runtime for rollback: %w", err, stopErr)
		}
	}
	restoreCtx, restoreCancel := context.WithTimeout(context.Background(), 20*time.Second)
	restoreErr := s.supervisor.Prepare(restoreCtx, previous)
	if restoreErr == nil && wasRunning {
		restoreErr = s.supervisor.Start(restoreCtx)
	}
	restoreCancel()
	if restoreErr != nil {
		return false, fmt.Errorf("apply Observatory interval failed: %v; previous config/runtime restore failed: %w", err, restoreErr)
	}
	return false, fmt.Errorf("apply Observatory interval failed; previous Xray config restored: %w", err)
}

func (s *Server) observatorySettings(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		if !validLocalReadRequest(r) {
			writeError(w, http.StatusForbidden, "local request required")
			return
		}
		s.opMu.Lock()
		value := s.observatoryInterval
		s.opMu.Unlock()
		writeJSON(w, http.StatusOK, observatorySettings{ProbeIntervalSeconds: value})
	case http.MethodPut:
		if !validLocalJSONRequest(r) {
			writeError(w, http.StatusForbidden, "local same-origin JSON request required")
			return
		}
		var req observatorySettings
		if err := decodeStrictJSON(w, r, &req, 4096); err != nil {
			writeError(w, http.StatusBadRequest, "invalid Observatory settings")
			return
		}
		if req.ProbeIntervalSeconds < 5 || req.ProbeIntervalSeconds > 3600 {
			writeError(w, http.StatusUnprocessableEntity, "probe interval must be between 5 and 3600 seconds")
			return
		}
		s.opMu.Lock()
		defer s.opMu.Unlock()
		if s.stateDir == "" {
			writeError(w, http.StatusServiceUnavailable, "state directory is not configured")
			return
		}
		dir := filepath.Join(s.stateDir, "observatory")
		if err := os.MkdirAll(dir, 0700); err != nil {
			writeError(w, http.StatusInternalServerError, "cannot create Observatory state directory")
			return
		}
		if err := os.Chmod(dir, 0700); err != nil {
			writeError(w, http.StatusInternalServerError, "cannot secure Observatory state directory")
			return
		}
		previousInterval := s.observatoryInterval
		data, _ := json.Marshal(req)
		tmp, err := os.CreateTemp(dir, ".settings-*.json")
		if err != nil {
			writeError(w, http.StatusInternalServerError, "cannot save Observatory settings")
			return
		}
		name := tmp.Name()
		defer os.Remove(name)
		if err = tmp.Chmod(0600); err == nil {
			_, err = tmp.Write(data)
		}
		if err == nil {
			err = tmp.Sync()
		}
		closeErr := tmp.Close()
		if err == nil {
			err = closeErr
		}
		if err != nil {
			writeError(w, http.StatusInternalServerError, "cannot prepare Observatory settings")
			return
		}
		wasRunning := s.supervisor.Status().Running
		runtimeUpdated, applyErr := s.applyObservatoryInterval(req.ProbeIntervalSeconds)
		if applyErr != nil {
			writeError(w, http.StatusServiceUnavailable, applyErr.Error())
			return
		}
		if err = os.Rename(name, filepath.Join(dir, "settings.json")); err != nil {
			if runtimeUpdated {
				if _, rollbackErr := s.applyObservatoryInterval(previousInterval); rollbackErr != nil {
					writeError(w, http.StatusInternalServerError, "cannot persist Observatory settings; runtime rollback failed: "+rollbackErr.Error())
					return
				}
			}
			writeError(w, http.StatusInternalServerError, "cannot persist Observatory settings")
			return
		}
		s.observatoryInterval = req.ProbeIntervalSeconds
		applies := "saved for next configuration build"
		if runtimeUpdated && wasRunning {
			applies = "active Xray runtime restarted"
		} else if runtimeUpdated {
			applies = "prepared Xray configuration updated"
		}
		writeJSON(w, http.StatusOK, map[string]any{"status": "saved", "probe_interval_seconds": s.observatoryInterval, "applies": applies})
	default:
		methodNotAllowed(w)
	}
}

func (s *Server) syncWatchlist() {
	s.watchlistMu.Lock()
	defer s.watchlistMu.Unlock()
	fail := func(err error) {
		s.watchlistErr = err.Error()
		if s.watchlist != nil {
			_ = s.watchlist.Close()
			s.watchlist = nil
		}
		s.watchlistPath = ""
		s.watchlistTargets = 0
		s.watchlistKeys = 0
		s.watchlistPartial = false
		s.watchlistExcluded = 0
	}
	if s.routingStore == nil {
		fail(fmt.Errorf("shared routing store is not configured"))
		return
	}
	cfg, found, err := s.routingStore.Load()
	if err != nil {
		fail(fmt.Errorf("cannot load routing snapshot"))
		return
	}
	if !found || !cfg.Enabled {
		if s.watchlist != nil {
			_ = s.watchlist.Close()
			s.watchlist = nil
		}
		s.watchlistPath = ""
		s.watchlistErr = ""
		s.watchlistTargets = 0
		s.watchlistKeys = 0
		s.watchlistPartial = false
		s.watchlistExcluded = 0
		return
	}
	if s.supervisor == nil || !s.supervisor.Status().Configured {
		fail(fmt.Errorf("shared routing runtime is not configured"))
		return
	}
	snapshot, err := hrwatchlist.Build(cfg, os.Getenv("XRAY_LOCATION_ASSET"))
	if err != nil {
		fail(fmt.Errorf("cannot build watchlist snapshot: %w", err))
		return
	}
	if s.stateDir == "" {
		fail(fmt.Errorf("watchlist state directory is not configured"))
		return
	}
	if err := os.MkdirAll(s.stateDir, 0700); err != nil {
		fail(fmt.Errorf("cannot create watchlist state directory"))
		return
	}
	info, err := os.Lstat(s.stateDir)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm()&0077 != 0 {
		fail(fmt.Errorf("watchlist state directory must be private and not a symlink"))
		return
	}
	path := filepath.Join(s.stateDir, "watchlist.sock")
	if s.watchlist == nil {
		s.watchlist, err = hrwatchlist.Listen(path, snapshot)
		if err != nil {
			fail(fmt.Errorf("cannot create local watchlist socket: %w", err))
			return
		}
	} else {
		s.watchlist.Update(snapshot)
	}
	s.watchlistPath = path
	s.watchlistErr = ""
	s.watchlistTargets = len(snapshot.Targets())
	s.watchlistKeys = len(snapshot.Entries())
	s.watchlistPartial = snapshot.Partial()
	s.watchlistExcluded = snapshot.ExcludedPatterns
}

func (s *Server) closeWatchlist() {
	s.watchlistMu.Lock()
	defer s.watchlistMu.Unlock()
	if s.watchlist != nil {
		_ = s.watchlist.Close()
		s.watchlist = nil
	}
	s.watchlistPath = ""
	s.watchlistTargets = 0
	s.watchlistKeys = 0
	s.watchlistPartial = false
	s.watchlistExcluded = 0
}

func (s *Server) watchlistStatus(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		methodNotAllowed(w)
		return
	}
	if !validLocalReadRequest(r) {
		writeError(w, http.StatusForbidden, "local request required")
		return
	}
	s.watchlistMu.Lock()
	status := map[string]any{
		"ready":             s.watchlist != nil,
		"path":              s.watchlistPath,
		"targets":           s.watchlistTargets,
		"keys":              s.watchlistKeys,
		"partial":           s.watchlistPartial,
		"excluded_patterns": s.watchlistExcluded,
	}
	if s.watchlistErr != "" {
		status["error"] = s.watchlistErr
	}
	s.watchlistMu.Unlock()
	writeJSON(w, http.StatusOK, status)
}

func (s *Server) setSmartDNSPolicyConfig(cfg hrsmartdns.Config) {
	sets := make([]hrsmartdns.DomainSet, len(cfg.DomainSets))
	for i, set := range cfg.DomainSets {
		sets[i] = set
		sets[i].Domains = append([]string(nil), set.Domains...)
	}
	s.dnsPolicyMu.Lock()
	s.smartDNSPolicyOn = cfg.Enabled
	s.smartDNSDomainSets = sets
	s.dnsPolicyMu.Unlock()
}

func (s *Server) applyDNSBlocklists() {
	if s.dns == nil {
		return
	}
	var blocked, allowed []string
	if s.blocklists != nil {
		snapshot := s.blocklists.Snapshot()
		blocked = append(blocked, snapshot.Blocked...)
		allowed = append(allowed, snapshot.Allowed...)
	}
	// Both managed URL lists and configured block/allow domain sets are
	// evaluated by the Core DNS front resolver. SmartDNS receives route sets
	// only, avoiding inherited address-flag precedence differences.
	s.dnsPolicyMu.RLock()
	policyEnabled := s.smartDNSPolicyOn
	sets := make([]hrsmartdns.DomainSet, len(s.smartDNSDomainSets))
	for i, set := range s.smartDNSDomainSets {
		sets[i] = set
		sets[i].Domains = append([]string(nil), set.Domains...)
	}
	s.dnsPolicyMu.RUnlock()
	if policyEnabled {
		for _, set := range sets {
			if !set.Enabled {
				continue
			}
			switch strings.ToLower(strings.TrimSpace(set.Kind)) {
			case "block":
				blocked = append(blocked, set.Domains...)
			case "allow":
				allowed = append(allowed, set.Domains...)
			}
		}
	}
	s.dns.SetBlocklistDomains(blocked, allowed)
}

func smartDNSListenPort(cfg hrsmartdns.Config) int {
	if cfg.ListenPort == 0 {
		return hrsmartdns.DefaultConfig().ListenPort
	}
	return cfg.ListenPort
}

func smartDNSForwardAddress(cfg hrsmartdns.Config) string {
	return net.JoinHostPort("127.0.0.1", strconv.Itoa(smartDNSListenPort(cfg)))
}

func (s *Server) smartDNSStatusError() string {
	return s.smartDNSErr
}

func (s *Server) smartDNSProfileRefs() []hrsmartdns.ProfileRef {
	if s.profileStore == nil {
		return nil
	}
	items, err := s.profileStore.List()
	if err != nil {
		return nil
	}
	out := make([]hrsmartdns.ProfileRef, 0, len(items))
	for _, item := range items {
		if !item.Enabled {
			continue
		}
		listen := item.Listen
		if listen == "" {
			listen = "127.0.0.1"
		}
		out = append(out, hrsmartdns.ProfileRef{ID: item.ID, Enabled: item.Enabled, Listen: listen, Port: int(item.Port)})
	}
	return out
}

func (s *Server) persistSmartDNSConfig(cfg hrsmartdns.Config) error {
	if s.smartDNSConfigPath == "" {
		return fmt.Errorf("SmartDNS state path is not configured")
	}
	dir := filepath.Dir(s.smartDNSConfigPath)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	info, err := os.Lstat(dir)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm()&0077 != 0 {
		return fmt.Errorf("SmartDNS configuration directory must be private and must not be a symlink")
	}
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".smartdns-config-*.tmp")
	if err != nil {
		return err
	}
	name := tmp.Name()
	defer os.Remove(name)
	if err := tmp.Chmod(0600); err != nil {
		tmp.Close()
		return err
	}
	if _, err := tmp.Write(append(data, '\n')); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(name, s.smartDNSConfigPath); err != nil {
		return err
	}
	d, err := os.Open(dir)
	if err != nil {
		return err
	}
	defer d.Close()
	return d.Sync()
}

func (s *Server) smartDNSHandler(w http.ResponseWriter, r *http.Request) {
	if s.smartDNSRuntime == nil {
		writeError(w, http.StatusServiceUnavailable, "SmartDNS runtime is unavailable")
		return
	}
	switch r.Method {
	case http.MethodGet:
		if !validLocalReadRequest(r) {
			writeError(w, http.StatusForbidden, "local request required")
			return
		}
		s.opMu.Lock()
		defer s.opMu.Unlock()
		writeJSON(w, http.StatusOK, map[string]any{"config": s.smartDNSConfig, "status": s.smartDNSRuntime.Status(), "error": s.smartDNSStatusError()})
	case http.MethodPut:
		if !validLocalJSONRequest(r) {
			writeError(w, http.StatusForbidden, "local same-origin JSON request required")
			return
		}
		var cfg hrsmartdns.Config
		if err := decodeStrictJSON(w, r, &cfg, 256<<10); err != nil {
			writeError(w, http.StatusBadRequest, "invalid SmartDNS configuration")
			return
		}
		defaults := hrsmartdns.DefaultConfig()
		if cfg.ListenHost == "" {
			cfg.ListenHost = defaults.ListenHost
		}
		if cfg.ListenPort == 0 {
			cfg.ListenPort = defaults.ListenPort
		}
		if s.dns != nil && cfg.ListenPort == s.dns.Config().Port {
			writeError(w, http.StatusConflict, "SmartDNS and Core DNS must use different loopback ports")
			return
		}
		cfg.CAPath = defaults.CAPath
		cfg.DataDir = ""
		s.opMu.Lock()
		defer s.opMu.Unlock()
		previous := s.smartDNSConfig
		wasRunning := s.smartDNSRuntime.Status().Running
		wasForwarding := s.dns != nil && s.dns.SmartDNSForwarder() != ""
		if err := s.persistSmartDNSConfig(cfg); err != nil {
			writeError(w, http.StatusInternalServerError, "cannot persist SmartDNS configuration")
			return
		}
		if err := s.smartDNSRuntime.Apply(r.Context(), cfg, s.smartDNSProfileRefs()); err != nil {
			if restoreErr := s.persistSmartDNSConfig(previous); restoreErr != nil {
				s.smartDNSErr = "SmartDNS apply failed and saved configuration rollback failed: " + restoreErr.Error()
			} else {
				s.smartDNSErr = err.Error()
			}
			writeError(w, http.StatusUnprocessableEntity, s.smartDNSErr)
			return
		}
		s.smartDNSConfig = cfg
		s.setSmartDNSPolicyConfig(cfg)
		s.smartDNSErr = ""
		s.applyDNSBlocklists()
		if s.dns != nil {
			if (wasRunning || wasForwarding) && cfg.Enabled {
				if err := s.dns.SetSmartDNSForwarder(smartDNSForwardAddress(cfg)); err != nil {
					s.smartDNSErr = err.Error()
					writeError(w, http.StatusInternalServerError, "SmartDNS restarted but policy forwarding could not be updated")
					return
				}
			} else if !cfg.Enabled {
				_ = s.dns.SetSmartDNSForwarder("")
			}
		}
		s.recordEvent("info", "dns", "smartdns_config_prepared", "SmartDNS configuration prepared")
		writeJSON(w, http.StatusOK, map[string]any{"config": s.smartDNSConfig, "status": s.smartDNSRuntime.Status(), "error": ""})
	default:
		methodNotAllowed(w)
	}
}

func (s *Server) smartDNSAction(w http.ResponseWriter, r *http.Request) {
	if s.smartDNSRuntime == nil {
		writeError(w, http.StatusServiceUnavailable, "SmartDNS runtime is unavailable")
		return
	}
	if r.Method != http.MethodPost {
		methodNotAllowed(w)
		return
	}
	if !validLocalJSONRequest(r) {
		writeError(w, http.StatusForbidden, "local same-origin JSON request required")
		return
	}
	action := strings.Trim(strings.TrimPrefix(r.URL.Path, "/v1/dns/smartdns/"), "/")
	s.opMu.Lock()
	defer s.opMu.Unlock()
	switch action {
	case "start":
		if !s.smartDNSConfig.Enabled {
			writeError(w, http.StatusConflict, "SmartDNS is disabled")
			return
		}
		if s.dns == nil || !s.dns.Config().Enabled || !s.dns.Status()["running"].(bool) {
			writeError(w, http.StatusConflict, "start the Core DNS policy resolver before SmartDNS")
			return
		}
		if s.dns.Config().Port == smartDNSListenPort(s.smartDNSConfig) {
			writeError(w, http.StatusConflict, "SmartDNS and Core DNS must use different loopback ports")
			return
		}
		// Install the exclusive forwarder before starting SmartDNS. Queries
		// during startup must fail closed rather than leak to public DNS.
		if err := s.dns.SetSmartDNSForwarder(smartDNSForwardAddress(s.smartDNSConfig)); err != nil {
			writeError(w, http.StatusInternalServerError, "cannot attach SmartDNS as the exclusive DNS upstream")
			return
		}
		if err := s.smartDNSRuntime.Start(r.Context()); err != nil {
			s.smartDNSErr = err.Error()
			s.recordEvent("error", "dns", "smartdns_start_failed", "SmartDNS failed to start; Core DNS remains fail-closed")
			writeError(w, http.StatusBadGateway, "SmartDNS failed to start; DNS forwarding remains fail-closed")
			return
		}
		s.smartDNSErr = ""
		s.recordEvent("info", "dns", "smartdns_started", "SmartDNS sidecar started behind Core DNS policy resolver")
	case "stop":
		if err := s.smartDNSRuntime.Stop(r.Context()); err != nil {
			s.smartDNSErr = err.Error()
			writeError(w, http.StatusBadGateway, "SmartDNS failed to stop")
			return
		}
		if s.dns != nil {
			_ = s.dns.SetSmartDNSForwarder("")
		}
		s.recordEvent("info", "dns", "smartdns_stopped", "SmartDNS sidecar stopped; Core DNS upstream restored")
	default:
		writeError(w, http.StatusNotFound, "SmartDNS action not found")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"config": s.smartDNSConfig, "status": s.smartDNSRuntime.Status(), "error": s.smartDNSErr})
}

func (s *Server) dnsBlocklistCustom(w http.ResponseWriter, r *http.Request) {
	if s.blocklists == nil {
		writeError(w, http.StatusServiceUnavailable, "DNS blocklist state is not configured")
		return
	}
	switch r.Method {
	case http.MethodGet:
		if !validLocalReadRequest(r) {
			writeError(w, http.StatusForbidden, "local request required")
			return
		}
		snapshot := s.blocklists.Snapshot()
		writeJSON(w, http.StatusOK, map[string]string{
			"block": strings.Join(snapshot.CustomBlock, "\n"),
			"allow": strings.Join(snapshot.CustomAllow, "\n"),
		})
	case http.MethodPut:
		if !validLocalJSONRequest(r) {
			writeError(w, http.StatusForbidden, "local same-origin JSON request required")
			return
		}
		var input struct {
			Block string `json:"block"`
			Allow string `json:"allow"`
		}
		if err := decodeStrictJSON(w, r, &input, 2<<20); err != nil {
			writeError(w, http.StatusBadRequest, "invalid custom DNS block/allow lists")
			return
		}
		block, err := dnsblocklist.ParseCustomList(strings.NewReader(input.Block))
		if err != nil {
			writeError(w, http.StatusUnprocessableEntity, "invalid custom block list: "+err.Error())
			return
		}
		allow, err := dnsblocklist.ParseCustomList(strings.NewReader(input.Allow))
		if err != nil {
			writeError(w, http.StatusUnprocessableEntity, "invalid custom allow list: "+err.Error())
			return
		}
		snapshot, err := s.blocklists.SaveCustomLists(block, allow)
		if err != nil {
			writeError(w, http.StatusUnprocessableEntity, err.Error())
			return
		}
		s.applyDNSBlocklists()
		s.recordEvent("info", "dns", "blocklist_custom_saved", "Custom DNS block and allow lists saved")
		writeJSON(w, http.StatusOK, map[string]any{
			"block":    strings.Join(snapshot.CustomBlock, "\n"),
			"allow":    strings.Join(snapshot.CustomAllow, "\n"),
			"snapshot": snapshot,
		})
	default:
		methodNotAllowed(w)
	}
}

func (s *Server) dnsBlocklistCatalog(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		methodNotAllowed(w)
		return
	}
	if !validLocalReadRequest(r) {
		writeError(w, http.StatusForbidden, "local request required")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"presets": dnsblocklist.Catalog()})
}

func (s *Server) dnsBlocklistSettings(w http.ResponseWriter, r *http.Request) {
	if s.blocklists == nil {
		writeError(w, http.StatusServiceUnavailable, "DNS blocklist state is not configured")
		return
	}
	switch r.Method {
	case http.MethodGet:
		if !validLocalReadRequest(r) {
			writeError(w, http.StatusForbidden, "local request required")
			return
		}
		settings := s.blocklists.Settings()
		next := any(nil)
		if settings.AutoUpdate {
			next = nextDailyBlocklistUpdate(time.Now(), settings.UpdateTime)
		}
		writeJSON(w, http.StatusOK, map[string]any{"settings": settings, "next_update": next})
	case http.MethodPut:
		if !validLocalJSONRequest(r) {
			writeError(w, http.StatusForbidden, "local same-origin JSON request required")
			return
		}
		var settings dnsblocklist.RefreshSettings
		if err := decodeStrictJSON(w, r, &settings, 4096); err != nil {
			writeError(w, http.StatusBadRequest, "invalid DNS blocklist refresh settings")
			return
		}
		saved, err := s.blocklists.SaveSettings(settings)
		if err != nil {
			writeError(w, http.StatusUnprocessableEntity, err.Error())
			return
		}
		s.wakeBlocklistScheduler()
		s.recordEvent("info", "dns", "blocklist_schedule_saved", "DNS blocklist automatic refresh settings saved")
		next := any(nil)
		if saved.AutoUpdate {
			next = nextDailyBlocklistUpdate(time.Now(), saved.UpdateTime)
		}
		writeJSON(w, http.StatusOK, map[string]any{"settings": saved, "next_update": next})
	default:
		methodNotAllowed(w)
	}
}

func (s *Server) dnsBlocklists(w http.ResponseWriter, r *http.Request) {
	if s.blocklists == nil {
		message := "DNS blocklist state is not configured"
		if s.blocklistsErr != "" {
			message = "DNS blocklist state could not be loaded"
		}
		writeError(w, http.StatusServiceUnavailable, message)
		return
	}
	switch r.Method {
	case http.MethodGet:
		if !validLocalReadRequest(r) {
			writeError(w, http.StatusForbidden, "local request required")
			return
		}
		writeJSON(w, http.StatusOK, s.blocklists.Snapshot())
	case http.MethodPost:
		if !validLocalJSONRequest(r) {
			writeError(w, http.StatusForbidden, "local same-origin JSON request required")
			return
		}
		var source dnsblocklist.Source
		if err := decodeStrictJSON(w, r, &source, 32<<10); err != nil {
			writeError(w, http.StatusBadRequest, "invalid DNS blocklist source")
			return
		}
		saved, err := s.blocklists.SaveSource(source)
		if err != nil {
			writeError(w, http.StatusUnprocessableEntity, err.Error())
			return
		}
		s.applyDNSBlocklists()
		s.recordEvent("info", "dns", "blocklist_source_saved", "DNS blocklist source saved")
		writeJSON(w, http.StatusCreated, map[string]any{"source": saved, "snapshot": s.blocklists.Snapshot()})
	default:
		methodNotAllowed(w)
	}
}

func (s *Server) dnsBlocklistItem(w http.ResponseWriter, r *http.Request) {
	if s.blocklists == nil {
		message := "DNS blocklist state is not configured"
		if s.blocklistsErr != "" {
			message = "DNS blocklist state could not be loaded"
		}
		writeError(w, http.StatusServiceUnavailable, message)
		return
	}
	path := strings.Trim(strings.TrimPrefix(r.URL.Path, "/v1/dns/blocklists/"), "/")
	parts := strings.Split(path, "/")
	if path == "" || len(parts) == 0 || parts[0] == "" {
		writeError(w, http.StatusNotFound, "DNS blocklist source not found")
		return
	}
	if len(parts) == 1 && parts[0] == "_refresh" {
		if r.Method != http.MethodPost {
			methodNotAllowed(w)
			return
		}
		if !validLocalJSONRequest(r) {
			writeError(w, http.StatusForbidden, "local same-origin JSON request required")
			return
		}
		failures := s.blocklists.RefreshAll(r.Context())
		s.applyDNSBlocklists()
		messages := make([]string, 0, len(failures))
		for _, err := range failures {
			messages = append(messages, err.Error())
		}
		if len(failures) > 0 {
			s.recordEvent("warning", "dns", "blocklist_refresh_all_partial", "One or more DNS blocklist sources failed to refresh; last known-good data was retained")
		} else {
			s.recordEvent("info", "dns", "blocklist_refresh_all", "DNS blocklist refresh completed")
		}
		writeJSON(w, http.StatusOK, map[string]any{"snapshot": s.blocklists.Snapshot(), "failures": messages})
		return
	}
	id := parts[0]
	if len(parts) == 1 {
		switch r.Method {
		case http.MethodGet:
			if !validLocalReadRequest(r) {
				writeError(w, http.StatusForbidden, "local request required")
				return
			}
			source, err := s.blocklists.Get(id)
			if err != nil {
				writeError(w, http.StatusNotFound, "DNS blocklist source not found")
				return
			}
			writeJSON(w, http.StatusOK, source)
		case http.MethodPut:
			if !validLocalJSONRequest(r) {
				writeError(w, http.StatusForbidden, "local same-origin JSON request required")
				return
			}
			var source dnsblocklist.Source
			if err := decodeStrictJSON(w, r, &source, 32<<10); err != nil {
				writeError(w, http.StatusBadRequest, "invalid DNS blocklist source")
				return
			}
			source.ID = id
			saved, err := s.blocklists.SaveSource(source)
			if err != nil {
				writeError(w, http.StatusUnprocessableEntity, err.Error())
				return
			}
			s.applyDNSBlocklists()
			writeJSON(w, http.StatusOK, map[string]any{"source": saved, "snapshot": s.blocklists.Snapshot()})
		case http.MethodPatch:
			if !validLocalJSONRequest(r) {
				writeError(w, http.StatusForbidden, "local same-origin JSON request required")
				return
			}
			var request struct {
				Enabled *bool `json:"enabled"`
			}
			if err := decodeStrictJSON(w, r, &request, 4096); err != nil || request.Enabled == nil {
				writeError(w, http.StatusBadRequest, "enabled boolean is required")
				return
			}
			source, err := s.blocklists.SetEnabled(id, *request.Enabled)
			if err != nil {
				writeError(w, http.StatusNotFound, "DNS blocklist source not found")
				return
			}
			s.applyDNSBlocklists()
			writeJSON(w, http.StatusOK, map[string]any{"source": source, "snapshot": s.blocklists.Snapshot()})
		case http.MethodDelete:
			if !validLocalReadRequest(r) {
				writeError(w, http.StatusForbidden, "local request required")
				return
			}
			if err := s.blocklists.Delete(id); err != nil {
				writeError(w, http.StatusNotFound, "DNS blocklist source not found")
				return
			}
			s.applyDNSBlocklists()
			s.recordEvent("warning", "dns", "blocklist_source_deleted", "DNS blocklist source deleted")
			writeJSON(w, http.StatusOK, s.blocklists.Snapshot())
		default:
			methodNotAllowed(w)
		}
		return
	}
	if len(parts) != 2 {
		writeError(w, http.StatusNotFound, "DNS blocklist action not found")
		return
	}
	action := parts[1]
	if action == "refresh" {
		if r.Method != http.MethodPost {
			methodNotAllowed(w)
			return
		}
		if !validLocalJSONRequest(r) {
			writeError(w, http.StatusForbidden, "local same-origin JSON request required")
			return
		}
		source, err := s.blocklists.Refresh(r.Context(), id)
		s.applyDNSBlocklists()
		if err != nil {
			s.recordEvent("warning", "dns", "blocklist_source_refresh_failed", "DNS blocklist source refresh failed; last known-good data was retained")
			writeJSON(w, http.StatusBadGateway, map[string]any{"source": source, "snapshot": s.blocklists.Snapshot(), "error": err.Error()})
			return
		}
		s.recordEvent("info", "dns", "blocklist_source_refreshed", "DNS blocklist source refreshed")
		writeJSON(w, http.StatusOK, map[string]any{"source": source, "snapshot": s.blocklists.Snapshot()})
		return
	}
	if action == "remove" || action == "restore" {
		if r.Method != http.MethodPost {
			methodNotAllowed(w)
			return
		}
		if !validLocalJSONRequest(r) {
			writeError(w, http.StatusForbidden, "local same-origin JSON request required")
			return
		}
		var request struct {
			Domain string `json:"domain"`
		}
		if err := decodeStrictJSON(w, r, &request, 4096); err != nil {
			writeError(w, http.StatusBadRequest, "invalid DNS blocklist domain")
			return
		}
		var err error
		if action == "remove" {
			err = s.blocklists.RemoveEntry(id, request.Domain)
		} else {
			err = s.blocklists.RestoreEntry(id, request.Domain)
		}
		if err != nil {
			writeError(w, http.StatusUnprocessableEntity, err.Error())
			return
		}
		s.applyDNSBlocklists()
		s.recordEvent("info", "dns", "blocklist_entry_updated", "DNS blocklist entry updated")
		writeJSON(w, http.StatusOK, s.blocklists.Snapshot())
		return
	}
	writeError(w, http.StatusNotFound, "DNS blocklist action not found")
}
