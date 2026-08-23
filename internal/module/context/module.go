// module.go registers the V1 context providers (architecture §3.1, contracts §2.6):
// architecture §11's four — `project` (10) · `wiki` (20) · `ticket` (30) · `repofiles` (40)
// — plus `event` (25), the causing event of a trigger-spawned run; `pr_history` (26), what
// happened on that event's pull request before it; and `review` (35), which assembles the
// whole review behind a run caused by a review fragment (LEXI-10).
//
// `pr_history` and `review` divide the same subject and must not repeat each other:
// `review` renders the review the run is answering, whole; `pr_history` lists what came
// before it, excluding that review's own fragments.
package contextmod

import (
	"context"
	"log/slog"

	"github.com/spruce/lexicode/internal/kernel"
	"github.com/spruce/lexicode/internal/kernel/secrets"
	"github.com/spruce/lexicode/internal/kernel/store"
)

// Options configures New.
type Options struct {
	// Store is where the providers read projects, wiki pages, tickets and acceptance
	// criteria. Required.
	Store *store.Store
	// Secrets reads the repo token the repofiles provider enumerates with. Nil disables
	// the enumeration (the provider yields nothing).
	Secrets *secrets.Store
	// Docs is the forge doc-detection seam (bootstrap.DocLister's twin, declared in this
	// package) the repofiles provider lists instruction files through. Nil disables the
	// enumeration.
	Docs DocLister
	// Reviews is the forge seam the `review` provider assembles a whole review through.
	// Nil leaves the provider with only what the causing event carried.
	Reviews ReviewReader
	// Logger receives repofiles' non-fatal enumeration warnings. Nil means slog.Default().
	Logger *slog.Logger
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
	if err := k.RegisterContextProvider(NewWikiProvider(m.opts.Store)); err != nil {
		return err
	}
	if err := k.RegisterContextProvider(NewEventProvider(m.opts.Store)); err != nil {
		return err
	}
	if err := k.RegisterContextProvider(NewPRHistoryProvider(m.opts.Store)); err != nil {
		return err
	}
	if err := k.RegisterContextProvider(NewTicketProvider(m.opts.Store)); err != nil {
		return err
	}
	if err := k.RegisterContextProvider(
		NewReviewProvider(m.opts.Store, m.opts.Secrets, m.opts.Reviews, m.opts.Logger)); err != nil {
		return err
	}
	return k.RegisterContextProvider(
		NewRepoFilesProvider(m.opts.Store, m.opts.Secrets, m.opts.Docs, m.opts.Logger))
}

// Start implements kernel.Module: nothing runs in the background.
func (m *Module) Start(context.Context) error { return nil }

// Stop implements kernel.Module.
func (m *Module) Stop(context.Context) error { return nil }
