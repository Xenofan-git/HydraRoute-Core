package health

import (
	"fmt"
	"sync"
	"time"
)

type Settings struct {
	Enabled          bool `json:"enabled"`
	IntervalSeconds  int  `json:"interval_seconds"`
	TimeoutSeconds   int  `json:"timeout_seconds"`
	FailThreshold    int  `json:"fail_threshold"`
	RecoverThreshold int  `json:"recover_threshold"`
}

func DefaultSettings() Settings {
	return Settings{Enabled: true, IntervalSeconds: 30, TimeoutSeconds: 4, FailThreshold: 2, RecoverThreshold: 2}
}

func (s Settings) Validate() error {
	if s.IntervalSeconds < 5 || s.IntervalSeconds > 3600 {
		return fmt.Errorf("health interval must be between 5 and 3600 seconds")
	}
	if s.TimeoutSeconds < 1 || s.TimeoutSeconds > 30 || s.TimeoutSeconds >= s.IntervalSeconds {
		return fmt.Errorf("health timeout must be between 1 and 30 seconds and less than the interval")
	}
	if s.FailThreshold < 1 || s.FailThreshold > 10 || s.RecoverThreshold < 1 || s.RecoverThreshold > 10 {
		return fmt.Errorf("health fail and recovery thresholds must be between 1 and 10")
	}
	return nil
}

type Check struct {
	Name    string `json:"name"`
	State   string `json:"state"`
	Message string `json:"message"`
}

type Metrics struct {
	DiskFreeBytes    uint64  `json:"disk_free_bytes,omitempty"`
	DiskTotalBytes   uint64  `json:"disk_total_bytes,omitempty"`
	MemoryFreeBytes  uint64  `json:"memory_available_bytes,omitempty"`
	MemoryTotalBytes uint64  `json:"memory_total_bytes,omitempty"`
	Load1            float64 `json:"load_1m,omitempty"`
	UptimeSeconds    float64 `json:"uptime_seconds,omitempty"`
}

type Snapshot struct {
	Status    string    `json:"status"`
	CheckedAt time.Time `json:"checked_at"`
	Checks    []Check   `json:"checks"`
	Metrics   Metrics   `json:"metrics"`
}

type Monitor struct {
	mu       sync.Mutex
	settings Settings
	last     Snapshot
	states   map[string]string
	failures map[string]int
	recovery map[string]int
}

func New(settings Settings) *Monitor {
	if settings.Validate() != nil {
		settings = DefaultSettings()
	}
	return &Monitor{settings: settings, states: make(map[string]string), failures: make(map[string]int), recovery: make(map[string]int)}
}

func (m *Monitor) Settings() Settings {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.settings
}

func (m *Monitor) Configure(settings Settings) error {
	if err := settings.Validate(); err != nil {
		return err
	}
	m.mu.Lock()
	m.settings = settings
	m.mu.Unlock()
	return nil
}

func (m *Monitor) Snapshot() Snapshot {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := m.last
	out.Checks = append([]Check(nil), m.last.Checks...)
	return out
}

func (m *Monitor) Apply(checks []Check, metrics Metrics, at time.Time) Snapshot {
	m.mu.Lock()
	defer m.mu.Unlock()
	result := Snapshot{CheckedAt: at.UTC(), Metrics: metrics, Checks: make([]Check, 0, len(checks))}
	warning, degraded := false, false
	for _, check := range checks {
		state := check.State
		switch state {
		case "failed":
			m.failures[check.Name]++
			m.recovery[check.Name] = 0
			if m.failures[check.Name] >= m.settings.FailThreshold {
				state = "degraded"
			} else {
				// A failing probe must never be reported as healthy, even
				// while waiting for the configured failure threshold.
				state = "checking"
			}
		case "healthy":
			m.failures[check.Name] = 0
			if m.states[check.Name] == "degraded" {
				m.recovery[check.Name]++
				if m.recovery[check.Name] < m.settings.RecoverThreshold {
					state = "degraded"
				} else {
					m.recovery[check.Name] = 0
				}
			} else {
				m.recovery[check.Name] = 0
			}
		case "warning", "disabled", "not_configured", "checking", "degraded":
			m.failures[check.Name] = 0
			m.recovery[check.Name] = 0
		default:
			state = "warning"
		}
		m.states[check.Name] = state
		check.State = state
		result.Checks = append(result.Checks, check)
		switch state {
		case "degraded":
			degraded = true
		case "warning", "checking", "not_configured":
			warning = true
		}
	}
	result.Status = "healthy"
	if warning {
		result.Status = "warning"
	}
	if degraded {
		result.Status = "degraded"
	}
	m.last = result
	return result
}
