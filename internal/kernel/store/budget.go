package store

import (
	"context"
)

// BudgetRepo reads and writes budget_ledger — the per-day spend rollup that lets admission
// control answer "is the ceiling reached?" without scanning runs (data model §9). Rows are
// keyed (day, project, agent, trigger); the trigger dimension stays NULL until S27/S28 record
// rule-scoped spend.
type BudgetRepo struct{ h handle }

// Budget returns the budget ledger repository.
func (s *Store) Budget() *BudgetRepo { return &BudgetRepo{h: s.handle()} }

// Budget returns the repository bound to this transaction.
func (t *Tx) Budget() *BudgetRepo { return &BudgetRepo{h: t.handle()} }

// Add rolls cents into the (day, project, agent, trigger) cell, creating it on first touch.
// agentID and triggerID may be empty (stored NULL). The store has a single writer connection,
// so the read-then-write pair cannot interleave with another Add.
func (r *BudgetRepo) Add(ctx context.Context, day, projectID, agentID, triggerID string, cents int64) error {
	if cents == 0 {
		return nil
	}
	res, err := r.h.w.ExecContext(ctx, `
		UPDATE budget_ledger SET cents = cents + ?
		WHERE day = ? AND project_id = ? AND agent_id IS NULLIF(?, '') AND trigger_id IS NULLIF(?, '')`,
		cents, day, projectID, agentID, triggerID)
	if err != nil {
		return mapErr(err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return mapErr(err)
	}
	if n > 0 {
		return nil
	}
	_, err = r.h.w.ExecContext(ctx, `
		INSERT INTO budget_ledger (day, project_id, agent_id, trigger_id, cents)
		VALUES (?, ?, NULLIF(?, ''), NULLIF(?, ''), ?)`,
		day, projectID, agentID, triggerID, cents)
	return mapErr(err)
}

// ProjectDay returns a project's total recorded spend for one UTC day.
func (r *BudgetRepo) ProjectDay(ctx context.Context, day, projectID string) (int64, error) {
	var cents int64
	err := r.h.r.QueryRowContext(ctx, `
		SELECT COALESCE(SUM(cents), 0) FROM budget_ledger WHERE day = ? AND project_id = ?`,
		day, projectID).Scan(&cents)
	return cents, mapErr(err)
}

// AgentDay returns one agent's total recorded spend for one UTC day.
func (r *BudgetRepo) AgentDay(ctx context.Context, day, agentID string) (int64, error) {
	var cents int64
	err := r.h.r.QueryRowContext(ctx, `
		SELECT COALESCE(SUM(cents), 0) FROM budget_ledger WHERE day = ? AND agent_id = ?`,
		day, agentID).Scan(&cents)
	return cents, mapErr(err)
}

// TriggerDay returns one trigger's total recorded spend for one UTC day — the loop guard's
// rule/day budget scope (S27; loop_config.daily_budget_cents).
func (r *BudgetRepo) TriggerDay(ctx context.Context, day, triggerID string) (int64, error) {
	var cents int64
	err := r.h.r.QueryRowContext(ctx, `
		SELECT COALESCE(SUM(cents), 0) FROM budget_ledger WHERE day = ? AND trigger_id = ?`,
		day, triggerID).Scan(&cents)
	return cents, mapErr(err)
}
