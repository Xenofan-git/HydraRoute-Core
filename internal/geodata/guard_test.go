package geodata

import (
	"os"
	"path/filepath"
	"testing"
)

func TestStartupGuardAllowsLegacyMissingStore(t *testing.T) {
	if err := CheckStartupReady(filepath.Join(t.TempDir(), "missing")); err != nil {
		t.Fatal(err)
	}
}
func TestStartupGuardBlocksInterruptedTransaction(t *testing.T) {
	root := t.TempDir()
	s := &GenerationStore{Root: root}
	if err := s.writeTransaction("generation-1", "stopped"); err != nil {
		t.Fatal(err)
	}
	if err := CheckStartupReady(root); err == nil {
		t.Fatal("allowed pending transaction")
	}
}
func TestStartupGuardBlocksDanglingPointerSwitch(t *testing.T) {
	root := t.TempDir()
	if err := os.Symlink("generation-1", filepath.Join(root, ".current-next")); err != nil {
		t.Fatal(err)
	}
	if err := CheckStartupReady(root); err == nil {
		t.Fatal("allowed interrupted pointer switch")
	}
}
func TestStartupGuardRejectsUnsafeRoot(t *testing.T) {
	root := t.TempDir()
	if err := os.Chmod(root, 0777); err != nil {
		t.Fatal(err)
	}
	if err := CheckStartupReady(root); err == nil {
		t.Fatal("allowed writable generation root")
	}
}
