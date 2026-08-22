package triggers

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

// Routes registers the S26 endpoints (contracts §5):
//
//	GET|POST /api/v1/projects/{key}/triggers      project members
//	GET|PATCH|DELETE /api/v1/triggers/{id}        members, resolved via the trigger
//	GET /api/v1/triggers/{id}/firings
//	GET /api/v1/projects/{key}/trigger-catalog    the merged editor catalog (S29, catalog.go)
//
// POST /triggers/{id}/backtest belongs to S30 and is not served yet.
func (s *Service) Routes(mux httpx.Registrar, a *auth.Service) {
	member := func(h http.HandlerFunc) http.Handler {
		return a.RequireAuth(a.RequireProjectMember(h))
	}
	viaTrigger := func(h http.HandlerFunc) http.Handler {
		return a.RequireAuth(s.requireMember(a, h))
	}

	mux.Handle("GET /api/v1/projects/{key}/triggers", member(s.handleList))
	mux.Handle("POST /api/v1/projects/{key}/triggers", member(s.handleCreate))
	mux.Handle("GET /api/v1/triggers/{id}", viaTrigger(s.handleGet))
	mux.Handle("PATCH /api/v1/triggers/{id}", viaTrigger(s.handlePatch))
	mux.Handle("DELETE /api/v1/triggers/{id}", viaTrigger(s.handleDelete))
	mux.Handle("GET /api/v1/triggers/{id}/firings", viaTrigger(s.handleFirings))
	mux.Handle("GET /api/v1/projects/{key}/trigger-catalog", member(s.handleCatalog))
}

// requireMember is RequireProjectMember for routes whose path carries no project key: the
// owning project is resolved through the trigger. Must sit inside RequireAuth.
func (s *Service) requireMember(a *auth.Service, next http.HandlerFunc) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		u, ok := auth.UserFrom(r.Context())
		if !ok {
			httpx.WriteProblem(w, http.StatusUnauthorized, "unauthenticated",
				"Not authenticated", "Sign in to use this endpoint.")
			return
		}
		tr, err := s.st.Triggers().ByID(r.Context(), r.PathValue("id"))
		if errors.Is(err, store.ErrNotFound) {
			httpx.WriteProblem(w, http.StatusNotFound, httpx.TypeNotFound,
				"Not found", "Nothing matches this path.")
			return
		}
		if err != nil {
			s.writeError(w, err)
			return
		}
		p, err := s.st.Projects().ByID(r.Context(), tr.ProjectID)
		if err != nil {
			s.writeError(w, err)
			return
		}
		isMember, err := a.IsProjectMember(r.Context(), u, p)
		if err != nil {
			s.writeError(w, err)
			return
		}
		if !isMember {
			httpx.WriteProblem(w, http.StatusForbidden, "forbidden",
				"Not a project member", "You are not a member of this project.")
			return
		}
		next(w, r)
	})
}

// ---------------------------------------------------------------- bodies -----

// triggerBody is how a trigger renders everywhere. The JSON columns pass through raw — their
// shapes are the data model's, already validated on the way in.
type triggerBody struct {
	ID            string          `json:"id"`
	ProjectID     string          `json:"project_id"`
	Name          string          `json:"name"`
	Enabled       bool            `json:"enabled"`
	SourceID      string          `json:"source_id"`
	Event         string          `json:"event"`
	ActivityTypes json.RawMessage `json:"activity_types"`
	Filters       json.RawMessage `json:"filters"`
	Conditions    json.RawMessage `json:"conditions"`
	Actions       json.RawMessage `json:"actions"`
	LoopConfig    json.RawMessage `json:"loop_config"`
	Cron          *string         `json:"cron"`
	CreatedBy     *string         `json:"created_by"`
	CreatedAt     string          `json:"created_at"`
	UpdatedAt     string          `json:"updated_at"`
	Health        *healthBody     `json:"health,omitempty"`
	// ActionSummaries is the THEN line of the prose card, one Describe() sentence per
	// action; present on the health-carrying reads (list/get).
	ActionSummaries []string `json:"action_summaries,omitempty"`
}

