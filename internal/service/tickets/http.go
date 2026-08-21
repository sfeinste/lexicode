package tickets

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

// Routes registers the S10 endpoints (contracts §5, plus the label routes the contract's
// route inventory leaves to the openapi):
//
//	GET|POST /api/v1/projects/{key}/tickets       project members
//	GET|POST /api/v1/projects/{key}/labels        project members
//	GET|PATCH|DELETE /api/v1/tickets/{id}         members, resolved via the ticket
//	POST /api/v1/tickets/{id}/unarchive
//	POST /api/v1/tickets/{id}/move
//	POST /api/v1/tickets/{id}/subtickets
//	GET  /api/v1/tickets/{id}/stream
//	POST /api/v1/tickets/{id}/criteria
//	PUT|DELETE /api/v1/tickets/{id}/labels/{label_id}
//	PATCH|DELETE /api/v1/criteria/{id}            members, resolved via the criterion's ticket
//	PATCH|DELETE /api/v1/labels/{id}              members, resolved via the label
func (s *Service) Routes(mux httpx.Registrar, a *auth.Service) {
	member := func(h http.HandlerFunc) http.Handler {
		return a.RequireAuth(a.RequireProjectMember(h))
	}
	viaTicket := func(h http.HandlerFunc) http.Handler {
		return a.RequireAuth(s.requireMember(a, s.projectOfTicket, h))
	}
	viaCriterion := func(h http.HandlerFunc) http.Handler {
		return a.RequireAuth(s.requireMember(a, s.projectOfCriterion, h))
	}
	viaLabel := func(h http.HandlerFunc) http.Handler {
		return a.RequireAuth(s.requireMember(a, s.projectOfLabel, h))
	}

	mux.Handle("GET /api/v1/projects/{key}/tickets", member(s.handleList))
	mux.Handle("POST /api/v1/projects/{key}/tickets", member(s.handleCreate))
	mux.Handle("GET /api/v1/projects/{key}/labels", member(s.handleListLabels))
	mux.Handle("POST /api/v1/projects/{key}/labels", member(s.handleCreateLabel))

	mux.Handle("GET /api/v1/tickets/{id}", viaTicket(s.handleGet))
	mux.Handle("PATCH /api/v1/tickets/{id}", viaTicket(s.handlePatch))
	mux.Handle("DELETE /api/v1/tickets/{id}", viaTicket(s.handleArchive))
	mux.Handle("POST /api/v1/tickets/{id}/unarchive", viaTicket(s.handleUnarchive))
	mux.Handle("POST /api/v1/tickets/{id}/move", viaTicket(s.handleMove))
	mux.Handle("POST /api/v1/tickets/{id}/subtickets", viaTicket(s.handleSubtickets))
	mux.Handle("GET /api/v1/tickets/{id}/stream", viaTicket(s.handleStream))
	mux.Handle("POST /api/v1/tickets/{id}/criteria", viaTicket(s.handleAddCriterion))
	mux.Handle("PUT /api/v1/tickets/{id}/labels/{label_id}", viaTicket(s.handleAttachLabel))
	mux.Handle("DELETE /api/v1/tickets/{id}/labels/{label_id}", viaTicket(s.handleDetachLabel))

	mux.Handle("PATCH /api/v1/criteria/{id}", viaCriterion(s.handlePatchCriterion))
	mux.Handle("DELETE /api/v1/criteria/{id}", viaCriterion(s.handleDeleteCriterion))
	mux.Handle("PATCH /api/v1/labels/{id}", viaLabel(s.handlePatchLabel))
	mux.Handle("DELETE /api/v1/labels/{id}", viaLabel(s.handleDeleteLabel))
}

// requireMember is RequireProjectMember for routes whose path carries no project key: resolve
// finds the owning project's ID from the path, and the same membership rule applies. Must sit
// inside RequireAuth.
func (s *Service) requireMember(a *auth.Service, resolve func(*http.Request) (string, error), next http.HandlerFunc) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		u, ok := auth.UserFrom(r.Context())
		if !ok {
			httpx.WriteProblem(w, http.StatusUnauthorized, "unauthenticated",
				"Not authenticated", "Sign in to use this endpoint.")
			return
		}
		projectID, err := resolve(r)
		if errIsNotFound(err) {
			httpx.WriteProblem(w, http.StatusNotFound, httpx.TypeNotFound,
				"Not found", "Nothing matches this path.")
			return
		}
		if err != nil {
			s.writeError(w, err)
			return
		}
		p, err := s.st.Projects().ByID(r.Context(), projectID)
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

