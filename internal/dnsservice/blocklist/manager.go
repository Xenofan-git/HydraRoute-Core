package blocklist

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"hydraroute.local/core/internal/parser"
)

const (
	maxSources       = 128
	maxRemoved       = 10000
	maxStateBytes    = 32 << 20
	maxSourceNameLen = 128
)

var (
	ErrSourceNotFound = errors.New("DNS blocklist source not found")
	sourceIDPattern   = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_-]{0,47}$`)
)

type RemovedEntry struct {
	Entry string    `json:"entry"`
	At    time.Time `json:"at"`
}

type Source struct {
	ID      string         `json:"id"`
	Name    string         `json:"name"`
	URL     string         `json:"url"`
	Enabled bool           `json:"enabled"`
	Removed []RemovedEntry `json:"removed,omitempty"`
	Count   int            `json:"count"`
	Allow   int            `json:"allow"`
	Dropped int            `json:"dropped"`
	Updated time.Time      `json:"updated,omitempty"`
	Error   string         `json:"error,omitempty"`
}

type Snapshot struct {
	Sources []Source `json:"sources"`
	Blocked []string `json:"blocked"`
	Allowed []string `json:"allowed"`
}

type Fetcher func(context.Context, string) (Result, error)

type persistedState struct {
	Version int               `json:"version"`
	Sources []Source          `json:"sources"`
	Results map[string]Result `json:"results"`
}

type Manager struct {
	mu      sync.RWMutex
	path    string
	sources []Source
	results map[string]Result
	fetcher Fetcher
	now     func() time.Time
}

func NewManager(path string, fetcher Fetcher) (*Manager, error) {
	if !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return nil, fmt.Errorf("blocklist state path must be absolute and clean")
	}
	if fetcher == nil {
		fetcher = Fetch
	}
	m := &Manager{
		path:    path,
		results: make(map[string]Result),
		fetcher: fetcher,
		now:     time.Now,
	}
	if err := m.load(); err != nil {
		return nil, err
	}
	return m, nil
}

func (m *Manager) List() []Source {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return cloneSources(m.sources)
}

func (m *Manager) Get(id string) (Source, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	index := m.sourceIndexLocked(id)
	if index < 0 {
		return Source{}, ErrSourceNotFound
	}
	return cloneSource(m.sources[index]), nil
}

func (m *Manager) SaveSource(input Source) (Source, error) {
	input.Name = strings.TrimSpace(input.Name)
	if input.Name == "" || len(input.Name) > maxSourceNameLen {
		return Source{}, fmt.Errorf("blocklist name must contain 1 to %d characters", maxSourceNameLen)
	}
	validURL, err := validateSourceURL(input.URL)
	if err != nil {
		return Source{}, err
	}
	input.URL = validURL
	if input.ID != "" && !sourceIDPattern.MatchString(input.ID) {
		return Source{}, fmt.Errorf("invalid blocklist source ID")
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	oldSources := cloneSources(m.sources)
	oldResults := cloneResults(m.results)

	if input.ID == "" {
		input.ID = nextSourceID(m.sources)
	}
	index := m.sourceIndexLocked(input.ID)
	if index >= 0 {
		previous := m.sources[index]
		input.Removed = cloneRemoved(previous.Removed)
		input.Count = previous.Count
		input.Allow = previous.Allow
		input.Dropped = previous.Dropped
		input.Updated = previous.Updated
		input.Error = previous.Error
		if previous.URL != input.URL {
			input.Error = "source URL changed; refresh required"
		}
		m.sources[index] = cloneSource(input)
	} else {
		if len(m.sources) >= maxSources {
			return Source{}, fmt.Errorf("maximum of %d DNS blocklist sources reached", maxSources)
		}
		input.Removed = nil
		input.Count, input.Allow, input.Dropped = 0, 0, 0
		input.Updated, input.Error = time.Time{}, ""
		m.sources = append(m.sources, cloneSource(input))
	}
	if err := m.persistLocked(); err != nil {
		m.sources, m.results = oldSources, oldResults
		return Source{}, err
	}
	return cloneSource(input), nil
}

func (m *Manager) SetEnabled(id string, enabled bool) (Source, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	index := m.sourceIndexLocked(id)
	if index < 0 {
		return Source{}, ErrSourceNotFound
	}
	old := cloneSource(m.sources[index])
	m.sources[index].Enabled = enabled
	if err := m.persistLocked(); err != nil {
		m.sources[index] = old
		return Source{}, err
	}
	return cloneSource(m.sources[index]), nil
}

func (m *Manager) Delete(id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	index := m.sourceIndexLocked(id)
	if index < 0 {
		return ErrSourceNotFound
	}
	oldSources := cloneSources(m.sources)
	oldResults := cloneResults(m.results)
	m.sources = append(m.sources[:index], m.sources[index+1:]...)
	delete(m.results, id)
	if err := m.persistLocked(); err != nil {
		m.sources, m.results = oldSources, oldResults
		return err
	}
	return nil
}

func (m *Manager) Refresh(ctx context.Context, id string) (Source, error) {
	m.mu.RLock()
	index := m.sourceIndexLocked(id)
	if index < 0 {
		m.mu.RUnlock()
		return Source{}, ErrSourceNotFound
	}
	source := cloneSource(m.sources[index])
	fetcher := m.fetcher
	m.mu.RUnlock()

	result, fetchErr := fetcher(ctx, source.URL)
	if fetchErr == nil {
		result = normalizeResult(result)
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	index = m.sourceIndexLocked(id)
	if index < 0 {
		return Source{}, ErrSourceNotFound
	}
	if m.sources[index].URL != source.URL {
		return cloneSource(m.sources[index]), fmt.Errorf("blocklist source changed during refresh; result discarded")
	}

	oldSource := cloneSource(m.sources[index])
	oldResult, hadResult := m.results[id]
	if fetchErr != nil {
		m.sources[index].Error = truncateError(fetchErr.Error(), 512)
		if err := m.persistLocked(); err != nil {
			m.sources[index] = oldSource
			return Source{}, err
		}
		return cloneSource(m.sources[index]), fmt.Errorf("refresh blocklist %q: %w", id, fetchErr)
	}

	m.results[id] = cloneResult(result)
	m.sources[index].Count = len(result.Blocked)
	m.sources[index].Allow = len(result.Allowed)
	m.sources[index].Dropped = result.Dropped
	m.sources[index].Updated = m.now()
	m.sources[index].Error = ""
	if err := m.persistLocked(); err != nil {
		m.sources[index] = oldSource
		if hadResult {
			m.results[id] = oldResult
		} else {
			delete(m.results, id)
		}
		return Source{}, err
	}
	return cloneSource(m.sources[index]), nil
}

func (m *Manager) RefreshAll(ctx context.Context) []error {
	sources := m.List()
	var failures []error
	for _, source := range sources {
		if !source.Enabled {
			continue
		}
		if _, err := m.Refresh(ctx, source.ID); err != nil {
			failures = append(failures, err)
		}
	}
	return failures
}

func (m *Manager) RemoveEntry(id, rawDomain string) error {
	domain, ok := normalizeDomain(rawDomain)
	if !ok {
		return fmt.Errorf("invalid DNS domain")
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	index := m.sourceIndexLocked(id)
	if index < 0 {
		return ErrSourceNotFound
	}
	result, exists := m.results[id]
	if !exists || (!contains(result.Blocked, domain) && !contains(result.Allowed, domain)) {
		return fmt.Errorf("domain is not present in blocklist source")
	}
	for _, removed := range m.sources[index].Removed {
		if removed.Entry == domain {
			return nil
		}
	}
	if len(m.sources[index].Removed) >= maxRemoved {
		return fmt.Errorf("maximum of %d removed entries reached", maxRemoved)
	}
	old := cloneSource(m.sources[index])
	m.sources[index].Removed = append(m.sources[index].Removed, RemovedEntry{Entry: domain, At: m.now()})
	if err := m.persistLocked(); err != nil {
		m.sources[index] = old
		return err
	}
	return nil
}

func (m *Manager) RestoreEntry(id, rawDomain string) error {
	domain, ok := normalizeDomain(rawDomain)
	if !ok {
		return fmt.Errorf("invalid DNS domain")
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	index := m.sourceIndexLocked(id)
	if index < 0 {
		return ErrSourceNotFound
	}
	removed := m.sources[index].Removed
	for i := range removed {
		if removed[i].Entry == domain {
			old := cloneSource(m.sources[index])
			m.sources[index].Removed = append(removed[:i], removed[i+1:]...)
			if err := m.persistLocked(); err != nil {
				m.sources[index] = old
				return err
			}
			return nil
		}
	}
	return fmt.Errorf("removed DNS domain not found")
}

func (m *Manager) Snapshot() Snapshot {
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := Snapshot{Sources: cloneSources(m.sources)}
	blocked := make(map[string]struct{})
	allowed := make(map[string]struct{})
	for _, source := range m.sources {
		if !source.Enabled {
			continue
		}
		result, ok := m.results[source.ID]
		if !ok {
			continue
		}
		removed := make(map[string]struct{}, len(source.Removed))
		for _, entry := range source.Removed {
			removed[entry.Entry] = struct{}{}
		}
		for _, domain := range result.Blocked {
			if _, skip := removed[domain]; !skip {
				blocked[domain] = struct{}{}
			}
		}
		for _, domain := range result.Allowed {
			if _, skip := removed[domain]; !skip {
				allowed[domain] = struct{}{}
			}
		}
	}
	out.Blocked = sortedKeys(blocked)
	out.Allowed = sortedKeys(allowed)
	return out
}

func (m *Manager) load() error {
	info, err := os.Lstat(m.path)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm()&0077 != 0 || info.Size() > maxStateBytes {
		return fmt.Errorf("blocklist state file must be a private regular file no larger than %d bytes", maxStateBytes)
	}
	data, err := os.ReadFile(m.path)
	if err != nil {
		return err
	}
	var state persistedState
	if err := json.Unmarshal(data, &state); err != nil {
		return fmt.Errorf("decode blocklist state: %w", err)
	}
	if state.Version != 1 {
		return fmt.Errorf("unsupported blocklist state version %d", state.Version)
	}
	if len(state.Sources) > maxSources {
		return fmt.Errorf("blocklist state exceeds source limit")
	}
	if state.Results == nil {
		state.Results = make(map[string]Result)
	}
	seen := make(map[string]struct{}, len(state.Sources))
	for i := range state.Sources {
		source := &state.Sources[i]
		if !sourceIDPattern.MatchString(source.ID) || strings.TrimSpace(source.Name) == "" || len(source.Name) > maxSourceNameLen {
			return fmt.Errorf("invalid blocklist source in state")
		}
		if _, exists := seen[source.ID]; exists {
			return fmt.Errorf("duplicate blocklist source ID %q", source.ID)
		}
		seen[source.ID] = struct{}{}
		validURL, err := validateSourceURL(source.URL)
		if err != nil {
			return fmt.Errorf("invalid URL for blocklist source %q: %w", source.ID, err)
		}
		source.URL = validURL
		if len(source.Removed) > maxRemoved {
			return fmt.Errorf("removed-entry limit exceeded for source %q", source.ID)
		}
		for j := range source.Removed {
			domain, ok := normalizeDomain(source.Removed[j].Entry)
			if !ok {
				return fmt.Errorf("invalid removed domain in source %q", source.ID)
			}
			source.Removed[j].Entry = domain
		}
	}
	for id, result := range state.Results {
		if _, exists := seen[id]; !exists {
			return fmt.Errorf("orphaned blocklist result for source %q", id)
		}
		state.Results[id] = normalizeResult(result)
	}
	m.sources = cloneSources(state.Sources)
	m.results = cloneResults(state.Results)
	return nil
}

func (m *Manager) persistLocked() error {
	dir := filepath.Dir(m.path)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	info, err := os.Lstat(dir)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm()&0077 != 0 {
		return fmt.Errorf("blocklist state directory must be private and must not be a symlink")
	}
	state := persistedState{Version: 1, Sources: cloneSources(m.sources), Results: cloneResults(m.results)}
	data, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return err
	}
	if len(data) > maxStateBytes {
		return fmt.Errorf("blocklist state exceeds %d bytes", maxStateBytes)
	}
	tmp, err := os.CreateTemp(dir, ".blocklists-*.tmp")
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
	if err := os.Rename(name, m.path); err != nil {
		return err
	}
	dirFile, err := os.Open(dir)
	if err != nil {
		return err
	}
	defer dirFile.Close()
	return dirFile.Sync()
}

func (m *Manager) sourceIndexLocked(id string) int {
	for i := range m.sources {
		if m.sources[i].ID == id {
			return i
		}
	}
	return -1
}

func validateSourceURL(raw string) (string, error) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || (strings.ToLower(u.Scheme) != "http" && strings.ToLower(u.Scheme) != "https") || u.Hostname() == "" || u.User != nil || u.Fragment != "" {
		return "", fmt.Errorf("blocklist URL must be an HTTP(S) URL without userinfo or fragment")
	}
	host := strings.ToLower(u.Hostname())
	if host == "localhost" || strings.HasSuffix(host, ".localhost") || strings.HasSuffix(host, ".local") || strings.HasSuffix(host, ".internal") {
		return "", fmt.Errorf("blocklist URL cannot target a local hostname")
	}
	if ip := net.ParseIP(host); ip != nil && !parser.IsPublicDialIP(ip) {
		return "", fmt.Errorf("blocklist URL cannot target a private or non-public address")
	}
	if port := u.Port(); port != "" {
		value, err := strconv.Atoi(port)
		if err != nil || value < 1 || value > 65535 {
			return "", fmt.Errorf("blocklist URL has an invalid port")
		}
	}
	u.Scheme = strings.ToLower(u.Scheme)
	return u.String(), nil
}

func normalizeResult(result Result) Result {
	result.Blocked = normalizeDomainList(result.Blocked, &result.Dropped)
	result.Allowed = normalizeDomainList(result.Allowed, &result.Dropped)
	if result.Dropped < 0 {
		result.Dropped = 0
	}
	return result
}

func normalizeDomainList(values []string, dropped *int) []string {
	set := make(map[string]struct{}, len(values))
	for _, raw := range values {
		domain, ok := normalizeDomain(raw)
		if !ok {
			(*dropped)++
			continue
		}
		set[domain] = struct{}{}
	}
	return sortedKeys(set)
}

func nextSourceID(sources []Source) string {
	maxID := 0
	for _, source := range sources {
		value, err := strconv.Atoi(source.ID)
		if err == nil && value > maxID {
			maxID = value
		}
	}
	return strconv.Itoa(maxID + 1)
}

func contains(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}

func truncateError(value string, max int) string {
	value = strings.TrimSpace(value)
	if len(value) > max {
		return value[:max]
	}
	return value
}

func cloneSource(source Source) Source {
	source.Removed = cloneRemoved(source.Removed)
	return source
}

func cloneSources(sources []Source) []Source {
	out := make([]Source, len(sources))
	for i := range sources {
		out[i] = cloneSource(sources[i])
	}
	return out
}

func cloneRemoved(values []RemovedEntry) []RemovedEntry {
	return append([]RemovedEntry(nil), values...)
}

func cloneResult(result Result) Result {
	result.Blocked = append([]string(nil), result.Blocked...)
	result.Allowed = append([]string(nil), result.Allowed...)
	return result
}

func cloneResults(values map[string]Result) map[string]Result {
	out := make(map[string]Result, len(values))
	for key, value := range values {
		out[key] = cloneResult(value)
	}
	return out
}

