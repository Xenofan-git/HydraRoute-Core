package smartdns

import (
	"fmt"
	"net"
	"net/url"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"hydraroute.local/core/internal/parser"
)

var idPattern = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_-]{0,47}$`)
var pathPattern = regexp.MustCompile(`^/[a-zA-Z0-9._~!$&'()*+,;=:@%/-]*$`)
var caPathPattern = regexp.MustCompile(`^/[a-zA-Z0-9._/-]+$`)

type ProfileRef struct {
	ID      string
	Enabled bool
	Listen  string
	Port    int
}

type Upstream struct {
	ID          string `json:"id"`
	ProfileID   string `json:"profile_id"`
	Protocol    string `json:"protocol"` // doh or dot
	Host        string `json:"host"`
	Port        int    `json:"port,omitempty"`
	Path        string `json:"path,omitempty"` // DoH only
	BootstrapIP string `json:"bootstrap_ip"`
	Enabled     bool   `json:"enabled"`
}

type DomainRule struct {
	ID        string `json:"id"`
	MatchType string `json:"match_type"` // exact or suffix
	Pattern   string `json:"pattern"`
	ProfileID string `json:"profile_id"`
	Enabled   bool   `json:"enabled"`
}

type DomainSet struct {
	ID        string   `json:"id"`
	Kind      string   `json:"kind"`                 // block, allow or route
	ProfileID string   `json:"profile_id,omitempty"` // required for route sets
	Domains   []string `json:"domains"`
	Enabled   bool     `json:"enabled"`
}

type Config struct {
	Enabled    bool         `json:"enabled"`
	ListenHost string       `json:"listen_host"`
	ListenPort int          `json:"listen_port"`
	CacheSize  int          `json:"cache_size"`
	CAPath     string       `json:"-"`
	DataDir    string       `json:"-"`
	Upstreams  []Upstream   `json:"upstreams"`
	Rules      []DomainRule `json:"rules"`
	DomainSets []DomainSet  `json:"domain_sets"`
}

func DefaultConfig() Config {
	return Config{ListenHost: "127.0.0.1", ListenPort: 5354, CAPath: "/etc/ssl/certs"}
}

type Output struct {
	Config         string
	Files          map[string][]byte // relative file name -> content, written atomically by the manager
	ProxyCount     int
	UpstreamCount  int
	RuleCount      int
	DomainSetCount int
}

type normalizedUpstream struct {
	Upstream
	host      string
	path      string
	groupName string
	proxyName string
}

type normalizedDomainSet struct {
	DomainSet
	name string
	file string
}

func normalizeDomain(value string) (string, error) {
	value = strings.TrimSuffix(strings.ToLower(strings.TrimSpace(value)), ".")
	if value == "" || len(value) > 253 || net.ParseIP(value) != nil {
		return "", fmt.Errorf("invalid DNS hostname/domain %q", value)
	}
	for _, label := range strings.Split(value, ".") {
		if len(label) == 0 || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return "", fmt.Errorf("invalid DNS hostname/domain %q", value)
		}
		for _, r := range label {
			if !((r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '-') {
				return "", fmt.Errorf("invalid DNS hostname/domain %q", value)
			}
		}
	}
	return value, nil
}

func normalizeConfig(cfg Config, profiles []ProfileRef) (Config, map[string]ProfileRef, []normalizedUpstream, []DomainRule, []normalizedDomainSet, error) {
	if !cfg.Enabled {
		return cfg, nil, nil, nil, nil, nil
	}
	if cfg.ListenHost == "" {
		cfg.ListenHost = "127.0.0.1"
	}
	if cfg.ListenHost != "127.0.0.1" {
		return cfg, nil, nil, nil, nil, fmt.Errorf("SmartDNS listener must be exactly 127.0.0.1")
	}
	if cfg.ListenPort < 1025 || cfg.ListenPort > 65535 {
		return cfg, nil, nil, nil, nil, fmt.Errorf("SmartDNS listener port must be between 1025 and 65535")
	}
	if cfg.CacheSize < 0 || cfg.CacheSize > 1_000_000 {
		return cfg, nil, nil, nil, nil, fmt.Errorf("SmartDNS cache size must be between 0 and 1000000")
	}
	if cfg.CAPath == "" {
		cfg.CAPath = "/etc/ssl/certs"
	}
	if !filepath.IsAbs(cfg.CAPath) || filepath.Clean(cfg.CAPath) != cfg.CAPath || !caPathPattern.MatchString(cfg.CAPath) {
		return cfg, nil, nil, nil, nil, fmt.Errorf("invalid SmartDNS CA directory")
	}
	if len(cfg.DomainSets) > 0 && (cfg.DataDir == "" || !filepath.IsAbs(cfg.DataDir) || filepath.Clean(cfg.DataDir) != cfg.DataDir || !caPathPattern.MatchString(cfg.DataDir)) {
		return cfg, nil, nil, nil, nil, fmt.Errorf("domain-set data directory must be an absolute safe path")
	}

	profileMap := make(map[string]ProfileRef, len(profiles))
	profilePorts := make(map[int]string, len(profiles))
	for _, profile := range profiles {
		if !idPattern.MatchString(profile.ID) {
			return cfg, nil, nil, nil, nil, fmt.Errorf("invalid Core profile ID %q", profile.ID)
		}
		if _, exists := profileMap[profile.ID]; exists {
			return cfg, nil, nil, nil, nil, fmt.Errorf("duplicate Core profile ID %q", profile.ID)
		}
		if profile.Enabled {
			if profile.Listen != "127.0.0.1" || profile.Port < 1025 || profile.Port > 65535 {
				return cfg, nil, nil, nil, nil, fmt.Errorf("profile %q does not have a safe loopback SOCKS endpoint", profile.ID)
			}
			if previous, exists := profilePorts[profile.Port]; exists {
				return cfg, nil, nil, nil, nil, fmt.Errorf("profiles %q and %q share SOCKS port %d", previous, profile.ID, profile.Port)
			}
			profilePorts[profile.Port] = profile.ID
		}
		profileMap[profile.ID] = profile
	}

	upstreams := make([]normalizedUpstream, 0, len(cfg.Upstreams))
	upstreamIDs := make(map[string]struct{}, len(cfg.Upstreams))
	profileHasUpstream := make(map[string]bool)
	for _, upstream := range cfg.Upstreams {
		if !upstream.Enabled {
			continue
		}
		if !idPattern.MatchString(upstream.ID) {
			return cfg, nil, nil, nil, nil, fmt.Errorf("invalid DNS upstream ID %q", upstream.ID)
		}
		if _, exists := upstreamIDs[upstream.ID]; exists {
			return cfg, nil, nil, nil, nil, fmt.Errorf("duplicate DNS upstream ID %q", upstream.ID)
		}
		upstreamIDs[upstream.ID] = struct{}{}
		profile, ok := profileMap[upstream.ProfileID]
		if !ok || !profile.Enabled {
			return cfg, nil, nil, nil, nil, fmt.Errorf("upstream %q references a missing or disabled Core profile %q", upstream.ID, upstream.ProfileID)
		}
		host, err := normalizeDomain(upstream.Host)
		if err != nil {
			return cfg, nil, nil, nil, nil, fmt.Errorf("upstream %q: %w", upstream.ID, err)
		}
		if net.ParseIP(upstream.BootstrapIP) == nil || !parser.IsPublicDialIP(net.ParseIP(upstream.BootstrapIP)) {
			return cfg, nil, nil, nil, nil, fmt.Errorf("upstream %q requires a public literal bootstrap IP", upstream.ID)
		}
		protocol := strings.ToLower(upstream.Protocol)
		port := upstream.Port
		path := upstream.Path
		switch protocol {
		case "doh":
			if port == 0 {
				port = 443
			}
			if path == "" {
				path = "/dns-query"
			}
			if !pathPattern.MatchString(path) || strings.Contains(path, "..") {
				return cfg, nil, nil, nil, nil, fmt.Errorf("upstream %q has an invalid DoH path", upstream.ID)
			}
		case "dot":
			if port == 0 {
				port = 853
			}
			if path != "" {
				return cfg, nil, nil, nil, nil, fmt.Errorf("upstream %q: DoT cannot have an HTTP path", upstream.ID)
			}
		default:
			return cfg, nil, nil, nil, nil, fmt.Errorf("upstream %q protocol must be DoH or DoT", upstream.ID)
		}
		if port < 1 || port > 65535 {
			return cfg, nil, nil, nil, nil, fmt.Errorf("upstream %q has an invalid port", upstream.ID)
		}
		if profile.Port == cfg.ListenPort {
			return cfg, nil, nil, nil, nil, fmt.Errorf("DNS listener port collides with SOCKS port for profile %q", profile.ID)
		}
		profileHasUpstream[profile.ID] = true
		upstream.Protocol, upstream.Port = protocol, port
		upstreams = append(upstreams, normalizedUpstream{Upstream: upstream, host: host, path: path, groupName: "hr_dns_" + profile.ID, proxyName: "hr_proxy_" + profile.ID})
	}
	if len(upstreams) == 0 {
		return cfg, nil, nil, nil, nil, fmt.Errorf("enabled SmartDNS requires at least one enabled proxied upstream")
	}

	rules := make([]DomainRule, 0, len(cfg.Rules))
	ruleIDs := make(map[string]struct{}, len(cfg.Rules))
	ruleKeys := make(map[string]string, len(cfg.Rules))
	for _, rule := range cfg.Rules {
		if !rule.Enabled {
			continue
		}
		if !idPattern.MatchString(rule.ID) {
			return cfg, nil, nil, nil, nil, fmt.Errorf("invalid DNS rule ID %q", rule.ID)
		}
		if _, exists := ruleIDs[rule.ID]; exists {
			return cfg, nil, nil, nil, nil, fmt.Errorf("duplicate DNS rule ID %q", rule.ID)
		}
		ruleIDs[rule.ID] = struct{}{}
		if !profileHasUpstream[rule.ProfileID] {
			return cfg, nil, nil, nil, nil, fmt.Errorf("DNS rule %q references profile %q without an enabled DNS upstream", rule.ID, rule.ProfileID)
		}
		pattern, err := normalizeDomain(rule.Pattern)
		if err != nil {
			return cfg, nil, nil, nil, nil, fmt.Errorf("DNS rule %q: %w", rule.ID, err)
		}
		matchType := strings.ToLower(rule.MatchType)
		if matchType != "exact" && matchType != "suffix" {
			return cfg, nil, nil, nil, nil, fmt.Errorf("DNS rule %q match type must be exact or suffix", rule.ID)
		}
		key := matchType + ":" + pattern
		if previous, exists := ruleKeys[key]; exists {
			return cfg, nil, nil, nil, nil, fmt.Errorf("duplicate/ambiguous DNS rule for %s assigned to profiles %s and %s", pattern, previous, rule.ProfileID)
		}
		ruleKeys[key] = rule.ProfileID
		rule.Pattern, rule.MatchType = pattern, matchType
		rules = append(rules, rule)
	}

	sort.Slice(upstreams, func(i, j int) bool {
		if upstreams[i].ProfileID != upstreams[j].ProfileID {
			return upstreams[i].ProfileID < upstreams[j].ProfileID
		}
		return upstreams[i].ID < upstreams[j].ID
	})
	sort.Slice(rules, func(i, j int) bool {
		if rules[i].MatchType != rules[j].MatchType {
			return rules[i].MatchType == "exact"
		}
		if len(rules[i].Pattern) != len(rules[j].Pattern) {
			return len(rules[i].Pattern) > len(rules[j].Pattern)
		}
		if rules[i].Pattern != rules[j].Pattern {
			return rules[i].Pattern < rules[j].Pattern
		}
		return rules[i].ID < rules[j].ID
	})
	sets := make([]normalizedDomainSet, 0, len(cfg.DomainSets))
	setIDs := make(map[string]struct{}, len(cfg.DomainSets))
	for _, set := range cfg.DomainSets {
		if !set.Enabled {
			continue
		}
		if !idPattern.MatchString(set.ID) {
			return cfg, nil, nil, nil, nil, fmt.Errorf("invalid domain-set ID %q", set.ID)
		}
		if _, exists := setIDs[set.ID]; exists {
			return cfg, nil, nil, nil, nil, fmt.Errorf("duplicate domain-set ID %q", set.ID)
		}
		setIDs[set.ID] = struct{}{}
		kind := strings.ToLower(set.Kind)
		if kind != "block" && kind != "allow" && kind != "route" {
			return cfg, nil, nil, nil, nil, fmt.Errorf("domain-set %q kind must be block, allow or route", set.ID)
		}
		if kind == "route" && !profileHasUpstream[set.ProfileID] {
			return cfg, nil, nil, nil, nil, fmt.Errorf("route domain-set %q references profile %q without an enabled upstream", set.ID, set.ProfileID)
		}
		if kind != "route" && set.ProfileID != "" {
			return cfg, nil, nil, nil, nil, fmt.Errorf("non-route domain-set %q must not specify a profile", set.ID)
		}
		domains := make([]string, 0, len(set.Domains))
		seenDomains := make(map[string]struct{}, len(set.Domains))
		for _, raw := range set.Domains {
			domain, err := normalizeDomain(raw)
			if err != nil {
				return cfg, nil, nil, nil, nil, fmt.Errorf("domain-set %q: %w", set.ID, err)
			}
			if _, exists := seenDomains[domain]; exists {
				continue
			}
			seenDomains[domain] = struct{}{}

			domains = append(domains, domain)
		}
		if len(domains) == 0 {
			return cfg, nil, nil, nil, nil, fmt.Errorf("domain-set %q must contain at least one domain", set.ID)
		}
		sort.Strings(domains)
		set.Kind, set.Domains = kind, domains
		if kind == "route" {
			for _, domain := range domains {
				for _, rule := range rules {
					overlaps := domain == rule.Pattern || (rule.MatchType == "suffix" && strings.HasSuffix(domain, "."+rule.Pattern)) || (rule.MatchType == "suffix" && strings.HasSuffix(rule.Pattern, "."+domain))
					if overlaps {
						return cfg, nil, nil, nil, nil, fmt.Errorf("route domain-set %q overlaps explicit rule %q; resolve before compile", set.ID, rule.ID)
					}
				}
			}
		}
		sets = append(sets, normalizedDomainSet{DomainSet: set, name: "hr_set_" + set.ID, file: "domain-set-" + set.ID + ".txt"})
	}
	// SmartDNS resolves domain rules by the most-specific matching suffix.
	// For the same exact domain, emit allow rules after block rules so an
	// explicit allow exception wins. Block entries covered by an allow parent
	// are removed below; allow child exceptions remain more specific.
	allowDomains := make([]string, 0)
	for _, set := range sets {
		if set.Kind == "allow" {
			allowDomains = append(allowDomains, set.Domains...)
		}
	}
	for i := range sets {
		if sets[i].Kind != "block" {
			continue
		}
		filtered := make([]string, 0, len(sets[i].Domains))
		for _, domain := range sets[i].Domains {
			coveredByAllowParent := false
			for _, allowed := range allowDomains {
				if domain == allowed || strings.HasSuffix(domain, "."+allowed) {
					coveredByAllowParent = true
					break
				}
			}
			if !coveredByAllowParent {
				filtered = append(filtered, domain)
			}
		}
		sets[i].Domains = filtered
	}
	nonEmpty := sets[:0]
	for _, set := range sets {
		if len(set.Domains) > 0 {
			nonEmpty = append(nonEmpty, set)
		}
	}
	sets = nonEmpty
	priority := func(kind string) int {
		switch kind {
		case "block":
			return 0
		case "route":
			return 1
		case "allow":
			return 2
		default:
			return 3
		}
	}
	sort.Slice(sets, func(i, j int) bool {
		if priority(sets[i].Kind) != priority(sets[j].Kind) {
			return priority(sets[i].Kind) < priority(sets[j].Kind)
		}
		return sets[i].ID < sets[j].ID
	})
	return cfg, profileMap, upstreams, rules, sets, nil
}

func Compile(cfg Config, profiles []ProfileRef) (Output, error) {
	if !cfg.Enabled {
		return Output{}, nil
	}
	cfg, profileMap, upstreams, rules, sets, err := normalizeConfig(cfg, profiles)
	if err != nil {
		return Output{}, err
	}
	profileIDs := make([]string, 0, len(profileMap))
	for id := range profileHasUpstreams(upstreams) {
		profileIDs = append(profileIDs, id)
	}
	sort.Strings(profileIDs)

	var b strings.Builder
	fmt.Fprintf(&b, "# Generated by HydraRoute Core. Do not edit.\n")
	fmt.Fprintf(&b, "# Default upstream group is intentionally empty: unmatched domains fail closed.\n")
	fmt.Fprintf(&b, "bind %s:%d\n", cfg.ListenHost, cfg.ListenPort)
	fmt.Fprintf(&b, "bind-tcp %s:%d\n", cfg.ListenHost, cfg.ListenPort)
	fmt.Fprintf(&b, "ca-path %s\n", cfg.CAPath)
	fmt.Fprintf(&b, "cache-size %d\n", cfg.CacheSize)
	b.WriteString("cache-persist no\nresolv-file /dev/null\nspeed-check-mode none\nlocal-ptr-enable no\nmdns-lookup no\nexpand-ptr-from-address no\naudit-enable no\nlog-level error\n")
	b.WriteString("\n# One SOCKS5 proxy per Core profile; never per Xray node.\n")
	for _, id := range profileIDs {
		profile := profileMap[id]
		fmt.Fprintf(&b, "proxy-server socks5://127.0.0.1:%d -name hr_proxy_%s\n", profile.Port, id)
	}
	b.WriteString("\n# Every proxied upstream is excluded from the default group.\n")
	for _, upstream := range upstreams {
		base := fmt.Sprintf("-group %s -exclude-default-group -proxy %s -host-name %s -tls-host-verify %s -host-ip %s", upstream.groupName, upstream.proxyName, upstream.host, upstream.host, upstream.BootstrapIP)
		if upstream.Protocol == "doh" {
			fmt.Fprintf(&b, "server-https https://%s:%d%s %s -http-host %s\n", upstream.host, upstream.Port, upstream.path, base, upstream.host)
		} else {
			fmt.Fprintf(&b, "server-tls %s:%d %s\n", upstream.host, upstream.Port, base)
		}
	}
	files := make(map[string][]byte, len(sets))
	if len(sets) > 0 {
		b.WriteString("\n# Domain sets are generated from validated domain names.\n")
	}
	for _, set := range sets {
		fullPath := filepath.Join(cfg.DataDir, set.file)
		fmt.Fprintf(&b, "domain-set -name %s -type list -file %s\n", set.name, fullPath)
		files[set.file] = []byte(strings.Join(set.Domains, "\n") + "\n")
		switch set.Kind {
		case "block":
			fmt.Fprintf(&b, "address /domain-set:%s/#\n", set.name)
		case "allow":
			fmt.Fprintf(&b, "address /domain-set:%s/-\n", set.name)
		case "route":
			fmt.Fprintf(&b, "nameserver /domain-set:%s/hr_dns_%s\n", set.name, set.ProfileID)
		}
	}
	if len(rules) > 0 {
		b.WriteString("\n# Explicit domain-to-profile policy. Exact rules precede suffix rules.\n")
	}
	for _, rule := range rules {
		pattern := rule.Pattern
		if rule.MatchType == "exact" {
			pattern = "-" + pattern
		}
		fmt.Fprintf(&b, "nameserver /%s/hr_dns_%s\n", pattern, rule.ProfileID)
	}
	return Output{Config: b.String(), Files: files, ProxyCount: len(profileIDs), UpstreamCount: len(upstreams), RuleCount: len(rules), DomainSetCount: len(sets)}, nil
}

func profileHasUpstreams(upstreams []normalizedUpstream) map[string]bool {
	result := make(map[string]bool)
	for _, upstream := range upstreams {
		result[upstream.ProfileID] = true
	}
	return result
}

// ValidateDoHURL is kept as a small public helper for API form validation.
func ValidateDoHURL(raw string) error {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || u.Fragment != "" {
		return fmt.Errorf("DoH endpoint must be a valid HTTPS URL without credentials or fragment")
	}
	return nil
}
