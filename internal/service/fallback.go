package service

import (
	"strings"

	"hydraroute.local/core/internal/compiler"
	"hydraroute.local/core/internal/parser"
)

// FallbackStatus is a credential-free snapshot of one profile-local Xray
// Observatory result. Stale results remain useful as last-known-good hints
// while a new configuration is being prepared.
type FallbackStatus struct {
	Alive        bool
	DelayMS      int64
	LastTryTime  int64
	LastSeenTime int64
}

// ProfileFallback carries the previous runtime candidate and observations
// across a refresh. ReferenceURI is secret-bearing and never serialized.
type ProfileFallback struct {
	CandidateKey  string
	ReferenceURI  string
	ReferenceURIs map[string]string
	Statuses      map[string]FallbackStatus
}

func normalizedReferenceURI(uri string) string {
	uri = strings.TrimSpace(uri)
	if i := strings.IndexByte(uri, '#'); i >= 0 {
		uri = uri[:i]
	}
	return uri
}

func referenceURIMatches(candidate, reference string) bool {
	candidate = normalizedReferenceURI(candidate)
	reference = normalizedReferenceURI(reference)
	return candidate != "" && reference != "" && strings.HasPrefix(candidate, reference)
}

func chooseFallbackKey(servers []parser.ParsedServer, input ProfileInput, profile compiler.Profile, directSource bool) string {
	picked := make(map[string]bool, len(profile.Picked))
	for _, key := range profile.Picked {
		picked[key] = true
	}
	excluded := make(map[string]bool, len(profile.Excluded))
	for _, key := range profile.Excluded {
		excluded[key] = true
	}
	eligible := make([]parser.ParsedServer, 0, len(servers))
	for _, server := range servers {
		key := server.CompilerKey()
		if serverFiltered(server.Name, input.FilterKeep, input.FilterExclude, directSource || server.Key.Source == "direct") || excluded[key] {
			continue
		}
		if (profile.ManualSelection || len(picked) > 0) && !picked[key] {
			continue
		}
		eligible = append(eligible, server)
	}
	if len(eligible) == 0 {
		return ""
	}
	statuses := input.Fallback.Statuses
	statusFor := func(server parser.ParsedServer) (FallbackStatus, bool) {
		key := server.CompilerKey()
		status, ok := statuses[key]
		if !ok || !status.Alive {
			return FallbackStatus{}, false
		}
		if previousURI, known := input.Fallback.ReferenceURIs[key]; known {
			if normalizedReferenceURI(previousURI) != normalizedReferenceURI(server.ReferenceURI) {
				return FallbackStatus{}, false
			}
		} else if input.Fallback.ReferenceURIs != nil {
			// A cache entry without a URI snapshot cannot be tied to this source
			// generation safely; never reuse it as a live fallback hint.
			return FallbackStatus{}, false
		}
		return status, true
	}

	// First retain the previous runtime candidate if it still belongs to this
	// profile's selected pool and its last observation was alive.
	if input.Fallback.CandidateKey != "" {
		for _, server := range eligible {
			key := server.CompilerKey()
			if key == input.Fallback.CandidateKey {
				if _, alive := statusFor(server); alive && (input.Fallback.ReferenceURI == "" || referenceURIMatches(server.ReferenceURI, input.Fallback.ReferenceURI)) {
					return key
				}
				break
			}
		}
	}

	// If the previous candidate disappeared from the selected set, retain the
	// last alive member from the same URI family (the original HRweb pool order
	// is the deterministic tie-breaker).
	reference := normalizedReferenceURI(input.Fallback.ReferenceURI)
	if reference != "" {
		lastAliveMatch := ""
		for _, server := range eligible {
			if !referenceURIMatches(server.ReferenceURI, reference) {
				continue
			}
			if _, alive := statusFor(server); alive {
				lastAliveMatch = server.CompilerKey()
			}
		}
		if lastAliveMatch != "" {
			return lastAliveMatch
		}
	}

	// Next choose the lowest-delay alive member. Prefer the URI family when it
	// has an alive measurement; otherwise use the whole profile-local pool.
	bestKey := ""
	bestDelay := int64(1<<63 - 1)
	for pass := 0; pass < 2 && bestKey == ""; pass++ {
		for _, server := range eligible {
			key := server.CompilerKey()
			status, alive := statusFor(server)
			if !alive {
				continue
			}
			if pass == 0 && reference != "" && !referenceURIMatches(server.ReferenceURI, reference) {
				continue
			}
			if bestKey == "" || status.DelayMS < bestDelay {
				bestKey, bestDelay = key, status.DelayMS
			}
		}
		if pass == 0 && bestKey != "" {
			break
		}
	}
	if bestKey != "" {
		return bestKey
	}

	// Last resort is always a member of this profile's own selected pool,
	// never the global direct outbound or another profile's node.
	return eligible[0].CompilerKey()
}
