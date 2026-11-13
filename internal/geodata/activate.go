package geodata

import (
	"context"
	"errors"
	"fmt"
	"os"
	"time"
)

// Runtime is the narrow interface needed for a coordinated Xray restart.
// Callers must serialize this operation with all other runtime mutations.
type Runtime interface {
	Stop(context.Context) error
	Start(context.Context) error
	IsRunning() bool
	IsConfigured() bool
}

type RuntimeStatus struct {
	Running    bool
	Configured bool
}

// Activate performs an update with a compensating rollback if the restarted
// runtime fails its health check. This operation requires a previously
// bootstrapped generation and never enables a previously stopped runtime.
func (s *GenerationStore) Activate(ctx context.Context, geoip, geosite []byte, preflight func(string) error, runtime Runtime, healthy func(context.Context) error) (string, error) {
	if runtime == nil || healthy == nil || preflight == nil {
		return "", errors.New("preflight, runtime and health probe required")
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}
	pending, err := s.pendingTransaction()
	if err != nil {
		return "", err
	}
	if pending != nil {
		return "", errors.New("pending geodata transaction must be recovered first")
	}
	previous, err := s.Current()
	if err != nil {
		return "", err
	}
	if previous == "" {
		return "", errors.New("generation store must be bootstrapped before activation")
	}
	if !runtime.IsConfigured() {
		return "", errors.New("Xray runtime is not configured")
	}
	// Check the new pair before disrupting the existing runtime.
	dir, err := StagePair(s.Root, geoip, geosite)
	if err != nil {
		return "", err
	}
	defer removeStage(dir)
	if err := preflight(dir); err != nil {
		return "", fmt.Errorf("Xray preflight failed: %w", err)
	}
	if !runtime.IsRunning() {
		return "", errors.New("Xray must be running for a monitored activation")
	}
	if err := s.writeTransaction(previous, "prepared"); err != nil {
		return "", err
	}
	stopCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	err = runtime.Stop(stopCtx)
	cancel()
	if err != nil {
		_ = s.clearTransaction()
		return "", fmt.Errorf("stop Xray before update: %w", err)
	}
	if err := s.writeTransaction(previous, "stopped"); err != nil {
		restartCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		restartErr := runtime.Start(restartCtx)
		cancel()
		return "", errors.Join(err, restartErr)
	}
	// Publish validates the same bytes again and switches a single generation.
	generation, err := s.Publish(geoip, geosite, preflight)
	if err != nil {
		// Publish may have switched current before reporting a filesystem error.
		// Restore the previous generation before restarting Xray.
		rollbackErr := s.Rollback(previous, preflight)
		if rollbackErr != nil {
			return "", errors.Join(fmt.Errorf("publish: %w", err), fmt.Errorf("rollback: %w", rollbackErr))
		}
		startCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		restartErr := runtime.Start(startCtx)
		cancel()
		if restartErr == nil {
			if clearErr := s.clearTransaction(); clearErr != nil {
				return "", errors.Join(err, clearErr)
			}
		}
		return "", errors.Join(fmt.Errorf("publish: %w", err), restartErr)
	}
	if err := s.writeTransaction(previous, "switched"); err != nil {
		// Leave journal for recovery, but still try to restore before restarting.
		if rollbackErr := s.Rollback(previous, preflight); rollbackErr != nil {
			return "", errors.Join(err, rollbackErr)
		}
		restartCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		restartErr := runtime.Start(restartCtx)
		cancel()
		return "", errors.Join(err, restartErr)
	}
	startCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	startErr := runtime.Start(startCtx)
	cancel()
	if startErr == nil {
		healthCtx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		startErr = healthy(healthCtx)
		cancel()
	}
	if startErr == nil {
		if err := s.clearTransaction(); err != nil {
			return "", fmt.Errorf("update running but journal cleanup failed: %w", err)
		}
		return generation, nil
	}
	// Never leave a rejected generation selected when rollback is possible.
	stopCtx, cancel = context.WithTimeout(context.Background(), 15*time.Second)
	stopErr := runtime.Stop(stopCtx)
	cancel()
	if stopErr != nil {
		return "", errors.Join(fmt.Errorf("new generation unhealthy: %w", startErr), fmt.Errorf("stop failed; rollback deferred: %w", stopErr))
	}
	rollbackErr := s.Rollback(previous, preflight)
	if rollbackErr != nil {
		return "", errors.Join(fmt.Errorf("new generation unhealthy: %w", startErr), fmt.Errorf("rollback failed: %w", rollbackErr))
	}
	restartCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	restartErr := runtime.Start(restartCtx)
	cancel()
	if restartErr == nil {
		probeCtx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		restartErr = healthy(probeCtx)
		cancel()
	}
	if restartErr != nil {
		return "", errors.Join(fmt.Errorf("new generation unhealthy: %w", startErr), fmt.Errorf("previous generation restored but runtime recovery failed: %w", restartErr))
	}
	if err := s.clearTransaction(); err != nil {
		return "", errors.Join(startErr, err)
	}
	return "", fmt.Errorf("new generation unhealthy and previous generation restored: %w", startErr)
}

func removeStage(dir string) {
	if dir != "" {
		_ = os.RemoveAll(dir)
	}
}