func (s *Service) projectOfTicket(r *http.Request) (string, error) {
	tk, err := s.st.Tickets().ByID(r.Context(), r.PathValue("id"))
	return tk.ProjectID, err
}

func (s *Service) projectOfCriterion(r *http.Request) (string, error) {
	c, err := s.st.Criteria().ByID(r.Context(), r.PathValue("id"))
	if err != nil {
		return "", err
	}
	tk, err := s.st.Tickets().ByID(r.Context(), c.TicketID)
	return tk.ProjectID, err
}

func (s *Service) projectOfLabel(r *http.Request) (string, error) {
	l, err := s.st.Labels().ByID(r.Context(), r.PathValue("id"))
	return l.ProjectID, err
}

// ---------------------------------------------------------------- bodies -----

// ticketBody is how a ticket renders everywhere. `category` is the ticket's status, derived
// from its column — never stored, never a column name (plan rule 3). `assignee_id` (human)
// and `delegate_agent_id` (agent) are separate fields (D1).
type ticketBody struct {
	ID               string   `json:"id"`
	ProjectID        string   `json:"project_id"`
	Seq              int64    `json:"seq"`
	Key              string   `json:"key"`
	Title            string   `json:"title"`
	Description      string   `json:"description"`
	ColumnID         string   `json:"column_id"`
	Category         string   `json:"category"`
	Position         float64  `json:"position"`
	Priority         string   `json:"priority"`
	AssigneeID       *string  `json:"assignee_id"`
	DelegateAgentID  *string  `json:"delegate_agent_id"`
	ParentID         *string  `json:"parent_id"`
	PRNumber         *int64   `json:"pr_number"`
	PRState          *string  `json:"pr_state"`
	Branch           *string  `json:"branch"`
	Origin           string   `json:"origin"`
	CreatedByUserID  *string  `json:"created_by_user_id"`
	CreatedByAgentID *string  `json:"created_by_agent_id"`
	LabelIDs         []string `json:"label_ids"`
	ArchivedAt       *string  `json:"archived_at"`
	CreatedAt        string   `json:"created_at"`
	UpdatedAt        string   `json:"updated_at"`
}

func toTicketBody(t TicketWithMeta) ticketBody {
	labelIDs := t.LabelIDs
	if labelIDs == nil {
		labelIDs = []string{}
	}
	tk := t.Ticket
	return ticketBody{
		ID: tk.ID, ProjectID: tk.ProjectID, Seq: tk.Seq, Key: tk.Key,
		Title: tk.Title, Description: tk.Description,
		ColumnID: tk.ColumnID, Category: string(t.Category), Position: tk.Position,
		Priority:   string(tk.Priority),
		AssigneeID: tk.AssigneeID, DelegateAgentID: tk.DelegateAgentID, ParentID: tk.ParentID,
		PRNumber: tk.PRNumber, PRState: tk.PRState, Branch: tk.Branch,
		Origin:          string(tk.Origin),
		CreatedByUserID: tk.CreatedByUserID, CreatedByAgentID: tk.CreatedByAgentID,
		LabelIDs: labelIDs, ArchivedAt: tk.ArchivedAt,
		CreatedAt: tk.CreatedAt, UpdatedAt: tk.UpdatedAt,
	}
}

type ticketListBody struct {
	Tickets []ticketBody `json:"tickets"`
}

type criterionBody struct {
	ID              string  `json:"id"`
	TicketID        string  `json:"ticket_id"`
	Position        int64   `json:"position"`
	Text            string  `json:"text"`
	Checked         bool    `json:"checked"`
	CheckedByRunID  *string `json:"checked_by_run_id"`
	CheckedByUserID *string `json:"checked_by_user_id"`
	Note            string  `json:"note"`
	UpdatedAt       string  `json:"updated_at"`
}

func toCriterionBody(c domain.Criterion) criterionBody {
	return criterionBody{
		ID: c.ID, TicketID: c.TicketID, Position: c.Position, Text: c.Text,
		Checked: c.Checked, CheckedByRunID: c.CheckedByRunID, CheckedByUserID: c.CheckedByUserID,
		Note: c.Note, UpdatedAt: c.UpdatedAt,
	}
}

