package blocklist

import (
	"bufio"
	"bytes"
	"fmt"
	"io"
	"net"
	"sort"
	"strings"
)

const (
	defaultMaxBytes = 16 << 20
	maxLineBytes    = 64 << 10
)

type Action string

const (
	Block Action = "block"
	Allow Action = "allow"
)

type Result struct {
	Blocked []string `json:"blocked,omitempty"`
	Allowed []string `json:"allowed,omitempty"`
	Lines   int      `json:"lines"`
	Parsed  int      `json:"parsed"`
	Ignored int      `json:"ignored"`
	Dropped int      `json:"dropped"`
}

// Parse reads the domain-oriented formats used by HRweb DNS block sources:
// hosts-file mappings, plain domain lists and the common Adblock Plus
// ||domain^ / @@||domain^ forms. It deliberately ignores URL/path filters,
// IP ranges and malformed entries rather than broadening them into domains.
func Parse(r io.Reader) (Result, error) {
	data, err := io.ReadAll(io.LimitReader(r, defaultMaxBytes+1))
	if err != nil {
		return Result{}, fmt.Errorf("read DNS blocklist: %w", err)
	}
	if len(data) > defaultMaxBytes {
		return Result{}, fmt.Errorf("DNS blocklist exceeds %d bytes", defaultMaxBytes)
	}

	result := Result{}
	blocked := make(map[string]struct{})
	allowed := make(map[string]struct{})
	scanner := bufio.NewScanner(bytes.NewReader(data))
	scanner.Buffer(make([]byte, 4096), maxLineBytes)
	for scanner.Scan() {
		result.Lines++
		line := strings.TrimSpace(strings.TrimPrefix(scanner.Text(), "\ufeff"))
		if line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, "!") || strings.HasPrefix(line, "[") {
			result.Ignored++
			continue
		}
		action, domains, ignored := parseLine(line)
		if ignored {
			result.Ignored++
			continue
		}
		if len(domains) == 0 {
			result.Dropped++
			continue
		}
		added := false
		for _, raw := range domains {
			domain, ok := normalizeDomain(raw)
			if !ok {
				continue
			}
			target := blocked
			if action == Allow {
				target = allowed
			}
			target[domain] = struct{}{}
			added = true
		}
		if added {
			result.Parsed++
		} else {
			result.Dropped++
		}
	}
	if err := scanner.Err(); err != nil {
		return Result{}, fmt.Errorf("scan DNS blocklist: %w", err)
	}
	result.Blocked = sortedKeys(blocked)
	result.Allowed = sortedKeys(allowed)
	return result, nil
}

func parseLine(line string) (Action, []string, bool) {
	action := Block
	if strings.HasPrefix(line, "@@") {
		action = Allow
		line = strings.TrimSpace(strings.TrimPrefix(line, "@@"))
		if line == "" {
			return action, nil, false
		}
	}

	// Hosts-file entries contain an address followed by one or more names.
	fields := strings.Fields(line)
	if len(fields) >= 2 && net.ParseIP(fields[0]) != nil {
		if !isBlocklistSinkholeAddress(fields[0]) {
			return action, nil, true
		}
		domains := make([]string, 0, len(fields)-1)
		for _, field := range fields[1:] {
			if strings.HasPrefix(field, "#") {
				break
			}
			domains = append(domains, field)
		}
		return action, domains, false
	}

	// Adblock Plus domain anchors. Modifiers and the end-of-domain marker
	// are not part of the hostname.
	if strings.HasPrefix(line, "||") {
		line = strings.TrimPrefix(line, "||")
		if i := strings.IndexAny(line, "^$|/"); i >= 0 {
			line = line[:i]
		}
		line = strings.TrimSpace(line)
		if line == "" {
			return action, nil, false
		}
		return action, []string{line}, false
	}

	// A plain domain may be followed by an inline hosts-style comment.
	if i := strings.Index(line, " #"); i >= 0 {
		line = strings.TrimSpace(line[:i])
	}
	if strings.ContainsAny(line, " \t") || strings.Contains(line, "://") || strings.ContainsAny(line, "/|^$") {
		return action, nil, true
	}
	return action, []string{line}, false
}

func isBlocklistSinkholeAddress(value string) bool {
	ip := net.ParseIP(value)
	if ip == nil {
		return false
	}
	return ip.Equal(net.ParseIP("0.0.0.0")) ||
		ip.Equal(net.ParseIP("127.0.0.1")) ||
		ip.Equal(net.ParseIP("::")) ||
		ip.Equal(net.ParseIP("::1"))
}

func normalizeDomain(value string) (string, bool) {
	value = strings.TrimSpace(strings.ToLower(value))
	value = strings.TrimPrefix(value, "*.")
	value = strings.TrimPrefix(value, ".")
	value = strings.TrimSuffix(value, ".")
	if value == "" || len(value) > 253 || net.ParseIP(value) != nil {
		return "", false
	}
	for _, label := range strings.Split(value, ".") {
		if len(label) == 0 || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return "", false
		}
		for _, r := range label {
			if !((r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '-') {
				return "", false
			}
		}
	}
	return value, true
}

func sortedKeys(values map[string]struct{}) []string {
	out := make([]string, 0, len(values))
	for value := range values {
		out = append(out, value)
	}
	sort.Strings(out)
	return out
}
