package agents

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strconv"

	"github.com/spruce/lexicode/internal/domain"
	"github.com/spruce/lexicode/internal/kernel/auth"
	"github.com/spruce/lexicode/internal/kernel/httpx"
	"github.com/spruce/lexicode/internal/kernel/store"
)

// Routes registers the S16 endpoints (contracts §5 route inventory):
//
//	GET|POST /api/v1/projects/{key}/agents        project members; GET takes ?eligible=1
//	POST /api/v1/projects/{key}/agents/starter    the starter roster action
//	GET|PATCH|DELETE /api/v1/agents/{id}          members, resolved via the agent; DELETE = archive
//	PUT /api/v1/agents/{id}/directive             save (append a version only when changed)
//	POST /api/v1/agents/{id}/directives           the contracts-inventory spelling of the same save
//	GET /api/v1/agents/{id}/directives            version list, newest first, bodies included
//	GET /api/v1/agents/{id}/directives/{version}  one version (the diff view's fetch)
//	POST /api/v1/agents/{id}/directive/estimate   live token estimate for an unsaved body
func (s *Service) Routes(mux httpx.Registrar, a *auth.Service) {
	member := func(h http.HandlerFunc) http.Handler {
		return a.RequireAuth(a.RequireProjectMember(h))
	}
	viaAgent := func(h http.HandlerFunc) http.Handler {
		return a.RequireAuth(s.requireMember(a, h))
	}

	mux.Handle("GET /api/v1/projects/{key}/agents", member(s.handleList))
	mux.Handle("POST /api/v1/projects/{key}/agents", member(s.handleCreate))
	mux.Handle("POST /api/v1/projects/{key}/agents/starter", member(s.handleStarter))

	mux.Handle("GET /api/v1/agents/{id}", viaAgent(s.handleGet))
	mux.Handle("PATCH /api/v1/agents/{id}", viaAgent(s.handlePatch))
	mux.Handle("DELETE /api/v1/agents/{id}", viaAgent(s.handleArchive))
	mux.Handle("PUT /api/v1/agents/{id}/directive", viaAgent(s.handleSaveDirective))
	mux.Handle("POST /api/v1/agents/{id}/directives", viaAgent(s.handleSaveDirective))
	mux.Handle("GET /api/v1/agents/{id}/directives", viaAgent(s.handleListDirectives))
	mux.Handle("GET /api/v1/agents/{id}/directives/{version}", viaAgent(s.handleDirectiveVersion))
	mux.Handle("POST /api/v1/agents/{id}/directive/estimate", viaAgent(s.handleEstimate))
}

// requireMember is RequireProjectMember for the /agents/{id} routes, whose path carries no
// project key: the owning project comes from the agent row. Must sit inside RequireAuth.
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

// ---------------------------------------------------------------- bodies -----

// permissionsBody is the §3.1 object on the wire — typed, never raw JSON.
type permissionsBody struct {
	ReadFiles       bool `json:"read_files"`
	EditFiles       bool `json:"edit_files"`
	RunCommands     bool `json:"run_commands"`
	PushBranches    bool `json:"push_branches"`
	OpenPRs         bool `json:"open_prs"`
	CommentPRs      bool `json:"comment_prs"`
	SubmitReviews   bool `json:"submit_reviews"`
	CreateWikiPages bool `json:"create_wiki_pages"`
}

func toPermissionsBody(p domain.AgentPermissions) permissionsBody {
	return permissionsBody(p)
}

// agentBody is how an agent renders everywhere, roster stats included (all zeros until runs
// exist — the queries are real, the data arrives with S22).
type agentBody struct {
	ID                  string          `json:"id"`
	ProjectID           string          `json:"project_id"`
	Name                string          `json:"name"`
	Role                string          `json:"role"`
	Color               string          `json:"color"`
	RuntimeID           string          `json:"runtime_id"`
	Model               string          `json:"model"`
	Effort              string          `json:"effort"`
	Autonomy            string          `json:"autonomy"`
	Permissions         permissionsBody `json:"permissions"`
	GitAuthorName       string          `json:"git_author_name"`
	GitAuthorEmail      string          `json:"git_author_email"`
	ForgeLogin          *string         `json:"forge_login"`
	ConcurrencyCap      int64           `json:"concurrency_cap"`
	DailyCapCents       *int64          `json:"daily_cap_cents"`
	MaxWallClockSeconds int64           `json:"max_wall_clock_seconds"`
	MaxSteps            int64           `json:"max_steps"`
	Enabled             bool            `json:"enabled"`
	DirectiveVersionID  *string         `json:"directive_version_id"`
	ArchivedAt          *string         `json:"archived_at"`
	CreatedAt           string          `json:"created_at"`
	UpdatedAt           string          `json:"updated_at"`
	RunsWeek            int64           `json:"runs_week"`
	SpendWeekCents      int64           `json:"spend_week_cents"`
	SuccessRate         *float64        `json:"success_rate"`
}

