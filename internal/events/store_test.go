package events

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

func TestAppendPersistsAndReloadsPrivateJournal(t *testing.T) {
	path := filepath.Join(t.TempDir(), "events", "events.json")
	s := New(path)
	got, err := s.Append("info", "runtime", "started", "Xray process started")
	if err != nil {
		t.Fatal(err)
	}
	if got.ID != 1 {
		t.Fatalf("event ID=%d", got.ID)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0600 {
		t.Fatalf("journal mode=%o", info.Mode().Perm())
	}
	reloaded := New(path)
	items := reloaded.List(10, 0)
	if len(items) != 1 || items[0].Message != "Xray process started" {
		t.Fatalf("reloaded events=%+v", items)
	}
	second, err := reloaded.Append("warning", "dns", "stopped", "Resolver stopped")
	if err != nil || second.ID != 2 {
		t.Fatalf("second event=%+v err=%v", second, err)
	}
}

func TestJournalBoundsEntriesAndSanitizesMessages(t *testing.T) {
	s := New("")
	for i := 0; i < maxEvents+5; i++ {
		if _, err := s.Append("info", "test", "tick", fmt.Sprintf("event %d\nsecret", i)); err != nil {
			t.Fatal(err)
		}
	}
	items := s.List(maxEvents, 0)
	if len(items) != maxEvents || items[0].ID != 6 || items[len(items)-1].ID != 505 {
		t.Fatalf("unexpected retained range: len=%d first=%d last=%d", len(items), items[0].ID, items[len(items)-1].ID)
	}
	if items[0].Message != "event 5 secret" {
		t.Fatalf("message not sanitized: %q", items[0].Message)
	}
}

func TestSubscribersReceiveEventsAndCanCancel(t *testing.T) {
	s := New("")
	ch, cancel := s.Subscribe()
	if _, err := s.Append("error", "runtime", "exit", "Xray exited"); err != nil {
		t.Fatal(err)
	}
	select {
	case event := <-ch:
		if event.Type != "exit" {
			t.Fatalf("event=%+v", event)
		}
	default:
		t.Fatal("subscriber did not receive event")
	}
	cancel()
	if _, err := s.Append("info", "runtime", "restart", "Xray restarted"); err != nil {
		t.Fatal(err)
	}
}

func TestClearRemovesEventsButKeepsMonotonicIDs(t *testing.T) {
	s := New("")
	first, _ := s.Append("info", "api", "ready", "API ready")
	if err := s.Clear(); err != nil {
		t.Fatal(err)
	}
	if len(s.List(10, 0)) != 0 {
		t.Fatal("events were not cleared")
	}
	second, err := s.Append("info", "api", "ready", "API ready again")
	if err != nil || second.ID != first.ID+1 {
		t.Fatalf("IDs not monotonic: first=%d second=%d err=%v", first.ID, second.ID, err)
	}
}

func TestListAfterIDReturnsNextPageNotLatestPage(t *testing.T) {
	s := New("")
	for i := 0; i < 200; i++ {
		if _, err := s.Append("info", "test", "tick", fmt.Sprintf("event %d", i+1)); err != nil {
			t.Fatal(err)
		}
	}
	items := s.List(10, 1)
	if len(items) != 10 || items[0].ID != 2 || items[9].ID != 11 {
		t.Fatalf("after_id pagination returned wrong range: len=%d first=%d last=%d", len(items), items[0].ID, items[len(items)-1].ID)
	}
}

func TestSubscriberLimitIsBounded(t *testing.T) {
	s := New("")
	cancels := make([]func(), 0, maxSubscribers)
	for i := 0; i < maxSubscribers; i++ {
		ch, cancel := s.Subscribe()
		if ch == nil {
			t.Fatalf("subscriber %d was rejected", i)
		}
		cancels = append(cancels, cancel)
	}
	if ch, _ := s.Subscribe(); ch != nil {
		t.Fatal("subscriber limit was not enforced")
	}
	for _, cancel := range cancels {
		cancel()
	}
	if ch, cancel := s.Subscribe(); ch == nil {
		t.Fatal("subscriber slot was not released")
	} else {
		cancel()
	}
}

func TestAppendPersistenceFailureDoesNotPublishOrConsumeID(t *testing.T) {
	root := t.TempDir()
	blocked := filepath.Join(root, "not-a-directory")
	if err := os.WriteFile(blocked, []byte("x"), 0600); err != nil {
		t.Fatal(err)
	}
	s := New(filepath.Join(blocked, "events.json"))
	if _, err := s.Append("info", "test", "write", "must fail"); err == nil {
		t.Fatal("expected persistence failure")
	}
	if got := s.List(10, 0); len(got) != 0 {
		t.Fatalf("failed event was published in memory: %+v", got)
	}
	if s.nextID != 1 {
		t.Fatalf("failed append consumed ID: next=%d", s.nextID)
	}
}
