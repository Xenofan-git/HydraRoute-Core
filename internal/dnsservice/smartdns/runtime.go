package smartdns

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	mdns "github.com/miekg/dns"
)

const (
	startupTimeout = 5 * time.Second
	probeInterval  = 100 * time.Millisecond
)

type Process interface {
	Wait() error
	Stop(context.Context) error
}

type Launcher interface {
	Start(context.Context, string, string) (Process, error)
}

type Probe func(context.Context, string) error

type RuntimeConfig struct {
	BinaryPath string
	StateDir   string
	ListenAddr string
}

type RuntimeStatus struct {
	Enabled     bool      `json:"enabled"`
	Desired     bool      `json:"desired"`
	Running     bool      `json:"running"`
	PID         int       `json:"pid,omitempty"`
	Generation  string    `json:"generation,omitempty"`
	LastStarted time.Time `json:"last_started,omitempty"`
	LastError   string    `json:"last_error,omitempty"`
	ConfigPath  string    `json:"config_path,omitempty"`
}

type Runtime struct {
	mu            sync.Mutex
	cfg           RuntimeConfig
	launcher      Launcher
	probe         Probe
	process       Process
	waitDone      chan error
	current       string
	desired       bool
	probeFailures int
	status        RuntimeStatus
}

func NewRuntime(cfg RuntimeConfig) (*Runtime, error) {
	if !filepath.IsAbs(cfg.BinaryPath) || filepath.Clean(cfg.BinaryPath) != cfg.BinaryPath {
		return nil, fmt.Errorf("SmartDNS binary path must be absolute and clean")
	}
	if !filepath.IsAbs(cfg.StateDir) || filepath.Clean(cfg.StateDir) != cfg.StateDir {
		return nil, fmt.Errorf("SmartDNS state directory must be absolute and clean")
	}
	if err := os.MkdirAll(cfg.StateDir, 0700); err != nil {
		return nil, fmt.Errorf("create SmartDNS state directory: %w", err)
	}
	stateInfo, err := os.Lstat(cfg.StateDir)
	if err != nil || !stateInfo.IsDir() || stateInfo.Mode()&os.ModeSymlink != 0 || stateInfo.Mode().Perm()&0077 != 0 {
		return nil, fmt.Errorf("SmartDNS state directory must be a private non-symlink directory")
	}
	if cfg.ListenAddr == "" {
		cfg.ListenAddr = "127.0.0.1:5353"
	}
	host, port, err := net.SplitHostPort(cfg.ListenAddr)
	if err != nil || host != "127.0.0.1" || port == "" {
		return nil, fmt.Errorf("SmartDNS readiness address must be loopback")
	}
	return &Runtime{
		cfg:      cfg,
		launcher: execLauncher{},
		probe:    probeDNS,
		status:   RuntimeStatus{Enabled: true},
	}, nil
}

// SetTestHooks is intentionally package-private in production use; tests inject
// a deterministic launcher and readiness probe without requiring SmartDNS.
func (r *Runtime) setTestHooks(launcher Launcher, probe Probe) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if launcher != nil {
		r.launcher = launcher
	}
	if probe != nil {
		r.probe = probe
	}
}

func (r *Runtime) Status() RuntimeStatus {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.reapLocked()
	status := r.status
	status.Desired = r.desired
	status.Running = r.process != nil
	return status
}

func (r *Runtime) reapLocked() {
	if r.process == nil || r.waitDone == nil {
		return
	}
	select {
	case processErr := <-r.waitDone:
		r.process = nil
		r.waitDone = nil
		r.status.Running = false
		r.status.PID = 0
		if processErr != nil {
			r.status.LastError = fmt.Sprintf("SmartDNS process exited: %v", processErr)
		}
	default:
	}
}

