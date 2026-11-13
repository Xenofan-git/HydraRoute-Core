package geodata

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestOperationLockRejectsConcurrentActivationAndRecovery(t *testing.T) {
	root := t.TempDir()
	s := &GenerationStore{Root: root}
	unlock, err := s.lockOperation()
	if err != nil {
		t.Fatal(err)
	}
	other := &GenerationStore{Root: root}
	if _, err := other.lockOperation(); err == nil {
		t.Fatal("second updater acquired lock")
	}
	if _, err := other.RecoverPending(func(string) error { return nil }); err == nil {
		t.Fatal("recovery bypassed updater lock")
	}
	if _, err := other.Activate(context.Background(), nil, nil, func(string) error { return nil }, &fakeRuntime{running: true, configured: true}, func(context.Context) error { return nil }); err == nil {
		t.Fatal("activation bypassed updater lock")
	}
	unlock()
	release, err := other.lockOperation()
	if err != nil {
		t.Fatalf("lock not released: %v", err)
	}
	release()
}
func TestOperationLockRejectsSymlink(t *testing.T) {
	root := t.TempDir()
	s := &GenerationStore{Root: root}
	if err := os.Symlink(filepath.Join(root, "elsewhere"), filepath.Join(root, ".operation.lock")); err != nil {
		t.Fatal(err)
	}
	if _, err := s.lockOperation(); err == nil {
		t.Fatal("accepted symlink lock")
	}
}
