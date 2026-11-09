package state

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"syscall"

	"hydraroute.local/core/internal/parser"
)

const CurrentVersion = 1
const maxStateBytes = 1 << 20

var profileIDPattern = regexp.MustCompile(`^[A-Za-z0-9_-]{1,32}$`)
var sourceIDPattern = regexp.MustCompile(`^[A-Za-z0-9_.-]{1,64}$`)

// Profile stores only stable source identity and user selections. It never
// stores subscription URLs, credentials, parsed outbounds or raw links.
type Profile struct {
	ID       string             `json:"id"`
	SourceID string             `json:"source_id"`
	PickMode string             `json:"pick_mode"`
	Picked   []parser.ServerKey `json:"picked,omitempty"`
	Excluded []parser.ServerKey `json:"excluded,omitempty"`
}

type State struct {
	Version  int       `json:"version"`
	Profiles []Profile `json:"profiles"`
}

func Validate(s State) error {
	if s.Version != CurrentVersion {
		return fmt.Errorf("unsupported state version %d", s.Version)
	}
	ids := map[string]bool{}
	for _, p := range s.Profiles {
		if !profileIDPattern.MatchString(p.ID) {
			return fmt.Errorf("invalid profile id %q", p.ID)
		}
		if ids[p.ID] {
			return fmt.Errorf("duplicate profile id %q", p.ID)
		}
		ids[p.ID] = true
		if !sourceIDPattern.MatchString(p.SourceID) {
			return fmt.Errorf("profile %s: source_id must be an opaque stable identifier", p.ID)
		}
		if p.PickMode == "" {
			p.PickMode = "auto"
		}
		if p.PickMode != "auto" && p.PickMode != "manual" {
			return fmt.Errorf("profile %s: invalid pick_mode", p.ID)
		}
		if p.PickMode == "auto" && len(p.Picked) > 0 {
			return fmt.Errorf("profile %s: auto mode cannot contain picked nodes", p.ID)
		}
		picked, err := validateKeys(p.SourceID, p.Picked)
		if err != nil {
			return fmt.Errorf("profile %s picked: %w", p.ID, err)
		}
		excluded, err := validateKeys(p.SourceID, p.Excluded)
		if err != nil {
			return fmt.Errorf("profile %s excluded: %w", p.ID, err)
		}
		for key := range picked {
			if excluded[key] {
				return fmt.Errorf("profile %s: a node cannot be both picked and excluded", p.ID)
			}
		}
	}
	return nil
}

func validateKeys(source string, keys []parser.ServerKey) (map[string]bool, error) {
	seen := map[string]bool{}
	for _, key := range keys {
		if key.Source != source || strings.TrimSpace(key.Name) == "" || len(key.Name) > 256 || key.Nth < 0 || strings.ContainsAny(key.Name, "\x00\r\n") {
			return nil, fmt.Errorf("invalid or foreign server key")
		}
		b, _ := json.Marshal(key)
		id := string(b)
		if seen[id] {
			return nil, fmt.Errorf("duplicate server key")
		}
		seen[id] = true
	}
	return seen, nil
}

func normalize(s State) State {
	out := State{Version: s.Version, Profiles: append([]Profile(nil), s.Profiles...)}
	for i := range out.Profiles {
		if out.Profiles[i].PickMode == "" {
			out.Profiles[i].PickMode = "auto"
		}
		out.Profiles[i].Picked = append([]parser.ServerKey(nil), out.Profiles[i].Picked...)
		out.Profiles[i].Excluded = append([]parser.ServerKey(nil), out.Profiles[i].Excluded...)
		sortKeys(out.Profiles[i].Picked)
		sortKeys(out.Profiles[i].Excluded)
	}
	sort.Slice(out.Profiles, func(i, j int) bool { return out.Profiles[i].ID < out.Profiles[j].ID })
	return out
}

func sortKeys(keys []parser.ServerKey) {
	sort.Slice(keys, func(i, j int) bool {
		if keys[i].Name != keys[j].Name {
			return keys[i].Name < keys[j].Name
		}
		return keys[i].Nth < keys[j].Nth
	})
}

// Save atomically writes a credential-free state file with private permissions.
func Save(path string, s State) error {
	if err := Validate(s); err != nil {
		return err
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return fmt.Errorf("create state directory: %w", err)
	}
	dirInfo, err := os.Lstat(dir)
	if err != nil || !dirInfo.IsDir() || dirInfo.Mode().Perm()&0077 != 0 {
		return fmt.Errorf("state directory must be a private directory")
	}
	dst, err := os.Lstat(path)
	if err == nil {
		if !dst.Mode().IsRegular() || dst.Mode().Perm()&0077 != 0 {
			return fmt.Errorf("state file must be regular and private")
		}
	} else if !os.IsNotExist(err) {
		return err
	}
	data, err := json.MarshalIndent(normalize(s), "", "  ")
	if err != nil {
		return err
	}
	if len(data) > maxStateBytes {
		return fmt.Errorf("state exceeds 1 MiB limit")
	}
	tmp, err := os.CreateTemp(dir, ".hydraroute-state-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if err := tmp.Chmod(0600); err != nil {
		tmp.Close()
		return err
	}
	if _, err := tmp.Write(append(data, '\n')); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmpName, path); err != nil {
		return err
	}
	d, err := os.Open(dir)
	if err == nil {
		_ = d.Sync()
		_ = d.Close()
	}
	return nil
}

// Load rejects symlinks, permissive files, unknown fields and oversized state.
func Load(path string) (State, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return State{}, err
	}
	if !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 {
		return State{}, fmt.Errorf("state file must be regular and private")
	}
	fd, err := syscall.Open(path, syscall.O_RDONLY|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return State{}, err
	}
	f := os.NewFile(uintptr(fd), path)
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, maxStateBytes+1))
	if err != nil {
		return State{}, err
	}
	if len(data) > maxStateBytes {
		return State{}, fmt.Errorf("state exceeds 1 MiB limit")
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	var s State
	if err := dec.Decode(&s); err != nil {
		return State{}, fmt.Errorf("decode state: %w", err)
	}
	var extra any
	if err := dec.Decode(&extra); err != io.EOF {
		return State{}, fmt.Errorf("state contains trailing JSON")
	}
	if err := Validate(s); err != nil {
		return State{}, err
	}
	return normalize(s), nil
}
