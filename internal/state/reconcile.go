package state

import (
	"encoding/json"
	"errors"

	"hydraroute.local/core/internal/parser"
)

var ErrNoAvailablePickedNodes = errors.New("manual selection has no currently available nodes")

type Reconciliation struct {
	Picked          []string
	Excluded        []string
	MissingPicked   []parser.ServerKey
	MissingExcluded []parser.ServerKey
}

// Reconcile maps persisted structured identities onto the current parsed list.
// Missing keys are returned for retention; they are never silently discarded.
func Reconcile(profile Profile, available []parser.ParsedServer) (Reconciliation, error) {
	state := State{Version: CurrentVersion, Profiles: []Profile{profile}}
	if err := Validate(state); err != nil {
		return Reconciliation{}, err
	}
	found := make(map[parser.ServerKey]parser.ParsedServer, len(available))
	for _, server := range available {
		found[server.Key] = server
	}
	result := Reconciliation{}
	for _, key := range profile.Picked {
		if server, ok := found[key]; ok {
			result.Picked = append(result.Picked, server.CompilerKey())
		} else {
			result.MissingPicked = append(result.MissingPicked, key)
		}
	}
	for _, key := range profile.Excluded {
		if server, ok := found[key]; ok {
			result.Excluded = append(result.Excluded, server.CompilerKey())
		} else {
			result.MissingExcluded = append(result.MissingExcluded, key)
		}
	}
	if profile.PickMode == "manual" && len(result.Picked) == 0 {
		return result, ErrNoAvailablePickedNodes
	}
	return result, nil
}

func CompilerKeys(keys []parser.ServerKey) []string {
	out := make([]string, 0, len(keys))
	for _, key := range keys {
		encoded, _ := json.Marshal(key)
		out = append(out, string(encoded))
	}
	return out
}
