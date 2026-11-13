package geodata

import (
	"context"
	"errors"
	"testing"
)

type fakeRuntime struct {
	running     bool
	configured  bool
	starts      int
	stops       int
	failStartAt int
	failStop    bool
}

func (f *fakeRuntime) Stop(context.Context) error {
	f.stops++
	if f.failStop {
		return errors.New("stop failed")
	}
	f.running = false
	return nil
}
func (f *fakeRuntime) Start(context.Context) error {
	f.starts++
	if f.starts == f.failStartAt {
		return errors.New("start failed")
	}
	f.running = true
	return nil
}
func (f *fakeRuntime) IsRunning() bool    { return f.running }
func (f *fakeRuntime) IsConfigured() bool { return f.configured }
func TestActivateRollbackOnRuntimeFailure(t *testing.T) {
	root := t.TempDir()
	s := &GenerationStore{Root: root}
	entry := append(field(1, []byte("RU")), field(2, field(1, []byte{127}))...)
	data := field(1, entry)
	check := func(string) error { return nil }
	baseline, err := s.Publish(data, data, check)
	if err != nil {
		t.Fatal(err)
	}
	f := &fakeRuntime{running: true, configured: true, failStartAt: 1}
	if _, err := s.Activate(context.Background(), data, data, check, f, func(context.Context) error { return nil }); err == nil {
		t.Fatal("expected failed activation")
	}
	current, _ := s.Current()
	if current != baseline {
		t.Fatalf("rollback failed: %q vs %q", current, baseline)
	}
	if !f.running || f.starts != 2 || f.stops != 2 {
		t.Fatalf("runtime not recovered: %+v", f)
	}
}
func TestActivateSuccessAndHealthFailure(t *testing.T) {
	root := t.TempDir()
	s := &GenerationStore{Root: root}
	entry := append(field(1, []byte("RU")), field(2, field(1, []byte{127}))...)
	data := field(1, entry)
	check := func(string) error { return nil }
	baseline, err := s.Publish(data, data, check)
	if err != nil {
		t.Fatal(err)
	}
	f := &fakeRuntime{running: true, configured: true}
	next, err := s.Activate(context.Background(), data, data, check, f, func(context.Context) error { return nil })
	if err != nil || next == baseline {
		t.Fatalf("activation failed: %q %v", next, err)
	}
	if _, err := s.Activate(context.Background(), data, data, check, f, func(context.Context) error { return errors.New("probe failed") }); err == nil {
		t.Fatal("health failure accepted")
	}
	current, _ := s.Current()
	if current != next {
		t.Fatal("failed health probe did not roll back")
	}
}
func TestActivateNoDowntimeOnPreflightFailure(t *testing.T) {
	root := t.TempDir()
	s := &GenerationStore{Root: root}
	entry := append(field(1, []byte("RU")), field(2, field(1, []byte{127}))...)
	data := field(1, entry)
	check := func(string) error { return nil }
	baseline, err := s.Publish(data, data, check)
	if err != nil {
		t.Fatal(err)
	}
	f := &fakeRuntime{running: true, configured: true}
	if _, err := s.Activate(context.Background(), data, data, func(string) error { return errors.New("invalid") }, f, func(context.Context) error { return nil }); err == nil {
		t.Fatal("accepted preflight failure")
	}
	current, _ := s.Current()
	if current != baseline || f.stops != 0 {
		t.Fatal("preflight failure disrupted running Xray")
	}
}

func TestActivateStopFailureKeepsOldGeneration(t *testing.T) {
	root := t.TempDir()
	s := &GenerationStore{Root: root}
	entry := append(field(1, []byte("RU")), field(2, field(1, []byte{127}))...)
	data := field(1, entry)
	check := func(string) error { return nil }
	baseline, err := s.Publish(data, data, check)
	if err != nil {
		t.Fatal(err)
	}
	f := &fakeRuntime{running: true, configured: true, failStop: true}
	if _, err := s.Activate(context.Background(), data, data, check, f, func(context.Context) error { return nil }); err == nil {
		t.Fatal("accepted failed stop")
	}
	current, _ := s.Current()
	if current != baseline || f.starts != 0 {
		t.Fatalf("failed stop changed generation or restarted runtime: %q %+v", current, f)
	}
}

func TestActivateRequiresBootstrappedStore(t *testing.T) {
	s := &GenerationStore{Root: t.TempDir()}
	f := &fakeRuntime{running: true, configured: true}
	if _, err := s.Activate(context.Background(), []byte("bad"), []byte("bad"), func(string) error { return nil }, f, func(context.Context) error { return nil }); err == nil {
		t.Fatal("accepted missing baseline")
	}
	if f.stops != 0 {
		t.Fatal("stopped runtime without baseline")
	}
}
