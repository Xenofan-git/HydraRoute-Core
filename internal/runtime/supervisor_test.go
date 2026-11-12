package runtime

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func fakeXray(t *testing.T, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "fake-xray")
	if err := os.WriteFile(p, []byte("#!/bin/sh\n"+body+"\n"), 0700); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestPrepareValidatesAndWritesPrivateConfigWithoutStarting(t *testing.T) {
	binary := fakeXray(t, "if [ \"$1\" = run ] && [ \"$2\" = -test ]; then case \"$4\" in *.json) exit 0;; *) echo config extension missing >&2; exit 1;; esac; fi\nsleep 30")
	config := filepath.Join(t.TempDir(), "private", "xray.json")
	s, err := NewSupervisor(binary, config)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Prepare(context.Background(), []byte(`{"inbounds":[],"outbounds":[]}`)); err != nil {
		t.Fatal(err)
	}
	st := s.Status()
	if !st.Configured || st.Running {
		t.Fatalf("status=%+v", st)
	}
	info, err := os.Stat(config)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0600 {
		t.Fatalf("config mode=%o", info.Mode().Perm())
	}
}

func TestPrepareRejectsInvalidXrayConfigAndKeepsPrevious(t *testing.T) {
	binary := fakeXray(t, "if [ \"$2\" = -test ]; then echo invalid >&2; exit 1; fi")
	config := filepath.Join(t.TempDir(), "xray.json")
	if err := os.WriteFile(config, []byte("previous"), 0600); err != nil {
		t.Fatal(err)
	}
	s, err := NewSupervisor(binary, config)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Prepare(context.Background(), []byte(`{"valid":true}`)); err == nil {
		t.Fatal("expected Xray validation failure")
	}
	b, _ := os.ReadFile(config)
	if string(b) != "previous" {
		t.Fatalf("previous config overwritten: %q", b)
	}
}

func TestStartStopAndStatus(t *testing.T) {
	binary := fakeXray(t, "if [ \"$2\" = -test ]; then exit 0; fi\ntrap 'exit 0' TERM\nwhile :; do sleep 0.1; done")
	s, err := NewSupervisor(binary, filepath.Join(t.TempDir(), "private", "xray.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Prepare(context.Background(), []byte(`{"inbounds":[],"outbounds":[]}`)); err != nil {
		t.Fatal(err)
	}
	if err := s.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !s.Status().Running {
		t.Fatal("expected running")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := s.Stop(ctx); err != nil {
		t.Fatal(err)
	}
	if s.Status().Running {
		t.Fatal("still running")
	}
}

func TestNewSupervisorRequiresAbsoluteExecutable(t *testing.T) {
	if _, err := NewSupervisor("xray", "/tmp/config"); err == nil {
		t.Fatal("expected absolute path validation")
	}
	if _, err := NewSupervisor("/missing/xray", "/tmp/config"); err == nil {
		t.Fatal("expected missing binary error")
	}
}

func TestBoundedOutput(t *testing.T) {
	got := boundedOutput([]byte(strings.Repeat("x", 3000)))
	if len(got) != 2048 {
		t.Fatalf("length=%d", len(got))
	}
}

func TestPrepareRejectsPermissiveConfigDirectory(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "shared")
	if err := os.Mkdir(dir, 0755); err != nil {
		t.Fatal(err)
	}
	binary := fakeXray(t, "exit 0")
	s, err := NewSupervisor(binary, filepath.Join(dir, "xray.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Prepare(context.Background(), []byte(`{"inbounds":[],"outbounds":[]}`)); err == nil {
		t.Fatal("expected permissive config directory rejection")
	}
}

func TestRestoreValidatesExistingConfigWithoutStarting(t *testing.T) {
	binary := fakeXray(t, `if [ "$2" = -test ]; then exit 0; fi; sleep 30`)
	config := filepath.Join(t.TempDir(), "private", "xray.json")
	first, err := NewSupervisor(binary, config)
	if err != nil {
		t.Fatal(err)
	}
	if err := first.Prepare(context.Background(), []byte(`{"inbounds":[],"outbounds":[]}`)); err != nil {
		t.Fatal(err)
	}
	second, err := NewSupervisor(binary, config)
	if err != nil {
		t.Fatal(err)
	}
	if err := second.Restore(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !second.Status().Configured || second.Status().Running {
		t.Fatalf("status=%+v", second.Status())
	}
}

func TestRestoreRejectsTamperedOrPermissiveConfig(t *testing.T) {
	binary := fakeXray(t, `if [ "$2" = -test ]; then exit 0; fi`)
	dir := filepath.Join(t.TempDir(), "private")
	if err := os.Mkdir(dir, 0700); err != nil {
		t.Fatal(err)
	}
	config := filepath.Join(dir, "xray.json")
	if err := os.WriteFile(config, []byte(`{"valid":true}`), 0644); err != nil {
		t.Fatal(err)
	}
	s, err := NewSupervisor(binary, config)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Restore(context.Background()); err == nil {
		t.Fatal("expected permissive config rejection")
	}
	if s.Status().Configured {
		t.Fatal("unsafe config must not be restored")
	}
}



func TestSupervisorRecoversUnexpectedExitButHonorsManualStop(t *testing.T) {
	marker := filepath.Join(t.TempDir(), "first-start")
	body := `if [ "$2" = "-test" ]; then exit 0; fi
if [ ! -e "` + marker + `" ]; then touch "` + marker + `"; exit 1; fi
trap 'exit 0' TERM
while :; do sleep 0.1; done`
	binary := fakeXray(t, body)
	s, err := NewSupervisor(binary, filepath.Join(t.TempDir(), "private", "xray.json"))
	if err != nil { t.Fatal(err) }
	if err := s.Prepare(context.Background(), []byte(`{"inbounds":[],"outbounds":[]}`)); err != nil { t.Fatal(err) }
	if err := s.Start(context.Background()); err != nil { t.Fatal(err) }
	deadline := time.Now().Add(time.Second)
	for s.Status().Running && time.Now().Before(deadline) { time.Sleep(5 * time.Millisecond) }
	if !s.Status().Desired { t.Fatal("unexpected exit cleared desired-running state") }
	if err := s.Recover(context.Background()); err != nil { t.Fatal(err) }
	if !s.Status().Running { t.Fatal("Xray did not recover after unexpected exit") }
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := s.Stop(ctx); err != nil { t.Fatal(err) }
	if s.Status().Desired { t.Fatal("manual Stop did not clear desired-running state") }
	if err := s.Recover(context.Background()); err != nil { t.Fatal(err) }
	if s.Status().Running { t.Fatal("recovery overrode manual Stop") }
}
