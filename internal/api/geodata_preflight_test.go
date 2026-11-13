package api

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestGeoDataPreflightRejectsUnsafeRequestsWithoutNetwork(t *testing.T) {
	h := New(apiSupervisor(t))
	cases := []struct {
		name    string
		request *http.Request
		code    int
	}{
		{"get", localGet("/v1/geodata/preflight"), http.StatusMethodNotAllowed},
		{"invalid-json", localPost("/v1/geodata/preflight", `{"geoip":{},"geosite":{},"unexpected":true}`), http.StatusBadRequest},
		{"missing-config", localPost("/v1/geodata/preflight", `{"geoip":{},"geosite":{}}`), http.StatusServiceUnavailable},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rr := httptest.NewRecorder()
			h.mux.ServeHTTP(rr, tc.request)
			if rr.Code != tc.code {
				t.Fatalf("status %d expected %d: %s", rr.Code, tc.code, rr.Body.String())
			}
		})
	}
	r := localPost("/v1/geodata/preflight", `{}`)
	r.Header.Set("Origin", "https://attacker.example")
	rr := httptest.NewRecorder()
	h.mux.ServeHTTP(rr, r)
	if rr.Code != http.StatusForbidden {
		t.Fatalf("cross origin accepted: %d", rr.Code)
	}
	r = localPost("/v1/geodata/preflight", `{}`)
	r.Header.Set("Content-Type", "text/plain")
	rr = httptest.NewRecorder()
	h.mux.ServeHTTP(rr, r)
	if rr.Code != http.StatusForbidden {
		t.Fatalf("non-json accepted: %d", rr.Code)
	}
	if strings.Contains(rr.Body.String(), "/tmp/") {
		t.Fatal("internal path leaked")
	}
}
