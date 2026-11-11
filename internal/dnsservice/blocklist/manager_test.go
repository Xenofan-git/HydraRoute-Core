package blocklist

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestCatalogMatchesHRNeoBuiltInPresets(t *testing.T) {
	presets := Catalog()
	if len(presets) != 7 {
		t.Fatalf("HRNeo preset count=%d, want 7", len(presets))
	}
	expectedURLs := map[string]string{
		"hagezi-light": "https://raw.githubusercontent.com/hagezi/dns-blocklists/main/wildcard/light-onlydomains.txt",
		"hagezi-multi": "https://raw.githubusercontent.com/hagezi/dns-blocklists/main/wildcard/multi-onlydomains.txt",
		"hagezi-pro":   "https://raw.githubusercontent.com/hagezi/dns-blocklists/main/wildcard/pro-onlydomains.txt",
		"oisd-small":   "https://small.oisd.nl/domainswild2",
		"oisd-big":     "https://big.oisd.nl/domainswild2",
		"adguard-dns":  "https://adguardteam.github.io/AdGuardSDNSFilter/Filters/filter.txt",
		"stevenblack":  "https://raw.githubusercontent.com/StevenBlack/hosts/master/hosts",
	}
	seen := make(map[string]struct{}, len(presets))
	for _, preset := range presets {
		if expectedURLs[preset.ID] != preset.URL {
			t.Fatalf("preset %q URL=%q, want %q", preset.ID, preset.URL, expectedURLs[preset.ID])
		}
		if preset.ID == "" || preset.Name == "" {
			t.Fatalf("incomplete preset: %+v", preset)
		}
		if _, exists := seen[preset.ID]; exists {
			t.Fatalf("duplicate preset ID %q", preset.ID)
		}
		seen[preset.ID] = struct{}{}
		if _, err := validateSourceURL(preset.URL); err != nil {
			t.Fatalf("preset %q has invalid URL: %v", preset.ID, err)
		}
	}
	if presets[0].ID != "hagezi-light" || presets[2].ID != "hagezi-pro" || presets[5].ID != "adguard-dns" || presets[6].ID != "stevenblack" {
		t.Fatalf("unexpected HRNeo preset ordering: %+v", presets)
	}
	presets[0].Name = "mutated"
	if Catalog()[0].Name != "HaGeZi Light" {
		t.Fatal("Catalog returned mutable shared preset storage")
	}
}

