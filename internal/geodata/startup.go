package geodata

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// RecoverStartup restores an interrupted generation only when the managed
// Xray asset path explicitly points to this store. It must be called before
// supervisor.Restore or Start; no running process may use the generation.
func RecoverStartup(root, binary, config, assetPath string) (bool, error) {
	if root == "" || !filepath.IsAbs(root) {
		return false, errors.New("absolute geodata root required")
	}
	if _, err := os.Lstat(root); os.IsNotExist(err) {
		return false, nil
	} else if err != nil {
		return false, err
	}
	s := &GenerationStore{Root: root}
	tx, err := s.pendingTransaction()
	if err != nil {
		return false, err
	}
	if tx == nil {
		return false, CheckStartupReady(root)
	}
	expected := filepath.Join(root, "current")
	if assetPath != expected {
		return false, fmt.Errorf("pending geodata recovery requires XRAY_LOCATION_ASSET=%s", expected)
	}
	if binary == "" || config == "" {
		return false, errors.New("Xray preflight paths required for geodata recovery")
	}
	// An interrupted pointer rename needs separate operator inspection;
	// never delete a filesystem entry outside the updater lock.
	if _, err := os.Lstat(filepath.Join(root, ".current-next")); err == nil {
		return false, errors.New("unfinished generation pointer requires inspection")
	} else if !os.IsNotExist(err) {
		return false, err
	}
	restored, err := s.RecoverPending((XrayCheck{Binary: binary, Config: config}).Check)
	if err != nil {
		return restored, err
	}
	return restored, CheckStartupReady(root)
}
