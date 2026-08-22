package cron

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/spruce/lexicode/internal/domain"
	"github.com/spruce/lexicode/internal/kernel"
	"github.com/spruce/lexicode/internal/kernel/bus"
	"github.com/spruce/lexicode/internal/kernel/ports"
	"github.com/spruce/lexicode/internal/kernel/store"
)

// moduleName is the kernel module name.
const moduleName = "cron"

// Options configures New. The zero value is what cmd/lexicode passes: everything nil is
// wired from the kernel in Init. Non-nil values win — that is how tests inject a store, a
// fake clock and a capture emit without a kernel.
type Options struct {
	// Store overrides the kernel store (tests).
	Store *store.Store
	// Logger overrides the kernel logger (tests capture output through it).
	Logger *slog.Logger
	// Now overrides the clock (tests). Nil means time.Now.
	Now func() time.Time
	// Lookback bounds the catch-up search for a missed firing. Zero means 366 days.
	Lookback time.Duration
}

// Module is the cron module: the kernel Module lifecycle around the one Source it registers.
type Module struct {
	src  *Source
	emit ports.Emit // wired from the kernel bus in Init; Start hands it to the source
}

// New builds the module. See Options for what may be left zero.
func New(opts Options) *Module {
	src := newSource()
	if opts.Store != nil {
		src.store = opts.Store
	}
	if opts.Logger != nil {
		src.logger = opts.Logger.With("module", moduleName)
	}
	if opts.Now != nil {
		src.now = opts.Now
	}
	if opts.Lookback > 0 {
		src.lookback = opts.Lookback
	}
	return &Module{src: src}
}

// Name implements kernel.Module.
func (m *Module) Name() string { return moduleName }

// Source returns the concrete event source, for tests and the one wiring site.
func (m *Module) Source() *Source { return m.src }

// Init registers the event source and wires what was not injected: the store, the logger,
// and the bus-publish emit (idempotent on the dedupe key — a duplicate is the catch-up
// overlap, not a failure). No I/O happens here.
func (m *Module) Init(k *kernel.Kernel) error {
	if m.src.store == nil {
		m.src.store = k.Store()
	}
	if m.src.logger == nil || m.src.logger == slog.Default() {
		m.src.logger = k.Logger().With("module", moduleName)
	}
	if b := k.Bus(); b != nil {
		m.emit = func(ctx context.Context, e domain.Event) error {
			if err := b.Publish(ctx, e); err != nil && !errors.Is(err, bus.ErrDuplicate) {
				return err
			}
			return nil
		}
	}
	return k.RegisterEventSource(m.src)
}

// Start implements kernel.Module: the minute scan loop, with the bus-publish emit wired in
// Init. Without a store or bus (registration-only tests) there is nothing to scan and Start
// is a no-op.
func (m *Module) Start(ctx context.Context) error {
	if m.src.store == nil || m.emit == nil {
		return nil
	}
	return m.src.Start(ctx, m.emit)
}

// Stop implements kernel.Module: drains the scan loop.
func (m *Module) Stop(ctx context.Context) error { return m.src.Stop(ctx) }

// compile-time checks: the module fits the kernel lifecycle; the source fits both ports.
var (
	_ kernel.Module       = (*Module)(nil)
	_ ports.EventSource   = (*Source)(nil)
	_ ports.TriggerVetter = (*Source)(nil)
)
