package tickets

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"

	"github.com/spruce/lexicode/internal/domain"
	"github.com/spruce/lexicode/internal/kernel/audit"
	"github.com/spruce/lexicode/internal/kernel/sched"
	"github.com/spruce/lexicode/internal/kernel/store"
)

// MoveInput is a POST /tickets/{id}/move body. The destination position is either an explicit
// fractional Position (the contracts §5 shape — an optimistic client that already computed the
// midpoint) or AfterID (the server computes the midpoint): null places the ticket at the top of
// the column, a ticket ID places it immediately after that ticket, and absent appends to the
// end. When float64 midpoints between two neighbours stop moving, the whole destination column
// is renormalised to gap-spaced positions inside the move's transaction.
type MoveInput struct {
	ColumnID string
	Position *float64
	AfterID  OptStr
}

// moveResult carries what the post-commit work needs out of the transaction.
type moveResult struct {
	before, after  domain.Ticket
	fromCol, toCol domain.Column
}

// Move places a ticket in a column at a fractional position. A move NEVER starts a run (brief
// D3): when the destination column has auto_start_delegate and the ticket has a delegate, the
// intent goes through the scheduler seam — audited, and a documented no-op until S22.
func (s *Service) Move(ctx context.Context, id string, in MoveInput) (TicketWithMeta, error) {
	var res moveResult
	err := s.st.Tx(ctx, func(tx *store.Tx) error {
		tk, err := tx.Tickets().ByID(ctx, id)
		if err != nil {
			return err
		}
		if tk.ArchivedAt != nil {
			return &ArchivedError{TicketKey: tk.Key}
		}
		res.before = tk
		if in.ColumnID == "" {
			return fieldErr("column_id", "A destination column is required.")
		}
		toCol, err := tx.Columns().ByID(ctx, in.ColumnID)
		if errors.Is(err, store.ErrNotFound) || (err == nil && toCol.ProjectID != tk.ProjectID) {
			return fieldErr("column_id", "No such column on this board.")
		}
		if err != nil {
			return err
		}
		fromCol, err := tx.Columns().ByID(ctx, tk.ColumnID)
		if err != nil {
			return err
		}
		res.fromCol, res.toCol = fromCol, toCol

		pos, err := s.placeTicket(ctx, tx, tk, toCol.ID, in)
		if err != nil {
			return err
		}
		now := s.now()
		if err := tx.Tickets().Move(ctx, tk.ID, toCol.ID, pos, now); err != nil {
			return err
		}
		moved := tk
		moved.ColumnID, moved.Position, moved.UpdatedAt = toCol.ID, pos, now
		res.after = moved

		// A same-column reorder is audited but writes no stream row — drag ordering is
		// presentation, not ticket history.
		if fromCol.ID != toCol.ID {
			return s.appendStream(ctx, tx, tk.ID, domain.StreamStatusChange, map[string]any{
				"event":          "moved",
				"from_column_id": fromCol.ID, "from_category": string(fromCol.Category),
				"to_column_id": toCol.ID, "to_category": string(toCol.Category),
			})
		}
		return nil
	})
	if err != nil {
		return TicketWithMeta{}, err
	}
	if err := s.audit.Write(ctx, "ticket.move",
		audit.Target{Kind: "ticket", ID: res.after.ID, ProjectID: res.after.ProjectID},
		map[string]any{"column_id": res.before.ColumnID, "position": res.before.Position},
		map[string]any{"column_id": res.after.ColumnID, "position": res.after.Position},
	); err != nil {
		return TicketWithMeta{}, err
	}
	s.emitTicket(ctx, "moved", res.after, map[string]any{
		"from_category": string(res.fromCol.Category),
		"to_category":   string(res.toCol.Category),
	})
	s.maybeAutoStart(ctx, res)
	return s.withMeta(ctx, res.after)
}

// placeTicket computes the moving ticket's new position among the destination column's tickets
// (the mover excluded), renormalising the column when fractional midpoints are exhausted.
func (s *Service) placeTicket(ctx context.Context, tx *store.Tx, tk domain.Ticket, toColumnID string, in MoveInput) (float64, error) {
	if in.Position != nil {
		if *in.Position <= 0 {
			return 0, fieldErr("position", "Positions are positive numbers.")
		}
		return *in.Position, nil
	}

	all, err := tx.Tickets().ForColumn(ctx, toColumnID)
	if err != nil {
		return 0, err
	}
	rest := make([]domain.Ticket, 0, len(all))
	for _, t := range all {
		if t.ID != tk.ID {
			rest = append(rest, t)
		}
	}

	idx := len(rest) // absent AfterID → append to the end
	if in.AfterID.Set {
		if in.AfterID.Null {
			idx = 0
		} else {
			if in.AfterID.Value == tk.ID {
				return 0, fieldErr("after_ticket_id", "A ticket cannot be placed after itself.")
			}
			found := false
			for i, t := range rest {
				if t.ID == in.AfterID.Value {
					idx, found = i+1, true
					break
				}
			}
			if !found {
				return 0, fieldErr("after_ticket_id", "No such ticket in the destination column.")
			}
		}
	}

	var lo, hi float64 // 0 = no neighbour on that side (domain.PositionBetween's sentinel)
	if idx > 0 {
		lo = rest[idx-1].Position
	}
	if idx < len(rest) {
		hi = rest[idx].Position
	}
	pos := domain.PositionBetween(lo, hi)
	if pos > lo && (hi == 0 || pos < hi) {
		return pos, nil
	}

	// Midpoints exhausted: renumber the whole destination column (mover included, at its
	// slot) to gap-spaced positions inside this transaction. One drag stays one request.
	final := append(append(append([]domain.Ticket{}, rest[:idx]...), tk), rest[idx:]...)
	now := s.now()
	var out float64
	for i := range final {
		p := float64(i+1) * domain.PositionGap
		if final[i].ID == tk.ID {
			out = p
			continue
		}
		if err := tx.Tickets().SetPosition(ctx, final[i].ID, p, now); err != nil {
			return 0, err
		}
	}
	return out, nil
}

