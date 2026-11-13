package geodata

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

type transaction struct {
	Previous string `json:"previous"`
	Phase    string `json:"phase"`
}

// writeTransaction durably records the rollback target before touching the
// runtime or generation pointer. Only the owning updater may remove it.
func (s *GenerationStore) writeTransaction(previous, phase string) error {
	if !validGeneration(previous) || (phase != "prepared" && phase != "stopped" && phase != "switched") {
		return errors.New("invalid geodata transaction")
	}
	if err := safeRoot(s.Root); err != nil {
		return err
	}
	data, err := json.Marshal(transaction{Previous: previous, Phase: phase})
	if err != nil {
		return err
	}
	temp, err := os.CreateTemp(s.Root, ".transaction-*")
	if err != nil {
		return err
	}
	name := temp.Name()
	defer os.Remove(name)
	if err := temp.Chmod(0600); err != nil {
		temp.Close()
		return err
	}
	if _, err := temp.Write(data); err != nil {
		temp.Close()
		return err
	}
	if err := temp.Sync(); err != nil {
		temp.Close()
		return err
	}
	if err := temp.Close(); err != nil {
		return err
	}
	if err := os.Rename(name, filepath.Join(s.Root, "transaction.json")); err != nil {
		return err
	}
	return syncDir(s.Root)
}
func (s *GenerationStore) pendingTransaction() (*transaction, error) {
	if err := safeRoot(s.Root); err != nil {
		return nil, err
	}
	path := filepath.Join(s.Root, "transaction.json")
	info, err := os.Lstat(path)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 || info.Size() > 4096 {
		return nil, errors.New("unsafe geodata transaction journal")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var tx transaction
	if err := json.Unmarshal(data, &tx); err != nil {
		return nil, err
	}
	if !validGeneration(tx.Previous) || (tx.Phase != "prepared" && tx.Phase != "stopped" && tx.Phase != "switched") {
		return nil, errors.New("invalid geodata transaction journal")
	}
	return &tx, nil
}
func (s *GenerationStore) clearTransaction() error {
	if err := os.Remove(filepath.Join(s.Root, "transaction.json")); err != nil {
		return err
	}
	return syncDir(s.Root)
}

// RecoverPending must run before the supervisor is allowed to autostart. It
// restores the previous validated generation after an interrupted update.
// It does not start or stop Xray; callers must coordinate runtime lifecycle.
func (s *GenerationStore) RecoverPending(check func(string) error) (bool, error) {
	if check == nil {
		return false, errors.New("Xray preflight required")
	}
	tx, err := s.pendingTransaction()
	if err != nil || tx == nil {
		return false, err
	}
	if err := s.Rollback(tx.Previous, check); err != nil {
		return true, fmt.Errorf("recover geodata transaction: %w", err)
	}
	if err := s.clearTransaction(); err != nil {
		return true, err
	}
	return true, nil
}
