package bootstrap

import (
	"context"
	"fmt"
	"net/http"

	"github.com/spruce/lexicode/internal/domain"
	"github.com/spruce/lexicode/internal/kernel/audit"
	"github.com/spruce/lexicode/internal/kernel/auth"
	"github.com/spruce/lexicode/internal/kernel/httpx"
	"github.com/spruce/lexicode/internal/kernel/ports"
)

// This file is the S35 wiki import: POST /projects/{key}/wiki/import, the re-runnable
// version of the bootstrap checklist's docs section, reachable from the WIKI page at any
// time (D-11 is import-only, but never once-only). It lives in this package because it is
// the S15 detection and import verbatim — detectDocs, ImportedPaths marking, importDoc —
// behind a smaller endpoint: `{"preview": true}` returns the detected files with proposed
// scopes and already-imported flags and writes nothing; `{"files": [{path, scope}]}`
// imports exactly the checked subset. Idempotency is wiki_pages.imported_from, same as the
// bootstrap apply: a path already imported is skipped even when a stale client re-sends it,
// so importing twice never duplicates.

// WikiImportRequest is the POST body. Preview=true ignores Files.
type WikiImportRequest struct {
	Preview bool        `json:"preview"`
	Files   []DocChoice `json:"files"`
}

// WikiImportPreview is the preview response: every detected doc, marked.
type WikiImportPreview struct {
	Docs []DocCandidate `json:"docs"`
}

// WikiImportResult reports what an import created and what it skipped as already imported.
type WikiImportResult struct {
	PagesCreated []string `json:"pages_created"` // slugs
	DocsSkipped  []string `json:"docs_skipped"`  // already-imported paths
}

// wikiImportRoutes registers the endpoint; called from Routes.
func (s *Service) wikiImportRoutes(mux httpx.Registrar, a *auth.Service) {
	member := func(h http.HandlerFunc) http.Handler {
		return a.RequireAuth(a.RequireProjectMember(h))
	}
	mux.Handle("POST /api/v1/projects/{key}/wiki/import", member(s.handleWikiImport))
}

func (s *Service) handleWikiImport(w http.ResponseWriter, r *http.Request) {
	body, ok := httpx.DecodeJSON[WikiImportRequest](w, r)
	if !ok {
		return
	}
	if body.Preview {
		pv, err := s.WikiImportPreview(r.Context(), r.PathValue("key"))
		if err != nil {
			s.writeError(w, err)
			return
		}
		httpx.WriteJSON(w, http.StatusOK, pv)
		return
	}
	res, err := s.WikiImport(r.Context(), r.PathValue("key"), body.Files)
	if err != nil {
		s.writeError(w, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, res)
}

// WikiImportPreview detects the repo's instruction docs and marks the already-imported
// ones — the S15 detection behind a docs-only payload. Writes nothing.
func (s *Service) WikiImportPreview(ctx context.Context, projectKey string) (WikiImportPreview, error) {
	p, creds, ref, branch, err := s.repoContext(ctx, projectKey)
	if err != nil {
		return WikiImportPreview{}, err
	}
	detected, _, err := s.detectDocs(ctx, creds, ref, branch)
	if err != nil {
		return WikiImportPreview{}, err
	}
	importedPaths, err := s.st.Wiki().ImportedPaths(ctx, p.ID)
	if err != nil {
		return WikiImportPreview{}, err
	}
	pv := WikiImportPreview{Docs: markImported(detected, importedPaths)}
	return pv, nil
}

// WikiImport imports the checked subset as live wiki pages, skipping paths already
// imported, and leaves one audit entry for the whole import.
func (s *Service) WikiImport(ctx context.Context, projectKey string, files []DocChoice) (WikiImportResult, error) {
	p, creds, ref, branch, err := s.repoContext(ctx, projectKey)
	if err != nil {
		return WikiImportResult{}, err
	}
	var userID string
	if u, ok := auth.UserFrom(ctx); ok {
		userID = u.ID
	}
	created, skipped, err := s.importDocChoices(ctx, p, creds, ref, branch, files, userID)
	if err != nil {
		return WikiImportResult{}, err
	}
	res := WikiImportResult{PagesCreated: created, DocsSkipped: skipped}
	if err := s.audit.Write(ctx, "wiki.import",
		audit.Target{Kind: "wiki_page", ID: p.ID, ProjectID: p.ID}, nil, res); err != nil {
		return WikiImportResult{}, err
	}
	s.emit(ctx, "wiki.imported", p, map[string]any{
		"pages": len(res.PagesCreated), "skipped": len(res.DocsSkipped),
	})
	return res, nil
}

// repoContext resolves everything a forge read needs: the project, its repo's creds, ref
// and default branch. Shared by the wiki import verbs. A project with no connected repo
// surfaces as ErrNotFound — writeError's 404 already reads "No repository is connected to
// this project", which is exactly the state the import dialog renders.
func (s *Service) repoContext(ctx context.Context, projectKey string) (domain.Project, ports.Creds, domain.RepoRef, string, error) {
	p, err := s.st.Projects().ByKey(ctx, projectKey)
	if err != nil {
		return domain.Project{}, ports.Creds{}, domain.RepoRef{}, "", err
	}
	rp, err := s.st.Repos().ByProject(ctx, p.ID)
	if err != nil {
		return domain.Project{}, ports.Creds{}, domain.RepoRef{}, "", err
	}
	creds, err := s.creds(ctx, rp)
	if err != nil {
		return domain.Project{}, ports.Creds{}, domain.RepoRef{}, "", err
	}
	branch := ""
	if rp.DefaultBranch != nil {
		branch = *rp.DefaultBranch
	}
	return p, creds, rp.Ref(), branch, nil
}

// markImported flags candidates whose path already backs a wiki page (unchecked, labeled) —
// what makes both the bootstrap preview and the wiki import preview honest on a re-run.
func markImported(detected []DocCandidate, importedPaths map[string]string) []DocCandidate {
	out := make([]DocCandidate, 0, len(detected))
	for _, d := range detected {
		if slug, ok := importedPaths[d.Path]; ok {
			d.Checked = false
			d.AlreadyImported = true
			d.PageSlug = slug
		}
		out = append(out, d)
	}
	return out
}

// importDocChoices imports the checked docs as live wiki pages (imported_from = the repo
// path), skipping already-imported paths. Shared by the bootstrap apply and the wiki
// import so the two flows can never drift.
func (s *Service) importDocChoices(ctx context.Context, p domain.Project, creds ports.Creds,
	ref domain.RepoRef, branch string, choices []DocChoice, userID string) (created, skipped []string, err error) {
	created, skipped = []string{}, []string{}
	importedPaths, err := s.st.Wiki().ImportedPaths(ctx, p.ID)
	if err != nil {
		return nil, nil, err
	}
	for _, choice := range choices {
		if _, done := importedPaths[choice.Path]; done {
			skipped = append(skipped, choice.Path)
			continue
		}
		scope := domain.AgentScope(choice.Scope)
		if !scope.IsValid() {
			return nil, nil, fieldErr("docs",
				fmt.Sprintf("%q is not a valid agent scope for %s.", choice.Scope, choice.Path))
		}
		content, ok, err := s.docs.ReadFileIfExists(ctx, creds, ref, branch, choice.Path)
		if err != nil {
			return nil, nil, err
		}
		if !ok {
			continue // deleted since the preview; nothing to import
		}
		slug, err := s.importDoc(ctx, p, choice, string(content), userID)
		if err != nil {
			return nil, nil, err
		}
		importedPaths[choice.Path] = slug
		created = append(created, slug)
	}
	return created, skipped, nil
}
