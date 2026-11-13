package geodata

import (
	"google.golang.org/protobuf/encoding/protowire"
	"os"
	"path/filepath"
	"testing"
)

func TestStagePairAndFailureIsolation(t *testing.T) {
	parent := t.TempDir()
	entry := append(field(1, []byte("RU")), field(2, field(1, []byte{127}))...)
	data := protowire.AppendBytes(protowire.AppendTag(nil, 1, protowire.BytesType), entry)
	dir, err := StagePair(parent, data, data)
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	for _, name := range []string{"geoip.dat", "geosite.dat"} {
		got, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil || string(got) != string(data) {
			t.Fatalf("invalid staged %s: %v", name, err)
		}
	}
	if _, err := StagePair(parent, data, []byte("corrupt")); err == nil {
		t.Fatal("accepted invalid pair")
	}
	matches, err := filepath.Glob(filepath.Join(parent, ".geodata-stage-*"))
	if err != nil || len(matches) != 1 {
		t.Fatalf("unexpected leftover staging: %v %v", matches, err)
	}
}
