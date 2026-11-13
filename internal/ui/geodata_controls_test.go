package ui

import (
	"strings"
	"testing"
)

func TestGeoDataUpdateControlsUseExplicitVerifiedSources(t *testing.T) {
	js := string(mustReadStatic(t, "app.js"))
	for _, fragment := range []string{
		`id="geodataUpdateForm"`,
		`name="geoip_url"`, `name="geoip_sha256"`,
		`name="geosite_url"`, `name="geosite_sha256"`,
		`pattern="[A-Fa-f0-9]{64}"`,
		`data-geodata-action="preflight"`,
		`data-geodata-action="activate"`,
		`'/v1/geodata/'+action`,
		`if(geoForm)geoForm.onsubmit=submitGeoDataUpdate`,
		`action==='activate'&&!confirm(`,
		`!u.ready||u.pending||!state.runtime?.running`,
	} {
		if !strings.Contains(js, fragment) {
			t.Errorf("GeoData UI missing %q", fragment)
		}
	}
}
func TestGeoDataUpdateUIHasNoImplicitActivation(t *testing.T) {
	js := string(mustReadStatic(t, "app.js"))
	if strings.Contains(js, `setInterval(submitGeoDataUpdate`) || strings.Contains(js, `setTimeout(submitGeoDataUpdate`) {
		t.Fatal("GeoData activation must be user initiated")
	}
	if !strings.Contains(js, `await api('/v1/geodata/'+action,'POST',body)`) {
		t.Fatal("GeoData form does not send both verified source descriptors")
	}
}
