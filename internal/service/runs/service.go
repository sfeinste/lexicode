// service.go is the run read/intervention surface that lands with the S22 scheduler:
//
//	GET  /api/v1/projects/{key}/runs           the run list (filters; the full UI is S23)
//	GET  /api/v1/runs/{id}                     one run, with outputs and context items
//	GET  /api/v1/runs/{id}/activities          the transcript (basic; the S23 UI adds views)
//	POST /api/v1/runs/{id}/messages            steering — queued, delivered between tool calls
//	POST /api/v1/runs/{id}/stop                terminal canceled, artifacts preserved (§10.5)
//	POST /api/v1/runs/{id}/takeover            501 until S24 builds take-over
//
// Only the scheduler writes run state; this service asks it (D-14).
package runs

import (
	"context"
	"log/slog"

	"github.com/spruce/lexicode/internal/domain"
	"github.com/spruce/lexicode/internal/kernel/audit"
	"github.com/spruce/lexicode/internal/kernel/store"
)

// RunControl is the slice of the scheduler this service needs. *sched.Scheduler satisfies it.
type RunControl interface {
	StopRun(ctx context.Context, runID, reason string) error
	TakeoverRun(ctx context.Context, runID, note string) error
	NotifySteering(runID string)
}

// Options configures New.
type Options struct {
	Store  *store.Store
	Audit  *audit.Writer
	Sched  RunControl
	Logger *slog.Logger
}

// Service serves the run endpoints.
type Service struct {
	st     *store.Store
	audit  *audit.Writer
	sched  RunControl
	logger *slog.Logger
	now    func() string
}

// New builds the service.
func New(opts Options) *Service {
	logger := opts.Logger
	if logger == nil {
		logger = slog.Default()
	}
	return &Service{
		st:     opts.Store,
		audit:  opts.Audit,
		sched:  opts.Sched,
		logger: logger,
		now:    domain.Now,
	}
}
