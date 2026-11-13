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