// Apply compiles and stages a complete immutable generation, then swaps the
// managed process. If the candidate fails readiness, the previous generation
// is restarted and remains active.
func (r *Runtime) Apply(ctx context.Context, cfg Config, profiles []ProfileRef) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.reapLocked()

	if !cfg.Enabled {
		r.desired = false
		if r.process != nil {
			if err := r.process.Stop(ctx); err != nil {
				return fmt.Errorf("stop SmartDNS while disabling: %w", err)
			}
			r.process = nil
			r.waitDone = nil
		}
		r.current = ""
		r.status = RuntimeStatus{Enabled: false}
		return nil
	}
	r.status.Enabled = true
	generation, err := newGenerationID()
	if err != nil {
		return err
	}
	generationDir := filepath.Join(r.cfg.StateDir, "generations", generation)
	dataDir := filepath.Join(generationDir, "sets")
	cfg.DataDir = dataDir
	output, err := Compile(cfg, profiles)
	if err != nil {
		r.status.LastError = err.Error()
		return err
	}
	if cfg.ListenPort > 0 {
		r.cfg.ListenAddr = net.JoinHostPort("127.0.0.1", strconv.Itoa(cfg.ListenPort))
	}
	if err := writeGeneration(generationDir, output); err != nil {
		r.status.LastError = err.Error()
		return err
	}
	if err := r.swapLocked(ctx, generation, generationDir); err != nil {
		r.status.LastError = err.Error()
		return err
	}
	r.status.LastError = ""
	return nil
}

func (r *Runtime) Start(ctx context.Context) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.reapLocked()
	if r.current == "" {
		return fmt.Errorf("SmartDNS has no prepared configuration")
	}
	r.desired = true
	if r.process != nil {
		return nil
	}
	generationDir := filepath.Join(r.cfg.StateDir, "generations", r.current)
	if err := r.startGenerationWithRetryLocked(ctx, r.current, generationDir); err != nil {
		r.status.LastError = err.Error()
		return err
	}
	return nil
}

func (r *Runtime) Stop(ctx context.Context) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.desired = false
	r.reapLocked()
	if r.process == nil {
		return nil
	}
	err := r.process.Stop(ctx)
	if err != nil {
		r.status.LastError = err.Error()
		return err
	}
	r.process = nil
	r.waitDone = nil
	r.status.Running = false
	r.status.PID = 0
	return nil
}

func (r *Runtime) SetDesiredRunning(desired bool) {
	r.mu.Lock()
	r.desired = desired && r.current != ""
	r.mu.Unlock()
}

// Recover restarts SmartDNS after an unexpected exit without overriding Stop.
func (r *Runtime) Recover(ctx context.Context) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.reapLocked()
	if !r.desired {
		r.probeFailures = 0
		return nil
	}
	if r.process != nil {
		probeCtx, cancel := context.WithTimeout(ctx, time.Second)
		err := r.probe(probeCtx, r.cfg.ListenAddr)
		cancel()
		if err == nil {
			r.probeFailures = 0
			return nil
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		r.probeFailures++
		if r.probeFailures < 3 {
			return fmt.Errorf("SmartDNS readiness failed (%d/3): %w", r.probeFailures, err)
		}
		// Restart only after three consecutive failed readiness checks.
		// Keep the policy-front forwarder pinned to SmartDNS (fail closed).
		stopCtx, stopCancel := context.WithTimeout(ctx, 3*time.Second)
		stopErr := r.process.Stop(stopCtx)
		stopCancel()
		if stopErr != nil {
			return fmt.Errorf("stop unresponsive SmartDNS: %w", stopErr)
		}
		r.process = nil
		r.waitDone = nil
		r.status.Running = false
		r.status.PID = 0
	}
	if r.current == "" {
		return fmt.Errorf("SmartDNS has no prepared configuration")
	}
	generationDir := filepath.Join(r.cfg.StateDir, "generations", r.current)
	if err := r.startGenerationWithRetryLocked(ctx, r.current, generationDir); err != nil {
		r.status.LastError = err.Error()
		return err
	}
	r.status.LastError = ""
	r.probeFailures = 0
	return nil
}

