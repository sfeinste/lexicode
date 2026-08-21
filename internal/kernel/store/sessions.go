package store

import (
	"context"
	"database/sql"

	"github.com/spruce/lexicode/internal/domain"
)

// SessionsRepo reads and writes the sessions table. Session IDs are hashes of the browser's
// opaque token; hashing happens in the auth layer (S05), never here.
type SessionsRepo struct{ h handle }

// Sessions returns the sessions repository.
func (s *Store) Sessions() *SessionsRepo { return &SessionsRepo{h: s.handle()} }

// Sessions returns the sessions repository bound to this transaction.
func (t *Tx) Sessions() *SessionsRepo { return &SessionsRepo{h: t.handle()} }

// Create inserts a session.
func (r *SessionsRepo) Create(ctx context.Context, sess *domain.Session) error {
	_, err := r.h.w.ExecContext(ctx, `
		INSERT INTO sessions (id, user_id, expires_at, user_agent, created_at)
		VALUES (?, ?, ?, ?, ?)`,
		sess.ID, sess.UserID, sess.ExpiresAt, nullStr(sess.UserAgent), sess.CreatedAt)
	return mapErr(err)
}

// ByID returns the session with this (hashed) ID, or ErrNotFound. Expiry is the caller's check:
// the repository returns what is stored.
func (r *SessionsRepo) ByID(ctx context.Context, id string) (domain.Session, error) {
	var (
		sess  domain.Session
		agent sql.NullString
	)
	err := r.h.r.QueryRowContext(ctx, `
		SELECT id, user_id, expires_at, user_agent, created_at FROM sessions WHERE id = ?`, id).
		Scan(&sess.ID, &sess.UserID, &sess.ExpiresAt, &agent, &sess.CreatedAt)
	if err != nil {
		return domain.Session{}, mapErr(err)
	}
	sess.UserAgent = strPtr(agent)
	return sess, nil
}

// Extend moves a session's expiry, for sliding refresh.
func (r *SessionsRepo) Extend(ctx context.Context, id, expiresAt string) error {
	res, err := r.h.w.ExecContext(ctx,
		`UPDATE sessions SET expires_at = ? WHERE id = ?`, expiresAt, id)
	if err != nil {
		return mapErr(err)
	}
	return errIfNone(res)
}

// Delete revokes one session. Deleting a session that does not exist is not an error: logout
// must be idempotent.
func (r *SessionsRepo) Delete(ctx context.Context, id string) error {
	_, err := r.h.w.ExecContext(ctx, `DELETE FROM sessions WHERE id = ?`, id)
	return mapErr(err)
}

// DeleteExpired removes every session that expired at or before now, returning how many.
func (r *SessionsRepo) DeleteExpired(ctx context.Context, now string) (int64, error) {
	res, err := r.h.w.ExecContext(ctx, `DELETE FROM sessions WHERE expires_at <= ?`, now)
	if err != nil {
		return 0, mapErr(err)
	}
	return res.RowsAffected()
}