func TestManagerSerializesConcurrentRefreshes(t *testing.T) {
	firstStarted := make(chan struct{})
	secondStarted := make(chan struct{})
	releaseFirst := make(chan struct{})
	var calls atomic.Int32
	fetcher := func(context.Context, string) (Result, error) {
		switch calls.Add(1) {
		case 1:
			close(firstStarted)
			<-releaseFirst
		default:
			close(secondStarted)
		}
		return Result{Blocked: []string{"blocked.example.test"}}, nil
	}
	manager, err := NewManager(filepath.Join(t.TempDir(), "blocklists.json"), fetcher)
	if err != nil {
		t.Fatal(err)
	}
	first, err := manager.SaveSource(Source{Name: "First", URL: "https://lists.example/first.txt", Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	second, err := manager.SaveSource(Source{Name: "Second", URL: "https://lists.example/second.txt", Enabled: true})
	if err != nil {
		t.Fatal(err)
	}

	firstDone := make(chan error, 1)
	secondDone := make(chan error, 1)
	go func() {
		_, err := manager.Refresh(context.Background(), first.ID)
		firstDone <- err
	}()
	<-firstStarted
	go func() {
		_, err := manager.Refresh(context.Background(), second.ID)
		secondDone <- err
	}()

	select {
	case <-secondStarted:
		t.Fatal("second refresh entered the fetcher while the first refresh was still active")
	case <-time.After(30 * time.Millisecond):
	}
	close(releaseFirst)
	select {
	case <-secondStarted:
	case <-time.After(time.Second):
		t.Fatal("second refresh did not start after the first completed")
	}
	if err := <-firstDone; err != nil {
		t.Fatal(err)
	}
	if err := <-secondDone; err != nil {
		t.Fatal(err)
	}
}

func TestManagerCustomListsPersistAndJoinAggregateSnapshot(t *testing.T) {
	path := filepath.Join(t.TempDir(), "blocklists.json")
	manager, err := NewManager(path, nil)
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := manager.SaveCustomLists(
		[]string{"Example.COM.", "*.ads.example.test", "ads.example.test"},
		[]string{"safe.example.com", "SAFE.example.com."},
	)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(snapshot.CustomBlock, ",") != "ads.example.test,example.com" {
		t.Fatalf("custom block list not normalized/deduplicated: %v", snapshot.CustomBlock)
	}
	if strings.Join(snapshot.CustomAllow, ",") != "safe.example.com" {
		t.Fatalf("custom allow list not normalized/deduplicated: %v", snapshot.CustomAllow)
	}
	if !containsString(snapshot.Blocked, "example.com") || !containsString(snapshot.Allowed, "safe.example.com") {
		t.Fatalf("custom lists missing from aggregate snapshot: %+v", snapshot)
	}

	reloaded, err := NewManager(path, nil)
	if err != nil {
		t.Fatal(err)
	}
	restored := reloaded.Snapshot()
	if strings.Join(restored.CustomBlock, ",") != strings.Join(snapshot.CustomBlock, ",") ||
		strings.Join(restored.CustomAllow, ",") != strings.Join(snapshot.CustomAllow, ",") {
		t.Fatalf("custom lists did not survive restart: %+v", restored)
	}
	if _, err := reloaded.SaveCustomLists([]string{"bad..domain"}, nil); err == nil {
		t.Fatal("invalid custom domain was accepted")
	}
	if strings.Join(reloaded.Snapshot().CustomBlock, ",") != strings.Join(snapshot.CustomBlock, ",") {
		t.Fatal("invalid custom update changed the last good custom lists")
	}
}

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

func TestManagerRefreshSettingsDefaultDailyScheduleAndPersistence(t *testing.T) {
	path := filepath.Join(t.TempDir(), "blocklists.json")
	manager, err := NewManager(path, nil)
	if err != nil {
		t.Fatal(err)
	}
	defaults := manager.Settings()
	if defaults.AutoUpdate || defaults.UpdateTime != "04:30" || defaults.Iface != "" {
		t.Fatalf("unexpected HRNeo default refresh settings: %+v", defaults)
	}
	saved, err := manager.SaveSettings(RefreshSettings{AutoUpdate: false, UpdateTime: "4:05", Iface: "eth1"})
	if err != nil {
		t.Fatal(err)
	}
	if saved.AutoUpdate || saved.UpdateTime != "04:05" || saved.Iface != "eth1" {
		t.Fatalf("settings were not normalized: %+v", saved)
	}
	reloaded, err := NewManager(path, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got := reloaded.Settings(); got != saved {
		t.Fatalf("refresh settings were not persisted: got %+v want %+v", got, saved)
	}
}

func TestManagerRefreshSettingsRejectInvalidTimeAndInterface(t *testing.T) {
	manager, err := NewManager(filepath.Join(t.TempDir(), "blocklists.json"), nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, settings := range []RefreshSettings{
		{AutoUpdate: true, UpdateTime: "24:00"},
		{AutoUpdate: true, UpdateTime: "04:60"},
		{AutoUpdate: true, UpdateTime: "04:30", Iface: "interface-name-too-long"},
		{AutoUpdate: true, UpdateTime: "04:30", Iface: "eth0;reboot"},
	} {
		if _, err := manager.SaveSettings(settings); err == nil {
			t.Fatalf("invalid settings accepted: %+v", settings)
		}
	}
}

func TestManagerUsesSelectedInterfaceFetcher(t *testing.T) {
	path := filepath.Join(t.TempDir(), "blocklists.json")
	fetcher := func(context.Context, string) (Result, error) {
		return Result{Blocked: []string{"default.example.test"}}, nil
	}
	manager, err := NewManager(path, fetcher)
	if err != nil {
		t.Fatal(err)
	}
	called := ""
	manager.SetInterfaceFetcher(func(_ context.Context, _ string, iface string) (Result, error) {
		called = iface
		return Result{Blocked: []string{"bound.example.test"}}, nil
	})
	if _, err := manager.SaveSettings(RefreshSettings{AutoUpdate: true, UpdateTime: "04:30", Iface: "eth1"}); err != nil {
		t.Fatal(err)
	}
	source, err := manager.SaveSource(Source{Name: "Bound", URL: "https://lists.example.test/list", Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Refresh(context.Background(), source.ID); err != nil {
		t.Fatal(err)
	}
	if called != "eth1" {
		t.Fatalf("interface fetcher called with %q, want eth1", called)
	}
	if got := manager.Snapshot().Blocked; len(got) != 1 || got[0] != "bound.example.test" {
		t.Fatalf("selected interface fetch result not applied: %v", got)
	}
}
