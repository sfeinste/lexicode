package runs

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"github.com/spruce/lexicode/internal/domain"
	"github.com/spruce/lexicode/internal/kernel/audit"
	"github.com/spruce/lexicode/internal/kernel/auth"
	"github.com/spruce/lexicode/internal/kernel/httpx"
	"github.com/spruce/lexicode/internal/kernel/store"
)

// Routes registers the run endpoints (contracts §5). List routes carry the project key;
// per-run routes resolve membership through the run's project.
func (s *Service) Routes(mux httpx.Registrar, a *auth.Service) {
	member := func(h http.HandlerFunc) http.Handler {
		return a.RequireAuth(a.RequireProjectMember(h))
	}
	viaRun := func(h http.HandlerFunc) http.Handler {
		return a.RequireAuth(s.requireRunMember(a, h))
	}

	mux.Handle("GET /api/v1/projects/{key}/runs", member(s.handleList))
	mux.Handle("GET /api/v1/runs/{id}", viaRun(s.handleGet))
	mux.Handle("GET /api/v1/runs/{id}/activities", viaRun(s.handleActivities))
	mux.Handle("POST /api/v1/runs/{id}/messages", viaRun(s.handleSteer))
	mux.Handle("POST /api/v1/runs/{id}/stop", viaRun(s.handleStop))
	mux.Handle("POST /api/v1/runs/{id}/takeover", viaRun(s.handleTakeover))
}

// requireRunMember checks project membership through the run in the path. Must sit inside
// RequireAuth.
func (s *Service) requireRunMember(a *auth.Service, next http.HandlerFunc) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		u, ok := auth.UserFrom(r.Context())
		if !ok {
			httpx.WriteProblem(w, http.StatusUnauthorized, "unauthenticated",
				"Not authenticated", "Sign in to use this endpoint.")
			return
		}
		run, err := s.st.Runs().ByID(r.Context(), r.PathValue("id"))
		if errors.Is(err, store.ErrNotFound) {
			httpx.WriteProblem(w, http.StatusNotFound, httpx.TypeNotFound,
				"No such run", "No run matches this path.")
			return
		}
		if err != nil {
			s.writeError(w, err)
			return
		}
		p, err := s.st.Projects().ByID(r.Context(), run.ProjectID)
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

// runBody is how a run renders. hold_reason says, in words, which limit is holding a queued
// run (§10.2 — never a bare spinner).
type runBody struct {
	ID                 string  `json:"id"`
	Seq                int64   `json:"seq"`
	ProjectID          string  `json:"project_id"`
	AgentID            string  `json:"agent_id"`
	TicketID           *string `json:"ticket_id"`
	TriggerID          *string `json:"trigger_id"`
	State              string  `json:"state"`
	StateReason        string  `json:"state_reason"`
	HoldReason         string  `json:"hold_reason"`
	Autonomy           string  `json:"autonomy"`
	DirectiveVersionID *string `json:"directive_version_id"`
	Model              string  `json:"model"`
	Effort             string  `json:"effort"`
	Branch             *string `json:"branch"`
	SubjectKey         string  `json:"subject_key"`
	CurrentStep        string  `json:"current_step"`
	CostCents          int64   `json:"cost_cents"`
	TokensIn           int64   `json:"tokens_in"`
	TokensOut          int64   `json:"tokens_out"`
	TokensCacheRead    int64   `json:"tokens_cache_read"`
	TokensCacheWrite   int64   `json:"tokens_cache_write"`
	StepCount          int64   `json:"step_count"`
	ErrorMessage       string  `json:"error_message"`
	QueuedAt           string  `json:"queued_at"`
	StartedAt          *string `json:"started_at"`
	EndedAt            *string `json:"ended_at"`
}

func toRunBody(r domain.Run) runBody {
	return runBody{
		ID: r.ID, Seq: r.Seq, ProjectID: r.ProjectID, AgentID: r.AgentID,
		TicketID: r.TicketID, TriggerID: r.TriggerID,
		State: string(r.State), StateReason: r.StateReason, HoldReason: r.HoldReason,
		Autonomy: string(r.Autonomy), DirectiveVersionID: r.DirectiveVersionID,
		Model: r.Model, Effort: r.Effort, Branch: r.Branch, SubjectKey: r.SubjectKey,
		CurrentStep: r.CurrentStep, CostCents: r.CostCents,
		TokensIn: r.TokensIn, TokensOut: r.TokensOut,
		TokensCacheRead: r.TokensCacheRead, TokensCacheWrite: r.TokensCacheWrite,
		StepCount: r.StepCount, ErrorMessage: r.ErrorMessage,
		QueuedAt: r.QueuedAt, StartedAt: r.StartedAt, EndedAt: r.EndedAt,
	}
}

