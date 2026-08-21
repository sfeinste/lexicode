// Package tickets is the tickets domain service (story S10): ticket CRUD with atomic key
// allocation, acceptance criteria, labels, sub-tickets, the move endpoint and archive.
//
// The rules this package exists to protect:
//
//   - Keys are allocated from projects.ticket_seq inside the insert transaction
//     (ProjectsRepo.AllocateTicketSeq), so two concurrent creates can never mint the same
//     PAY-14.
//   - Sub-tickets are one level (data model §10.1): a ticket whose parent_id is set can never
//     become a parent, and a ticket with children can never be given a parent. Violations are
//     the typed SubticketDepthError.
//   - Status is derived from the ticket's column's *category* — never from the column's name
//     (plan rule 3) and never stored on the ticket.
//   - A move NEVER starts a run (brief D3). The one exception — destination column has
//     auto_start_delegate and the ticket has a delegate — goes through the scheduler seam
//     (kernel/sched.Requester), is audited as an attempt, and is a documented no-op until S22.
//   - Archive is D-15's soft delete: archived_at is stamped, active runs are cancelled through
//     the scheduler seam with reason "ticket archived" (no-op until S22), history is kept, and
//     unarchive restores. The API requires the caller to confirm the active-run count.
//   - Every mutation writes the audit log, appends to ticket_stream with actor attribution
//     (in the mutation's transaction), and emits a bus event with the contracts §4 normalized
//     ticket payload. Exception: a same-column reorder and a criteria reorder are audited but
//     write no stream row — drag ordering is presentation, not ticket history.
package tickets

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"

	"github.com/spruce/lexicode/internal/domain"
	"github.com/spruce/lexicode/internal/kernel/audit"
	"github.com/spruce/lexicode/internal/kernel/auth"
	"github.com/spruce/lexicode/internal/kernel/bus"
	"github.com/spruce/lexicode/internal/kernel/httpx"
	"github.com/spruce/lexicode/internal/kernel/sched"
	"github.com/spruce/lexicode/internal/kernel/store"
)

// Service is the tickets service. Construct with New.
type Service struct {
	st     *store.Store
	audit  *audit.Writer
	bus    *bus.Bus
	sched  sched.Requester
	logger *slog.Logger
	now    func() string
}

// Options configures New.
type Options struct {
	// Store is the open, migrated database. Required.
	Store *store.Store
	// Audit is the audit-log writer. Required — every mutation writes an entry.
	Audit *audit.Writer
	// Bus emits internal events for mutations. Nil (tests) skips emission.
	Bus *bus.Bus
	// Sched is the scheduler seam for column auto-start and archive-time cancellation.
	// Nil means sched.Unscheduled — the documented no-op until S22.
	Sched sched.Requester
	// Logger receives failure lines. Nil means slog.Default().
	Logger *slog.Logger
	// Now overrides the clock in tests. Nil means domain.Now.
	Now func() string
}

// New builds the service.
func New(opts Options) *Service {
	logger := opts.Logger
	if logger == nil {
		logger = slog.Default()
	}
	now := opts.Now
	if now == nil {
		now = domain.Now
	}
	sc := opts.Sched
	if sc == nil {
		sc = sched.Unscheduled{}
	}
	return &Service{st: opts.Store, audit: opts.Audit, bus: opts.Bus, sched: sc,
		logger: logger, now: now}
}

// ---------------------------------------------------------------- errors -----

// ValidationError carries field-level problems up to the HTTP layer as a 400 validation_failed.
type ValidationError struct{ Fields []httpx.FieldError }

// Error names the invalid fields.
func (e *ValidationError) Error() string {
	names := make([]string, len(e.Fields))
	for i, f := range e.Fields {
		names[i] = f.Field
	}
	return "invalid fields: " + strings.Join(names, ", ")
}

func fieldErr(field, msg string) *ValidationError {
	return &ValidationError{Fields: []httpx.FieldError{{Field: field, Message: msg}}}
}

// SubticketDepthError is the one-level guardrail (data model §10.1): the mutation would create
// a sub-ticket of a sub-ticket. The HTTP layer renders it as the 409 `subticket_depth` problem.
type SubticketDepthError struct{ TicketKey string }

