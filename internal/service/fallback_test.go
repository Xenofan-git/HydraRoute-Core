package service

import (
	"testing"

	"hydraroute.local/core/internal/compiler"
	"hydraroute.local/core/internal/parser"
)

func fallbackServer(name, uri string) parser.ParsedServer {
	return parser.ParsedServer{Key: parser.ServerKey{Source: "source-a", Name: name}, Name: name, ReferenceURI: uri}
}

func fallbackStatus(alive bool, delay, tried, seen int64) FallbackStatus {
	return FallbackStatus{Alive: alive, DelayMS: delay, LastTryTime: tried, LastSeenTime: seen}
}

func TestChooseFallbackRetainsPreviousAliveCandidate(t *testing.T) {
	a := fallbackServer("A", "vless://id-a@example.test:443#A")
	b := fallbackServer("B", "vless://id-b@example.test:443#B")
	ak, bk := a.CompilerKey(), b.CompilerKey()
	input := ProfileInput{Fallback: ProfileFallback{
		CandidateKey: bk, ReferenceURI: "vless://id-b@example.test:443",
		ReferenceURIs: map[string]string{ak: a.ReferenceURI, bk: b.ReferenceURI},
		Statuses:      map[string]FallbackStatus{ak: fallbackStatus(true, 40, 10, 10), bk: fallbackStatus(true, 90, 11, 11)},
	}}
	got := chooseFallbackKey([]parser.ParsedServer{a, b}, input, compiler.Profile{ID: "p", Enabled: true}, false)
	if got != bk {
		t.Fatalf("want previous alive candidate %q, got %q", bk, got)
	}
}

func TestChooseFallbackUsesLastAliveMatchingReferenceURI(t *testing.T) {
	a := fallbackServer("A", "vless://shared@example.test:443#A")
	b := fallbackServer("B", "vless://shared@example.test:443#B")
	c := fallbackServer("C", "vless://other@example.test:443#C")
	ak, bk, ck := a.CompilerKey(), b.CompilerKey(), c.CompilerKey()
	input := ProfileInput{Fallback: ProfileFallback{
		CandidateKey: "missing", ReferenceURI: "vless://shared@example.test:443#old",
		ReferenceURIs: map[string]string{ak: a.ReferenceURI, bk: b.ReferenceURI, ck: c.ReferenceURI},
		Statuses:      map[string]FallbackStatus{ak: fallbackStatus(true, 80, 10, 10), bk: fallbackStatus(true, 50, 11, 11), ck: fallbackStatus(true, 5, 12, 12)},
	}}
	got := chooseFallbackKey([]parser.ParsedServer{a, b, c}, input, compiler.Profile{ID: "p", Enabled: true}, false)
	if got != bk {
		t.Fatalf("want last alive URI-family member %q, got %q", bk, got)
	}
}

func TestChooseFallbackUsesMinimumDelayWhenReferenceFamilyIsDown(t *testing.T) {
	a := fallbackServer("A", "vless://old@example.test:443#A")
	b := fallbackServer("B", "vless://new-b@example.test:443#B")
	c := fallbackServer("C", "vless://new-c@example.test:443#C")
	ak, bk, ck := a.CompilerKey(), b.CompilerKey(), c.CompilerKey()
	input := ProfileInput{Fallback: ProfileFallback{
		CandidateKey: ak, ReferenceURI: a.ReferenceURI,
		ReferenceURIs: map[string]string{ak: a.ReferenceURI, bk: b.ReferenceURI, ck: c.ReferenceURI},
		Statuses:      map[string]FallbackStatus{ak: fallbackStatus(false, 0, 10, 0), bk: fallbackStatus(true, 65, 11, 0), ck: fallbackStatus(true, 20, 12, 0)},
	}}
	got := chooseFallbackKey([]parser.ParsedServer{a, b, c}, input, compiler.Profile{ID: "p", Enabled: true}, false)
	if got != ck {
		t.Fatalf("want minimum-delay alive member %q, got %q", ck, got)
	}
}

func TestChooseFallbackUsesFirstSelectedWhenAllNodesAreDead(t *testing.T) {
	a := fallbackServer("A", "vless://a@example.test:443#A")
	b := fallbackServer("B", "vless://b@example.test:443#B")
	ak, bk := a.CompilerKey(), b.CompilerKey()
	input := ProfileInput{Fallback: ProfileFallback{
		Statuses:      map[string]FallbackStatus{ak: fallbackStatus(false, 0, 10, 0), bk: fallbackStatus(false, 0, 11, 0)},
		ReferenceURIs: map[string]string{ak: a.ReferenceURI, bk: b.ReferenceURI},
	}}
	got := chooseFallbackKey([]parser.ParsedServer{a, b}, input, compiler.Profile{ID: "p", Enabled: true}, false)
	if got != ak {
		t.Fatalf("want first selected fallback %q, got %q", ak, got)
	}
}

func TestChooseFallbackCannotReuseStatusAcrossChangedURI(t *testing.T) {
	current := fallbackServer("A", "vless://new-id@example.test:443#A")
	other := fallbackServer("B", "vless://other@example.test:443#B")
	key, otherKey := current.CompilerKey(), other.CompilerKey()
	input := ProfileInput{Fallback: ProfileFallback{
		CandidateKey: key, ReferenceURI: "vless://old-id@example.test:443",
		ReferenceURIs: map[string]string{key: "vless://old-id@example.test:443#old", otherKey: other.ReferenceURI},
		Statuses:      map[string]FallbackStatus{key: fallbackStatus(true, 1, 10, 10), otherKey: fallbackStatus(true, 25, 11, 11)},
	}}
	got := chooseFallbackKey([]parser.ParsedServer{current, other}, input, compiler.Profile{ID: "p", Enabled: true}, false)
	if got != otherKey {
		t.Fatalf("stale status from changed URI was reused: got %q want %q", got, otherKey)
	}
}

func TestChooseFallbackRespectsManualSelectionAndExclusions(t *testing.T) {
	a := fallbackServer("A", "vless://a@example.test:443#A")
	b := fallbackServer("B", "vless://b@example.test:443#B")
	ak, bk := a.CompilerKey(), b.CompilerKey()
	input := ProfileInput{Fallback: ProfileFallback{CandidateKey: bk, Statuses: map[string]FallbackStatus{bk: fallbackStatus(true, 1, 10, 10)}}}
	profile := compiler.Profile{ID: "p", Enabled: true, ManualSelection: true, Picked: []string{ak}, Excluded: []string{bk}}
	got := chooseFallbackKey([]parser.ParsedServer{a, b}, input, profile, false)
	if got != ak {
		t.Fatalf("fallback escaped the manually selected profile pool: got %q want %q", got, ak)
	}
}