func toAgentBody(ws WithStats) agentBody {
	a := ws.Agent
	return agentBody{
		ID: a.ID, ProjectID: a.ProjectID, Name: a.Name, Role: a.Role, Color: a.Color,
		RuntimeID: a.RuntimeID, Model: a.Model, Effort: a.Effort,
		Autonomy: string(a.Autonomy), Permissions: toPermissionsBody(a.Permissions),
		GitAuthorName: a.GitAuthorName, GitAuthorEmail: a.GitAuthorEmail,
		ForgeLogin:     a.ForgeLogin,
		ConcurrencyCap: a.ConcurrencyCap, DailyCapCents: a.DailyCapCents,
		MaxWallClockSeconds: a.MaxWallClockSeconds, MaxSteps: a.MaxSteps,
		Enabled: a.Enabled, DirectiveVersionID: a.DirectiveVersionID,
		ArchivedAt: a.ArchivedAt, CreatedAt: a.CreatedAt, UpdatedAt: a.UpdatedAt,
		RunsWeek: ws.Stats.RunsWeek, SpendWeekCents: ws.Stats.SpendWeekCents,
		SuccessRate: ws.Stats.SuccessRate,
	}
}

type agentListBody struct {
	Agents []agentBody `json:"agents"`
}

type directiveBody struct {
	ID            string  `json:"id"`
	AgentID       string  `json:"agent_id"`
	Version       int64   `json:"version"`
	Body          string  `json:"body"`
	TokenEstimate int64   `json:"token_estimate"`
	AuthorID      *string `json:"author_id"`
	Note          string  `json:"note"`
	CreatedAt     string  `json:"created_at"`
}

func toDirectiveBody(d domain.AgentDirective) directiveBody {
	return directiveBody{
		ID: d.ID, AgentID: d.AgentID, Version: d.Version, Body: d.Body,
		TokenEstimate: d.TokenEstimate, AuthorID: d.AuthorID, Note: d.Note,
		CreatedAt: d.CreatedAt,
	}
}

type directiveListBody struct {
	Directives []directiveBody `json:"directives"`
}

// ---------------------------------------------------------------- handlers -----

func (s *Service) handleList(w http.ResponseWriter, r *http.Request) {
	list, err := s.List(r.Context(), r.PathValue("key"), r.URL.Query().Get("eligible") == "1")
	if err != nil {
		s.writeError(w, err)
		return
	}
	body := agentListBody{Agents: make([]agentBody, 0, len(list))}
	for _, a := range list {
		body.Agents = append(body.Agents, toAgentBody(a))
	}
	httpx.WriteJSON(w, http.StatusOK, body)
}

type createAgentBody struct {
	Name      string `json:"name"`
	Role      string `json:"role"`
	Color     string `json:"color"`
	Model     string `json:"model"`
	Effort    string `json:"effort"`
	Autonomy  string `json:"autonomy"`
	Directive string `json:"directive"`
}

func (s *Service) handleCreate(w http.ResponseWriter, r *http.Request) {
	body, ok := httpx.DecodeJSON[createAgentBody](w, r)
	if !ok {
		return
	}
	a, err := s.Create(r.Context(), r.PathValue("key"), CreateInput(body))
	if err != nil {
		s.writeError(w, err)
		return
	}
	httpx.WriteJSON(w, http.StatusCreated, toAgentBody(a))
}

type starterBody struct {
	Created []string `json:"created"`
}

func (s *Service) handleStarter(w http.ResponseWriter, r *http.Request) {
	created, err := s.Starter(r.Context(), r.PathValue("key"))
	if err != nil {
		s.writeError(w, err)
		return
	}
	if created == nil {
		created = []string{}
	}
	httpx.WriteJSON(w, http.StatusOK, starterBody{Created: created})
}

