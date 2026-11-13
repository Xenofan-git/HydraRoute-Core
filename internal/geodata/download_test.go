package geodata

import (
	"context"
	"strings"
	"testing"
)

func TestFetchRejectsUnsafeSources(t *testing.T) {
	for _, u := range []string{"http://example.com/geoip.dat", "https://127.0.0.1/geoip.dat", "https://user:pass@example.com/geoip.dat", "https://example.com:8443/geoip.dat", "https://example.com/geoip.dat#fragment"} {
		_, err := Fetch(context.Background(), Source{URL: u, SHA256: strings.Repeat("0", 64)})
		if err == nil {
			t.Fatalf("accepted %s", u)
		}
	}
	_, err := Fetch(context.Background(), Source{URL: "https://example.com/geoip.dat", SHA256: "bad"})
	if err == nil {
		t.Fatal("accepted missing digest")
	}
}
