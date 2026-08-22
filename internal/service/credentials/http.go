package credentials

import (
	"errors"
	"net/http"

	"github.com/spruce/lexicode/internal/kernel/auth"
	"github.com/spruce/lexicode/internal/kernel/httpx"
)

// Routes registers the S19 endpoints. Owner-only, like the rest of the workspace settings
// screen they render in.
//
//	GET    /api/v1/workspace/credentials              status + health, no values
//	PUT    /api/v1/workspace/credentials/oauth-token  store the pasted setup-token output
//	DELETE /api/v1/workspace/credentials/oauth-token  forget it
//	POST   /api/v1/workspace/credentials/import       Linux only: read the CLI's own login
func (s *Service) Routes(mux httpx.Registrar, a *auth.Service) {
	owner := func(h http.HandlerFunc) http.Handler {
		return a.RequireAuth(auth.RequireOwner(h))
	}
	mux.Handle("GET /api/v1/workspace/credentials", owner(s.handleStatus))
	mux.Handle("PUT /api/v1/workspace/credentials/oauth-token", owner(s.handleSetToken))
	mux.Handle("DELETE /api/v1/workspace/credentials/oauth-token", owner(s.handleClearToken))
	mux.Handle("POST /api/v1/workspace/credentials/import", owner(s.handleImport))
}

// setTokenBody is PUT /workspace/credentials/oauth-token. The value goes in and never comes
// back out (D-16).
type setTokenBody struct {
	Token string `json:"token"`
}

func (s *Service) handleStatus(w http.ResponseWriter, r *http.Request) {
	st, err := s.Status(r.Context())
	if err != nil {
		s.writeError(w, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, st)
}

func (s *Service) handleSetToken(w http.ResponseWriter, r *http.Request) {
	body, ok := httpx.DecodeJSON[setTokenBody](w, r)
	if !ok {
		return
	}
	u, _ := auth.UserFrom(r.Context())
	st, err := s.SetToken(r.Context(), body.Token, u.ID)
	if err != nil {
		s.writeError(w, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, st)
}

func (s *Service) handleClearToken(w http.ResponseWriter, r *http.Request) {
	st, err := s.ClearToken(r.Context())
	if err != nil {
		s.writeError(w, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, st)
}

func (s *Service) handleImport(w http.ResponseWriter, r *http.Request) {
	u, _ := auth.UserFrom(r.Context())
	st, err := s.ImportToken(r.Context(), u.ID)
	if err != nil {
		s.writeError(w, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, st)
}

// writeError maps service errors to problems: validation → 400, import on a non-Linux host →
// 409 (the button is hidden, but the check is the endpoint's, at request time), a missing CLI
// login file → 404, clearing nothing → 404, everything else → a logged 500.
func (s *Service) writeError(w http.ResponseWriter, err error) {
	var ve *ValidationError
	if errors.As(err, &ve) {
		httpx.WriteValidation(w, []httpx.FieldError{ve.FieldError()})
		return
	}
	switch {
	case errors.Is(err, ErrImportUnsupported):
		httpx.WriteProblem(w, http.StatusConflict, "import_unsupported",
			"Import unavailable", err.Error())
	case errors.Is(err, ErrNoCredentialsFile):
		httpx.WriteProblem(w, http.StatusNotFound, httpx.TypeNotFound,
			"No CLI login found", err.Error())
	case errors.Is(err, ErrNotConfigured):
		httpx.WriteProblem(w, http.StatusNotFound, httpx.TypeNotFound,
			"Not configured", err.Error())
	default:
		s.logger.Error("credentials request failed", "error", err.Error())
		httpx.WriteProblem(w, http.StatusInternalServerError, httpx.TypeInternal,
			"Internal error", "Something went wrong handling credentials.")
	}
}
