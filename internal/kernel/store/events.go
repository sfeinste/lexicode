package store

import (
	"context"
	"database/sql"
	"encoding/json"

	"github.com/spruce/lexicode/internal/domain"
)

// EventsRepo reads and writes the events table. The bus (S04) builds its idempotent Publish on
// Insert's ErrUnique from the dedupe_key.
type EventsRepo struct{ h handle }

// Events returns the events repository.
func (s *Store) Events() *EventsRepo { return &EventsRepo{h: s.handle()} }

// Events returns the events repository bound to this transaction.
func (t *Tx) Events() *EventsRepo { return &EventsRepo{h: t.handle()} }

const eventCols = `id, project_id, source, kind, activity_type,
	actor_kind, actor_id, actor_login, actor_email,
	subject_kind, subject_id, subject_number, subject_branch,
	payload, cause_run_id, dedupe_key, dispatch_state, occurred_at, created_at`

// Insert stores one event. A repeated dedupe_key surfaces as ErrUnique — the caller decides
// whether that is a drop (the bus) or a bug (anything else).
func (r *EventsRepo) Insert(ctx context.Context, e *domain.Event) error {
	_, err := r.h.w.ExecContext(ctx, `
		INSERT INTO events (`+eventCols+`)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		e.ID, nullStr(e.ProjectID), e.Source, e.Kind, e.ActivityType,
		string(e.ActorKind), nullStr(e.ActorID), nullStr(e.ActorLogin), nullStr(e.ActorEmail),
		e.SubjectKind, nullStr(e.SubjectID), nullInt(e.SubjectNumber), nullStr(e.SubjectBranch),
		rawText(e.Payload, "{}"), nullStr(e.CauseRunID), e.DedupeKey,
		string(e.DispatchState), e.OccurredAt, e.CreatedAt)
	return mapErr(err)
}

// ByID returns the event with this ID, or ErrNotFound.
func (r *EventsRepo) ByID(ctx context.Context, id string) (domain.Event, error) {
	return scanEvent(r.h.r.QueryRowContext(ctx,
		`SELECT `+eventCols+` FROM events WHERE id = ?`, id))
}

// ListPending returns every event still awaiting dispatch, oldest first — what boot recovery
// re-delivers (S04).
func (r *EventsRepo) ListPending(ctx context.Context) ([]domain.Event, error) {
	rows, err := r.h.r.QueryContext(ctx, `
		SELECT `+eventCols+` FROM events
		WHERE dispatch_state = 'pending' ORDER BY created_at, id`)
	if err != nil {
		return nil, mapErr(err)
	}
	defer func() { _ = rows.Close() }()

	var out []domain.Event
	for rows.Next() {
		e, err := scanEvent(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// SetDispatchState records the outcome of a dispatch.
func (r *EventsRepo) SetDispatchState(ctx context.Context, id string, state domain.DispatchState) error {
	res, err := r.h.w.ExecContext(ctx,
		`UPDATE events SET dispatch_state = ? WHERE id = ?`, string(state), id)
	if err != nil {
		return mapErr(err)
	}
	return errIfNone(res)
}

func scanEvent(row rowScanner) (domain.Event, error) {
	var (
		e                                    domain.Event
		actorKind, dispatch, payload         string
		projectID, actorID, login, email     sql.NullString
		subjectID, subjectBranch, causeRunID sql.NullString
		subjectNumber                        sql.NullInt64
	)
	err := row.Scan(&e.ID, &projectID, &e.Source, &e.Kind, &e.ActivityType,
		&actorKind, &actorID, &login, &email,
		&e.SubjectKind, &subjectID, &subjectNumber, &subjectBranch,
		&payload, &causeRunID, &e.DedupeKey, &dispatch, &e.OccurredAt, &e.CreatedAt)
	if err != nil {
		return domain.Event{}, mapErr(err)
	}
	e.ProjectID = strPtr(projectID)
	e.ActorKind = domain.ActorKind(actorKind)
	e.ActorID = strPtr(actorID)
	e.ActorLogin = strPtr(login)
	e.ActorEmail = strPtr(email)
	e.SubjectID = strPtr(subjectID)
	e.SubjectNumber = intPtr(subjectNumber)
	e.SubjectBranch = strPtr(subjectBranch)
	e.Payload = json.RawMessage(payload)
	e.CauseRunID = strPtr(causeRunID)
	e.DispatchState = domain.DispatchState(dispatch)
	return e, nil
}

// ByCauseRun returns the events a run caused (events.cause_run_id — the downward half of the
// causal chain), oldest first.
func (r *EventsRepo) ByCauseRun(ctx context.Context, runID string) ([]domain.Event, error) {
	rows, err := r.h.r.QueryContext(ctx, `
		SELECT `+eventCols+` FROM events
		WHERE cause_run_id = ? ORDER BY occurred_at, created_at, id`, runID)
	if err != nil {
		return nil, mapErr(err)
	}
	defer func() { _ = rows.Close() }()

	var out []domain.Event
	for rows.Next() {
		e, err := scanEvent(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// NewestHumanActionAt returns the occurred_at of the newest human-actor event on the given
// subject (matched on the subject columns, which agree with the descriptor-derived subject
// keys), or "" when no human has acted there. This is the loop guard's depth-reset probe
// (architecture §9): causal hops older than this timestamp no longer count — a human's
// intervention on a stalled chain must not inherit the chain's exhausted budget.
func (r *EventsRepo) NewestHumanActionAt(ctx context.Context, projectID, subjectKind string, subjectNumber *int64, subjectID *string) (string, error) {
	var at string
	err := r.h.r.QueryRowContext(ctx, `
		SELECT COALESCE(MAX(occurred_at), '') FROM events
		WHERE project_id = ? AND actor_kind = 'human' AND subject_kind = ?
		  AND subject_number IS ? AND subject_id IS ?`,
		projectID, subjectKind, nullInt(subjectNumber), nullStr(subjectID)).Scan(&at)
	return at, mapErr(err)
}
