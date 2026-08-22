package store

import (
	"context"
	"database/sql"
	"encoding/json"

	"github.com/spruce/lexicode/internal/domain"
)

// FiringsRepo reads and writes trigger_firings — one row per terminal outcome of the trigger
// pipeline, including the outcomes that did nothing (architecture §8). The
// UNIQUE(trigger_id, event_id) index is the engine's idempotency against bus re-dispatch;
// Create leans on it rather than racing a lookup.
type FiringsRepo struct{ h handle }

// Firings returns the trigger-firings repository.
func (s *Store) Firings() *FiringsRepo { return &FiringsRepo{h: s.handle()} }

// Firings returns the trigger-firings repository bound to this transaction.
func (t *Tx) Firings() *FiringsRepo { return &FiringsRepo{h: t.handle()} }

const firingCols = `id, trigger_id, event_id, outcome, reason, run_id, absorbed_by_run_id,
	warnings, created_at`

// Create inserts one firing, idempotent on (trigger_id, event_id): a pair already recorded
// inserts nothing and returns false — the re-dispatch path, not an error.
func (r *FiringsRepo) Create(ctx context.Context, f *domain.TriggerFiring) (bool, error) {
	res, err := r.h.w.ExecContext(ctx, `
		INSERT INTO trigger_firings (`+firingCols+`)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT (trigger_id, event_id) DO NOTHING`,
		f.ID, f.TriggerID, f.EventID, string(f.Outcome), f.Reason,
		nullStr(f.RunID), nullStr(f.AbsorbedByRunID),
		rawText(f.Warnings, "[]"), f.CreatedAt)
	if err != nil {
		return false, mapErr(err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, mapErr(err)
	}
	return n > 0, nil
}

// Exists reports whether this (trigger, event) pair already has a firing — the engine's
// pre-flight check so a re-dispatched event never re-executes actions.
func (r *FiringsRepo) Exists(ctx context.Context, triggerID, eventID string) (bool, error) {
	var one int64
	err := r.h.r.QueryRowContext(ctx,
		`SELECT 1 FROM trigger_firings WHERE trigger_id = ? AND event_id = ?`,
		triggerID, eventID).Scan(&one)
	if err == sql.ErrNoRows {
		return false, nil
	}
	if err != nil {
		return false, mapErr(err)
	}
	return true, nil
}

// ForTrigger returns the trigger's firings, newest first, at most limit rows (<= 0 means 50).
func (r *FiringsRepo) ForTrigger(ctx context.Context, triggerID string, limit int) ([]domain.TriggerFiring, error) {
	if limit <= 0 {
		limit = 50
	}
	rows, err := r.h.r.QueryContext(ctx,
		`SELECT `+firingCols+` FROM trigger_firings
		 WHERE trigger_id = ? ORDER BY created_at DESC, id DESC LIMIT ?`,
		triggerID, limit)
	if err != nil {
		return nil, mapErr(err)
	}
	defer func() { _ = rows.Close() }()

	var out []domain.TriggerFiring
	for rows.Next() {
		f, err := scanFiring(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, f)
	}
	return out, rows.Err()
}

// Health is the rule-health aggregate (data model §6): per-outcome counts over the trigger's
// last N firings plus the newest firing's timestamp — one GROUP BY, never collapsed to
// success/failure.
type Health struct {
	Counts      map[domain.FiringOutcome]int64
	LastFiredAt string // "" when the trigger has never fired
}

// HealthFor computes Health over the last lastN firings (<= 0 means 50).
func (r *FiringsRepo) HealthFor(ctx context.Context, triggerID string, lastN int) (Health, error) {
	if lastN <= 0 {
		lastN = 50
	}
	h := Health{Counts: map[domain.FiringOutcome]int64{}}
	rows, err := r.h.r.QueryContext(ctx, `
		SELECT outcome, COUNT(*), MAX(created_at) FROM (
			SELECT outcome, created_at FROM trigger_firings
			WHERE trigger_id = ? ORDER BY created_at DESC, id DESC LIMIT ?
		) GROUP BY outcome`,
		triggerID, lastN)
	if err != nil {
		return Health{}, mapErr(err)
	}
	defer func() { _ = rows.Close() }()

	for rows.Next() {
		var outcome string
		var count int64
		var last sql.NullString
		if err := rows.Scan(&outcome, &count, &last); err != nil {
			return Health{}, mapErr(err)
		}
		h.Counts[domain.FiringOutcome(outcome)] = count
		if last.Valid && last.String > h.LastFiredAt {
			h.LastFiredAt = last.String
		}
	}
	return h, rows.Err()
}

func scanFiring(row rowScanner) (domain.TriggerFiring, error) {
	var (
		f             domain.TriggerFiring
		outcome       string
		runID, absorb sql.NullString
		warnings      string
	)
	err := row.Scan(&f.ID, &f.TriggerID, &f.EventID, &outcome, &f.Reason,
		&runID, &absorb, &warnings, &f.CreatedAt)
	if err != nil {
		return domain.TriggerFiring{}, mapErr(err)
	}
	f.Outcome = domain.FiringOutcome(outcome)
	f.RunID = strPtr(runID)
	f.AbsorbedByRunID = strPtr(absorb)
	f.Warnings = json.RawMessage(warnings)
	return f, nil
}

// Supersede retroactively marks the firing that started runID — under this trigger — as
// `superseded`, linking it to the run that replaced it. This is the one place a firing's
// outcome is ever rewritten: cancel-in-progress (architecture §9 layer 3) cancels the
// previous run after the new one exists, and the previous firing's "succeeded" would
// otherwise claim work that was thrown away. The rule-health breakdown's `superseded` class
// exists exactly for these rows.
func (r *FiringsRepo) Supersede(ctx context.Context, triggerID, runID, absorbedByRunID, reason string) error {
	_, err := r.h.w.ExecContext(ctx, `
		UPDATE trigger_firings
		SET outcome = 'superseded', reason = ?, absorbed_by_run_id = ?
		WHERE trigger_id = ? AND run_id = ? AND outcome != 'superseded'`,
		reason, absorbedByRunID, triggerID, runID)
	return mapErr(err)
}
