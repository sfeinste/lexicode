// delegate.go is the ticket↔run surface that lands with the S22 scheduler: the delegate
// endpoint (contracts §5: POST /tickets/{id}/delegate → enqueues a run) and the category
// mover the scheduler's board coupling drives (§10.4: on start the ticket moves to the
// running column, on a PR to the review column — category lookups, never names).
package tickets

import (
	"context"
	"errors"

	"github.com/spruce/lexicode/internal/domain"
	"github.com/spruce/lexicode/internal/kernel/audit"
	"github.com/spruce/lexicode/internal/kernel/auth"
	"github.com/spruce/lexicode/internal/kernel/httpx"
	"github.com/spruce/lexicode/internal/kernel/sched"
	"github.com/spruce/lexicode/internal/kernel/store"
)

// DelegateInput is a POST /tickets/{id}/delegate body.
type DelegateInput struct {
	AgentID string
	Prompt  string // optional extra instruction, becomes the prompt's Task section
}

// Delegate asks the scheduler for a run of agent on this ticket, recording the agent as the
// ticket's delegate when it was not already. Returns the created run's ID.
func (s *Service) Delegate(ctx context.Context, ticketID string, in DelegateInput) (string, []httpx.FieldError, error) {
	tk, err := s.st.Tickets().ByID(ctx, ticketID)
	if err != nil {
		return "", nil, err
	}
	if tk.ArchivedAt != nil {
		return "", nil, &ArchivedError{TicketKey: tk.Key}
	}
	if in.AgentID == "" {
		return "", []httpx.FieldError{{Field: "agent_id", Message: "An agent is required."}}, nil
	}
	delegate, ferr, err := s.resolveDelegate(ctx, tk.ProjectID, in.AgentID)
	if err != nil || len(ferr) > 0 {
		return "", ferr, err
	}

	// Delegating from the picker also records the delegate on the ticket (D1's agent axis),
	// exactly as the field editor would.
	if tk.DelegateAgentID == nil || *tk.DelegateAgentID != *delegate {
		err = s.st.Tx(ctx, func(tx *store.Tx) error {
			tk.DelegateAgentID = delegate
			tk.UpdatedAt = s.now()
			if err := tx.Tickets().Update(ctx, &tk); err != nil {
				return err
			}
			return s.appendStream(ctx, tx, tk.ID, domain.StreamFieldChange, map[string]any{
				"event": "delegated", "delegate_agent_id": *delegate,
			})
		})
		if err != nil {
			return "", nil, err
		}
	}

	req := sched.RunRequest{
		ProjectID:      tk.ProjectID,
		AgentID:        *delegate,
		TicketID:       tk.ID,
		Reason:         "delegate button",
		PromptOverride: in.Prompt,
	}
	if a, ok := auth.ActorFrom(ctx); ok && a.Kind == domain.ActorHuman {
		req.RequestedByUserID = a.ID
	}
	runID, err := s.sched.RequestRun(ctx, req)
	note := "run requested"
	if err != nil {
		note = "scheduler refused: " + err.Error()
	}
	if aerr := s.audit.Write(ctx, "ticket.delegate_run",
		audit.Target{Kind: "ticket", ID: tk.ID, ProjectID: tk.ProjectID, Note: note},
		nil, req); aerr != nil {
		return "", nil, aerr
	}
	if err != nil {
		return "", nil, err
	}
	return runID, nil, nil
}

// MoveTicketToCategory implements the scheduler's sched.TicketMover seam: place the ticket at
// the end of the project's first column of the category (by position). Already-there is a
// no-op. Deliberately NOT the drag path: no auto-start re-entry — the scheduler moving a
// ticket into a running column must not enqueue a second run.
func (s *Service) MoveTicketToCategory(ctx context.Context, ticketID string, cat domain.ColumnCategory, note string) error {
	_, _, err := s.moveToCategory(ctx, ticketID, cat, note)
	return err
}

