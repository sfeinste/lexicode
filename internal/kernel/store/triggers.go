package store

import (
	"context"
	"database/sql"
	"encoding/json"

	"github.com/spruce/lexicode/internal/domain"
)

// TriggersRepo reads and writes the triggers table. S15 needs creation (the two suggested
// rules, disabled) and the per-project listing; the trigger engine story grows this.
type TriggersRepo struct{ h handle }

// Triggers returns the triggers repository.
func (s *Store) Triggers() *TriggersRepo { return &TriggersRepo{h: s.handle()} }

// Triggers returns the triggers repository bound to this transaction.
func (t *Tx) Triggers() *TriggersRepo { return &TriggersRepo{h: t.handle()} }

const triggerCols = `id, project_id, name, enabled, source_id, event, activity_types,
	filters, conditions, actions, loop_config, cron, created_by, created_at, updated_at`

// Create inserts a trigger row.
func (r *TriggersRepo) Create(ctx context.Context, tr *domain.Trigger) error {
	_, err := r.h.w.ExecContext(ctx, `
		INSERT INTO triggers (`+triggerCols+`)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		tr.ID, tr.ProjectID, tr.Name, boolInt(tr.Enabled), tr.SourceID, tr.Event,
		rawText(tr.ActivityTypes, "[]"), rawText(tr.Filters, "{}"),
		rawText(tr.Conditions, `{"all":[]}`), rawText(tr.Actions, "[]"),
		rawText(tr.LoopConfig, "{}"), nullStr(tr.Cron), nullStr(tr.CreatedBy),
		tr.CreatedAt, tr.UpdatedAt)
	return mapErr(err)
}

// ForProject returns the project's triggers, oldest first.
func (r *TriggersRepo) ForProject(ctx context.Context, projectID string) ([]domain.Trigger, error) {
	rows, err := r.h.r.QueryContext(ctx,
		`SELECT `+triggerCols+` FROM triggers WHERE project_id = ? ORDER BY created_at, id`,
		projectID)
	if err != nil {
		return nil, mapErr(err)
	}
	defer func() { _ = rows.Close() }()

	var out []domain.Trigger
	for rows.Next() {
		tr, err := scanTrigger(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, tr)
	}
	return out, rows.Err()
}

// ByID returns the trigger with this ID, or ErrNotFound.
func (r *TriggersRepo) ByID(ctx context.Context, id string) (domain.Trigger, error) {
	return scanTrigger(r.h.r.QueryRowContext(ctx,
		`SELECT `+triggerCols+` FROM triggers WHERE id = ?`, id))
}

// Enabled returns the project's enabled triggers, oldest first — what the engine evaluates
// against each event. Disabled triggers are invisible here by design: they write no firing
// rows at all.
func (r *TriggersRepo) Enabled(ctx context.Context, projectID string) ([]domain.Trigger, error) {
	rows, err := r.h.r.QueryContext(ctx,
		`SELECT `+triggerCols+` FROM triggers
		 WHERE project_id = ? AND enabled = 1 ORDER BY created_at, id`,
		projectID)
	if err != nil {
		return nil, mapErr(err)
	}
	defer func() { _ = rows.Close() }()

	var out []domain.Trigger
	for rows.Next() {
		tr, err := scanTrigger(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, tr)
	}
	return out, rows.Err()
}

// Update rewrites every mutable column of the trigger row. ErrNotFound when the ID matches
// nothing.
func (r *TriggersRepo) Update(ctx context.Context, tr *domain.Trigger) error {
	res, err := r.h.w.ExecContext(ctx, `
		UPDATE triggers SET name = ?, enabled = ?, source_id = ?, event = ?,
			activity_types = ?, filters = ?, conditions = ?, actions = ?,
			loop_config = ?, cron = ?, updated_at = ?
		WHERE id = ?`,
		tr.Name, boolInt(tr.Enabled), tr.SourceID, tr.Event,
		rawText(tr.ActivityTypes, "[]"), rawText(tr.Filters, "{}"),
		rawText(tr.Conditions, `{"all":[]}`), rawText(tr.Actions, "[]"),
		rawText(tr.LoopConfig, "{}"), nullStr(tr.Cron), tr.UpdatedAt, tr.ID)
	if err != nil {
		return mapErr(err)
	}
	return errIfNone(res)
}

// Delete removes the trigger and its firing history. Call it on a Tx so the two deletes are
// atomic. A trigger still
// referenced elsewhere (a ticket's provenance, a run, a budget row) surfaces as
// ErrForeignKey — the caller renders that as "disable it instead of deleting it".
func (r *TriggersRepo) Delete(ctx context.Context, id string) error {
	if _, err := r.h.w.ExecContext(ctx,
		`DELETE FROM trigger_firings WHERE trigger_id = ?`, id); err != nil {
		return mapErr(err)
	}
	res, err := r.h.w.ExecContext(ctx, `DELETE FROM triggers WHERE id = ?`, id)
	if err != nil {
		return mapErr(err)
	}
	return errIfNone(res)
}

func scanTrigger(row rowScanner) (domain.Trigger, error) {
	var (
		tr                                     domain.Trigger
		enabled                                int64
		activity, filters, conditions, actions string
		loopCfg                                string
		cron, createdBy                        sql.NullString
	)
	err := row.Scan(&tr.ID, &tr.ProjectID, &tr.Name, &enabled, &tr.SourceID, &tr.Event,
		&activity, &filters, &conditions, &actions, &loopCfg, &cron, &createdBy,
		&tr.CreatedAt, &tr.UpdatedAt)
	if err != nil {
		return domain.Trigger{}, mapErr(err)
	}
	tr.Enabled = enabled != 0
	tr.ActivityTypes = json.RawMessage(activity)
	tr.Filters = json.RawMessage(filters)
	tr.Conditions = json.RawMessage(conditions)
	tr.Actions = json.RawMessage(actions)
	tr.LoopConfig = json.RawMessage(loopCfg)
	tr.Cron = strPtr(cron)
	tr.CreatedBy = strPtr(createdBy)
	return tr, nil
}