// Error names the ticket that cannot take (or become) a child.
func (e *SubticketDepthError) Error() string {
	return fmt.Sprintf("sub-tickets are one level: %s cannot be nested further", e.TicketKey)
}

// ArchivedError is a mutation on an archived ticket — everything but unarchive is refused.
// The HTTP layer renders it as the 409 `ticket_archived` problem.
type ArchivedError struct{ TicketKey string }

// Error names the archived ticket.
func (e *ArchivedError) Error() string {
	return fmt.Sprintf("ticket %s is archived; unarchive it first", e.TicketKey)
}

// NotArchivedError is an unarchive of a ticket that is not archived — the 409
// `ticket_not_archived` problem.
type NotArchivedError struct{ TicketKey string }

// Error names the ticket.
func (e *NotArchivedError) Error() string {
	return fmt.Sprintf("ticket %s is not archived", e.TicketKey)
}

// ActiveRunsError is D-15's typed confirmation failing: the caller's confirmed active-run
// count does not match reality. The HTTP layer renders it as the 409 `active_runs_confirmation`
// problem naming the actual count, which is exactly what the S12 dialog re-renders from.
type ActiveRunsError struct{ Active, Confirmed int64 }

// Error names both counts.
func (e *ActiveRunsError) Error() string {
	return fmt.Sprintf("archiving cancels %d active runs; the request confirmed %d",
		e.Active, e.Confirmed)
}

// ---------------------------------------------------------------- reads -----

// TicketWithMeta pairs a ticket with its derived status (the column's category — plan rule 3),
// its attached label IDs, and its criteria progress (S11: the board card's "▸ 3/5 acceptance
// criteria" without a per-card fetch) — the list/board read model.
type TicketWithMeta struct {
	Ticket          domain.Ticket
	Category        domain.ColumnCategory
	LabelIDs        []string
	CriteriaTotal   int64
	CriteriaChecked int64
}

// Detail is the GET /tickets/{id} read model. The stream is its own endpoint.
type Detail struct {
	TicketWithMeta
	Criteria []domain.Criterion
	Labels   []domain.Label
	Children []TicketWithMeta
}

// List returns a project's tickets in board order (column, then position). Archived tickets
// are excluded unless includeArchived.
func (s *Service) List(ctx context.Context, projectKey string, includeArchived bool) ([]TicketWithMeta, error) {
	p, err := s.st.Projects().ByKey(ctx, projectKey)
	if err != nil {
		return nil, err
	}
	var tks []domain.Ticket
	if includeArchived {
		tks, err = s.st.Tickets().ForProjectIncludingArchived(ctx, p.ID)
	} else {
		tks, err = s.st.Tickets().ForProject(ctx, p.ID)
	}
	if err != nil {
		return nil, err
	}
	cats, err := s.categoriesByColumn(ctx, p.ID)
	if err != nil {
		return nil, err
	}
	labelIDs, err := s.st.Labels().IDsForProjectTickets(ctx, p.ID)
	if err != nil {
		return nil, err
	}
	counts, err := s.st.Criteria().CountsForProjectTickets(ctx, p.ID)
	if err != nil {
		return nil, err
	}
	out := make([]TicketWithMeta, len(tks))
	for i, tk := range tks {
		c := counts[tk.ID]
		out[i] = TicketWithMeta{Ticket: tk, Category: cats[tk.ColumnID], LabelIDs: labelIDs[tk.ID],
			CriteriaTotal: c.Total, CriteriaChecked: c.Checked}
	}
	return out, nil
}