func (r *Runtime) Ready(ctx context.Context) error {
	r.mu.Lock()
	address, running, probe := r.cfg.ListenAddr, r.process != nil, r.probe
	r.mu.Unlock()
	if !running {
		return fmt.Errorf("SmartDNS process is not running")
	}
	return probe(ctx, address)
}

func (r *Runtime) swapLocked(ctx context.Context, generation, generationDir string) error {
	previousGeneration := r.current
	previousDir := ""
	if previousGeneration != "" {
		previousDir = filepath.Join(r.cfg.StateDir, "generations", previousGeneration)
	}
	wasRunning := r.process != nil
	if wasRunning {
		if err := r.process.Stop(ctx); err != nil {
			return fmt.Errorf("stop previous SmartDNS generation: %w", err)
		}
		r.process = nil
		r.waitDone = nil
		r.status.Running = false
		r.status.PID = 0
	}
	r.current = generation
	if !wasRunning {
		r.status.Generation = generation
		r.status.ConfigPath = filepath.Join(generationDir, "smartdns.conf")
		return nil
	}
	if err := r.startGenerationWithRetryLocked(ctx, generation, generationDir); err == nil {
		return nil
	} else {
		candidateErr := err
		if previousGeneration != "" {
			// A canceled API request must not prevent recovery of the previously
			// active resolver after the candidate has already stopped it.
			rollbackCtx, cancel := context.WithTimeout(context.Background(), 2*startupTimeout+3*time.Second)
			rollbackErr := r.startGenerationWithRetryLocked(rollbackCtx, previousGeneration, previousDir)
			cancel()
			if rollbackErr != nil {
				r.current = ""
				return fmt.Errorf("candidate SmartDNS failed: %v; previous generation rollback failed: %w", candidateErr, rollbackErr)
			}
			r.current = previousGeneration
			return fmt.Errorf("candidate SmartDNS failed; previous generation restored: %w", candidateErr)
		}
		r.current = ""
		return fmt.Errorf("candidate SmartDNS failed to start: %w", candidateErr)
	}
}

// startGenerationWithRetryLocked mirrors HRweb's DNS service startup policy:
// one immediate retry after a failed start, unless the caller canceled.
func (r *Runtime) startGenerationWithRetryLocked(ctx context.Context, generation, generationDir string) error {
	firstErr := r.startGenerationLocked(ctx, generation, generationDir)
	if firstErr == nil {
		return nil
	}
	if ctx.Err() != nil {
		return firstErr
	}
	if err := r.startGenerationLocked(ctx, generation, generationDir); err != nil {
		return fmt.Errorf("SmartDNS failed to start after one retry (first attempt: %v): %w", firstErr, err)
	}
	return nil
}

func (r *Runtime) startGenerationLocked(ctx context.Context, generation, generationDir string) error {
	configPath := filepath.Join(generationDir, "smartdns.conf")
	process, err := r.launcher.Start(ctx, r.cfg.BinaryPath, configPath)
	if err != nil {
		return fmt.Errorf("start SmartDNS: %w", err)
	}
	waitDone := make(chan error, 1)
	go func() { waitDone <- process.Wait() }()

	probeCtx, cancel := context.WithTimeout(ctx, startupTimeout)
	defer cancel()
	ticker := time.NewTicker(probeInterval)
	defer ticker.Stop()
	for {
		select {
		case processErr := <-waitDone:
			_ = process.Stop(context.Background())
			return fmt.Errorf("SmartDNS exited before readiness: %v", processErr)
		case <-probeCtx.Done():
			_ = process.Stop(context.Background())
			return fmt.Errorf("SmartDNS did not become ready at %s: %w", r.cfg.ListenAddr, probeCtx.Err())
		case <-ticker.C:
			if err := r.probe(probeCtx, r.cfg.ListenAddr); err == nil {
				r.process = process
				r.waitDone = waitDone
				r.current = generation
				r.status.Running = true
				r.status.Generation = generation
				r.status.PID = processPID(process)
				r.status.ConfigPath = configPath
				r.status.LastStarted = time.Now().UTC()
				return nil
			}
		}
	}
}

