package secrets

import (
	"errors"
	"log/slog"
	"net/http"

	"github.com/spruce/lexicode/internal/domain"
	"github.com/spruce/lexicode/internal/kernel/auth"
	"github.com/spruce/lexicode/internal/kernel/httpx"
	kernelsecrets "github.com/spruce/lexicode/internal/kernel/secrets"
	"github.com/spruce/lexicode/internal/kernel/store"
)

// Routes registers the S13 endpoints. Project-scope routes (contracts §5) are for project
// members; workspace-scope routes are owner-only, matching the owner-only workspace settings
// screen they render in.
//
//	GET|POST         /api/v1/projects/{key}/secrets
//	PATCH|DELETE     /api/v1/projects/{key}/secrets/{id}
//	GET|POST         /api/v1/workspace/secrets
//	PATCH|DELETE     /api/v1/workspace/secrets/{id}
//
// There is no GET-one and no value in any response: values are write-only (D-16).
func (s *Service) Routes(mux httpx.Registrar, a *auth.Service) {
	member := func(h http.HandlerFunc) http.Handler {
		return a.RequireAuth(a.RequireProjectMember(h))
	}
	owner := func(h http.HandlerFunc) http.Handler {
		return a.RequireAuth(auth.RequireOwner(h))
	}
	mux.Handle("GET /api/v1/projects/{key}/secrets", member(s.handleProjectList))
	mux.Handle("POST /api/v1/projects/{key}/secrets", member(s.handleProjectSet))
	mux.Handle("PATCH /api/v1/projects/{key}/secrets/{id}", member(s.handleProjectRename))
	mux.Handle("DELETE /api/v1/projects/{key}/secrets/{id}", member(s.handleProjectDelete))
	mux.Handle("GET /api/v1/workspace/secrets", owner(s.handleWorkspaceList))
	mux.Handle("POST /api/v1/workspace/secrets", owner(s.handleWorkspaceSet))
	mux.Handle("PATCH /api/v1/workspace/secrets/{id}", owner(s.handleWorkspaceRename))
	mux.Handle("DELETE /api/v1/workspace/secrets/{id}", owner(s.handleWorkspaceDelete))
}

// ---------------------------------------------------------------- bodies -----

// secretBody is a secret on the wire: metadata only, by construction — it is built from
// kernelsecrets.Info, which has no value field.
type secretBody struct {
	ID        string             `json:"id"`
	Scope     domain.SecretScope `json:"scope"`
	ProjectID *string            `json:"project_id"`
	Name      string             `json:"name"`
	CreatedAt string             `json:"created_at"`
	UpdatedAt string             `json:"updated_at"`
}

func toBody(i kernelsecrets.Info) secretBody {
	return secretBody{ID: i.ID, Scope: i.Scope, ProjectID: i.ProjectID, Name: i.Name,
		CreatedAt: i.CreatedAt, UpdatedAt: i.UpdatedAt}
}

type listBody struct {
	Secrets []secretBody `json:"secrets"`
}

type setBody struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

type renameBody struct {
	Name string `json:"name"`
}

// ---------------------------------------------------------------- handlers -----

func (s *Service) handleProjectList(w http.ResponseWriter, r *http.Request) {
	ref, err := s.projectRef(r.Context(), r.PathValue("key"))
	if err != nil {
		s.writeError(w, err)
		return
	}
	s.list(w, r, ref)
}

func (s *Service) handleWorkspaceList(w http.ResponseWriter, r *http.Request) {
	s.list(w, r, scopeRef{Scope: domain.SecretScopeWorkspace})
}

func (s *Service) list(w http.ResponseWriter, r *http.Request, ref scopeRef) {
	infos, err := s.List(r.Context(), ref)
	if err != nil {
		s.writeError(w, err)
		return
	}
	body := listBody{Secrets: make([]secretBody, 0, len(infos))}
	for _, i := range infos {
		body.Secrets = append(body.Secrets, toBody(i))
	}
	httpx.WriteJSON(w, http.StatusOK, body)
}

