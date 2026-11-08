package routeconfig

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"syscall"

	"hydraroute.local/core/internal/compiler"
)

const maxConfigBytes = 1 << 20

type Store struct{ dir string }

func NewStore(dir string) *Store { return &Store{dir: dir} }

func (s *Store) checkDir(create bool) error {
	if s.dir == "" { return fmt.Errorf("routing config directory is not configured") }
	if create {
		if err := os.MkdirAll(s.dir, 0700); err != nil { return err }
	}
	info, err := os.Lstat(s.dir)
	if err != nil { return err }
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm()&0077 != 0 {
		return fmt.Errorf("routing config directory must be private and not a symlink")
	}
	return nil
}

func (s *Store) Load() (compiler.RoutingConfig, bool, error) {
	if s.dir == "" { return compiler.RoutingConfig{}, false, fmt.Errorf("routing config directory is not configured") }
	if err := s.checkDir(false); err != nil {
		if os.IsNotExist(err) { return compiler.RoutingConfig{}, false, nil }
		return compiler.RoutingConfig{}, false, err
	}
	path := filepath.Join(s.dir, "routing.json")
	info, err := os.Lstat(path)
	if os.IsNotExist(err) { return compiler.RoutingConfig{}, false, nil }
	if err != nil { return compiler.RoutingConfig{}, false, err }
	if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm()&0077 != 0 {
		return compiler.RoutingConfig{}, false, fmt.Errorf("routing config must be a private regular file")
	}
	fd, err := syscall.Open(path, syscall.O_RDONLY|syscall.O_NOFOLLOW, 0)
	if err != nil { return compiler.RoutingConfig{}, false, err }
	file := os.NewFile(uintptr(fd), path)
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, maxConfigBytes+1))
	if err != nil { return compiler.RoutingConfig{}, false, err }
	if len(data) > maxConfigBytes { return compiler.RoutingConfig{}, false, fmt.Errorf("routing config exceeds 1 MiB limit") }
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	var cfg compiler.RoutingConfig
	if err := dec.Decode(&cfg); err != nil { return compiler.RoutingConfig{}, false, fmt.Errorf("decode routing config: %w", err) }
	var extra any
	if err := dec.Decode(&extra); err != io.EOF { return compiler.RoutingConfig{}, false, fmt.Errorf("routing config contains trailing JSON") }
	return cfg, true, nil
}

func (s *Store) Save(cfg compiler.RoutingConfig) error {
	if err := s.checkDir(true); err != nil { return err }
	path := filepath.Join(s.dir, "routing.json")
	if info, err := os.Lstat(path); err == nil {
		if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm()&0077 != 0 {
			return fmt.Errorf("routing config must be a private regular file")
		}
	} else if !os.IsNotExist(err) { return err }
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil { return err }
	if len(data) > maxConfigBytes { return fmt.Errorf("routing config exceeds 1 MiB limit") }
	tmp, err := os.CreateTemp(s.dir, ".routing-*")
	if err != nil { return err }
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if err := tmp.Chmod(0600); err != nil { tmp.Close(); return err }
	if _, err := tmp.Write(append(data, '\n')); err != nil { tmp.Close(); return err }
	if err := tmp.Sync(); err != nil { tmp.Close(); return err }
	if err := tmp.Close(); err != nil { return err }
	if err := os.Rename(tmpName, path); err != nil { return err }
	if d, err := os.Open(s.dir); err == nil { _ = d.Sync(); _ = d.Close() }
	return nil
}

func (s *Store) Delete() error {
	if err := s.checkDir(false); err != nil {
		if os.IsNotExist(err) { return nil }
		return err
	}
	path := filepath.Join(s.dir, "routing.json")
	info, err := os.Lstat(path)
	if os.IsNotExist(err) { return nil }
	if err != nil { return err }
	if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm()&0077 != 0 {
		return fmt.Errorf("routing config must be a private regular file")
	}
	if err := os.Remove(path); err != nil { return err }
	if d, err := os.Open(s.dir); err == nil { _ = d.Sync(); _ = d.Close() }
	return nil
}
