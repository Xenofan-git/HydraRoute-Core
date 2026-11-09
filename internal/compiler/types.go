package compiler

import "encoding/json"

// Profile is an independent proxy interface. Empty Picked means automatic mode:
// every supported pool member not listed in Excluded is eligible.
type Profile struct {
	ID                string    `json:"id"`
	Enabled           bool      `json:"enabled"`
	Listen            string    `json:"listen,omitempty"`
	Port              uint16    `json:"port"`
	Strategy          string    `json:"strategy"`
	OutboundInterface string    `json:"outbound_interface,omitempty"`
	DPIBypass         DPIBypass `json:"dpi_bypass,omitempty"`
	Nodes             []Node    `json:"nodes"`
	Picked            []string  `json:"picked,omitempty"`
	Excluded          []string  `json:"excluded,omitempty"`
	ManualSelection   bool      `json:"manual_selection,omitempty"`
}

// Node contains a stable identity and a validated Xray outbound object.
// Outbound is supplied by a future protocol parser; it is never used as identity.
type Node struct {
	Key      string          `json:"key"`
	Outbound json.RawMessage `json:"outbound"`
}

// RoutingConfig optionally adds a dedicated shared SOCKS5 inbound. Its traffic
// is routed only by Rules and the explicit default action. Profile inbounds
// remain independently bound to their own balancers.
type DPIBypass struct {
	NoiseTraffic         bool `json:"noise_traffic,omitempty"`
	TLSFragmentation     bool `json:"tls_fragmentation,omitempty"`
	RandomTLSFingerprint bool `json:"random_tls_fingerprint,omitempty"`
}

type RoutingConfig struct {
	Enabled          bool          `json:"enabled"`
	Listen           string        `json:"listen,omitempty"`
	Port             uint16        `json:"port"`
	DefaultAction    string        `json:"default_action"`
	DefaultProfileID string        `json:"default_profile_id,omitempty"`
	Rules            []RoutingRule `json:"rules,omitempty"`
}

// RoutingRule is evaluated in slice order; the first matching rule wins.
// MatchType is domain or ip. Values use Xray domain/IP syntax.
type RoutingRule struct {
	ID        string   `json:"id"`
	Name      string   `json:"name"`
	Enabled   bool     `json:"enabled"`
	MatchType string   `json:"match_type"`
	Values    []string `json:"values"`
	Action    string   `json:"action"`
	ProfileID string   `json:"profile_id,omitempty"`
}

// DNSConfig connects only the managed HydraRoute Xray process to the local
// Core DNS service. It never changes the host resolver configuration.
type DNSConfig struct {
	Enabled bool   `json:"enabled"`
	Listen  string `json:"listen"`
	Port    uint16 `json:"port"`
}

type CompileRequest struct {
	Profiles []Profile      `json:"profiles"`
	Routing  *RoutingConfig `json:"routing,omitempty"`
	DNS      *DNSConfig     `json:"dns,omitempty"`
}
