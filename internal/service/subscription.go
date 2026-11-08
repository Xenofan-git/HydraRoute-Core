package service

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"hydraroute.local/core/internal/compiler"
	"hydraroute.local/core/internal/parser"
	"hydraroute.local/core/internal/profiles"
	"hydraroute.local/core/internal/state"
)

type ProfileInput struct {
	ID       string   `json:"id"`
	Enabled  bool     `json:"enabled"`
	Listen   string   `json:"listen,omitempty"`
	Port     uint16   `json:"port"`
	Strategy string   `json:"strategy,omitempty"`
	OutboundInterface string `json:"outbound_interface,omitempty"`
	Picked   []string `json:"picked,omitempty"`
	Excluded []string `json:"excluded,omitempty"`
}

type ProfileRequest struct {
	Source          string       `json:"source"`
	Profile         ProfileInput `json:"profile"`
	Links           []string     `json:"links,omitempty"`
	Subscription    string       `json:"subscription,omitempty"`
	SubscriptionURL string       `json:"subscription_url,omitempty"`
}

type SubscriptionRequest struct {
	Source          string                  `json:"source,omitempty"`
	Profile         ProfileInput            `json:"profile,omitempty"`
	Links           []string                `json:"links,omitempty"`
	Subscription    string                  `json:"subscription,omitempty"`
	SubscriptionURL string                  `json:"subscription_url,omitempty"`
	Profiles        []ProfileRequest        `json:"profiles,omitempty"`
	Routing         *compiler.RoutingConfig `json:"-"`
}

type CompileResult struct {
	Config       []byte
	ProfileCount int
	NodeCount    int
}

type PreviewNode struct {
	Key      parser.ServerKey `json:"key"`
	Name     string           `json:"name"`
	Picked   bool             `json:"picked"`
	Excluded bool             `json:"excluded"`
}

type ProfilePreview struct {
	ProfileID       string             `json:"profile_id"`
	SourceID        string             `json:"source_id"`
	PickMode        string             `json:"pick_mode"`
	Nodes           []PreviewNode      `json:"nodes"`
	MissingPicked   []parser.ServerKey `json:"missing_picked,omitempty"`
	MissingExcluded []parser.ServerKey `json:"missing_excluded,omitempty"`
}

// PreviewProfile fetches/parses one encrypted profile source and returns only
// credential-free server identities plus reconciled selection metadata. Raw
// outbounds, links, subscription URLs and credentials never leave this layer.
func PreviewProfile(ctx context.Context, profile profiles.Profile, stateDir string) (ProfilePreview, error) {
	if err := profiles.Validate(profile); err != nil {
		return ProfilePreview{}, fmt.Errorf("invalid stored profile")
	}
	var servers []parser.ParsedServer
	var err error
	switch {
	case profile.SubscriptionURL != "":
		var body string
		body, err = parser.FetchSubscription(ctx, profile.SubscriptionURL)
		if err == nil {
			servers, err = parser.ParseSubscription(profile.SourceID, body)
		}
	case profile.Subscription != "":
		servers, err = parser.ParseSubscription(profile.SourceID, profile.Subscription)
	default:
		servers, err = parser.ParseLinks(profile.SourceID, profile.Links)
	}
	if err != nil {
		return ProfilePreview{}, fmt.Errorf("profile source preview failed")
	}
	selection := state.Profile{ID: profile.ID, SourceID: profile.SourceID, PickMode: "auto"}
	if stateDir != "" {
		saved, loadErr := LoadSelection(stateDir, profile.ID)
		if loadErr == nil {
			if saved.SourceID != profile.SourceID {
				return ProfilePreview{}, fmt.Errorf("saved selection does not match profile/source")
			}
			selection = saved
		} else if !os.IsNotExist(loadErr) {
			return ProfilePreview{}, fmt.Errorf("cannot load saved selection state")
		}
	}
	reconciled, reconcileErr := state.Reconcile(selection, servers)
	if reconcileErr != nil && !errors.Is(reconcileErr, state.ErrNoAvailablePickedNodes) {
		return ProfilePreview{}, fmt.Errorf("saved selection reconciliation failed")
	}
	picked := make(map[string]bool, len(reconciled.Picked))
	for _, key := range reconciled.Picked {
		picked[key] = true
	}
	excluded := make(map[string]bool, len(reconciled.Excluded))
	for _, key := range reconciled.Excluded {
		excluded[key] = true
	}
	out := ProfilePreview{ProfileID: profile.ID, SourceID: profile.SourceID, PickMode: selection.PickMode, Nodes: make([]PreviewNode, 0, len(servers)), MissingPicked: reconciled.MissingPicked, MissingExcluded: reconciled.MissingExcluded}
	for _, server := range servers {
		key := server.CompilerKey()
		out.Nodes = append(out.Nodes, PreviewNode{Key: server.Key, Name: server.Name, Picked: picked[key], Excluded: excluded[key]})
	}
	return out, nil
}