// maybeAutoStart is the one sanctioned exception to "a move never starts a run" (brief D3):
// the destination column auto-starts delegates and the ticket has one. The request goes
// through the scheduler seam (D-14) and the attempt is audited whatever the outcome. Until
// S22 the seam returns sched.ErrNotImplemented and nothing runs — by design.
func (s *Service) maybeAutoStart(ctx context.Context, res moveResult) {
	if res.fromCol.ID == res.toCol.ID || !res.toCol.AutoStartDelegate || res.after.DelegateAgentID == nil {
		return
	}
	req := sched.RunRequest{
		ProjectID: res.after.ProjectID,
		AgentID:   *res.after.DelegateAgentID,
		TicketID:  res.after.ID,
		Reason:    "column auto-start",
	}
	note := "run requested"
	err := s.sched.RequestRun(ctx, req)
	switch {
	case errors.Is(err, sched.ErrNotImplemented):
		note = "scheduler not implemented until S22; no run started"
	case err != nil:
		note = "scheduler refused: " + err.Error()
		s.logger.Error("tickets: auto-start request failed",
			slog.String("ticket", res.after.Key), slog.String("error", err.Error()))
	}
	if aerr := s.audit.Write(ctx, "ticket.autostart_delegate",
		audit.Target{Kind: "ticket", ID: res.after.ID, ProjectID: res.after.ProjectID, Note: note},
		nil, req); aerr != nil {
		s.logger.Error("tickets: auto-start audit failed", slog.String("error", aerr.Error()))
	}
}

// ---------------------------------------------------------------- archive -----

// Archive is D-15's soft delete: archived_at is stamped, active runs are cancelled with reason
// "ticket archived", history is kept. confirmed is the caller's statement of how many active
// runs it knows the archive will cancel — a mismatch is the typed ActiveRunsError, which is
// what makes the S12 confirmation dialog honest.
func (s *Service) Archive(ctx context.Context, id string, confirmed int64) (TicketWithMeta, error) {
	before, err := s.st.Tickets().ByID(ctx, id)
	if err != nil {
		return TicketWithMeta{}, err
	}
	if before.ArchivedAt != nil {
		return TicketWithMeta{}, &ArchivedError{TicketKey: before.Key}
	}
	runs, err := s.st.Runs().ForTicket(ctx, id)
	if err != nil {
		return TicketWithMeta{}, err
	}
	var active int64
	for _, r := range runs {
		if !r.State.Terminal() {
			active++
		}
	}
	if active != confirmed {
		return TicketWithMeta{}, &ActiveRunsError{Active: active, Confirmed: confirmed}
	}

	// Cancellation goes through the scheduler seam (only the scheduler writes runs.state,
	// data model §10.4). Until S22 there is nothing to cancel and the seam says so.
	note := ""
	cancelled, err := s.sched.CancelTicketRuns(ctx, id, "ticket archived")
	switch {
	case errors.Is(err, sched.ErrNotImplemented):
		note = "scheduler not implemented until S22; no runs to cancel"
	case err != nil:
		return TicketWithMeta{}, fmt.Errorf("cancel active runs: %w", err)
	default:
		note = fmt.Sprintf("cancelled %d active runs", cancelled)
	}

	tk := before
	err = s.st.Tx(ctx, func(tx *store.Tx) error {
		at := s.now()
		tk.ArchivedAt = &at
		tk.UpdatedAt = at
		if err := tx.Tickets().Update(ctx, &tk); err != nil {
			return err
		}
		return s.appendStream(ctx, tx, tk.ID, domain.StreamStatusChange, map[string]any{
			"event": "archived", "active_runs_cancelled": active,
		})
	})
	if err != nil {
		return TicketWithMeta{}, err
	}
	if err := s.audit.Write(ctx, "ticket.archive",
		audit.Target{Kind: "ticket", ID: tk.ID, ProjectID: tk.ProjectID, Note: note},
		before, tk); err != nil {
		return TicketWithMeta{}, err
	}
	s.emitTicket(ctx, "archived", tk, nil)
	return s.withMeta(ctx, tk)
}

