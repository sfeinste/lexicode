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
