package store

import (
	"context"
	"database/sql"
	"encoding/json"

	"github.com/spruce/lexicode/internal/domain"
)

// ElicitationsRepo reads and writes the elicitations table: one blocking question or approval
// a run asked (story S21).
type ElicitationsRepo struct{ h handle }

// Elicitations returns the elicitations repository.
func (s *Store) Elicitations() *ElicitationsRepo { return &ElicitationsRepo{h: s.handle()} }

// Elicitations returns the elicitations repository bound to this transaction.
func (t *Tx) Elicitations() *ElicitationsRepo { return &ElicitationsRepo{h: t.handle()} }

const elicitationCols = `id, run_id, activity_seq, kind, request, state, response,
	responded_by, responded_at, created_at`

// Create inserts an elicitation.
func (r *ElicitationsRepo) Create(ctx context.Context, e *domain.Elicitation) error {
	_, err := r.h.w.ExecContext(ctx, `
		INSERT INTO elicitations (`+elicitationCols+`)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		e.ID, e.RunID, e.ActivitySeq, string(e.Kind), rawText(e.Request, "{}"),
		string(e.State), nullRawText(e.Response), nullStr(e.RespondedBy),
		nullStr(e.RespondedAt), e.CreatedAt)
	return mapErr(err)
}

// ByID returns the elicitation with this ID, or ErrNotFound.
func (r *ElicitationsRepo) ByID(ctx context.Context, id string) (domain.Elicitation, error) {
	return scanElicitation(r.h.r.QueryRowContext(ctx,
		`SELECT `+elicitationCols+` FROM elicitations WHERE id = ?`, id))
}

// PendingByRequest finds a run's pending elicitation with this exact kind and request JSON —
// the S21 idempotent re-ask: a restarted (or retried) tool call with an identical question
// reuses the open row instead of stacking a duplicate. Byte-identical request text is the
// identity; the MCP server canonicalizes the request before storing, so a retry of the same
// call matches. ErrNotFound when no such row is pending.
func (r *ElicitationsRepo) PendingByRequest(ctx context.Context, runID string, kind domain.ElicitationKind, request json.RawMessage) (domain.Elicitation, error) {
	return scanElicitation(r.h.r.QueryRowContext(ctx, `
		SELECT `+elicitationCols+` FROM elicitations
		WHERE run_id = ? AND kind = ? AND state = 'pending' AND request = ?
		ORDER BY created_at LIMIT 1`,
		runID, string(kind), rawText(request, "{}")))
}

// PendingForRun returns a run's open elicitations, oldest first.
func (r *ElicitationsRepo) PendingForRun(ctx context.Context, runID string) ([]domain.Elicitation, error) {
	rows, err := r.h.r.QueryContext(ctx, `
		SELECT `+elicitationCols+` FROM elicitations
		WHERE run_id = ? AND state = 'pending' ORDER BY created_at`, runID)
	if err != nil {
		return nil, mapErr(err)
	}
	defer func() { _ = rows.Close() }()

	var out []domain.Elicitation
	for rows.Next() {
		e, err := scanElicitation(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// ForRun returns every elicitation of a run, oldest first — the run detail's respond
// surface correlates them to timeline rows by activity_seq (S24).
func (r *ElicitationsRepo) ForRun(ctx context.Context, runID string) ([]domain.Elicitation, error) {
	rows, err := r.h.r.QueryContext(ctx, `
		SELECT `+elicitationCols+` FROM elicitations
		WHERE run_id = ? ORDER BY created_at, id`, runID)
	if err != nil {
		return nil, mapErr(err)
	}
	defer func() { _ = rows.Close() }()

	var out []domain.Elicitation
	for rows.Next() {
		e, err := scanElicitation(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// PendingOlderThan returns every pending elicitation created at or before cutoff (RFC3339),
// oldest first — the S24 escalation ticker's scan (interaction rule 11: an unanswered
// question escalates to the inbox).
func (r *ElicitationsRepo) PendingOlderThan(ctx context.Context, cutoff string) ([]domain.Elicitation, error) {
	rows, err := r.h.r.QueryContext(ctx, `
		SELECT `+elicitationCols+` FROM elicitations
		WHERE state = 'pending' AND created_at <= ? ORDER BY created_at, id`, cutoff)
	if err != nil {
		return nil, mapErr(err)
	}
	defer func() { _ = rows.Close() }()

	var out []domain.Elicitation
	for rows.Next() {
		e, err := scanElicitation(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// Respond moves one pending elicitation to a resolved state with its response and responder
// attribution, guarded on state = 'pending' so two concurrent responders cannot both win:
// the loser gets ErrNotFound and must re-read the row to see what happened.
func (r *ElicitationsRepo) Respond(ctx context.Context, id string, state domain.ElicitationState, response json.RawMessage, respondedBy *string, respondedAt string) error {
	res, err := r.h.w.ExecContext(ctx, `
		UPDATE elicitations SET state = ?, response = ?, responded_by = ?, responded_at = ?
		WHERE id = ? AND state = 'pending'`,
		string(state), nullRawText(response), nullStr(respondedBy), respondedAt, id)
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

func scanElicitation(row rowScanner) (domain.Elicitation, error) {
	var (
		e                        domain.Elicitation
		kind, state, request     string
		response                 sql.NullString
		respondedBy, respondedAt sql.NullString
	)
	err := row.Scan(&e.ID, &e.RunID, &e.ActivitySeq, &kind, &request, &state,
		&response, &respondedBy, &respondedAt, &e.CreatedAt)
	if err != nil {
		return domain.Elicitation{}, mapErr(err)
	}
	e.Kind = domain.ElicitationKind(kind)
	e.State = domain.ElicitationState(state)
	e.Request = json.RawMessage(request)
	if response.Valid {
		e.Response = json.RawMessage(response.String)
	}
	e.RespondedBy = strPtr(respondedBy)
	e.RespondedAt = strPtr(respondedAt)
	return e, nil
}
