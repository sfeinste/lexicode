package sched

import (
	"context"
	"fmt"
	"strings"

	"github.com/spruce/lexicode/internal/domain"
	"github.com/spruce/lexicode/internal/kernel/ports"
)

// assemblePrompt renders the run's prompt (architecture §10.4) and records where every piece
// came from. Order, each in a labelled section:
//
//  1. the agent directive (the snapshot taken at enqueue),
//  2. every registered ContextProvider's injected items, in provider Priority order —
//     `project` (guidance) at 10 and `ticket` (description + acceptance criteria) at 30 now;
//     `wiki` (20) and `repofiles` (40, listed-not-injected) join in S34,
//  3. the request's prompt override, as the final "Task" section.
//
// The returned items are the run_context_items rows (position is stamped by the caller).
func (s *Scheduler) assemblePrompt(ctx context.Context, agent domain.Agent, ticket *domain.Ticket, directive string, req RunRequest) (string, []domain.RunContextItem, error) {
	var b strings.Builder
	if strings.TrimSpace(directive) != "" {
		b.WriteString("# Agent directive\n\n")
		b.WriteString(strings.TrimSpace(directive))
		b.WriteString("\n")
	}

	creq := ports.ContextRequest{
		ProjectID: req.ProjectID,
		AgentID:   agent.ID,
		TicketID:  req.TicketID,
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

	var items []domain.RunContextItem
	for _, p := range s.providersInOrder() {
		resolved, err := p.Resolve(ctx, creq)
		if err != nil {
			return "", nil, fmt.Errorf("sched: context provider %s: %w", p.ID(), err)
		}
		for _, it := range resolved {
			items = append(items, domain.RunContextItem{
				Provider:   p.ID(),
				SourceKind: it.SourceKind,
				SourceRef:  it.SourceRef,
				Title:      it.Title,
				Reason:     it.Reason,
				Tokens:     int64(it.Tokens),
				Injected:   it.Injected,
			})
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
	}

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
