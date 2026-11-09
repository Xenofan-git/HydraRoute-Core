package watchlist

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"syscall"

	"google.golang.org/protobuf/encoding/protowire"

	"hydraroute.local/core/internal/compiler"
)

const maxGeoDataBytes = 128 << 20

var geoFileName = regexp.MustCompile("^[A-Za-z0-9][A-Za-z0-9._-]{0,123}\\.dat$")
var safeDomainKey = regexp.MustCompile("^[A-Za-z0-9_-]+(\\.[A-Za-z0-9_-]+)*$")
var safeTargetName = regexp.MustCompile("^[A-Za-z0-9_-]{1,64}$")

type Target struct {
	Rank int
	Name string
}

type Entry struct {
	Key    string
	Rank   int
	Target string
}

type Snapshot struct {
	targets          []Target
	entries          map[string]Entry
	keys             []Entry
	ExcludedPatterns int
}

func (s *Snapshot) Partial() bool {
	return s != nil && s.ExcludedPatterns > 0
}

func Build(cfg compiler.RoutingConfig, assetDir string) (*Snapshot, error) {
	if !cfg.Enabled {
		return nil, nil
	}
	s := &Snapshot{entries: make(map[string]Entry)}
	targetRanks := make(map[string]int)
	for _, rule := range cfg.Rules {
		if !rule.Enabled {
			continue
		}
		target, err := routeTarget(rule.Action, rule.ProfileID)
		if err != nil {
			return nil, err
		}
		rank, ok := targetRanks[target]
		if !ok {
			rank = len(s.targets)
			targetRanks[target] = rank
			s.targets = append(s.targets, Target{Rank: rank, Name: target})
		}
		if rule.MatchType != "domain" {
			continue
		}
		for _, value := range rule.Values {
			value = strings.TrimSpace(value)
			switch {
			case strings.HasPrefix(value, "domain:"), strings.HasPrefix(value, "full:"):
				key := value[strings.IndexByte(value, ':')+1:]
				if !s.add(key, rank, target) {
					s.ExcludedPatterns++
				}
			case strings.HasPrefix(value, "geosite:"):
				tag, attrs := parseTag(value[len("geosite:"):])
				keys, excluded, err := loadGeoSiteKeys(filepath.Join(assetDir, "geosite.dat"), tag, attrs)
				if err != nil {
					return nil, fmt.Errorf("load geosite %q: %w", tag, err)
				}
				s.ExcludedPatterns += excluded
				for _, key := range keys {
					if !s.add(key, rank, target) {
						s.ExcludedPatterns++
					}
				}
			case strings.HasPrefix(value, "ext:"):
				parts := strings.Split(value, ":")
				if len(parts) != 3 || !geoFileName.MatchString(parts[1]) {
					return nil, fmt.Errorf("invalid external geosite reference %q", value)
				}
				tag, attrs := parseTag(parts[2])
				keys, excluded, err := loadGeoSiteKeys(filepath.Join(assetDir, parts[1]), tag, attrs)
				if err != nil {
					return nil, fmt.Errorf("load external geosite %q: %w", tag, err)
				}
				s.ExcludedPatterns += excluded
				for _, key := range keys {
					if !s.add(key, rank, target) {
						s.ExcludedPatterns++
					}
				}
			case strings.HasPrefix(value, "keyword:"), strings.HasPrefix(value, "regexp:"):
				// The original Watchlist protocol exposes concrete suffix keys only.
				s.ExcludedPatterns++
			}
		}
	}
	s.keys = make([]Entry, 0, len(s.entries))
	for _, entry := range s.entries {
		s.keys = append(s.keys, entry)
	}
	sort.Slice(s.keys, func(i, j int) bool { return s.keys[i].Key < s.keys[j].Key })
	return s, nil
}

