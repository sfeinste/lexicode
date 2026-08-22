package sched

import (
	"context"
	"errors"
)

// ErrNotImplemented is what the placeholder Requester returns for every call. It survives S22
// for the callers that may still be wired against Unscheduled in tests: they treat it as "the
// scheduler is not here", audit the intent and carry on.
var ErrNotImplemented = errors.New("sched: run scheduler not implemented until S22")

// RunRequest is the intent a caller hands the scheduler. It is deliberately smaller than a run
// row: everything else — seq, prompt assembly, directive snapshot, admission, state — is the
// scheduler's business (D-14), decided at enqueue and admission time, not the caller's.
type RunRequest struct {
	// ProjectID scopes the run. Required.
	ProjectID string
	// AgentID names the agent to run. Required.
	AgentID string
	// TicketID couples the run to a ticket. Empty is legal — free-floating runs exist (D-15).
	TicketID string
	// Reason is the human-readable cause, rendered in the run list and the audit trail:
	// "column auto-start", "delegate button", "@mention", "trigger t-…".
	Reason string
	// PromptOverride is appended as the prompt's final "Task" section: the delegate dialog's
	// optional prompt, or a trigger's interpolated prompt override (S28).
	PromptOverride string
	// RequestedByUserID is the delegating human (D1) — the notification target. Empty for
	// trigger-spawned runs.
	RequestedByUserID string
	// TriggerID is set when a trigger action requested the run (S28).
	TriggerID string
	// CauseEventID is the event that spawned this run (architecture §6.3); empty if manual.
	CauseEventID string
	// ParentRunID chains a run onto the run that caused it (loop-guard depth, S27).
	ParentRunID string
	// ChangedPaths is the set of file paths the causing event touched — known for
	// PR-triggered runs, empty otherwise (contracts §2.6). The wiki context provider's
	// `paths`-scoped pages match their globs against these at resolution (S34).
	ChangedPaths []string
	// SubjectKey is the loop-protection subject the guard derived ("pr:219"); empty lets the
	// scheduler fall back to the ticket-derived key. Stored on the run at enqueue so the
	// guard's debounce and cancel-in-progress probes are one indexed query (S27).
	SubjectKey string
	// Depth is the run's position in the causal chain, computed by the guard's depth
	// counter; zero for chain roots and manual runs.
	Depth int64
	// SupersededRunID is the still-active run the guard's cancel-in-progress layer elected
	// to stop (S27). The scheduler cancels it right after this run's row exists — after,
	// because the cancellation reason names this run's seq ("superseded by run #N") — and
	// before waking admission, so cancel-then-admit ordering holds.
	SupersededRunID string
}

// Requester is the seam between everything that may *want* a run and the one component allowed
// to *start* one (D-14: modules request runs; the kernel decides). The Scheduler is the real
// implementation; Unscheduled remains as the documented no-op for tests wired without one.
type Requester interface {
	// RequestRun asks for a run and returns the created run's ID. The scheduler owns
	// admission (concurrency caps, WIP governor, budget) — a nil error means "queued",
	// not "running".
	RequestRun(ctx context.Context, req RunRequest) (string, error)
	// CancelTicketRuns cancels every active run coupled to a ticket, recording reason as the
	// runs' state_reason ("ticket archived", D-15), and returns how many were cancelled.
	CancelTicketRuns(ctx context.Context, ticketID, reason string) (int64, error)
}

// Unscheduled is the documented no-op Requester: every call returns ErrNotImplemented and
// touches nothing. It exists so call sites can be wired and tested without a live scheduler.
type Unscheduled struct{}

// RequestRun always returns ErrNotImplemented; no run is created or queued.
func (Unscheduled) RequestRun(context.Context, RunRequest) (string, error) {
	return "", ErrNotImplemented
}

// CancelTicketRuns always returns (0, ErrNotImplemented); nothing to cancel exists.
func (Unscheduled) CancelTicketRuns(context.Context, string, string) (int64, error) {
	return 0, ErrNotImplemented
}
