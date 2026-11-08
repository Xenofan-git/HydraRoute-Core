package routeconfig

import (
	"os"
	"path/filepath"
	"testing"

	"hydraroute.local/core/internal/compiler"
)

func TestStoreRoundTripAndPrivatePermissions(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "state")
	store := NewStore(dir)
	if _, found, err := store.Load(); err != nil || found { t.Fatalf("initial load found=%v err=%v", found, err) }
	want := compiler.RoutingConfig{Enabled:true, Listen:"127.0.0.1", Port:16500, DefaultAction:"block",
		Rules:[]compiler.RoutingRule{{ID:"ru", Name:"Russian", Enabled:true, MatchType:"domain", Values:[]string{"geosite:category-ru"}, Action:"direct"}}}
	if err := store.Save(want); err != nil { t.Fatal(err) }
	got, found, err := store.Load()
	if err != nil || !found { t.Fatalf("load found=%v err=%v", found, err) }
	if got.Port != want.Port || got.DefaultAction != want.DefaultAction || len(got.Rules) != 1 || got.Rules[0].ID != "ru" { t.Fatalf("round trip mismatch: %+v", got) }
	info, err := os.Stat(filepath.Join(dir, "routing.json"))
	if err != nil { t.Fatal(err) }
	if info.Mode().Perm() != 0600 { t.Fatalf("routing file mode=%o, want 0600", info.Mode().Perm()) }
	if err := store.Delete(); err != nil { t.Fatal(err) }
	if _, found, err := store.Load(); err != nil || found { t.Fatalf("after delete found=%v err=%v", found, err) }
}

func TestStoreRejectsPermissiveDirectoryAndSymlinkFile(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "open")
	if err := os.Mkdir(dir, 0755); err != nil { t.Fatal(err) }
	if err := NewStore(dir).Save(compiler.RoutingConfig{}); err == nil { t.Fatal("expected permissive directory rejection") }
	private := filepath.Join(t.TempDir(), "private")
	if err := os.Mkdir(private, 0700); err != nil { t.Fatal(err) }
	target := filepath.Join(t.TempDir(), "target")
	if err := os.WriteFile(target, []byte("{}"), 0600); err != nil { t.Fatal(err) }
	if err := os.Symlink(target, filepath.Join(private, "routing.json")); err != nil { t.Fatal(err) }
	if _, _, err := NewStore(private).Load(); err == nil { t.Fatal("expected symlink rejection") }
}
