package store

import (
	"context"
	"database/sql"

	"github.com/spruce/lexicode/internal/domain"
)

// ReposRepo reads and writes the repos table — one row per project (project_id is the
// primary key), created by repo connect (S15).
type ReposRepo struct{ h handle }

// Repos returns the repos repository.
func (s *Store) Repos() *ReposRepo { return &ReposRepo{h: s.handle()} }

// Repos returns the repos repository bound to this transaction.
func (t *Tx) Repos() *ReposRepo { return &ReposRepo{h: t.handle()} }

const repoCols = `project_id, provider, owner, name, default_branch, branch_template,
	setup_script, image_ref, network_policy, network_allowlist, token_secret_id,
	connected_at, last_synced_at, head_sha, head_message, created_at, updated_at`

// Create inserts the project's repos row. A second connect for the same project surfaces as
// ErrUnique (the primary key); callers use Update to refresh an existing connection.
func (r *ReposRepo) Create(ctx context.Context, rp *domain.Repo) error {
	allow, err := jsonText(rp.NetworkAllowlist)
	if err != nil {
		return err
	}
	_, err = r.h.w.ExecContext(ctx, `
		INSERT INTO repos (`+repoCols+`)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		rp.ProjectID, rp.Provider, rp.Owner, rp.Name, nullStr(rp.DefaultBranch),
		nullStr(rp.BranchTemplate), rp.SetupScript, nullStr(rp.ImageRef),
		nullStr(rp.NetworkPolicy), allow, nullStr(rp.TokenSecretID),
		nullStr(rp.ConnectedAt), nullStr(rp.LastSyncedAt), nullStr(rp.HeadSHA),
		nullStr(rp.HeadMessage), rp.CreatedAt, rp.UpdatedAt)
	return mapErr(err)
}

// List returns every connected repo row, ordered by project id — the poller starts one
// worker per row at boot (story S25).
func (r *ReposRepo) List(ctx context.Context) ([]domain.Repo, error) {
	rows, err := r.h.r.QueryContext(ctx,
		`SELECT `+repoCols+` FROM repos ORDER BY project_id`)
	if err != nil {
		return nil, mapErr(err)
	}
	defer func() { _ = rows.Close() }()

	var out []domain.Repo
	for rows.Next() {
		rp, err := scanRepo(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, rp)
	}
	return out, rows.Err()
}

// ByProject returns the project's repo row, or ErrNotFound when no repo is connected.
func (r *ReposRepo) ByProject(ctx context.Context, projectID string) (domain.Repo, error) {
	return scanRepo(r.h.r.QueryRowContext(ctx,
		`SELECT `+repoCols+` FROM repos WHERE project_id = ?`, projectID))
}

// Update rewrites the row's mutable fields (everything but project_id and created_at).
func (r *ReposRepo) Update(ctx context.Context, rp *domain.Repo) error {
	allow, err := jsonText(rp.NetworkAllowlist)
	if err != nil {
		return err
	}
	res, err := r.h.w.ExecContext(ctx, `
		UPDATE repos SET provider = ?, owner = ?, name = ?, default_branch = ?,
			branch_template = ?, setup_script = ?, image_ref = ?, network_policy = ?,
			network_allowlist = ?, token_secret_id = ?, connected_at = ?, last_synced_at = ?,
			head_sha = ?, head_message = ?, updated_at = ?
		WHERE project_id = ?`,
		rp.Provider, rp.Owner, rp.Name, nullStr(rp.DefaultBranch), nullStr(rp.BranchTemplate),
		rp.SetupScript, nullStr(rp.ImageRef), nullStr(rp.NetworkPolicy), allow,
		nullStr(rp.TokenSecretID), nullStr(rp.ConnectedAt), nullStr(rp.LastSyncedAt),
		nullStr(rp.HeadSHA), nullStr(rp.HeadMessage), rp.UpdatedAt, rp.ProjectID)
	if err != nil {
		return mapErr(err)
	}
	return errIfNone(res)
}

// Delete removes the project's repo row. Disconnect keeps everything the bootstrap imported —
// tickets, wiki pages, triggers and agents reference the project, never the repo row.
func (r *ReposRepo) Delete(ctx context.Context, projectID string) error {
	res, err := r.h.w.ExecContext(ctx, `DELETE FROM repos WHERE project_id = ?`, projectID)
	if err != nil {
		return mapErr(err)
	}
	return errIfNone(res)
}

func scanRepo(row rowScanner) (domain.Repo, error) {
	var (
		rp                                      domain.Repo
		branch, tmpl, image, policy, secretID   sql.NullString
		connectedAt, syncedAt, headSHA, headMsg sql.NullString
		allow                                   string
	)
	err := row.Scan(&rp.ProjectID, &rp.Provider, &rp.Owner, &rp.Name, &branch, &tmpl,
		&rp.SetupScript, &image, &policy, &allow, &secretID, &connectedAt, &syncedAt,
		&headSHA, &headMsg, &rp.CreatedAt, &rp.UpdatedAt)
	if err != nil {
		return domain.Repo{}, mapErr(err)
	}
	rp.DefaultBranch = strPtr(branch)
	rp.BranchTemplate = strPtr(tmpl)
	rp.ImageRef = strPtr(image)
	rp.NetworkPolicy = strPtr(policy)
	rp.TokenSecretID = strPtr(secretID)
	rp.ConnectedAt = strPtr(connectedAt)
	rp.LastSyncedAt = strPtr(syncedAt)
	rp.HeadSHA = strPtr(headSHA)
	rp.HeadMessage = strPtr(headMsg)
	if err := jsonScan(allow, &rp.NetworkAllowlist); err != nil {
		return domain.Repo{}, err
	}
	return rp, nil
}
