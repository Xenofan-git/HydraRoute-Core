package ui

import (
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"
)

func TestDashboardServesRoutingEditorScript(t *testing.T) {
	r := httptest.NewRequest("GET", "/app.js", nil)
	w := httptest.NewRecorder()
	Handler().ServeHTTP(w, r)
	if w.Code != 200 {
		t.Fatalf("GET /app.js status=%d", w.Code)
	}
	if got := w.Header().Get("Content-Type"); !strings.Contains(got, "javascript") {
		t.Fatalf("app.js content type=%q", got)
	}
	if !strings.Contains(w.Body.String(), "function saveRouting()") {
		t.Fatal("served app.js does not contain routing editor")
	}
	if w.Header().Get("Content-Security-Policy") == "" || w.Header().Get("X-Frame-Options") != "DENY" {
		t.Fatalf("missing dashboard security headers: %v", w.Header())
	}
}

func TestDashboardIncludesPerProfileServerPickerWorkflow(t *testing.T) {
	script := string(mustReadStatic(t, "app.js"))
	for _, required := range []string{
		`data-preview-servers=`,
		`/v1/profiles/preview`,
		`/v1/selections`,
		`missing_picked`,
		`missing_excluded`,
		`pick_mode`,
		`class="pick-node"`,
		`class="exclude-node"`,
	} {
		if !strings.Contains(script, required) {
			t.Errorf("dashboard server picker is missing %q", required)
		}
	}
}

func TestRoutingEditorSplitsMultilineValuesCorrectly(t *testing.T) {
	script := string(mustReadStatic(t, "app.js"))
	const parser = `.value.split(/[\r\n,]+/)`
	const malformed = `.value.split(/[\\r\\n,]+/)`
	if !strings.Contains(script, parser) {
		t.Fatalf("routing editor must split on CR/LF and commas using %q", parser)
	}
	if strings.Contains(script, malformed) {
		t.Fatalf("routing editor still contains over-escaped parser %q", malformed)
	}
	// Mirror the JavaScript character-class semantics to guard the multiline
	// contract even on build hosts without an installed JavaScript runtime.
	re, err := regexp.Compile(`[\r\n,]+`)
	if err != nil {
		t.Fatal(err)
	}
	got := re.Split("domain:one.example\r\ndomain:two.example,domain:three.example", -1)
	want := []string{"domain:one.example", "domain:two.example", "domain:three.example"}
	if len(got) != len(want) {
		t.Fatalf("split result=%q, want %q", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("split result=%q, want %q", got, want)
		}
	}
}
