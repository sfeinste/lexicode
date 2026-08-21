package store

import (
	"context"
	"database/sql"

	"github.com/spruce/lexicode/internal/domain"
)

// ColumnsRepo reads and writes the columns table. Code reaching for a column by its user-facing
// name is wrong by construction (plan rule 3); the lookups here are by ID, project and category.
type ColumnsRepo struct{ h handle }

// Columns returns the columns repository.
func (s *Store) Columns() *ColumnsRepo { return &ColumnsRepo{h: s.handle()} }

// Columns returns the columns repository bound to this transaction.
func (t *Tx) Columns() *ColumnsRepo { return &ColumnsRepo{h: t.handle()} }

const columnCols = `id, project_id, name, category, position, wip_limit, auto_start_delegate,
	created_at, updated_at`

// Create inserts a column.
func (r *ColumnsRepo) Create(ctx context.Context, c *domain.Column) error {
	_, err := r.h.w.ExecContext(ctx, `
		INSERT INTO columns (`+columnCols+`)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		c.ID, c.ProjectID, c.Name, string(c.Category), c.Position, nullInt(c.WIPLimit),
		boolInt(c.AutoStartDelegate), c.CreatedAt, c.UpdatedAt)
	return mapErr(err)
}

// ByID returns the column with this ID, or ErrNotFound.
func (r *ColumnsRepo) ByID(ctx context.Context, id string) (domain.Column, error) {
	return scanColumn(r.h.r.QueryRowContext(ctx,
		`SELECT `+columnCols+` FROM columns WHERE id = ?`, id))
}

// ForProject returns a project's columns in board order.
func (r *ColumnsRepo) ForProject(ctx context.Context, projectID string) ([]domain.Column, error) {
	rows, err := r.h.r.QueryContext(ctx,
		`SELECT `+columnCols+` FROM columns WHERE project_id = ? ORDER BY position, id`, projectID)
	if err != nil {
		return nil, mapErr(err)
	}
	defer func() { _ = rows.Close() }()

	var out []domain.Column
	for rows.Next() {
		c, err := scanColumn(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// ByCategory returns a project's columns of one category, in board order. Categories are not
// unique per project (data model §10.3), so this is a slice, never a single row.
func (r *ColumnsRepo) ByCategory(ctx context.Context, projectID string, cat domain.ColumnCategory) ([]domain.Column, error) {
	rows, err := r.h.r.QueryContext(ctx, `
		SELECT `+columnCols+` FROM columns
		WHERE project_id = ? AND category = ? ORDER BY position, id`,
		projectID, string(cat))
	if err != nil {
		return nil, mapErr(err)
	}
	defer func() { _ = rows.Close() }()

	var out []domain.Column
	for rows.Next() {
		c, err := scanColumn(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// Update rewrites a column's mutable fields (everything but id, project_id, created_at).
func (r *ColumnsRepo) Update(ctx context.Context, c *domain.Column) error {
	res, err := r.h.w.ExecContext(ctx, `
		UPDATE columns SET name = ?, category = ?, position = ?, wip_limit = ?,
			auto_start_delegate = ?, updated_at = ?
		WHERE id = ?`,
		c.Name, string(c.Category), c.Position, nullInt(c.WIPLimit),
		boolInt(c.AutoStartDelegate), c.UpdatedAt, c.ID)
	if err != nil {
		return mapErr(err)
	}
	return errIfNone(res)
}

func scanColumn(row rowScanner) (domain.Column, error) {
	var (
		c         domain.Column
		category  string
		wip       sql.NullInt64
		autoStart int64
	)
	err := row.Scan(&c.ID, &c.ProjectID, &c.Name, &category, &c.Position, &wip, &autoStart,
		&c.CreatedAt, &c.UpdatedAt)
	if err != nil {
		return domain.Column{}, mapErr(err)
	}
	c.Category = domain.ColumnCategory(category)
	c.WIPLimit = intPtr(wip)
	c.AutoStartDelegate = autoStart != 0
	return c, nil
}

// Delete removes a column. Tickets still referencing it make this an ErrForeignKey (deletes
// are RESTRICT, D-2) — the service moves them to a destination column first, in the same
// transaction.
func (r *ColumnsRepo) Delete(ctx context.Context, id string) error {
	res, err := r.h.w.ExecContext(ctx, `DELETE FROM columns WHERE id = ?`, id)
	if err != nil {
		return mapErr(err)
	}
	return errIfNone(res)
}

// TicketCounts returns, per column ID, how many tickets a project's columns hold. Archived
// tickets count too: they still reference the column (soft delete keeps the row), so they are
// what the delete guardrail must account for. Columns with no tickets are absent from the map.
func (r *ColumnsRepo) TicketCounts(ctx context.Context, projectID string) (map[string]int64, error) {
	rows, err := r.h.r.QueryContext(ctx, `
		SELECT column_id, COUNT(*) FROM tickets
		WHERE project_id = ?
		GROUP BY column_id`, projectID)
	if err != nil {
		return nil, mapErr(err)
	}
	defer func() { _ = rows.Close() }()

	out := map[string]int64{}
	for rows.Next() {
		var id string
		var n int64
		if err := rows.Scan(&id, &n); err != nil {
			return nil, mapErr(err)
		}
		out[id] = n
	}
	return out, rows.Err()
}
