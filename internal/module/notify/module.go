package notify

import (
	"context"
	"errors"

	"github.com/spruce/lexicode/internal/domain"
	"github.com/spruce/lexicode/internal/kernel"
)

// Options configures New.
type Options struct {
	// Deliver writes (or refreshes in place) the notification row and emits
	// `notification.updated` — wired in cmd/lexicode to the S24 notify service's
	// DeliverInApp. Required.
	Deliver func(ctx context.Context, n domain.Notification) error
}

// Module is the notify module. Construct with New, register with kernel.RegisterModule.
type Module struct {
	opts Options
}

// New builds the module.
func New(opts Options) *Module { return &Module{opts: opts} }

// Name implements kernel.Module.
func (m *Module) Name() string { return "notify" }

// Init registers the in-app notifier. No I/O.
func (m *Module) Init(k *kernel.Kernel) error {
	return k.RegisterNotifier(&InApp{deliver: m.opts.Deliver})
}

// Start implements kernel.Module. The module has no background work.
func (m *Module) Start(context.Context) error { return nil }

// Stop implements kernel.Module.
func (m *Module) Stop(context.Context) error { return nil }

// InApp is the "inapp" ports.Notifier: delivery is the notification row itself — the inbox
// badge, the SSE frame — via the injected service seam. One row per (user, run), updated in
// place, never stacked (interaction rule 3); runless notifications insert plainly.
type InApp struct {
	deliver func(ctx context.Context, n domain.Notification) error
}

// NewInApp builds the notifier directly — the seam tests and module/actions tests use, so
// they need no kernel.
func NewInApp(deliver func(ctx context.Context, n domain.Notification) error) *InApp {
	return &InApp{deliver: deliver}
}

// ID implements ports.Notifier.
func (i *InApp) ID() string { return "inapp" }

// Deliver implements ports.Notifier.
func (i *InApp) Deliver(ctx context.Context, n domain.Notification) error {
	if i.deliver == nil {
		return errors.New("notify: the in-app delivery seam is not wired")
	}
	if n.UserID == "" {
		return errors.New("notify: a notification needs a user to deliver to")
	}
	return i.deliver(ctx, n)
}
