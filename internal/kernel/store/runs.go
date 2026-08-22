package store

import (
	"context"
	"database/sql"

	"github.com/spruce/lexicode/internal/domain"
)

// RunsRepo reads and writes the runs table. Note that after launch, runs.state belongs to the
// scheduler alone (data model §10.4) — this repository deliberately has no UpdateState; that
// method arrives unexported inside kernel/sched (S16).
type RunsRepo struct{ h handle }

// Runs returns the runs repository.
func (s *Store) Runs() *RunsRepo { return &RunsRepo{h: s.handle()} }

// Runs returns the runs repository bound to this transaction.
func (t *Tx) Runs() *RunsRepo { return &RunsRepo{h: t.handle()} }

const runCols = `id, seq, project_id, agent_id, ticket_id, trigger_id, cause_event_id,
	parent_run_id, requested_by_user_id, state, state_reason, hold_reason, autonomy,
	directive_version_id, model, effort, prompt, runtime_id, sandbox_id,
	container_id, instance_id, log_offset, branch, base_sha, depth, subject_key, current_step,
	cost_cents, tokens_in, tokens_out, tokens_cache_read, tokens_cache_write, step_count,
	error_message, takeover_note, queued_at, started_at, ended_at, acknowledged_at`

// NextSeq returns the next per-project run number ("run #482"). Call it inside the Tx that
// inserts the run, so two concurrent launches cannot mint the same number.
func (r *RunsRepo) NextSeq(ctx context.Context, projectID string) (int64, error) {
	var seq int64
	err := r.h.w.QueryRowContext(ctx,
		`SELECT COALESCE(MAX(seq), 0) + 1 FROM runs WHERE project_id = ?`, projectID).Scan(&seq)
	return seq, mapErr(err)
}

// Create inserts a run.
func (r *RunsRepo) Create(ctx context.Context, run *domain.Run) error {
	_, err := r.h.w.ExecContext(ctx, `
		INSERT INTO runs (`+runCols+`)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?,
			?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		run.ID, run.Seq, run.ProjectID, run.AgentID, nullStr(run.TicketID),
		nullStr(run.TriggerID), nullStr(run.CauseEventID), nullStr(run.ParentRunID),
		nullStr(run.RequestedByUserID), string(run.State), run.StateReason, run.HoldReason,
		string(run.Autonomy), nullStr(run.DirectiveVersionID), run.Model, run.Effort,
		run.Prompt, run.RuntimeID, run.SandboxID,
		nullStr(run.ContainerID), nullStr(run.InstanceID), run.LogOffset,
		nullStr(run.Branch), nullStr(run.BaseSHA), run.Depth, run.SubjectKey, run.CurrentStep,
		run.CostCents, run.TokensIn, run.TokensOut, run.TokensCacheRead, run.TokensCacheWrite,
		run.StepCount, run.ErrorMessage, run.TakeoverNote,
		run.QueuedAt, nullStr(run.StartedAt), nullStr(run.EndedAt), nullStr(run.AcknowledgedAt))
	return mapErr(err)
}

// ByID returns the run with this ID, or ErrNotFound.
func (r *RunsRepo) ByID(ctx context.Context, id string) (domain.Run, error) {
	return scanRun(r.h.r.QueryRowContext(ctx,
		`SELECT `+runCols+` FROM runs WHERE id = ?`, id))
}

// BranchInUse reports whether any of the project's runs already claimed this branch name —
// the S19 collision check behind the deterministic `-2`, `-3` suffixes. Terminal runs count
// too: their branches may still exist on the remote.
func (r *RunsRepo) BranchInUse(ctx context.Context, projectID, branch string) (bool, error) {
	var n int64
	err := r.h.r.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM runs WHERE project_id = ? AND branch = ?`, projectID, branch).
		Scan(&n)
	return n > 0, mapErr(err)
}

