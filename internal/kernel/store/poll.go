package store

import (
	"context"
	"database/sql"

	"github.com/spruce/lexicode/internal/domain"
)

// PollCursorsRepo reads and writes poll_cursors — the GitHub poller's per-(project, resource)
// position: last upstream updated_at, etag, and whether the cold-start baseline ran (story S25,
// architecture §7).
type PollCursorsRepo struct{ h handle }

// PollCursors returns the poll-cursors repository.
func (s *Store) PollCursors() *PollCursorsRepo { return &PollCursorsRepo{h: s.handle()} }

// PollCursors returns the poll-cursors repository bound to this transaction.
func (t *Tx) PollCursors() *PollCursorsRepo { return &PollCursorsRepo{h: t.handle()} }

// Get returns the cursor for one (project, resource), or ErrNotFound before the first Upsert.
func (r *PollCursorsRepo) Get(ctx context.Context, projectID, resource string) (domain.PollCursor, error) {
	var (
		c      domain.PollCursor
		done   int64
		polled sql.NullString
	)
	err := r.h.r.QueryRowContext(ctx, `
		SELECT project_id, resource, cursor, etag, baseline_done, last_polled_at
		FROM poll_cursors WHERE project_id = ? AND resource = ?`, projectID, resource).
		Scan(&c.ProjectID, &c.Resource, &c.Cursor, &c.Etag, &done, &polled)
	if err != nil {
		return domain.PollCursor{}, mapErr(err)
	}
	c.BaselineDone = done != 0
	c.LastPolledAt = strPtr(polled)
	return c, nil
}

// Upsert writes the cursor row, inserting or replacing on the (project, resource) key.
func (r *PollCursorsRepo) Upsert(ctx context.Context, c *domain.PollCursor) error {
	_, err := r.h.w.ExecContext(ctx, `
		INSERT INTO poll_cursors (project_id, resource, cursor, etag, baseline_done, last_polled_at)
		VALUES (?, ?, ?, ?, ?, ?)
		ON CONFLICT (project_id, resource) DO UPDATE SET
			cursor = excluded.cursor, etag = excluded.etag,
			baseline_done = excluded.baseline_done, last_polled_at = excluded.last_polled_at`,
		c.ProjectID, c.Resource, c.Cursor, c.Etag, boolInt(c.BaselineDone), nullStr(c.LastPolledAt))
	return mapErr(err)
}

// DeleteForProject removes every cursor row of one project — repo disconnect, so a later
// reconnect starts from a fresh baseline.
func (r *PollCursorsRepo) DeleteForProject(ctx context.Context, projectID string) error {
	_, err := r.h.w.ExecContext(ctx,
		`DELETE FROM poll_cursors WHERE project_id = ?`, projectID)
	return mapErr(err)
}

// PollPRStateRepo reads and writes poll_pr_state — the per-PR snapshot the poller diffs
// against to derive activity types (architecture §7).
type PollPRStateRepo struct{ h handle }

// PollPRState returns the poll-PR-state repository.
func (s *Store) PollPRState() *PollPRStateRepo { return &PollPRStateRepo{h: s.handle()} }

// PollPRState returns the poll-PR-state repository bound to this transaction.
func (t *Tx) PollPRState() *PollPRStateRepo { return &PollPRStateRepo{h: t.handle()} }

// ForProject returns every recorded PR state of the project, keyed by PR number.
func (r *PollPRStateRepo) ForProject(ctx context.Context, projectID string) (map[int64]domain.PollPRState, error) {
	rows, err := r.h.r.QueryContext(ctx, `
		SELECT project_id, number, head_sha, state, draft, updated_at, review_cursor,
			additions, deletions
		FROM poll_pr_state WHERE project_id = ?`, projectID)
	if err != nil {
		return nil, mapErr(err)
	}
	defer func() { _ = rows.Close() }()

	out := make(map[int64]domain.PollPRState)
	for rows.Next() {
		var (
			st         domain.PollPRState
			draft      int64
			adds, dels sql.NullInt64
		)
		if err := rows.Scan(&st.ProjectID, &st.Number, &st.HeadSHA, &st.State, &draft,
			&st.UpdatedAt, &st.ReviewCursor, &adds, &dels); err != nil {
			return nil, mapErr(err)
		}
		st.Draft = draft != 0
		st.Additions = intPtr(adds)
		st.Deletions = intPtr(dels)
		out[st.Number] = st
	}
	return out, rows.Err()
}

// Upsert writes one PR's state, inserting or replacing on the (project, number) key.
func (r *PollPRStateRepo) Upsert(ctx context.Context, st *domain.PollPRState) error {
	_, err := r.h.w.ExecContext(ctx, `
		INSERT INTO poll_pr_state (project_id, number, head_sha, state, draft, updated_at,
			review_cursor, additions, deletions)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT (project_id, number) DO UPDATE SET
			head_sha = excluded.head_sha, state = excluded.state, draft = excluded.draft,
			updated_at = excluded.updated_at, review_cursor = excluded.review_cursor,
			additions = excluded.additions, deletions = excluded.deletions`,
		st.ProjectID, st.Number, st.HeadSHA, st.State, boolInt(st.Draft),
		st.UpdatedAt, st.ReviewCursor, nullInt(st.Additions), nullInt(st.Deletions))
	return mapErr(err)
}

// DeleteForProject removes every PR-state row of one project (repo disconnect).
func (r *PollPRStateRepo) DeleteForProject(ctx context.Context, projectID string) error {
	_, err := r.h.w.ExecContext(ctx,
		`DELETE FROM poll_pr_state WHERE project_id = ?`, projectID)
	return mapErr(err)
}
