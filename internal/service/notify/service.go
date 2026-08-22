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
// S24 ships the escalation path and the badge; the full inbox page, read/dismiss UX and
// browser push tiers are S36.
package notify

import (
	"context"
	"errors"
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
	userID, err := s.routeTo(ctx, run)
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

// routeTo resolves the delegating human (architecture §12): requested_by → ticket assignee
// → project owner.
func (s *Service) routeTo(ctx context.Context, run domain.Run) (string, error) {
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
