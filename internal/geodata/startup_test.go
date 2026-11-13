package geodata

import (
	"os"
	"path/filepath"
	"testing"
)

func TestRecoverStartupValidatesPreviousGeneration(t *testing.T) {
	root := t.TempDir()
	s := &GenerationStore{Root: root}
	entry := append(field(1, []byte("RU")), field(2, field(1, []byte{127}))...)
	data := field(1, entry)
	check := func(string) error { return nil }
	first, err := s.Publish(data, data, check)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Publish(data, data, check); err != nil {
		t.Fatal(err)
	}
	if err := s.writeTransaction(first, "switched"); err != nil {
		t.Fatal(err)
	}
	script := filepath.Join(root, "fake-xray")
	if err := os.WriteFile(script, []byte("#!/bin/sh\n[ \"$1\" = run ] && [ \"$2\" = -test ] && [ \"$3\" = -config ] || exit 7\nexit 0\n"), 0700); err != nil {
		t.Fatal(err)
	}
	config := filepath.Join(root, "config.json")
	if err := os.WriteFile(config, []byte("{}"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := RecoverStartup(root, script, config, "/other/assets"); err == nil {
		t.Fatal("accepted unrelated Xray asset path")
	}
	if _, err := os.Stat(filepath.Join(root, "transaction.json")); err != nil {
		t.Fatalf("lost recovery journal: %v", err)
	}
	restored, err := RecoverStartup(root, script, config, filepath.Join(root, "current"))
	if err != nil || !restored {
		t.Fatalf("recovery: %v %v", restored, err)
	}
	current, err := s.Current()
	if err != nil || current != first {
		t.Fatalf("wrong generation: %q %v", current, err)
	}
	if err := CheckStartupReady(root); err != nil {
		t.Fatal(err)
	}
}
func TestRecoverStartupNoStore(t *testing.T) {
	root := filepath.Join(t.TempDir(), "missing")
	recovered, err := RecoverStartup(root, "", "", "")
	if err != nil || recovered {
		t.Fatalf("legacy store: %v %v", recovered, err)
	}
}