// Get returns one ticket with criteria, labels and children.
func (s *Service) Get(ctx context.Context, id string) (Detail, error) {
	tk, err := s.st.Tickets().ByID(ctx, id)
	if err != nil {
		return Detail{}, err
	}
	cats, err := s.categoriesByColumn(ctx, tk.ProjectID)
	if err != nil {
		return Detail{}, err
	}
	crit, err := s.st.Criteria().ForTicket(ctx, id)
	if err != nil {
		return Detail{}, err
	}
	labels, err := s.st.Labels().ForTicket(ctx, id)
	if err != nil {
		return Detail{}, err
	}
	children, err := s.st.Tickets().Children(ctx, id)
	if err != nil {
		return Detail{}, err
	}
	labelIDs := make([]string, len(labels))
	for i, l := range labels {
		labelIDs[i] = l.ID
	}
	var checked int64
	for _, c := range crit {
		if c.Checked {
			checked++
		}
	}
	counts, err := s.st.Criteria().CountsForProjectTickets(ctx, tk.ProjectID)
	if err != nil {
		return Detail{}, err
	}
	d := Detail{
		TicketWithMeta: TicketWithMeta{Ticket: tk, Category: cats[tk.ColumnID], LabelIDs: labelIDs,
			CriteriaTotal: int64(len(crit)), CriteriaChecked: checked},
		Criteria: crit,
		Labels:   labels,
		Children: make([]TicketWithMeta, len(children)),
	}
	for i, c := range children {
		ids, err := s.st.Labels().ForTicket(ctx, c.ID)
		if err != nil {
			return Detail{}, err
		}
		childLabels := make([]string, len(ids))
		for j, l := range ids {
			childLabels[j] = l.ID
		}
		cc := counts[c.ID]
		d.Children[i] = TicketWithMeta{Ticket: c, Category: cats[c.ColumnID], LabelIDs: childLabels,
			CriteriaTotal: cc.Total, CriteriaChecked: cc.Checked}
	}
	return d, nil
}

// Stream returns a ticket's whole unified stream, oldest first.
func (s *Service) Stream(ctx context.Context, ticketID string) ([]domain.StreamEntry, error) {
	if _, err := s.st.Tickets().ByID(ctx, ticketID); err != nil {
		return nil, err
	}
	return s.st.TicketStream().ForTicket(ctx, ticketID)
}

// categoriesByColumn maps a project's column IDs to their categories.
func (s *Service) categoriesByColumn(ctx context.Context, projectID string) (map[string]domain.ColumnCategory, error) {
	cols, err := s.st.Columns().ForProject(ctx, projectID)
	if err != nil {
		return nil, err
	}
	out := make(map[string]domain.ColumnCategory, len(cols))
	for _, c := range cols {
		out[c.ID] = c.Category
	}
	return out, nil
}

// ---------------------------------------------------------------- create -----

// CreateInput is what a new ticket needs. Empty ColumnID lands the ticket in the project's
// first backlog-category column. AssigneeID (human) and DelegateAgentID (agent) are separate
// axes from the start (D1).
type CreateInput struct {
	Title           string
	Description     string
	Priority        string // "" → none
	ColumnID        string
	AssigneeID      string
	DelegateAgentID string
	ParentID        string
}

// Create inserts a ticket, allocating its key atomically inside the insert transaction.
func (s *Service) Create(ctx context.Context, projectKey string, in CreateInput) (TicketWithMeta, error) {
	p, err := s.st.Projects().ByKey(ctx, projectKey)
	if err != nil {
		return TicketWithMeta{}, err
	}

	in.Title = strings.TrimSpace(in.Title)
	if in.Priority == "" {
		in.Priority = string(domain.PriorityNone)
	}
	var errs []httpx.FieldError
	if in.Title == "" {
		errs = append(errs, httpx.FieldError{Field: "title", Message: "Title is required."})
	}
	if !domain.Priority(in.Priority).IsValid() {
		errs = append(errs, httpx.FieldError{Field: "priority",
			Message: "One of none, low, medium, high, urgent."})
	}
	col, ferr, err := s.resolveColumn(ctx, p.ID, in.ColumnID)
	if err != nil {
		return TicketWithMeta{}, err
	}
	errs = append(errs, ferr...)
	assignee, ferr, err := s.resolveAssignee(ctx, in.AssigneeID)
	if err != nil {
		return TicketWithMeta{}, err
	}
	errs = append(errs, ferr...)
	delegate, ferr, err := s.resolveDelegate(ctx, p.ID, in.DelegateAgentID)
	if err != nil {
		return TicketWithMeta{}, err
	}
	errs = append(errs, ferr...)
	if len(errs) > 0 {
		return TicketWithMeta{}, &ValidationError{Fields: errs}
	}

	var parent *domain.Ticket
	if in.ParentID != "" {
		pt, err := s.st.Tickets().ByID(ctx, in.ParentID)
		if errors.Is(err, store.ErrNotFound) {
			return TicketWithMeta{}, fieldErr("parent_id", "No such ticket in this project.")
		}
		if err != nil {
			return TicketWithMeta{}, err
		}
		if err := s.parentOK(pt, p.ID); err != nil {
			return TicketWithMeta{}, err
		}
		parent = &pt
	}

	now := s.now()
	tk := domain.Ticket{
		ID: domain.NewID(), ProjectID: p.ID,
		Title: in.Title, Description: in.Description,
		ColumnID: col.ID, Priority: domain.Priority(in.Priority),
		AssigneeID: assignee, DelegateAgentID: delegate,
		Origin:    domain.OriginHuman,
		CreatedAt: now, UpdatedAt: now,
	}
	if parent != nil {
		pid := parent.ID
		tk.ParentID = &pid
	}
	s.stampCreator(ctx, &tk)

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
		return s.appendStream(ctx, tx, tk.ID, domain.StreamFieldChange, map[string]any{
			"event": "created", "column_id": col.ID, "category": string(col.Category),
		})
	})
	if err != nil {
		return TicketWithMeta{}, err
	}
	if err := s.audit.Write(ctx, "ticket.create",
		audit.Target{Kind: "ticket", ID: tk.ID, ProjectID: p.ID}, nil, tk); err != nil {
		return TicketWithMeta{}, err
	}
	s.emitTicket(ctx, "created", tk, nil)
	return TicketWithMeta{Ticket: tk, Category: col.Category, LabelIDs: nil}, nil
}

