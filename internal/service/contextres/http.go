package contextres

import (
	"errors"
	"net/http"

	"github.com/spruce/lexicode/internal/domain"
	"github.com/spruce/lexicode/internal/kernel/auth"
	"github.com/spruce/lexicode/internal/kernel/httpx"
	"github.com/spruce/lexicode/internal/kernel/store"
)

// Routes registers the two S34 read surfaces (contracts §5):
//
//	GET /api/v1/agents/{id}/context-preview        dry resolve — "what every run sees"
//	GET /api/v1/projects/{key}/wiki/context-budget always-total vs threshold, per page
func (s *Service) Routes(mux httpx.Registrar, a *auth.Service) {
	member := func(h http.HandlerFunc) http.Handler {
		return a.RequireAuth(a.RequireProjectMember(h))
	}
	viaAgent := func(h http.HandlerFunc) http.Handler {
		return a.RequireAuth(s.requireMember(a, h))
	}
	mux.Handle("GET /api/v1/agents/{id}/context-preview", viaAgent(s.handlePreview))
	mux.Handle("GET /api/v1/projects/{key}/wiki/context-budget", member(s.handleBudget))
}

// requireMember resolves membership through the agent row, exactly like the agents service's
// /agents/{id} routes. Must sit inside RequireAuth.
func (s *Service) requireMember(a *auth.Service, next http.HandlerFunc) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		u, ok := auth.UserFrom(r.Context())
		if !ok {
			httpx.WriteProblem(w, http.StatusUnauthorized, "unauthenticated",
				"Not authenticated", "Sign in to use this endpoint.")
			return
		}
		ag, err := s.st.Agents().ByID(r.Context(), r.PathValue("id"))
		if errors.Is(err, store.ErrNotFound) {
			httpx.WriteProblem(w, http.StatusNotFound, httpx.TypeNotFound,
				"Not found", "Nothing matches this path.")
			return
		}
		if err != nil {
			s.writeError(w, err)
			return
		}
		p, err := s.st.Projects().ByID(r.Context(), ag.ProjectID)
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

// contextItemBody mirrors the run detail's RunContextItem wire shape — the preview renders
// with the same component vocabulary as the Context panel (one resolver, three surfaces).
type contextItemBody struct {
	Provider   string `json:"provider"`
	SourceKind string `json:"source_kind"`
	SourceRef  string `json:"source_ref"`
	Title      string `json:"title"`
	Reason     string `json:"reason"`
	Tokens     int64  `json:"tokens"`
	Position   int64  `json:"position"`
	Injected   bool   `json:"injected"`
}

// handlePreview is GET /agents/{id}/context-preview: the dry resolve (contracts §2.6,
// Dry=true). The request carries no ticket, changed paths or task summary — the ticket
// provider yields nothing and path/keyword wiki scopes match nothing, so the stack is
// exactly what EVERY run of this agent sees. total_tokens counts injected items only;
// listed-not-injected repo files (D-11) appear in the stack but cost no prompt tokens.
func (s *Service) handlePreview(w http.ResponseWriter, r *http.Request) {
	ag, err := s.st.Agents().ByID(r.Context(), r.PathValue("id"))
	if err != nil {
		s.writeError(w, err)
		return
	}
	if s.res == nil {
		httpx.WriteProblem(w, http.StatusServiceUnavailable, httpx.TypeInternal,
			"Resolver unavailable", "The run scheduler is not running.")
		return
	}
	items, err := s.res.PreviewContext(r.Context(), ag.ProjectID, ag.ID)
	if err != nil {
		s.writeError(w, err)
		return
	}
	bodies := make([]contextItemBody, 0, len(items))
	var total int64
	for _, it := range items {
		bodies = append(bodies, contextItemBody{
			Provider: it.Provider, SourceKind: it.SourceKind, SourceRef: it.SourceRef,
			Title: it.Title, Reason: it.Reason, Tokens: it.Tokens,
			Position: it.Position, Injected: it.Injected,
		})
		if it.Injected {
			total += it.Tokens
		}
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{
		"items": bodies, "total_tokens": total,
	})
}

// handleBudget is GET /projects/{key}/wiki/context-budget: the numbers behind the
// ContextMeter — every live always-scoped page with its token estimate, their total, and
// the project's effective threshold (its own, or the inherited workspace default).
func (s *Service) handleBudget(w http.ResponseWriter, r *http.Request) {
	p, err := s.st.Projects().ByKey(r.Context(), r.PathValue("key"))
	if err != nil {
		s.writeError(w, err)
		return
	}
	threshold := int64(0)
	if p.ContextThresholdTokens != nil {
		threshold = *p.ContextThresholdTokens
	} else {
		ws, err := s.st.Workspace().Get(r.Context())
		if err != nil {
			s.writeError(w, err)
			return
		}
		threshold = ws.DefaultContextThresholdTokens
	}
	pages, err := s.st.Wiki().ForProject(r.Context(), p.ID)
	if err != nil {
		s.writeError(w, err)
		return
	}
	type pageBody struct {
		ID     string `json:"id"`
		Slug   string `json:"slug"`
		Title  string `json:"title"`
		Tokens int64  `json:"tokens"`
	}
	out := make([]pageBody, 0)
	var always int64
	for _, pg := range pages {
		if pg.AgentScope != domain.ScopeAlways || pg.State != domain.WikiLive || pg.ArchivedAt != nil {
			continue
		}
		out = append(out, pageBody{ID: pg.ID, Slug: pg.Slug, Title: pg.Title, Tokens: pg.TokenEstimate})
		always += pg.TokenEstimate
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{
		"threshold_tokens": threshold,
		"always_tokens":    always,
		"over":             always > threshold,
		"pages":            out,
	})
}

func (s *Service) writeError(w http.ResponseWriter, err error) {
	if errors.Is(err, store.ErrNotFound) {
		httpx.WriteProblem(w, http.StatusNotFound, httpx.TypeNotFound,
			"Not found", "Nothing matches this path.")
		return
	}
	s.logger.Error("contextres: request failed", "error", err.Error())
	httpx.WriteProblem(w, http.StatusInternalServerError, httpx.TypeInternal,
		"Something went wrong", "The server could not complete this request.")
}
