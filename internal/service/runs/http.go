package runs

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/spruce/lexicode/internal/domain"
	"github.com/spruce/lexicode/internal/kernel/audit"
	"github.com/spruce/lexicode/internal/kernel/auth"
	"github.com/spruce/lexicode/internal/kernel/httpx"
	"github.com/spruce/lexicode/internal/kernel/sched"
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
	mux.Handle("POST /api/v1/projects/{key}/runs", member(s.handleCreate))
	mux.Handle("GET /api/v1/runs/{id}", viaRun(s.handleGet))
	mux.Handle("GET /api/v1/runs/{id}/activities", viaRun(s.handleActivities))
	mux.Handle("GET /api/v1/runs/{id}/chain", viaRun(s.handleChain))
	mux.Handle("POST /api/v1/runs/{id}/messages", viaRun(s.handleSteer))
	mux.Handle("POST /api/v1/runs/{id}/stop", viaRun(s.handleStop))
	mux.Handle("POST /api/v1/runs/{id}/takeover", viaRun(s.handleTakeover))
	mux.Handle("POST /api/v1/runs/{id}/acknowledge", viaRun(s.handleAcknowledge))
	mux.Handle("GET /api/v1/inbox", a.RequireAuth(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.handleInbox(w, r, a)
	})))
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
	TakeoverNote       string  `json:"takeover_note"`
	QueuedAt           string  `json:"queued_at"`
	StartedAt          *string `json:"started_at"`
	EndedAt            *string `json:"ended_at"`
	AcknowledgedAt     *string `json:"acknowledged_at"`
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
		StepCount: r.StepCount, ErrorMessage: r.ErrorMessage, TakeoverNote: r.TakeoverNote,
		QueuedAt: r.QueuedAt, StartedAt: r.StartedAt, EndedAt: r.EndedAt,
		AcknowledgedAt: r.AcknowledgedAt,
	}
}

// runMessageBody is one steering message: queued renders the "Applied after the current
// step." chip; delivered_at flips it.
type runMessageBody struct {
	ID          string  `json:"id"`
	RunID       string  `json:"run_id"`
	Body        string  `json:"body"`
	State       string  `json:"state"`
	CreatedAt   string  `json:"created_at"`
	DeliveredAt *string `json:"delivered_at"`
}

func toRunMessageBody(m domain.RunMessage) runMessageBody {
	return runMessageBody{
		ID: m.ID, RunID: m.RunID, Body: m.Body, State: string(m.State),
		CreatedAt: m.CreatedAt, DeliveredAt: m.DeliveredAt,
	}
}

// runElicitationBody mirrors the elicitation wire shape the respond endpoint uses, plus
// activity_seq so the timeline row and the respond surface stay one thing.
type runElicitationBody struct {
	ID          string          `json:"id"`
	RunID       string          `json:"run_id"`
	ActivitySeq int64           `json:"activity_seq"`
	Kind        string          `json:"kind"`
	State       string          `json:"state"`
	Request     json.RawMessage `json:"request"`
	Response    json.RawMessage `json:"response,omitempty"`
	RespondedBy *string         `json:"responded_by,omitempty"`
	RespondedAt *string         `json:"responded_at,omitempty"`
	CreatedAt   string          `json:"created_at"`
}

func toRunElicitationBody(el domain.Elicitation) runElicitationBody {
	return runElicitationBody{
		ID: el.ID, RunID: el.RunID, ActivitySeq: el.ActivitySeq, Kind: string(el.Kind),
		State: string(el.State), Request: el.Request, Response: el.Response,
		RespondedBy: el.RespondedBy, RespondedAt: el.RespondedAt, CreatedAt: el.CreatedAt,
	}
}

