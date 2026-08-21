package auth

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"time"

	"github.com/spruce/lexicode/internal/domain"
	"github.com/spruce/lexicode/internal/kernel/store"
)

// SessionCookie is the name of the session cookie. It carries the raw token; the database holds
// only the token's hash.
const SessionCookie = "lexicode_session"

// maxBodyBytes bounds every auth request body; the largest legitimate one is a few hundred bytes.
const maxBodyBytes = 64 << 10

// Routes registers the auth endpoints on the mux. The setup gate and the CSRF check are not
// applied here — they wrap the whole /api/ namespace where the server assembles its handler —
// so these routes stay plain and testable.
func (s *Service) Routes(mux *http.ServeMux) {
	mux.Handle("POST /api/v1/auth/setup", http.HandlerFunc(s.handleSetup))
	mux.Handle("POST /api/v1/auth/login", http.HandlerFunc(s.handleLogin))
	mux.Handle("POST /api/v1/auth/logout", http.HandlerFunc(s.handleLogout))
	mux.Handle("POST /api/v1/auth/redeem", http.HandlerFunc(s.handleRedeem))
	// Contracts §5 canonical form; token in the path, same handler underneath.
	mux.Handle("POST /api/v1/invites/{token}/redeem", http.HandlerFunc(s.handleRedeem))
	mux.Handle("GET /api/v1/auth/me", s.RequireAuth(http.HandlerFunc(s.handleMe)))
	mux.Handle("POST /api/v1/invites",
		s.RequireAuth(RequireOwner(http.HandlerFunc(s.handleCreateInvite))))
}

// userBody is how a user renders in every auth response. The password hash never leaves.
type userBody struct {
	ID          string `json:"id"`
	Email       string `json:"email"`
	DisplayName string `json:"display_name"`
	Role        string `json:"role"`
	AvatarColor string `json:"avatar_color"`
	CreatedAt   string `json:"created_at"`
}

func toUserBody(u domain.User) userBody {
	return userBody{ID: u.ID, Email: u.Email, DisplayName: u.DisplayName,
		Role: string(u.Role), AvatarColor: u.AvatarColor, CreatedAt: u.CreatedAt}
}

type credentialsBody struct {
	Email       string `json:"email"`
	DisplayName string `json:"display_name"`
	Password    string `json:"password"`
	Token       string `json:"token"` // redeem only
}

func (s *Service) handleSetup(w http.ResponseWriter, r *http.Request) {
	var body credentialsBody
	if !s.decode(w, r, &body) {
		return
	}
	u, err := s.Setup(r.Context(), body.Email, body.DisplayName, body.Password)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	s.startSession(w, r, u, http.StatusCreated)
}

func (s *Service) handleLogin(w http.ResponseWriter, r *http.Request) {
	var body credentialsBody
	if !s.decode(w, r, &body) {
		return
	}
	u, err := s.Login(r.Context(), body.Email, body.Password)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	s.startSession(w, r, u, http.StatusOK)
}

func (s *Service) handleLogout(w http.ResponseWriter, r *http.Request) {
	if c, err := r.Cookie(SessionCookie); err == nil {
		if err := s.Logout(r.Context(), c.Value); err != nil {
			s.writeError(w, r, err)
			return
		}
	}
	// Idempotent: no cookie, an unknown token and a revoked token all end the same way.
	s.clearSessionCookie(w, r)
	w.WriteHeader(http.StatusNoContent)
}

func (s *Service) handleMe(w http.ResponseWriter, r *http.Request) {
	u, ok := UserFrom(r.Context())
	if !ok { // unreachable behind RequireAuth; belt and braces
		writeProblem(w, http.StatusUnauthorized, "unauthenticated", "Not authenticated", "")
		return
	}
	writeJSON(w, http.StatusOK, toUserBody(u))
}

// inviteBody is the response of POST /api/v1/invites: the one-time path the owner copies into a
// message, plus when it stops working. The raw token appears here once and is never readable
// again (D-8: copyable link, no email delivery).
type inviteBody struct {
	Path      string `json:"path"`
	ExpiresAt string `json:"expires_at"`
}

func (s *Service) handleCreateInvite(w http.ResponseWriter, r *http.Request) {
	u, _ := UserFrom(r.Context())
	path, inv, err := s.CreateInvite(r.Context(), u.ID)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, inviteBody{Path: path, ExpiresAt: inv.ExpiresAt})
}

func (s *Service) handleRedeem(w http.ResponseWriter, r *http.Request) {
	var body credentialsBody
	if !s.decode(w, r, &body) {
		return
	}
	if t := r.PathValue("token"); t != "" {
		body.Token = t
	}
	u, err := s.Redeem(r.Context(), body.Token, body.Email, body.DisplayName, body.Password)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	s.startSession(w, r, u, http.StatusCreated)
}

