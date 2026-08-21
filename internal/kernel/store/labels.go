package store

import (
	"context"

	"github.com/spruce/lexicode/internal/domain"
)

// LabelsRepo reads and writes the labels and ticket_labels tables.
type LabelsRepo struct{ h handle }

// Labels returns the labels repository.
func (s *Store) Labels() *LabelsRepo { return &LabelsRepo{h: s.handle()} }

// Labels returns the labels repository bound to this transaction.
func (t *Tx) Labels() *LabelsRepo { return &LabelsRepo{h: t.handle()} }

const labelCols = `id, project_id, name, color`

// Create inserts a label. A duplicate name within the project surfaces as ErrUnique.
func (r *LabelsRepo) Create(ctx context.Context, l *domain.Label) error {
	_, err := r.h.w.ExecContext(ctx,
		`INSERT INTO labels (`+labelCols+`) VALUES (?, ?, ?, ?)`,
		l.ID, l.ProjectID, l.Name, l.Color)
	return mapErr(err)
}

// ByID returns the label with this ID, or ErrNotFound.
func (r *LabelsRepo) ByID(ctx context.Context, id string) (domain.Label, error) {
	return scanLabel(r.h.r.QueryRowContext(ctx,
		`SELECT `+labelCols+` FROM labels WHERE id = ?`, id))
}

// ForProject returns a project's labels sorted by name.
func (r *LabelsRepo) ForProject(ctx context.Context, projectID string) ([]domain.Label, error) {
	rows, err := r.h.r.QueryContext(ctx,
		`SELECT `+labelCols+` FROM labels WHERE project_id = ? ORDER BY name, id`, projectID)
	if err != nil {
		return nil, mapErr(err)
	}
	defer func() { _ = rows.Close() }()

	var out []domain.Label
	for rows.Next() {
		l, err := scanLabel(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, l)
	}
	return out, rows.Err()
}

// Update rewrites a label's name and color. A duplicate name surfaces as ErrUnique.
func (r *LabelsRepo) Update(ctx context.Context, l *domain.Label) error {
	res, err := r.h.w.ExecContext(ctx,
		`UPDATE labels SET name = ?, color = ? WHERE id = ?`, l.Name, l.Color, l.ID)
	if err != nil {
		return mapErr(err)
	}
	return errIfNone(res)
}

// Delete removes a label row. Detach the label from every ticket first (DetachAll, in the same
// transaction) — ticket_labels references labels and deletes are RESTRICT (D-2).
func (r *LabelsRepo) Delete(ctx context.Context, id string) error {
	res, err := r.h.w.ExecContext(ctx, `DELETE FROM labels WHERE id = ?`, id)
	if err != nil {
		return mapErr(err)
	}
	return errIfNone(res)
}

// Attach adds a label to a ticket. Attaching twice surfaces as ErrUnique.
func (r *LabelsRepo) Attach(ctx context.Context, ticketID, labelID string) error {
	_, err := r.h.w.ExecContext(ctx,
		`INSERT INTO ticket_labels (ticket_id, label_id) VALUES (?, ?)`, ticketID, labelID)
	return mapErr(err)
}

// Detach removes a label from a ticket, reporting whether a row was actually removed.
func (r *LabelsRepo) Detach(ctx context.Context, ticketID, labelID string) (bool, error) {
	res, err := r.h.w.ExecContext(ctx,
		`DELETE FROM ticket_labels WHERE ticket_id = ? AND label_id = ?`, ticketID, labelID)
	if err != nil {
		return false, mapErr(err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, mapErr(err)
	}
	return n > 0, nil
}

// DetachAll removes a label from every ticket, returning how many attachments were removed.
func (r *LabelsRepo) DetachAll(ctx context.Context, labelID string) (int64, error) {
	res, err := r.h.w.ExecContext(ctx,
		`DELETE FROM ticket_labels WHERE label_id = ?`, labelID)
	if err != nil {
		return 0, mapErr(err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return 0, mapErr(err)
	}
	return n, nil
}

// ForTicket returns one ticket's labels sorted by name.
func (r *LabelsRepo) ForTicket(ctx context.Context, ticketID string) ([]domain.Label, error) {
	rows, err := r.h.r.QueryContext(ctx, `
		SELECT l.id, l.project_id, l.name, l.color
		FROM ticket_labels tl JOIN labels l ON l.id = tl.label_id
		WHERE tl.ticket_id = ? ORDER BY l.name, l.id`, ticketID)
	if err != nil {
		return nil, mapErr(err)
	}
	defer func() { _ = rows.Close() }()

	var out []domain.Label
	for rows.Next() {
		l, err := scanLabel(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, l)
	}
	return out, rows.Err()
}

// IDsForProjectTickets returns ticket_id → attached label IDs for every ticket of a project —
// the one query the ticket list needs instead of one per row.
func (r *LabelsRepo) IDsForProjectTickets(ctx context.Context, projectID string) (map[string][]string, error) {
	rows, err := r.h.r.QueryContext(ctx, `
		SELECT tl.ticket_id, tl.label_id
		FROM ticket_labels tl JOIN labels l ON l.id = tl.label_id
		WHERE l.project_id = ? ORDER BY l.name, l.id`, projectID)
	if err != nil {
		return nil, mapErr(err)
	}
	defer func() { _ = rows.Close() }()

	out := map[string][]string{}
	for rows.Next() {
		var ticketID, labelID string
		if err := rows.Scan(&ticketID, &labelID); err != nil {
			return nil, mapErr(err)
		}
		out[ticketID] = append(out[ticketID], labelID)
	}
	return out, rows.Err()
}

func scanLabel(row rowScanner) (domain.Label, error) {
	var l domain.Label
	if err := row.Scan(&l.ID, &l.ProjectID, &l.Name, &l.Color); err != nil {
		return domain.Label{}, mapErr(err)
	}
	return l, nil
}
