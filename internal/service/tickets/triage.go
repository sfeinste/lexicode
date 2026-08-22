// triage.go is the S28 trigger-side of triage: automated ticket creation lands in the triage
// queue, never directly on the board (brief §6.4; data model §10.7). The triage LIST — the
// keyboard-driven review surface, accept/duplicate/decline/snooze — is story S31; what lives
// here is the invariant's write side: the ticket row and its `pending` triage item are
// created in one transaction, and the board query (store.TicketsRepo.ForProject) excludes
// every ticket whose item is unresolved.
package tickets

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/spruce/lexicode/internal/domain"
	"github.com/spruce/lexicode/internal/kernel/audit"
	"github.com/spruce/lexicode/internal/kernel/store"
)

// NoColumnOfCategoryError is a category move or placement into a category the project has no
// column of — the named error brief D2's category-not-name rule demands (S28 move_ticket).
type NoColumnOfCategoryError struct{ Category domain.ColumnCategory }

// Error names the missing category, and the fix.
func (e *NoColumnOfCategoryError) Error() string {
	return fmt.Sprintf("the project has no %s-category column; add one on the board settings or pick another category",
		e.Category)
}

// PendingTriageError is a move of a ticket that is still in triage: pending-triage tickets are
// invisible to the board and to move_ticket actions (data model §10, invariant 7).
type PendingTriageError struct{ TicketKey string }

// Error names the ticket and why it cannot move.
func (e *PendingTriageError) Error() string {
	return fmt.Sprintf("ticket %s is pending triage and invisible to moves; accept it in the triage queue first",
		e.TicketKey)
}

// TriggerCreateInput is what the create_ticket trigger action supplies (S28).
type TriggerCreateInput struct {
	ProjectID   string
	Title       string
	Description string
	// LabelNames attach existing project labels by name; unknown names are skipped, not
	// created — a trigger must not mint labels nobody defined.
	LabelNames []string
	// Provenance is the human sentence the triage row renders: "Created by trigger `CI
	// failed → file a ticket` from run #482".
	Provenance      string
	SourceTriggerID string
	SourceRunID     string
}

// CreateFromTrigger inserts a trigger-created ticket INTO TRIAGE: the ticket row (origin
// `trigger`, parked in the project's first backlog-category column) and its `pending`
// triage_items row are one transaction, so there is no instant in which the ticket is
// board-visible (§10.7 — the board query excludes it until the item resolves; S31 builds the
// queue that resolves it). Emits `triage.created` (contracts §5.1), not `ticket.created` —
// the ticket is not on the board, and the badge listens for triage.
func (s *Service) CreateFromTrigger(ctx context.Context, in TriggerCreateInput) (domain.Ticket, error) {
	p, err := s.st.Projects().ByID(ctx, in.ProjectID)
	if err != nil {
		return domain.Ticket{}, err
	}
	in.Title = strings.TrimSpace(in.Title)
	if in.Title == "" {
		return domain.Ticket{}, fieldErr("title", "Title is required.")
	}
	cols, err := s.st.Columns().ByCategory(ctx, p.ID, domain.CategoryBacklog)
	if err != nil {
		return domain.Ticket{}, err
	}
	if len(cols) == 0 {
		return domain.Ticket{}, &NoColumnOfCategoryError{Category: domain.CategoryBacklog}
	}
	col := cols[0]

	labels, err := s.st.Labels().ForProject(ctx, p.ID)
	if err != nil {
		return domain.Ticket{}, err
	}
	var attach []string
	for _, name := range in.LabelNames {
		for _, l := range labels {
			if l.Name == name {
				attach = append(attach, l.ID)
				break
			}
		}
	}

	now := s.now()
	tk := domain.Ticket{
		ID: domain.NewID(), ProjectID: p.ID,
		Title: in.Title, Description: in.Description,
		ColumnID: col.ID, Priority: domain.PriorityNone,
		Origin:    domain.OriginTrigger,
		CreatedAt: now, UpdatedAt: now,
	}
	item := domain.TriageItem{
		ID: domain.NewID(), TicketID: tk.ID,
		Provenance: in.Provenance, State: domain.TriagePending,
		CreatedAt: now,
	}
	if in.SourceTriggerID != "" {
		id := in.SourceTriggerID
		item.SourceTriggerID = &id
	}
	if in.SourceRunID != "" {
		id := in.SourceRunID
		item.SourceRunID = &id
	}

	err = s.st.Tx(ctx, func(tx *store.Tx) error {
		seq, err := tx.Projects().AllocateTicketSeq(ctx, p.ID)
		if err != nil {
			return err
		}
		tk.Seq = seq
		tk.Key = fmt.Sprintf("%s-%d", p.Key, seq)
		last, err := lastPosition(ctx, tx, col.ID)
		if err != nil {
			return err
		}
		tk.Position = domain.PositionBetween(last, 0)
		if err := tx.Tickets().Create(ctx, &tk); err != nil {
			return err
		}
		if err := tx.Triage().Create(ctx, &item); err != nil {
			return err
		}
		for _, labelID := range attach {
			if err := tx.Labels().Attach(ctx, tk.ID, labelID); err != nil {
				return err
			}
		}
		return s.appendStream(ctx, tx, tk.ID, domain.StreamFieldChange, map[string]any{
			"event": "created", "column_id": col.ID, "category": string(col.Category),
			"provenance": in.Provenance,
		})
	})
	if err != nil {
		return domain.Ticket{}, err
	}
	if err := s.audit.Write(ctx, "ticket.create_from_trigger",
		audit.Target{Kind: "ticket", ID: tk.ID, ProjectID: p.ID, Note: in.Provenance},
		nil, tk); err != nil {
		return domain.Ticket{}, err
	}
	s.emitTriageCreated(ctx, tk, item)
	return tk, nil
}

// emitTriageCreated publishes the `triage.created` bus event (contracts §5.1): the triage tab
// badge and the S31 list re-render from it. Best-effort, like every post-commit emission here.
func (s *Service) emitTriageCreated(ctx context.Context, tk domain.Ticket, item domain.TriageItem) {
	if s.bus == nil {
		return
	}
	payload, err := json.Marshal(map[string]any{
		"ticket": s.normalizedTicket(ctx, tk),
		"triage": map[string]any{"id": item.ID, "provenance": item.Provenance, "state": string(item.State)},
	})
	if err != nil {
		s.logger.Error("tickets: marshal triage payload failed")
		return
	}
	pid, tid := tk.ProjectID, tk.ID
	e := domain.Event{
		ProjectID: &pid, Kind: "triage", ActivityType: "created",
		ActorKind:   domain.ActorTrigger,
		SubjectKind: "ticket", SubjectID: &tid,
		Payload: payload, OccurredAt: s.now(),
	}
	if err := s.bus.Emit(ctx, e); err != nil {
		s.logger.Error("tickets: emit triage.created failed")
	}
}
