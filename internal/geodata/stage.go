package geodata

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"syscall"
)

// StagePair writes and fsyncs validated candidates in an isolated private
// directory. Activation is deliberately separate: Xray's existing asset
// directory must not be replaced until a crash-safe generation switch exists.
func StagePair(parent string, geoip, geosite []byte) (string, error) {
	if err := Validate("geoip.dat", geoip); err != nil {
		return "", fmt.Errorf("geoip: %w", err)
	}
	if err := Validate("geosite.dat", geosite); err != nil {
		return "", fmt.Errorf("geosite: %w", err)
	}
	info, err := os.Lstat(parent)
	if err != nil {
		return "", err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm()&0002 != 0 {
		return "", errors.New("unsafe staging parent")
	}
	dir, err := os.MkdirTemp(parent, ".geodata-stage-")
	if err != nil {
		return "", err
	}
	ok := false
	defer func() {
		if !ok {
			_ = os.RemoveAll(dir)
		}
	}()
	for _, item := range []struct {
		name string
		data []byte
	}{{"geoip.dat", geoip}, {"geosite.dat", geosite}} {
		path := filepath.Join(dir, item.name)
		f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
		if err != nil {
			return "", err
		}
		n, writeErr := f.Write(item.data)
		if writeErr == nil && n != len(item.data) {
			writeErr = io.ErrShortWrite
		}
		if writeErr == nil {
			writeErr = f.Sync()
		}
		closeErr := f.Close()
		if writeErr != nil {
			return "", writeErr
		}
		if closeErr != nil {
			return "", closeErr
		}
	}
	d, err := os.Open(dir)
	if err != nil {
		return "", err
	}
	syncErr := d.Sync()
	closeErr := d.Close()
	if syncErr != nil {
		return "", syncErr
	}
	if closeErr != nil {
		return "", closeErr
	}
	// Sync the parent directory so the staged generation survives a crash.
	p, err := os.Open(parent)
	if err != nil {
		return "", err
	}
	syncErr = p.Sync()
	closeErr = p.Close()
	if syncErr != nil {
		return "", syncErr
	}
	if closeErr != nil {
		return "", closeErr
	}
	// Reject an unexpected mount/device transition.
	a, _ := os.Stat(parent)
	b, _ := os.Stat(dir)
	if a == nil || b == nil {
		return "", errors.New("staging directory disappeared")
	}
	if sa, ok := a.Sys().(*syscall.Stat_t); ok {
		if sb, ok := b.Sys().(*syscall.Stat_t); ok && sa.Dev != sb.Dev {
			return "", errors.New("staging crossed filesystem")
		}
	}
	ok = true
	return dir, nil
}
