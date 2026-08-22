package store

import (
	"context"
	"database/sql"
	"strings"

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

// CreatePage inserts a wiki page. The FTS index follows via the 0002 triggers — every
// wiki_pages write path (insert, update, delete) syncs wiki_fts without repo code having to
// remember. A duplicate slug within the project surfaces as ErrUnique.
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
	return mapErr(err)
}

// ByID returns one page, or ErrNotFound.
func (r *WikiRepo) ByID(ctx context.Context, id string) (domain.WikiPage, error) {
	return scanWikiPage(r.h.r.QueryRowContext(ctx,
		`SELECT `+wikiCols+` FROM wiki_pages WHERE id = ?`, id))
}

// UpdatePage writes every mutable column back. The 0002 triggers re-index title/body/tags
// changes in wiki_fts inside the same transaction.
func (r *WikiRepo) UpdatePage(ctx context.Context, p *domain.WikiPage) error {
	scopePaths, err := jsonText(p.ScopePaths)
	if err != nil {
		return err
	}
	tags, err := jsonText(p.Tags)
	if err != nil {
		return err
	}
	res, err := r.h.w.ExecContext(ctx, `
		UPDATE wiki_pages SET slug = ?, title = ?, parent_id = ?, position = ?, owner_id = ?,
			verified_until = ?, agent_scope = ?, scope_paths = ?, tags = ?, body = ?,
			token_estimate = ?, state = ?, demoted_at = ?, demoted_from = ?, archived_at = ?,
			updated_at = ?
		WHERE id = ?`,
		p.Slug, p.Title, nullStr(p.ParentID), p.Position, nullStr(p.OwnerID),
		nullStr(p.VerifiedUntil), string(p.AgentScope), scopePaths, tags, p.Body,
		p.TokenEstimate, string(p.State), nullStr(p.DemotedAt), nullStr(p.DemotedFrom),
		nullStr(p.ArchivedAt), p.UpdatedAt, p.ID)
	if err != nil {
		return mapErr(err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return mapErr(err)
	}
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

// HasChildren reports whether any non-archived page names this one as parent — the guard
// behind the two-level rule (a page with children can never itself become a child).
func (r *WikiRepo) HasChildren(ctx context.Context, id string) (bool, error) {
	var n int64
	err := r.h.r.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM wiki_pages
		WHERE parent_id = ? AND archived_at IS NULL`, id).Scan(&n)
	return n > 0, mapErr(err)
}

// WikiSearchHit is one FTS5 result: the page plus highlighted snippets. Match regions are
// wrapped in \x01…\x02 markers (snippet() with char(1)/char(2)) so the client can render its
// own <mark> without the server shipping HTML.
type WikiSearchHit struct {
	Page         domain.WikiPage
	TitleSnippet string
	BodySnippet  string
}

// wikiColsQualified is wikiCols with every column prefixed for the FTS join, where bare
// title/body/tags would be ambiguous against wiki_fts's columns.
var wikiColsQualified = func() string {
	cols := strings.Split(wikiCols, ",")
	for i, c := range cols {
		cols[i] = "wiki_pages." + strings.TrimSpace(c)
	}
	return strings.Join(cols, ", ")
}()

// Search runs an FTS5 MATCH over live, non-archived pages of one project, best match first
// (bm25 with the title weighted over body over tags). The match string must already be valid
// FTS5 query syntax — the service quotes user input before calling.
func (r *WikiRepo) Search(ctx context.Context, projectID, match string, limit int) ([]WikiSearchHit, error) {
	rows, err := r.h.r.QueryContext(ctx, `
		SELECT `+wikiColsQualified+`,
			snippet(wiki_fts, 0, char(1), char(2), '…', 12),
			snippet(wiki_fts, 1, char(1), char(2), '…', 16)
		FROM wiki_fts
		JOIN wiki_pages ON wiki_pages.rowid = wiki_fts.rowid
		WHERE wiki_fts MATCH ? AND wiki_pages.project_id = ?
			AND wiki_pages.archived_at IS NULL AND wiki_pages.state = 'live'
		ORDER BY bm25(wiki_fts, 4.0, 1.0, 2.0)
		LIMIT ?`, match, projectID, limit)
	if err != nil {
		return nil, mapErr(err)
	}
	defer func() { _ = rows.Close() }()

	var out []WikiSearchHit
	for rows.Next() {
		h, err := scanWikiSearchHit(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, h)
	}
	return out, rows.Err()
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

// ExpiredAlways returns, across every project, the live `always`-scoped pages whose
// verified_until date is strictly before today (YYYY-MM-DD) — the daily demotion job's
// worklist (architecture §11: verified_until enforcement). "Verified until 2026-11-01"
// holds through that day, matching the S33 client-side red-date rule.
func (r *WikiRepo) ExpiredAlways(ctx context.Context, today string) ([]domain.WikiPage, error) {
	rows, err := r.h.r.QueryContext(ctx, `
		SELECT `+wikiCols+` FROM wiki_pages
		WHERE agent_scope = 'always' AND state = 'live' AND archived_at IS NULL
		  AND verified_until IS NOT NULL AND verified_until < ?
		ORDER BY project_id, position, id`, today)
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

// BySlug returns the project's page with this slug, or ErrNotFound. Slugs are unique per
// project (schema); proposals therefore carry their own distinct slugs.
func (r *WikiRepo) BySlug(ctx context.Context, projectID, slug string) (domain.WikiPage, error) {
	return scanWikiPage(r.h.r.QueryRowContext(ctx,
		`SELECT `+wikiCols+` FROM wiki_pages WHERE project_id = ? AND slug = ?`,
		projectID, slug))
}

// LatestVersion returns the highest version number recorded for a page, 0 when the page has
// no version rows yet. The S21 proposal flow snapshots it as proposed_base_version so the
// S35 accept can run its three-way check.
func (r *WikiRepo) LatestVersion(ctx context.Context, pageID string) (int64, error) {
	var v int64
	err := r.h.r.QueryRowContext(ctx,
		`SELECT COALESCE(MAX(version), 0) FROM wiki_versions WHERE page_id = ?`, pageID).Scan(&v)
	return v, mapErr(err)
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

// wikiScanBuf holds the nullable temporaries one wiki_pages row scans through. dests
// returns the pointer list in wikiCols order; finish copies the temporaries onto the page.
type wikiScanBuf struct {
	p                                          domain.WikiPage
	scope, state, scopePaths, tags             string
	parent, owner, verified, runID, target     sql.NullString
	imported, demotedAt, demotedFrom, archived sql.NullString
	baseVersion                                sql.NullInt64
}

func (b *wikiScanBuf) dests() []any {
	return []any{&b.p.ID, &b.p.ProjectID, &b.p.Slug, &b.p.Title, &b.parent, &b.p.Position,
		&b.owner, &b.verified, &b.scope, &b.scopePaths, &b.tags, &b.p.Body,
		&b.p.TokenEstimate, &b.state, &b.runID, &b.baseVersion, &b.target, &b.imported,
		&b.demotedAt, &b.demotedFrom, &b.archived, &b.p.CreatedAt, &b.p.UpdatedAt}
}

func (b *wikiScanBuf) finish() (domain.WikiPage, error) {
	p := b.p
	p.AgentScope = domain.AgentScope(b.scope)
	p.State = domain.WikiState(b.state)
	p.ParentID = strPtr(b.parent)
	p.OwnerID = strPtr(b.owner)
	p.VerifiedUntil = strPtr(b.verified)
	p.ProposedByRunID = strPtr(b.runID)
	p.ProposedBaseVersion = intPtr(b.baseVersion)
	p.ProposalTargetID = strPtr(b.target)
	p.ImportedFrom = strPtr(b.imported)
	p.DemotedAt = strPtr(b.demotedAt)
	p.DemotedFrom = strPtr(b.demotedFrom)
	p.ArchivedAt = strPtr(b.archived)
	if err := jsonScan(b.scopePaths, &p.ScopePaths); err != nil {
		return domain.WikiPage{}, err
	}
	if err := jsonScan(b.tags, &p.Tags); err != nil {
		return domain.WikiPage{}, err
	}
	return p, nil
}

func scanWikiPage(row rowScanner) (domain.WikiPage, error) {
	var b wikiScanBuf
	if err := row.Scan(b.dests()...); err != nil {
		return domain.WikiPage{}, mapErr(err)
	}
	return b.finish()
}

func scanWikiSearchHit(row rowScanner) (WikiSearchHit, error) {
	var (
		b   wikiScanBuf
		hit WikiSearchHit
	)
	if err := row.Scan(append(b.dests(), &hit.TitleSnippet, &hit.BodySnippet)...); err != nil {
		return WikiSearchHit{}, mapErr(err)
	}
	p, err := b.finish()
	if err != nil {
		return WikiSearchHit{}, err
	}
	hit.Page = p
	return hit, nil
}