type runOutputBody struct {
	ID        string `json:"id"`
	Kind      string `json:"kind"`
	Ref       string `json:"ref"`
	URL       string `json:"url"`
	Summary   string `json:"summary"`
	CreatedAt string `json:"created_at"`
	// Additions/Deletions are live PR line counts joined from poll_pr_state for
	// kind=pull_request rows (S37 diff-size warning). Null until the poller's detail read
	// has seen the PR.
	Additions *int64 `json:"additions,omitempty"`
	Deletions *int64 `json:"deletions,omitempty"`
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
	Seq             int64           `json:"seq"`
	Type            string          `json:"type"`
	Level           int64           `json:"level"`
	ToolName        string          `json:"tool_name"`
	GroupKey        string          `json:"group_key"`
	Title           string          `json:"title"`
	Payload         json.RawMessage `json:"payload"`
	OK              *bool           `json:"ok"`
	Attempt         int64           `json:"attempt"`
	DurationMS      *int64          `json:"duration_ms"`
	QueuedMS        *int64          `json:"queued_ms"`
	ModelMS         *int64          `json:"model_ms"`
	ToolMS          *int64          `json:"tool_ms"`
	CostCents       int64           `json:"cost_cents"`
	TokensIn        int64           `json:"tokens_in"`
	TokensCacheRead int64           `json:"tokens_cache_read"`
	TokensOut       int64           `json:"tokens_out"`
	CreatedAt       string          `json:"created_at"`
}

// ---------------------------------------------------------------- handlers -----