// LatestForAgentOnBranch returns the agent's most recent run on this branch, preferring a
// run that is still alive (non-terminal) over an ended one — the D-9 attribution fallback
// (architecture §6.3): when a marker gives no run id, an external event caused by the agent is
// pinned to "the agent's most recent run touching that subject". ErrNotFound when the agent
// never ran on the branch.
func (r *RunsRepo) LatestForAgentOnBranch(ctx context.Context, projectID, agentID, branch string) (domain.Run, error) {
	return scanRun(r.h.r.QueryRowContext(ctx, `
		SELECT `+runCols+` FROM runs
		WHERE project_id = ? AND agent_id = ? AND branch = ?
		ORDER BY CASE WHEN state IN ('completed','failed','timed_out','canceled','loop_stopped')
			THEN 1 ELSE 0 END, queued_at DESC, seq DESC
		LIMIT 1`, projectID, agentID, branch))
}

// ForProject returns a project's runs, newest first.
func (r *RunsRepo) ForProject(ctx context.Context, projectID string) ([]domain.Run, error) {
	rows, err := r.h.r.QueryContext(ctx,
		`SELECT `+runCols+` FROM runs WHERE project_id = ? ORDER BY seq DESC`, projectID)
	if err != nil {
		return nil, mapErr(err)
	}
	defer func() { _ = rows.Close() }()
	return collectRuns(rows)
}

// ForTicket returns a ticket's runs, newest first.
func (r *RunsRepo) ForTicket(ctx context.Context, ticketID string) ([]domain.Run, error) {
	rows, err := r.h.r.QueryContext(ctx,
		`SELECT `+runCols+` FROM runs WHERE ticket_id = ? ORDER BY queued_at DESC, seq DESC`, ticketID)
	if err != nil {
		return nil, mapErr(err)
	}
	defer func() { _ = rows.Close() }()
	return collectRuns(rows)
}

func collectRuns(rows *sql.Rows) ([]domain.Run, error) {
	var out []domain.Run
	for rows.Next() {
		run, err := scanRun(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, run)
	}
	return out, rows.Err()
}

func scanRun(row rowScanner) (domain.Run, error) {
	var (
		run                                  domain.Run
		state, autonomy                      string
		ticketID, triggerID, causeEventID    sql.NullString
		parentRunID, requestedBy, directive  sql.NullString
		containerID, instanceID, branch, sha sql.NullString
		startedAt, endedAt, acknowledgedAt   sql.NullString
	)
	err := row.Scan(&run.ID, &run.Seq, &run.ProjectID, &run.AgentID, &ticketID, &triggerID,
		&causeEventID, &parentRunID, &requestedBy, &state, &run.StateReason, &run.HoldReason,
		&autonomy, &directive, &run.Model, &run.Effort, &run.Prompt, &run.RuntimeID,
		&run.SandboxID, &containerID, &instanceID, &run.LogOffset, &branch, &sha,
		&run.Depth, &run.SubjectKey, &run.CurrentStep,
		&run.CostCents, &run.TokensIn, &run.TokensOut, &run.TokensCacheRead,
		&run.TokensCacheWrite, &run.StepCount, &run.ErrorMessage, &run.TakeoverNote,
		&run.QueuedAt, &startedAt, &endedAt, &acknowledgedAt)
	if err != nil {
		return domain.Run{}, mapErr(err)
	}
	run.State = domain.RunState(state)
	run.Autonomy = domain.Autonomy(autonomy)
	run.TicketID = strPtr(ticketID)
	run.TriggerID = strPtr(triggerID)
	run.CauseEventID = strPtr(causeEventID)
	run.ParentRunID = strPtr(parentRunID)
	run.RequestedByUserID = strPtr(requestedBy)
	run.DirectiveVersionID = strPtr(directive)
	run.ContainerID = strPtr(containerID)
	run.InstanceID = strPtr(instanceID)
	run.Branch = strPtr(branch)
	run.BaseSHA = strPtr(sha)
	run.StartedAt = strPtr(startedAt)
	run.EndedAt = strPtr(endedAt)
	run.AcknowledgedAt = strPtr(acknowledgedAt)
	return run, nil
}

