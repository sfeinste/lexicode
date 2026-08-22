// module.go registers the V1 context providers (architecture §3.1, contracts §2.6). S22
// ships `project` (priority 10) and `ticket` (priority 30) — the two prompt assembly cannot
// run without; `wiki` (20) and `repofiles` (40) join with S34.
package contextmod

import (
	"context"

	"github.com/spruce/lexicode/internal/kernel"
	"github.com/spruce/lexicode/internal/kernel/store"
)

// Options configures New.
type Options struct {
	// Store is where the providers read projects, tickets and acceptance criteria. Required.
	Store *store.Store
}

// Module is the context-provider module.
type Module struct {
	opts Options
}

// New builds the module.
func New(opts Options) *Module { return &Module{opts: opts} }

// Name implements kernel.Module.
func (m *Module) Name() string { return "context" }

// Init implements kernel.Module: register the providers. No I/O.
func (m *Module) Init(k *kernel.Kernel) error {
	if err := k.RegisterContextProvider(NewProjectProvider(m.opts.Store)); err != nil {
		return err
	}
	return k.RegisterContextProvider(NewTicketProvider(m.opts.Store))
}

// Start implements kernel.Module: nothing runs in the background.
func (m *Module) Start(context.Context) error { return nil }

// Stop implements kernel.Module.
func (m *Module) Stop(context.Context) error { return nil }
