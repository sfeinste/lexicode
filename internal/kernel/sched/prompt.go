package sched

import (
	"context"
	"fmt"
	"log/slog"
	"strings"

	"github.com/spruce/lexicode/internal/domain"
	"github.com/spruce/lexicode/internal/kernel/ports"
)

// resolvedItem is one provider contribution plus the provider that made it — the shape both
// resolution surfaces share.
type resolvedItem struct {
	ports.ContextItem
	Provider string
}

// resolveContext is THE context resolver (architecture §11): every registered provider, in
// Priority order, against one ContextRequest. Both surfaces go through here — assemblePrompt
// at enqueue and PreviewContext for the agent detail's dry preview — which is what makes
// "the preview and an actual run agree token-for-token" structurally true rather than a
// convention (one code path, three render surfaces).
func (s *Scheduler) resolveContext(ctx context.Context, creq ports.ContextRequest) ([]resolvedItem, error) {
	var out []resolvedItem
	for _, p := range s.providersInOrder() {
		resolved, err := p.Resolve(ctx, creq)
		if err != nil {
			return nil, fmt.Errorf("sched: context provider %s: %w", p.ID(), err)
		}
		for _, it := range resolved {
			out = append(out, resolvedItem{ContextItem: it, Provider: p.ID()})
		}
	}
	return out, nil
}

// toRunContextItems maps resolved items onto run_context_items rows, stamping position
// (1-based stack order). RunID and ID are stamped by the caller when the rows persist.
func toRunContextItems(resolved []resolvedItem) []domain.RunContextItem {
	items := make([]domain.RunContextItem, 0, len(resolved))
	for i, it := range resolved {
		items = append(items, domain.RunContextItem{
			Provider:   it.Provider,
			SourceKind: it.SourceKind,
			SourceRef:  it.SourceRef,
			Title:      it.Title,
			Reason:     it.Reason,
			Tokens:     int64(it.Tokens),
			Position:   int64(i + 1),
			Injected:   it.Injected,
		})
	}
	return items
}

// PreviewContext is the dry resolve behind GET /agents/{id}/context-preview — "what every
// run of this agent sees" (architecture §11; UI spec §5.8). Same resolver, same providers,
// same order as a real enqueue; the request simply has no ticket, no changed paths and no
// task summary, so the ticket provider yields nothing and the wiki provider's `paths`/`auto`
// scopes match nothing — exactly what "every run" (as opposed to "this run") can be promised.
func (s *Scheduler) PreviewContext(ctx context.Context, projectID, agentID string) ([]domain.RunContextItem, error) {
	resolved, err := s.resolveContext(ctx, ports.ContextRequest{
		ProjectID: projectID, AgentID: agentID, Dry: true,
	})
	if err != nil {
		return nil, err
	}
	return toRunContextItems(resolved), nil
}

