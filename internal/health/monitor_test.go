package health

import (
	"testing"
	"time"
)

func TestSettingsValidation(t *testing.T) {
	if err := DefaultSettings().Validate(); err != nil {
		t.Fatal(err)
	}
	bad := DefaultSettings()
	bad.TimeoutSeconds = bad.IntervalSeconds
	if err := bad.Validate(); err == nil {
		t.Fatal("expected timeout/interval validation")
	}
}

func TestFailAndRecoveryThresholds(t *testing.T) {
	m := New(DefaultSettings())
	now := time.Now()
	checks := []Check{{Name: "xray", State: "healthy"}}
	if got := m.Apply(checks, Metrics{}, now).Checks[0].State; got != "healthy" {
		t.Fatalf("initial state=%s", got)
	}
	checks[0].State = "failed"
	if got := m.Apply(checks, Metrics{}, now).Checks[0].State; got != "checking" {
		t.Fatalf("first failure must not report healthy, got %s", got)
	}
	if got := m.Apply(checks, Metrics{}, now).Checks[0].State; got != "degraded" {
		t.Fatalf("second failure should degrade, got %s", got)
	}
	checks[0].State = "healthy"
	if got := m.Apply(checks, Metrics{}, now).Checks[0].State; got != "degraded" {
		t.Fatalf("first recovery should remain degraded, got %s", got)
	}
	if got := m.Apply(checks, Metrics{}, now).Checks[0].State; got != "healthy" {
		t.Fatalf("second recovery should restore healthy, got %s", got)
	}
}

func TestSnapshotCopiesChecks(t *testing.T) {
	m := New(DefaultSettings())
	m.Apply([]Check{{Name: "api", State: "healthy"}}, Metrics{}, time.Now())
	copy := m.Snapshot()
	copy.Checks[0].Name = "mutated"
	if m.Snapshot().Checks[0].Name != "api" {
		t.Fatal("snapshot exposed mutable internal slice")
	}
}

func TestFailureAfterHealthyNeverReportsHealthy(t *testing.T) {
	m := New(DefaultSettings())
	now := time.Now()
	m.Apply([]Check{{Name: "smartdns", State: "healthy"}}, Metrics{}, now)
	first := m.Apply([]Check{{Name: "smartdns", State: "failed"}}, Metrics{}, now)
	if first.Status != "warning" || first.Checks[0].State != "checking" {
		t.Fatalf("first failed probe falsely healthy: %+v", first)
	}
	second := m.Apply([]Check{{Name: "smartdns", State: "failed"}}, Metrics{}, now)
	if second.Status != "degraded" || second.Checks[0].State != "degraded" {
		t.Fatalf("second failed probe should degrade: %+v", second)
	}
}

func TestDegradedRemainsLatchedAcrossIntermittentRecovery(t *testing.T) {
	m := New(DefaultSettings())
	now := time.Now()
	check := func(state string) string {
		t.Helper()
		snapshot := m.Apply([]Check{{Name: "dns", State: state}}, Metrics{}, now)
		return snapshot.Checks[0].State
	}
	if got := check("failed"); got != "checking" {
		t.Fatalf("first failure=%s", got)
	}
	if got := check("failed"); got != "degraded" {
		t.Fatalf("second failure=%s", got)
	}
	if got := check("healthy"); got != "degraded" {
		t.Fatalf("first recovery=%s", got)
	}
	if got := check("failed"); got != "degraded" {
		t.Fatalf("intermittent failure must not downgrade to checking: %s", got)
	}
	if got := check("healthy"); got != "degraded" {
		t.Fatalf("recovery count must reset after failure: %s", got)
	}
	if got := check("healthy"); got != "healthy" {
		t.Fatalf("second consecutive recovery=%s", got)
	}
}

func TestDisabledComponentResetsFailureAndRecoveryCounters(t *testing.T) {
	m := New(DefaultSettings())
	now := time.Now()
	check := func(state string) string {
		t.Helper()
		return m.Apply([]Check{{Name: "dns", State: state}}, Metrics{}, now).Checks[0].State
	}
	if got := check("failed"); got != "checking" {
		t.Fatalf("initial failure=%s", got)
	}
	if got := check("disabled"); got != "disabled" {
		t.Fatalf("disabled state=%s", got)
	}
	if got := check("failed"); got != "checking" {
		t.Fatalf("stale failures survived disabling: %s", got)
	}
	if got := check("failed"); got != "degraded" {
		t.Fatalf("second failure=%s", got)
	}
	if got := check("healthy"); got != "degraded" {
		t.Fatalf("first recovery=%s", got)
	}
	if got := check("disabled"); got != "disabled" {
		t.Fatalf("disabled after degraded=%s", got)
	}
	if got := check("healthy"); got != "healthy" {
		t.Fatalf("stale degraded state survived disabling: %s", got)
	}
}

func TestHealthMonitorThresholdOneDegradesAndRecoversImmediately(t *testing.T) {
	settings := DefaultSettings()
	settings.FailThreshold = 1
	settings.RecoverThreshold = 1
	m := New(settings)
	now := time.Now()
	failed := m.Apply([]Check{{Name: "smartdns", State: "failed"}}, Metrics{}, now)
	if failed.Status != "degraded" || failed.Checks[0].State != "degraded" {
		t.Fatalf("failure with threshold one=%+v", failed)
	}
	healthy := m.Apply([]Check{{Name: "smartdns", State: "healthy"}}, Metrics{}, now)
	if healthy.Status != "healthy" || healthy.Checks[0].State != "healthy" {
		t.Fatalf("recovery with threshold one=%+v", healthy)
	}
}
