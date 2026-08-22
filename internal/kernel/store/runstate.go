package store

import (
	"context"
	"strings"

	"github.com/spruce/lexicode/internal/domain"
)

// This file is the scheduler's write surface on the runs table. Only kernel/sched may call
// these methods (data model §10, invariant 4: only the scheduler writes runs.state) — the rule
// a compiler cannot enforce across packages is enforced by kernel/sched's statewriter lint
// test, which fails on any other caller of SetState.

// RunStateUpdate carries the columns one state transition may touch alongside runs.state.
// Nil fields are left untouched.
type RunStateUpdate struct {
	StateReason  *string
	HoldReason   *string
	ErrorMessage *string
	StartedAt    *string
	EndedAt      *string
}

// SetState moves a run from one of the expected states to the target state, applying u in the
// same UPDATE. It returns false (and no error) when the run was not in any expected state —
// the caller lost a transition race and must re-read the row. An empty `from` means "any
// state", which only the crash reconciler may use.
func (r *RunsRepo) SetState(ctx context.Context, id string, from []domain.RunState, to domain.RunState, u RunStateUpdate) (bool, error) {
	set := []string{"state = ?"}
	args := []any{string(to)}
	if u.StateReason != nil {
		set = append(set, "state_reason = ?")
		args = append(args, *u.StateReason)
	}
	if u.HoldReason != nil {
		set = append(set, "hold_reason = ?")
		args = append(args, *u.HoldReason)
	}
	if u.ErrorMessage != nil {
		set = append(set, "error_message = ?")
		args = append(args, *u.ErrorMessage)
	}
	if u.StartedAt != nil {
		set = append(set, "started_at = ?")
		args = append(args, *u.StartedAt)
	}
	if u.EndedAt != nil {
		set = append(set, "ended_at = ?")
		args = append(args, *u.EndedAt)
	}
	q := "UPDATE runs SET " + strings.Join(set, ", ") + " WHERE id = ?"
	args = append(args, id)
	if len(from) > 0 {
		marks := make([]string, len(from))
		for i, s := range from {
			marks[i] = "?"
			args = append(args, string(s))
		}
		q += " AND state IN (" + strings.Join(marks, ",") + ")"
	}
	res, err := r.h.w.ExecContext(ctx, q, args...)
	if err != nil {
		return false, mapErr(err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, mapErr(err)
	}
	return n > 0, nil
}

// SetHoldReason updates why a queued run is still queued, in words (§10.2 — never a bare
// spinner). It touches only queued runs; a run that left `queued` between the admission pass's
// read and this write is left alone.
func (r *RunsRepo) SetHoldReason(ctx context.Context, id, reason string) error {
	_, err := r.h.w.ExecContext(ctx,
		`UPDATE runs SET hold_reason = ? WHERE id = ? AND state = 'queued'`, reason, id)
	return mapErr(err)
}

// SetProvisioned records what provisioning produced: the sandbox instance, the run branch and
// (when known) the base SHA. Called once, between Prepare and the provisioning→running
// transition.
func (r *RunsRepo) SetProvisioned(ctx context.Context, id string, instanceID, containerID, branch, baseSHA string) error {
	res, err := r.h.w.ExecContext(ctx, `
		UPDATE runs SET instance_id = ?, container_id = NULLIF(?, ''),
			branch = NULLIF(?, ''), base_sha = NULLIF(?, '')
		WHERE id = ?`, instanceID, containerID, branch, baseSHA, id)
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

// SetLogOffset persists how many bytes of the agent's stream have been fully consumed —
// the reattach cursor (§10.6). Monotonic: an offset smaller than the stored one is ignored,
// so a re-played stream can never rewind the cursor.
func (r *RunsRepo) SetLogOffset(ctx context.Context, id string, offset int64) error {
	_, err := r.h.w.ExecContext(ctx,
		`UPDATE runs SET log_offset = ? WHERE id = ? AND log_offset < ?`, offset, id, offset)
	return mapErr(err)
}

// AddUsage rolls one usage delta into the run's token/cost columns.
func (r *RunsRepo) AddUsage(ctx context.Context, id string, d domain.UsageDelta) error {
	_, err := r.h.w.ExecContext(ctx, `
		UPDATE runs SET
			cost_cents = cost_cents + ?,
			tokens_in = tokens_in + ?, tokens_out = tokens_out + ?,
			tokens_cache_read = tokens_cache_read + ?, tokens_cache_write = tokens_cache_write + ?
		WHERE id = ?`,
		d.CostCents, d.TokensIn, d.TokensOut, d.TokensCacheRead, d.TokensCacheWrite, id)
	return mapErr(err)
}

// SetStepCount records how many action steps the run has taken (the step-cap counter and the
// "Failed after N steps" number).
func (r *RunsRepo) SetStepCount(ctx context.Context, id string, n int64) error {
	_, err := r.h.w.ExecContext(ctx,
		`UPDATE runs SET step_count = ? WHERE id = ?`, n, id)
	return mapErr(err)
}

// activeRunStates are the states that hold (or are about to hold) a container: everything
// between admission and terminal, `queued` excluded.
const activeRunStates = `('provisioning','running','needs_input','awaiting_approval')`

// ByStates returns every run in any of the given states, across all projects, oldest queue
// entry first — the admission pass's and the crash reconciler's read.
func (r *RunsRepo) ByStates(ctx context.Context, states ...domain.RunState) ([]domain.Run, error) {
	if len(states) == 0 {
		return nil, nil
	}
	marks := make([]string, len(states))
	args := make([]any, len(states))
	for i, s := range states {
		marks[i] = "?"
		args[i] = string(s)
	}
	rows, err := r.h.r.QueryContext(ctx,
		`SELECT `+runCols+` FROM runs WHERE state IN (`+strings.Join(marks, ",")+`)
		 ORDER BY queued_at, seq`, args...)
	if err != nil {
		return nil, mapErr(err)
	}
	defer func() { _ = rows.Close() }()
	return collectRuns(rows)
}

// ActiveCountByAgent returns, per agent, how many runs currently hold a container slot
// (provisioning | running | needs_input | awaiting_approval), across all projects.
func (r *RunsRepo) ActiveCountByAgent(ctx context.Context) (map[string]int64, error) {
	rows, err := r.h.r.QueryContext(ctx,
		`SELECT agent_id, COUNT(*) FROM runs WHERE state IN `+activeRunStates+` GROUP BY agent_id`)
	if err != nil {
		return nil, mapErr(err)
	}
	defer func() { _ = rows.Close() }()
	out := map[string]int64{}
	for rows.Next() {
		var id string
		var n int64
		if err := rows.Scan(&id, &n); err != nil {
			return nil, mapErr(err)
		}
		out[id] = n
	}
	return out, rows.Err()
}

// ActiveCount returns how many runs hold a container slot, workspace-wide (§10.2 check 3).
func (r *RunsRepo) ActiveCount(ctx context.Context) (int64, error) {
	var n int64
	err := r.h.r.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM runs WHERE state IN `+activeRunStates).Scan(&n)
	return n, mapErr(err)
}

// ActiveForTicket returns a ticket's non-terminal runs, oldest first — the archive-time
// cancellation set (D-15).
func (r *RunsRepo) ActiveForTicket(ctx context.Context, ticketID string) ([]domain.Run, error) {
	rows, err := r.h.r.QueryContext(ctx, `
		SELECT `+runCols+` FROM runs
		WHERE ticket_id = ? AND state NOT IN `+terminalStates+`
		ORDER BY queued_at, seq`, ticketID)
	if err != nil {
		return nil, mapErr(err)
	}
	defer func() { _ = rows.Close() }()
	return collectRuns(rows)
}

// RunFilter narrows ForProjectFiltered. Zero fields mean "any".
type RunFilter struct {
	States   []domain.RunState
	AgentID  string
	TicketID string
}

// ForProjectFiltered returns a project's runs, newest first, narrowed by f.
func (r *RunsRepo) ForProjectFiltered(ctx context.Context, projectID string, f RunFilter) ([]domain.Run, error) {
	q := `SELECT ` + runCols + ` FROM runs WHERE project_id = ?`
	args := []any{projectID}
	if len(f.States) > 0 {
		marks := make([]string, len(f.States))
		for i, s := range f.States {
			marks[i] = "?"
			args = append(args, string(s))
		}
		q += ` AND state IN (` + strings.Join(marks, ",") + `)`
	}
	if f.AgentID != "" {
		q += ` AND agent_id = ?`
		args = append(args, f.AgentID)
	}
	if f.TicketID != "" {
		q += ` AND ticket_id = ?`
		args = append(args, f.TicketID)
	}
	q += ` ORDER BY seq DESC`
	rows, err := r.h.r.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, mapErr(err)
	}
	defer func() { _ = rows.Close() }()
	return collectRuns(rows)
}
