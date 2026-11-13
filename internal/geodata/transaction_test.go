package geodata

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestRecoverPendingAfterInterruptedSwitch(t *testing.T) {
	root := t.TempDir()
	s := &GenerationStore{Root: root}
	entry := append(field(1, []byte("RU")), field(2, field(1, []byte{127}))...)
	data := field(1, entry)
	check := func(string) error { return nil }
	first, err := s.Publish(data, data, check)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.writeTransaction(first, "stopped"); err != nil {
		t.Fatal(err)
	}
	second, err := s.Publish(data, data, check)
	if err != nil {
		t.Fatal(err)
	}
	if first == second {
		t.Fatal("generation did not change")
	}
	if err := s.writeTransaction(first, "switched"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Activate(context.Background(), data, data, check, &fakeRuntime{running: true, configured: true}, func(context.Context) error { return nil }); err == nil {
		t.Fatal("accepted update with pending transaction")
	}
	recovered, err := s.RecoverPending(check)
	if err != nil || !recovered {
		t.Fatalf("recovery: %v %v", recovered, err)
	}
	current, err := s.Current()
	if err != nil || current != first {
		t.Fatalf("recovery selected %q, wanted %q: %v", current, first, err)
	}
	if _, err := os.Stat(filepath.Join(root, "transaction.json")); !os.IsNotExist(err) {
		t.Fatalf("journal not removed: %v", err)
	}
	recovered, err = s.RecoverPending(check)
	if err != nil || recovered {
		t.Fatalf("repeated recovery: %v %v", recovered, err)
	}
}
func TestRecoverPendingRetainsJournalOnFailedPreflight(t *testing.T) {
	root := t.TempDir()
	s := &GenerationStore{Root: root}
	entry := append(field(1, []byte("RU")), field(2, field(1, []byte{127}))...)
	data := field(1, entry)
	first, err := s.Publish(data, data, func(string) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	if err := s.writeTransaction(first, "stopped"); err != nil {
		t.Fatal(err)
	}
	recovered, err := s.RecoverPending(func(string) error { return errors.New("bad xray") })
	if err == nil || !recovered {
		t.Fatal("accepted failed recovery")
	}
	if _, err := os.Stat(filepath.Join(root, "transaction.json")); err != nil {
		t.Fatalf("journal lost: %v", err)
	}
}
func TestRejectUnsafeTransactionJournal(t *testing.T) {
	root := t.TempDir()
	s := &GenerationStore{Root: root}
	if err := os.Symlink("/etc/passwd", filepath.Join(root, "transaction.json")); err != nil {
		t.Fatal(err)
	}
	if _, err := s.pendingTransaction(); err == nil {
		t.Fatal("accepted symlink transaction")
	}
}
