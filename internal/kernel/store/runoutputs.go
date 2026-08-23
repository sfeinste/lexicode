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

// OpenAgentPRs returns the `pull_request` outputs of completed runs that are still awaiting
// a human's review — architecture §12's "outputs awaiting review" include open agent PRs.
// The PR's open/merged/closed state lives in poll_pr_state (the poller's per-PR snapshot,
// keyed (project_id, number); run_outputs.ref is the PR number the forge adapter recorded).
// The LEFT JOIN keeps a PR whose state row is missing: when no poller is running there is no
// snapshot, so a completed run's PR output stays a review row until closed/merged events
// arrive and the snapshot says otherwise. projectID "" means every project.
func (r *RunOutputsRepo) OpenAgentPRs(ctx context.Context, projectID string) ([]domain.RunOutput, error) {
	rows, err := r.h.r.QueryContext(ctx, `
		SELECT ro.id, ro.run_id, ro.kind, ro.ref, ro.url, ro.summary, ro.created_at
		FROM run_outputs ro
		JOIN runs ON runs.id = ro.run_id
		LEFT JOIN poll_pr_state pps
			ON pps.project_id = runs.project_id AND pps.number = CAST(ro.ref AS INTEGER)
		WHERE ro.kind = 'pull_request'
			AND runs.state = 'completed'
			AND (pps.number IS NULL OR pps.state = 'open')
			AND (? = '' OR runs.project_id = ?)
		ORDER BY ro.created_at, ro.id`, projectID, projectID)
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

// PRTicket is what TicketForPR found by walking a pull request back to the work it came from:
// the ticket, and the run that opened the pull request while working on it.
type PRTicket struct {
	TicketID string
	RunID    string
	RunSeq   int64 // renders as "run #482"
}

// TicketForPR resolves the ticket a pull request belongs to, through the artifact trail:
// the `pull_request` output whose ref is this number, the run that recorded it, and that run's
// ticket. It is the indirection a trigger-spawned run needs — such a run is free-floating by
// design (D-15), so the only thing that still knows which ticket its pull request serves is the
// run that opened it. LEXI-12 replaces this with a direct tickets.pr_number link.
//
// Only runs of projectID are considered: PR numbers are per-repository, so a number alone means
// nothing across projects. Runs that were themselves free-floating are skipped rather than
// ending the search — a re-run that opened a replacement PR without a ticket should not hide the
// ticket a later row still names. Oldest row first, so the run that actually opened the pull
// request wins over anything that recorded the same number afterwards.
//
// ErrNotFound means no run in this project is known to have opened that pull request, or every
// run that did was free-floating.
func (r *RunOutputsRepo) TicketForPR(ctx context.Context, projectID string, prNumber int) (PRTicket, error) {
	var out PRTicket
	err := r.h.r.QueryRowContext(ctx, `
		SELECT runs.ticket_id, runs.id, runs.seq
		FROM run_outputs ro
		JOIN runs ON runs.id = ro.run_id
		WHERE ro.kind = 'pull_request'
			AND runs.project_id = ?
			AND CAST(ro.ref AS INTEGER) = ?
			AND runs.ticket_id IS NOT NULL
		ORDER BY ro.created_at, ro.id
		LIMIT 1`, projectID, prNumber).Scan(&out.TicketID, &out.RunID, &out.RunSeq)
	if err != nil {
		return PRTicket{}, mapErr(err)
	}
	return out, nil
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
