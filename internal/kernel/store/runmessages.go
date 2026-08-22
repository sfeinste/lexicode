package store

import (
	"context"
	"database/sql"

	"github.com/spruce/lexicode/internal/domain"
)

// RunMessagesRepo reads and writes run_messages — the steering queue ("queue, don't
// interrupt", D-12). The API creates rows `queued`; the scheduler's supervisor drains them
// into Handle.Steer and stamps delivered_at, or drops what a terminal run can no longer hear.
type RunMessagesRepo struct{ h handle }

// RunMessages returns the run messages repository.
func (s *Store) RunMessages() *RunMessagesRepo { return &RunMessagesRepo{h: s.handle()} }

// RunMessages returns the run messages repository bound to this transaction.
func (t *Tx) RunMessages() *RunMessagesRepo { return &RunMessagesRepo{h: t.handle()} }

const runMessageCols = `id, run_id, body, author_id, state, created_at, delivered_at`

// Create inserts one queued steering message.
func (r *RunMessagesRepo) Create(ctx context.Context, m *domain.RunMessage) error {
	_, err := r.h.w.ExecContext(ctx, `
		INSERT INTO run_messages (`+runMessageCols+`) VALUES (?, ?, ?, ?, ?, ?, ?)`,
		m.ID, m.RunID, m.Body, nullStr(m.AuthorID), string(m.State),
		m.CreatedAt, nullStr(m.DeliveredAt))
	return mapErr(err)
}

// QueuedForRun returns a run's undelivered messages, oldest first — delivery order.
func (r *RunMessagesRepo) QueuedForRun(ctx context.Context, runID string) ([]domain.RunMessage, error) {
	rows, err := r.h.r.QueryContext(ctx, `
		SELECT `+runMessageCols+` FROM run_messages
		WHERE run_id = ? AND state = 'queued' ORDER BY created_at, id`, runID)
	if err != nil {
		return nil, mapErr(err)
	}
	defer func() { _ = rows.Close() }()
	return collectRunMessages(rows)
}

// ForRun returns all of a run's messages, oldest first.
func (r *RunMessagesRepo) ForRun(ctx context.Context, runID string) ([]domain.RunMessage, error) {
	rows, err := r.h.r.QueryContext(ctx, `
		SELECT `+runMessageCols+` FROM run_messages
		WHERE run_id = ? ORDER BY created_at, id`, runID)
	if err != nil {
		return nil, mapErr(err)
	}
	defer func() { _ = rows.Close() }()
	return collectRunMessages(rows)
}

// MarkDelivered flips one queued message to delivered at the given instant. ErrNotFound when
// the row is not queued (already delivered, dropped, or missing) — the caller lost a race.
func (r *RunMessagesRepo) MarkDelivered(ctx context.Context, id, at string) error {
	return r.mark(ctx, id, domain.MessageDelivered, &at)
}

// DropQueued drops every still-queued message of a run — the terminal-state sweep: a message
// a run can no longer hear is honestly `dropped`, never silently forgotten. Returns how many.
func (r *RunMessagesRepo) DropQueued(ctx context.Context, runID string) (int64, error) {
	res, err := r.h.w.ExecContext(ctx, `
		UPDATE run_messages SET state = 'dropped' WHERE run_id = ? AND state = 'queued'`, runID)
	if err != nil {
		return 0, mapErr(err)
	}
	n, err := res.RowsAffected()
	return n, mapErr(err)
}

func (r *RunMessagesRepo) mark(ctx context.Context, id string, state domain.RunMessageState, deliveredAt *string) error {
	res, err := r.h.w.ExecContext(ctx, `
		UPDATE run_messages SET state = ?, delivered_at = ? WHERE id = ? AND state = 'queued'`,
		string(state), nullStr(deliveredAt), id)
	if err != nil {
		return mapErr(err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return mapErr(err)
	}
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

func collectRunMessages(rows *sql.Rows) ([]domain.RunMessage, error) {
	var out []domain.RunMessage
	for rows.Next() {
		var (
			m                     domain.RunMessage
			state                 string
			authorID, deliveredAt sql.NullString
		)
		if err := rows.Scan(&m.ID, &m.RunID, &m.Body, &authorID, &state,
			&m.CreatedAt, &deliveredAt); err != nil {
			return nil, mapErr(err)
		}
		m.State = domain.RunMessageState(state)
		m.AuthorID = strPtr(authorID)
		m.DeliveredAt = strPtr(deliveredAt)
		out = append(out, m)
	}
	return out, rows.Err()
}
