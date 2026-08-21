package sched

import (
	"context"
	"errors"
)

// ErrNotImplemented is what the placeholder Requester returns for every call. Callers that are
// allowed to exist before S22 (the S10 column auto-start path, the S10 archive cancellation)
// treat it as "the scheduler is not here yet": they audit the intent and carry on. Callers that
// only make sense with a live scheduler must surface it.
var ErrNotImplemented = errors.New("sched: run scheduler not implemented until S22")

// RunRequest is the intent a caller hands the scheduler. It is deliberately smaller than a run
// row: everything else — seq, prompt assembly, directive snapshot, admission, state — is the
// scheduler's business (D-14), decided at dequeue time, not the caller's.
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
}

// Requester is the seam between everything that may *want* a run and the one component allowed
// to *start* one (D-14: modules request runs; the kernel decides). S10 wires the tickets
// service to it for column auto-start and archive-time cancellation; S22 builds the real
// Scheduler behind it. Until then the only implementation is Unscheduled.
type Requester interface {
	// RequestRun asks for a run. The scheduler owns admission (concurrency caps, WIP
	// governor, budget) — a nil error means "queued", not "running".
	RequestRun(ctx context.Context, req RunRequest) error
	// CancelTicketRuns cancels every active run coupled to a ticket, recording reason as the
	// runs' state_reason ("ticket archived", D-15), and returns how many were cancelled.
	CancelTicketRuns(ctx context.Context, ticketID, reason string) (int64, error)
}

// Unscheduled is the documented no-op Requester wired until S22 lands: every call returns
// ErrNotImplemented and touches nothing. It exists so the call sites, their audit entries and
// their tests are real now, and S22 only swaps the implementation.
type Unscheduled struct{}

// RequestRun always returns ErrNotImplemented; no run is created or queued.
func (Unscheduled) RequestRun(context.Context, RunRequest) error { return ErrNotImplemented }

// CancelTicketRuns always returns (0, ErrNotImplemented); nothing to cancel exists before S22.
func (Unscheduled) CancelTicketRuns(context.Context, string, string) (int64, error) {
	return 0, ErrNotImplemented
}