// CompileSubscriptions parses links or securely fetches subscription URLs,
// applies persisted selections (when present), and compiles isolated profiles.
// State filenames are derived only from validated profile IDs; callers cannot
// supply filesystem paths through the API.
type ProfileFailure struct {
	ProfileID string `json:"profile_id"`
	Reason    string `json:"reason"`
}

type RefreshError struct {
	Failures []ProfileFailure `json:"failures"`
}

func (e *RefreshError) Error() string {
	parts := make([]string, 0, len(e.Failures))
	for _, failure := range e.Failures {
		parts = append(parts, fmt.Sprintf("%s: %s", failure.ProfileID, failure.Reason))
	}
	return "one or more profile sources failed; previous config was preserved: " + strings.Join(parts, "; ")
}

func CompileSubscriptions(ctx context.Context, req SubscriptionRequest, stateDir string) (CompileResult, error) {
	inputs := req.Profiles
	if len(inputs) > 0 {
		if req.Source != "" || req.Profile.ID != "" || len(req.Links) > 0 || req.Subscription != "" || req.SubscriptionURL != "" {
			return CompileResult{}, fmt.Errorf("use either profiles or the legacy single-profile fields, not both")
		}
	} else {
		inputs = []ProfileRequest{{Source: req.Source, Profile: req.Profile, Links: req.Links, Subscription: req.Subscription, SubscriptionURL: req.SubscriptionURL}}
	}
	compiledProfiles := make([]compiler.Profile, 0, len(inputs))
	failures := make([]ProfileFailure, 0)
	nodeCount := 0
	for _, input := range inputs {
		id := input.Profile.ID
		fail := func(reason string) { failures = append(failures, ProfileFailure{ProfileID: id, Reason: reason}) }
		if err := state.Validate(state.State{Version: state.CurrentVersion, Profiles: []state.Profile{{ID: id, SourceID: input.Source, PickMode: "auto"}}}); err != nil {
			fail("invalid profile/source identity")
			continue
		}
		profile := compiler.Profile{ID: id, Enabled: input.Profile.Enabled, Listen: input.Profile.Listen, Port: input.Profile.Port, Strategy: input.Profile.Strategy, OutboundInterface: input.Profile.OutboundInterface, Picked: input.Profile.Picked, Excluded: input.Profile.Excluded}
		if !profile.Enabled {
			compiledProfiles = append(compiledProfiles, profile)
			continue
		}
		if (input.Subscription != "" || input.SubscriptionURL != "") && len(input.Links) > 0 {
			fail("ambiguous source input")
			continue
		}
		if input.Subscription != "" && input.SubscriptionURL != "" {
			fail("ambiguous source input")
			continue
		}
		if len(profile.Picked) > 0 || len(profile.Excluded) > 0 {
			fail("inline selection is unsupported; use /v1/selections")
			continue
		}
		var servers []parser.ParsedServer
		var parseErr error
		switch {
		case input.SubscriptionURL != "":
			body, fetchErr := parser.FetchSubscription(ctx, input.SubscriptionURL)
			if fetchErr != nil {
				fail("subscription fetch failed")
				continue
			}
			servers, parseErr = parser.ParseSubscription(input.Source, body)
		case input.Subscription != "":
			servers, parseErr = parser.ParseSubscription(input.Source, input.Subscription)
		default:
			servers, parseErr = parser.ParseLinks(input.Source, input.Links)
		}
		if parseErr != nil {
			fail("subscription parsing failed")
			continue
		}
		if stateDir != "" {
			if dirInfo, dirErr := os.Lstat(stateDir); dirErr == nil {
				if !dirInfo.IsDir() || dirInfo.Mode().Perm()&0077 != 0 {
					fail("selection state directory is not private")
					continue
				}
			} else if !os.IsNotExist(dirErr) {
				fail("cannot inspect selection state directory")
				continue
			}
			stored, loadErr := state.Load(filepath.Join(stateDir, id+".json"))
			if loadErr == nil {
				var saved *state.Profile
				for j := range stored.Profiles {
					if stored.Profiles[j].ID == id {
						saved = &stored.Profiles[j]
						break
					}
				}
				if saved == nil || saved.SourceID != input.Source {
					fail("saved selection does not match profile/source")
					continue
				}
				selection, reconcileErr := state.Reconcile(*saved, servers)
				if reconcileErr != nil {
					fail("saved selection reconciliation failed")
					continue
				}
				profile.Picked, profile.Excluded = selection.Picked, selection.Excluded
			} else if !os.IsNotExist(loadErr) {
				fail("cannot load saved selection state")
				continue
			}
		}
		for _, server := range servers {
			profile.Nodes = append(profile.Nodes, compiler.Node{Key: server.CompilerKey(), Outbound: server.CompilerOutbound()})
		}
		nodeCount += len(servers)
		compiledProfiles = append(compiledProfiles, profile)
	}
	if len(failures) > 0 {
		return CompileResult{}, &RefreshError{Failures: failures}
	}
	compiled, err := compiler.Compile(compiler.CompileRequest{Profiles: compiledProfiles, Routing: req.Routing})
	if err != nil {
		return CompileResult{}, err
	}
	return CompileResult{Config: compiled, ProfileCount: len(compiledProfiles), NodeCount: nodeCount}, nil
}