type labelBody struct {
	ID        string `json:"id"`
	ProjectID string `json:"project_id"`
	Name      string `json:"name"`
	Color     string `json:"color"`
}

func toLabelBody(l domain.Label) labelBody {
	return labelBody{ID: l.ID, ProjectID: l.ProjectID, Name: l.Name, Color: l.Color}
}

type detailBody struct {
	ticketBody
	Criteria []criterionBody `json:"criteria"`
	Labels   []labelBody     `json:"labels"`
	Children []ticketBody    `json:"children"`
}

type streamEntryBody struct {
	ID        string          `json:"id"`
	TicketID  string          `json:"ticket_id"`
	Kind      string          `json:"kind"`
	ActorKind string          `json:"actor_kind"`
	ActorID   *string         `json:"actor_id"`
	Body      string          `json:"body"`
	Payload   json.RawMessage `json:"payload"`
	RunID     *string         `json:"run_id"`
	EditedAt  *string         `json:"edited_at"`
	CreatedAt string          `json:"created_at"`
}

type streamBody struct {
	Entries []streamEntryBody `json:"entries"`
}

// ---------------------------------------------------------------- handlers -----

func (s *Service) handleList(w http.ResponseWriter, r *http.Request) {
	list, err := s.List(r.Context(), r.PathValue("key"), r.URL.Query().Get("archived") == "1")
	if err != nil {
		s.writeError(w, err)
		return
	}
	body := ticketListBody{Tickets: make([]ticketBody, 0, len(list))}
	for _, t := range list {
		body.Tickets = append(body.Tickets, toTicketBody(t))
	}
	httpx.WriteJSON(w, http.StatusOK, body)
}

type createTicketBody struct {
	Title           string `json:"title"`
	Description     string `json:"description"`
	Priority        string `json:"priority"`
	ColumnID        string `json:"column_id"`
	AssigneeID      string `json:"assignee_id"`
	DelegateAgentID string `json:"delegate_agent_id"`
	ParentID        string `json:"parent_id"`
}

func (s *Service) handleCreate(w http.ResponseWriter, r *http.Request) {
	body, ok := httpx.DecodeJSON[createTicketBody](w, r)
	if !ok {
		return
	}
	t, err := s.Create(r.Context(), r.PathValue("key"), CreateInput(body))
	if err != nil {
		s.writeError(w, err)
		return
	}
	httpx.WriteJSON(w, http.StatusCreated, toTicketBody(t))
}

func (s *Service) handleGet(w http.ResponseWriter, r *http.Request) {
	d, err := s.Get(r.Context(), r.PathValue("id"))
	if err != nil {
		s.writeError(w, err)
		return
	}
	body := detailBody{
		ticketBody: toTicketBody(d.TicketWithMeta),
		Criteria:   make([]criterionBody, 0, len(d.Criteria)),
		Labels:     make([]labelBody, 0, len(d.Labels)),
		Children:   make([]ticketBody, 0, len(d.Children)),
	}
	for _, c := range d.Criteria {
		body.Criteria = append(body.Criteria, toCriterionBody(c))
	}
	for _, l := range d.Labels {
		body.Labels = append(body.Labels, toLabelBody(l))
	}
	for _, c := range d.Children {
		body.Children = append(body.Children, toTicketBody(c))
	}
	httpx.WriteJSON(w, http.StatusOK, body)
}

type patchTicketBody struct {
	Title           *string `json:"title"`
	Description     *string `json:"description"`
	Priority        *string `json:"priority"`
	AssigneeID      OptStr  `json:"assignee_id"`
	DelegateAgentID OptStr  `json:"delegate_agent_id"`
	ParentID        OptStr  `json:"parent_id"`
}

