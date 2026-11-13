package geodata

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestAuditLegacyAssetsPreservesAndReportsAdditionalFiles(t *testing.T) {
	source := t.TempDir()
	for _, name := range []string{"geoip.dat", "geosite.dat", "custom.dat", ".hidden"} {
		if err := os.WriteFile(filepath.Join(source, name), []byte("fixture"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Symlink("/etc/passwd", filepath.Join(source, "other-link")); err != nil {
		t.Fatal(err)
	}
	audit, err := AuditLegacyAssets(source)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{".hidden", "custom.dat", "other-link"}
	if !reflect.DeepEqual(audit.Extra, want) {
		t.Fatalf("extras=%v want=%v", audit.Extra, want)
	}
	for _, name := range []string{"geoip.dat", "geosite.dat", "custom.dat", ".hidden"} {
		data, err := os.ReadFile(filepath.Join(source, name))
		if err != nil || string(data) != "fixture" {
			t.Fatalf("modified %s: %v", name, err)
		}
	}
}
func TestAuditLegacyAssetsRejectsUnsafeSource(t *testing.T) {
	source := t.TempDir()
	for _, name := range []string{"geoip.dat", "geosite.dat"} {
		if err := os.WriteFile(filepath.Join(source, name), []byte("fixture"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	link := filepath.Join(t.TempDir(), "legacy")
	if err := os.Symlink(source, link); err != nil {
		t.Fatal(err)
	}
	if _, err := AuditLegacyAssets(link); err == nil {
		t.Fatal("accepted symlink source")
	}
	if err := os.Remove(filepath.Join(source, "geoip.dat")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("/etc/passwd", filepath.Join(source, "geoip.dat")); err != nil {
		t.Fatal(err)
	}
	if _, err := AuditLegacyAssets(source); err == nil {
		t.Fatal("accepted symlink geoip")
	}
}
