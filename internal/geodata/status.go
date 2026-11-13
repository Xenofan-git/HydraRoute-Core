package geodata

import (
	"os"
	"path/filepath"
)

// StoreStatus exposes only safe, non-sensitive update state to the UI.
type StoreStatus struct {
	Present bool   `json:"present"`
	Current string `json:"current,omitempty"`
	Pending bool   `json:"pending"`
	Phase   string `json:"phase,omitempty"`
	Ready   bool   `json:"ready"`
	Error   string `json:"error,omitempty"`
}

func InspectStore(root string) StoreStatus {
	result := StoreStatus{}
	if _, err := os.Lstat(root); os.IsNotExist(err) {
		return result
	}
	result.Present = true
	if err := safeRoot(root); err != nil {
		result.Error = "Unsafe GeoData store"
		return result
	}
	s := &GenerationStore{Root: root}
	tx, err := s.pendingTransaction()
	if err != nil {
		result.Error = "Invalid GeoData recovery journal"
		return result
	}
	if tx != nil {
		result.Pending = true
		result.Phase = tx.Phase
	}
	current, err := s.Current()
	if err != nil {
		result.Error = "Invalid GeoData generation pointer"
		return result
	}
	result.Current = current
	if _, err := os.Lstat(filepath.Join(root, ".current-next")); err == nil {
		result.Error = "Interrupted GeoData pointer switch"
		return result
	} else if !os.IsNotExist(err) {
		result.Error = "GeoData pointer inspection failed"
		return result
	}
	result.Ready = current != "" && !result.Pending
	return result
}
