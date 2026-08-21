package board

import (
	"errors"
	"log/slog"
	"net/http"

	"github.com/spruce/lexicode/internal/domain"
	"github.com/spruce/lexicode/internal/kernel/auth"
	"github.com/spruce/lexicode/internal/kernel/httpx"
	"github.com/spruce/lexicode/internal/kernel/store"
)

// Routes registers the S09 endpoints (contracts §5):
//
//	GET|POST /api/v1/projects/{key}/columns    project members
//	PATCH|DELETE /api/v1/columns/{id}          project members — resolved via the column,
//	                                           since these paths carry no project key
func (s *Service) Routes(mux httpx.Registrar, a *auth.Service) {
	member := func(h http.HandlerFunc) http.Handler {
		return a.RequireAuth(a.RequireProjectMember(h))
	}
	colMember := func(h http.HandlerFunc) http.Handler {
		return a.RequireAuth(s.requireColumnMember(a, h))
	}
	mux.Handle("GET /api/v1/projects/{key}/columns", member(s.handleList))
	mux.Handle("POST /api/v1/projects/{key}/columns", member(s.handleCreate))
	mux.Handle("PATCH /api/v1/columns/{id}", colMember(s.handlePatch))
	mux.Handle("DELETE /api/v1/columns/{id}", colMember(s.handleDelete))
}

// requireColumnMember is RequireProjectMember for routes addressed by column ID: it resolves
// the column's project and applies the same membership rule. Must sit inside RequireAuth.
func (s *Service) requireColumnMember(a *auth.Service, next http.HandlerFunc) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		u, ok := auth.UserFrom(r.Context())
		if !ok {
			httpx.WriteProblem(w, http.StatusUnauthorized, "unauthenticated",
				"Not authenticated", "Sign in to use this endpoint.")
			return
		}
		c, err := s.st.Columns().ByID(r.Context(), r.PathValue("id"))
		if errors.Is(err, store.ErrNotFound) {
			httpx.WriteProblem(w, http.StatusNotFound, httpx.TypeNotFound,
				"No such column", "No column matches this path.")
			return
		}
		if err != nil {
			s.writeError(w, err)
			return
		}
		p, err := s.st.Projects().ByID(r.Context(), c.ProjectID)
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

// columnBody is how a column renders everywhere. The category is the machine field automation
// keys off; the name is a display string and nothing more (plan rule 3).
type columnBody struct {
	ID                string `json:"id"`
	ProjectID         string `json:"project_id"`
	Name              string `json:"name"`
	Category          string `json:"category"`
	Position          int64  `json:"position"`
	WIPLimit          *int64 `json:"wip_limit"`
	AutoStartDelegate bool   `json:"auto_start_delegate"`
	TicketCount       int64  `json:"ticket_count"`
	CreatedAt         string `json:"created_at"`
	UpdatedAt         string `json:"updated_at"`
}

func toColumnBody(c domain.Column, ticketCount int64) columnBody {
	return columnBody{
		ID: c.ID, ProjectID: c.ProjectID, Name: c.Name, Category: string(c.Category),
		Position: c.Position, WIPLimit: c.WIPLimit, AutoStartDelegate: c.AutoStartDelegate,
		TicketCount: ticketCount, CreatedAt: c.CreatedAt, UpdatedAt: c.UpdatedAt,
	}
}

type columnListBody struct {
	Columns []columnBody `json:"columns"`
}

// ---------------------------------------------------------------- handlers -----

func (s *Service) handleList(w http.ResponseWriter, r *http.Request) {
	list, err := s.List(r.Context(), r.PathValue("key"))
	if err != nil {
		s.writeError(w, err)
		return
	}
	body := columnListBody{Columns: make([]columnBody, 0, len(list))}
	for _, it := range list {
		body.Columns = append(body.Columns, toColumnBody(it.Column, it.TicketCount))
	}
	httpx.WriteJSON(w, http.StatusOK, body)
}

type createBody struct {
	Name     string `json:"name"`
	Category string `json:"category"`
}

func (s *Service) handleCreate(w http.ResponseWriter, r *http.Request) {
	body, ok := httpx.DecodeJSON[createBody](w, r)
	if !ok {
		return
	}
	c, err := s.Create(r.Context(), r.PathValue("key"), CreateInput{
		Name: body.Name, Category: domain.ColumnCategory(body.Category),
	})
	if err != nil {
		s.writeError(w, err)
		return
	}
	httpx.WriteJSON(w, http.StatusCreated, toColumnBody(c, 0))
}

type patchBody struct {
	Name              *string `json:"name"`
	Category          *string `json:"category"`
	WIPLimit          OptInt  `json:"wip_limit"`
	AutoStartDelegate *bool   `json:"auto_start_delegate"`
	AfterID           OptStr  `json:"after_id"`
}

func (s *Service) handlePatch(w http.ResponseWriter, r *http.Request) {
	body, ok := httpx.DecodeJSON[patchBody](w, r)
	if !ok {
		return
	}
	c, err := s.Update(r.Context(), r.PathValue("id"), UpdatePatch(body))
	if err != nil {
		s.writeError(w, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, toColumnBody(c.Column, c.TicketCount))
}

func (s *Service) handleDelete(w http.ResponseWriter, r *http.Request) {
	err := s.Delete(r.Context(), r.PathValue("id"), r.URL.Query().Get("destination_column_id"))
	if err != nil {
		s.writeError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// writeError maps service errors to problems: field errors → 400 validation_failed, the
// required-category guardrail → 409 last_category_column naming the category, missing rows →
// 404, everything else → a logged 500.
func (s *Service) writeError(w http.ResponseWriter, err error) {
	var ve *ValidationError
	if errors.As(err, &ve) {
		httpx.WriteValidation(w, ve.Fields)
		return
	}
	var rce *RequiredCategoryError
	if errors.As(err, &rce) {
		httpx.WriteProblem(w, http.StatusConflict, "last_category_column",
			"Required category",
			"This is the project's last \""+string(rce.Category)+"\" column; every project"+
				" must keep at least one "+string(rce.Category)+" column.")
		return
	}
	if errors.Is(err, store.ErrNotFound) {
		httpx.WriteProblem(w, http.StatusNotFound, httpx.TypeNotFound,
			"Not found", "No column or project matches this path.")
		return
	}
	s.logger.Error("board: request failed", slog.String("error", err.Error()))
	httpx.WriteProblem(w, http.StatusInternalServerError, httpx.TypeInternal,
		"Internal error", "Something went wrong on the server. The error has been logged.")
}
