package events

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

const (
	maxEvents = 500
	maxBytes  = 1 << 20
)

type Event struct {
	ID        uint64    `json:"id"`
	Time      time.Time `json:"time"`
	Level     string    `json:"level"`
	Component string    `json:"component"`
	Type      string    `json:"type"`
	Message   string    `json:"message"`
}

type diskState struct {
	Version int     `json:"version"`
	NextID  uint64  `json:"next_id"`
	Events  []Event `json:"events"`
}

type Store struct {
	mu      sync.Mutex
	path    string
	nextID  uint64
	events  []Event
	subs    map[uint64]chan Event
	nextSub uint64
	loadErr error
}

func New(path string) *Store {
	s := &Store{path: path, nextID: 1, subs: make(map[uint64]chan Event)}
	if path != "" {
		s.loadErr = s.load()
	}
	return s
}

func (s *Store) load() error {
	info, err := os.Lstat(s.path)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm()&0077 != 0 || info.Size() > maxBytes {
		return fmt.Errorf("event journal is not a private regular file within size limit")
	}
	data, err := os.ReadFile(s.path)
	if err != nil {
		return err
	}
	var saved diskState
	if err := json.Unmarshal(data, &saved); err != nil || saved.Version != 1 {
		return fmt.Errorf("event journal has an invalid format")
	}
	if len(saved.Events) > maxEvents {
		return fmt.Errorf("event journal exceeds entry limit")
	}
	var maxID uint64
	for _, event := range saved.Events {
		if event.ID == 0 || event.ID <= maxID || event.Time.IsZero() ||
			(event.Level != "debug" && event.Level != "info" && event.Level != "warning" && event.Level != "error") ||
			event.Component == "" || event.Type == "" || event.Message == "" ||
			len(event.Component) > 48 || len(event.Type) > 64 || len(event.Message) > 512 {
			return fmt.Errorf("event journal contains an invalid record")
		}
		maxID = event.ID
	}
	s.events = append([]Event(nil), saved.Events...)
	s.nextID = saved.NextID
	if s.nextID <= maxID {
		s.nextID = maxID + 1
	}
	if s.nextID == 0 {
		s.nextID = 1
	}
	return nil
}

func clean(value string, limit int) string {
	value = strings.TrimSpace(strings.ReplaceAll(strings.ReplaceAll(value, "\r", " "), "\n", " "))
	if len(value) > limit {
		value = value[:limit]
	}
	return value
}

func (s *Store) Append(level, component, eventType, message string) (Event, error) {
	level = strings.ToLower(clean(level, 16))
	if level != "debug" && level != "info" && level != "warning" && level != "error" {
		return Event{}, fmt.Errorf("invalid event level")
	}
	component, eventType, message = clean(component, 48), clean(eventType, 64), clean(message, 512)
	if component == "" || eventType == "" || message == "" {
		return Event{}, fmt.Errorf("event component, type and message are required")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.loadErr != nil {
		return Event{}, s.loadErr
	}
	event := Event{ID: s.nextID, Time: time.Now().UTC(), Level: level, Component: component, Type: eventType, Message: message}
	s.nextID++
	s.events = append(s.events, event)
	if len(s.events) > maxEvents {
		s.events = append([]Event(nil), s.events[len(s.events)-maxEvents:]...)
	}
	if err := s.persistLocked(); err != nil {
		return event, err
	}
	for _, ch := range s.subs {
		select {
		case ch <- event:
		default:
		}
	}
	return event, nil
}

func (s *Store) List(limit int, afterID uint64) []Event {
	s.mu.Lock()
	defer s.mu.Unlock()
	if limit <= 0 || limit > maxEvents {
		limit = 100
	}
	result := make([]Event, 0, limit)
	start := len(s.events) - limit
	if start < 0 {
		start = 0
	}
	for _, event := range s.events[start:] {
		if event.ID > afterID {
			result = append(result, event)
		}
	}
	return result
}

func (s *Store) Subscribe() (<-chan Event, func()) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.nextSub++
	id := s.nextSub
	ch := make(chan Event, 32)
	s.subs[id] = ch
	cancel := func() {
		s.mu.Lock()
		if current, ok := s.subs[id]; ok {
			delete(s.subs, id)
			close(current)
		}
		s.mu.Unlock()
	}
	return ch, cancel
}

func (s *Store) Clear() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.events = nil
	s.loadErr = nil
	return s.persistLocked()
}

func (s *Store) persistLocked() error {
	if s.path == "" {
		return nil
	}
	dir := filepath.Dir(s.path)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	info, err := os.Lstat(dir)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm()&0077 != 0 {
		return fmt.Errorf("event journal directory must be private and not a symlink")
	}
	data, err := json.Marshal(diskState{Version: 1, NextID: s.nextID, Events: s.events})
	if err != nil {
		return err
	}
	if len(data) > maxBytes {
		return fmt.Errorf("event journal exceeds byte limit")
	}
	tmp, err := os.CreateTemp(dir, ".events-*.json")
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
	if err := os.Rename(name, s.path); err != nil {
		return err
	}
	d, err := os.Open(dir)
	if err != nil {
		return err
	}
	defer d.Close()
	return d.Sync()
}

func (s *Store) Error() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.loadErr
}
