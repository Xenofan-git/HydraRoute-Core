package state

import (
	"os"
	"path/filepath"
	"testing"

	"hydraroute.local/core/internal/parser"
)

func TestSaveLoadStateRoundTripPrivateAndDeterministic(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "private", "state.json")
	input := State{Version: CurrentVersion, Profiles: []Profile{
		{ID: "torrent", SourceID: "torrent-sub", PickMode: "manual", Picked: []parser.ServerKey{{Source: "torrent-sub", Name: "B"}, {Source: "torrent-sub", Name: "A"}}, Excluded: []parser.ServerKey{{Source: "torrent-sub", Name: "C"}}},
		{ID: "foreign", SourceID: "foreign-sub", PickMode: "manual", Picked: []parser.ServerKey{{Source: "foreign-sub", Name: "node"}}},
	}}
	if err := Save(path, input); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0600 {
		t.Fatalf("mode=%o", info.Mode().Perm())
	}
	got, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Profiles) != 2 || got.Profiles[0].ID != "foreign" || got.Profiles[1].Picked[0].Name != "A" {
		t.Fatalf("state=%#v", got)
	}
}

func TestValidateRejectsSecretsAndInvalidSelectionState(t *testing.T) {
	cases := []State{
		{Version: CurrentVersion, Profiles: []Profile{{ID: "p", SourceID: "https://user:token@example.com/sub"}}},
		{Version: CurrentVersion, Profiles: []Profile{{ID: "p", SourceID: "src", Picked: []parser.ServerKey{{Source: "other", Name: "n"}}}}},
		{Version: CurrentVersion, Profiles: []Profile{{ID: "p", SourceID: "src", Picked: []parser.ServerKey{{Source: "src", Name: "n"}}, Excluded: []parser.ServerKey{{Source: "src", Name: "n"}}}}},
		{Version: 99},
	}
	for i, s := range cases {
		if err := Validate(s); err == nil {
			t.Errorf("case %d should fail", i)
		}
	}
}

func TestLoadRejectsPermissiveAndUnknownState(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "state.json")
	if err := os.WriteFile(path, []byte("{\"version\":1,\"profiles\":[],\"secret\":\"x\"}"), 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(path); err == nil {
		t.Fatal("expected permission rejection")
	}
	if err := os.Chmod(path, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(path); err == nil {
		t.Fatal("expected unknown field rejection")
	}
}

func TestSaveRejectsDuplicateProfileAndKeys(t *testing.T) {
	dup := State{Version: CurrentVersion, Profiles: []Profile{{ID: "p", SourceID: "src"}, {ID: "p", SourceID: "src"}}}
	if err := Validate(dup); err == nil {
		t.Fatal("expected duplicate profile rejection")
	}
	keys := State{Version: CurrentVersion, Profiles: []Profile{{ID: "p", SourceID: "src", Picked: []parser.ServerKey{{Source: "src", Name: "n"}, {Source: "src", Name: "n"}}}}}
	if err := Validate(keys); err == nil {
		t.Fatal("expected duplicate key rejection")
	}
}

func TestSaveRejectsPermissiveDirectory(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "open-dir")
	if err := os.Mkdir(dir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := Save(filepath.Join(dir, "state.json"), State{Version: CurrentVersion, Profiles: []Profile{}}); err == nil {
		t.Fatal("expected permissive directory rejection")
	}
}

func TestLoadRejectsSymlink(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "target.json")
	if err := os.WriteFile(target, []byte("{\"version\":1,\"profiles\":[]}"), 0600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "state.json")
	if err := os.Symlink(target, link); err != nil {
		t.Skip(err)
	}
	if _, err := Load(link); err == nil {
		t.Fatal("expected symlink rejection")
	}
}

func TestReconcilePreservesMissingSelectionsAndMapsCurrentKeys(t *testing.T) {
	available, err := parser.ParseLinks("src", []string{"vless://00000000-0000-4000-8000-000000000001@a.example:443#A", "vless://00000000-0000-4000-8000-000000000001@b.example:443#B"})
	if err != nil {
		t.Fatal(err)
	}
	missing := parser.ServerKey{Source: "src", Name: "C"}
	profile := Profile{ID: "p", SourceID: "src", PickMode: "manual", Picked: []parser.ServerKey{available[0].Key, missing}, Excluded: []parser.ServerKey{available[1].Key}}
	result, err := Reconcile(profile, available)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Picked) != 1 || len(result.Excluded) != 1 || len(result.MissingPicked) != 1 || result.MissingPicked[0] != missing {
		t.Fatalf("result=%#v", result)
	}
	if result.Picked[0] != available[0].CompilerKey() {
		t.Fatalf("picked key=%s", result.Picked[0])
	}
}

func TestReconcileManualAllMissingFailsClosed(t *testing.T) {
	profile := Profile{ID: "p", SourceID: "src", PickMode: "manual", Picked: []parser.ServerKey{{Source: "src", Name: "old"}}}
	result, err := Reconcile(profile, nil)
	if err != ErrNoAvailablePickedNodes || len(result.MissingPicked) != 1 {
		t.Fatalf("result=%#v err=%v", result, err)
	}
}

func TestReconcileAutoModeUsesOnlyAvailableExclusions(t *testing.T) {
	available, err := parser.ParseLinks("src", []string{"vless://00000000-0000-4000-8000-000000000001@a.example:443#A"})
	if err != nil {
		t.Fatal(err)
	}
	profile := Profile{ID: "p", SourceID: "src", PickMode: "auto", Excluded: []parser.ServerKey{{Source: "src", Name: "old"}, available[0].Key}}
	result, err := Reconcile(profile, available)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Picked) != 0 || len(result.Excluded) != 1 || len(result.MissingExcluded) != 1 {
		t.Fatalf("result=%#v", result)
	}
}
