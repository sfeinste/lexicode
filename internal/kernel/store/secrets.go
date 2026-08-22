package store

import (
	"context"
	"database/sql"

	"github.com/spruce/lexicode/internal/domain"
)

// SecretsRepo reads and writes the secrets table (data model §2, D-16). It stores ciphertext
// and nonce blobs verbatim; all cryptography lives in internal/kernel/secrets, which is this
// repository's only caller. Nothing above kernel/secrets ever sees these rows.
//
// One schema note this repository has to compensate for: in SQLite, NULLs are distinct in a
// UNIQUE index, so `UNIQUE (scope, project_id, name)` does not stop two workspace-scope rows
// (project_id NULL) from sharing a name. kernel/secrets serialises Set/Rename through
// ByName lookups inside the process's single write path instead.
type SecretsRepo struct{ h handle }

// Secrets returns the secrets repository.
func (s *Store) Secrets() *SecretsRepo { return &SecretsRepo{h: s.handle()} }

// Secrets returns the secrets repository bound to this transaction.
func (t *Tx) Secrets() *SecretsRepo { return &SecretsRepo{h: t.handle()} }

const secretCols = `id, scope, project_id, name, ciphertext, nonce, created_by, created_at, updated_at`

// Create inserts a secret row.
func (r *SecretsRepo) Create(ctx context.Context, sec *domain.Secret) error {
	_, err := r.h.w.ExecContext(ctx, `
		INSERT INTO secrets (`+secretCols+`)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		sec.ID, sec.Scope, nullStr(sec.ProjectID), sec.Name, sec.Ciphertext, sec.Nonce,
		sec.CreatedBy, sec.CreatedAt, sec.UpdatedAt)
	return mapErr(err)
}

// Update rewrites a secret's name, ciphertext, nonce and updated_at — the mutable columns.
// Scope and project_id are immutable: a secret never moves between projects.
func (r *SecretsRepo) Update(ctx context.Context, sec *domain.Secret) error {
	res, err := r.h.w.ExecContext(ctx, `
		UPDATE secrets SET name = ?, ciphertext = ?, nonce = ?, updated_at = ?
		WHERE id = ?`,
		sec.Name, sec.Ciphertext, sec.Nonce, sec.UpdatedAt, sec.ID)
	if err != nil {
		return mapErr(err)
	}
	return errIfNone(res)
}

// Delete removes a secret row. Rows referencing it (repos.token_secret_id,
// agents.forge_token_secret_id) RESTRICT the delete into ErrForeignKey.
func (r *SecretsRepo) Delete(ctx context.Context, id string) error {
	res, err := r.h.w.ExecContext(ctx, `DELETE FROM secrets WHERE id = ?`, id)
	if err != nil {
		return mapErr(err)
	}
	return errIfNone(res)
}

// ByID returns one secret.
func (r *SecretsRepo) ByID(ctx context.Context, id string) (domain.Secret, error) {
	return scanSecret(r.h.r.QueryRowContext(ctx,
		`SELECT `+secretCols+` FROM secrets WHERE id = ?`, id))
}

// ByName returns the secret with this name in the given scope. projectID is empty for
// workspace scope.
func (r *SecretsRepo) ByName(ctx context.Context, scope domain.SecretScope, projectID, name string) (domain.Secret, error) {
	if scope == domain.SecretScopeWorkspace {
		return scanSecret(r.h.r.QueryRowContext(ctx, `
			SELECT `+secretCols+` FROM secrets
			WHERE scope = ? AND project_id IS NULL AND name = ?`, scope, name))
	}
	return scanSecret(r.h.r.QueryRowContext(ctx, `
		SELECT `+secretCols+` FROM secrets
		WHERE scope = ? AND project_id = ? AND name = ?`, scope, projectID, name))
}

// List returns the secrets of one scope, name order. projectID is empty for workspace scope.
func (r *SecretsRepo) List(ctx context.Context, scope domain.SecretScope, projectID string) ([]domain.Secret, error) {
	var (
		rows *sql.Rows
		err  error
	)
	if scope == domain.SecretScopeWorkspace {
		rows, err = r.h.r.QueryContext(ctx, `
			SELECT `+secretCols+` FROM secrets
			WHERE scope = ? AND project_id IS NULL ORDER BY name`, scope)
	} else {
		rows, err = r.h.r.QueryContext(ctx, `
			SELECT `+secretCols+` FROM secrets
			WHERE scope = ? AND project_id = ? ORDER BY name`, scope, projectID)
	}
	if err != nil {
		return nil, mapErr(err)
	}
	defer func() { _ = rows.Close() }()

	var out []domain.Secret
	for rows.Next() {
		sec, err := scanSecret(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, sec)
	}
	return out, mapErr(rows.Err())
}

func scanSecret(row rowScanner) (domain.Secret, error) {
	var (
		sec domain.Secret
		pid sql.NullString
	)
	err := row.Scan(&sec.ID, &sec.Scope, &pid, &sec.Name, &sec.Ciphertext, &sec.Nonce,
		&sec.CreatedBy, &sec.CreatedAt, &sec.UpdatedAt)
	if err != nil {
		return domain.Secret{}, mapErr(err)
	}
	sec.ProjectID = strPtr(pid)
	return sec, nil
}