type runOutputBody struct {
	ID        string `json:"id"`
	Kind      string `json:"kind"`
	Ref       string `json:"ref"`
	URL       string `json:"url"`
	Summary   string `json:"summary"`
	CreatedAt string `json:"created_at"`
}

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

type activityBody struct {
	Seq        int64           `json:"seq"`
	Type       string          `json:"type"`
	Level      int64           `json:"level"`
	ToolName   string          `json:"tool_name"`
	GroupKey   string          `json:"group_key"`
	Title      string          `json:"title"`
	Payload    json.RawMessage `json:"payload"`
	OK         *bool           `json:"ok"`
	Attempt    int64           `json:"attempt"`
	DurationMS *int64          `json:"duration_ms"`
	CostCents  int64           `json:"cost_cents"`
	CreatedAt  string          `json:"created_at"`
}

// ---------------------------------------------------------------- handlers -----

// handleList is GET /projects/{key}/runs?status=&agent=&ticket=.
func (s *Service) handleList(w http.ResponseWriter, r *http.Request) {
	p, err := s.st.Projects().ByKey(r.Context(), r.PathValue("key"))
	if err != nil {
		s.writeError(w, err)
		return
	}
	var f store.RunFilter
	if v := r.URL.Query().Get("status"); v != "" {
		for _, part := range strings.Split(v, ",") {
			st := domain.RunState(strings.TrimSpace(part))
			if !st.IsValid() {
				httpx.WriteValidation(w, []httpx.FieldError{{Field: "status",
					Message: "Unknown run state: " + string(st)}})
				return
			}
			f.States = append(f.States, st)
		}
	}
	f.AgentID = r.URL.Query().Get("agent")
	f.TicketID = r.URL.Query().Get("ticket")
	runs, err := s.st.Runs().ForProjectFiltered(r.Context(), p.ID, f)
	if err != nil {
		s.writeError(w, err)
		return
	}
	out := make([]runBody, 0, len(runs))
	for _, run := range runs {
		out = append(out, toRunBody(run))
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"runs": out})
}

// handleGet is GET /runs/{id}: the run plus its outputs and context items.
func (s *Service) handleGet(w http.ResponseWriter, r *http.Request) {
	run, err := s.st.Runs().ByID(r.Context(), r.PathValue("id"))
	if err != nil {
		s.writeError(w, err)
		return
	}
	outputs, err := s.st.RunOutputs().ForRun(r.Context(), run.ID)
	if err != nil {
		s.writeError(w, err)
		return
	}
	items, err := s.st.RunContextItems().ForRun(r.Context(), run.ID)
	if err != nil {
		s.writeError(w, err)
		return
	}
	outBodies := make([]runOutputBody, 0, len(outputs))
	for _, o := range outputs {
		outBodies = append(outBodies, runOutputBody{
			ID: o.ID, Kind: string(o.Kind), Ref: o.Ref, URL: o.URL,
			Summary: o.Summary, CreatedAt: o.CreatedAt,
		})
	}
	itemBodies := make([]contextItemBody, 0, len(items))
	for _, it := range items {
		itemBodies = append(itemBodies, contextItemBody{
			Provider: it.Provider, SourceKind: it.SourceKind, SourceRef: it.SourceRef,
			Title: it.Title, Reason: it.Reason, Tokens: it.Tokens,
			Position: it.Position, Injected: it.Injected,
		})
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{
		"run": toRunBody(run), "outputs": outBodies, "context": itemBodies,
	})
}

// handleActivities is GET /runs/{id}/activities?since=&level=.
func (s *Service) handleActivities(w http.ResponseWriter, r *http.Request) {
	run, err := s.st.Runs().ByID(r.Context(), r.PathValue("id"))
	if err != nil {
		s.writeError(w, err)
		return
	}
	activities, err := s.st.Activities().ForRun(r.Context(), run.ID)
	if err != nil {
		s.writeError(w, err)
		return
	}
	out := make([]activityBody, 0, len(activities))
	for _, a := range activities {
		out = append(out, activityBody{
			Seq: a.Seq, Type: string(a.Type), Level: a.Level, ToolName: a.ToolName,
			GroupKey: a.GroupKey, Title: a.Title, Payload: a.Payload, OK: a.OK,
			Attempt: a.Attempt, DurationMS: a.DurationMS, CostCents: a.CostCents,
			CreatedAt: a.CreatedAt,
		})
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"activities": out})
}

