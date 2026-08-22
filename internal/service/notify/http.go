package notify

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"

	"github.com/spruce/lexicode/internal/domain"
	"github.com/spruce/lexicode/internal/kernel/auth"
	"github.com/spruce/lexicode/internal/kernel/httpx"
	"github.com/spruce/lexicode/internal/kernel/store"
)

// Routes registers the notification endpoints (contracts §5):
//
//	GET  /api/v1/notifications                 the caller's rows + unread count (badge)
//	POST /api/v1/notifications/{id}/read
//	POST /api/v1/notifications/{id}/dismiss
//
// Notifications are personal: every route resolves rows through the session user, and a
// row belonging to someone else is a 404, not a 403 — existence is not leaked.
func (s *Service) Routes(mux httpx.Registrar, a *auth.Service) {
	authed := func(h func(http.ResponseWriter, *http.Request, domain.User)) http.Handler {
		return a.RequireAuth(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			u, ok := auth.UserFrom(r.Context())
			if !ok {
				httpx.WriteProblem(w, http.StatusUnauthorized, "unauthenticated",
					"Not authenticated", "Sign in to use this endpoint.")
				return
			}
			h(w, r, u)
		}))
	}
	mux.Handle("GET /api/v1/notifications", authed(s.handleList))
	mux.Handle("POST /api/v1/notifications/{id}/read", authed(s.handleRead))
	mux.Handle("POST /api/v1/notifications/{id}/dismiss", authed(s.handleDismiss))
}

// notificationBody is the wire shape of one notification.
func notificationBody(n domain.Notification) map[string]any {
	return map[string]any{
		"id": n.ID, "user_id": n.UserID, "project_id": n.ProjectID, "run_id": n.RunID,
		"flavor": string(n.Flavor), "title": n.Title, "body": n.Body,
		"state": string(n.State), "created_at": n.CreatedAt, "updated_at": n.UpdatedAt,
	}
}

func (s *Service) handleList(w http.ResponseWriter, r *http.Request, u domain.User) {
	rows, err := s.st.Notifications().ForUser(r.Context(), u.ID)
	if err != nil {
		s.writeError(w, err)
		return
	}
	unread, err := s.st.Notifications().UnreadCount(r.Context(), u.ID)
	if err != nil {
		s.writeError(w, err)
		return
	}
	keys := map[string]string{}
	out := make([]map[string]any, 0, len(rows))
	for _, n := range rows {
		body := notificationBody(n)
		key, ok := keys[n.ProjectID]
		if !ok {
			if p, err := s.st.Projects().ByID(r.Context(), n.ProjectID); err == nil {
				key = p.Key
			}
			keys[n.ProjectID] = key
		}
		body["project_key"] = key
		out = append(out, body)
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{
		"notifications": out, "unread": unread,
	})
}

func (s *Service) handleRead(w http.ResponseWriter, r *http.Request, u domain.User) {
	s.setState(w, r, u, domain.NotificationRead)
}

func (s *Service) handleDismiss(w http.ResponseWriter, r *http.Request, u domain.User) {
	s.setState(w, r, u, domain.NotificationDismissed)
}

func (s *Service) setState(w http.ResponseWriter, r *http.Request, u domain.User, state domain.NotificationState) {
	n, err := s.st.Notifications().ByID(r.Context(), r.PathValue("id"))
	if err != nil || n.UserID != u.ID {
		if err == nil || errors.Is(err, store.ErrNotFound) {
			httpx.WriteProblem(w, http.StatusNotFound, httpx.TypeNotFound,
				"Not found", "No notification matches this path.")
			return
		}
		s.writeError(w, err)
		return
	}
	if err := s.st.Notifications().MarkState(r.Context(), n.ID, state,
		domain.FormatTime(s.now())); err != nil {
		s.writeError(w, err)
		return
	}
	after, err := s.st.Notifications().ByID(r.Context(), n.ID)
	if err != nil {
		s.writeError(w, err)
		return
	}
	s.emitUpdated(r.Context(), after)
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"notification": notificationBody(after)})
}

func (s *Service) writeError(w http.ResponseWriter, err error) {
	if errors.Is(err, store.ErrNotFound) {
		httpx.WriteProblem(w, http.StatusNotFound, httpx.TypeNotFound,
			"Not found", "Nothing matches this path.")
		return
	}
	s.logger.Error("notify: request failed", "error", err.Error())
	httpx.WriteProblem(w, http.StatusInternalServerError, httpx.TypeInternal,
		"Something went wrong", "The server could not complete this request.")
}

// mustJSON marshals a value the service controls; a failure is rendered honestly rather
// than panicking.
func mustJSON(v any) json.RawMessage {
	b, err := json.Marshal(v)
	if err != nil {
		return json.RawMessage(fmt.Sprintf(`{"marshal_error":%q}`, err.Error()))
	}
	return b
}
