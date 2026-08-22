// Package notify is the attention service (story S24; architecture §12): the escalation
// ticker that turns an unanswered elicitation into a notification row for the delegating
// human (interaction rule 11 — inline when watched, escalate to the inbox when not), and the
// HTTP surface the inbox badge reads. One notification row per (user, run), updated in
// place — never stacked (interaction rule 3).
//
// Routing (brief D1): the delegating human — runs.requested_by_user_id, falling back to the
// ticket's assignee, falling back to the project owner. Never "everyone". (Architecture §12
// also names "the ticket's delegating human" for trigger-spawned runs; the schema does not
// model a per-ticket delegating human — tickets.delegate_agent_id is an agent — so the
// assignee is the first ticket-level fallback.)
//
// S24 shipped the escalation path and the badge. S36 completes the surface: the run-state
// subscriber below rewrites a run's notification in place when it ends (the flavor changes,
// the row never stacks), and the browser push tiers are computed client-side from the
// flavor (see Subscribe).
package notify

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/spruce/lexicode/internal/domain"
	"github.com/spruce/lexicode/internal/kernel/bus"
	"github.com/spruce/lexicode/internal/kernel/store"
)

// DefaultEscalateAfter is how long an elicitation may sit unanswered before it escalates to
// a notification (interaction rule 11).
const DefaultEscalateAfter = 60 * time.Second

// DefaultInterval is the escalation scan cadence.
const DefaultInterval = 10 * time.Second

// Options configures New.
type Options struct {
	Store  *store.Store
	Bus    *bus.Bus
	Logger *slog.Logger
	// EscalateAfter overrides DefaultEscalateAfter (tests).
	EscalateAfter time.Duration
	// Interval overrides DefaultInterval (tests).
	Interval time.Duration
	// Now overrides the clock (tests).
	Now func() time.Time
}

// Service is the notify service.
type Service struct {
	st            *store.Store
	bus           *bus.Bus
	logger        *slog.Logger
	escalateAfter time.Duration
	interval      time.Duration
	now           func() time.Time

	mu      sync.Mutex
	started bool
	done    chan struct{}
}

// New builds the service.
func New(opts Options) *Service {
	logger := opts.Logger
	if logger == nil {
		logger = slog.Default()
	}
	if opts.EscalateAfter <= 0 {
		opts.EscalateAfter = DefaultEscalateAfter
	}
	if opts.Interval <= 0 {
		opts.Interval = DefaultInterval
	}
	if opts.Now == nil {
		opts.Now = time.Now
	}
	return &Service{
		st: opts.Store, bus: opts.Bus, logger: logger,
		escalateAfter: opts.EscalateAfter, interval: opts.Interval, now: opts.Now,
	}
}

// Subscribe registers the run-state listener on the bus (S36) — call before bus.Start so
// boot recovery reaches it. When a run whose notification row exists reaches a terminal
// state, the row is updated IN PLACE — the flavor changes, the row never stacks (the
// UNIQUE(user_id, run_id) index; interaction rule 3).
//
// Tiering (architecture §12): the backend carries only the flavor; the browser decides the
// delivery tier from it — `question` / `approval` / `failure` push (Notification API,
// permission requested at the first occurrence, never on load), `review` (which includes
// "completed — review the output") silently updates the badge. See web/src/lib/push/tier.ts.
func (s *Service) Subscribe(b *bus.Bus) error {
	return b.SubscribeKind("notify.run-state", "run", s.handleRunEvent)
}

