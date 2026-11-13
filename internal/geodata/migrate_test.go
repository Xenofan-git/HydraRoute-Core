package geodata

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestBootstrapPreservesLegacyAssets(t *testing.T) {
	source := t.TempDir()
	root := t.TempDir()
	entry := append(field(1, []byte("RU")), field(2, field(1, []byte{127}))...)
	data := field(1, entry)
	for _, name := range []string{"geoip.dat", "geosite.dat"} {
		if err := os.WriteFile(filepath.Join(source, name), data, 0600); err != nil {
			t.Fatal(err)
		}
	}
	s := &GenerationStore{Root: root}
	if _, err := s.Bootstrap(source, func(string) error { return errors.New("xray failed") }); err == nil {
		t.Fatal("accepted failed preflight")
	}
	current, _ := s.Current()
	if current != "" {
		t.Fatal("failed preflight activated store")
	}
	generation, err := s.Bootstrap(source, func(string) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	if generation != "generation-0" {
		t.Fatal(generation)
	}
	if _, err := s.Bootstrap(source, func(string) error { return nil }); err == nil {
		t.Fatal("repeated migration")
	}
	for _, name := range []string{"geoip.dat", "geosite.dat"} {
		got, err := os.ReadFile(filepath.Join(source, name))
		if err != nil || string(got) != string(data) {
			t.Fatalf("legacy asset modified: %s", name)
		}
	}
}

func TestBootstrapRefusesToDropCustomAssets(t *testing.T) {
	source := t.TempDir()
	root := t.TempDir()
	entry := append(field(1, []byte("RU")), field(2, field(1, []byte{127}))...)
	data := field(1, entry)
	for _, name := range []string{"geoip.dat", "geosite.dat", "custom.dat"} {
		if err := os.WriteFile(filepath.Join(source, name), data, 0600); err != nil {
			t.Fatal(err)
		}
	}
	store := &GenerationStore{Root: root}
	if _, err := store.Bootstrap(source, func(string) error { return nil }); err == nil {
		t.Fatal("bootstrap silently dropped custom asset")
	}
	current, err := store.Current()
	if err != nil || current != "" {
		t.Fatalf("bootstrap modified current: %q %v", current, err)
	}
	if _, err := os.Stat(filepath.Join(source, "custom.dat")); err != nil {
		t.Fatal("custom asset lost: ", err)
	}
}