// TriggerMoveToCategory is the move_ticket trigger action's seam (S28). Same category move as
// the scheduler's, with the two differences the action's contract demands:
//
//   - A ticket with an unresolved triage item is invisible to move_ticket (data model §10.7 /
//     §10, invariant 7): the move is refused with the typed PendingTriageError.
//   - Brief D3's one exception applies to ANY move, this one included: when the destination
//     column auto-starts delegates and the ticket has one, the run request goes through the
//     scheduler seam and is audited — exactly like the drag path. (The scheduler's own
//     MoveTicketToCategory deliberately skips this, but only to avoid re-entry when the
//     scheduler itself is the mover.)
//
// moved=false with a nil error means the ticket was already in a column of the category (or
// is archived) — the action reports it as `no_action` rather than a lie of success.
func (s *Service) TriggerMoveToCategory(ctx context.Context, ticketID string, cat domain.ColumnCategory, note string) (moved bool, err error) {
	tk, err := s.st.Tickets().ByID(ctx, ticketID)
	if err != nil {
		return false, err
	}
	if it, err := s.st.Triage().ByTicket(ctx, tk.ID); err == nil && it.Unresolved() {
		return false, &PendingTriageError{TicketKey: tk.Key}
	} else if err != nil && !errors.Is(err, store.ErrNotFound) {
		return false, err
	}
	res, moved, err := s.moveToCategory(ctx, ticketID, cat, note)
	if err != nil || !moved {
		return moved, err
	}
	s.maybeAutoStart(ctx, res)
	return true, nil
}

// moveToCategory is the shared category-move core: resolve the destination, move, stream,
// audit, emit. moved=false means the no-op cases (archived, already there).
func (s *Service) moveToCategory(ctx context.Context, ticketID string, cat domain.ColumnCategory, note string) (moveResult, bool, error) {
	tk, err := s.st.Tickets().ByID(ctx, ticketID)
	if err != nil {
		return moveResult{}, false, err
	}
	if tk.ArchivedAt != nil {
		return moveResult{}, false, nil // an archived ticket stays where history left it
	}
	fromCol, err := s.st.Columns().ByID(ctx, tk.ColumnID)
	if err != nil {
		return moveResult{}, false, err
	}
	if fromCol.Category == cat {
		return moveResult{}, false, nil
	}
	cols, err := s.st.Columns().ByCategory(ctx, tk.ProjectID, cat)
	if err != nil {
		return moveResult{}, false, err
	}
	if len(cols) == 0 {
		return moveResult{}, false, &NoColumnOfCategoryError{Category: cat}
	}
	dest := cols[0]

	before := tk
	err = s.st.Tx(ctx, func(tx *store.Tx) error {
		last, err := lastPosition(ctx, tx, dest.ID)
		if err != nil {
			return err
		}
		pos := domain.PositionBetween(last, 0)
		now := s.now()
		if err := tx.Tickets().Move(ctx, tk.ID, dest.ID, pos, now); err != nil {
			return err
		}
		tk.ColumnID, tk.Position, tk.UpdatedAt = dest.ID, pos, now
		return s.appendStream(ctx, tx, tk.ID, domain.StreamStatusChange, map[string]any{
			"event":          "moved",
			"from_column_id": fromCol.ID, "from_category": string(fromCol.Category),
			"to_column_id": dest.ID, "to_category": string(dest.Category),
			"note": note,
		})
	})
	if err != nil {
		return moveResult{}, false, err
	}
	if err := s.audit.Write(ctx, "ticket.move",
		audit.Target{Kind: "ticket", ID: tk.ID, ProjectID: tk.ProjectID, Note: note},
		map[string]any{"column_id": before.ColumnID, "position": before.Position},
		map[string]any{"column_id": tk.ColumnID, "position": tk.Position},
	); err != nil {
		return moveResult{}, false, err
	}
	s.emitTicket(ctx, "moved", tk, map[string]any{
		"from_category": string(fromCol.Category),
		"to_category":   string(dest.Category),
	})
	return moveResult{before: before, after: tk, fromCol: fromCol, toCol: dest}, true, nil
}
