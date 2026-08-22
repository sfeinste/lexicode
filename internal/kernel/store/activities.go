package store

import (
	"context"
	"database/sql"
	"encoding/json"

	"github.com/spruce/lexicode/internal/domain"
)

// ActivitiesRepo appends to and reads a run's activity transcript, keyed (run_id, seq).
type ActivitiesRepo struct{ h handle }

// Activities returns the activities repository.
func (s *Store) Activities() *ActivitiesRepo { return &ActivitiesRepo{h: s.handle()} }

// Activities returns the activities repository bound to this transaction.
func (t *Tx) Activities() *ActivitiesRepo { return &ActivitiesRepo{h: t.handle()} }

const activityCols = `run_id, seq, type, level, tool_name, group_key, title, payload, ok, attempt,
	duration_ms, queued_ms, model_ms, tool_ms, cost_cents, tokens_in, tokens_out, created_at`

// Append inserts one activity. A repeated (run_id, seq) surfaces as ErrUnique.
func (r *ActivitiesRepo) Append(ctx context.Context, a *domain.Activity) error {
	_, err := r.h.w.ExecContext(ctx, `
		INSERT INTO activities (`+activityCols+`)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		a.RunID, a.Seq, string(a.Type), a.Level, a.ToolName, a.GroupKey, a.Title,
		rawText(a.Payload, "{}"), nullBool(a.OK), a.Attempt,
		nullInt(a.DurationMS), nullInt(a.QueuedMS), nullInt(a.ModelMS), nullInt(a.ToolMS),
		a.CostCents, a.TokensIn, a.TokensOut, a.CreatedAt)
	return mapErr(err)
}

// AppendNext inserts one activity at the run's next free seq (MAX(seq)+1, or 0 for the first
// row), writing the allocated seq back onto a. The seq subquery runs inside the INSERT, and
// SQLite has a single writer, so concurrent appenders cannot collide. Callers that number
// their own transcript (the S20 ingest) keep using Append; AppendNext is for out-of-band
// appenders — the S18 egress proxy's decision log — that must interleave safely.
func (r *ActivitiesRepo) AppendNext(ctx context.Context, a *domain.Activity) error {
	row := r.h.w.QueryRowContext(ctx, `
		INSERT INTO activities (`+activityCols+`)
		VALUES (?, (SELECT COALESCE(MAX(seq), -1) + 1 FROM activities WHERE run_id = ?),
			?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		RETURNING seq`,
		a.RunID, a.RunID, string(a.Type), a.Level, a.ToolName, a.GroupKey, a.Title,
		rawText(a.Payload, "{}"), nullBool(a.OK), a.Attempt,
		nullInt(a.DurationMS), nullInt(a.QueuedMS), nullInt(a.ModelMS), nullInt(a.ToolMS),
		a.CostCents, a.TokensIn, a.TokensOut, a.CreatedAt)
	if err := row.Scan(&a.Seq); err != nil {
		return mapErr(err)
	}
	return nil
}

// Update rewrites the activity at (run_id, seq) — the tool_result-merge path: the runtime
// adapter re-emits an action's sequence once its result arrives, and the scheduler's sink maps
// that back onto the persisted row (contracts §2.4 RunSink semantics). ErrNotFound when no
// such row exists.
func (r *ActivitiesRepo) Update(ctx context.Context, a *domain.Activity) error {
	res, err := r.h.w.ExecContext(ctx, `
		UPDATE activities SET type = ?, level = ?, tool_name = ?, group_key = ?, title = ?,
			payload = ?, ok = ?, attempt = ?, duration_ms = ?, queued_ms = ?, model_ms = ?,
			tool_ms = ?, cost_cents = ?, tokens_in = ?, tokens_out = ?
		WHERE run_id = ? AND seq = ?`,
		string(a.Type), a.Level, a.ToolName, a.GroupKey, a.Title,
		rawText(a.Payload, "{}"), nullBool(a.OK), a.Attempt,
		nullInt(a.DurationMS), nullInt(a.QueuedMS), nullInt(a.ModelMS), nullInt(a.ToolMS),
		a.CostCents, a.TokensIn, a.TokensOut, a.RunID, a.Seq)
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

// MaxSeq returns the highest seq of a run's transcript, or -1 for an empty one.
func (r *ActivitiesRepo) MaxSeq(ctx context.Context, runID string) (int64, error) {
	var seq int64
	err := r.h.r.QueryRowContext(ctx,
		`SELECT COALESCE(MAX(seq), -1) FROM activities WHERE run_id = ?`, runID).Scan(&seq)
	return seq, mapErr(err)
}

// ForRun returns a run's transcript in step order.
// ByRunSeq returns one activity row by its (run_id, seq) key, or ErrNotFound — how the
// notify service reads the elicitation's level-0 title (S24).
func (r *ActivitiesRepo) ByRunSeq(ctx context.Context, runID string, seq int64) (domain.Activity, error) {
	row := r.h.r.QueryRowContext(ctx,
		`SELECT `+activityCols+` FROM activities WHERE run_id = ? AND seq = ?`, runID, seq)
	var (
		a                                domain.Activity
		typ, payload                     string
		ok                               sql.NullInt64
		duration, queued, model, toolDur sql.NullInt64
	)
	err := row.Scan(&a.RunID, &a.Seq, &typ, &a.Level, &a.ToolName, &a.GroupKey, &a.Title,
		&payload, &ok, &a.Attempt, &duration, &queued, &model, &toolDur,
		&a.CostCents, &a.TokensIn, &a.TokensOut, &a.CreatedAt)
	if err != nil {
		return domain.Activity{}, mapErr(err)
	}
	a.Type = domain.ActivityType(typ)
	a.Payload = json.RawMessage(payload)
	a.OK = boolPtr(ok)
	a.DurationMS = intPtr(duration)
	a.QueuedMS = intPtr(queued)
	a.ModelMS = intPtr(model)
	a.ToolMS = intPtr(toolDur)
	return a, nil
}

func (r *ActivitiesRepo) ForRun(ctx context.Context, runID string) ([]domain.Activity, error) {
	rows, err := r.h.r.QueryContext(ctx,
		`SELECT `+activityCols+` FROM activities WHERE run_id = ? ORDER BY seq`, runID)
	if err != nil {
		return nil, mapErr(err)
	}
	defer func() { _ = rows.Close() }()

	var out []domain.Activity
	for rows.Next() {
		var (
			a                                domain.Activity
			typ, payload                     string
			ok                               sql.NullInt64
			duration, queued, model, toolDur sql.NullInt64
		)
		err := rows.Scan(&a.RunID, &a.Seq, &typ, &a.Level, &a.ToolName, &a.GroupKey, &a.Title,
			&payload, &ok, &a.Attempt, &duration, &queued, &model, &toolDur,
			&a.CostCents, &a.TokensIn, &a.TokensOut, &a.CreatedAt)
		if err != nil {
			return nil, mapErr(err)
		}
		a.Type = domain.ActivityType(typ)
		a.Payload = json.RawMessage(payload)
		a.OK = boolPtr(ok)
		a.DurationMS = intPtr(duration)
		a.QueuedMS = intPtr(queued)
		a.ModelMS = intPtr(model)
		a.ToolMS = intPtr(toolDur)
		out = append(out, a)
	}
	return out, rows.Err()
}