func routeTarget(action, profileID string) (string, error) {
	switch action {
	case "profile":
		if !safeTargetName.MatchString(profileID) {
			return "", fmt.Errorf("profile watchlist target is invalid")
		}
		return profileID, nil
	case "direct":
		return "DIRECT", nil
	case "block":
		return "BLOCK", nil
	default:
		return "", fmt.Errorf("unsupported watchlist action %q", action)
	}
}

func parseTag(value string) (string, []string) {
	parts := strings.Split(value, "@")
	tag := strings.ToUpper(parts[0])
	attrs := make([]string, 0, len(parts)-1)
	for _, part := range parts[1:] {
		if part != "" {
			attrs = append(attrs, part)
		}
	}
	return tag, attrs
}

func (s *Snapshot) add(key string, rank int, target string) bool {
	key = strings.ToLower(key)
	if !safeDomainKey.MatchString(key) || len(key) > 253 {
		return false
	}
	if _, exists := s.entries[key]; exists {
		return true // Original watchlist keeps the first inserted owner for duplicate keys.
	}
	s.entries[key] = Entry{Key: key, Rank: rank, Target: target}
	return true
}

func (s *Snapshot) Match(name string) (Entry, bool, error) {
	if len(name) > 0 && name[len(name)-1] == '.' {
		name = name[:len(name)-1]
	}
	if len(name) == 0 || len(name) > 253 {
		return Entry{}, false, fmt.Errorf("ERR name")
	}
	name = strings.ToLower(name)
	var best Entry
	found := false
	for start := 0; start < len(name); start++ {
		if start != 0 && name[start-1] != '.' {
			continue
		}
		if entry, ok := s.entries[name[start:]]; ok && (!found || entry.Rank < best.Rank) {
			best, found = entry, true
		}
	}
	return best, found, nil
}

func (s *Snapshot) Targets() []Target {
	return append([]Target(nil), s.targets...)
}

func (s *Snapshot) Entries() []Entry {
	return append([]Entry(nil), s.keys...)
}

func (s *Snapshot) Dump(w io.Writer) error {
	for _, target := range s.targets {
		if _, err := fmt.Fprintf(w, "T %d %s\n", target.Rank, target.Name); err != nil {
			return err
		}
	}
	for _, entry := range s.keys {
		if _, err := fmt.Fprintf(w, "K %s %d\n", entry.Key, entry.Rank); err != nil {
			return err
		}
	}
	_, err := fmt.Fprintf(w, "END %d\n", len(s.keys))
	return err
}

func loadGeoSiteKeys(path, code string, attrs []string) ([]string, int, error) {
	data, err := readAsset(path)
	if err != nil {
		return nil, 0, err
	}
	var keys []string
	found := false
	for len(data) > 0 {
		num, typ, n := protowire.ConsumeTag(data)
		if n < 0 {
			return nil, 0, protowire.ParseError(n)
		}
		data = data[n:]
		if typ != protowire.BytesType {
			m := protowire.ConsumeFieldValue(num, typ, data)
			if m < 0 {
				return nil, 0, protowire.ParseError(m)
			}
			data = data[m:]
			continue
		}
		payload, m := protowire.ConsumeBytes(data)
		if m < 0 {
			return nil, 0, protowire.ParseError(m)
		}
		data = data[m:]
		if num != 1 {
			continue
		}
		entryCode, domains, err := parseGeoSite(payload)
		if err != nil {
			return nil, 0, err
		}
		if entryCode != code {
			continue
		}
		found = true
		excluded := 0
		for _, d := range domains {
			if d.kind != 2 && d.kind != 3 {
				excluded++
				continue
			}
			ok := true
			for _, required := range attrs {
				if !d.attrs[required] {
					ok = false
					break
				}
			}
			if ok && d.value != "" {
				keys = append(keys, d.value)
			}
		}
		return keys, excluded, nil // Xray resolves the first matching tag entry.
	}
	if !found {
		return nil, 0, fmt.Errorf("tag %s not found", code)
	}
	return keys, 0, nil
}

type geoDomain struct {
	kind  uint64
	value string
	attrs map[string]bool
}

