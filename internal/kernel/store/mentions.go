package store

import (
	"context"

	"github.com/spruce/lexicode/internal/domain"
)

// MentionsRepo reads and writes the mentions table (data model §5): `@` references parsed out
// of comment and description bodies (S12) and wiki pages (later stories). Rows are replaced
// wholesale per source — an edited body re-derives its mentions rather than diffing them.
type MentionsRepo struct{ h handle }

// Mentions returns the mentions repository.
func (s *Store) Mentions() *MentionsRepo { return &MentionsRepo{h: s.handle()} }

// Mentions returns the mentions repository bound to this transaction.
func (t *Tx) Mentions() *MentionsRepo { return &MentionsRepo{h: t.handle()} }

const mentionCols = `id, project_id, from_kind, from_id, to_kind, to_id, linked, context_text`

// ReplaceForSource deletes every mention row for one source and inserts the given set — the
// write shape for "this body was (re)saved". Call inside the mutation's transaction.
func (r *MentionsRepo) ReplaceForSource(ctx context.Context, fromKind, fromID string, ms []domain.Mention) error {
	if _, err := r.h.w.ExecContext(ctx,
		`DELETE FROM mentions WHERE from_kind = ? AND from_id = ?`, fromKind, fromID); err != nil {
		return mapErr(err)
	}
	for i := range ms {
		m := &ms[i]
		_, err := r.h.w.ExecContext(ctx, `
			INSERT INTO mentions (`+mentionCols+`)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
			m.ID, m.ProjectID, m.FromKind, m.FromID, m.ToKind, m.ToID,
			boolInt(m.Linked), m.ContextText)
		if err != nil {
			return mapErr(err)
		}
	}
	return nil
}

// ForSource returns every mention parsed out of one source body, insertion order.
func (r *MentionsRepo) ForSource(ctx context.Context, fromKind, fromID string) ([]domain.Mention, error) {
	rows, err := r.h.r.QueryContext(ctx, `
		SELECT `+mentionCols+` FROM mentions
		WHERE from_kind = ? AND from_id = ? ORDER BY id`, fromKind, fromID)
	if err != nil {
		return nil, mapErr(err)
	}
	defer func() { _ = rows.Close() }()

	var out []domain.Mention
	for rows.Next() {
		var (
			m      domain.Mention
			linked int64
		)
		err := rows.Scan(&m.ID, &m.ProjectID, &m.FromKind, &m.FromID, &m.ToKind, &m.ToID,
			&linked, &m.ContextText)
		if err != nil {
			return nil, mapErr(err)
		}
		m.Linked = linked != 0
		out = append(out, m)
	}
	return out, rows.Err()
}

// ForTarget returns every mention pointing at one target — the backlinks read (UI spec §5.6),
// context paragraph included.
func (r *MentionsRepo) ForTarget(ctx context.Context, toKind, toID string) ([]domain.Mention, error) {
	rows, err := r.h.r.QueryContext(ctx, `
		SELECT `+mentionCols+` FROM mentions
		WHERE to_kind = ? AND to_id = ? ORDER BY id`, toKind, toID)
	if err != nil {
		return nil, mapErr(err)
	}
	defer func() { _ = rows.Close() }()

	var out []domain.Mention
	for rows.Next() {
		var (
			m      domain.Mention
			linked int64
		)
		err := rows.Scan(&m.ID, &m.ProjectID, &m.FromKind, &m.FromID, &m.ToKind, &m.ToID,
			&linked, &m.ContextText)
		if err != nil {
			return nil, mapErr(err)
		}
		m.Linked = linked != 0
		out = append(out, m)
	}
	return out, rows.Err()
}