func (s *Service) handleGet(w http.ResponseWriter, r *http.Request) {
	a, err := s.Get(r.Context(), r.PathValue("id"))
	if err != nil {
		s.writeError(w, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, toAgentBody(a))
}

type patchAgentBody struct {
	Name                *string          `json:"name"`
	Role                *string          `json:"role"`
	Color               *string          `json:"color"`
	Model               *string          `json:"model"`
	Effort              *string          `json:"effort"`
	Autonomy            *string          `json:"autonomy"`
	Permissions         *permissionsBody `json:"permissions"`
	GitAuthorName       *string          `json:"git_author_name"`
	GitAuthorEmail      *string          `json:"git_author_email"`
	Enabled             *bool            `json:"enabled"`
	ConcurrencyCap      *int64           `json:"concurrency_cap"`
	DailyCapCents       OptInt           `json:"daily_cap_cents"`
	MaxWallClockSeconds *int64           `json:"max_wall_clock_seconds"`
	MaxSteps            *int64           `json:"max_steps"`
}

func (s *Service) handlePatch(w http.ResponseWriter, r *http.Request) {
	body, ok := httpx.DecodeJSON[patchAgentBody](w, r)
	if !ok {
		return
	}
	patch := UpdatePatch{
		Name: body.Name, Role: body.Role, Color: body.Color, Model: body.Model,
		Effort: body.Effort, Autonomy: body.Autonomy,
		GitAuthorName: body.GitAuthorName, GitAuthorEmail: body.GitAuthorEmail,
		Enabled:        body.Enabled,
		ConcurrencyCap: body.ConcurrencyCap, DailyCapCents: body.DailyCapCents,
		MaxWallClockSeconds: body.MaxWallClockSeconds, MaxSteps: body.MaxSteps,
	}
	if body.Permissions != nil {
		p := domain.AgentPermissions(*body.Permissions)
		patch.Permissions = &p
	}
	a, err := s.Update(r.Context(), r.PathValue("id"), patch)
	if err != nil {
		s.writeError(w, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, toAgentBody(a))
}

func (s *Service) handleArchive(w http.ResponseWriter, r *http.Request) {
	if err := s.Archive(r.Context(), r.PathValue("id")); err != nil {
		s.writeError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

type saveDirectiveBody struct {
	Body string `json:"body"`
	Note string `json:"note"`
}

// saveDirectiveResponse carries the (current or newly appended) version plus whether a new
// version was created — the UI's "Saved v3" vs "No changes" affordance.
type saveDirectiveResponse struct {
	directiveBody
	Created bool `json:"created"`
}

func (s *Service) handleSaveDirective(w http.ResponseWriter, r *http.Request) {
	body, ok := httpx.DecodeJSON[saveDirectiveBody](w, r)
	if !ok {
		return
	}
	d, created, err := s.SaveDirective(r.Context(), r.PathValue("id"), body.Body, body.Note)
	if err != nil {
		s.writeError(w, err)
		return
	}
	status := http.StatusOK
	if created {
		status = http.StatusCreated
	}
	httpx.WriteJSON(w, status, saveDirectiveResponse{directiveBody: toDirectiveBody(d), Created: created})
}

func (s *Service) handleListDirectives(w http.ResponseWriter, r *http.Request) {
	list, err := s.Directives(r.Context(), r.PathValue("id"))
	if err != nil {
		s.writeError(w, err)
		return
	}
	body := directiveListBody{Directives: make([]directiveBody, 0, len(list))}
	for _, d := range list {
		body.Directives = append(body.Directives, toDirectiveBody(d))
	}
	httpx.WriteJSON(w, http.StatusOK, body)
}

func (s *Service) handleDirectiveVersion(w http.ResponseWriter, r *http.Request) {
	version, err := strconv.ParseInt(r.PathValue("version"), 10, 64)
	if err != nil {
		httpx.WriteProblem(w, http.StatusBadRequest, httpx.TypeInvalidRequest,
			"Invalid version", "The version must be an integer.")
		return
	}
	d, err := s.DirectiveVersion(r.Context(), r.PathValue("id"), version)
	if err != nil {
		s.writeError(w, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, toDirectiveBody(d))
}

type estimateBody struct {
	Body string `json:"body"`
}

type estimateResponse struct {
	TokenEstimate int64 `json:"token_estimate"`
}

// handleEstimate is the live token counter behind the directive editor: the same documented
// chars/4 heuristic that stamps saved versions, computed server-side for an unsaved body.
func (s *Service) handleEstimate(w http.ResponseWriter, r *http.Request) {
	body, ok := httpx.DecodeJSON[estimateBody](w, r)
	if !ok {
		return
	}
	httpx.WriteJSON(w, http.StatusOK, estimateResponse{TokenEstimate: EstimateTokens(body.Body)})
}

// ---------------------------------------------------------------- errors -----

// writeError maps service errors to problems: field errors → 400 validation_failed; the name
// collision → 409 agent_name_taken with the field named; archived → 409 agent_archived.
func (s *Service) writeError(w http.ResponseWriter, err error) {
	var ve *ValidationError
	if errors.As(err, &ve) {
		httpx.WriteValidation(w, ve.Fields)
		return
	}
	var taken *NameTakenError
	if errors.As(err, &taken) {
		httpx.WriteProblemFields(w, http.StatusConflict, "agent_name_taken",
			"Agent name is taken",
			"An agent named "+taken.Name+" already exists in this project.",
			[]httpx.FieldError{{Field: "name",
				Message: "An agent named " + taken.Name + " already exists in this project."}})
		return
	}
	var arch *ArchivedError
	if errors.As(err, &arch) {
		httpx.WriteProblem(w, http.StatusConflict, "agent_archived",
			"Agent is archived",
			"Agent "+arch.Name+" is archived and can no longer be changed.")
		return
	}
	if errors.Is(err, store.ErrNotFound) {
		httpx.WriteProblem(w, http.StatusNotFound, httpx.TypeNotFound,
			"Not found", "Nothing matches this path.")
		return
	}
	s.logger.Error("agents: request failed", slog.String("error", err.Error()))
	httpx.WriteProblem(w, http.StatusInternalServerError, httpx.TypeInternal,
		"Internal error", "Something went wrong on the server. The error has been logged.")
}

// OptInt's JSON decoding lives here with the other wire plumbing.

// UnmarshalJSON records that the field appeared, and whether it was null.
func (o *OptInt) UnmarshalJSON(b []byte) error {
	o.Set = true
	if string(b) == "null" {
		o.Null = true
		return nil
	}
	return json.Unmarshal(b, &o.Value)
}