// stampCreator records origin and created_by from the request's actor.
func (s *Service) stampCreator(ctx context.Context, tk *domain.Ticket) {
	a, ok := auth.ActorFrom(ctx)
	if !ok {
		return
	}
	switch a.Kind {
	case domain.ActorHuman:
		if a.ID != "" {
			id := a.ID
			tk.CreatedByUserID = &id
		}
	case domain.ActorAgent:
		tk.Origin = domain.OriginAgent
		if a.ID != "" {
			id := a.ID
			tk.CreatedByAgentID = &id
		}
	case domain.ActorTrigger:
		tk.Origin = domain.OriginTrigger
	}
}

// resolveColumn resolves the destination column for a create: explicit ID (must belong to the
// project) or the project's first backlog-category column.
func (s *Service) resolveColumn(ctx context.Context, projectID, columnID string) (domain.Column, []httpx.FieldError, error) {
	if columnID == "" {
		cols, err := s.st.Columns().ByCategory(ctx, projectID, domain.CategoryBacklog)
		if err != nil {
			return domain.Column{}, nil, err
		}
		if len(cols) == 0 {
			// Unreachable while the board guardrail holds (S09) — but a typed message beats
			// a nil dereference if it ever breaks.
			return domain.Column{}, []httpx.FieldError{{Field: "column_id",
				Message: "The project has no backlog column; name a column explicitly."}}, nil
		}
		return cols[0], nil, nil
	}
	col, err := s.st.Columns().ByID(ctx, columnID)
	if errors.Is(err, store.ErrNotFound) {
		return domain.Column{}, []httpx.FieldError{{Field: "column_id",
			Message: "No such column on this board."}}, nil
	}
	if err != nil {
		return domain.Column{}, nil, err
	}
	if col.ProjectID != projectID {
		return domain.Column{}, []httpx.FieldError{{Field: "column_id",
			Message: "No such column on this board."}}, nil
	}
	return col, nil, nil
}

// resolveAssignee validates a human assignee exists. "" means unassigned.
func (s *Service) resolveAssignee(ctx context.Context, userID string) (*string, []httpx.FieldError, error) {
	if userID == "" {
		return nil, nil, nil
	}
	if _, err := s.st.Users().ByID(ctx, userID); errors.Is(err, store.ErrNotFound) {
		return nil, []httpx.FieldError{{Field: "assignee_id", Message: "No such user."}}, nil
	} else if err != nil {
		return nil, nil, err
	}
	id := userID
	return &id, nil, nil
}