func parseGeoSite(data []byte) (string, []geoDomain, error) {
	var code string
	var domains []geoDomain
	for len(data) > 0 {
		num, typ, n := protowire.ConsumeTag(data)
		if n < 0 {
			return "", nil, protowire.ParseError(n)
		}
		data = data[n:]
		switch {
		case num == 1 && typ == protowire.BytesType:
			v, m := protowire.ConsumeString(data)
			if m < 0 {
				return "", nil, protowire.ParseError(m)
			}
			code = strings.ToUpper(v)
			data = data[m:]
		case num == 2 && typ == protowire.BytesType:
			v, m := protowire.ConsumeBytes(data)
			if m < 0 {
				return "", nil, protowire.ParseError(m)
			}
			d, err := parseGeoDomain(v)
			if err != nil {
				return "", nil, err
			}
			domains = append(domains, d)
			data = data[m:]
		default:
			m := protowire.ConsumeFieldValue(num, typ, data)
			if m < 0 {
				return "", nil, protowire.ParseError(m)
			}
			data = data[m:]
		}
	}
	return code, domains, nil
}

func parseGeoDomain(data []byte) (geoDomain, error) {
	d := geoDomain{attrs: make(map[string]bool)}
	for len(data) > 0 {
		num, typ, n := protowire.ConsumeTag(data)
		if n < 0 {
			return d, protowire.ParseError(n)
		}
		data = data[n:]
		switch {
		case num == 1 && typ == protowire.VarintType:
			v, m := protowire.ConsumeVarint(data)
			if m < 0 {
				return d, protowire.ParseError(m)
			}
			d.kind = v
			data = data[m:]
		case num == 2 && typ == protowire.BytesType:
			v, m := protowire.ConsumeString(data)
			if m < 0 {
				return d, protowire.ParseError(m)
			}
			d.value = v
			data = data[m:]
		case num == 3 && typ == protowire.BytesType:
			v, m := protowire.ConsumeBytes(data)
			if m < 0 {
				return d, protowire.ParseError(m)
			}
			key, err := parseGeoAttribute(v)
			if err != nil {
				return d, err
			}
			d.attrs[key] = true
			data = data[m:]
		default:
			m := protowire.ConsumeFieldValue(num, typ, data)
			if m < 0 {
				return d, protowire.ParseError(m)
			}
			data = data[m:]
		}
	}
	return d, nil
}

func parseGeoAttribute(data []byte) (string, error) {
	var key string
	for len(data) > 0 {
		num, typ, n := protowire.ConsumeTag(data)
		if n < 0 {
			return "", protowire.ParseError(n)
		}
		data = data[n:]
		if num == 1 && typ == protowire.BytesType {
			v, m := protowire.ConsumeString(data)
			if m < 0 {
				return "", protowire.ParseError(m)
			}
			key = v
			data = data[m:]
			continue
		}
		m := protowire.ConsumeFieldValue(num, typ, data)
		if m < 0 {
			return "", protowire.ParseError(m)
		}
		data = data[m:]
	}
	return key, nil
}

func readAsset(path string) ([]byte, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 || info.Size() <= 0 || info.Size() > maxGeoDataBytes {
		return nil, fmt.Errorf("geodata asset is not a safe regular file")
	}
	fd, err := syscall.Open(path, syscall.O_RDONLY|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return nil, err
	}
	f := os.NewFile(uintptr(fd), path)
	defer f.Close()
	opened, err := f.Stat()
	if err != nil || !os.SameFile(info, opened) {
		return nil, fmt.Errorf("geodata asset changed while opening")
	}
	data, err := io.ReadAll(io.LimitReader(f, maxGeoDataBytes+1))
	if err != nil {
		return nil, err
	}
	if len(data) == 0 || int64(len(data)) > maxGeoDataBytes {
		return nil, fmt.Errorf("geodata asset exceeds size limit")
	}
	return data, nil
}
