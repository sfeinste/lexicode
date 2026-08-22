package store

import (
	"context"

	"github.com/spruce/lexicode/internal/domain"
)

// RunContextItemsRepo reads and writes run_context_items — the recorded context stack of one
// run (architecture §11). Written once at enqueue by the scheduler's context resolution; read
// by the run detail's Context panel.
type RunContextItemsRepo struct{ h handle }

// RunContextItems returns the run context items repository.
func (s *Store) RunContextItems() *RunContextItemsRepo { return &RunContextItemsRepo{h: s.handle()} }

// RunContextItems returns the repository bound to this transaction.
func (t *Tx) RunContextItems() *RunContextItemsRepo { return &RunContextItemsRepo{h: t.handle()} }

const runContextCols = `id, run_id, provider, source_kind, source_ref, title, reason,
	tokens, position, injected`

// Create inserts one context item.
func (r *RunContextItemsRepo) Create(ctx context.Context, it *domain.RunContextItem) error {
	_, err := r.h.w.ExecContext(ctx, `
		INSERT INTO run_context_items (`+runContextCols+`)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		it.ID, it.RunID, it.Provider, it.SourceKind, it.SourceRef, it.Title, it.Reason,
		it.Tokens, it.Position, boolInt(it.Injected))
	return mapErr(err)
}

// ForRun returns a run's context items in stack order (position ascending).
func (r *RunContextItemsRepo) ForRun(ctx context.Context, runID string) ([]domain.RunContextItem, error) {
	rows, err := r.h.r.QueryContext(ctx, `
		SELECT `+runContextCols+` FROM run_context_items WHERE run_id = ? ORDER BY position`, runID)
	if err != nil {
		return nil, mapErr(err)
	}
	defer func() { _ = rows.Close() }()

	var out []domain.RunContextItem
	for rows.Next() {
		var (
			it       domain.RunContextItem
			injected int64
		)
		if err := rows.Scan(&it.ID, &it.RunID, &it.Provider, &it.SourceKind, &it.SourceRef,
			&it.Title, &it.Reason, &it.Tokens, &it.Position, &injected); err != nil {
			return nil, mapErr(err)
		}
		it.Injected = injected != 0
		out = append(out, it)
	}
	return out, rows.Err()
}
