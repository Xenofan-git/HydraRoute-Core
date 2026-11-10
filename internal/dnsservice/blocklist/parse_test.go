package blocklist

import (
	"strings"
	"testing"
)

func TestParseMixedHRWebDomainSources(t *testing.T) {
	input := strings.Join([]string{
		"\ufeff# comment",
		"! title",
		"[Adblock Plus 2.0]",
		"0.0.0.0 Ads.Example.COM ads.example.com",
		"127.0.0.1 tracker.example.org",
		"||video.example.net^$third-party",
		"@@||safe.example.net^",
		"*.wild.example.net",
		"bad_domain_entry",
		"https://example.net/path",
		"",
	}, "\n")

	got, err := Parse(strings.NewReader(input))
	if err != nil {
		t.Fatal(err)
	}
	wantBlocked := []string{"ads.example.com", "tracker.example.org", "video.example.net", "wild.example.net"}
	wantAllowed := []string{"safe.example.net"}
	if strings.Join(got.Blocked, ",") != strings.Join(wantBlocked, ",") {
		t.Fatalf("blocked = %v, want %v", got.Blocked, wantBlocked)
	}
	if strings.Join(got.Allowed, ",") != strings.Join(wantAllowed, ",") {
		t.Fatalf("allowed = %v, want %v", got.Allowed, wantAllowed)
	}
	if got.Lines != 10 || got.Parsed != 5 || got.Ignored != 4 || got.Dropped != 1 {
		t.Fatalf("unexpected parse counters: %+v", got)
	}
}

func TestParseDeduplicatesAndNormalizes(t *testing.T) {
	got, err := Parse(strings.NewReader("Example.COM.\nexample.com\n*.Sub.Example.com\n"))
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"example.com", "sub.example.com"}
	if strings.Join(got.Blocked, ",") != strings.Join(want, ",") {
		t.Fatalf("blocked = %v, want %v", got.Blocked, want)
	}
}

func TestParseIgnoresNonDomainFilters(t *testing.T) {
	got, err := Parse(strings.NewReader("|https://example.com/path|\n@@||good.example^\n||bad.example^\n"))
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Blocked) != 1 || got.Blocked[0] != "bad.example" {
		t.Fatalf("blocked = %v", got.Blocked)
	}
	if len(got.Allowed) != 1 || got.Allowed[0] != "good.example" {
		t.Fatalf("allowed = %v", got.Allowed)
	}
}

func TestParseRejectsOversizedSource(t *testing.T) {
	input := strings.Repeat("a", defaultMaxBytes+1)
	if _, err := Parse(strings.NewReader(input)); err == nil {
		t.Fatal("expected oversized source to fail")
	}
}

func TestParseRejectsOversizedLine(t *testing.T) {
	input := strings.Repeat("a", maxLineBytes+1)
	if _, err := Parse(strings.NewReader(input)); err == nil {
		t.Fatal("expected oversized line to fail")
	}
}