// handleRunEvent reacts to run.state frames: a terminal run's existing notification rows
// are rewritten in place with the terminal copy. Rows are only ever updated, never created
// here — a run nobody was notified about completes silently (the needs-you surfaces still
// carry it when it needs review). Idempotent: boot recovery may re-deliver, and a row that
// already carries the terminal copy is left alone whatever its read state.
func (s *Service) handleRunEvent(ctx context.Context, e domain.Event) error {
	if e.ActivityType != "state" || e.SubjectID == nil {
		return nil
	}
	run, err := s.st.Runs().ByID(ctx, *e.SubjectID)
	if errors.Is(err, store.ErrNotFound) {
		return nil // the run vanished (project delete); nothing to update
	}
	if err != nil {
		return err
	}
	if !run.State.Terminal() {
		return nil // the escalation ticker owns the parked states
	}
	rows, err := s.st.Notifications().ForRun(ctx, run.ID)
	if err != nil || len(rows) == 0 {
		return err
	}

	agentName := "An agent"
	if a, err := s.st.Agents().ByID(ctx, run.AgentID); err == nil {
		agentName = a.Name
	}
	now := domain.FormatTime(s.now())

	for _, existing := range rows {
		if run.State == domain.RunCanceled {
			// A human stopped the run; the pending ask is moot. Quiet the row rather than
			// re-raise it.
			if existing.State == domain.NotificationDismissed {
				continue
			}
			if err := s.st.Notifications().MarkState(ctx, existing.ID,
				domain.NotificationDismissed, now); err != nil {
				return err
			}
			existing.State = domain.NotificationDismissed
			existing.UpdatedAt = now
			s.emitUpdated(ctx, existing)
			continue
		}
		flavor, title, body := terminalCopy(run, agentName)
		if existing.Flavor == flavor && existing.Title == title && existing.Body == body {
			continue // already carries this outcome (redelivery); do not flip read → unread
		}
		rid := run.ID
		n := domain.Notification{
			ID: domain.NewID(), UserID: existing.UserID, ProjectID: run.ProjectID,
			RunID: &rid, Flavor: flavor, Title: title, Body: body,
			State: domain.NotificationUnread, CreatedAt: now, UpdatedAt: now,
		}
		// Upsert hits the (user_id, run_id) unique row: the stored id and created_at
		// survive — updated in place, never stacked.
		if err := s.st.Notifications().Upsert(ctx, &n); err != nil {
			return err
		}
		s.emitUpdated(ctx, n)
	}
	return nil
}

// terminalCopy is the in-place rewrite for a terminal run's notification: `completed`
// becomes a review row ("completed" is the badge-only tier — see Subscribe), everything
// else a failure row naming what happened.
func terminalCopy(run domain.Run, agentName string) (domain.NotificationFlavor, string, string) {
	switch run.State {
	case domain.RunCompleted:
		return domain.FlavorReview, agentName + " finished — review the output",
			fmt.Sprintf("Run #%d completed.", run.Seq)
	case domain.RunTimedOut:
		return domain.FlavorFailure, agentName + " timed out",
			fmt.Sprintf("Run #%d hit its wall-clock limit.", run.Seq)
	case domain.RunLoopStopped:
		return domain.FlavorFailure, agentName + " was stopped by loop protection",
			fmt.Sprintf("Run #%d tripped the loop guard.", run.Seq)
	default: // failed
		body := fmt.Sprintf("Run #%d failed.", run.Seq)
		if run.ErrorMessage != "" {
			body = run.ErrorMessage
		}
		return domain.FlavorFailure, agentName + " failed", body
	}
}

// Start runs the escalation ticker until ctx ends.
func (s *Service) Start(ctx context.Context) {
	s.mu.Lock()
	if s.started {
		s.mu.Unlock()
		return
	}
	s.started = true
	s.done = make(chan struct{})
	done := s.done
	s.mu.Unlock()

	go func() {
		defer close(done)
		t := time.NewTicker(s.interval)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				s.Escalate(context.WithoutCancel(ctx))
			}
		}
	}()
}

// Wait blocks until the ticker goroutine has exited (shutdown hygiene).
func (s *Service) Wait() {
	s.mu.Lock()
	done := s.done
	s.mu.Unlock()
	if done != nil {
		<-done
	}
}

// Escalate is one scan: every pending elicitation older than the threshold whose run is
// still parked produces (or refreshes, in place) the delegating human's notification row.
// Exported so tests drive it with a fake clock instead of sleeping.
func (s *Service) Escalate(ctx context.Context) {
	cutoff := domain.FormatTime(s.now().Add(-s.escalateAfter))
	pending, err := s.st.Elicitations().PendingOlderThan(ctx, cutoff)
	if err != nil {
		s.logger.Error("notify: escalation scan failed", slog.String("error", err.Error()))
		return
	}
	for _, el := range pending {
		if err := s.escalateOne(ctx, el); err != nil {
			s.logger.Error("notify: escalation failed",
				slog.String("elicitation", el.ID), slog.String("error", err.Error()))
		}
	}
}