// Unarchive restores an archived ticket to exactly where it was.
func (s *Service) Unarchive(ctx context.Context, id string) (TicketWithMeta, error) {
	before, err := s.st.Tickets().ByID(ctx, id)
	if err != nil {
		return TicketWithMeta{}, err
	}
	if before.ArchivedAt == nil {
		return TicketWithMeta{}, &NotArchivedError{TicketKey: before.Key}
	}
	tk := before
	err = s.st.Tx(ctx, func(tx *store.Tx) error {
		tk.ArchivedAt = nil
		tk.UpdatedAt = s.now()
		if err := tx.Tickets().Update(ctx, &tk); err != nil {
			return err
		}
		return s.appendStream(ctx, tx, tk.ID, domain.StreamStatusChange, map[string]any{
			"event": "unarchived",
		})
	})
	if err != nil {
		return TicketWithMeta{}, err
	}
	if err := s.audit.Write(ctx, "ticket.unarchive",
		audit.Target{Kind: "ticket", ID: tk.ID, ProjectID: tk.ProjectID}, before, tk); err != nil {
		return TicketWithMeta{}, err
	}
	s.emitTicket(ctx, "unarchived", tk, nil)
	return s.withMeta(ctx, tk)
}

// ---------------------------------------------------------------- sub-tickets -----

// maxSubticketBatch bounds one selection→sub-tickets request; a selection is lines of text,
// not an import job.
const maxSubticketBatch = 100

// Subtickets creates one sub-ticket per title, atomically, in the parent's column. The parent
// must not itself be a sub-ticket (one level, data model §10.1).
func (s *Service) Subtickets(ctx context.Context, parentID string, titles []string) ([]TicketWithMeta, error) {
	parent, err := s.st.Tickets().ByID(ctx, parentID)
	if err != nil {
		return nil, err
	}
	if parent.ArchivedAt != nil {
		return nil, &ArchivedError{TicketKey: parent.Key}
	}
	if parent.ParentID != nil {
		return nil, &SubticketDepthError{TicketKey: parent.Key}
	}
	clean := make([]string, 0, len(titles))
	for _, t := range titles {
		if t = strings.TrimSpace(t); t != "" {
			clean = append(clean, t)
		}
	}
	if len(clean) == 0 {
		return nil, fieldErr("titles", "At least one non-empty title is required.")
	}
	if len(clean) > maxSubticketBatch {
		return nil, fieldErr("titles",
			fmt.Sprintf("At most %d sub-tickets per request.", maxSubticketBatch))
	}
	col, err := s.st.Columns().ByID(ctx, parent.ColumnID)
	if err != nil {
		return nil, err
	}

	created := make([]domain.Ticket, 0, len(clean))
	err = s.st.Tx(ctx, func(tx *store.Tx) error {
		created = created[:0]
		last, err := lastPosition(ctx, tx, parent.ColumnID)
		if err != nil {
			return err
		}
		p, err := tx.Projects().ByID(ctx, parent.ProjectID)
		if err != nil {
			return err
		}
		keys := make([]string, 0, len(clean))
		for _, title := range clean {
			seq, err := tx.Projects().AllocateTicketSeq(ctx, parent.ProjectID)
			if err != nil {
				return err
			}
			now := s.now()
			last = domain.PositionBetween(last, 0)
			pid := parent.ID
			tk := domain.Ticket{
				ID: domain.NewID(), ProjectID: parent.ProjectID,
				Seq: seq, Key: fmt.Sprintf("%s-%d", p.Key, seq),
				Title: title, ColumnID: parent.ColumnID, Position: last,
				Priority: domain.PriorityNone, ParentID: &pid,
				Origin:    domain.OriginHuman,
				CreatedAt: now, UpdatedAt: now,
			}
			s.stampCreator(ctx, &tk)
			if err := tx.Tickets().Create(ctx, &tk); err != nil {
				return err
			}
			if err := s.appendStream(ctx, tx, tk.ID, domain.StreamFieldChange, map[string]any{
				"event": "created", "column_id": col.ID, "category": string(col.Category),
				"parent_id": parent.ID,
			}); err != nil {
				return err
			}
			created = append(created, tk)
			keys = append(keys, tk.Key)
		}
		return s.appendStream(ctx, tx, parent.ID, domain.StreamFieldChange, map[string]any{
			"event": "subtickets_added", "count": len(keys), "keys": keys,
		})
	})
	if err != nil {
		return nil, err
	}
	out := make([]TicketWithMeta, len(created))
	for i, tk := range created {
		if err := s.audit.Write(ctx, "ticket.create",
			audit.Target{Kind: "ticket", ID: tk.ID, ProjectID: tk.ProjectID,
				Note: "sub-ticket of " + parent.Key}, nil, tk); err != nil {
			return nil, err
		}
		s.emitTicket(ctx, "created", tk, map[string]any{"parent_key": parent.Key})
		out[i] = TicketWithMeta{Ticket: tk, Category: col.Category}
	}
	return out, nil
}
