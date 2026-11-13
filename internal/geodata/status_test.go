package geodata

import (
	"os"
	"path/filepath"
	"testing"
)

func TestInspectStoreReportsTransactionAndGeneration(t *testing.T) {
	root := t.TempDir()
	s := &GenerationStore{Root: root}
	entry := append(field(1, []byte("RU")), field(2, field(1, []byte{127}))...)
	data := field(1, entry)
	generation, err := s.Publish(data, data, func(string) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	status := InspectStore(root)
	if !status.Present || !status.Ready || status.Current != generation {
		t.Fatalf("ready status: %+v", status)
	}
	if err := s.writeTransaction(generation, "stopped"); err != nil {
		t.Fatal(err)
	}
	status = InspectStore(root)
	if !status.Pending || status.Ready || status.Phase != "stopped" {
		t.Fatalf("pending status: %+v", status)
	}
}
func TestInspectStoreRejectsUnsafeAndMissing(t *testing.T) {
	root := filepath.Join(t.TempDir(), "missing")
	if status := InspectStore(root); status.Present || status.Ready {
		t.Fatalf("missing: %+v", status)
	}
	if err := os.Mkdir(root, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("generation-123", filepath.Join(root, ".current-next")); err != nil {
		t.Fatal(err)
	}
	if status := InspectStore(root); status.Error == "" || status.Ready {
		t.Fatalf("interrupted: %+v", status)
	}
}
