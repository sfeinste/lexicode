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

// ForPRSubject returns the project's events about one pull request, oldest first — the pull
// request's own history. Without it a run spawned by the second review on a pull request is
// handed that review and nothing else: the first review, the comments answering it and the
// pull request's own opening are all rows this run can see nothing of.
//
// Matched on the subject columns, which is what every producer of a PR-shaped event sets (the
// GitHub poller's newEvent and the `submit_review` MCP tool both write subject_kind 'pr' plus
// the number), so this rides ix_events_subject's (project_id, subject_kind, subject_number).
// `trigger` kind events are excluded, as in SinceForProject: a firing notification is
// Lexicode's own bookkeeping, not something that happened on the pull request.
func (r *EventsRepo) ForPRSubject(ctx context.Context, projectID string, prNumber int64) ([]domain.Event, error) {
	rows, err := r.h.r.QueryContext(ctx, `
		SELECT `+eventCols+` FROM events
		WHERE project_id = ? AND subject_kind = 'pr' AND subject_number = ?
			AND kind != 'trigger'
		ORDER BY occurred_at, created_at, id`, projectID, prNumber)
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

// SinceForProject returns the project's events with occurred_at >= since, newest first —
// the backtest scan (D-13, architecture §8.1). `trigger` kind events are excluded for parity
// with the engine, which never evaluates its own firing notifications. Rides the
// ix_events_backtest index's (project_id, …) prefix.
func (r *EventsRepo) SinceForProject(ctx context.Context, projectID, since string) ([]domain.Event, error) {
	rows, err := r.h.r.QueryContext(ctx, `
		SELECT `+eventCols+` FROM events
		WHERE project_id = ? AND kind != 'trigger' AND occurred_at >= ?
		ORDER BY occurred_at DESC, created_at DESC, id DESC`, projectID, since)
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

// HasExternalForProject reports whether the project has any stored events from an external
// source — how backtest tells "no history yet" apart from "history exists but nothing in the
// window". Internal bookkeeping events (source 'internal', bus.SourceInternal — project
// created, agent updated, …) exist from a project's first second and are not the history the
// empty state talks about: that builds up from the moment a repository is connected.
func (r *EventsRepo) HasExternalForProject(ctx context.Context, projectID string) (bool, error) {
	var n int
	err := r.h.r.QueryRowContext(ctx, `
		SELECT EXISTS(SELECT 1 FROM events WHERE project_id = ? AND source != 'internal')`,
		projectID).Scan(&n)
	return n == 1, mapErr(err)
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