// SetCurrentStep updates the mutable one-liner (runs.current_step) the `set_step` MCP tool
// writes. current_step is presentation, not lifecycle: it is deliberately outside the
// "only the scheduler writes runs.state" rule. ErrNotFound for an unknown run.
func (r *RunsRepo) SetCurrentStep(ctx context.Context, id, step string) error {
	res, err := r.h.w.ExecContext(ctx,
		`UPDATE runs SET current_step = ? WHERE id = ?`, step, id)
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

// AgentRunStats is the roster-card aggregate for one agent, computed from the runs table.
// "Since" fields count runs queued at or after the caller's window start (the roster uses a
// rolling seven days); Ended/Succeeded cover the agent's whole history, so the success rate
// stays meaningful in a quiet week. All zeros until runs exist (S22).
type AgentRunStats struct {
	RunsSince       int64 // runs queued in the window
	SpendCentsSince int64 // cost of runs queued in the window
	Ended           int64 // all-time runs in a terminal state
	Succeeded       int64 // all-time runs that completed
}

// terminal run states, as the schema CHECK spells them.
const terminalStates = `('completed','failed','timed_out','canceled','loop_stopped')`

// StatsForProjectAgents aggregates per-agent run stats for a whole project in one query —
// the roster renders N cards without N queries. Agents with no runs are absent from the map.
func (r *RunsRepo) StatsForProjectAgents(ctx context.Context, projectID, since string) (map[string]AgentRunStats, error) {
	rows, err := r.h.r.QueryContext(ctx, `
		SELECT agent_id,
			SUM(CASE WHEN queued_at >= ? THEN 1 ELSE 0 END),
			SUM(CASE WHEN queued_at >= ? THEN cost_cents ELSE 0 END),
			SUM(CASE WHEN state IN `+terminalStates+` THEN 1 ELSE 0 END),
			SUM(CASE WHEN state = 'completed' THEN 1 ELSE 0 END)
		FROM runs WHERE project_id = ? GROUP BY agent_id`, since, since, projectID)
	if err != nil {
		return nil, mapErr(err)
	}
	defer func() { _ = rows.Close() }()

	out := map[string]AgentRunStats{}
	for rows.Next() {
		var id string
		var s AgentRunStats
		if err := rows.Scan(&id, &s.RunsSince, &s.SpendCentsSince, &s.Ended, &s.Succeeded); err != nil {
			return nil, mapErr(err)
		}
		out[id] = s
	}
	return out, rows.Err()
}

// LatestOnSubject returns the most recent run for (trigger, subject) queued at or after
// cutoff — the loop guard's debounce probe (S27). It is a database query, not an in-memory
// timer, so the window survives restarts. loop_stopped rows are excluded: they are guard
// artifacts, not started work, and must not absorb a later firing.
func (r *RunsRepo) LatestOnSubject(ctx context.Context, triggerID, subjectKey, cutoffQueuedAt string) (domain.Run, error) {
	return scanRun(r.h.r.QueryRowContext(ctx, `
		SELECT `+runCols+` FROM runs
		WHERE trigger_id = ? AND subject_key = ? AND queued_at >= ? AND state != 'loop_stopped'
		ORDER BY queued_at DESC, seq DESC LIMIT 1`, triggerID, subjectKey, cutoffQueuedAt))
}

// ActiveOnSubject returns the newest non-terminal run for (trigger, subject) — the loop
// guard's cancel-in-progress probe (S27). ErrNotFound when nothing is live.
func (r *RunsRepo) ActiveOnSubject(ctx context.Context, triggerID, subjectKey string) (domain.Run, error) {
	return scanRun(r.h.r.QueryRowContext(ctx, `
		SELECT `+runCols+` FROM runs
		WHERE trigger_id = ? AND subject_key = ? AND state NOT IN `+terminalStates+`
		ORDER BY queued_at DESC, seq DESC LIMIT 1`, triggerID, subjectKey))
}

// ByCauseEvent returns the runs an event spawned (runs.cause_event_id — the downward half of
// the causal chain), oldest first.
func (r *RunsRepo) ByCauseEvent(ctx context.Context, eventID string) ([]domain.Run, error) {
	rows, err := r.h.r.QueryContext(ctx, `
		SELECT `+runCols+` FROM runs
		WHERE cause_event_id = ? ORDER BY queued_at, seq`, eventID)
	if err != nil {
		return nil, mapErr(err)
	}
	defer func() { _ = rows.Close() }()
	return collectRuns(rows)
}
