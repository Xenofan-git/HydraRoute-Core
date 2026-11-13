package geodata

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestGenerationPublishRollback(t *testing.T) {
	root := t.TempDir()
	s := &GenerationStore{Root: root}
	entry := append(field(1, []byte("RU")), field(2, field(1, []byte{127}))...)
	data := field(1, entry)
	check := func(path string) error {
		for _, name := range []string{"geoip.dat", "geosite.dat"} {
			if _, err := os.Stat(filepath.Join(path, name)); err != nil {
				return err
			}
		}
		return nil
	}
	first, err := s.Publish(data, data, check)
	if err != nil {
		t.Fatal(err)
	}
	current, err := s.Current()
	if err != nil || current != first {
		t.Fatalf("current=%q err=%v", current, err)
	}
	if _, err := s.Publish(data, data, func(string) error { return errors.New("xray rejected") }); err == nil {
		t.Fatal("accepted invalid runtime")
	}
	current, _ = s.Current()
	if current != first {
		t.Fatal("failed candidate replaced live generation")
	}
	second, err := s.Publish(data, data, check)
	if err != nil {
		t.Fatal(err)
	}
	if second == first {
		t.Fatal("duplicate generation")
	}
	if err := s.Rollback(first, check); err != nil {
		t.Fatal(err)
	}
	current, _ = s.Current()
	if current != first {
		t.Fatal("rollback did not restore first generation")
	}
	if err := s.Rollback("../evil", check); err == nil {
		t.Fatal("accepted path traversal")
	}
	if err := s.Rollback(second, func(string) error { return errors.New("bad") }); err == nil {
		t.Fatal("accepted failed rollback validation")
	}
	current, _ = s.Current()
	if current != first {
		t.Fatal("failed rollback changed current")
	}
}
func TestGenerationRejectsUnsafeRootAndLink(t *testing.T) {
	root := t.TempDir()
	s := &GenerationStore{Root: root}
	if err := os.Symlink("../../etc", filepath.Join(root, "current")); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Current(); err == nil {
		t.Fatal("accepted unsafe link")
	}
	link := filepath.Join(t.TempDir(), "link")
	if err := os.Symlink(root, link); err != nil {
		t.Fatal(err)
	}
	if _, err := (&GenerationStore{Root: link}).Current(); err == nil {
		t.Fatal("accepted symlink root")
	}
}

func TestGenerationRejectsDanglingCurrent(t *testing.T) {
	root := t.TempDir()
	s := &GenerationStore{Root: root}
	if err := os.Symlink("generation-999", filepath.Join(root, "current")); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Current(); err == nil {
		t.Fatal("accepted dangling current generation")
	}
}

func TestGenerationXrayPreflightFailureDoesNotActivate(t *testing.T) {
	root := t.TempDir()
	entry := append(field(1, []byte("RU")), field(2, field(1, []byte{127}))...)
	data := field(1, entry)
	s := &GenerationStore{Root: root}
	cfg := filepath.Join(root, "config.json")
	if err := os.WriteFile(cfg, []byte("{}"), 0600); err != nil {
		t.Fatal(err)
	}
	script := filepath.Join(root, "fake-xray")
	if err := os.WriteFile(script, []byte("#!/bin/sh\nexit 1\n"), 0700); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Publish(data, data, (XrayCheck{Binary: script, Config: cfg}).Check); err == nil {
		t.Fatal("failed Xray preflight must block publish")
	}
	current, err := s.Current()
	if err != nil || current != "" {
		t.Fatalf("failed preflight activated generation: %q %v", current, err)
	}
}
