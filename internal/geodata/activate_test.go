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
	pending, err := s.pendingTransaction()
	if err != nil || pending == nil || pending.Previous != baseline {
		t.Fatalf("lost recovery journal on ambiguous stop: %+v %v", pending, err)
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

// A failed control-API probe after switching generations must restore the
// original files and leave no recovery journal when the old runtime recovers.
func TestActivateFailedHealthProbeRestoresPreviousAssetsAndJournal(t *testing.T) {
	root := t.TempDir()
	store := &GenerationStore{Root: root}
	entry := append(field(1, []byte("RU")), field(2, field(1, []byte{127}))...)
	original := field(1, entry)
	next := append([]byte(nil), original...)
	next = append(next, field(1, entry)...)
	check := func(string) error { return nil }
	baseline, err := store.Publish(original, original, check)
	if err != nil {
		t.Fatal(err)
	}
	runtime := &fakeRuntime{running: true, configured: true}
	probes := 0
	probe := func(context.Context) error {
		probes++
		if probes == 1 {
			return errors.New("control API unreachable")
		}
		return nil
	}
	if _, err := store.Activate(context.Background(), next, next, check, runtime, probe); err == nil {
		t.Fatal("unhealthy candidate was accepted")
	}
	if probes != 2 || runtime.starts != 2 || runtime.stops != 2 || !runtime.running {
		t.Fatalf("runtime recovery incomplete: probes=%d runtime=%+v", probes, runtime)
	}
	current, err := store.Current()
	if err != nil || current != baseline {
		t.Fatalf("wrong generation after rollback: %s %v", current, err)
	}
	pending, err := store.pendingTransaction()
	if err != nil || pending != nil {
		t.Fatalf("journal not cleared after recovery: %+v %v", pending, err)
	}
	if err := CheckStartupReady(root); err != nil {
		t.Fatalf("recovered store blocks restart: %v", err)
	}
}

func TestActivateFailedRecoveryRetainsJournalForStartup(t *testing.T) {
	root := t.TempDir()
	store := &GenerationStore{Root: root}
	entry := append(field(1, []byte("RU")), field(2, field(1, []byte{127}))...)
	data := field(1, entry)
	check := func(string) error { return nil }
	baseline, err := store.Publish(data, data, check)
	if err != nil {
		t.Fatal(err)
	}
	runtime := &fakeRuntime{running: true, configured: true, failStartAt: 2}
	if _, err := store.Activate(context.Background(), data, data, check, runtime, func(context.Context) error { return errors.New("control API unreachable") }); err == nil {
		t.Fatal("accepted failed recovery")
	}
	current, err := store.Current()
	if err != nil || current != baseline {
		t.Fatalf("baseline not selected: %s %v", current, err)
	}
	pending, err := store.pendingTransaction()
	if err != nil || pending == nil {
		t.Fatalf("recovery journal lost: %+v %v", pending, err)
	}
	if err := CheckStartupReady(root); err == nil {
		t.Fatal("unsafe restart allowed with pending recovery")
	}
}
