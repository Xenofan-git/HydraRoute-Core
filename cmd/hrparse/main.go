package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"

	"hydraroute.local/core/internal/compiler"
	"hydraroute.local/core/internal/parser"
	"hydraroute.local/core/internal/state"
)

type profileInput struct {
	ID       string   `json:"id"`
	Enabled  bool     `json:"enabled"`
	Listen   string   `json:"listen,omitempty"`
	Port     uint16   `json:"port"`
	Strategy string   `json:"strategy,omitempty"`
	Picked   []string `json:"picked,omitempty"`
	Excluded []string `json:"excluded,omitempty"`
}

type profileRequest struct {
	Source          string       `json:"source"`
	Profile         profileInput `json:"profile"`
	Links           []string     `json:"links,omitempty"`
	Subscription    string       `json:"subscription,omitempty"`
	SubscriptionURL string       `json:"subscription_url,omitempty"`
	StateFile       string       `json:"state_file,omitempty"`
}

type request struct {
	Source          string           `json:"source,omitempty"`
	Profile         profileInput     `json:"profile,omitempty"`
	Links           []string         `json:"links,omitempty"`
	Subscription    string           `json:"subscription,omitempty"`
	SubscriptionURL string           `json:"subscription_url,omitempty"`
	StateFile       string           `json:"state_file,omitempty"`
	Profiles        []profileRequest `json:"profiles,omitempty"`
}

func main() {
	input := flag.String("input", "-", "JSON share-link request file, or - for stdin")
	output := flag.String("output", "-", "Xray JSON output file, or - for stdout")
	flag.Parse()
	var reader io.Reader = os.Stdin
	if *input != "-" {
		f, err := os.Open(*input)
		fatalIf(err)
		defer f.Close()
		reader = f
	}
	dec := json.NewDecoder(io.LimitReader(reader, 8<<20))
	dec.DisallowUnknownFields()
	var req request
	fatalIf(dec.Decode(&req))
	var extra any
	if err := dec.Decode(&extra); err != io.EOF {
		if err == nil {
			fatalIf(fmt.Errorf("multiple JSON values in input"))
		}
		fatalIf(err)
	}
	result, err := compileRequest(req)
	fatalIf(err)
	if *output == "-" {
		_, err = os.Stdout.Write(append(result, '\n'))
		fatalIf(err)
		return
	}
	tmp := *output + ".tmp"
	fatalIf(os.WriteFile(tmp, append(result, '\n'), 0600))
	fatalIf(os.Rename(tmp, *output))
}

func compileRequest(req request) ([]byte, error) {
	inputs := req.Profiles
	if len(inputs) > 0 {
		if req.Source != "" || req.Profile.ID != "" || len(req.Links) > 0 || req.Subscription != "" || req.SubscriptionURL != "" || req.StateFile != "" {
			return nil, fmt.Errorf("use either profiles or the legacy single-profile fields, not both")
		}
	} else {
		inputs = []profileRequest{{Source: req.Source, Profile: req.Profile, Links: req.Links, Subscription: req.Subscription, SubscriptionURL: req.SubscriptionURL, StateFile: req.StateFile}}
	}
	profiles := make([]compiler.Profile, 0, len(inputs))
	for i, input := range inputs {
		if (input.Subscription != "" || input.SubscriptionURL != "") && len(input.Links) > 0 {
			return nil, fmt.Errorf("profile %d: provide either subscription or links, not both", i+1)
		}
		var servers []parser.ParsedServer
		var err error
		if input.Subscription != "" && input.SubscriptionURL != "" {
			return nil, fmt.Errorf("profile %d: provide either subscription or subscription_url, not both", i+1)
		}
		if input.SubscriptionURL != "" {
			body, fetchErr := parser.FetchSubscription(context.Background(), input.SubscriptionURL)
			if fetchErr != nil {
				return nil, fmt.Errorf("profile %d: %w", i+1, fetchErr)
			}
			servers, err = parser.ParseSubscription(input.Source, body)
		} else if input.Subscription != "" {
			servers, err = parser.ParseSubscription(input.Source, input.Subscription)
		} else {
			servers, err = parser.ParseLinks(input.Source, input.Links)
		}
		if err != nil {
			return nil, fmt.Errorf("profile %d: %w", i+1, err)
		}
		profile := compiler.Profile{
			ID: input.Profile.ID, Enabled: input.Profile.Enabled, Listen: input.Profile.Listen,
			Port: input.Profile.Port, Strategy: input.Profile.Strategy,
			Picked: input.Profile.Picked, Excluded: input.Profile.Excluded,
		}
		if input.StateFile != "" {
			if len(profile.Picked) > 0 || len(profile.Excluded) > 0 {
				return nil, fmt.Errorf("profile %d: state_file cannot be combined with inline picked/excluded", i+1)
			}
			stored, loadErr := state.Load(input.StateFile)
			if loadErr != nil {
				return nil, fmt.Errorf("profile %d: load selection state: %w", i+1, loadErr)
			}
			var saved *state.Profile
			for j := range stored.Profiles {
				if stored.Profiles[j].ID == input.Profile.ID {
					saved = &stored.Profiles[j]
					break
				}
			}
			if saved == nil || saved.SourceID != input.Source {
				return nil, fmt.Errorf("profile %d: selection state does not match profile/source identity", i+1)
			}
			selection, reconcileErr := state.Reconcile(*saved, servers)
			if reconcileErr != nil {
				return nil, fmt.Errorf("profile %d: reconcile selection: %w", i+1, reconcileErr)
			}
			if len(selection.MissingPicked) > 0 {
				fmt.Fprintf(os.Stderr, "hrparse: profile %s: %d stored picked nodes are currently missing; retained in state file\n", input.Profile.ID, len(selection.MissingPicked))
			}
			if len(selection.MissingExcluded) > 0 {
				fmt.Fprintf(os.Stderr, "hrparse: profile %s: %d stored excluded nodes are currently missing; retained in state file\n", input.Profile.ID, len(selection.MissingExcluded))
			}
			profile.Picked = selection.Picked
			profile.Excluded = selection.Excluded
		}
		for _, server := range servers {
			profile.Nodes = append(profile.Nodes, compiler.Node{Key: server.CompilerKey(), Outbound: server.CompilerOutbound()})
		}
		profiles = append(profiles, profile)
	}
	return compiler.Compile(compiler.CompileRequest{Profiles: profiles})
}

func fatalIf(err error) {
	if err != nil {
		fmt.Fprintln(os.Stderr, "hrparse:", err)
		os.Exit(1)
	}
}