// assemblePrompt renders the run's prompt (architecture §10.4) and records where every piece
// came from. Order, each in a labelled section:
//
//  1. the agent directive (the snapshot taken at enqueue),
//  2. every registered ContextProvider's injected items, in provider Priority order —
//     `project` (guidance) 10 · `wiki` 20 · `ticket` 30 · `repofiles` 40 (listed, never
//     injected — D-11),
//  3. the request's prompt override, as the final "Task" section.
//
// The returned items are the run_context_items rows (RunID/ID stamped by the caller).
func (s *Scheduler) assemblePrompt(ctx context.Context, agent domain.Agent, ticket *domain.Ticket, directive string, req RunRequest) (string, []domain.RunContextItem, error) {
	var b strings.Builder
	if strings.TrimSpace(directive) != "" {
		b.WriteString("# Agent directive\n\n")
		b.WriteString(strings.TrimSpace(directive))
		b.WriteString("\n")
	}

	creq := ports.ContextRequest{
		ProjectID:    req.ProjectID,
		AgentID:      agent.ID,
		TicketID:     req.TicketID,
		ChangedPaths: req.ChangedPaths,
	}
	if ticket != nil {
		creq.TaskSummary = ticket.Title
	}
	if req.Reason != "" {
		if creq.TaskSummary != "" {
			creq.TaskSummary += " — "
		}
		creq.TaskSummary += req.Reason
	}
	// The causing event, for the providers that react to what happened (the `review`
	// provider assembles the whole review a review fragment belongs to). A read failure is
	// not fatal: those providers simply contribute nothing, exactly as for a manual run.
	if req.CauseEventID != "" {
		ev, err := s.st.Events().ByID(ctx, req.CauseEventID)
		if err != nil {
			s.logger.Warn("sched: causing event unreadable for context resolution",
				slog.String("event", req.CauseEventID), slog.String("error", err.Error()))
		} else {
			creq.CausingEvent = &ev
		}
	}

	resolved, err := s.resolveContext(ctx, creq)
	if err != nil {
		return "", nil, err
	}
	for _, it := range resolved {
		if !it.Injected || strings.TrimSpace(it.Body) == "" {
			continue
		}
		if b.Len() > 0 {
			b.WriteString("\n")
		}
		b.WriteString("# " + it.Title + "\n\n")
		b.WriteString(strings.TrimSpace(it.Body))
		b.WriteString("\n")
	}
	items := toRunContextItems(resolved)

	// §10.7: when a previous run on this ticket was taken over, the human's note is
	// injected into this run's prompt — the prompt IS the run's first stdin message
	// (contracts §3.1), so a labelled section here is "the first message of the next run".
	if ticket != nil {
		note, seq, err := s.st.Runs().LatestTakeoverNote(ctx, ticket.ID)
		if err != nil {
			return "", nil, fmt.Errorf("sched: reading takeover note: %w", err)
		}
		if strings.TrimSpace(note) != "" {
			if b.Len() > 0 {
				b.WriteString("\n")
			}
			fmt.Fprintf(&b, "# Human takeover\n\nA human took over run #%d on this ticket and worked on its branch directly. Their note:\n\n%s\n",
				seq, strings.TrimSpace(note))
		}
	}

	if strings.TrimSpace(req.PromptOverride) != "" {
		if b.Len() > 0 {
			b.WriteString("\n")
		}
		b.WriteString("# Task\n\n")
		b.WriteString(strings.TrimSpace(req.PromptOverride))
		b.WriteString("\n")
	}
	return b.String(), items, nil
}

// EstimateTokens is the crude chars/4 heuristic every context surface shares.
func EstimateTokens(text string) int {
	return (len(text) + 3) / 4
}

// AlwaysReason is the exact reason string the wiki provider stamps on always-scoped pages
// (architecture §11's provider table) — the budget check keys on it, so the constant lives
// where both sides can share it.
const AlwaysReason = "always"

// warnContextBudget enforces the project context budget (architecture §11 step 3): if the
// `always`-scoped items alone exceed the project's threshold, the run proceeds and a
// level-1 system warning is appended to its transcript. Best-effort — a failure to warn
// never fails the enqueue.
func (s *Scheduler) warnContextBudget(ctx context.Context, run domain.Run, items []domain.RunContextItem) {
	var always int64
	for _, it := range items {
		if it.Reason == AlwaysReason {
			always += it.Tokens
		}
	}
	if always == 0 {
		return
	}
	threshold, err := s.contextThreshold(ctx, run.ProjectID)
	if err != nil {
		s.logger.Error("sched: context threshold lookup failed",
			slog.String("run", run.ID), slog.String("error", err.Error()))
		return
	}
	if threshold <= 0 || always <= threshold {
		return
	}
	a := domain.Activity{
		RunID: run.ID, Type: domain.ActivitySystem, Level: 1, GroupKey: "system",
		Title: fmt.Sprintf("Context budget exceeded: always-on pages total ~%d tokens (threshold %d)",
			always, threshold),
		Payload: mustJSON(map[string]any{
			"warning": "context_budget_exceeded", "always_tokens": always,
			"threshold_tokens": threshold,
		}),
		Attempt: 1, CreatedAt: s.now(),
	}
	if err := s.st.Activities().AppendNext(ctx, &a); err != nil {
		s.logger.Error("sched: context budget warning append failed",
			slog.String("run", run.ID), slog.String("error", err.Error()))
	}
}

// contextThreshold is the effective context budget for a project: its own
// context_threshold_tokens, or the workspace default when the project inherits.
func (s *Scheduler) contextThreshold(ctx context.Context, projectID string) (int64, error) {
	p, err := s.st.Projects().ByID(ctx, projectID)
	if err != nil {
		return 0, err
	}
	if p.ContextThresholdTokens != nil {
		return *p.ContextThresholdTokens, nil
	}
	ws, err := s.st.Workspace().Get(ctx)
	if err != nil {
		return 0, err
	}
	return ws.DefaultContextThresholdTokens, nil
}
