package wiki

import (
	"errors"
	"fmt"
	"log/slog"
	"net/http"

	"github.com/spruce/lexicode/internal/domain"
	"github.com/spruce/lexicode/internal/kernel/auth"
	"github.com/spruce/lexicode/internal/kernel/httpx"
	"github.com/spruce/lexicode/internal/kernel/store"
)

// Routes registers the S33 + S35 endpoints (contracts §5 route inventory):
//
//	GET|POST /api/v1/projects/{key}/wiki        the tree list · create a page
//	GET      /api/v1/projects/{key}/wiki/search ?q= — FTS5, primary navigation
//	GET|PATCH|DELETE /api/v1/wiki/{id}          detail (with backlinks) · edit · archive
//	POST     /api/v1/wiki/{id}/accept           accept an agent proposal (S35)
//	POST     /api/v1/wiki/{id}/dismiss          dismiss an agent proposal (S35)
//
// The context-budget read lives in contextres (S34); the repo import in bootstrap (S35 —
// it needs the forge seams).
func (s *Service) Routes(mux httpx.Registrar, a *auth.Service) {
	member := func(h http.HandlerFunc) http.Handler {
		return a.RequireAuth(a.RequireProjectMember(h))
	}
	viaPage := func(h http.HandlerFunc) http.Handler {
		return a.RequireAuth(s.requireMember(a, h))
	}

	mux.Handle("GET /api/v1/projects/{key}/wiki", member(s.handleList))
	mux.Handle("POST /api/v1/projects/{key}/wiki", member(s.handleCreate))
	mux.Handle("GET /api/v1/projects/{key}/wiki/search", member(s.handleSearch))

	mux.Handle("GET /api/v1/wiki/{id}", viaPage(s.handleGet))
	mux.Handle("PATCH /api/v1/wiki/{id}", viaPage(s.handlePatch))
	mux.Handle("DELETE /api/v1/wiki/{id}", viaPage(s.handleArchive))
	mux.Handle("POST /api/v1/wiki/{id}/accept", viaPage(s.handleAccept))
	mux.Handle("POST /api/v1/wiki/{id}/dismiss", viaPage(s.handleDismiss))
}

// requireMember is RequireProjectMember for the /wiki/{id} routes, whose path carries no
// project key: the owning project comes from the page row. Must sit inside RequireAuth.
func (s *Service) requireMember(a *auth.Service, next http.HandlerFunc) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		u, ok := auth.UserFrom(r.Context())
		if !ok {
			httpx.WriteProblem(w, http.StatusUnauthorized, "unauthenticated",
				"Not authenticated", "Sign in to use this endpoint.")
			return
		}
		page, err := s.st.Wiki().ByID(r.Context(), r.PathValue("id"))
		if errors.Is(err, store.ErrNotFound) {
			httpx.WriteProblem(w, http.StatusNotFound, httpx.TypeNotFound,
				"Not found", "Nothing matches this path.")
			return
		}
		if err != nil {
			s.writeError(w, err)
			return
		}
		p, err := s.st.Projects().ByID(r.Context(), page.ProjectID)
		if err != nil {
			s.writeError(w, err)
			return
		}
		member, err := a.IsProjectMember(r.Context(), u, p)
		if err != nil {
			s.writeError(w, err)
			return
		}
		if !member {
			httpx.WriteProblem(w, http.StatusForbidden, "forbidden",
				"Not a project member", "You are not a member of this project.")
			return
		}
		next(w, r)
	})
}

// ---------------------------------------------------------------- bodies -----

// wikiPageBody is how a page renders everywhere. The list includes bodies too — a wiki is
// prompt-sized by design (it is injected into agent context), so one payload feeds the
// tree, the tag index and mention autocomplete.
type wikiPageBody struct {
	ID                  string   `json:"id"`
	ProjectID           string   `json:"project_id"`
	Slug                string   `json:"slug"`
	Title               string   `json:"title"`
	ParentID            *string  `json:"parent_id"`
	Position            float64  `json:"position"`
	OwnerID             *string  `json:"owner_id"`
	VerifiedUntil       *string  `json:"verified_until"`
	AgentScope          string   `json:"agent_scope"`
	ScopePaths          []string `json:"scope_paths"`
	Tags                []string `json:"tags"`
	Body                string   `json:"body"`
	TokenEstimate       int64    `json:"token_estimate"`
	State               string   `json:"state"`
	ProposedByRunID     *string  `json:"proposed_by_run_id"`
	ProposedBaseVersion *int64   `json:"proposed_base_version"`
	ProposalTargetID    *string  `json:"proposal_target_id"`
	ProposedReason      *string  `json:"proposed_reason"`
	ImportedFrom        *string  `json:"imported_from"`
	DemotedAt           *string  `json:"demoted_at"`
	DemotedFrom         *string  `json:"demoted_from"`
	ArchivedAt          *string  `json:"archived_at"`
	CreatedAt           string   `json:"created_at"`
	UpdatedAt           string   `json:"updated_at"`
}

