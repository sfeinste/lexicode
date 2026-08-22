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
	var (
		d      domain.AgentDirective
		author sql.NullString
	)
	err := r.h.r.QueryRowContext(ctx, `
		SELECT id, agent_id, version, body, token_estimate, author_id, note, created_at
		FROM agent_directives WHERE id = ?`, id).
		Scan(&d.ID, &d.AgentID, &d.Version, &d.Body, &d.TokenEstimate, &author, &d.Note,
			&d.CreatedAt)
	if err != nil {
		return domain.AgentDirective{}, mapErr(err)
	}
	d.AuthorID = strPtr(author)
	return d, nil
}
