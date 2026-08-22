package store

import (
	"context"

	"github.com/spruce/lexicode/internal/domain"
)

// RunOutputsRepo reads and writes the run_outputs table — one row per artifact a run produced.
// The forge adapter appends here (through the narrow writer it is constructed with) after every
// successful write (contracts §2.2 step 3).
type RunOutputsRepo struct{ h handle }

// RunOutputs returns the run outputs repository.
func (s *Store) RunOutputs() *RunOutputsRepo { return &RunOutputsRepo{h: s.handle()} }

// RunOutputs returns the run outputs repository bound to this transaction.
func (t *Tx) RunOutputs() *RunOutputsRepo { return &RunOutputsRepo{h: t.handle()} }

const runOutputCols = `id, run_id, kind, ref, url, summary, created_at`

// Append inserts one output row.
func (r *RunOutputsRepo) Append(ctx context.Context, o *domain.RunOutput) error {
	_, err := r.h.w.ExecContext(ctx, `
		INSERT INTO run_outputs (`+runOutputCols+`) VALUES (?, ?, ?, ?, ?, ?, ?)`,
		o.ID, o.RunID, string(o.Kind), o.Ref, o.URL, o.Summary, o.CreatedAt)
	return mapErr(err)
}

// ForRun returns a run's outputs, oldest first.
func (r *RunOutputsRepo) ForRun(ctx context.Context, runID string) ([]domain.RunOutput, error) {
	rows, err := r.h.r.QueryContext(ctx, `
		SELECT `+runOutputCols+` FROM run_outputs WHERE run_id = ? ORDER BY created_at, id`, runID)
	if err != nil {
		return nil, mapErr(err)
	}
	defer func() { _ = rows.Close() }()

	var out []domain.RunOutput
	for rows.Next() {
		var (
			o    domain.RunOutput
			kind string
		)
		if err := rows.Scan(&o.ID, &o.RunID, &kind, &o.Ref, &o.URL, &o.Summary, &o.CreatedAt); err != nil {
			return nil, mapErr(err)
		}
		o.Kind = domain.RunOutputKind(kind)
		out = append(out, o)
	}
	return out, rows.Err()
}