func (s *Service) escalateOne(ctx context.Context, el domain.Elicitation) error {
	run, err := s.st.Runs().ByID(ctx, el.RunID)
	if err != nil {
		return err
	}
	if run.State.Terminal() {
		return nil // the scheduler cancels its elicitations; a race here is harmless
	}
	userID, err := s.RouteTo(ctx, run)
	if err != nil {
		return err
	}
	if userID == "" {
		return nil // nobody to notify (ownerless project mid-delete)
	}

	agentName := "An agent"
	if a, err := s.st.Agents().ByID(ctx, run.AgentID); err == nil {
		agentName = a.Name
	}
	flavor := domain.FlavorQuestion
	title := agentName + " asked a question"
	if el.Kind == domain.ElicitationApproval {
		flavor = domain.FlavorApproval
		title = agentName + " is waiting for an approval"
	}
	body := elicitationSummary(ctx, s.st, el)

	// Updated in place, not nagging: a row that already carries this exact ask is left
	// alone whatever its state — reading it must quiet the badge while the same question
	// stays open. Only new content (the agent asked something else) upserts the row back
	// to unread.
	if existing, err := s.st.Notifications().ByUserAndRun(ctx, userID, run.ID); err == nil {
		if existing.Flavor == flavor && existing.Title == title && existing.Body == body {
			return nil
		}
	} else if !errors.Is(err, store.ErrNotFound) {
		return err
	}

	now := domain.FormatTime(s.now())
	rid := run.ID
	n := domain.Notification{
		ID: domain.NewID(), UserID: userID, ProjectID: run.ProjectID, RunID: &rid,
		Flavor: flavor, Title: title, Body: body,
		State: domain.NotificationUnread, CreatedAt: now, UpdatedAt: now,
	}
	if err := s.st.Notifications().Upsert(ctx, &n); err != nil {
		return err
	}
	s.emitUpdated(ctx, n)
	return nil
}

// RouteTo resolves the delegating human for a run (brief D1; architecture §12): requested_by
// → ticket assignee → project owner. Exported since S28: the `notify` trigger action routes
// with exactly this rule, and the actions module receives it as an injected seam
// (cmd/lexicode wiring) rather than importing this service.
func (s *Service) RouteTo(ctx context.Context, run domain.Run) (string, error) {
	if run.RequestedByUserID != nil && *run.RequestedByUserID != "" {
		return *run.RequestedByUserID, nil
	}
	if run.TicketID != nil {
		if tk, err := s.st.Tickets().ByID(ctx, *run.TicketID); err == nil &&
			tk.AssigneeID != nil && *tk.AssigneeID != "" {
			return *tk.AssigneeID, nil
		}
	}
	p, err := s.st.Projects().ByID(ctx, run.ProjectID)
	if err != nil {
		return "", err
	}
	return p.OwnerID, nil
}

// DeliverInApp writes (or refreshes, in place — the Upsert's (user, run) unique row) one
// notification and emits `notification.updated`. This is the in-app delivery the
// module/notify Notifier port impl is injected with (S28): the module cannot import this
// service — module → kernel/ports → domain is the dependency rule — so cmd/lexicode hands it
// this function. ID, state and timestamps are defaulted here so callers supply only content
// and routing.
func (s *Service) DeliverInApp(ctx context.Context, n domain.Notification) error {
	if n.UserID == "" {
		return errors.New("notify: a notification needs a user to deliver to")
	}
	if n.ID == "" {
		n.ID = domain.NewID()
	}
	now := domain.FormatTime(s.now())
	if n.CreatedAt == "" {
		n.CreatedAt = now
	}
	n.UpdatedAt = now
	if n.State == "" {
		n.State = domain.NotificationUnread
	}
	if err := s.st.Notifications().Upsert(ctx, &n); err != nil {
		return err
	}
	s.emitUpdated(ctx, n)
	return nil
}

// elicitationSummary is the notification body: the elicitation's level-0 activity title —
// "Question: …" / "Approval: …" — which the MCP server wrote when it parked the run.
func elicitationSummary(ctx context.Context, st *store.Store, el domain.Elicitation) string {
	if a, err := st.Activities().ByRunSeq(ctx, el.RunID, el.ActivitySeq); err == nil {
		return a.Title
	}
	if el.Kind == domain.ElicitationApproval {
		return "An approval is waiting."
	}
	return "A question is waiting."
}

// emitUpdated publishes the notification.updated frame on the inbox topic (contracts §5.1).
func (s *Service) emitUpdated(ctx context.Context, n domain.Notification) {
	if s.bus == nil {
		return
	}
	pid, nid := n.ProjectID, n.ID
	e := domain.Event{
		ProjectID: &pid, Kind: "notification", ActivityType: "updated",
		SubjectKind: "notification", SubjectID: &nid,
		ActorKind:  domain.ActorSystem,
		Payload:    mustJSON(map[string]any{"notification": notificationBody(n)}),
		OccurredAt: domain.FormatTime(s.now()),
	}
	if err := s.bus.Emit(context.WithoutCancel(ctx), e); err != nil {
		s.logger.Error("notify: emit failed", slog.String("error", err.Error()))
	}
}
