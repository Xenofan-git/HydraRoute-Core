package profiles

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestEncryptedProfileStoreRoundTripAndMetadataRedaction(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "private")
	store := NewStore(dir)
	p := Profile{ID: "foreign", SourceID: "providerA", Enabled: true, Port: 16140, Strategy: "roundRobin", OutboundInterface: "eth0", DPIBypass: DPIBypass{NoiseTraffic: true, TLSFragmentation: true}, SubscriptionURL: "https://provider.example/sub?token=SECRET-TOKEN"}
	if err := store.Save(p); err != nil {
		t.Fatal(err)
	}
	got, err := store.Get("foreign")
	if err != nil {
		t.Fatal(err)
	}
	if got.SubscriptionURL != p.SubscriptionURL || got.Port != p.Port || got.Strategy != p.Strategy || got.OutboundInterface != p.OutboundInterface || got.DPIBypass != p.DPIBypass {
		t.Fatalf("got=%+v", got)
	}
	blob, err := os.ReadFile(filepath.Join(dir, "foreign.profile"))
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(blob, []byte("SECRET-TOKEN")) || bytes.Contains(blob, []byte("provider.example")) {
		t.Fatal("profile secret appears in plaintext at rest")
	}
	keyInfo, err := os.Stat(filepath.Join(dir, "profiles.key"))
	if err != nil || keyInfo.Mode().Perm() != 0600 {
		t.Fatalf("key mode=%v err=%v", keyInfo, err)
	}
	fileInfo, err := os.Stat(filepath.Join(dir, "foreign.profile"))
	if err != nil || fileInfo.Mode().Perm() != 0600 {
		t.Fatalf("profile mode=%v err=%v", fileInfo, err)
	}
	list, err := store.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 || list[0].Input != "url" || list[0].Strategy != p.Strategy || list[0].OutboundInterface != p.OutboundInterface || list[0].DPIBypass != p.DPIBypass {
		t.Fatalf("metadata=%+v", list)
	}
	meta, _ := json.Marshal(list)
	if bytes.Contains(meta, []byte("SECRET-TOKEN")) || bytes.Contains(meta, []byte("provider.example")) {
		t.Fatal("metadata leaked subscription URL")
	}
}

func TestProfileStoreUpdateDeleteAndAuthentication(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "profiles")
	store := NewStore(dir)
	p := Profile{ID: "torrent", SourceID: "providerB", Enabled: true, Port: 16141, Links: []string{"vless://00000000-0000-4000-8000-000000000001@edge.example:443#node"}}
	if err := store.Save(p); err != nil {
		t.Fatal(err)
	}
	p.Port = 16142
	if err := store.Save(p); err != nil {
		t.Fatal(err)
	}
	got, err := store.Get("torrent")
	if err != nil || got.Port != 16142 {
		t.Fatalf("got=%+v err=%v", got, err)
	}
	blobPath := filepath.Join(dir, "torrent.profile")
	blob, _ := os.ReadFile(blobPath)
	blob[len(blob)-1] ^= 0xff
	if err := os.WriteFile(blobPath, blob, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Get("torrent"); err == nil {
		t.Fatal("expected GCM authentication failure")
	}
	if err := store.Delete("torrent"); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Get("torrent"); !os.IsNotExist(err) {
		t.Fatalf("expected not found, got %v", err)
	}
}

func TestProfileValidationRejectsAmbiguousInputsAndUnsafeIDs(t *testing.T) {
	cases := []Profile{
		{ID: "../escape", SourceID: "src", Links: []string{"vless://x"}},
		{ID: "ok", SourceID: "src", Links: []string{"vless://x"}, SubscriptionURL: "https://provider.example/sub"},
		{ID: "ok", SourceID: "src"},
		{ID: "ok", SourceID: "../src", Links: []string{"vless://x"}},
	}
	for _, p := range cases {
		if err := Validate(p); err == nil {
			t.Fatalf("expected invalid profile: %+v", p)
		}
	}
}

func TestProfileStoreRejectsPermissiveDirectoryAndSymlink(t *testing.T) {
	parent := t.TempDir()
	open := filepath.Join(parent, "open")
	if err := os.Mkdir(open, 0755); err != nil {
		t.Fatal(err)
	}
	if err := NewStore(open).Save(Profile{ID: "p", SourceID: "s", Links: []string{"vless://x"}}); err == nil {
		t.Fatal("expected permissive directory rejection")
	}
	target := filepath.Join(parent, "target")
	if err := os.Mkdir(target, 0700); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(parent, "link")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	if err := NewStore(link).Save(Profile{ID: "p", SourceID: "s", Links: []string{"vless://x"}}); err == nil {
		t.Fatal("expected symlink directory rejection")
	}
}

func TestAutoUpdateRequiresURLAndBoundedInterval(t *testing.T) {
	base := Profile{ID: "auto", SourceID: "source", Enabled: true, Port: 16000, Links: []string{"vless://00000000-0000-4000-8000-000000000001@edge.example:443#node"}, AutoUpdateEnabled: true, AutoUpdateInterval: 1440}
	if err := Validate(base); err == nil {
		t.Fatal("auto-update without URL should be rejected")
	}
	base.Links = nil
	base.SubscriptionURL = "https://provider.example/sub"
	base.AutoUpdateInterval = 4
	if err := Validate(base); err == nil {
		t.Fatal("interval under five minutes should be rejected")
	}
	base.AutoUpdateInterval = 10081
	if err := Validate(base); err == nil {
		t.Fatal("interval over seven days should be rejected")
	}
	base.AutoUpdateInterval = 1440
	if err := Validate(base); err != nil {
		t.Fatalf("valid per-profile schedule rejected: %v", err)
	}
}
