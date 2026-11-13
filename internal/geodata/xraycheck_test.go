package geodata

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestXrayCheckCandidateIsolation(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX test")
	}
	root := t.TempDir()
	entry := append(field(1, []byte("RU")), field(2, field(1, []byte{127}))...)
	data := field(1, entry)
	for _, name := range []string{"geoip.dat", "geosite.dat"} {
		if err := os.WriteFile(filepath.Join(root, name), data, 0600); err != nil {
			t.Fatal(err)
		}
	}
	script := filepath.Join(root, "fake-xray")
	body := "#!/bin/sh\n[ \"$1\" = run ] && [ \"$2\" = -test ] && [ \"$3\" = -config ] || exit 7\n[ \"$XRAY_LOCATION_ASSET\" = \"" + root + "\" ] || exit 8\nexit 0\n"
	if err := os.WriteFile(script, []byte(body), 0700); err != nil {
		t.Fatal(err)
	}
	cfg := filepath.Join(root, "config.json")
	if err := os.WriteFile(cfg, []byte("{}"), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("XRAY_LOCATION_ASSET", "/wrong/asset/path")
	check := XrayCheck{Binary: script, Config: cfg}
	if err := check.Check(root); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(script, []byte("#!/bin/sh\nexit 13\n"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := check.Check(root); err == nil || !strings.Contains(err.Error(), "rejected") {
		t.Fatalf("failed to reject: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, "geosite.dat"), []byte("bad"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := check.Check(root); err == nil {
		t.Fatal("invalid database accepted")
	}
}
