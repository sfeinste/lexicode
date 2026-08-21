package store

import (
	"context"
	"database/sql"

	"github.com/spruce/lexicode/internal/domain"
)

// AgentsRepo reads and writes the agents table. Permissions cross as the typed
// domain.AgentPermissions — enforcement config, never raw JSON (data model §3.1).
type AgentsRepo struct{ h handle }

// Agents returns the agents repository.
func (s *Store) Agents() *AgentsRepo { return &AgentsRepo{h: s.handle()} }

// Agents returns the agents repository bound to this transaction.
func (t *Tx) Agents() *AgentsRepo { return &AgentsRepo{h: t.handle()} }

const agentCols = `id, project_id, name, role, color, runtime_id, model, effort, autonomy,
	permissions, git_author_name, git_author_email, forge_login, forge_token_secret_id,
	concurrency_cap, daily_cap_cents, max_wall_clock_seconds, max_steps, enabled,
	directive_version_id, archived_at, created_at, updated_at`

// Create inserts an agent. A duplicate name within the project surfaces as ErrUnique.
func (r *AgentsRepo) Create(ctx context.Context, a *domain.Agent) error {
	perms, err := jsonText(a.Permissions)
	if err != nil {
		return err
	}
	_, err = r.h.w.ExecContext(ctx, `
		INSERT INTO agents (`+agentCols+`)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		a.ID, a.ProjectID, a.Name, a.Role, a.Color, a.RuntimeID, a.Model, a.Effort,
		string(a.Autonomy), perms, a.GitAuthorName, a.GitAuthorEmail,
		nullStr(a.ForgeLogin), nullStr(a.ForgeTokenSecretID),
		a.ConcurrencyCap, nullInt(a.DailyCapCents), a.MaxWallClockSeconds, a.MaxSteps,
		boolInt(a.Enabled), nullStr(a.DirectiveVersionID), nullStr(a.ArchivedAt),
		a.CreatedAt, a.UpdatedAt)
	return mapErr(err)
}

// ByID returns the agent with this ID, or ErrNotFound.
func (r *AgentsRepo) ByID(ctx context.Context, id string) (domain.Agent, error) {
	return scanAgent(r.h.r.QueryRowContext(ctx,
		`SELECT `+agentCols+` FROM agents WHERE id = ?`, id))
}

// ForProject returns a project's agents, archived included, oldest first.
func (r *AgentsRepo) ForProject(ctx context.Context, projectID string) ([]domain.Agent, error) {
	rows, err := r.h.r.QueryContext(ctx,
		`SELECT `+agentCols+` FROM agents WHERE project_id = ? ORDER BY created_at, id`, projectID)
	if err != nil {
		return nil, mapErr(err)
	}
	defer func() { _ = rows.Close() }()

	var out []domain.Agent
	for rows.Next() {
		a, err := scanAgent(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// Update rewrites an agent's mutable fields (everything but id, project_id, created_at).
func (r *AgentsRepo) Update(ctx context.Context, a *domain.Agent) error {
	perms, err := jsonText(a.Permissions)
	if err != nil {
		return err
	}
	res, err := r.h.w.ExecContext(ctx, `
		UPDATE agents SET name = ?, role = ?, color = ?, runtime_id = ?, model = ?, effort = ?,
			autonomy = ?, permissions = ?, git_author_name = ?, git_author_email = ?,
			forge_login = ?, forge_token_secret_id = ?, concurrency_cap = ?, daily_cap_cents = ?,
			max_wall_clock_seconds = ?, max_steps = ?, enabled = ?, directive_version_id = ?,
			archived_at = ?, updated_at = ?
		WHERE id = ?`,
		a.Name, a.Role, a.Color, a.RuntimeID, a.Model, a.Effort, string(a.Autonomy), perms,
		a.GitAuthorName, a.GitAuthorEmail, nullStr(a.ForgeLogin), nullStr(a.ForgeTokenSecretID),
		a.ConcurrencyCap, nullInt(a.DailyCapCents), a.MaxWallClockSeconds, a.MaxSteps,
		boolInt(a.Enabled), nullStr(a.DirectiveVersionID), nullStr(a.ArchivedAt),
		a.UpdatedAt, a.ID)
	if err != nil {
		return mapErr(err)
	}
	return errIfNone(res)
}

func scanAgent(row rowScanner) (domain.Agent, error) {
	var (
		a                                        domain.Agent
		autonomy, perms                          string
		forgeLogin, forgeSecret, directive, arch sql.NullString
		dailyCap                                 sql.NullInt64
		enabled                                  int64
	)
	err := row.Scan(&a.ID, &a.ProjectID, &a.Name, &a.Role, &a.Color, &a.RuntimeID, &a.Model,
		&a.Effort, &autonomy, &perms, &a.GitAuthorName, &a.GitAuthorEmail,
		&forgeLogin, &forgeSecret, &a.ConcurrencyCap, &dailyCap, &a.MaxWallClockSeconds,
		&a.MaxSteps, &enabled, &directive, &arch, &a.CreatedAt, &a.UpdatedAt)
	if err != nil {
		return domain.Agent{}, mapErr(err)
	}
	a.Autonomy = domain.Autonomy(autonomy)
	a.ForgeLogin = strPtr(forgeLogin)
	a.ForgeTokenSecretID = strPtr(forgeSecret)
	a.DailyCapCents = intPtr(dailyCap)
	a.Enabled = enabled != 0
	a.DirectiveVersionID = strPtr(directive)
	a.ArchivedAt = strPtr(arch)
	if err := jsonScan(perms, &a.Permissions); err != nil {
		return domain.Agent{}, err
	}
	return a, nil
}
