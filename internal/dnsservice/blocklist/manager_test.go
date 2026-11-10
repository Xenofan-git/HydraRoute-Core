package blocklist

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestManagerRefreshPreservesLastKnownGoodOnFailure(t *testing.T) {
	path := filepath.Join(t.TempDir(), "dns", "blocklists.json")
	fail := false
	fetcher := func(context.Context, string) (Result, error) {
		if fail {
			return Result{}, errors.New("upstream unavailable")
		}
		return Result{
			Blocked: []string{"Ads.Example.com"},
			Allowed: []string{"safe.example.com"},
			Lines:   2,
			Parsed:  2,
		}, nil
	}
	manager, err := NewManager(path, fetcher)
	if err != nil {
		t.Fatal(err)
	}
	source, err := manager.SaveSource(Source{Name: "Ads", URL: "https://lists.example/ads.txt", Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	updated, err := manager.Refresh(context.Background(), source.ID)
	if err != nil {
		t.Fatal(err)
	}
	if updated.Count != 1 || updated.Allow != 1 || updated.Updated.IsZero() || updated.Error != "" {
		t.Fatalf("unexpected source after refresh: %+v", updated)
	}
	fail = true
	failed, err := manager.Refresh(context.Background(), source.ID)
	if err == nil || !strings.Contains(failed.Error, "upstream unavailable") {
		t.Fatalf("expected recorded refresh error, source=%+v err=%v", failed, err)
	}
	if !failed.Updated.Equal(updated.Updated) {
		t.Fatalf("failed refresh changed last-success timestamp: %v -> %v", updated.Updated, failed.Updated)
	}
	snapshot := manager.Snapshot()
	if strings.Join(snapshot.Blocked, ",") != "ads.example.com" || strings.Join(snapshot.Allowed, ",") != "safe.example.com" {
		t.Fatalf("failed refresh lost last-known-good domains: %+v", snapshot)
	}
	reloaded, err := NewManager(path, fetcher)
	if err != nil {
		t.Fatal(err)
	}
	reloadedSnapshot := reloaded.Snapshot()
	if strings.Join(reloadedSnapshot.Blocked, ",") != "ads.example.com" || reloadedSnapshot.Sources[0].Error == "" {
		t.Fatalf("last-known-good state was not persisted: %+v", reloadedSnapshot)
	}
}

func TestManagerRemoveRestoreToggleAndDelete(t *testing.T) {
	path := filepath.Join(t.TempDir(), "blocklists.json")
	fetcher := func(context.Context, string) (Result, error) {
		return Result{Blocked: []string{"ads.example.com"}, Allowed: []string{"safe.example.com"}}, nil
	}
	manager, err := NewManager(path, fetcher)
	if err != nil {
		t.Fatal(err)
	}
	source, err := manager.SaveSource(Source{Name: "Ads", URL: "https://lists.example/ads.txt", Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Refresh(context.Background(), source.ID); err != nil {
		t.Fatal(err)
	}
	if err := manager.RemoveEntry(source.ID, "ADS.EXAMPLE.COM."); err != nil {
		t.Fatal(err)
	}
	if got := manager.Snapshot().Blocked; len(got) != 0 {
		t.Fatalf("removed domain is still active: %v", got)
	}
	if err := manager.RestoreEntry(source.ID, "ads.example.com"); err != nil {
		t.Fatal(err)
	}
	if got := manager.Snapshot().Blocked; len(got) != 1 || got[0] != "ads.example.com" {
		t.Fatalf("restored domain is not active: %v", got)
	}
	if _, err := manager.SetEnabled(source.ID, false); err != nil {
		t.Fatal(err)
	}
	if got := manager.Snapshot().Blocked; len(got) != 0 {
		t.Fatalf("disabled source still contributes domains: %v", got)
	}
	if err := manager.Delete(source.ID); err != nil {
		t.Fatal(err)
	}
	if got := manager.List(); len(got) != 0 {
		t.Fatalf("deleted source remains: %+v", got)
	}
}

func TestManagerRemovedEntriesOnlySuppressBlockedDomains(t *testing.T) {
	path := filepath.Join(t.TempDir(), "blocklists.json")
	fetcher := func(context.Context, string) (Result, error) {
		return Result{
			Blocked: []string{"ads.example.com"},
			Allowed: []string{"safe.example.com"},
		}, nil
	}
	manager, err := NewManager(path, fetcher)
	if err != nil {
		t.Fatal(err)
	}
	source, err := manager.SaveSource(Source{Name: "Ads", URL: "https://lists.example/ads.txt", Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Refresh(context.Background(), source.ID); err != nil {
		t.Fatal(err)
	}
	if err := manager.RemoveEntry(source.ID, "safe.example.com"); err == nil {
		t.Fatal("source allow exception was incorrectly removable as a blocked entry")
	}
	if err := manager.RemoveEntry(source.ID, "ads.example.com"); err != nil {
		t.Fatal(err)
	}
	snapshot := manager.Snapshot()
	if len(snapshot.Blocked) != 0 {
		t.Fatalf("removed blocked domain is still active: %v", snapshot.Blocked)
	}
	if len(snapshot.Allowed) != 1 || snapshot.Allowed[0] != "safe.example.com" {
		t.Fatalf("removing a blocked entry changed source allow exceptions: %v", snapshot.Allowed)
	}
}

func TestManagerReportsRemovedEntriesMissingFromRefreshedSource(t *testing.T) {
	path := filepath.Join(t.TempDir(), "blocklists.json")
	refresh := 0
	fetcher := func(context.Context, string) (Result, error) {
		refresh++
		if refresh == 1 {
			return Result{Blocked: []string{"old-ads.example.com"}, Allowed: []string{"safe.example.com"}}, nil
		}
		return Result{Blocked: []string{"new-ads.example.com"}, Allowed: []string{"safe.example.com"}}, nil
	}
	manager, err := NewManager(path, fetcher)
	if err != nil {
		t.Fatal(err)
	}
	source, err := manager.SaveSource(Source{Name: "Ads", URL: "https://lists.example/ads.txt", Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Refresh(context.Background(), source.ID); err != nil {
		t.Fatal(err)
	}
	if err := manager.RemoveEntry(source.ID, "old-ads.example.com"); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Refresh(context.Background(), source.ID); err != nil {
		t.Fatal(err)
	}
	snapshot := manager.Snapshot()
	missing := snapshot.Missing[source.ID]
	if len(missing) != 1 || missing[0] != "old-ads.example.com" {
		t.Fatalf("removed entry missing from source was not reported: %v", missing)
	}
	if len(snapshot.Allowed) != 1 || snapshot.Allowed[0] != "safe.example.com" {
		t.Fatalf("source allow exception changed after refresh: %v", snapshot.Allowed)
	}
	if _, err := manager.SetEnabled(source.ID, false); err != nil {
		t.Fatal(err)
	}
	disabled := manager.Snapshot()
	if len(disabled.Missing[source.ID]) != 1 || disabled.Missing[source.ID][0] != "old-ads.example.com" {
		t.Fatalf("disabled source lost its removed-entry status: %+v", disabled.Missing)
	}
	if len(disabled.Blocked) != 0 || len(disabled.Allowed) != 0 {
		t.Fatalf("disabled source still contributes active domains: blocked=%v allowed=%v", disabled.Blocked, disabled.Allowed)
	}
}

func TestManagerRejectsUnsafeSourceURLs(t *testing.T) {
	manager, err := NewManager(filepath.Join(t.TempDir(), "blocklists.json"), nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, rawURL := range []string{
		"ftp://lists.example/list.txt",
		"http://127.0.0.1/list.txt",
		"https://localhost/list.txt",
		"http://192.168.1.1/list.txt",
		"https://lists.example/list.txt#fragment",
		"https://user:pass@lists.example/list.txt",
	} {
		t.Run(rawURL, func(t *testing.T) {
			if _, err := manager.SaveSource(Source{Name: "unsafe", URL: rawURL}); err == nil {
				t.Fatalf("unsafe source URL was accepted: %s", rawURL)
			}
		})
	}
}

func TestManagerStateFileIsPrivateAndRejectsLoosePermissions(t *testing.T) {
	path := filepath.Join(t.TempDir(), "blocklists.json")
	manager, err := NewManager(path, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := manager.SaveSource(Source{Name: "Ads", URL: "https://lists.example/ads.txt"}); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0600 {
		t.Fatalf("state file mode = %o, want 600", info.Mode().Perm())
	}
	if err := os.Chmod(path, 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := NewManager(path, nil); err == nil {
		t.Fatal("expected insecure state file permissions to be rejected")
	}
}

func TestManagerSourceURLChangePreservesDataButRequiresRefresh(t *testing.T) {
	path := filepath.Join(t.TempDir(), "blocklists.json")
	fetcher := func(context.Context, string) (Result, error) {
		return Result{Blocked: []string{"ads.example.com"}}, nil
	}
	manager, err := NewManager(path, fetcher)
	if err != nil {
		t.Fatal(err)
	}
	source, err := manager.SaveSource(Source{Name: "Ads", URL: "https://lists.example/one.txt", Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Refresh(context.Background(), source.ID); err != nil {
		t.Fatal(err)
	}
	source.URL = "https://lists.example/two.txt"
	updated, err := manager.SaveSource(source)
	if err != nil {
		t.Fatal(err)
	}
	if updated.Error == "" || len(manager.Snapshot().Blocked) != 1 {
		t.Fatalf("URL update should retain last-good data and mark it stale: %+v", updated)
	}
}

func TestManagerRefreshAllSkipsDisabledSources(t *testing.T) {
	path := filepath.Join(t.TempDir(), "blocklists.json")
	calls := 0
	fetcher := func(context.Context, string) (Result, error) {
		calls++
		return Result{Blocked: []string{"ads.example.com"}}, nil
	}
	manager, err := NewManager(path, fetcher)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := manager.SaveSource(Source{Name: "Enabled", URL: "https://lists.example/enabled.txt", Enabled: true}); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.SaveSource(Source{Name: "Disabled", URL: "https://lists.example/disabled.txt", Enabled: false}); err != nil {
		t.Fatal(err)
	}
	if failures := manager.RefreshAll(context.Background()); len(failures) != 0 {
		t.Fatalf("unexpected refresh failures: %v", failures)
	}
	if calls != 1 {
		t.Fatalf("fetcher called %d times, want 1", calls)
	}
}

func TestManagerRemovedEntryTimestampIsRecorded(t *testing.T) {
	path := filepath.Join(t.TempDir(), "blocklists.json")
	fetcher := func(context.Context, string) (Result, error) {
		return Result{Blocked: []string{"ads.example.com"}}, nil
	}
	manager, err := NewManager(path, fetcher)
	if err != nil {
		t.Fatal(err)
	}
	source, err := manager.SaveSource(Source{Name: "Ads", URL: "https://lists.example/ads.txt", Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Refresh(context.Background(), source.ID); err != nil {
		t.Fatal(err)
	}
	if err := manager.RemoveEntry(source.ID, "ads.example.com"); err != nil {
		t.Fatal(err)
	}
	got, err := manager.Get(source.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Removed) != 1 || got.Removed[0].At.IsZero() {
		t.Fatalf("removed entry timestamp missing: %+v", got.Removed)
	}
	if _, err := time.Parse(time.RFC3339Nano, got.Removed[0].At.Format(time.RFC3339Nano)); err != nil {
		t.Fatal(err)
	}
}