func toWikiPageBody(p domain.WikiPage) wikiPageBody {
	scopePaths := p.ScopePaths
	if scopePaths == nil {
		scopePaths = []string{}
	}
	tags := p.Tags
	if tags == nil {
		tags = []string{}
	}
	return wikiPageBody{
		ID: p.ID, ProjectID: p.ProjectID, Slug: p.Slug, Title: p.Title,
		ParentID: p.ParentID, Position: p.Position, OwnerID: p.OwnerID,
		VerifiedUntil: p.VerifiedUntil, AgentScope: string(p.AgentScope),
		ScopePaths: scopePaths, Tags: tags, Body: p.Body,
		TokenEstimate: p.TokenEstimate, State: string(p.State),
		ProposedByRunID: p.ProposedByRunID, ProposedBaseVersion: p.ProposedBaseVersion,
		ProposalTargetID: p.ProposalTargetID, ProposedReason: p.ProposedReason,
		ImportedFrom: p.ImportedFrom, DemotedAt: p.DemotedAt, DemotedFrom: p.DemotedFrom,
		ArchivedAt: p.ArchivedAt, CreatedAt: p.CreatedAt, UpdatedAt: p.UpdatedAt,
	}
}

type wikiListBody struct {
	Pages []wikiPageBody `json:"pages"`
}

type backlinkBody struct {
	SourceKind string   `json:"source_kind"`
	SourceID   string   `json:"source_id"`
	Title      string   `json:"title"`
	SourceSlug string   `json:"source_slug,omitempty"`
	SourceKey  string   `json:"source_key,omitempty"`
	Paragraphs []string `json:"paragraphs"`
}

type unlinkedBody struct {
	PageID    string `json:"page_id"`
	Slug      string `json:"slug"`
	Title     string `json:"title"`
	Paragraph string `json:"paragraph"`
}

// proposalBody is the review view's extras for a proposed page (S35): the reason, the
// proposing run, and — for edit-proposals — the target and the bodies the diff and the
// three-way conflict warning render from.
type proposalBody struct {
	Reason         string  `json:"reason"`
	RunID          *string `json:"run_id"`
	TargetID       *string `json:"target_id"`
	TargetSlug     string  `json:"target_slug,omitempty"`
	TargetTitle    string  `json:"target_title,omitempty"`
	TargetBody     string  `json:"target_body,omitempty"`
	BaseVersion    int64   `json:"base_version,omitempty"`
	CurrentVersion int64   `json:"current_version,omitempty"`
	BaseBody       string  `json:"base_body,omitempty"`
}

type wikiDetailBody struct {
	Page      wikiPageBody   `json:"page"`
	Version   int64          `json:"version"`
	Backlinks []backlinkBody `json:"backlinks"`
	Unlinked  []unlinkedBody `json:"unlinked_mentions"`
	Proposal  *proposalBody  `json:"proposal,omitempty"`
}

// searchHitBody carries snippets with match regions wrapped in \x01…\x02 (FTS5 snippet with
// char(1)/char(2)); the client splits on the markers and renders its own <mark> — the
// server never ships HTML.
type searchHitBody struct {
	ID           string `json:"id"`
	Slug         string `json:"slug"`
	Title        string `json:"title"`
	TitleSnippet string `json:"title_snippet"`
	BodySnippet  string `json:"body_snippet"`
}

type searchBody struct {
	Results []searchHitBody `json:"results"`
}

// ---------------------------------------------------------------- handlers -----

func (s *Service) handleList(w http.ResponseWriter, r *http.Request) {
	pages, err := s.List(r.Context(), r.PathValue("key"))
	if err != nil {
		s.writeError(w, err)
		return
	}
	body := wikiListBody{Pages: make([]wikiPageBody, 0, len(pages))}
	for _, p := range pages {
		body.Pages = append(body.Pages, toWikiPageBody(p))
	}
	httpx.WriteJSON(w, http.StatusOK, body)
}

type createPageBody struct {
	Title    string  `json:"title"`
	ParentID *string `json:"parent_id"`
	Body     string  `json:"body"`
}

func (s *Service) handleCreate(w http.ResponseWriter, r *http.Request) {
	body, ok := httpx.DecodeJSON[createPageBody](w, r)
	if !ok {
		return
	}
	page, err := s.Create(r.Context(), r.PathValue("key"), CreateInput(body))
	if err != nil {
		s.writeError(w, err)
		return
	}
	httpx.WriteJSON(w, http.StatusCreated, toWikiPageBody(page))
}

func (s *Service) handleSearch(w http.ResponseWriter, r *http.Request) {
	hits, err := s.Search(r.Context(), r.PathValue("key"), r.URL.Query().Get("q"))
	if err != nil {
		s.writeError(w, err)
		return
	}
	body := searchBody{Results: make([]searchHitBody, 0, len(hits))}
	for _, h := range hits {
		body.Results = append(body.Results, searchHitBody{
			ID: h.Page.ID, Slug: h.Page.Slug, Title: h.Page.Title,
			TitleSnippet: h.TitleSnippet, BodySnippet: h.BodySnippet,
		})
	}
	httpx.WriteJSON(w, http.StatusOK, body)
}

