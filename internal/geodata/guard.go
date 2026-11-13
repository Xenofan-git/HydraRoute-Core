package geodata

import (
	"fmt"
	"os"
	"path/filepath"
)

// CheckStartupReady is a read-only fail-closed guard. A missing generation
// store is allowed for legacy installations; an existing unsafe or unfinished
// store must be recovered under supervisor coordination before Xray autostart.
func CheckStartupReady(root string) error {
	if root == "" || !filepath.IsAbs(root) {
		return fmt.Errorf("absolute geodata root required")
	}
	if _, err := os.Lstat(root); os.IsNotExist(err) {
		return nil
	} else if err != nil {
		return err
	}
	s := &GenerationStore{Root: root}
	if err := safeRoot(root); err != nil {
		return err
	}
	tx, err := s.pendingTransaction()
	if err != nil {
		return fmt.Errorf("geodata transaction inspection: %w", err)
	}
	if tx != nil {
		return fmt.Errorf("unfinished geodata transaction (%s); recovery required before Xray start", tx.Phase)
	}
	if _, err := os.Lstat(filepath.Join(root, ".current-next")); err == nil {
		return fmt.Errorf("unfinished geodata pointer switch; inspection required before Xray start")
	} else if !os.IsNotExist(err) {
		return err
	}
	return nil
}

// CheckAssetStartupReady additionally requires a complete generation when
// Xray explicitly uses this store. Legacy asset paths retain their original
// missing-store compatibility behavior.
func CheckAssetStartupReady(root, assetPath string) error {
	if err := CheckStartupReady(root); err != nil {
		return err
	}
	if assetPath != filepath.Join(root, "current") {
		return nil
	}
	s := &GenerationStore{Root: root}
	current, err := s.Current()
	if err != nil {
		return fmt.Errorf("managed GeoData pointer: %w", err)
	}
	if current == "" {
		return fmt.Errorf("managed GeoData generation is missing")
	}
	dir := filepath.Join(root, current)
	for _, name := range []string{"geoip.dat", "geosite.dat"} {
		data, err := readCandidate(filepath.Join(dir, name))
		if err != nil {
			return fmt.Errorf("managed %s: %w", name, err)
		}
		if err := Validate(name, data); err != nil {
			return fmt.Errorf("managed %s invalid: %w", name, err)
		}
	}
	return nil
}
