package store

import (
	"context"
	"database/sql"

	"github.com/spruce/lexicode/internal/domain"
)

// DirectivesRepo appends to and reads the append-only agent_directives table. S15 writes each
// starter agent's version 1; the agent settings story (S16) adds the version list and diff.
type DirectivesRepo struct{ h handle }

// Directives returns the agent-directives repository.
func (s *Store) Directives() *DirectivesRepo { return &DirectivesRepo{h: s.handle()} }

// Directives returns the agent-directives repository bound to this transaction.
func (t *Tx) Directives() *DirectivesRepo { return &DirectivesRepo{h: t.handle()} }

// Create appends one directive version. A duplicate (agent_id, version) surfaces as ErrUnique.
func (r *DirectivesRepo) Create(ctx context.Context, d *domain.AgentDirective) error {
	_, err := r.h.w.ExecContext(ctx, `
		INSERT INTO agent_directives (id, agent_id, version, body, token_estimate,
			author_id, note, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		d.ID, d.AgentID, d.Version, d.Body, d.TokenEstimate,
		nullStr(d.AuthorID), d.Note, d.CreatedAt)
	return mapErr(err)
}

// ByID returns one directive version.
func (r *DirectivesRepo) ByID(ctx context.Context, id string) (domain.AgentDirective, error) {
	return scanDirective(r.h.r.QueryRowContext(ctx, `
		SELECT id, agent_id, version, body, token_estimate, author_id, note, created_at
		FROM agent_directives WHERE id = ?`, id))
}

// ByAgentVersion returns one directive version addressed by (agent, version) — what the diff
// view fetches.
func (r *DirectivesRepo) ByAgentVersion(ctx context.Context, agentID string, version int64) (domain.AgentDirective, error) {
	return scanDirective(r.h.r.QueryRowContext(ctx, `
		SELECT id, agent_id, version, body, token_estimate, author_id, note, created_at
		FROM agent_directives WHERE agent_id = ? AND version = ?`, agentID, version))
}

// ForAgent returns an agent's directive versions, newest first (the version-list order).
func (r *DirectivesRepo) ForAgent(ctx context.Context, agentID string) ([]domain.AgentDirective, error) {
	rows, err := r.h.r.QueryContext(ctx, `
		SELECT id, agent_id, version, body, token_estimate, author_id, note, created_at
		FROM agent_directives WHERE agent_id = ? ORDER BY version DESC`, agentID)
	if err != nil {
		return nil, mapErr(err)
	}
	defer func() { _ = rows.Close() }()

	var out []domain.AgentDirective
	for rows.Next() {
		d, err := scanDirective(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

func scanDirective(row rowScanner) (domain.AgentDirective, error) {
	var (
		d      domain.AgentDirective
		author sql.NullString
	)
	err := row.Scan(&d.ID, &d.AgentID, &d.Version, &d.Body, &d.TokenEstimate, &author, &d.Note,
		&d.CreatedAt)
	if err != nil {
		return domain.AgentDirective{}, mapErr(err)
	}
	d.AuthorID = strPtr(author)
	return d, nil
}
