package tickets

import (
	"context"
	"errors"
	"strings"

	"github.com/spruce/lexicode/internal/domain"
	"github.com/spruce/lexicode/internal/kernel/audit"
	"github.com/spruce/lexicode/internal/kernel/auth"
	"github.com/spruce/lexicode/internal/kernel/store"
)

// criterionGap is the spacing between adjacent criterion positions — same gapped-integer
// scheme as board columns: appends land at last+gap, a reorder takes the midpoint of its new
// neighbours, and an exhausted gap renumbers the checklist in the same transaction.
const criterionGap = 1024

// AddCriterion appends one acceptance criterion to a ticket's checklist.
func (s *Service) AddCriterion(ctx context.Context, ticketID, text string) (domain.Criterion, error) {
	tk, err := s.st.Tickets().ByID(ctx, ticketID)
	if err != nil {
		return domain.Criterion{}, err
	}
	if tk.ArchivedAt != nil {
		return domain.Criterion{}, &ArchivedError{TicketKey: tk.Key}
	}
	text = strings.TrimSpace(text)
	if text == "" {
		return domain.Criterion{}, fieldErr("text", "Criterion text is required.")
	}

	c := domain.Criterion{
		ID: domain.NewID(), TicketID: tk.ID, Text: text, UpdatedAt: s.now(),
	}
	err = s.st.Tx(ctx, func(tx *store.Tx) error {
		existing, err := tx.Criteria().ForTicket(ctx, tk.ID)
		if err != nil {
			return err
		}
		c.Position = criterionGap
		if len(existing) > 0 {
			c.Position = existing[len(existing)-1].Position + criterionGap
		}
		if err := tx.Criteria().Create(ctx, &c); err != nil {
			return err
		}
		return s.appendStream(ctx, tx, tk.ID, domain.StreamFieldChange, map[string]any{
			"event": "criterion_added", "criterion_id": c.ID, "text": c.Text,
		})
	})
	if err != nil {
		return domain.Criterion{}, err
	}
	if err := s.audit.Write(ctx, "ticket.criterion.add",
		audit.Target{Kind: "criterion", ID: c.ID, ProjectID: tk.ProjectID}, nil, c); err != nil {
		return domain.Criterion{}, err
	}
	s.emitTicket(ctx, "updated", tk, nil)
	return c, nil
}

// CriterionPatch is a PATCH /criteria/{id} body: absent fields are unchanged. Checked flips
// the checkbox — a human check stamps checked_by_user_id from the request actor; the
// checked_by_run_id column is written by agent runs (S22+), never by this API. AfterID
// reorders: null moves the criterion to the top, a criterion ID places it after that one.
type CriterionPatch struct {
	Text    *string
	Note    *string
	Checked *bool
	AfterID OptStr
}

