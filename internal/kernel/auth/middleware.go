package auth

import (
	"errors"
	"net/http"
	"net/url"

	"github.com/spruce/lexicode/internal/domain"
	"github.com/spruce/lexicode/internal/kernel/store"
)

// SetupPath is the one API endpoint that answers before the first user exists.
const SetupPath = "/api/v1/auth/setup"

// SetupGate answers every request with 401 "setup_required" while the database has zero users,
// except POST /api/v1/auth/setup. Wrap it around the whole /api/ namespace: the SPA switches on
// the type slug and routes to the setup screen (S05). The zero-user check is cached once it has
// ever been false — users are never hard-deleted, so a workspace never returns to first-run.
func (s *Service) SetupGate(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost && r.URL.Path == SetupPath {
			next.ServeHTTP(w, r)
			return
		}
		done, err := s.SetupDone(r.Context())
		if err != nil {
			s.writeError(w, r, err)
			return
		}
		if !done {
			writeProblem(w, http.StatusUnauthorized, "setup_required",
				"Setup required", "No user exists yet. Create the owner with POST "+SetupPath+".")
			return
		}
		next.ServeHTTP(w, r)
	})
}

// RequireAuth resolves the session cookie to a user and rejects the request with a 401 problem
// when it cannot: "unauthenticated" for a missing, unknown or revoked token, "session_expired"
// for one past its expiry. On success the request context carries the user (UserFrom) and a
// human Actor (ActorFrom) for the audit writer (S06), and a sliding-refreshed session re-issues
// the cookie with a fresh Max-Age.
func (s *Service) RequireAuth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var token string
		if c, err := r.Cookie(SessionCookie); err == nil {
			token = c.Value
		}
		u, _, refreshed, err := s.Authenticate(r.Context(), token)
		if err != nil {
			if errors.Is(err, ErrUnauthenticated) || errors.Is(err, ErrSessionExpired) {
				s.clearSessionCookie(w, r)
			}
			s.writeError(w, r, err)
			return
		}
		if refreshed {
			s.setSessionCookie(w, r, token)
		}
		ctx := withUser(r.Context(), u)
		ctx = WithActor(ctx, Actor{Kind: domain.ActorHuman, ID: u.ID})
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// RequireOwner rejects non-owners with a 403 problem. It reads the user RequireAuth put on the
// context, so it must sit inside RequireAuth.
func RequireOwner(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		u, ok := UserFrom(r.Context())
		if !ok {
			writeProblem(w, http.StatusUnauthorized, "unauthenticated",
				"Not authenticated", "Sign in to use this endpoint.")
			return
		}
		if u.Role != domain.RoleOwner {
			writeProblem(w, http.StatusForbidden, "forbidden",
				"Owner only", "Only the workspace owner can use this endpoint.")
			return
		}
		next.ServeHTTP(w, r)
	})
}

// RequireProjectMember rejects users who are not members of the project named in the path with
// a 403 problem. The project comes from the route's {key} path value (contracts §5 routes are
// keyed by project key), or {project_id} where a route carries the ID instead. Workspace owners
// and the project's owner pass without a membership row — the owner administers every project.
// It must sit inside RequireAuth.
func (s *Service) RequireProjectMember(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		u, ok := UserFrom(r.Context())
		if !ok {
			writeProblem(w, http.StatusUnauthorized, "unauthenticated",
				"Not authenticated", "Sign in to use this endpoint.")
			return
		}
		var (
			p   domain.Project
			err error
		)
		if key := r.PathValue("key"); key != "" {
			p, err = s.st.Projects().ByKey(r.Context(), key)
		} else if id := r.PathValue("project_id"); id != "" {
			p, err = s.st.Projects().ByID(r.Context(), id)
		} else {
			// A route without a project in its path is a programming error, not a client one,
			// but answering 404 keeps the mistake visible without leaking anything.
			err = store.ErrNotFound
		}
		if errors.Is(err, store.ErrNotFound) {
			writeProblem(w, http.StatusNotFound, "not_found",
				"No such project", "No project matches this path.")
			return
		}
		if err != nil {
			s.writeError(w, r, err)
			return
		}
		member, err := s.IsProjectMember(r.Context(), u, p)
		if err != nil {
			s.writeError(w, r, err)
			return
		}
		if !member {
			writeProblem(w, http.StatusForbidden, "forbidden",
				"Not a project member", "You are not a member of this project.")
			return
		}
		next.ServeHTTP(w, r)
	})
}

// CSRF rejects cross-site unsafe requests. The session cookie is already SameSite=Lax; this is
// the second, explicit layer (S05). The exact check, on every method except GET, HEAD and
// OPTIONS:
//
//  1. If an Origin header is present, its host (host:port) must equal the request's Host.
//     Mismatch — including the opaque "null" origin — is a 403 "origin_forbidden". The scheme
//     is deliberately not compared: behind a TLS-terminating proxy the browser says https
//     while r.TLS is nil, and the host is the part that identifies the site.
//  2. With no Origin, a Sec-Fetch-Site of "same-site" or "cross-site" is a 403. ("same-site"
//     is rejected because a sibling subdomain is not this app.)
//  3. A request with neither header — curl and other non-browser clients — is allowed: it
//     carries no ambient browser credentials to launder. Browsers always send Origin on
//     cross-origin unsafe requests, so a forged form post cannot reach here headerless.
//
// Safe methods pass through untouched.
func CSRF(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet, http.MethodHead, http.MethodOptions:
			next.ServeHTTP(w, r)
			return
		}
		if origin := r.Header.Get("Origin"); origin != "" {
			o, err := url.Parse(origin)
			if err != nil || o.Host == "" || o.Host != r.Host {
				writeProblem(w, http.StatusForbidden, "origin_forbidden",
					"Cross-origin request refused",
					"This request's Origin does not match the server it was sent to.")
				return
			}
		} else if site := r.Header.Get("Sec-Fetch-Site"); site == "same-site" || site == "cross-site" {
			writeProblem(w, http.StatusForbidden, "origin_forbidden",
				"Cross-site request refused",
				"This request was made across sites and is refused.")
			return
		}
		next.ServeHTTP(w, r)
	})
}
