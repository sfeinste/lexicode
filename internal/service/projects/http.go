package projects

import (
	"errors"
	"log/slog"
	"net/http"

	"github.com/spruce/lexicode/internal/domain"
	"github.com/spruce/lexicode/internal/kernel/auth"
	"github.com/spruce/lexicode/internal/kernel/httpx"
	"github.com/spruce/lexicode/internal/kernel/store"
)

// Routes registers the S08 endpoints (contracts §5):
//
//	GET|POST /api/v1/projects              any signed-in user; POST makes the caller owner+member
//	GET|PATCH /api/v1/projects/{key}       project members (owners pass per RequireProjectMember)
//	GET /api/v1/projects/{key}/overview    project members
//	GET|PUT /api/v1/workspace/settings     workspace owner only (UI spec: /settings is owner-only)
//
// Project settings ride on GET|PATCH /projects/{key} — the route inventory has no separate
// settings path, and the inheritance triples are part of the project resource (`settings`).
func (s *Service) Routes(mux httpx.Registrar, a *auth.Service) {
	member := func(h http.HandlerFunc) http.Handler {
		return a.RequireAuth(a.RequireProjectMember(h))
	}
	mux.Handle("GET /api/v1/projects", a.RequireAuth(http.HandlerFunc(s.handleList)))
	mux.Handle("POST /api/v1/projects", a.RequireAuth(http.HandlerFunc(s.handleCreate)))
	mux.Handle("GET /api/v1/projects/{key}", member(s.handleGet))
	mux.Handle("PATCH /api/v1/projects/{key}", member(s.handlePatch))
	mux.Handle("GET /api/v1/projects/{key}/overview", member(s.handleOverview))
	mux.Handle("GET /api/v1/workspace/settings",
		a.RequireAuth(auth.RequireOwner(http.HandlerFunc(s.handleWorkspaceGet))))
	mux.Handle("PUT /api/v1/workspace/settings",
		a.RequireAuth(auth.RequireOwner(http.HandlerFunc(s.handleWorkspacePut))))
}

// ---------------------------------------------------------------- bodies -----

// inheritedInt is the wire shape of one inheritable setting: the effective value, whether it is
// inherited (project column is null), and the live workspace default — everything the UI's
// InheritedField needs without recomputing (S08).
type inheritedInt struct {
	Value          int64 `json:"value"`
	Inherited      bool  `json:"inherited"`
	WorkspaceValue int64 `json:"workspace_value"`
}

func resolveInt(override *int64, workspace int64) inheritedInt {
	if override == nil {
		return inheritedInt{Value: workspace, Inherited: true, WorkspaceValue: workspace}
	}
	return inheritedInt{Value: *override, Inherited: false, WorkspaceValue: workspace}
}

// settingsBody groups the project's inheritable settings.
type settingsBody struct {
	DailyBudgetCents       inheritedInt `json:"daily_budget_cents"`
	ContextThresholdTokens inheritedInt `json:"context_threshold_tokens"`
	VerificationDays       inheritedInt `json:"verification_days"`
}

// projectBody is how a project renders everywhere.
type projectBody struct {
	ID            string       `json:"id"`
	Key           string       `json:"key"`
	Name          string       `json:"name"`
	Description   string       `json:"description"`
	Color         string       `json:"color"`
	OwnerID       string       `json:"owner_id"`
	AgentGuidance string       `json:"agent_guidance"`
	ArchivedAt    *string      `json:"archived_at"`
	CreatedAt     string       `json:"created_at"`
	UpdatedAt     string       `json:"updated_at"`
	Settings      settingsBody `json:"settings"`
}

func toProjectBody(p domain.Project, ws domain.WorkspaceSettings) projectBody {
	return projectBody{
		ID: p.ID, Key: p.Key, Name: p.Name, Description: p.Description, Color: p.Color,
		OwnerID: p.OwnerID, AgentGuidance: p.AgentGuidance, ArchivedAt: p.ArchivedAt,
		CreatedAt: p.CreatedAt, UpdatedAt: p.UpdatedAt,
		Settings: settingsBody{
			DailyBudgetCents:       resolveInt(p.DailyBudgetCents, ws.DefaultDailyBudgetCents),
			ContextThresholdTokens: resolveInt(p.ContextThresholdTokens, ws.DefaultContextThresholdTokens),
			VerificationDays:       resolveInt(p.VerificationDays, ws.DefaultVerificationDays),
		},
	}
}

// statsBody is the Home-table counters for one project row (UI spec §5.1).
type statsBody struct {
	OpenTickets     int64  `json:"open_tickets"`
	RunningAgents   int64  `json:"running_agents"`
	NeedsYou        int64  `json:"needs_you"`
	SpendTodayCents int64  `json:"spend_today_cents"`
	LastActivity    string `json:"last_activity"`
}

type projectListItem struct {
	projectBody
	Stats statsBody `json:"stats"`
}

