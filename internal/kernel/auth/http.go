package auth

import (
	"errors"
	"log/slog"
	"net/http"
	"time"

	"github.com/spruce/lexicode/internal/domain"
	"github.com/spruce/lexicode/internal/kernel/httpx"
	"github.com/spruce/lexicode/internal/kernel/store"
)

// SessionCookie is the name of the session cookie. It carries the raw token; the database holds
// only the token's hash.
const SessionCookie = "lexicode_session"

// Routes registers the auth endpoints on the mux. The setup gate and the CSRF check are not
// applied here — they wrap the whole /api/ namespace where the server assembles its handler —
// so these routes stay plain and testable.
func (s *Service) Routes(mux httpx.Registrar) {
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
	body, ok := httpx.DecodeJSON[credentialsBody](w, r)
	if !ok {
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
	body, ok := httpx.DecodeJSON[credentialsBody](w, r)
	if !ok {
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
		httpx.WriteProblem(w, http.StatusUnauthorized, "unauthenticated", "Not authenticated", "")
		return
	}
	httpx.WriteJSON(w, http.StatusOK, toUserBody(u))
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
	httpx.WriteJSON(w, http.StatusCreated, inviteBody{Path: path, ExpiresAt: inv.ExpiresAt})
}

func (s *Service) handleRedeem(w http.ResponseWriter, r *http.Request) {
	body, ok := httpx.DecodeJSON[credentialsBody](w, r)
	if !ok {
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
	httpx.WriteJSON(w, status, toUserBody(u))
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

// writeError maps the service's error vocabulary onto problem+json responses. Anything outside
// the vocabulary is a 500 with a generic detail; the real error goes to the log, not the wire.
func (s *Service) writeError(w http.ResponseWriter, r *http.Request, err error) {
	var ve *ValidationError
	switch {
	case errors.As(err, &ve):
		httpx.WriteProblem(w, http.StatusBadRequest, "invalid_request", "Invalid request", ve.Detail)
	case errors.Is(err, ErrAlreadySetup):
		httpx.WriteProblem(w, http.StatusConflict, "already_setup",
			"Setup already completed", "This workspace already has an owner. Sign in instead.")
	case errors.Is(err, ErrInvalidCredentials):
		httpx.WriteProblem(w, http.StatusUnauthorized, "invalid_credentials",
			"Invalid credentials", "The email or password is incorrect.")
	case errors.Is(err, ErrSessionExpired):
		httpx.WriteProblem(w, http.StatusUnauthorized, "session_expired",
			"Session expired", "Your session has expired. Sign in again.")
	case errors.Is(err, ErrUnauthenticated):
		httpx.WriteProblem(w, http.StatusUnauthorized, "unauthenticated",
			"Not authenticated", "Sign in to use this endpoint.")
	case errors.Is(err, ErrInviteInvalid):
		httpx.WriteProblem(w, http.StatusNotFound, "invite_invalid",
			"Invite not valid", "This invite link is not valid or was already used.")
	case errors.Is(err, ErrInviteExpired):
		httpx.WriteProblem(w, http.StatusGone, "invite_expired",
			"Invite expired", "This invite link has expired. Ask for a new one.")
	case errors.Is(err, store.ErrUnique):
		httpx.WriteProblem(w, http.StatusConflict, "email_taken",
			"Email already in use", "A user with this email already exists.")
	default:
		s.logger.Error("auth request failed",
			slog.String("method", r.Method), slog.String("path", r.URL.Path),
			slog.String("error", err.Error()))
		httpx.WriteProblem(w, http.StatusInternalServerError, "internal",
			"Internal error", "Something went wrong on the server.")
	}
}

// The problem+json writer lives in kernel/httpx (S06): one canonical shape for the whole
// process. This package only maps its error vocabulary onto it (writeError above).
