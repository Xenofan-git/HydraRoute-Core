package geodata

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"syscall"
)

// lockOperation serializes updater processes sharing one generation root.
// LOCK_NB fails closed rather than waiting indefinitely behind a stalled updater.
func (s *GenerationStore) lockOperation() (func(), error) {
	if err := safeRoot(s.Root); err != nil {
		return nil, err
	}
	path := filepath.Join(s.Root, ".operation.lock")
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR|syscall.O_NOFOLLOW, 0600)
	if err != nil {
		return nil, fmt.Errorf("open geodata lock: %w", err)
	}
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 {
		f.Close()
		return nil, errors.New("unsafe geodata lock file")
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		f.Close()
		return nil, fmt.Errorf("geodata update already in progress: %w", err)
	}
	return func() { _ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN); _ = f.Close() }, nil
}