// SaveSelection creates or replaces the single profile's credential-free state.
func SaveSelection(stateDir string, profile state.Profile) error {
	if stateDir == "" {
		return fmt.Errorf("selection state directory is not configured")
	}
	if err := state.Validate(state.State{Version: state.CurrentVersion, Profiles: []state.Profile{profile}}); err != nil {
		return err
	}
	if err := os.MkdirAll(stateDir, 0700); err != nil {
		return fmt.Errorf("create selection state directory: %w", err)
	}
	info, err := os.Lstat(stateDir)
	if err != nil || !info.IsDir() || info.Mode().Perm()&0077 != 0 {
		return fmt.Errorf("selection state directory must be private")
	}
	return state.Save(filepath.Join(stateDir, profile.ID+".json"), state.State{Version: state.CurrentVersion, Profiles: []state.Profile{profile}})
}

func LoadSelection(stateDir, profileID string) (state.Profile, error) {
	if stateDir == "" {
		return state.Profile{}, fmt.Errorf("selection state directory is not configured")
	}
	dirInfo, dirErr := os.Lstat(stateDir)
	if dirErr != nil {
		return state.Profile{}, dirErr
	}
	if !dirInfo.IsDir() || dirInfo.Mode().Perm()&0077 != 0 {
		return state.Profile{}, fmt.Errorf("selection state directory must be private")
	}
	if err := state.Validate(state.State{Version: state.CurrentVersion, Profiles: []state.Profile{{ID: profileID, SourceID: "placeholder", PickMode: "auto"}}}); err != nil {
		return state.Profile{}, err
	}
	stored, err := state.Load(filepath.Join(stateDir, profileID+".json"))
	if err != nil {
		return state.Profile{}, err
	}
	if len(stored.Profiles) != 1 || stored.Profiles[0].ID != profileID {
		return state.Profile{}, fmt.Errorf("selection state profile mismatch")
	}
	return stored.Profiles[0], nil
}

// DeleteSelection removes a profile's private selection state after validating
// its identifier and the containing directory/file permissions.
func DeleteSelection(stateDir, profileID string) error {
	if stateDir == "" {
		return nil
	}
	if err := state.Validate(state.State{Version: state.CurrentVersion, Profiles: []state.Profile{{ID: profileID, SourceID: "placeholder", PickMode: "auto"}}}); err != nil {
		return err
	}
	info, err := os.Lstat(stateDir)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	if !info.IsDir() || info.Mode().Perm()&0077 != 0 {
		return fmt.Errorf("selection state directory must be private")
	}
	path := filepath.Join(stateDir, profileID+".json")
	fileInfo, err := os.Lstat(path)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	if !fileInfo.Mode().IsRegular() || fileInfo.Mode().Perm()&0077 != 0 {
		return fmt.Errorf("selection state file must be regular and private")
	}
	return os.Remove(path)
}
