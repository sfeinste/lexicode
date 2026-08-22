package store

import (
	"context"
	"database/sql"

	"github.com/spruce/lexicode/internal/domain"
)

// NotificationsRepo reads and writes the notifications table: one attention row per
// (user, run), updated in place — the UNIQUE(user_id, run_id) index is the schema-level
// "never stack" rule (interaction rule 3; architecture §12).
type NotificationsRepo struct{ h handle }

// Notifications returns the notifications repository.
func (s *Store) Notifications() *NotificationsRepo { return &NotificationsRepo{h: s.handle()} }

// Notifications returns the notifications repository bound to this transaction.
func (t *Tx) Notifications() *NotificationsRepo { return &NotificationsRepo{h: t.handle()} }

const notificationCols = `id, user_id, project_id, run_id, flavor, title, body, state,
	pushed, created_at, updated_at`

// Upsert inserts the notification, or — when a row for (user_id, run_id) already exists —
// updates that row in place: flavor, title, body and updated_at are refreshed and the row
// returns to `unread`, because an update means something new is waiting. The stored row
// (with its original id and created_at) is written back into n.
func (r *NotificationsRepo) Upsert(ctx context.Context, n *domain.Notification) error {
	if n.RunID == nil {
		// No run to key uniqueness on: plain insert.
		_, err := r.h.w.ExecContext(ctx, `
			INSERT INTO notifications (`+notificationCols+`)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			n.ID, n.UserID, n.ProjectID, nullStr(n.RunID), string(n.Flavor),
			n.Title, n.Body, string(n.State), boolInt(n.Pushed), n.CreatedAt, n.UpdatedAt)
		return mapErr(err)
	}
	_, err := r.h.w.ExecContext(ctx, `
		INSERT INTO notifications (`+notificationCols+`)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT (user_id, run_id) DO UPDATE SET
			flavor = excluded.flavor, title = excluded.title, body = excluded.body,
			state = 'unread', updated_at = excluded.updated_at`,
		n.ID, n.UserID, n.ProjectID, nullStr(n.RunID), string(n.Flavor),
		n.Title, n.Body, string(n.State), boolInt(n.Pushed), n.CreatedAt, n.UpdatedAt)
	if err != nil {
		return mapErr(err)
	}
	stored, err := r.ByUserAndRun(ctx, n.UserID, *n.RunID)
	if err != nil {
		return err
	}
	*n = stored
	return nil
}

// ByID returns the notification with this ID, or ErrNotFound.
func (r *NotificationsRepo) ByID(ctx context.Context, id string) (domain.Notification, error) {
	return scanNotification(r.h.r.QueryRowContext(ctx,
		`SELECT `+notificationCols+` FROM notifications WHERE id = ?`, id))
}

// ByUserAndRun returns the one row the unique index allows for (user, run), or ErrNotFound.
func (r *NotificationsRepo) ByUserAndRun(ctx context.Context, userID, runID string) (domain.Notification, error) {
	return scanNotification(r.h.r.QueryRowContext(ctx,
		`SELECT `+notificationCols+` FROM notifications WHERE user_id = ? AND run_id = ?`,
		userID, runID))
}

// ForRun returns every user's notification row for one run (the UNIQUE(user_id, run_id)
// index allows at most one per user) — what the S36 run-state subscriber updates in place
// when the run reaches a terminal state.
func (r *NotificationsRepo) ForRun(ctx context.Context, runID string) ([]domain.Notification, error) {
	rows, err := r.h.r.QueryContext(ctx, `
		SELECT `+notificationCols+` FROM notifications
		WHERE run_id = ? ORDER BY created_at, id`, runID)
	if err != nil {
		return nil, mapErr(err)
	}
	defer func() { _ = rows.Close() }()

	var out []domain.Notification
	for rows.Next() {
		n, err := scanNotification(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, n)
	}
	return out, rows.Err()
}

// ForUser returns a user's non-dismissed notifications, most recently updated first.
func (r *NotificationsRepo) ForUser(ctx context.Context, userID string) ([]domain.Notification, error) {
	rows, err := r.h.r.QueryContext(ctx, `
		SELECT `+notificationCols+` FROM notifications
		WHERE user_id = ? AND state != 'dismissed'
		ORDER BY updated_at DESC, id DESC`, userID)
	if err != nil {
		return nil, mapErr(err)
	}
	defer func() { _ = rows.Close() }()

	var out []domain.Notification
	for rows.Next() {
		n, err := scanNotification(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, n)
	}
	return out, rows.Err()
}

// UnreadCount returns how many unread notifications a user has — the inbox badge.
func (r *NotificationsRepo) UnreadCount(ctx context.Context, userID string) (int64, error) {
	var n int64
	err := r.h.r.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM notifications WHERE user_id = ? AND state = 'unread'`,
		userID).Scan(&n)
	if err != nil {
		return 0, mapErr(err)
	}
	return n, nil
}

// MarkState moves one notification to read or dismissed (unread happens only through Upsert).
func (r *NotificationsRepo) MarkState(ctx context.Context, id string, state domain.NotificationState, at string) error {
	res, err := r.h.w.ExecContext(ctx,
		`UPDATE notifications SET state = ?, updated_at = ? WHERE id = ?`,
		string(state), at, id)
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

func scanNotification(row rowScanner) (domain.Notification, error) {
	var (
		n             domain.Notification
		runID         sql.NullString
		flavor, state string
		pushed        int64
	)
	err := row.Scan(&n.ID, &n.UserID, &n.ProjectID, &runID, &flavor, &n.Title, &n.Body,
		&state, &pushed, &n.CreatedAt, &n.UpdatedAt)
	if err != nil {
		return domain.Notification{}, mapErr(err)
	}
	n.RunID = strPtr(runID)
	n.Flavor = domain.NotificationFlavor(flavor)
	n.State = domain.NotificationState(state)
	n.Pushed = pushed != 0
	return n, nil
}
