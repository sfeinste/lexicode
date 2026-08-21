// Package board is the board domain service. Story S09 fills in its column half: the default
// column set a project is born with, and column CRUD — rename, reorder, add, delete, category,
// WIP limit, auto_start_delegate. Tickets and the board read model arrive with S10/S11.
//
// The rule this package exists to protect (plan rule 3, brief D2): code never references a
// column by its user-facing name. Names here are opaque display strings; every decision keys
// off Category. A lint test in this package scans internal/ for violations.
//
// Ordering: columns.position is INTEGER (data model §4), so "fractional" reordering is done
// with gapped integers — new columns land at max+1024, a reorder places the column at the
// midpoint of its new neighbours, and when a gap is exhausted the whole set is renumbered to
// multiples of 1024 inside the same transaction. One write in the common case, atomic always.
//
// Guardrails (S09): a project must always keep at least one column of each required category
// (backlog, running, done) — deleting the last one, or changing its category away, is a typed
// `last_category_column` problem naming the category. Deleting a column that holds tickets
// requires a destination column; the tickets move there, in the delete's transaction.
//
// Every mutation writes the audit log and emits a `board.updated` bus event (the SSE type of
// the same name, contracts §5.1) so open boards can refetch.
package board

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"

	"github.com/spruce/lexicode/internal/domain"
	"github.com/spruce/lexicode/internal/kernel/audit"
	"github.com/spruce/lexicode/internal/kernel/auth"
	"github.com/spruce/lexicode/internal/kernel/bus"
	"github.com/spruce/lexicode/internal/kernel/httpx"
	"github.com/spruce/lexicode/internal/kernel/store"
)

// requiredCategories are the categories a project can never be left without (S09 guardrail:
// "a project always has the three required categories").
var requiredCategories = map[domain.ColumnCategory]bool{
	domain.CategoryBacklog: true,
	domain.CategoryRunning: true,
	domain.CategoryDone:    true,
}

// Service is the board service. Construct with New.
type Service struct {
	st     *store.Store
	audit  *audit.Writer
	bus    *bus.Bus
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
	return &Service{st: opts.Store, audit: opts.Audit, bus: opts.Bus, logger: logger, now: now}
}

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

// RequiredCategoryError is the typed guardrail failure: the mutation would leave the project
// without any column of a required category. The HTTP layer renders it as the 409
// `last_category_column` problem, naming the category.
type RequiredCategoryError struct{ Category domain.ColumnCategory }

// Error names the category the project would lose.
func (e *RequiredCategoryError) Error() string {
	return fmt.Sprintf("the project's last %q column cannot be removed", e.Category)
}

// ColumnWithCount pairs a column with how many tickets it holds — the list read model the
// settings screen needs (the delete flow asks for a destination only when tickets exist).
type ColumnWithCount struct {
	Column      domain.Column
	TicketCount int64
}

// ---------------------------------------------------------------- reads -----

// List returns a project's columns in board order, with ticket counts.
func (s *Service) List(ctx context.Context, projectKey string) ([]ColumnWithCount, error) {
	p, err := s.st.Projects().ByKey(ctx, projectKey)
	if err != nil {
		return nil, err
	}
	cols, err := s.st.Columns().ForProject(ctx, p.ID)
	if err != nil {
		return nil, err
	}
	counts, err := s.st.Columns().TicketCounts(ctx, p.ID)
	if err != nil {
		return nil, err
	}
	out := make([]ColumnWithCount, len(cols))
	for i, c := range cols {
		out[i] = ColumnWithCount{Column: c, TicketCount: counts[c.ID]}
	}
	return out, nil
}

// ---------------------------------------------------------------- create -----

// CreateInput is what a new column needs. Category is chosen at creation (and editable later);
// there is no default — the caller must decide what the column means to automation.
type CreateInput struct {
	Name     string
	Category domain.ColumnCategory
}

// Create appends a column to the end of a project's board.
func (s *Service) Create(ctx context.Context, projectKey string, in CreateInput) (domain.Column, error) {
	p, err := s.st.Projects().ByKey(ctx, projectKey)
	if err != nil {
		return domain.Column{}, err
	}

	in.Name = strings.TrimSpace(in.Name)
	var errs []httpx.FieldError
	if in.Name == "" {
		errs = append(errs, httpx.FieldError{Field: "name", Message: "Name is required."})
	}
	if !in.Category.IsValid() {
		errs = append(errs, httpx.FieldError{Field: "category",
			Message: "One of backlog, ready, running, review, done, canceled."})
	}
	if len(errs) > 0 {
		return domain.Column{}, &ValidationError{Fields: errs}
	}

	now := s.now()
	c := domain.Column{
		ID: domain.NewID(), ProjectID: p.ID, Name: in.Name, Category: in.Category,
		CreatedAt: now, UpdatedAt: now,
	}
	err = s.st.Tx(ctx, func(tx *store.Tx) error {
		cols, err := tx.Columns().ForProject(ctx, p.ID)
		if err != nil {
			return err
		}
		c.Position = positionGap
		if len(cols) > 0 {
			c.Position = cols[len(cols)-1].Position + positionGap
		}
		return tx.Columns().Create(ctx, &c)
	})
	if err != nil {
		return domain.Column{}, err
	}
	if err := s.audit.Write(ctx, "column.create",
		audit.Target{Kind: "column", ID: c.ID, ProjectID: p.ID}, nil, c); err != nil {
		return domain.Column{}, err
	}
	s.emit(ctx, p.ID, c, "created")
	return c, nil
}

