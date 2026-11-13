package geodata

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// GenerationStore manages a pair of assets through a single atomic symlink
// switch. It is isolated from XRAY_LOCATION_ASSET until explicit integration.
type GenerationStore struct {
	Root string
	mu   sync.Mutex
}

func (s *GenerationStore) Publish(geoip, geosite []byte, check func(string) error) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if check == nil {
		return "", errors.New("candidate runtime validation required")
	}
	if err := safeRoot(s.Root); err != nil {
		return "", err
	}
	dir, err := StagePair(s.Root, geoip, geosite)
	if err != nil {
		return "", err
	}
	defer func() {
		if dir != "" {
			_ = os.RemoveAll(dir)
		}
	}()
	if err := check(dir); err != nil {
		return "", fmt.Errorf("candidate runtime validation: %w", err)
	}
	// Ensure no symlink is substituted for the staged generation.
	staged, err := os.Lstat(dir)
	if err != nil || !staged.IsDir() || staged.Mode()&os.ModeSymlink != 0 {
		return "", errors.New("unsafe staged generation")
	}
	generation := fmt.Sprintf("generation-%d", time.Now().UnixNano())
	path := filepath.Join(s.Root, generation)
	if err := os.Rename(dir, path); err != nil {
		return "", err
	}
	dir = ""
	if err := syncDir(s.Root); err != nil {
		return "", err
	}
	if err := s.switchTo(generation); err != nil {
		return "", err
	}
	return generation, nil
}

// Rollback switches back to a named existing, validated generation. It never
// accepts paths or untrusted symlink targets.
func (s *GenerationStore) Rollback(generation string, check func(string) error) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if check == nil {
		return errors.New("rollback runtime validation required")
	}
	if err := safeRoot(s.Root); err != nil {
		return err
	}
	if !validGeneration(generation) {
		return errors.New("invalid generation name")
	}
	path := filepath.Join(s.Root, generation)
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return errors.New("invalid generation directory")
	}
	for _, name := range []string{"geoip.dat", "geosite.dat"} {
		data, err := readCandidate(filepath.Join(path, name))
		if err != nil {
			return err
		}
		if err := Validate(name, data); err != nil {
			return err
		}
	}
	if err := check(path); err != nil {
		return fmt.Errorf("rollback validation: %w", err)
	}
	return s.switchTo(generation)
}

func (s *GenerationStore) Current() (string, error) {
	if err := safeRoot(s.Root); err != nil {
		return "", err
	}
	target, err := os.Readlink(filepath.Join(s.Root, "current"))
	if os.IsNotExist(err) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	if !validGeneration(target) {
		return "", errors.New("unsafe current generation link")
	}
	info, err := os.Lstat(filepath.Join(s.Root, target))
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return "", errors.New("current generation target is missing or unsafe")
	}
	return target, nil
}
func (s *GenerationStore) switchTo(target string) error {
	if !validGeneration(target) {
		return errors.New("invalid generation")
	}
	temp := filepath.Join(s.Root, ".current-next")
	if _, err := os.Lstat(temp); err == nil {
		return errors.New("pending generation link exists")
	} else if !os.IsNotExist(err) {
		return err
	}
	if err := os.Symlink(target, temp); err != nil {
		return err
	}
	defer os.Remove(temp)
	if err := syncDir(s.Root); err != nil {
		return err
	}
	if err := os.Rename(temp, filepath.Join(s.Root, "current")); err != nil {
		return err
	}
	return syncDir(s.Root)
}
func validGeneration(name string) bool {
	if !strings.HasPrefix(name, "generation-") || len(name) <= len("generation-") {
		return false
	}
	for _, c := range name[len("generation-"):] {
		if c < '0' || c > '9' {
			return false
		}
	}
	return true
}
func safeRoot(root string) error {
	if root == "" || !filepath.IsAbs(root) {
		return errors.New("absolute generation root required")
	}
	info, err := os.Lstat(root)
	if err != nil {
		return err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm()&0022 != 0 {
		return errors.New("unsafe generation root")
	}
	return nil
}
func syncDir(path string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	return f.Sync()
}
func readCandidate(path string) ([]byte, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 || info.Size() <= 0 || info.Size() > MaxBytes {
		return nil, errors.New("unsafe generation asset")
	}
	return os.ReadFile(path)
}