type projectListBody struct {
	Projects []projectListItem `json:"projects"`
}

// workspaceBody is GET/PUT /workspace/settings.
type workspaceBody struct {
	DefaultBranch                 string `json:"default_branch"`
	DefaultBranchTemplate         string `json:"default_branch_template"`
	DefaultNetworkPolicy          string `json:"default_network_policy"`
	DefaultDailyBudgetCents       int64  `json:"default_daily_budget_cents"`
	DefaultContextThresholdTokens int64  `json:"default_context_threshold_tokens"`
	DefaultVerificationDays       int64  `json:"default_verification_days"`
	MaxConcurrentContainers       int64  `json:"max_concurrent_containers"`
	PollIntervalSeconds           int64  `json:"poll_interval_seconds"`
	UpdatedAt                     string `json:"updated_at"`
}

func toWorkspaceBody(ws domain.WorkspaceSettings) workspaceBody {
	return workspaceBody{
		DefaultBranch: ws.DefaultBranch, DefaultBranchTemplate: ws.DefaultBranchTemplate,
		DefaultNetworkPolicy:          ws.DefaultNetworkPolicy,
		DefaultDailyBudgetCents:       ws.DefaultDailyBudgetCents,
		DefaultContextThresholdTokens: ws.DefaultContextThresholdTokens,
		DefaultVerificationDays:       ws.DefaultVerificationDays,
		MaxConcurrentContainers:       ws.MaxConcurrentContainers,
		PollIntervalSeconds:           ws.PollIntervalSeconds,
		UpdatedAt:                     ws.UpdatedAt,
	}
}

// overviewBody is GET /projects/{key}/overview — the About card. Repo is null until S15
// connects one; recent runs, pinned pages and the activity feed arrive with their stories.
type overviewBody struct {
	Project         projectBody `json:"project"`
	Owner           ownerBody   `json:"owner"`
	Repo            *repoBody   `json:"repo"`
	AgentCount      int64       `json:"agent_count"`
	OpenTickets     int64       `json:"open_tickets"`
	RunsToday       int64       `json:"runs_today"`
	SpendTodayCents int64       `json:"spend_today_cents"`
}

// repoBody is the About card's repo facts (S15): what §5.2 shows — repo, branch, last commit.
type repoBody struct {
	Provider      string  `json:"provider"`
	Owner         string  `json:"owner"`
	Name          string  `json:"name"`
	DefaultBranch *string `json:"default_branch"`
	HeadSHA       *string `json:"head_sha"`
	HeadMessage   *string `json:"head_message"`
}

type ownerBody struct {
	ID          string `json:"id"`
	DisplayName string `json:"display_name"`
	AvatarColor string `json:"avatar_color"`
}

// ---------------------------------------------------------------- handlers -----

func (s *Service) handleList(w http.ResponseWriter, r *http.Request) {
	includeArchived := r.URL.Query().Get("archived") == "1" ||
		r.URL.Query().Get("archived") == "true"
	list, err := s.List(r.Context(), includeArchived)
	if err != nil {
		s.writeError(w, err)
		return
	}
	ws, err := s.st.Workspace().Get(r.Context())
	if err != nil {
		s.writeError(w, err)
		return
	}
	body := projectListBody{Projects: make([]projectListItem, 0, len(list))}
	for _, it := range list {
		body.Projects = append(body.Projects, projectListItem{
			projectBody: toProjectBody(it.Project, ws),
			Stats: statsBody{
				OpenTickets: it.Stats.OpenTickets, RunningAgents: it.Stats.RunningAgents,
				NeedsYou: it.Stats.NeedsYou, SpendTodayCents: it.Stats.SpendTodayCents,
				LastActivity: it.Stats.LastActivity,
			},
		})
	}
	httpx.WriteJSON(w, http.StatusOK, body)
}

type createBody struct {
	Key         string `json:"key"`
	Name        string `json:"name"`
	Description string `json:"description"`
	Color       string `json:"color"`
}

func (s *Service) handleCreate(w http.ResponseWriter, r *http.Request) {
	body, ok := httpx.DecodeJSON[createBody](w, r)
	if !ok {
		return
	}
	u, _ := auth.UserFrom(r.Context())
	p, err := s.CreateProject(r.Context(), CreateInput{
		Key: body.Key, Name: body.Name, Description: body.Description, Color: body.Color,
		OwnerID: u.ID,
	})
	if err != nil {
		s.writeError(w, err)
		return
	}
	ws, err := s.st.Workspace().Get(r.Context())
	if err != nil {
		s.writeError(w, err)
		return
	}
	httpx.WriteJSON(w, http.StatusCreated, toProjectBody(p, ws))
}

