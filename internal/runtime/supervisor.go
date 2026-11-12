package runtime

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"hydraroute.local/core/internal/compiler"
)

type Status struct {
	Configured bool   `json:"configured"`
	Desired    bool   `json:"desired"`
	Running    bool   `json:"running"`
	PID        int    `json:"pid,omitempty"`
	LastExit   string `json:"last_exit,omitempty"`
}

type Supervisor struct {
	mu         sync.Mutex
	binary     string
	configPath string
	cmd        *exec.Cmd
	done       chan error
	configured bool
	desired    bool
	running    bool
	lastExit   string
}

func NewSupervisor(binary, configPath string) (*Supervisor, error) {
	if !filepath.IsAbs(binary) || !filepath.IsAbs(configPath) {
		return nil, fmt.Errorf("Xray binary and config paths must be absolute")
	}
	info, err := os.Stat(binary)
	if err != nil {
		return nil, fmt.Errorf("stat Xray binary: %w", err)
	}
	if !info.Mode().IsRegular() || info.Mode().Perm()&0111 == 0 {
		return nil, fmt.Errorf("Xray binary must be a regular executable file")
	}
	return &Supervisor{binary: binary, configPath: configPath}, nil
}

// Restore validates an existing private config after process restart. It only
// marks the config as prepared; it never starts Xray. A missing config is not
// an error so first-run installations remain unconfigured.
func (s *Supervisor) Restore(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.configured = false
	if s.running {
		return fmt.Errorf("cannot restore config while Xray is running")
	}
	dir := filepath.Dir(s.configPath)
	dirInfo, err := os.Lstat(dir)
	if os.IsNotExist(err) {
		s.configured = false
		return nil
	}
	if err != nil {
		return err
	}
	if !dirInfo.IsDir() || dirInfo.Mode().Perm()&0077 != 0 {
		return fmt.Errorf("config directory must be private")
	}
	info, err := os.Lstat(s.configPath)
	if os.IsNotExist(err) {
		s.configured = false
		return nil
	}
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 {
		return fmt.Errorf("existing config must be a private regular file")
	}
	config, err := os.ReadFile(s.configPath)
	if err != nil {
		return err
	}
	if err := compiler.ValidateJSON(config); err != nil {
		return fmt.Errorf("existing config is invalid: %w", err)
	}
	out, err := exec.CommandContext(ctx, s.binary, "run", "-test", "-config", s.configPath).CombinedOutput()
	if err != nil {
		return fmt.Errorf("existing Xray config validation failed: %s", boundedOutput(out))
	}
	s.configured = true
	return nil
}

// Prepare validates the generated config with the selected Xray binary before
// atomically writing it. It never starts the process.
func (s *Supervisor) Prepare(ctx context.Context, config []byte) error {
	if err := compiler.ValidateJSON(config); err != nil {
		return fmt.Errorf("invalid config JSON: %w", err)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.running {
		return fmt.Errorf("cannot replace config while Xray is running")
	}
	dir := filepath.Dir(s.configPath)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return fmt.Errorf("create config directory: %w", err)
	}
	info, err := os.Lstat(dir)
	if err != nil || !info.IsDir() || info.Mode().Perm()&0077 != 0 {
		return fmt.Errorf("config directory must be private (0700 or stricter)")
	}
	tmp, err := os.CreateTemp(dir, ".hrcore-config-*.json")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)
	if err := tmp.Chmod(0600); err != nil {
		tmp.Close()
		return err
	}
	if _, err := tmp.Write(config); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if out, err := exec.CommandContext(ctx, s.binary, "run", "-test", "-config", tmpPath).CombinedOutput(); err != nil {
		return fmt.Errorf("Xray config validation failed: %s", boundedOutput(out))
	}
	if err := os.Rename(tmpPath, s.configPath); err != nil {
		return err
	}
	s.configured = true
	return nil
}

// SnapshotConfig returns the last prepared config for rollback during refresh.
// The API serializes refresh operations with its operation mutex.
func (s *Supervisor) SnapshotConfig() ([]byte, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.configured {
		return nil, nil
	}
	info, err := os.Lstat(s.configPath)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 {
		return nil, fmt.Errorf("prepared config is not a private regular file")
	}
	return os.ReadFile(s.configPath)
}

func (s *Supervisor) Start(ctx context.Context) error {
	if err := ctx.Err(); err != nil { return err }
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.configured { return fmt.Errorf("no validated config has been prepared") }
	if s.running { return fmt.Errorf("Xray is already running") }
	s.desired = true
	return s.startLocked(ctx)
}

func (s *Supervisor) Recover(ctx context.Context) error {
	if err := ctx.Err(); err != nil { return err }
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.desired || !s.configured || s.running { return nil }
	return s.startLocked(ctx)
}

func (s *Supervisor) startLocked(ctx context.Context) error {
	if err := ctx.Err(); err != nil { return err }
	cmd := exec.Command(s.binary, "run", "-config", s.configPath)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("start Xray: %w", err)
	}
	s.cmd = cmd
	s.done = make(chan error, 1)
	s.running = true
	s.lastExit = ""
	done := s.done
	go func() {
		err := cmd.Wait()
		s.mu.Lock()
		if s.cmd == cmd {
			s.running = false
			s.cmd = nil
			if err != nil {
				s.lastExit = err.Error()
			} else {
				s.lastExit = "clean exit"
			}
		}
		s.mu.Unlock()
		done <- err
	}()
	return nil
}

func (s *Supervisor) Stop(ctx context.Context) error {
	s.mu.Lock()
	s.desired = false
	if !s.running || s.cmd == nil {
		s.mu.Unlock()
		return nil
	}
	cmd := s.cmd
	done := s.done
	_ = cmd.Process.Signal(syscall.SIGTERM)
	s.mu.Unlock()
	select {
	case <-done:
		// The process normally exits with an interrupt signal after Stop.
		return nil
	case <-ctx.Done():
		_ = cmd.Process.Kill()
		return ctx.Err()
	case <-time.After(5 * time.Second):
		_ = cmd.Process.Kill()
		select {
		case <-done:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}

func (s *Supervisor) Status() Status {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := Status{Configured: s.configured, Desired: s.desired, Running: s.running, LastExit: s.lastExit}
	if s.cmd != nil && s.cmd.Process != nil {
		out.PID = s.cmd.Process.Pid
	}
	return out
}

func boundedOutput(b []byte) string {
	const max = 2048
	if len(b) > max {
		b = b[:max]
	}
	return strings.TrimSpace(string(b))
}