// resolveDelegate validates a delegate agent exists, belongs to the project and is not
// archived. "" means no delegate.
func (s *Service) resolveDelegate(ctx context.Context, projectID, agentID string) (*string, []httpx.FieldError, error) {
	if agentID == "" {
		return nil, nil, nil
	}
	a, err := s.st.Agents().ByID(ctx, agentID)
	if errors.Is(err, store.ErrNotFound) {
		return nil, []httpx.FieldError{{Field: "delegate_agent_id", Message: "No such agent in this project."}}, nil
	}
	if err != nil {
		return nil, nil, err
	}
	if a.ProjectID != projectID || a.ArchivedAt != nil {
		return nil, []httpx.FieldError{{Field: "delegate_agent_id", Message: "No such agent in this project."}}, nil
	}
	id := agentID
	return &id, nil, nil
}

// parentOK checks a would-be parent: same project, not archived, and — the one-level rule —
// itself not a sub-ticket.
func (s *Service) parentOK(parent domain.Ticket, projectID string) error {
	if parent.ProjectID != projectID {
		return fieldErr("parent_id", "No such ticket in this project.")
	}
	if parent.ArchivedAt != nil {
		return &ArchivedError{TicketKey: parent.Key}
	}
	if parent.ParentID != nil {
		return &SubticketDepthError{TicketKey: parent.Key}
	}
	return nil
}

// lastPosition returns the last position in a column, or 0 when it is empty.
func lastPosition(ctx context.Context, tx *store.Tx, columnID string) (float64, error) {
	existing, err := tx.Tickets().ForColumn(ctx, columnID)
	if err != nil {
		return 0, err
	}
	if len(existing) == 0 {
		return 0, nil
	}
	return existing[len(existing)-1].Position, nil
}

// ---------------------------------------------------------------- update -----

// UpdatePatch is a PATCH /tickets/{id} body: absent fields are unchanged. The OptStr fields
// are tri-state — null clears (unassign, remove delegate, detach from parent).
type UpdatePatch struct {
	Title           *string
	Description     *string
	Priority        *string
	AssigneeID      OptStr
	DelegateAgentID OptStr
	ParentID        OptStr
}

