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

func TestManagedStartupRequiresExistingGeneration(t *testing.T) {
	root := filepath.Join(t.TempDir(), "missing")
	managed := filepath.Join(root, "current")
	if err := CheckAssetStartupReady(root, managed); err == nil {
		t.Fatal("managed startup allowed missing store")
	}
	if err := CheckAssetStartupReady(root, "/legacy/assets"); err != nil {
		t.Fatal("legacy startup blocked: ", err)
	}
	if err := os.Mkdir(root, 0700); err != nil {
		t.Fatal(err)
	}
	if err := CheckAssetStartupReady(root, managed); err == nil {
		t.Fatal("managed startup allowed empty store")
	}
}
func TestManagedStartupRejectsCorruptAndMissingAssets(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "generation-0")
	if err := os.Mkdir(dir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("generation-0", filepath.Join(root, "current")); err != nil {
		t.Fatal(err)
	}
	managed := filepath.Join(root, "current")
	if err := CheckAssetStartupReady(root, managed); err == nil {
		t.Fatal("allowed missing databases")
	}
	for _, name := range []string{"geoip.dat", "geosite.dat"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("corrupt"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	if err := CheckAssetStartupReady(root, managed); err == nil {
		t.Fatal("allowed corrupt databases")
	}
	if err := CheckAssetStartupReady(root, "/legacy/assets"); err != nil {
		t.Fatal("legacy startup blocked by managed corruption: ", err)
	}
}
