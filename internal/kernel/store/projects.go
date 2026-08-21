package store

import (
	"context"
	"database/sql"

	"github.com/spruce/lexicode/internal/domain"
)

// ProjectsRepo reads and writes projects and project_members.
type ProjectsRepo struct{ h handle }

// Projects returns the projects repository.
func (s *Store) Projects() *ProjectsRepo { return &ProjectsRepo{h: s.handle()} }

// Projects returns the projects repository bound to this transaction.
func (t *Tx) Projects() *ProjectsRepo { return &ProjectsRepo{h: t.handle()} }

const projectCols = `id, key, name, description, color, owner_id, agent_guidance,
	daily_budget_cents, context_threshold_tokens, verification_days, ticket_seq,
	archived_at, created_at, updated_at`

// Create inserts a project. A duplicate key surfaces as ErrUnique.
func (r *ProjectsRepo) Create(ctx context.Context, p *domain.Project) error {
	_, err := r.h.w.ExecContext(ctx, `
		INSERT INTO projects (`+projectCols+`)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		p.ID, p.Key, p.Name, p.Description, p.Color, p.OwnerID, p.AgentGuidance,
		nullInt(p.DailyBudgetCents), nullInt(p.ContextThresholdTokens),
		nullInt(p.VerificationDays), p.TicketSeq,
		nullStr(p.ArchivedAt), p.CreatedAt, p.UpdatedAt)
	return mapErr(err)
}

// ByID returns the project with this ID, or ErrNotFound.
func (r *ProjectsRepo) ByID(ctx context.Context, id string) (domain.Project, error) {
	return scanProject(r.h.r.QueryRowContext(ctx,
		`SELECT `+projectCols+` FROM projects WHERE id = ?`, id))
}

// ByKey returns the project with this key ('PAY'), or ErrNotFound.
func (r *ProjectsRepo) ByKey(ctx context.Context, key string) (domain.Project, error) {
	return scanProject(r.h.r.QueryRowContext(ctx,
		`SELECT `+projectCols+` FROM projects WHERE key = ?`, key))
}

// List returns every project, archived included, oldest first.
func (r *ProjectsRepo) List(ctx context.Context) ([]domain.Project, error) {
	rows, err := r.h.r.QueryContext(ctx,
		`SELECT `+projectCols+` FROM projects ORDER BY created_at, id`)
	if err != nil {
		return nil, mapErr(err)
	}
	defer func() { _ = rows.Close() }()

	var out []domain.Project
	for rows.Next() {
		p, err := scanProject(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// Update rewrites a project's mutable fields (everything but id, key, ticket_seq, created_at).
// The nullable settings columns write through as-is: nil stays "inherit from workspace".
func (r *ProjectsRepo) Update(ctx context.Context, p *domain.Project) error {
	res, err := r.h.w.ExecContext(ctx, `
		UPDATE projects SET name = ?, description = ?, color = ?, owner_id = ?,
			agent_guidance = ?, daily_budget_cents = ?, context_threshold_tokens = ?,
			verification_days = ?, archived_at = ?, updated_at = ?
		WHERE id = ?`,
		p.Name, p.Description, p.Color, p.OwnerID, p.AgentGuidance,
		nullInt(p.DailyBudgetCents), nullInt(p.ContextThresholdTokens),
		nullInt(p.VerificationDays), nullStr(p.ArchivedAt), p.UpdatedAt, p.ID)
	if err != nil {
		return mapErr(err)
	}
	return errIfNone(res)
}

// AllocateTicketSeq bumps the project's ticket allocator and returns the new sequence number —
// the 14 of PAY-14. Call it inside the same Tx that inserts the ticket, so a failed insert does
// not burn a number silently (a burned number is legal, just untidy).
func (r *ProjectsRepo) AllocateTicketSeq(ctx context.Context, projectID string) (int64, error) {
	var seq int64
	err := r.h.w.QueryRowContext(ctx, `
		UPDATE projects SET ticket_seq = ticket_seq + 1 WHERE id = ? RETURNING ticket_seq`,
		projectID).Scan(&seq)
	if err != nil {
		return 0, mapErr(err)
	}
	return seq, nil
}

// AddMember adds a user to a project. Adding twice surfaces as ErrUnique.
func (r *ProjectsRepo) AddMember(ctx context.Context, projectID, userID string) error {
	_, err := r.h.w.ExecContext(ctx,
		`INSERT INTO project_members (project_id, user_id) VALUES (?, ?)`, projectID, userID)
	return mapErr(err)
}

// MemberIDs returns the user IDs of a project's members.
func (r *ProjectsRepo) MemberIDs(ctx context.Context, projectID string) ([]string, error) {
	rows, err := r.h.r.QueryContext(ctx,
		`SELECT user_id FROM project_members WHERE project_id = ? ORDER BY user_id`, projectID)
	if err != nil {
		return nil, mapErr(err)
	}
	defer func() { _ = rows.Close() }()

	var out []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, rows.Err()
}

func scanProject(row rowScanner) (domain.Project, error) {
	var (
		p                        domain.Project
		budget, threshold, verif sql.NullInt64
		archived                 sql.NullString
	)
	err := row.Scan(&p.ID, &p.Key, &p.Name, &p.Description, &p.Color, &p.OwnerID,
		&p.AgentGuidance, &budget, &threshold, &verif, &p.TicketSeq,
		&archived, &p.CreatedAt, &p.UpdatedAt)
	if err != nil {
		return domain.Project{}, mapErr(err)
	}
	p.DailyBudgetCents = intPtr(budget)
	p.ContextThresholdTokens = intPtr(threshold)
	p.VerificationDays = intPtr(verif)
	p.ArchivedAt = strPtr(archived)
	return p, nil
}

// ProjectStats is the Home-table read model for one project (UI spec §5.1): live counts, spend
// since a caller-chosen instant, and the newest timestamp across the project's activity. It is
// a read shape, not a table, so it lives here rather than in domain.
type ProjectStats struct {
	ProjectID       string
	OpenTickets     int64
	RunningAgents   int64
	NeedsYou        int64
	SpendTodayCents int64
	LastActivity    string
}

// Stats computes ProjectStats for every project in one query. sinceUTC is the RFC3339 instant
// "spend today" starts at (the service passes UTC midnight). Open tickets are those in columns
// whose CATEGORY is not done/canceled — by category, never by column name (plan rule 3).
// RunningAgents counts live runs (queued/provisioning/running); NeedsYou counts runs blocked on
// a human (needs_input/awaiting_approval). LastActivity is the newest of the project's own
// updated_at, its tickets' updated_at, its runs' queued_at and its events' created_at — RFC3339
// UTC strings sort chronologically, so MAX over text is correct.
func (r *ProjectsRepo) Stats(ctx context.Context, sinceUTC string) (map[string]ProjectStats, error) {
	rows, err := r.h.r.QueryContext(ctx, `
		SELECT p.id,
			(SELECT COUNT(*) FROM tickets t JOIN columns c ON c.id = t.column_id
				WHERE t.project_id = p.id AND c.category NOT IN (?, ?)),
			(SELECT COUNT(*) FROM runs r WHERE r.project_id = p.id AND r.state IN (?, ?, ?)),
			(SELECT COUNT(*) FROM runs r WHERE r.project_id = p.id AND r.state IN (?, ?)),
			(SELECT COALESCE(SUM(r.cost_cents), 0) FROM runs r
				WHERE r.project_id = p.id AND r.queued_at >= ?),
			MAX(p.updated_at,
				COALESCE((SELECT MAX(t.updated_at) FROM tickets t WHERE t.project_id = p.id), ''),
				COALESCE((SELECT MAX(r.queued_at) FROM runs r WHERE r.project_id = p.id), ''),
				COALESCE((SELECT MAX(e.created_at) FROM events e WHERE e.project_id = p.id), ''))
		FROM projects p`,
		string(domain.CategoryDone), string(domain.CategoryCanceled),
		string(domain.RunQueued), string(domain.RunProvisioning), string(domain.RunRunning),
		string(domain.RunNeedsInput), string(domain.RunAwaitingApproval),
		sinceUTC)
	if err != nil {
		return nil, mapErr(err)
	}
	defer func() { _ = rows.Close() }()

	out := map[string]ProjectStats{}
	for rows.Next() {
		var s ProjectStats
		if err := rows.Scan(&s.ProjectID, &s.OpenTickets, &s.RunningAgents, &s.NeedsYou,
			&s.SpendTodayCents, &s.LastActivity); err != nil {
			return nil, err
		}
		out[s.ProjectID] = s
	}
	return out, rows.Err()
}

// AgentCount returns how many agents a project has (Overview About card).
func (r *ProjectsRepo) AgentCount(ctx context.Context, projectID string) (int64, error) {
	var n int64
	err := r.h.r.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM agents WHERE project_id = ?`, projectID).Scan(&n)
	return n, mapErr(err)
}

// RunsSince counts a project's runs queued at or after sinceUTC (Overview "runs today").
func (r *ProjectsRepo) RunsSince(ctx context.Context, projectID, sinceUTC string) (int64, error) {
	var n int64
	err := r.h.r.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM runs WHERE project_id = ? AND queued_at >= ?`,
		projectID, sinceUTC).Scan(&n)
	return n, mapErr(err)
}