// ---------------------------------------------------------------- update -----

// UpdatePatch is a PATCH /columns/{id} body: absent fields are unchanged. WIPLimit is
// tri-state (null clears the limit). AfterID is the reorder instruction: null moves the column
// to the front, a column ID places it immediately after that column.
type UpdatePatch struct {
	Name              *string
	Category          *string
	WIPLimit          OptInt
	AutoStartDelegate *bool
	AfterID           OptStr
}

// Update applies a patch — rename, category change, WIP limit, auto-start toggle, reorder —
// atomically. A category change that would strip the project's last required-category column
// fails with RequiredCategoryError, same as deleting it would.
func (s *Service) Update(ctx context.Context, id string, patch UpdatePatch) (ColumnWithCount, error) {
	var before, c domain.Column
	err := s.st.Tx(ctx, func(tx *store.Tx) error {
		var err error
		before, err = tx.Columns().ByID(ctx, id)
		if err != nil {
			return err
		}
		c = before

		var errs []httpx.FieldError
		if patch.Name != nil {
			if n := strings.TrimSpace(*patch.Name); n == "" {
				errs = append(errs, httpx.FieldError{Field: "name", Message: "Name is required."})
			} else {
				c.Name = n
			}
		}
		if patch.Category != nil {
			next := domain.ColumnCategory(*patch.Category)
			if !next.IsValid() {
				errs = append(errs, httpx.FieldError{Field: "category",
					Message: "One of backlog, ready, running, review, done, canceled."})
			} else if next != c.Category {
				if err := s.requireCategorySurvives(ctx, tx, c); err != nil {
					return err
				}
				c.Category = next
			}
		}
		if patch.WIPLimit.Set {
			if patch.WIPLimit.Null {
				c.WIPLimit = nil
			} else if patch.WIPLimit.Value < 1 {
				errs = append(errs, httpx.FieldError{Field: "wip_limit",
					Message: "Must be at least 1, or null for no limit."})
			} else {
				v := patch.WIPLimit.Value
				c.WIPLimit = &v
			}
		}
		if patch.AutoStartDelegate != nil {
			c.AutoStartDelegate = *patch.AutoStartDelegate
		}
		if len(errs) > 0 {
			return &ValidationError{Fields: errs}
		}

		if patch.AfterID.Set {
			pos, err := s.reorder(ctx, tx, &c, patch.AfterID)
			if err != nil {
				return err
			}
			c.Position = pos
		}

		c.UpdatedAt = s.now()
		return tx.Columns().Update(ctx, &c)
	})
	if err != nil {
		return ColumnWithCount{}, err
	}
	if err := s.audit.Write(ctx, "column.update",
		audit.Target{Kind: "column", ID: c.ID, ProjectID: c.ProjectID}, before, c); err != nil {
		return ColumnWithCount{}, err
	}
	s.emit(ctx, c.ProjectID, c, "updated")
	counts, err := s.st.Columns().TicketCounts(ctx, c.ProjectID)
	if err != nil {
		return ColumnWithCount{}, err
	}
	return ColumnWithCount{Column: c, TicketCount: counts[c.ID]}, nil
}

// reorder computes the column's new position: the midpoint of its new neighbours, or — when
// the midpoint would collide because the gap is exhausted — a fresh gap-spaced renumbering of
// every column, written inside the caller's transaction. Returns the column's new position.
func (s *Service) reorder(ctx context.Context, tx *store.Tx, c *domain.Column, after OptStr) (int64, error) {
	cols, err := tx.Columns().ForProject(ctx, c.ProjectID)
	if err != nil {
		return 0, err
	}
	// Rebuild the board order without the moving column, then find the insertion index.
	rest := make([]domain.Column, 0, len(cols))
	for _, col := range cols {
		if col.ID != c.ID {
			rest = append(rest, col)
		}
	}
	idx := 0 // null → front
	if !after.Null {
		if after.Value == c.ID {
			return 0, &ValidationError{Fields: []httpx.FieldError{
				{Field: "after_id", Message: "A column cannot be placed after itself."}}}
		}
		found := false
		for i, col := range rest {
			if col.ID == after.Value {
				idx, found = i+1, true
				break
			}
		}
		if !found {
			return 0, &ValidationError{Fields: []httpx.FieldError{
				{Field: "after_id", Message: "No such column on this board."}}}
		}
	}

	var lo, hi int64 // exclusive bounds for the new position
	switch {
	case len(rest) == 0:
		return positionGap, nil
	case idx == 0:
		lo, hi = 0, rest[0].Position
	case idx == len(rest):
		return rest[len(rest)-1].Position + positionGap, nil
	default:
		lo, hi = rest[idx-1].Position, rest[idx].Position
	}
	if mid := lo + (hi-lo)/2; mid > lo && mid < hi {
		return mid, nil
	}

	// Gap exhausted: renumber everything to multiples of positionGap in the final order.
	// The moving column itself is written by the caller with the position returned here.
	final := append(append(append([]domain.Column{}, rest[:idx]...), *c), rest[idx:]...)
	var pos int64
	for i := range final {
		final[i].Position = int64(i+1) * positionGap
		if final[i].ID == c.ID {
			pos = final[i].Position
			continue
		}
		final[i].UpdatedAt = s.now()
		if err := tx.Columns().Update(ctx, &final[i]); err != nil {
			return 0, err
		}
	}
	return pos, nil
}

