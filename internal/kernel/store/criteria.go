package store

import (
	"context"
	"database/sql"

	"github.com/spruce/lexicode/internal/domain"
)

// CriteriaRepo reads and writes the acceptance_criteria table.
type CriteriaRepo struct{ h handle }

// Criteria returns the acceptance-criteria repository.
func (s *Store) Criteria() *CriteriaRepo { return &CriteriaRepo{h: s.handle()} }

// Criteria returns the acceptance-criteria repository bound to this transaction.
func (t *Tx) Criteria() *CriteriaRepo { return &CriteriaRepo{h: t.handle()} }

const criterionCols = `id, ticket_id, position, text, checked, checked_by_run_id,
	checked_by_user_id, note, updated_at`

// Create inserts a criterion.
func (r *CriteriaRepo) Create(ctx context.Context, c *domain.Criterion) error {
	_, err := r.h.w.ExecContext(ctx, `
		INSERT INTO acceptance_criteria (`+criterionCols+`)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		c.ID, c.TicketID, c.Position, c.Text, boolInt(c.Checked),
		nullStr(c.CheckedByRunID), nullStr(c.CheckedByUserID), c.Note, c.UpdatedAt)
	return mapErr(err)
}

// ByID returns the criterion with this ID, or ErrNotFound.
func (r *CriteriaRepo) ByID(ctx context.Context, id string) (domain.Criterion, error) {
	return scanCriterion(r.h.r.QueryRowContext(ctx,
		`SELECT `+criterionCols+` FROM acceptance_criteria WHERE id = ?`, id))
}

// ForTicket returns a ticket's criteria in checklist order.
func (r *CriteriaRepo) ForTicket(ctx context.Context, ticketID string) ([]domain.Criterion, error) {
	rows, err := r.h.r.QueryContext(ctx, `
		SELECT `+criterionCols+` FROM acceptance_criteria
		WHERE ticket_id = ? ORDER BY position, id`, ticketID)
	if err != nil {
		return nil, mapErr(err)
	}
	defer func() { _ = rows.Close() }()

	var out []domain.Criterion
	for rows.Next() {
		c, err := scanCriterion(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// Update rewrites a criterion's mutable fields (everything but id and ticket_id).
func (r *CriteriaRepo) Update(ctx context.Context, c *domain.Criterion) error {
	res, err := r.h.w.ExecContext(ctx, `
		UPDATE acceptance_criteria
		SET position = ?, text = ?, checked = ?, checked_by_run_id = ?,
			checked_by_user_id = ?, note = ?, updated_at = ?
		WHERE id = ?`,
		c.Position, c.Text, boolInt(c.Checked), nullStr(c.CheckedByRunID),
		nullStr(c.CheckedByUserID), c.Note, c.UpdatedAt, c.ID)
	if err != nil {
		return mapErr(err)
	}
	return errIfNone(res)
}

// CriteriaCounts is a ticket's checklist progress — what the board card renders as
// "▸ 3/5 acceptance criteria" (UI spec §5.3) without loading the rows.
type CriteriaCounts struct {
	Total   int64
	Checked int64
}

// CountsForTicket returns one ticket's criteria progress. A ticket with no criteria
// returns zeros, not an error.
func (r *CriteriaRepo) CountsForTicket(ctx context.Context, ticketID string) (CriteriaCounts, error) {
	var c CriteriaCounts
	err := r.h.r.QueryRowContext(ctx, `
		SELECT COUNT(*), COALESCE(SUM(checked), 0)
		FROM acceptance_criteria WHERE ticket_id = ?`, ticketID).Scan(&c.Total, &c.Checked)
	if err != nil {
		return CriteriaCounts{}, mapErr(err)
	}
	return c, nil
}

// CountsForProjectTickets returns ticket_id → criteria progress for every ticket of a
// project — the one query the ticket list needs instead of one per row (same shape as
// LabelsRepo.IDsForProjectTickets). Tickets without criteria are absent from the map.
func (r *CriteriaRepo) CountsForProjectTickets(ctx context.Context, projectID string) (map[string]CriteriaCounts, error) {
	rows, err := r.h.r.QueryContext(ctx, `
		SELECT ac.ticket_id, COUNT(*), COALESCE(SUM(ac.checked), 0)
		FROM acceptance_criteria ac JOIN tickets t ON t.id = ac.ticket_id
		WHERE t.project_id = ? GROUP BY ac.ticket_id`, projectID)
	if err != nil {
		return nil, mapErr(err)
	}
	defer func() { _ = rows.Close() }()

	out := map[string]CriteriaCounts{}
	for rows.Next() {
		var ticketID string
		var c CriteriaCounts
		if err := rows.Scan(&ticketID, &c.Total, &c.Checked); err != nil {
			return nil, mapErr(err)
		}
		out[ticketID] = c
	}
	return out, rows.Err()
}

// Delete removes a criterion.
func (r *CriteriaRepo) Delete(ctx context.Context, id string) error {
	res, err := r.h.w.ExecContext(ctx, `DELETE FROM acceptance_criteria WHERE id = ?`, id)
	if err != nil {
		return mapErr(err)
	}
	return errIfNone(res)
}

func scanCriterion(row rowScanner) (domain.Criterion, error) {
	var (
		c             domain.Criterion
		checked       int64
		byRun, byUser sql.NullString
	)
	err := row.Scan(&c.ID, &c.TicketID, &c.Position, &c.Text, &checked,
		&byRun, &byUser, &c.Note, &c.UpdatedAt)
	if err != nil {
		return domain.Criterion{}, mapErr(err)
	}
	c.Checked = checked != 0
	c.CheckedByRunID = strPtr(byRun)
	c.CheckedByUserID = strPtr(byUser)
	return c, nil
}
