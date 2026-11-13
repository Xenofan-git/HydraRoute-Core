package geodata

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// Bootstrap imports the existing immutable Xray assets into a private
// generation store. It never modifies or removes the source assets.
// The returned generation is the initial rollback baseline.
func (s *GenerationStore) Bootstrap(source string, check func(string) error) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if check == nil {
		return "", errors.New("Xray preflight required")
	}
	if err := safeRoot(s.Root); err != nil {
		return "", err
	}
	if source == "" || !filepath.IsAbs(source) {
		return "", errors.New("absolute source required")
	}
	srcInfo, err := os.Lstat(source)
	if err != nil {
		return "", err
	}
	if !srcInfo.IsDir() || srcInfo.Mode()&os.ModeSymlink != 0 || srcInfo.Mode().Perm()&0002 != 0 {
		return "", errors.New("unsafe legacy asset directory")
	}
	current, err := s.Current()
	if err != nil {
		return "", err
	}
	if current != "" {
		return "", errors.New("generation store already initialized")
	}
	if _, err := os.Lstat(filepath.Join(s.Root, "current")); err == nil {
		return "", errors.New("current link already exists")
	} else if !os.IsNotExist(err) {
		return "", err
	}
	// A managed generation contains only the two standard databases. Refuse
	// migration if custom assets would become inaccessible to Xray.
	audit, err := AuditLegacyAssets(source)
	if err != nil {
		return "", err
	}
	if len(audit.Extra) != 0 {
		return "", fmt.Errorf("legacy asset directory contains additional files requiring an explicit migration plan: %v", audit.Extra)
	}
	assets := make([][]byte, 0, 2)
	for _, name := range []string{"geoip.dat", "geosite.dat"} {
		data, err := readCandidate(filepath.Join(source, name))
		if err != nil {
			return "", fmt.Errorf("legacy %s: %w", name, err)
		}
		if err := Validate(name, data); err != nil {
			return "", fmt.Errorf("legacy %s: %w", name, err)
		}
		assets = append(assets, data)
	}
	// Validate the copied generation, not the source directory.
	dir, err := StagePair(s.Root, assets[0], assets[1])
	if err != nil {
		return "", err
	}
	defer os.RemoveAll(dir)
	if err := check(dir); err != nil {
		return "", fmt.Errorf("legacy Xray preflight: %w", err)
	}
	generation := "generation-0"
	if _, err := os.Lstat(filepath.Join(s.Root, generation)); err == nil {
		return "", errors.New("initial generation already exists")
	} else if !os.IsNotExist(err) {
		return "", err
	}
	if err := os.Rename(dir, filepath.Join(s.Root, generation)); err != nil {
		return "", err
	}
	if err := syncDir(s.Root); err != nil {
		return "", err
	}
	if err := s.switchTo(generation); err != nil {
		return "", err
	}
	return generation, nil
}