// healthBody is the rule-health aggregate: per-outcome counts over the last 50 firings —
// never collapsed to success/failure (data model §6) — plus the recent outcome sequence
// (oldest first) the card sparkline renders.
type healthBody struct {
	Counts      map[string]int64 `json:"counts"`
	LastFiredAt *string          `json:"last_fired_at"`
	Recent      []string         `json:"recent"`
}

// firingBody is one trigger_firings row, with a summary of the event that caused it so the
// S29 history list can say what happened without a per-row fetch.
type firingBody struct {
	ID              string           `json:"id"`
	TriggerID       string           `json:"trigger_id"`
	EventID         string           `json:"event_id"`
	Outcome         string           `json:"outcome"`
	Reason          string           `json:"reason"`
	RunID           *string          `json:"run_id"`
	AbsorbedByRunID *string          `json:"absorbed_by_run_id"`
	Warnings        json.RawMessage  `json:"warnings"`
	CreatedAt       string           `json:"created_at"`
	Event           *firingEventBody `json:"event,omitempty"`
}

// firingEventBody summarizes the causing event: kind, activity, actor and the guard-style
// subject ("pr:219").
type firingEventBody struct {
	Kind         string  `json:"kind"`
	ActivityType string  `json:"activity_type"`
	ActorKind    string  `json:"actor_kind"`
	ActorLogin   *string `json:"actor_login"`
	Subject      string  `json:"subject"`
	OccurredAt   string  `json:"occurred_at"`
}

func toTriggerBody(tr domain.Trigger) triggerBody {
	return triggerBody{
		ID: tr.ID, ProjectID: tr.ProjectID, Name: tr.Name, Enabled: tr.Enabled,
		SourceID: tr.SourceID, Event: tr.Event,
		ActivityTypes: tr.ActivityTypes, Filters: tr.Filters,
		Conditions: tr.Conditions, Actions: tr.Actions, LoopConfig: tr.LoopConfig,
		Cron: tr.Cron, CreatedBy: tr.CreatedBy,
		CreatedAt: tr.CreatedAt, UpdatedAt: tr.UpdatedAt,
	}
}

func toBodyWithHealth(wh WithHealth) triggerBody {
	b := toTriggerBody(wh.Trigger)
	h := healthBody{Counts: map[string]int64{}, Recent: []string{}}
	for outcome, n := range wh.Health.Counts {
		h.Counts[string(outcome)] = n
	}
	if wh.Health.LastFiredAt != "" {
		last := wh.Health.LastFiredAt
		h.LastFiredAt = &last
	}
	for _, o := range wh.Recent {
		h.Recent = append(h.Recent, string(o))
	}
	b.Health = &h
	if wh.ActionSummaries != nil {
		b.ActionSummaries = wh.ActionSummaries
	} else {
		b.ActionSummaries = []string{}
	}
	return b
}

func toFiringBody(f domain.TriggerFiring) firingBody {
	return firingBody{
		ID: f.ID, TriggerID: f.TriggerID, EventID: f.EventID,
		Outcome: string(f.Outcome), Reason: f.Reason,
		RunID: f.RunID, AbsorbedByRunID: f.AbsorbedByRunID,
		Warnings: f.Warnings, CreatedAt: f.CreatedAt,
	}
}

// ---------------------------------------------------------------- handlers -----

func (s *Service) handleList(w http.ResponseWriter, r *http.Request) {
	list, err := s.List(r.Context(), r.PathValue("key"))
	if err != nil {
		s.writeError(w, err)
		return
	}
	out := make([]triggerBody, 0, len(list))
	for _, wh := range list {
		out = append(out, toBodyWithHealth(wh))
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"triggers": out})
}

