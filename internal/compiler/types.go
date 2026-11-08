package compiler

import "encoding/json"

// Profile is an independent proxy interface. Empty Picked means automatic mode:
// every supported pool member not listed in Excluded is eligible.
type Profile struct {
	ID       string   `json:"id"`
	Enabled  bool     `json:"enabled"`
	Listen   string   `json:"listen,omitempty"`
	Port     uint16   `json:"port"`
	Strategy string   `json:"strategy"`
	Nodes    []Node   `json:"nodes"`
	Picked   []string `json:"picked,omitempty"`
	Excluded []string `json:"excluded,omitempty"`
}

// Node contains a stable identity and a validated Xray outbound object.
// Outbound is supplied by a future protocol parser; it is never used as identity.
type Node struct {
	Key      string          `json:"key"`
	Outbound json.RawMessage `json:"outbound"`
}

type CompileRequest struct {
	Profiles []Profile `json:"profiles"`
}
