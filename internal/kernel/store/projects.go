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