func (s *Service) handleGet(w http.ResponseWriter, r *http.Request) {
	d, err := s.Get(r.Context(), r.PathValue("id"))
	if err != nil {
		s.writeError(w, err)
		return
	}
	body := wikiDetailBody{
		Page: toWikiPageBody(d.Page), Version: d.Version,
		Backlinks: make([]backlinkBody, 0, len(d.Backlinks)),
		Unlinked:  make([]unlinkedBody, 0, len(d.Unlinked)),
	}
	for _, g := range d.Backlinks {
		body.Backlinks = append(body.Backlinks, backlinkBody(g))
	}
	for _, u := range d.Unlinked {
		body.Unlinked = append(body.Unlinked, unlinkedBody(u))
	}
	if d.Proposal != nil {
		body.Proposal = &proposalBody{
			Reason: d.Proposal.Reason, RunID: d.Proposal.RunID,
			TargetID: d.Proposal.TargetID, TargetSlug: d.Proposal.TargetSlug,
			TargetTitle: d.Proposal.TargetTitle, TargetBody: d.Proposal.TargetBody,
			BaseVersion: d.Proposal.BaseVersion, CurrentVersion: d.Proposal.CurrentVersion,
			BaseBody: d.Proposal.BaseBody,
		}
	}
	httpx.WriteJSON(w, http.StatusOK, body)
}

// handleAccept is POST /wiki/{id}/accept: the resulting live page comes back — the proposal
// itself gone live for creates, the updated target for edits. A stale edit-proposal is the
// 409 `wiki_proposal_conflict` problem naming both versions.
func (s *Service) handleAccept(w http.ResponseWriter, r *http.Request) {
	page, err := s.AcceptProposal(r.Context(), r.PathValue("id"))
	if err != nil {
		s.writeError(w, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"page": toWikiPageBody(page)})
}

// handleDismiss is POST /wiki/{id}/dismiss: archives the proposal, leaves the audit row.
func (s *Service) handleDismiss(w http.ResponseWriter, r *http.Request) {
	if err := s.DismissProposal(r.Context(), r.PathValue("id")); err != nil {
		s.writeError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

type patchPageBody struct {
	Title         *string   `json:"title"`
	Body          *string   `json:"body"`
	ParentID      OptStr    `json:"parent_id"`
	Position      *float64  `json:"position"`
	OwnerID       OptStr    `json:"owner_id"`
	VerifiedUntil OptStr    `json:"verified_until"`
	AgentScope    *string   `json:"agent_scope"`
	ScopePaths    *[]string `json:"scope_paths"`
	Tags          *[]string `json:"tags"`
}

func (s *Service) handlePatch(w http.ResponseWriter, r *http.Request) {
	body, ok := httpx.DecodeJSON[patchPageBody](w, r)
	if !ok {
		return
	}
	page, err := s.Update(r.Context(), r.PathValue("id"), UpdatePatch(body))
	if err != nil {
		s.writeError(w, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, toWikiPageBody(page))
}

func (s *Service) handleArchive(w http.ResponseWriter, r *http.Request) {
	if _, err := s.Archive(r.Context(), r.PathValue("id")); err != nil {
		s.writeError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// writeError maps service errors to problems: field errors → 400 validation_failed; the
// depth rule → 409 wiki_depth_exceeded; archived → 409 wiki_page_archived; missing → 404.
func (s *Service) writeError(w http.ResponseWriter, err error) {
	var ve *ValidationError
	if errors.As(err, &ve) {
		httpx.WriteValidation(w, ve.Fields)
		return
	}
	var de *DepthError
	if errors.As(err, &de) {
		httpx.WriteProblem(w, http.StatusConflict, "wiki_depth_exceeded",
			"Two levels at most", de.Detail)
		return
	}
	var ae *ArchivedError
	if errors.As(err, &ae) {
		httpx.WriteProblem(w, http.StatusConflict, "wiki_page_archived",
			"Page is archived", ae.Title+" is archived and can no longer be changed.")
		return
	}
	var np *NotAProposalError
	if errors.As(err, &np) {
		httpx.WriteProblem(w, http.StatusConflict, "wiki_not_a_proposal",
			"Not a pending proposal",
			np.Title+" is not a pending proposal — it may already have been accepted or dismissed.")
		return
	}
	var ce *ConflictError
	if errors.As(err, &ce) {
		httpx.WriteProblem(w, http.StatusConflict, "wiki_proposal_conflict",
			"The page has changed since this was proposed",
			fmt.Sprintf("%s is now at version %d, but the proposal was written against version %d. "+
				"Review the differences and use Edit to bring the proposal up to date.",
				ce.TargetTitle, ce.CurrentVersion, ce.BaseVersion))
		return
	}
	if errors.Is(err, store.ErrNotFound) {
		httpx.WriteProblem(w, http.StatusNotFound, httpx.TypeNotFound,
			"Not found", "Nothing matches this path.")
		return
	}
	s.logger.Error("wiki: request failed", slog.String("error", err.Error()))
	httpx.WriteProblem(w, http.StatusInternalServerError, httpx.TypeInternal,
		"Internal error", "Something went wrong on the server. The error has been logged.")
}