func writeGeneration(dir string, output Output) error {
	parent := filepath.Dir(dir)
	if err := os.MkdirAll(parent, 0700); err != nil {
		return err
	}
	if info, err := os.Lstat(parent); err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm()&0077 != 0 {
		return fmt.Errorf("SmartDNS generation parent must be a private directory")
	}
	stage, err := os.MkdirTemp(parent, ".candidate-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(stage)
	if err := os.Mkdir(filepath.Join(stage, "sets"), 0700); err != nil {
		return err
	}
	for name, data := range output.Files {
		if filepath.Base(name) != name || !strings.HasPrefix(name, "domain-set-") {
			return fmt.Errorf("invalid generated SmartDNS data file name %q", name)
		}
		if err := writePrivateFile(filepath.Join(stage, "sets", name), data); err != nil {
			return err
		}
	}
	if err := writePrivateFile(filepath.Join(stage, "smartdns.conf"), []byte(output.Config)); err != nil {
		return err
	}
	if err := os.Rename(stage, dir); err != nil {
		return err
	}
	d, err := os.Open(parent)
	if err != nil {
		return err
	}
	defer d.Close()
	return d.Sync()
}

func writePrivateFile(path string, data []byte) error {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return err
	}
	if _, err := f.Write(data); err != nil {
		f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}

func newGenerationID() (string, error) {
	var raw [12]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(raw[:]), nil
}

func probeDNS(ctx context.Context, address string) error {
	host, port, err := net.SplitHostPort(address)
	if err != nil || host != "127.0.0.1" {
		return fmt.Errorf("invalid SmartDNS probe address")
	}
	query := new(mdns.Msg)
	query.SetQuestion("healthcheck.invalid.", mdns.TypeA)
	client := &mdns.Client{Net: "udp", Timeout: 500 * time.Millisecond}
	response, _, err := client.ExchangeContext(ctx, query, net.JoinHostPort(host, port))
	if err != nil {
		return err
	}
	if response == nil || !response.Response {
		return fmt.Errorf("invalid SmartDNS readiness response")
	}
	return nil
}

type execLauncher struct{}

func (execLauncher) Start(ctx context.Context, binary, config string) (Process, error) {
	info, err := os.Lstat(binary)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm()&0111 == 0 {
		return nil, fmt.Errorf("SmartDNS binary must be a regular executable file")
	}
	cmd := exec.Command(binary, "-f", "-c", config, "-p", "-")
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	return &execProcess{cmd: cmd, done: make(chan struct{})}, nil
}

type execProcess struct {
	cmd      *exec.Cmd
	waitOnce sync.Once
	done     chan struct{}
	waitErr  error
}

func (p *execProcess) Wait() error {
	p.waitOnce.Do(func() {
		p.waitErr = p.cmd.Wait()
		close(p.done)
	})
	<-p.done
	return p.waitErr
}

func (p *execProcess) Stop(ctx context.Context) error {
	if p.cmd.Process == nil {
		return nil
	}
	if err := p.cmd.Process.Signal(syscall.SIGTERM); err != nil && !errors.Is(err, os.ErrProcessDone) {
		return err
	}
	done := make(chan struct{})
	go func() {
		_ = p.Wait()
		close(done)
	}()
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		_ = p.cmd.Process.Kill()
		<-done
		return nil
	case <-time.After(2 * time.Second):
		_ = p.cmd.Process.Kill()
		<-done
		return nil
	}
}

func processPID(process Process) int {
	if p, ok := process.(*execProcess); ok && p.cmd.Process != nil {
		return p.cmd.Process.Pid
	}
	return 0
}