func (s *Service) handlePatch(w http.ResponseWriter, r *http.Request) {
	body, ok := httpx.DecodeJSON[patchTicketBody](w, r)
	if !ok {
		return
	}
	t, err := s.Update(r.Context(), r.PathValue("id"), UpdatePatch(body))
	if err != nil {
		s.writeError(w, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, toTicketBody(t))
}

func (s *Service) handleArchive(w http.ResponseWriter, r *http.Request) {
	var confirmed int64
	if raw := r.URL.Query().Get("confirm_active_runs"); raw != "" {
		n, err := strconv.ParseInt(raw, 10, 64)
		if err != nil || n < 0 {
			httpx.WriteValidation(w, []httpx.FieldError{{Field: "confirm_active_runs",
				Message: "A non-negative integer."}})
			return
		}
		confirmed = n
	}
	if _, err := s.Archive(r.Context(), r.PathValue("id"), confirmed); err != nil {
		s.writeError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Service) handleUnarchive(w http.ResponseWriter, r *http.Request) {
	t, err := s.Unarchive(r.Context(), r.PathValue("id"))
	if err != nil {
		s.writeError(w, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, toTicketBody(t))
}

type moveBody struct {
	ColumnID string   `json:"column_id"`
	Position *float64 `json:"position"`
	AfterID  OptStr   `json:"after_ticket_id"`
}

func (s *Service) handleMove(w http.ResponseWriter, r *http.Request) {
	body, ok := httpx.DecodeJSON[moveBody](w, r)
	if !ok {
		return
	}
	t, err := s.Move(r.Context(), r.PathValue("id"), MoveInput(body))
	if err != nil {
		s.writeError(w, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, toTicketBody(t))
}

// subticketsBody accepts `titles` (this API's name) and `texts` (the contracts §5 route
// inventory's name) — one line per sub-ticket either way.
type subticketsBody struct {
	Titles []string `json:"titles"`
	Texts  []string `json:"texts"`
}

func (s *Service) handleSubtickets(w http.ResponseWriter, r *http.Request) {
	body, ok := httpx.DecodeJSON[subticketsBody](w, r)
	if !ok {
		return
	}
	titles := body.Titles
	if len(titles) == 0 {
		titles = body.Texts
	}
	created, err := s.Subtickets(r.Context(), r.PathValue("id"), titles)
	if err != nil {
		s.writeError(w, err)
		return
	}
	out := ticketListBody{Tickets: make([]ticketBody, 0, len(created))}
	for _, t := range created {
		out.Tickets = append(out.Tickets, toTicketBody(t))
	}
	httpx.WriteJSON(w, http.StatusCreated, out)
}

func (s *Service) handleStream(w http.ResponseWriter, r *http.Request) {
	entries, err := s.Stream(r.Context(), r.PathValue("id"))
	if err != nil {
		s.writeError(w, err)
		return
	}
	body := streamBody{Entries: make([]streamEntryBody, 0, len(entries))}
	for _, e := range entries {
		body.Entries = append(body.Entries, streamEntryBody{
			ID: e.ID, TicketID: e.TicketID, Kind: string(e.Kind),
			ActorKind: string(e.ActorKind), ActorID: e.ActorID,
			Body: e.Body, Payload: e.Payload, RunID: e.RunID,
			EditedAt: e.EditedAt, CreatedAt: e.CreatedAt,
		})
	}
	httpx.WriteJSON(w, http.StatusOK, body)
}

type addCriterionBody struct {
	Text string `json:"text"`
}

func (s *Service) handleAddCriterion(w http.ResponseWriter, r *http.Request) {
	body, ok := httpx.DecodeJSON[addCriterionBody](w, r)
	if !ok {
		return
	}
	c, err := s.AddCriterion(r.Context(), r.PathValue("id"), body.Text)
	if err != nil {
		s.writeError(w, err)
		return
	}
	httpx.WriteJSON(w, http.StatusCreated, toCriterionBody(c))
}

type patchCriterionBody struct {
	Text    *string `json:"text"`
	Note    *string `json:"note"`
	Checked *bool   `json:"checked"`
	AfterID OptStr  `json:"after_id"`
}

func (s *Service) handlePatchCriterion(w http.ResponseWriter, r *http.Request) {
	body, ok := httpx.DecodeJSON[patchCriterionBody](w, r)
	if !ok {
		return
	}
	c, err := s.UpdateCriterion(r.Context(), r.PathValue("id"), CriterionPatch(body))
	if err != nil {
		s.writeError(w, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, toCriterionBody(c))
}

func (s *Service) handleDeleteCriterion(w http.ResponseWriter, r *http.Request) {
	if err := s.DeleteCriterion(r.Context(), r.PathValue("id")); err != nil {
		s.writeError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Service) handleListLabels(w http.ResponseWriter, r *http.Request) {
	labels, err := s.ListLabels(r.Context(), r.PathValue("key"))
	if err != nil {
		s.writeError(w, err)
		return
	}
	body := struct {
		Labels []labelBody `json:"labels"`
	}{Labels: make([]labelBody, 0, len(labels))}
	for _, l := range labels {
		body.Labels = append(body.Labels, toLabelBody(l))
	}
	httpx.WriteJSON(w, http.StatusOK, body)
}

type labelBodyIn struct {
	Name  string `json:"name"`
	Color string `json:"color"`
}

func (s *Service) handleCreateLabel(w http.ResponseWriter, r *http.Request) {
	body, ok := httpx.DecodeJSON[labelBodyIn](w, r)
	if !ok {
		return
	}
	l, err := s.CreateLabel(r.Context(), r.PathValue("key"), body.Name, body.Color)
	if err != nil {
		s.writeError(w, err)
		return
	}
	httpx.WriteJSON(w, http.StatusCreated, toLabelBody(l))
}

type patchLabelBody struct {
	Name  *string `json:"name"`
	Color *string `json:"color"`
}

func (s *Service) handlePatchLabel(w http.ResponseWriter, r *http.Request) {
	body, ok := httpx.DecodeJSON[patchLabelBody](w, r)
	if !ok {
		return
	}
	l, err := s.UpdateLabel(r.Context(), r.PathValue("id"), LabelPatch(body))
	if err != nil {
		s.writeError(w, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, toLabelBody(l))
}

func (s *Service) handleDeleteLabel(w http.ResponseWriter, r *http.Request) {
	if err := s.DeleteLabel(r.Context(), r.PathValue("id")); err != nil {
		s.writeError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Service) handleAttachLabel(w http.ResponseWriter, r *http.Request) {
	if err := s.AttachLabel(r.Context(), r.PathValue("id"), r.PathValue("label_id")); err != nil {
		s.writeError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Service) handleDetachLabel(w http.ResponseWriter, r *http.Request) {
	if err := s.DetachLabel(r.Context(), r.PathValue("id"), r.PathValue("label_id")); err != nil {
		s.writeError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// ---------------------------------------------------------------- errors -----

// writeError maps service errors to problems: field errors → 400 validation_failed; the
// typed guardrails → their named 409s; missing rows → 404; everything else → a logged 500.
func (s *Service) writeError(w http.ResponseWriter, err error) {
	var ve *ValidationError
	if errors.As(err, &ve) {
		httpx.WriteValidation(w, ve.Fields)
		return
	}
	var depth *SubticketDepthError
	if errors.As(err, &depth) {
		httpx.WriteProblem(w, http.StatusConflict, "subticket_depth",
			"Sub-tickets are one level",
			"Ticket "+depth.TicketKey+" cannot be nested further: sub-tickets cannot have"+
				" sub-tickets of their own.")
		return
	}
	var arch *ArchivedError
	if errors.As(err, &arch) {
		httpx.WriteProblem(w, http.StatusConflict, "ticket_archived",
			"Ticket is archived",
			"Ticket "+arch.TicketKey+" is archived; unarchive it before changing it.")
		return
	}
	var notArch *NotArchivedError
	if errors.As(err, &notArch) {
		httpx.WriteProblem(w, http.StatusConflict, "ticket_not_archived",
			"Ticket is not archived",
			"Ticket "+notArch.TicketKey+" is not archived; there is nothing to restore.")
		return
	}
	var runs *ActiveRunsError
	if errors.As(err, &runs) {
		httpx.WriteProblem(w, http.StatusConflict, "active_runs_confirmation",
			"Active runs must be confirmed", runs.Error()+".")
		return
	}
	if errors.Is(err, store.ErrNotFound) {
		httpx.WriteProblem(w, http.StatusNotFound, httpx.TypeNotFound,
			"Not found", "Nothing matches this path.")
		return
	}
	s.logger.Error("tickets: request failed", slog.String("error", err.Error()))
	httpx.WriteProblem(w, http.StatusInternalServerError, httpx.TypeInternal,
		"Internal error", "Something went wrong on the server. The error has been logged.")
}
