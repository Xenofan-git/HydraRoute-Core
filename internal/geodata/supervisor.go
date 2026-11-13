package geodata

import (
	"context"
	hrruntime "hydraroute.local/core/internal/runtime"
)

// SupervisorAdapter bridges the existing Xray process supervisor to the
// transactional updater without changing its public lifecycle semantics.
type SupervisorAdapter struct{ Supervisor *hrruntime.Supervisor }

func (a SupervisorAdapter) Stop(ctx context.Context) error  { return a.Supervisor.Stop(ctx) }
func (a SupervisorAdapter) Start(ctx context.Context) error { return a.Supervisor.Start(ctx) }
func (a SupervisorAdapter) IsRunning() bool                 { return a.Supervisor.Status().Running }
func (a SupervisorAdapter) IsConfigured() bool              { return a.Supervisor.Status().Configured }

var _ Runtime = SupervisorAdapter{}