// ---------------------------------------------------------------- delete -----

// Delete removes a column. If the column holds tickets, destinationID names the column that
// receives them — required, on the same board, and not the column being deleted; the move and
// the delete are one transaction. Deleting (or re-categorising) the project's last backlog,
// running or done column is refused with RequiredCategoryError.
func (s *Service) Delete(ctx context.Context, id, destinationID string) error {
	var (
		c     domain.Column
		moved int64
	)
	err := s.st.Tx(ctx, func(tx *store.Tx) error {
		var err error
		c, err = tx.Columns().ByID(ctx, id)
		if err != nil {
			return err
		}
		if err := s.requireCategorySurvives(ctx, tx, c); err != nil {
			return err
		}
		counts, err := tx.Columns().TicketCounts(ctx, c.ProjectID)
		if err != nil {
			return err
		}
		if counts[c.ID] > 0 {
			if destinationID == "" {
				return &ValidationError{Fields: []httpx.FieldError{{
					Field: "destination_column_id",
					Message: fmt.Sprintf("This column holds %d tickets; choose a column to move them to.",
						counts[c.ID]),
				}}}
			}
			if destinationID == c.ID {
				return &ValidationError{Fields: []httpx.FieldError{{
					Field:   "destination_column_id",
					Message: "The destination cannot be the column being deleted.",
				}}}
			}
			dest, err := tx.Columns().ByID(ctx, destinationID)
			if err != nil || dest.ProjectID != c.ProjectID {
				return &ValidationError{Fields: []httpx.FieldError{{
					Field:   "destination_column_id",
					Message: "No such column on this board.",
				}}}
			}
			if moved, err = tx.Tickets().MoveAllToColumn(ctx, c.ID, dest.ID, s.now()); err != nil {
				return err
			}
		}
		return tx.Columns().Delete(ctx, c.ID)
	})
	if err != nil {
		return err
	}
	note := ""
	if moved > 0 {
		note = fmt.Sprintf("moved %d tickets to column %s", moved, destinationID)
	}
	if err := s.audit.Write(ctx, "column.delete",
		audit.Target{Kind: "column", ID: c.ID, ProjectID: c.ProjectID, Note: note}, c, nil); err != nil {
		return err
	}
	s.emit(ctx, c.ProjectID, c, "deleted")
	return nil
}

// requireCategorySurvives fails with RequiredCategoryError when c is the last column of a
// required category on its board — the shared guardrail behind delete and category change.
func (s *Service) requireCategorySurvives(ctx context.Context, tx *store.Tx, c domain.Column) error {
	if !requiredCategories[c.Category] {
		return nil
	}
	same, err := tx.Columns().ByCategory(ctx, c.ProjectID, c.Category)
	if err != nil {
		return err
	}
	if len(same) <= 1 {
		return &RequiredCategoryError{Category: c.Category}
	}
	return nil
}

// ---------------------------------------------------------------- events -----

// emit publishes a board.updated bus event — the SSE event of the same name (contracts §5.1),
// scoped to the project topic. Best-effort: the mutation is committed and audited by the time
// this runs, so a bus failure is logged, never unwound.
func (s *Service) emit(ctx context.Context, projectID string, c domain.Column, action string) {
	if s.bus == nil {
		return
	}
	payload, _ := json.Marshal(map[string]any{
		"action": action,
		"column": map[string]string{"id": c.ID, "category": string(c.Category)},
	})
	pid := projectID
	cid := c.ID
	e := domain.Event{
		ProjectID: &pid, Kind: "board.updated", SubjectKind: "column", SubjectID: &cid,
		Payload: payload, OccurredAt: s.now(),
	}
	if a, ok := auth.ActorFrom(ctx); ok {
		e.ActorKind = a.Kind
		if a.ID != "" {
			aid := a.ID
			e.ActorID = &aid
		}
	}
	if err := s.bus.Emit(ctx, e); err != nil {
		s.logger.Error("board: emit failed",
			slog.String("kind", "board.updated"), slog.String("error", err.Error()))
	}
}
