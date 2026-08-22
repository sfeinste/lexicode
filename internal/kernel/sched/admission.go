package sched

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/spruce/lexicode/internal/domain"
	"github.com/spruce/lexicode/internal/kernel/store"
)

// admitLoop is the single admission goroutine: woken by enqueues and freed slots, ticking as
// a backstop, it re-evaluates every queued run in queue order.
func (s *Scheduler) admitLoop(ctx context.Context) {
	ticker := time.NewTicker(s.opts.AdmitInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-s.wake:
		case <-ticker.C:
		}
		s.admitPass(ctx)
	}
}

// admitCounters is one pass's view of the occupied capacity, updated as runs are admitted so
// twenty simultaneous enqueues cannot all pass the same stale count.
type admitCounters struct {
	byAgent      map[string]int64
	global       int64
	columnTicket map[string]int64 // running-column ID → tickets currently in it
}

// admitPass evaluates every queued run against §10.2's checks, in order, oldest first.
// Failing 1–3 leaves the run queued with hold_reason saying which limit, in words; failing 4
// terminates it (budget exceeded is not a wait, it is an answer).
func (s *Scheduler) admitPass(ctx context.Context) {
	queued, err := s.st.Runs().ByStates(ctx, domain.RunQueued)
	if err != nil {
		s.logger.Error("sched: admission read failed", slog.String("error", err.Error()))
		return
	}
	if len(queued) == 0 {
		return
	}
	ws, err := s.st.Workspace().Get(ctx)
	if err != nil {
		s.logger.Error("sched: admission workspace read failed", slog.String("error", err.Error()))
		return
	}
	byAgent, err := s.st.Runs().ActiveCountByAgent(ctx)
	if err != nil {
		s.logger.Error("sched: admission count failed", slog.String("error", err.Error()))
		return
	}
	global, err := s.st.Runs().ActiveCount(ctx)
	if err != nil {
		s.logger.Error("sched: admission count failed", slog.String("error", err.Error()))
		return
	}
	c := &admitCounters{byAgent: byAgent, global: global, columnTicket: map[string]int64{}}

	for _, run := range queued {
		select {
		case <-ctx.Done():
			return
		default:
		}
		s.admitOne(ctx, run, ws, c)
	}
}

// admitOne runs the four checks for one queued run.
func (s *Scheduler) admitOne(ctx context.Context, run domain.Run, ws domain.WorkspaceSettings, c *admitCounters) {
	agent, err := s.st.Agents().ByID(ctx, run.AgentID)
	if err != nil {
		s.failQueued(ctx, run, "agent missing", "The run's agent no longer exists.")
		return
	}

	// 1 — agent concurrency cap.
	if agent.ConcurrencyCap > 0 && c.byAgent[agent.ID] >= agent.ConcurrencyCap {
		s.hold(ctx, run, fmt.Sprintf("waiting: %s is at its %d-run limit",
			agent.Name, agent.ConcurrencyCap))
		return
	}

	// 2 — the running-category column's WIP limit is ENFORCING (brief §6.4).
	var destColumn *domain.Column
	if run.TicketID != nil {
		col, holdReason, err := s.wipCheck(ctx, run, c)
		if err != nil {
			s.logger.Error("sched: WIP check failed",
				slog.String("run", run.ID), slog.String("error", err.Error()))
			return // transient; retry next pass
		}
		if holdReason != "" {
			s.hold(ctx, run, holdReason)
			return
		}
		destColumn = col
	}

	// 3 — global container cap (workspace setting, default 6).
	if ws.MaxConcurrentContainers > 0 && c.global >= ws.MaxConcurrentContainers {
		s.hold(ctx, run, fmt.Sprintf("waiting: the workspace is at its %d-container limit",
			ws.MaxConcurrentContainers))
		return
	}

	// 4 — budgets. Exceeded is terminal, not a wait (§10.2).
	day := s.day()
	budget := ws.DefaultDailyBudgetCents
	project, err := s.st.Projects().ByID(ctx, run.ProjectID)
	if err == nil && project.DailyBudgetCents != nil {
		budget = *project.DailyBudgetCents
	}
	if budget > 0 {
		spent, err := s.st.Budget().ProjectDay(ctx, day, run.ProjectID)
		if err != nil {
			s.logger.Error("sched: budget read failed", slog.String("error", err.Error()))
			return
		}
		if spent >= budget {
			s.failQueued(ctx, run, "budget exceeded", fmt.Sprintf(
				"Budget exceeded: %s has spent %s of its %s daily budget.",
				project.Name, cents(spent), cents(budget)))
			return
		}
	}
	if agent.DailyCapCents != nil && *agent.DailyCapCents > 0 {
		spent, err := s.st.Budget().AgentDay(ctx, day, agent.ID)
		if err != nil {
			s.logger.Error("sched: budget read failed", slog.String("error", err.Error()))
			return
		}
		if spent >= *agent.DailyCapCents {
			s.failQueued(ctx, run, "budget exceeded", fmt.Sprintf(
				"Budget exceeded: %s has spent %s of its %s daily cap.",
				agent.Name, cents(spent), cents(*agent.DailyCapCents)))
			return
		}
	}

	// Admitted: clear the hold, move to provisioning, occupy the counters, hand off to a
	// supervisor.
	empty := ""
	after, err := s.transition(ctx, run.ID, domain.RunProvisioning,
		store.RunStateUpdate{HoldReason: &empty})
	if err != nil {
		s.logger.Error("sched: admit transition failed",
			slog.String("run", run.ID), slog.String("error", err.Error()))
		return
	}
	c.byAgent[agent.ID]++
	c.global++
	if destColumn != nil {
		c.columnTicket[destColumn.ID]++
	}
	s.startSupervisor(after)
}

