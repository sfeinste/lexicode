package runs

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"sort"
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
	QueuedMS   *int64          `json:"queued_ms"`
	ModelMS    *int64          `json:"model_ms"`
	ToolMS     *int64          `json:"tool_ms"`
	CostCents  int64           `json:"cost_cents"`
	TokensIn   int64           `json:"tokens_in"`
	TokensOut  int64           `json:"tokens_out"`
	CreatedAt  string          `json:"created_at"`
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
		runs, err := s.st.Runs().NeedsYou(r.Context(), p.ID)
		if err != nil {
			s.writeError(w, err)
			return
		}
		proposals, err := s.st.Wiki().Proposals(r.Context(), p.ID)
		if err != nil {
			s.writeError(w, err)
			return
		}
		rows, err := s.needsYouRows(r.Context(), runs, proposals)
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

// needsYouBody is one row of the needs-you surfaces (architecture §12): the flavor is the
// §4.3 vocabulary, and every renderer prints it in words (interaction rule 1).
//
// `kind` discriminates the row's subject (S35): "run" rows are blocked runs (`id` is a run
// id, the row links to the run), "wiki_proposal" rows are pending agent proposals awaiting
// review (`id` is the proposed page's id, `page_slug`/`page_title` name it, and the row
// links to the wiki page). S36's full inbox adds the remaining review subjects (open agent
// PRs) on this same shape.
type needsYouBody struct {
	Kind        string  `json:"kind"`
	ID          string  `json:"id"`
	ProjectKey  string  `json:"project_key"`
	TicketID    *string `json:"ticket_id"`
	TicketKey   *string `json:"ticket_key"`
	TicketTitle *string `json:"ticket_title"`
	Agent       string  `json:"agent"`
	Flavor      string  `json:"flavor"`
	Status      string  `json:"status"`
	StartedAt   string  `json:"started_at"`
	PageSlug    string  `json:"page_slug,omitempty"`
	PageTitle   string  `json:"page_title,omitempty"`
}

// needsYouFlavor is the §4.3 flavor computation, one place: parked runs are a question or
// an approval by state; unacknowledged terminal failures are a failure. The fourth flavor,
// review, is the wiki-proposal rows' (S35); S36 extends it to the remaining outputs
// awaiting a human.
func needsYouFlavor(state domain.RunState) string {
	switch state {
	case domain.RunNeedsInput:
		return "question"
	case domain.RunAwaitingApproval:
		return "approval"
	default:
		return "failure"
	}
}

// flavorRank is the home-strip sort (UI spec §5.1): answer a question first, then approve,
// then failed, then review.
func flavorRank(flavor string) int {
	switch flavor {
	case "question":
		return 0
	case "approval":
		return 1
	case "failure":
		return 2
	default:
		return 3
	}
}

// needsYouRows joins runs with their agent and ticket names, appends the pending
// wiki-proposal rows (flavor review, S35), and sorts by flavor rank, then oldest first
// (the longest-blocked row surfaces first).
func (s *Service) needsYouRows(ctx context.Context, runs []domain.Run, proposals []domain.WikiPage) ([]needsYouBody, error) {
	agents := map[string]string{}
	projects := map[string]string{}
	out := make([]needsYouBody, 0, len(runs)+len(proposals))
	for _, run := range runs {
		name, ok := agents[run.AgentID]
		if !ok {
			if a, err := s.st.Agents().ByID(ctx, run.AgentID); err == nil {
				name = a.Name
			} else {
				name = "agent"
			}
			agents[run.AgentID] = name
		}
		key, ok := projects[run.ProjectID]
		if !ok {
			if p, err := s.st.Projects().ByID(ctx, run.ProjectID); err == nil {
				key = p.Key
			}
			projects[run.ProjectID] = key
		}
		row := needsYouBody{
			Kind:       "run",
			ID:         run.ID,
			ProjectKey: key,
			TicketID:   run.TicketID,
			Agent:      name,
			Flavor:     needsYouFlavor(run.State),
			Status:     string(run.State),
			StartedAt:  run.QueuedAt,
		}
		if run.StartedAt != nil {
			row.StartedAt = *run.StartedAt
		}
		if run.TicketID != nil {
			if tk, err := s.st.Tickets().ByID(ctx, *run.TicketID); err == nil {
				k, t := tk.Key, tk.Title
				row.TicketKey, row.TicketTitle = &k, &t
			}
		}
		out = append(out, row)
	}
	for _, page := range proposals {
		key, ok := projects[page.ProjectID]
		if !ok {
			if p, err := s.st.Projects().ByID(ctx, page.ProjectID); err == nil {
				key = p.Key
			}
			projects[page.ProjectID] = key
		}
		// The proposing agent's name, through the proposing run — "agent" when either link
		// is gone (a proposal outlives nothing, but degrade rather than 500).
		name := "agent"
		if page.ProposedByRunID != nil {
			if run, err := s.st.Runs().ByID(ctx, *page.ProposedByRunID); err == nil {
				if cached, ok := agents[run.AgentID]; ok {
					name = cached
				} else if a, err := s.st.Agents().ByID(ctx, run.AgentID); err == nil {
					name = a.Name
					agents[run.AgentID] = name
				}
			}
		}
		out = append(out, needsYouBody{
			Kind:       "wiki_proposal",
			ID:         page.ID,
			ProjectKey: key,
			Agent:      name,
			Flavor:     "review",
			Status:     string(page.State),
			StartedAt:  page.CreatedAt,
			PageSlug:   page.Slug,
			PageTitle:  page.Title,
		})
	}
	sort.SliceStable(out, func(i, j int) bool {
		return flavorRank(out[i].Flavor) < flavorRank(out[j].Flavor)
	})
	return out, nil
}

// handleInbox is GET /inbox: the workspace-wide needs-you rows, restricted to projects the
// user is a member of — the home strip, the left rail and /inbox render this one query
// (architecture §12; the S36 inbox adds outputs awaiting review on top).
func (s *Service) handleInbox(w http.ResponseWriter, r *http.Request, a *auth.Service) {
	u, ok := auth.UserFrom(r.Context())
	if !ok {
		httpx.WriteProblem(w, http.StatusUnauthorized, "unauthenticated",
			"Not authenticated", "Sign in to use this endpoint.")
		return
	}
	runs, err := s.st.Runs().NeedsYouAll(r.Context())
	if err != nil {
		s.writeError(w, err)
		return
	}
	memberOf := map[string]bool{}
	member := func(projectID string) (bool, error) {
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
	}
	visible := runs[:0]
	for _, run := range runs {
		allowed, err := member(run.ProjectID)
		if err != nil {
			s.writeError(w, err)
			return
		}
		if allowed {
			visible = append(visible, run)
		}
	}
	proposals, err := s.st.Wiki().Proposals(r.Context(), "")
	if err != nil {
		s.writeError(w, err)
		return
	}
	visibleProposals := proposals[:0]
	for _, page := range proposals {
		allowed, err := member(page.ProjectID)
		if err != nil {
			s.writeError(w, err)
			return
		}
		if allowed {
			visibleProposals = append(visibleProposals, page)
		}
	}
	rows, err := s.needsYouRows(r.Context(), visible, visibleProposals)
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
