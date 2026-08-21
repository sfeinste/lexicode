package store

import (
	"context"
	"database/sql"

	"github.com/spruce/lexicode/internal/domain"
)

// TicketsRepo reads and writes the tickets table.
type TicketsRepo struct{ h handle }

// Tickets returns the tickets repository.
func (s *Store) Tickets() *TicketsRepo { return &TicketsRepo{h: s.handle()} }

// Tickets returns the tickets repository bound to this transaction.
func (t *Tx) Tickets() *TicketsRepo { return &TicketsRepo{h: t.handle()} }

const ticketCols = `id, project_id, seq, key, title, description, column_id, position, priority,
	assignee_id, delegate_agent_id, parent_id,
	pr_number, pr_state, pr_checks, pr_additions, pr_deletions, branch,
	origin, created_by_user_id, created_by_agent_id, archived_at, created_at, updated_at`

// Create inserts a ticket. Seq and Key come from ProjectsRepo.AllocateTicketSeq, ideally in the
// same transaction.
func (r *TicketsRepo) Create(ctx context.Context, tk *domain.Ticket) error {
	_, err := r.h.w.ExecContext(ctx, `
		INSERT INTO tickets (`+ticketCols+`)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		tk.ID, tk.ProjectID, tk.Seq, tk.Key, tk.Title, tk.Description, tk.ColumnID, tk.Position,
		string(tk.Priority), nullStr(tk.AssigneeID), nullStr(tk.DelegateAgentID),
		nullStr(tk.ParentID), nullInt(tk.PRNumber), nullStr(tk.PRState), nullStr(tk.PRChecks),
		nullInt(tk.PRAdditions), nullInt(tk.PRDeletions), nullStr(tk.Branch),
		string(tk.Origin), nullStr(tk.CreatedByUserID), nullStr(tk.CreatedByAgentID),
		nullStr(tk.ArchivedAt), tk.CreatedAt, tk.UpdatedAt)
	return mapErr(err)
}

// ByID returns the ticket with this ID, or ErrNotFound.
func (r *TicketsRepo) ByID(ctx context.Context, id string) (domain.Ticket, error) {
	return scanTicket(r.h.r.QueryRowContext(ctx,
		`SELECT `+ticketCols+` FROM tickets WHERE id = ?`, id))
}

// ByKey returns the ticket with this key ('PAY-14'), or ErrNotFound. Keys are unique in practice
// because (project_id, seq) is; the key column just denormalizes them.
func (r *TicketsRepo) ByKey(ctx context.Context, key string) (domain.Ticket, error) {
	return scanTicket(r.h.r.QueryRowContext(ctx,
		`SELECT `+ticketCols+` FROM tickets WHERE key = ?`, key))
}

// ForProject returns a project's unarchived tickets in board order (column, then position).
// Note the board itself must additionally exclude triage-pending tickets (data model §4); that
// query arrives with the board service (S10), which owns the rule.
func (r *TicketsRepo) ForProject(ctx context.Context, projectID string) ([]domain.Ticket, error) {
	rows, err := r.h.r.QueryContext(ctx, `
		SELECT `+ticketCols+` FROM tickets
		WHERE project_id = ? AND archived_at IS NULL
		ORDER BY column_id, position, id`, projectID)
	if err != nil {
		return nil, mapErr(err)
	}
	defer func() { _ = rows.Close() }()
	return collectTickets(rows)
}

// ForColumn returns a column's unarchived tickets in position order.
func (r *TicketsRepo) ForColumn(ctx context.Context, columnID string) ([]domain.Ticket, error) {
	rows, err := r.h.r.QueryContext(ctx, `
		SELECT `+ticketCols+` FROM tickets
		WHERE column_id = ? AND archived_at IS NULL
		ORDER BY position, id`, columnID)
	if err != nil {
		return nil, mapErr(err)
	}
	defer func() { _ = rows.Close() }()
	return collectTickets(rows)
}

// Move places a ticket in a column at a position (see domain.PositionBetween) and stamps
// updated_at. Column–project agreement is the service layer's invariant (data model §10.2).
func (r *TicketsRepo) Move(ctx context.Context, id, columnID string, position float64, now string) error {
	res, err := r.h.w.ExecContext(ctx, `
		UPDATE tickets SET column_id = ?, position = ?, updated_at = ? WHERE id = ?`,
		columnID, position, now, id)
	if err != nil {
		return mapErr(err)
	}
	return errIfNone(res)
}

// Update rewrites a ticket's mutable fields (everything but id, project_id, seq, key, origin,
// created_by_*, created_at).
func (r *TicketsRepo) Update(ctx context.Context, tk *domain.Ticket) error {
	res, err := r.h.w.ExecContext(ctx, `
		UPDATE tickets SET title = ?, description = ?, column_id = ?, position = ?, priority = ?,
			assignee_id = ?, delegate_agent_id = ?, parent_id = ?,
			pr_number = ?, pr_state = ?, pr_checks = ?, pr_additions = ?, pr_deletions = ?,
			branch = ?, archived_at = ?, updated_at = ?
		WHERE id = ?`,
		tk.Title, tk.Description, tk.ColumnID, tk.Position, string(tk.Priority),
		nullStr(tk.AssigneeID), nullStr(tk.DelegateAgentID), nullStr(tk.ParentID),
		nullInt(tk.PRNumber), nullStr(tk.PRState), nullStr(tk.PRChecks),
		nullInt(tk.PRAdditions), nullInt(tk.PRDeletions), nullStr(tk.Branch),
		nullStr(tk.ArchivedAt), tk.UpdatedAt, tk.ID)
	if err != nil {
		return mapErr(err)
	}
	return errIfNone(res)
}

func collectTickets(rows *sql.Rows) ([]domain.Ticket, error) {
	var out []domain.Ticket
	for rows.Next() {
		tk, err := scanTicket(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, tk)
	}
	return out, rows.Err()
}

func scanTicket(row rowScanner) (domain.Ticket, error) {
	var (
		tk                                      domain.Ticket
		priority, origin                        string
		assignee, delegate, parent              sql.NullString
		prState, prChecks, branch               sql.NullString
		prNumber, prAdd, prDel                  sql.NullInt64
		createdByUser, createdByAgent, archived sql.NullString
	)
	err := row.Scan(&tk.ID, &tk.ProjectID, &tk.Seq, &tk.Key, &tk.Title, &tk.Description,
		&tk.ColumnID, &tk.Position, &priority, &assignee, &delegate, &parent,
		&prNumber, &prState, &prChecks, &prAdd, &prDel, &branch,
		&origin, &createdByUser, &createdByAgent, &archived, &tk.CreatedAt, &tk.UpdatedAt)
	if err != nil {
		return domain.Ticket{}, mapErr(err)
	}
	tk.Priority = domain.Priority(priority)
	tk.Origin = domain.TicketOrigin(origin)
	tk.AssigneeID = strPtr(assignee)
	tk.DelegateAgentID = strPtr(delegate)
	tk.ParentID = strPtr(parent)
	tk.PRNumber = intPtr(prNumber)
	tk.PRState = strPtr(prState)
	tk.PRChecks = strPtr(prChecks)
	tk.PRAdditions = intPtr(prAdd)
	tk.PRDeletions = intPtr(prDel)
	tk.Branch = strPtr(branch)
	tk.CreatedByUserID = strPtr(createdByUser)
	tk.CreatedByAgentID = strPtr(createdByAgent)
	tk.ArchivedAt = strPtr(archived)
	return tk, nil
}

// MoveAllToColumn moves every ticket (archived ones included — they must not keep a foreign key
// into a column being deleted) from one column to the end of another, preserving their relative
// order, and returns how many moved. The offset arithmetic runs on values read before the
// UPDATE, so the statement never chases its own writes; call it inside a transaction with the
// column mutation it accompanies.
func (r *TicketsRepo) MoveAllToColumn(ctx context.Context, fromColumnID, toColumnID, now string) (int64, error) {
	var minFrom, maxTo sql.NullFloat64
	if err := r.h.r.QueryRowContext(ctx,
		`SELECT MIN(position) FROM tickets WHERE column_id = ?`, fromColumnID).Scan(&minFrom); err != nil {
		return 0, mapErr(err)
	}
	if !minFrom.Valid {
		return 0, nil // nothing to move
	}
	if err := r.h.r.QueryRowContext(ctx,
		`SELECT MAX(position) FROM tickets WHERE column_id = ?`, toColumnID).Scan(&maxTo); err != nil {
		return 0, mapErr(err)
	}
	offset := maxTo.Float64 + 1 - minFrom.Float64 // maxTo is 0 when the destination is empty
	res, err := r.h.w.ExecContext(ctx, `
		UPDATE tickets SET column_id = ?, position = position + ?, updated_at = ?
		WHERE column_id = ?`,
		toColumnID, offset, now, fromColumnID)
	if err != nil {
		return 0, mapErr(err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return 0, mapErr(err)
	}
	return n, nil
}