func (s *Service) handleCreate(w http.ResponseWriter, r *http.Request) {
	in, ok := httpx.DecodeJSON[Input](w, r)
	if !ok {
		return
	}
	var userID string
	if u, ok := auth.UserFrom(r.Context()); ok {
		userID = u.ID
	}
	tr, err := s.Create(r.Context(), r.PathValue("key"), in, userID)
	if err != nil {
		s.writeError(w, err)
		return
	}
	httpx.WriteJSON(w, http.StatusCreated, toTriggerBody(tr))
}

func (s *Service) handleGet(w http.ResponseWriter, r *http.Request) {
	wh, err := s.Get(r.Context(), r.PathValue("id"))
	if err != nil {
		s.writeError(w, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, toBodyWithHealth(wh))
}

func (s *Service) handlePatch(w http.ResponseWriter, r *http.Request) {
	in, ok := httpx.DecodeJSON[Input](w, r)
	if !ok {
		return
	}
	tr, err := s.Update(r.Context(), r.PathValue("id"), in)
	if err != nil {
		s.writeError(w, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, toTriggerBody(tr))
}

func (s *Service) handleDelete(w http.ResponseWriter, r *http.Request) {
	if err := s.Delete(r.Context(), r.PathValue("id")); err != nil {
		s.writeError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Service) handleFirings(w http.ResponseWriter, r *http.Request) {
	limit := 0
	if q := r.URL.Query().Get("limit"); q != "" {
		n, err := strconv.Atoi(q)
		if err != nil || n < 1 || n > 500 {
			httpx.WriteProblem(w, http.StatusBadRequest, httpx.TypeInvalidRequest,
				"Invalid limit", "limit must be an integer between 1 and 500.")
			return
		}
		limit = n
	}
	firings, err := s.Firings(r.Context(), r.PathValue("id"), limit)
	if err != nil {
		s.writeError(w, err)
		return
	}
	out := make([]firingBody, 0, len(firings))
	for _, f := range firings {
		b := toFiringBody(f)
		if ev, err := s.st.Events().ByID(r.Context(), f.EventID); err == nil {
			b.Event = &firingEventBody{
				Kind: ev.Kind, ActivityType: ev.ActivityType,
				ActorKind: string(ev.ActorKind), ActorLogin: ev.ActorLogin,
				Subject: eventSubject(ev), OccurredAt: ev.OccurredAt,
			}
		}
		out = append(out, b)
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"firings": out})
}

// eventSubject renders the event's subject columns the way the guard keys read
// ("pr:219" / "ticket:PAY-14" / "repo") — the same rendering the run chain uses.
func eventSubject(ev domain.Event) string {
	switch {
	case ev.SubjectKind == "" || ev.SubjectKind == "repo":
		return "repo"
	case ev.SubjectNumber != nil:
		return ev.SubjectKind + ":" + strconv.FormatInt(*ev.SubjectNumber, 10)
	case ev.SubjectID != nil:
		return ev.SubjectKind + ":" + *ev.SubjectID
	default:
		return ev.SubjectKind
	}
}

func (s *Service) writeError(w http.ResponseWriter, err error) {
	var ve *ValidationError
	if errors.As(err, &ve) {
		httpx.WriteValidation(w, ve.Fields)
		return
	}
	var inUse *InUseError
	if errors.As(err, &inUse) {
		httpx.WriteProblem(w, http.StatusConflict, "trigger_in_use",
			"Trigger is referenced", inUse.Error()+".")
		return
	}
	if errors.Is(err, store.ErrNotFound) {
		httpx.WriteProblem(w, http.StatusNotFound, httpx.TypeNotFound,
			"Not found", "Nothing matches this path.")
		return
	}
	s.logger.Error("triggers: request failed", slog.String("error", err.Error()))
	httpx.WriteProblem(w, http.StatusInternalServerError, httpx.TypeInternal,
		"Internal error", "Something went wrong on the server. The error has been logged.")
}