// Update applies a patch. Every changed field lands in the audit snapshot; assignment,
// delegation and reparenting write their own stream rows so the ticket history reads as what
// happened.
func (s *Service) Update(ctx context.Context, id string, patch UpdatePatch) (TicketWithMeta, error) {
	var before, tk domain.Ticket
	var streamPayloads []map[string]any
	err := s.st.Tx(ctx, func(tx *store.Tx) error {
		var err error
		before, err = tx.Tickets().ByID(ctx, id)
		if err != nil {
			return err
		}
		if before.ArchivedAt != nil {
			return &ArchivedError{TicketKey: before.Key}
		}
		tk = before
		streamPayloads = nil

		var errs []httpx.FieldError
		var edited []string
		if patch.Title != nil {
			if t := strings.TrimSpace(*patch.Title); t == "" {
				errs = append(errs, httpx.FieldError{Field: "title", Message: "Title is required."})
			} else if t != tk.Title {
				tk.Title = t
				edited = append(edited, "title")
			}
		}
		if patch.Description != nil && *patch.Description != tk.Description {
			tk.Description = *patch.Description
			edited = append(edited, "description")
		}
		if patch.Priority != nil {
			next := domain.Priority(*patch.Priority)
			if !next.IsValid() {
				errs = append(errs, httpx.FieldError{Field: "priority",
					Message: "One of none, low, medium, high, urgent."})
			} else if next != tk.Priority {
				tk.Priority = next
				edited = append(edited, "priority")
			}
		}

		if patch.AssigneeID.Set {
			if patch.AssigneeID.Null {
				if tk.AssigneeID != nil {
					tk.AssigneeID = nil
					streamPayloads = append(streamPayloads, map[string]any{"event": "unassigned"})
				}
			} else {
				next, ferr, err := s.resolveAssignee(ctx, patch.AssigneeID.Value)
				if err != nil {
					return err
				}
				errs = append(errs, ferr...)
				if len(ferr) == 0 && (tk.AssigneeID == nil || *tk.AssigneeID != *next) {
					tk.AssigneeID = next
					streamPayloads = append(streamPayloads, map[string]any{
						"event": "assigned", "assignee_id": *next})
				}
			}
		}
		if patch.DelegateAgentID.Set {
			if patch.DelegateAgentID.Null {
				if tk.DelegateAgentID != nil {
					tk.DelegateAgentID = nil
					streamPayloads = append(streamPayloads, map[string]any{"event": "undelegated"})
				}
			} else {
				next, ferr, err := s.resolveDelegate(ctx, tk.ProjectID, patch.DelegateAgentID.Value)
				if err != nil {
					return err
				}
				errs = append(errs, ferr...)
				if len(ferr) == 0 && (tk.DelegateAgentID == nil || *tk.DelegateAgentID != *next) {
					tk.DelegateAgentID = next
					streamPayloads = append(streamPayloads, map[string]any{
						"event": "delegated", "delegate_agent_id": *next})
				}
			}
		}
		if patch.ParentID.Set {
			if patch.ParentID.Null {
				if tk.ParentID != nil {
					tk.ParentID = nil
					streamPayloads = append(streamPayloads, map[string]any{"event": "detached_from_parent"})
				}
			} else if tk.ParentID == nil || *tk.ParentID != patch.ParentID.Value {
				if patch.ParentID.Value == tk.ID {
					errs = append(errs, httpx.FieldError{Field: "parent_id",
						Message: "A ticket cannot be its own parent."})
				} else {
					parent, err := tx.Tickets().ByID(ctx, patch.ParentID.Value)
					if errors.Is(err, store.ErrNotFound) {
						errs = append(errs, httpx.FieldError{Field: "parent_id",
							Message: "No such ticket in this project."})
					} else if err != nil {
						return err
					} else {
						if err := s.parentOK(parent, tk.ProjectID); err != nil {
							return err
						}
						// The other direction of the one-level rule: a ticket with children
						// cannot itself become a child.
						n, err := tx.Tickets().CountChildren(ctx, tk.ID)
						if err != nil {
							return err
						}
						if n > 0 {
							return &SubticketDepthError{TicketKey: tk.Key}
						}
						pid := parent.ID
						tk.ParentID = &pid
						streamPayloads = append(streamPayloads, map[string]any{
							"event": "reparented", "parent_id": pid})
					}
				}
			}
		}
		if len(errs) > 0 {
			return &ValidationError{Fields: errs}
		}

		if len(edited) > 0 {
			streamPayloads = append(streamPayloads, map[string]any{
				"event": "updated", "fields": edited})
		}
		if len(streamPayloads) == 0 {
			return nil // nothing changed; commit as a no-op
		}
		tk.UpdatedAt = s.now()
		if err := tx.Tickets().Update(ctx, &tk); err != nil {
			return err
		}
		for _, p := range streamPayloads {
			if err := s.appendStream(ctx, tx, tk.ID, domain.StreamFieldChange, p); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return TicketWithMeta{}, err
	}
	if len(streamPayloads) > 0 {
		if err := s.audit.Write(ctx, "ticket.update",
			audit.Target{Kind: "ticket", ID: tk.ID, ProjectID: tk.ProjectID}, before, tk); err != nil {
			return TicketWithMeta{}, err
		}
		s.emitTicket(ctx, "updated", tk, nil)
	}
	return s.withMeta(ctx, tk)
}

// withMeta loads the derived category, label IDs and criteria progress for a single-ticket
// response.
func (s *Service) withMeta(ctx context.Context, tk domain.Ticket) (TicketWithMeta, error) {
	col, err := s.st.Columns().ByID(ctx, tk.ColumnID)
	if err != nil {
		return TicketWithMeta{}, err
	}
	labels, err := s.st.Labels().ForTicket(ctx, tk.ID)
	if err != nil {
		return TicketWithMeta{}, err
	}
	ids := make([]string, len(labels))
	for i, l := range labels {
		ids[i] = l.ID
	}
	counts, err := s.st.Criteria().CountsForTicket(ctx, tk.ID)
	if err != nil {
		return TicketWithMeta{}, err
	}
	return TicketWithMeta{Ticket: tk, Category: col.Category, LabelIDs: ids,
		CriteriaTotal: counts.Total, CriteriaChecked: counts.Checked}, nil
}