func (s *Service) handleGet(w http.ResponseWriter, r *http.Request) {
	p, err := s.Get(r.Context(), r.PathValue("key"))
	if err != nil {
		s.writeError(w, err)
		return
	}
	ws, err := s.st.Workspace().Get(r.Context())
	if err != nil {
		s.writeError(w, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, toProjectBody(p, ws))
}

type patchBody struct {
	Name                   *string `json:"name"`
	Description            *string `json:"description"`
	Color                  *string `json:"color"`
	OwnerID                *string `json:"owner_id"`
	AgentGuidance          *string `json:"agent_guidance"`
	Archived               *bool   `json:"archived"`
	DailyBudgetCents       OptInt  `json:"daily_budget_cents"`
	ContextThresholdTokens OptInt  `json:"context_threshold_tokens"`
	VerificationDays       OptInt  `json:"verification_days"`
}

func (s *Service) handlePatch(w http.ResponseWriter, r *http.Request) {
	body, ok := httpx.DecodeJSON[patchBody](w, r)
	if !ok {
		return
	}
	p, err := s.UpdateProject(r.Context(), r.PathValue("key"), UpdatePatch(body))
	if err != nil {
		s.writeError(w, err)
		return
	}
	ws, err := s.st.Workspace().Get(r.Context())
	if err != nil {
		s.writeError(w, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, toProjectBody(p, ws))
}

func (s *Service) handleOverview(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	p, err := s.Get(ctx, r.PathValue("key"))
	if err != nil {
		s.writeError(w, err)
		return
	}
	ws, err := s.st.Workspace().Get(ctx)
	if err != nil {
		s.writeError(w, err)
		return
	}
	owner, err := s.st.Users().ByID(ctx, p.OwnerID)
	if err != nil {
		s.writeError(w, err)
		return
	}
	agentCount, err := s.st.Projects().AgentCount(ctx, p.ID)
	if err != nil {
		s.writeError(w, err)
		return
	}
	since := s.dayStartUTC()
	runsToday, err := s.st.Projects().RunsSince(ctx, p.ID, since)
	if err != nil {
		s.writeError(w, err)
		return
	}
	stats, err := s.st.Projects().Stats(ctx, since)
	if err != nil {
		s.writeError(w, err)
		return
	}
	var repo *repoBody
	switch rp, err := s.st.Repos().ByProject(ctx, p.ID); {
	case err == nil:
		repo = &repoBody{Provider: rp.Provider, Owner: rp.Owner, Name: rp.Name,
			DefaultBranch: rp.DefaultBranch, HeadSHA: rp.HeadSHA, HeadMessage: rp.HeadMessage}
	case !errors.Is(err, store.ErrNotFound):
		s.writeError(w, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, overviewBody{
		Project: toProjectBody(p, ws),
		Owner: ownerBody{ID: owner.ID, DisplayName: owner.DisplayName,
			AvatarColor: owner.AvatarColor},
		Repo:        repo,
		AgentCount:  agentCount,
		OpenTickets: stats[p.ID].OpenTickets,
		RunsToday:   runsToday, SpendTodayCents: stats[p.ID].SpendTodayCents,
	})
}

func (s *Service) handleWorkspaceGet(w http.ResponseWriter, r *http.Request) {
	ws, err := s.Workspace(r.Context())
	if err != nil {
		s.writeError(w, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, toWorkspaceBody(ws))
}

type workspacePutBody struct {
	DefaultBranch                 *string `json:"default_branch"`
	DefaultBranchTemplate         *string `json:"default_branch_template"`
	DefaultNetworkPolicy          *string `json:"default_network_policy"`
	DefaultDailyBudgetCents       *int64  `json:"default_daily_budget_cents"`
	DefaultContextThresholdTokens *int64  `json:"default_context_threshold_tokens"`
	DefaultVerificationDays       *int64  `json:"default_verification_days"`
	MaxConcurrentContainers       *int64  `json:"max_concurrent_containers"`
	PollIntervalSeconds           *int64  `json:"poll_interval_seconds"`
}

func (s *Service) handleWorkspacePut(w http.ResponseWriter, r *http.Request) {
	body, ok := httpx.DecodeJSON[workspacePutBody](w, r)
	if !ok {
		return
	}
	ws, err := s.UpdateWorkspace(r.Context(), WorkspacePatch(body))
	if err != nil {
		s.writeError(w, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, toWorkspaceBody(ws))
}

// writeError maps service errors to problems: field errors → 400 validation_failed, missing
// rows → 404, everything else → a logged 500.
func (s *Service) writeError(w http.ResponseWriter, err error) {
	var ve *ValidationError
	if errors.As(err, &ve) {
		httpx.WriteValidation(w, ve.Fields)
		return
	}
	if errors.Is(err, store.ErrNotFound) {
		httpx.WriteProblem(w, http.StatusNotFound, httpx.TypeNotFound,
			"Not found", "No project matches this path.")
		return
	}
	s.logger.Error("projects: request failed", slog.String("error", err.Error()))
	httpx.WriteProblem(w, http.StatusInternalServerError, httpx.TypeInternal,
		"Internal error", "Something went wrong on the server. The error has been logged.")
}