func (s *Service) handleProjectSet(w http.ResponseWriter, r *http.Request) {
	ref, err := s.projectRef(r.Context(), r.PathValue("key"))
	if err != nil {
		s.writeError(w, err)
		return
	}
	s.set(w, r, ref)
}

func (s *Service) handleWorkspaceSet(w http.ResponseWriter, r *http.Request) {
	s.set(w, r, scopeRef{Scope: domain.SecretScopeWorkspace})
}

func (s *Service) set(w http.ResponseWriter, r *http.Request, ref scopeRef) {
	body, ok := httpx.DecodeJSON[setBody](w, r)
	if !ok {
		return
	}
	u, _ := auth.UserFrom(r.Context())
	inf, created, err := s.SetSecret(r.Context(), ref, body.Name, body.Value, u.ID)
	if err != nil {
		s.writeError(w, err)
		return
	}
	status := http.StatusOK
	if created {
		status = http.StatusCreated
	}
	httpx.WriteJSON(w, status, toBody(inf))
}

func (s *Service) handleProjectRename(w http.ResponseWriter, r *http.Request) {
	ref, err := s.projectRef(r.Context(), r.PathValue("key"))
	if err != nil {
		s.writeError(w, err)
		return
	}
	s.rename(w, r, ref)
}

func (s *Service) handleWorkspaceRename(w http.ResponseWriter, r *http.Request) {
	s.rename(w, r, scopeRef{Scope: domain.SecretScopeWorkspace})
}

func (s *Service) rename(w http.ResponseWriter, r *http.Request, ref scopeRef) {
	body, ok := httpx.DecodeJSON[renameBody](w, r)
	if !ok {
		return
	}
	inf, err := s.RenameSecret(r.Context(), ref, r.PathValue("id"), body.Name)
	if err != nil {
		s.writeError(w, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, toBody(inf))
}

func (s *Service) handleProjectDelete(w http.ResponseWriter, r *http.Request) {
	ref, err := s.projectRef(r.Context(), r.PathValue("key"))
	if err != nil {
		s.writeError(w, err)
		return
	}
	s.delete(w, r, ref)
}

func (s *Service) handleWorkspaceDelete(w http.ResponseWriter, r *http.Request) {
	s.delete(w, r, scopeRef{Scope: domain.SecretScopeWorkspace})
}

func (s *Service) delete(w http.ResponseWriter, r *http.Request, ref scopeRef) {
	if err := s.DeleteSecret(r.Context(), ref, r.PathValue("id")); err != nil {
		s.writeError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// writeError maps service errors to problems: field errors → 400 validation_failed, a name
// collision → 400 on "name", missing rows (including out-of-scope IDs) → 404, a secret
// something still references → 409, everything else → a logged 500.
func (s *Service) writeError(w http.ResponseWriter, err error) {
	var ve *ValidationError
	if errors.As(err, &ve) {
		httpx.WriteValidation(w, ve.Fields)
		return
	}
	if errors.Is(err, kernelsecrets.ErrNameTaken) {
		httpx.WriteValidation(w, []httpx.FieldError{
			{Field: "name", Message: "A secret with this name already exists."}})
		return
	}
	if errors.Is(err, store.ErrNotFound) {
		httpx.WriteProblem(w, http.StatusNotFound, httpx.TypeNotFound,
			"Not found", "No secret matches this path.")
		return
	}
	if errors.Is(err, store.ErrForeignKey) {
		httpx.WriteProblem(w, http.StatusConflict, "secret_in_use",
			"Secret in use", "Something still references this secret; disconnect it first.")
		return
	}
	s.logger.Error("secrets: request failed", slog.String("error", err.Error()))
	httpx.WriteProblem(w, http.StatusInternalServerError, httpx.TypeInternal,
		"Internal error", "Something went wrong on the server. The error has been logged.")
}