// steerBody is a POST /runs/{id}/messages request.
type steerBody struct {
	Body string `json:"body"`
}

// handleSteer queues one steering message ("queue, don't interrupt"): a run_messages row,
// delivered by the supervisor between tool calls, delivered_at stamped when the adapter
// accepts it. Enabled from queued through provisioning and running (§10.3).
func (s *Service) handleSteer(w http.ResponseWriter, r *http.Request) {
	body, ok := httpx.DecodeJSON[steerBody](w, r)
	if !ok {
		return
	}
	if strings.TrimSpace(body.Body) == "" {
		httpx.WriteValidation(w, []httpx.FieldError{{Field: "body",
			Message: "A message is required."}})
		return
	}
	run, err := s.st.Runs().ByID(r.Context(), r.PathValue("id"))
	if err != nil {
		s.writeError(w, err)
		return
	}
	if run.State.Terminal() {
		httpx.WriteProblem(w, http.StatusConflict, "run_ended",
			"This run has ended", "A finished run cannot be steered.")
		return
	}
	m := domain.RunMessage{
		ID: domain.NewID(), RunID: run.ID, Body: body.Body,
		State: domain.MessageQueued, CreatedAt: s.now(),
	}
	if a, ok := auth.ActorFrom(r.Context()); ok && a.Kind == domain.ActorHuman {
		id := a.ID
		m.AuthorID = &id
	}
	if err := s.st.RunMessages().Create(r.Context(), &m); err != nil {
		s.writeError(w, err)
		return
	}
	if err := s.audit.Write(r.Context(), "run.steer",
		audit.Target{Kind: "run", ID: run.ID, ProjectID: run.ProjectID},
		nil, map[string]any{"message_id": m.ID}); err != nil {
		s.writeError(w, err)
		return
	}
	if s.sched != nil {
		s.sched.NotifySteering(run.ID)
	}
	httpx.WriteJSON(w, http.StatusCreated, map[string]any{
		"id": m.ID, "run_id": m.RunID, "body": m.Body, "state": string(m.State),
		"created_at": m.CreatedAt,
	})
}

// stopBody is a POST /runs/{id}/stop request.
type stopBody struct {
	Reason string `json:"reason"`
}

// handleStop is POST /runs/{id}/stop: terminal `canceled` with the reason recorded and the
// §10.5 artifact push preserved.
func (s *Service) handleStop(w http.ResponseWriter, r *http.Request) {
	body, ok := httpx.DecodeJSON[stopBody](w, r)
	if !ok {
		return
	}
	run, err := s.st.Runs().ByID(r.Context(), r.PathValue("id"))
	if err != nil {
		s.writeError(w, err)
		return
	}
	if run.State.Terminal() {
		httpx.WriteProblem(w, http.StatusConflict, "run_ended",
			"This run has ended", "A finished run cannot be stopped.")
		return
	}
	if s.sched == nil {
		httpx.WriteProblem(w, http.StatusServiceUnavailable, "scheduler_unavailable",
			"No scheduler", "The run scheduler is not running.")
		return
	}
	reason := strings.TrimSpace(body.Reason)
	if reason == "" {
		reason = "stopped by a human"
	}
	if err := s.audit.Write(r.Context(), "run.stop",
		audit.Target{Kind: "run", ID: run.ID, ProjectID: run.ProjectID, Note: reason},
		map[string]any{"state": string(run.State)}, nil); err != nil {
		s.writeError(w, err)
		return
	}
	if err := s.sched.StopRun(r.Context(), run.ID, reason); err != nil {
		s.writeError(w, err)
		return
	}
	after, err := s.st.Runs().ByID(r.Context(), run.ID)
	if err != nil {
		s.writeError(w, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"run": toRunBody(after)})
}

// handleTakeover is POST /runs/{id}/takeover: not before S24, and honest about it (the
// copy-paste checkout block and the takeover note UI are that story's).
func (s *Service) handleTakeover(w http.ResponseWriter, _ *http.Request) {
	httpx.WriteProblem(w, http.StatusNotImplemented, "not_implemented",
		"Take over is not available yet",
		"Take over arrives with story S24. Stop the run instead; its branch is preserved.")
}

// writeError maps store errors onto problems.
func (s *Service) writeError(w http.ResponseWriter, err error) {
	if errors.Is(err, store.ErrNotFound) {
		httpx.WriteProblem(w, http.StatusNotFound, httpx.TypeNotFound,
			"Not found", "Nothing matches this path.")
		return
	}
	httpx.WriteProblem(w, http.StatusInternalServerError, httpx.TypeInternal,
		"Something went wrong", "The server could not complete this request.")
}