// wipCheck resolves the run's destination running-category column and answers whether its WIP
// limit blocks admission. Returns the destination column when the path is clear, or the hold
// reason when it is not.
func (s *Scheduler) wipCheck(ctx context.Context, run domain.Run, c *admitCounters) (*domain.Column, string, error) {
	ticket, err := s.st.Tickets().ByID(ctx, *run.TicketID)
	if err != nil {
		return nil, "", err
	}
	current, err := s.st.Columns().ByID(ctx, ticket.ColumnID)
	if err != nil {
		return nil, "", err
	}
	// The ticket's own column when it is already running-category; else the project's first
	// running-category column by position — a category lookup, never a name (plan rule 3).
	dest := current
	if current.Category != domain.CategoryRunning {
		cols, err := s.st.Columns().ByCategory(ctx, run.ProjectID, domain.CategoryRunning)
		if err != nil {
			return nil, "", err
		}
		if len(cols) == 0 {
			// No running column exists (the board service refuses to delete the last one, so
			// this is a seed-data edge). No WIP to enforce.
			return nil, "", nil
		}
		dest = cols[0]
	}
	if dest.WIPLimit == nil || *dest.WIPLimit <= 0 || dest.ID == current.ID {
		// No limit, or the ticket already occupies the destination — nothing to add.
		return &dest, "", nil
	}
	if _, counted := c.columnTicket[dest.ID]; !counted {
		tickets, err := s.st.Tickets().ForColumn(ctx, dest.ID)
		if err != nil {
			return nil, "", err
		}
		c.columnTicket[dest.ID] = int64(len(tickets))
	}
	if c.columnTicket[dest.ID] >= *dest.WIPLimit {
		return nil, fmt.Sprintf("waiting: %s is at %d/%d",
			dest.Name, c.columnTicket[dest.ID], *dest.WIPLimit), nil
	}
	return &dest, "", nil
}

// hold records why a queued run stays queued, in words, and streams the change — only when
// the reason actually changed, so a busy queue does not chatter.
func (s *Scheduler) hold(ctx context.Context, run domain.Run, reason string) {
	if run.HoldReason == reason {
		return
	}
	if err := s.st.Runs().SetHoldReason(ctx, run.ID, reason); err != nil {
		s.logger.Error("sched: hold reason write failed",
			slog.String("run", run.ID), slog.String("error", err.Error()))
		return
	}
	run.HoldReason = reason
	s.emitRunState(ctx, run)
}

// failQueued terminates a queued run that can never start (budget exceeded, missing agent):
// straight to failed with the reason in words. No container ever existed, so there is nothing
// to tear down or preserve.
func (s *Scheduler) failQueued(ctx context.Context, run domain.Run, reason, message string) {
	if _, err := s.transition(ctx, run.ID, domain.RunFailed, store.RunStateUpdate{
		StateReason:  &reason,
		ErrorMessage: &message,
	}); err != nil {
		s.logger.Error("sched: terminal transition failed",
			slog.String("run", run.ID), slog.String("error", err.Error()))
	}
	if _, err := s.st.RunMessages().DropQueued(ctx, run.ID); err != nil {
		s.logger.Error("sched: dropping steering queue failed",
			slog.String("run", run.ID), slog.String("error", err.Error()))
	}
}

// cents renders integer cents as dollars for hold and failure copy.
func cents(c int64) string {
	return fmt.Sprintf("$%d.%02d", c/100, c%100)
}
