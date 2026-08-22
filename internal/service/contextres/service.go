// Package contextres is the context-resolution surface service (S34; architecture §11):
// the agent detail's dry context preview, the wiki context-budget endpoint the ContextMeter
// reads, and the daily verified_until demotion job. The resolver itself lives in the
// scheduler (one resolver, three surfaces — this package renders, it never re-resolves);
// this service reaches it through the narrow Resolver seam so that the wiring in
// cmd/lexicode can late-bind the scheduler exactly like every other seam.
package contextres

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"github.com/spruce/lexicode/internal/domain"
	"github.com/spruce/lexicode/internal/kernel/audit"
	"github.com/spruce/lexicode/internal/kernel/bus"
	"github.com/spruce/lexicode/internal/kernel/store"
)

// Resolver is the seam onto the scheduler's context resolution — the SAME code path a real
// enqueue runs, in dry mode. Implemented by *sched.Scheduler; late-bound in cmd/lexicode.
type Resolver interface {
	PreviewContext(ctx context.Context, projectID, agentID string) ([]domain.RunContextItem, error)
}

// Notify delivers one in-app notification row — the notify service's DeliverInApp,
// handed over at the wiring site (a service may not import a sibling service).
type Notify func(ctx context.Context, n domain.Notification) error

// Service is the context-resolution service. Construct with New.
type Service struct {
	st     *store.Store
	res    Resolver
	audit  *audit.Writer
	bus    *bus.Bus
	notify Notify
	logger *slog.Logger
	now    func() time.Time

	// demotion job lifecycle
	wg   sync.WaitGroup
	tick time.Duration
}

// Options configures New.
type Options struct {
	// Store is the open, migrated database. Required.
	Store *store.Store
	// Resolver is the scheduler's dry-resolve seam. Required for the preview endpoint.
	Resolver Resolver
	// Audit is the audit-log writer. Required — every demotion writes an entry.
	Audit *audit.Writer
	// Bus emits wiki.updated events for demotions. Nil (tests) skips emission.
	Bus *bus.Bus
	// Notify delivers the page owner's demotion notification. Nil skips notification.
	Notify Notify
	// Logger receives failure lines. Nil means slog.Default().
	Logger *slog.Logger
	// Now overrides the clock in tests. Nil means time.Now.
	Now func() time.Time
	// DemoteInterval is how often the verified_until job re-runs after boot. Zero means
	// 24h (architecture §11: on boot and every 24h).
	DemoteInterval time.Duration
}

// New builds the service.
func New(opts Options) *Service {
	logger := opts.Logger
	if logger == nil {
		logger = slog.Default()
	}
	now := opts.Now
	if now == nil {
		now = time.Now
	}
	tick := opts.DemoteInterval
	if tick == 0 {
		tick = 24 * time.Hour
	}
	return &Service{
		st: opts.Store, res: opts.Resolver, audit: opts.Audit, bus: opts.Bus,
		notify: opts.Notify, logger: logger, now: now, tick: tick,
	}
}