// startSession mints a session for u, sets the cookie and writes the user body. Setup, login
// and redeem all end here: every way of proving who you are leaves you logged in.
func (s *Service) startSession(w http.ResponseWriter, r *http.Request, u domain.User, status int) {
	token, _, err := s.CreateSession(r.Context(), u.ID, r.UserAgent())
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	s.setSessionCookie(w, r, token)
	writeJSON(w, status, toUserBody(u))
}

// setSessionCookie writes the session cookie: HttpOnly always, SameSite=Lax, Secure when the
// request itself arrived over TLS (D-8). Max-Age mirrors the session TTL; sliding refresh
// re-issues the cookie so both expiries slide together.
func (s *Service) setSessionCookie(w http.ResponseWriter, r *http.Request, token string) {
	http.SetCookie(w, &http.Cookie{
		Name:     SessionCookie,
		Value:    token,
		Path:     "/",
		MaxAge:   int(s.sessionTTL / time.Second),
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		Secure:   r.TLS != nil,
	})
}

func (s *Service) clearSessionCookie(w http.ResponseWriter, r *http.Request) {
	http.SetCookie(w, &http.Cookie{
		Name:     SessionCookie,
		Value:    "",
		Path:     "/",
		MaxAge:   -1,
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		Secure:   r.TLS != nil,
	})
}

// decode reads a JSON body into v, answering 400 invalid_request itself when the body is not
// JSON. The return value says whether to continue.
func (s *Service) decode(w http.ResponseWriter, r *http.Request, v any) bool {
	r.Body = http.MaxBytesReader(w, r.Body, maxBodyBytes)
	if err := json.NewDecoder(r.Body).Decode(v); err != nil {
		writeProblem(w, http.StatusBadRequest, "invalid_request",
			"Invalid request body", "The request body must be a JSON object.")
		return false
	}
	return true
}

// writeError maps the service's error vocabulary onto problem+json responses. Anything outside
// the vocabulary is a 500 with a generic detail; the real error goes to the log, not the wire.
func (s *Service) writeError(w http.ResponseWriter, r *http.Request, err error) {
	var ve *ValidationError
	switch {
	case errors.As(err, &ve):
		writeProblem(w, http.StatusBadRequest, "invalid_request", "Invalid request", ve.Detail)
	case errors.Is(err, ErrAlreadySetup):
		writeProblem(w, http.StatusConflict, "already_setup",
			"Setup already completed", "This workspace already has an owner. Sign in instead.")
	case errors.Is(err, ErrInvalidCredentials):
		writeProblem(w, http.StatusUnauthorized, "invalid_credentials",
			"Invalid credentials", "The email or password is incorrect.")
	case errors.Is(err, ErrSessionExpired):
		writeProblem(w, http.StatusUnauthorized, "session_expired",
			"Session expired", "Your session has expired. Sign in again.")
	case errors.Is(err, ErrUnauthenticated):
		writeProblem(w, http.StatusUnauthorized, "unauthenticated",
			"Not authenticated", "Sign in to use this endpoint.")
	case errors.Is(err, ErrInviteInvalid):
		writeProblem(w, http.StatusNotFound, "invite_invalid",
			"Invite not valid", "This invite link is not valid or was already used.")
	case errors.Is(err, ErrInviteExpired):
		writeProblem(w, http.StatusGone, "invite_expired",
			"Invite expired", "This invite link has expired. Ask for a new one.")
	case errors.Is(err, store.ErrUnique):
		writeProblem(w, http.StatusConflict, "email_taken",
			"Email already in use", "A user with this email already exists.")
	default:
		s.logger.Error("auth request failed",
			slog.String("method", r.Method), slog.String("path", r.URL.Path),
			slog.String("error", err.Error()))
		writeProblem(w, http.StatusInternalServerError, "internal",
			"Internal error", "Something went wrong on the server.")
	}
}

// problem is RFC 9457 application/problem+json with a stable type slug the frontend switches on
// (architecture §14). S06's kernel/httpx takes over this shape; the fields match serve's.
type problem struct {
	Type   string `json:"type"`
	Title  string `json:"title"`
	Status int    `json:"status"`
	Detail string `json:"detail,omitempty"`
}

func writeProblem(w http.ResponseWriter, status int, slug, title, detail string) {
	w.Header().Set("Content-Type", "application/problem+json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(problem{Type: slug, Title: title, Status: status, Detail: detail})
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