// UpdateCriterion applies a patch — edit, check/uncheck, reorder — atomically.
func (s *Service) UpdateCriterion(ctx context.Context, id string, patch CriterionPatch) (domain.Criterion, error) {
	var c domain.Criterion
	var tk domain.Ticket
	err := s.st.Tx(ctx, func(tx *store.Tx) error {
		var err error
		c, err = tx.Criteria().ByID(ctx, id)
		if err != nil {
			return err
		}
		tk, err = tx.Tickets().ByID(ctx, c.TicketID)
		if err != nil {
			return err
		}
		if tk.ArchivedAt != nil {
			return &ArchivedError{TicketKey: tk.Key}
		}

		var payloads []map[string]any
		if patch.Text != nil {
			t := strings.TrimSpace(*patch.Text)
			if t == "" {
				return fieldErr("text", "Criterion text is required.")
			}
			if t != c.Text {
				c.Text = t
				payloads = append(payloads, map[string]any{
					"event": "criterion_updated", "criterion_id": c.ID, "text": c.Text})
			}
		}
		if patch.Note != nil && *patch.Note != c.Note {
			c.Note = *patch.Note
			payloads = append(payloads, map[string]any{
				"event": "criterion_updated", "criterion_id": c.ID, "note": c.Note})
		}
		if patch.Checked != nil && *patch.Checked != c.Checked {
			c.Checked = *patch.Checked
			c.CheckedByRunID, c.CheckedByUserID = nil, nil
			event := "criterion_unchecked"
			if c.Checked {
				event = "criterion_checked"
				if a, ok := auth.ActorFrom(ctx); ok && a.Kind == domain.ActorHuman && a.ID != "" {
					uid := a.ID
					c.CheckedByUserID = &uid
				}
			}
			payloads = append(payloads, map[string]any{
				"event": event, "criterion_id": c.ID, "text": c.Text})
		}
		if patch.AfterID.Set {
			pos, err := s.reorderCriterion(ctx, tx, &c, patch.AfterID)
			if err != nil {
				return err
			}
			c.Position = pos
			// Reordering a checklist is presentation, not ticket history: audited, no
			// stream row — same rule as a same-column ticket reorder.
		}

		c.UpdatedAt = s.now()
		if err := tx.Criteria().Update(ctx, &c); err != nil {
			return err
		}
		for _, p := range payloads {
			if err := s.appendStream(ctx, tx, tk.ID, domain.StreamFieldChange, p); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return domain.Criterion{}, err
	}
	if err := s.audit.Write(ctx, "ticket.criterion.update",
		audit.Target{Kind: "criterion", ID: c.ID, ProjectID: tk.ProjectID}, nil, c); err != nil {
		return domain.Criterion{}, err
	}
	s.emitTicket(ctx, "updated", tk, nil)
	return c, nil
}

// reorderCriterion computes the criterion's new position among its ticket's checklist, gapped
// integers with a same-transaction renumber when the gap is exhausted.
func (s *Service) reorderCriterion(ctx context.Context, tx *store.Tx, c *domain.Criterion, after OptStr) (int64, error) {
	all, err := tx.Criteria().ForTicket(ctx, c.TicketID)
	if err != nil {
		return 0, err
	}
	rest := make([]domain.Criterion, 0, len(all))
	for _, it := range all {
		if it.ID != c.ID {
			rest = append(rest, it)
		}
	}
	idx := 0 // null → top
	if !after.Null {
		if after.Value == c.ID {
			return 0, fieldErr("after_id", "A criterion cannot be placed after itself.")
		}
		found := false
		for i, it := range rest {
			if it.ID == after.Value {
				idx, found = i+1, true
				break
			}
		}
		if !found {
			return 0, fieldErr("after_id", "No such criterion on this ticket.")
		}
	}

	var lo, hi int64
	switch {
	case len(rest) == 0:
		return criterionGap, nil
	case idx == 0:
		lo, hi = 0, rest[0].Position
	case idx == len(rest):
		return rest[len(rest)-1].Position + criterionGap, nil
	default:
		lo, hi = rest[idx-1].Position, rest[idx].Position
	}
	if mid := lo + (hi-lo)/2; mid > lo && mid < hi {
		return mid, nil
	}

	// Gap exhausted: renumber the checklist; the moving criterion is written by the caller.
	final := append(append(append([]domain.Criterion{}, rest[:idx]...), *c), rest[idx:]...)
	var pos int64
	for i := range final {
		final[i].Position = int64(i+1) * criterionGap
		if final[i].ID == c.ID {
			pos = final[i].Position
			continue
		}
		final[i].UpdatedAt = s.now()
		if err := tx.Criteria().Update(ctx, &final[i]); err != nil {
			return 0, err
		}
	}
	return pos, nil
}

// DeleteCriterion removes a criterion from its ticket's checklist.
func (s *Service) DeleteCriterion(ctx context.Context, id string) error {
	var c domain.Criterion
	var tk domain.Ticket
	err := s.st.Tx(ctx, func(tx *store.Tx) error {
		var err error
		c, err = tx.Criteria().ByID(ctx, id)
		if err != nil {
			return err
		}
		tk, err = tx.Tickets().ByID(ctx, c.TicketID)
		if err != nil {
			return err
		}
		if tk.ArchivedAt != nil {
			return &ArchivedError{TicketKey: tk.Key}
		}
		if err := tx.Criteria().Delete(ctx, c.ID); err != nil {
			return err
		}
		return s.appendStream(ctx, tx, tk.ID, domain.StreamFieldChange, map[string]any{
			"event": "criterion_removed", "criterion_id": c.ID, "text": c.Text,
		})
	})
	if err != nil {
		return err
	}
	if err := s.audit.Write(ctx, "ticket.criterion.delete",
		audit.Target{Kind: "criterion", ID: c.ID, ProjectID: tk.ProjectID}, c, nil); err != nil {
		return err
	}
	s.emitTicket(ctx, "updated", tk, nil)
	return nil
}

// errIsNotFound is a tiny readability helper for handlers that must distinguish missing rows.
func errIsNotFound(err error) bool { return errors.Is(err, store.ErrNotFound) }
