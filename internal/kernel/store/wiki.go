package store

import (
	"context"
	"database/sql"

	"github.com/spruce/lexicode/internal/domain"
)

// WikiRepo reads and writes wiki_pages and wiki_versions. S15 needs creation (bootstrap doc
// import) and the imported_from read model that makes a re-scan idempotent; the full wiki
// service arrives with its own story and grows this repository.
type WikiRepo struct{ h handle }

// Wiki returns the wiki repository.
func (s *Store) Wiki() *WikiRepo { return &WikiRepo{h: s.handle()} }

// Wiki returns the wiki repository bound to this transaction.
func (t *Tx) Wiki() *WikiRepo { return &WikiRepo{h: t.handle()} }

const wikiCols = `id, project_id, slug, title, parent_id, position, owner_id, verified_until,
	agent_scope, scope_paths, tags, body, token_estimate, state, proposed_by_run_id,
	proposed_base_version, proposal_target_id, imported_from, demoted_at, demoted_from,
	archived_at, created_at, updated_at`

// CreatePage inserts a wiki page and keeps the FTS index in step (wiki_fts is an external
// content table; nothing else maintains it yet). A duplicate slug within the project surfaces
// as ErrUnique.
func (r *WikiRepo) CreatePage(ctx context.Context, p *domain.WikiPage) error {
	scopePaths, err := jsonText(p.ScopePaths)
	if err != nil {
		return err
	}
	tags, err := jsonText(p.Tags)
	if err != nil {
		return err
	}
	_, err = r.h.w.ExecContext(ctx, `
		INSERT INTO wiki_pages (`+wikiCols+`)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		p.ID, p.ProjectID, p.Slug, p.Title, nullStr(p.ParentID), p.Position,
		nullStr(p.OwnerID), nullStr(p.VerifiedUntil), string(p.AgentScope), scopePaths, tags,
		p.Body, p.TokenEstimate, string(p.State), nullStr(p.ProposedByRunID),
		nullInt(p.ProposedBaseVersion), nullStr(p.ProposalTargetID), nullStr(p.ImportedFrom),
		nullStr(p.DemotedAt), nullStr(p.DemotedFrom), nullStr(p.ArchivedAt),
		p.CreatedAt, p.UpdatedAt)
	if err != nil {
		return mapErr(err)
	}
	_, err = r.h.w.ExecContext(ctx, `
		INSERT INTO wiki_fts(rowid, title, body, tags)
		SELECT rowid, title, body, tags FROM wiki_pages WHERE id = ?`, p.ID)
	return mapErr(err)
}

// CreateVersion appends one immutable snapshot row.
func (r *WikiRepo) CreateVersion(ctx context.Context, v *domain.WikiVersion) error {
	fm, err := jsonText(v.FrontMatter)
	if err != nil {
		return err
	}
	_, err = r.h.w.ExecContext(ctx, `
		INSERT INTO wiki_versions (id, page_id, version, title, body, front_matter,
			author_user_id, author_run_id, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		v.ID, v.PageID, v.Version, v.Title, v.Body, fm,
		nullStr(v.AuthorUserID), nullStr(v.AuthorRunID), v.CreatedAt)
	return mapErr(err)
}

// ForProject returns the project's pages, tree order (position within parent), archived
// included — callers filter.
func (r *WikiRepo) ForProject(ctx context.Context, projectID string) ([]domain.WikiPage, error) {
	rows, err := r.h.r.QueryContext(ctx, `
		SELECT `+wikiCols+` FROM wiki_pages WHERE project_id = ?
		ORDER BY parent_id IS NOT NULL, position, id`, projectID)
	if err != nil {
		return nil, mapErr(err)
	}
	defer func() { _ = rows.Close() }()

	var out []domain.WikiPage
	for rows.Next() {
		p, err := scanWikiPage(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// ImportedPaths returns the set of repo paths already seeded as wiki pages
// (imported_from → page slug), the idempotency basis for a re-scan (S15).
func (r *WikiRepo) ImportedPaths(ctx context.Context, projectID string) (map[string]string, error) {
	rows, err := r.h.r.QueryContext(ctx, `
		SELECT imported_from, slug FROM wiki_pages
		WHERE project_id = ? AND imported_from IS NOT NULL AND archived_at IS NULL`, projectID)
	if err != nil {
		return nil, mapErr(err)
	}
	defer func() { _ = rows.Close() }()

	out := map[string]string{}
	for rows.Next() {
		var path, slug string
		if err := rows.Scan(&path, &slug); err != nil {
			return nil, mapErr(err)
		}
		out[path] = slug
	}
	return out, rows.Err()
}

func scanWikiPage(row rowScanner) (domain.WikiPage, error) {
	var (
		p                                          domain.WikiPage
		scope, state, scopePaths, tags             string
		parent, owner, verified, runID, target     sql.NullString
		imported, demotedAt, demotedFrom, archived sql.NullString
		baseVersion                                sql.NullInt64
	)
	err := row.Scan(&p.ID, &p.ProjectID, &p.Slug, &p.Title, &parent, &p.Position, &owner,
		&verified, &scope, &scopePaths, &tags, &p.Body, &p.TokenEstimate, &state, &runID,
		&baseVersion, &target, &imported, &demotedAt, &demotedFrom, &archived,
		&p.CreatedAt, &p.UpdatedAt)
	if err != nil {
		return domain.WikiPage{}, mapErr(err)
	}
	p.AgentScope = domain.AgentScope(scope)
	p.State = domain.WikiState(state)
	p.ParentID = strPtr(parent)
	p.OwnerID = strPtr(owner)
	p.VerifiedUntil = strPtr(verified)
	p.ProposedByRunID = strPtr(runID)
	p.ProposedBaseVersion = intPtr(baseVersion)
	p.ProposalTargetID = strPtr(target)
	p.ImportedFrom = strPtr(imported)
	p.DemotedAt = strPtr(demotedAt)
	p.DemotedFrom = strPtr(demotedFrom)
	p.ArchivedAt = strPtr(archived)
	if err := jsonScan(scopePaths, &p.ScopePaths); err != nil {
		return domain.WikiPage{}, err
	}
	if err := jsonScan(tags, &p.Tags); err != nil {
		return domain.WikiPage{}, err
	}
	return p, nil
}
