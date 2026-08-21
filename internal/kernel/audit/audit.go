// Package audit writes and serves the audit log (story S06, architecture §14): every mutation
// through a service records who did what to what, with before/after snapshots. The actor comes
// from the request context (auth.WithActor / RequireAuth), so attribution is impossible to
// forget — a service never passes an actor by hand.
package audit

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"

	"github.com/spruce/lexicode/internal/domain"
	"github.com/spruce/lexicode/internal/kernel/auth"
	"github.com/spruce/lexicode/internal/kernel/store"
)

// Writer appends to audit_log. One per process, shared through Kernel.Audit().
type Writer struct {
	st     *store.Store
	logger *slog.Logger
	now    func() string
}

// Options configures New.
type Options struct {
	// Store is the open, migrated database. Required.
	Store *store.Store
	// Logger receives write-failure lines. Nil means slog.Default().
	Logger *slog.Logger
	// Now overrides the clock in tests. Nil means domain.Now.
	Now func() string
}

// New builds a writer.
func New(opts Options) *Writer {
	logger := opts.Logger
	if logger == nil {
		logger = slog.Default()
	}
	now := opts.Now
	if now == nil {
		now = domain.Now
	}
	return &Writer{st: opts.Store, logger: logger, now: now}
}

// Target names what a mutation touched. ProjectID scopes the entry to a project's audit view;
// leave it empty for workspace-level targets (users, settings).
type Target struct {
	Kind      string // "ticket", "agent", "project", …  (audit_log.target_kind)
	ID        string // the target's ULID or key
	ProjectID string // owning project's ID, or "" for workspace-level entries
	Note      string // optional human note ("moved by trigger t-…")
}

// Write appends one entry: action is the dotted verb ("ticket.move", "agent.directive.update"),
// before and after are snapshots of the target around the mutation. Either may be nil — nil
// marshals to SQL NULL, meaning "not captured", which is what a create's before and a delete's
// after are. A json.RawMessage is stored as-is.
//
// The actor is read from ctx (auth.ActorFrom). A context with no actor writes a system entry —
// boot tasks and seeds are system actions — but request paths always carry one, because
// RequireAuth sets it.
func (w *Writer) Write(ctx context.Context, action string, target Target, before, after any) error {
	if action == "" || target.Kind == "" || target.ID == "" {
		return fmt.Errorf("audit: action, target kind and target id are all required (got %q, %q, %q)",
			action, target.Kind, target.ID)
	}
	beforeJSON, err := snapshot(before)
	if err != nil {
		return fmt.Errorf("audit: marshal before snapshot for %s: %w", action, err)
	}
	afterJSON, err := snapshot(after)
	if err != nil {
		return fmt.Errorf("audit: marshal after snapshot for %s: %w", action, err)
	}

	e := domain.AuditEntry{
		ID:         domain.NewID(),
		ActorKind:  domain.ActorSystem,
		Action:     action,
		TargetKind: target.Kind,
		TargetID:   target.ID,
		Before:     beforeJSON,
		After:      afterJSON,
		Note:       target.Note,
		CreatedAt:  w.now(),
	}
	if target.ProjectID != "" {
		p := target.ProjectID
		e.ProjectID = &p
	}
	if a, ok := auth.ActorFrom(ctx); ok {
		e.ActorKind = a.Kind
		if a.ID != "" {
			id := a.ID
			e.ActorID = &id
		}
	}

	if err := w.st.Audit().Append(ctx, &e); err != nil {
		w.logger.Error("audit: append failed",
			slog.String("action", action),
			slog.String("target", target.Kind+":"+target.ID),
			slog.String("error", err.Error()))
		return fmt.Errorf("audit: append %s: %w", action, err)
	}
	return nil
}

// snapshot marshals a before/after value nil-safely: a nil any, a nil typed pointer and a nil
// RawMessage all become nil (SQL NULL); everything else is its JSON.
func snapshot(v any) (json.RawMessage, error) {
	if v == nil {
		return nil, nil
	}
	if raw, ok := v.(json.RawMessage); ok {
		if len(raw) == 0 {
			return nil, nil
		}
		return raw, nil
	}
	b, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	// A nil typed pointer marshals to "null"; store NULL instead so "not captured" has one
	// representation.
	if string(b) == "null" {
		return nil, nil
	}
	return json.RawMessage(b), nil
}