// handleList is GET /projects/{key}/runs?status=&agent=&ticket=&view=. `view=needs_you`
// returns the board lane's rows — the same query the home strip and /inbox render, scoped
// to one project (architecture §12).
func (s *Service) handleList(w http.ResponseWriter, r *http.Request) {
	p, err := s.st.Projects().ByKey(r.Context(), r.PathValue("key"))
	if err != nil {
		s.writeError(w, err)
		return
	}
	if view := r.URL.Query().Get("view"); view != "" {
		if view != "needs_you" {
			httpx.WriteValidation(w, []httpx.FieldError{{Field: "view",
				Message: "Unknown view: " + view}})
			return
		}
		rows, err := s.NeedsYou(r.Context(), NeedsYouScope{ProjectID: p.ID})
		if err != nil {
			s.writeError(w, err)
			return
		}
		httpx.WriteJSON(w, http.StatusOK, map[string]any{"runs": rows})
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

// createRunBody is a POST /projects/{key}/runs request — the ⌘J ask-an-agent palette's
// free-floating run (D-15, S38): an agent and a prompt, no ticket. Everything else about
// the run (admission, prompt assembly, state) stays the scheduler's business (D-14).
type createRunBody struct {
	AgentID string `json:"agent_id"`
	Prompt  string `json:"prompt"`
}

// handleCreate is POST /projects/{key}/runs: request a free-floating run of one agent with
// a prompt. Returns 201 with the created run's id; the scheduler owns admission, so a 201
// means "queued", not "running".
func (s *Service) handleCreate(w http.ResponseWriter, r *http.Request) {
	p, err := s.st.Projects().ByKey(r.Context(), r.PathValue("key"))
	if err != nil {
		s.writeError(w, err)
		return
	}
	body, ok := httpx.DecodeJSON[createRunBody](w, r)
	if !ok {
		return
	}
	if body.AgentID == "" {
		httpx.WriteValidation(w, []httpx.FieldError{{Field: "agent_id",
			Message: "An agent is required."}})
		return
	}
	if strings.TrimSpace(body.Prompt) == "" {
		httpx.WriteValidation(w, []httpx.FieldError{{Field: "prompt",
			Message: "A prompt is required."}})
		return
	}
	a, err := s.st.Agents().ByID(r.Context(), body.AgentID)
	if errors.Is(err, store.ErrNotFound) {
		httpx.WriteValidation(w, []httpx.FieldError{{Field: "agent_id",
			Message: "No such agent in this project."}})
		return
	}
	if err != nil {
		s.writeError(w, err)
		return
	}
	if a.ProjectID != p.ID || a.ArchivedAt != nil {
		httpx.WriteValidation(w, []httpx.FieldError{{Field: "agent_id",
			Message: "No such agent in this project."}})
		return
	}
	if !a.Enabled {
		httpx.WriteValidation(w, []httpx.FieldError{{Field: "agent_id",
			Message: "This agent is disabled."}})
		return
	}
	if s.req == nil {
		httpx.WriteProblem(w, http.StatusServiceUnavailable, "scheduler_unavailable",
			"The scheduler is not running", "Runs cannot be requested right now.")
		return
	}
	req := sched.RunRequest{
		ProjectID:      p.ID,
		AgentID:        a.ID,
		Reason:         "ask an agent",
		PromptOverride: body.Prompt,
	}
	if actor, ok := auth.ActorFrom(r.Context()); ok && actor.Kind == domain.ActorHuman {
		req.RequestedByUserID = actor.ID
	}
	runID, reqErr := s.req.RequestRun(r.Context(), req)
	note := "run requested"
	if reqErr != nil {
		note = "scheduler refused: " + reqErr.Error()
	}
	if aerr := s.audit.Write(r.Context(), "run.request",
		audit.Target{Kind: "agent", ID: a.ID, ProjectID: p.ID, Note: note},
		nil, req); aerr != nil {
		s.writeError(w, aerr)
		return
	}
	if reqErr != nil {
		httpx.WriteProblem(w, http.StatusConflict, "run_refused",
			"The run was refused", reqErr.Error())
		return
	}
	httpx.WriteJSON(w, http.StatusCreated, map[string]any{"run_id": runID})
}

// handleGet is GET /runs/{id}: the run plus its outputs, context items, steering messages
// (queued chips flip to delivered live) and elicitations (what the S24 respond surfaces
// answer, correlated to timeline rows by activity_seq).
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
	messages, err := s.st.RunMessages().ForRun(r.Context(), run.ID)
	if err != nil {
		s.writeError(w, err)
		return
	}
	elicitations, err := s.st.Elicitations().ForRun(r.Context(), run.ID)
	if err != nil {
		s.writeError(w, err)
		return
	}
	// PR sizes for the diff-size warning: join pull_request outputs (ref = the PR number)
	// against the poller's live per-PR state.
	var prState map[int64]domain.PollPRState
	for _, o := range outputs {
		if o.Kind != domain.OutputPullRequest {
			continue
		}
		if prState == nil {
			if prState, err = s.st.PollPRState().ForProject(r.Context(), run.ProjectID); err != nil {
				s.writeError(w, err)
				return
			}
		}
	}
	outBodies := make([]runOutputBody, 0, len(outputs))
	for _, o := range outputs {
		ob := runOutputBody{
			ID: o.ID, Kind: string(o.Kind), Ref: o.Ref, URL: o.URL,
			Summary: o.Summary, CreatedAt: o.CreatedAt,
		}
		if o.Kind == domain.OutputPullRequest {
			if n, perr := strconv.ParseInt(o.Ref, 10, 64); perr == nil {
				if st, ok := prState[n]; ok {
					ob.Additions, ob.Deletions = st.Additions, st.Deletions
				}
			}
		}
		outBodies = append(outBodies, ob)
	}
	itemBodies := make([]contextItemBody, 0, len(items))
	for _, it := range items {
		itemBodies = append(itemBodies, contextItemBody{
			Provider: it.Provider, SourceKind: it.SourceKind, SourceRef: it.SourceRef,
			Title: it.Title, Reason: it.Reason, Tokens: it.Tokens,
			Position: it.Position, Injected: it.Injected,
		})
	}
	messageBodies := make([]runMessageBody, 0, len(messages))
	for _, m := range messages {
		messageBodies = append(messageBodies, toRunMessageBody(m))
	}
	elicitationBodies := make([]runElicitationBody, 0, len(elicitations))
	for _, el := range elicitations {
		elicitationBodies = append(elicitationBodies, toRunElicitationBody(el))
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{
		"run": toRunBody(run), "outputs": outBodies, "context": itemBodies,
		"messages": messageBodies, "elicitations": elicitationBodies,
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
			Attempt: a.Attempt, DurationMS: a.DurationMS,
			QueuedMS: a.QueuedMS, ModelMS: a.ModelMS, ToolMS: a.ToolMS,
			CostCents: a.CostCents, TokensIn: a.TokensIn, TokensOut: a.TokensOut,
			TokensCacheRead: a.TokensCacheRead,
			CreatedAt:       a.CreatedAt,
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

// takeoverBody is a POST /runs/{id}/takeover request.
type takeoverBody struct {
	Note string `json:"note"`
}

// handleTakeover is POST /runs/{id}/takeover (§10.7): stop with reason `takeover` (artifact
// push preserved), store the note on the run — it is injected into the next run on the same
// ticket — and return the copy-paste checkout block.
func (s *Service) handleTakeover(w http.ResponseWriter, r *http.Request) {
	body, ok := httpx.DecodeJSON[takeoverBody](w, r)
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
			"This run has ended", "A finished run cannot be taken over.")
		return
	}
	if s.sched == nil {
		httpx.WriteProblem(w, http.StatusServiceUnavailable, "scheduler_unavailable",
			"No scheduler", "The run scheduler is not running.")
		return
	}
	if err := s.audit.Write(r.Context(), "run.takeover",
		audit.Target{Kind: "run", ID: run.ID, ProjectID: run.ProjectID, Note: body.Note},
		map[string]any{"state": string(run.State)}, nil); err != nil {
		s.writeError(w, err)
		return
	}
	if err := s.sched.TakeoverRun(r.Context(), run.ID, body.Note); err != nil {
		s.writeError(w, err)
		return
	}
	after, err := s.st.Runs().ByID(r.Context(), run.ID)
	if err != nil {
		s.writeError(w, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{
		"run":      toRunBody(after),
		"checkout": checkoutBlock(after.Branch),
	})
}

// checkoutBlock is the §10.7 copy-paste command. Empty when the run never got a branch
// (stopped while queued).
func checkoutBlock(branch *string) string {
	if branch == nil || *branch == "" {
		return ""
	}
	return "git fetch origin && git checkout " + *branch
}

// handleAcknowledge is POST /runs/{id}/acknowledge: dismiss a terminal run from the
// needs-you surfaces. 409 while the run is still live — a live run needs answering, not
// dismissing.
func (s *Service) handleAcknowledge(w http.ResponseWriter, r *http.Request) {
	run, err := s.st.Runs().ByID(r.Context(), r.PathValue("id"))
	if err != nil {
		s.writeError(w, err)
		return
	}
	if !run.State.Terminal() {
		httpx.WriteProblem(w, http.StatusConflict, "run_not_ended",
			"This run is still live", "Only a finished run can be acknowledged.")
		return
	}
	if err := s.st.Runs().Acknowledge(r.Context(), run.ID, s.now()); err != nil {
		s.writeError(w, err)
		return
	}
	if err := s.audit.Write(r.Context(), "run.acknowledge",
		audit.Target{Kind: "run", ID: run.ID, ProjectID: run.ProjectID},
		nil, nil); err != nil {
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

// ---------------------------------------------------------------- needs-you -----

// handleInbox is GET /inbox: the workspace-wide needs-you rows — blocked runs, pending
// wiki proposals, open agent PRs — restricted to projects the user is a member of. The
// home strip, the left rail and /inbox render this one query (architecture §12); the
// project-scoped board lane is the same method with a ProjectID scope (handleList).
func (s *Service) handleInbox(w http.ResponseWriter, r *http.Request, a *auth.Service) {
	u, ok := auth.UserFrom(r.Context())
	if !ok {
		httpx.WriteProblem(w, http.StatusUnauthorized, "unauthenticated",
			"Not authenticated", "Sign in to use this endpoint.")
		return
	}
	memberOf := map[string]bool{}
	rows, err := s.NeedsYou(r.Context(), NeedsYouScope{
		Visible: func(projectID string) (bool, error) {
			allowed, seen := memberOf[projectID]
			if !seen {
				p, err := s.st.Projects().ByID(r.Context(), projectID)
				if err != nil {
					memberOf[projectID] = false
					return false, nil // a vanished project hides its rows, it does not 500
				}
				allowed, err = a.IsProjectMember(r.Context(), u, p)
				if err != nil {
					return false, err
				}
				memberOf[projectID] = allowed
			}
			return allowed, nil
		},
	})
	if err != nil {
		s.writeError(w, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"runs": rows})
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
