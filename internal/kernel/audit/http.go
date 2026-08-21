package audit

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/spruce/lexicode/internal/domain"
	"github.com/spruce/lexicode/internal/kernel/httpx"
	"github.com/spruce/lexicode/internal/kernel/store"
)

// maxPage caps ?limit=. Deeper history pages through the cursor.
const maxPage = 200

// entryBody is how one audit entry renders on GET /api/v1/audit.
type entryBody struct {
	ID         string          `json:"id"`
	ProjectID  *string         `json:"project_id"`
	ActorKind  string          `json:"actor_kind"`
	ActorID    *string         `json:"actor_id"`
	Action     string          `json:"action"`
	TargetKind string          `json:"target_kind"`
	TargetID   string          `json:"target_id"`
	Before     json.RawMessage `json:"before"`
	After      json.RawMessage `json:"after"`
	Note       string          `json:"note,omitempty"`
	CreatedAt  string          `json:"created_at"`
}

// listBody is the response of GET /api/v1/audit. NextCursor is present exactly when the page
// was full; pass it back as ?cursor= for the next page.
type listBody struct {
	Entries    []entryBody `json:"entries"`
	NextCursor string      `json:"next_cursor,omitempty"`
}

// Handler serves GET /api/v1/audit with filters (contracts §5): ?project= (key or id),
// ?actor= ("human", or "human:<id>"), ?action=, ?target= (target kind), ?since= / ?until=
// (RFC3339), ?limit= and ?cursor= for keyset pagination. The kernel registers it behind
// RequireAuth + RequireOwner — the audit log names every user's actions and is the owner's
// surface only.
func (w *Writer) Handler() http.Handler {
	return http.HandlerFunc(w.handleList)
}

func (w *Writer) handleList(rw http.ResponseWriter, r *http.Request) {
	f, ok := w.filterFrom(rw, r)
	if !ok {
		return
	}
	entries, err := w.st.Audit().List(r.Context(), f)
	if err != nil {
		w.logger.Error("audit: list failed", slog.String("error", err.Error()))
		httpx.WriteProblem(rw, http.StatusInternalServerError, httpx.TypeInternal,
			"Internal error", "The audit log could not be read.")
		return
	}

	body := listBody{Entries: make([]entryBody, 0, len(entries))}
	for _, e := range entries {
		body.Entries = append(body.Entries, entryBody{
			ID: e.ID, ProjectID: e.ProjectID,
			ActorKind: string(e.ActorKind), ActorID: e.ActorID,
			Action: e.Action, TargetKind: e.TargetKind, TargetID: e.TargetID,
			Before: e.Before, After: e.After, Note: e.Note, CreatedAt: e.CreatedAt,
		})
	}
	if len(entries) == f.Limit {
		last := entries[len(entries)-1]
		body.NextCursor = last.CreatedAt + "~" + last.ID
	}
	httpx.WriteJSON(rw, http.StatusOK, body)
}

// filterFrom parses the query into a store filter, answering the 400s itself.
func (w *Writer) filterFrom(rw http.ResponseWriter, r *http.Request) (store.AuditFilter, bool) {
	q := r.URL.Query()
	f := store.AuditFilter{Limit: 50}
	bad := func(detail string) (store.AuditFilter, bool) {
		httpx.WriteProblem(rw, http.StatusBadRequest, httpx.TypeInvalidRequest,
			"Invalid audit filter", detail)
		return f, false
	}

	if p := q.Get("project"); p != "" {
		// The UI passes the project key ("PAY"); the log stores the id. Accept both.
		if proj, err := w.st.Projects().ByKey(r.Context(), p); err == nil {
			p = proj.ID
		} else if !errors.Is(err, store.ErrNotFound) {
			httpx.WriteProblem(rw, http.StatusInternalServerError, httpx.TypeInternal,
				"Internal error", "The project filter could not be resolved.")
			return f, false
		}
		f.ProjectID = p
	}
	if a := q.Get("actor"); a != "" {
		kind, id, _ := strings.Cut(a, ":")
		if !domain.ActorKind(kind).IsValid() {
			return bad("?actor= must be one of human, agent, trigger, system — optionally with :<id>.")
		}
		f.ActorKind, f.ActorID = kind, id
	}
	f.Action = q.Get("action")
	f.TargetKind = q.Get("target")
	if s := q.Get("since"); s != "" {
		t, err := time.Parse(time.RFC3339, s)
		if err != nil {
			return bad("?since= must be an RFC3339 timestamp.")
		}
		f.Since = domain.FormatTime(t)
	}
	if u := q.Get("until"); u != "" {
		t, err := time.Parse(time.RFC3339, u)
		if err != nil {
			return bad("?until= must be an RFC3339 timestamp.")
		}
		f.Until = domain.FormatTime(t)
	}
	if l := q.Get("limit"); l != "" {
		n, err := strconv.Atoi(l)
		if err != nil || n < 1 || n > maxPage {
			return bad("?limit= must be a number between 1 and " + strconv.Itoa(maxPage) + ".")
		}
		f.Limit = n
	}
	if c := q.Get("cursor"); c != "" {
		createdAt, id, ok := strings.Cut(c, "~")
		if !ok || createdAt == "" || id == "" {
			return bad("?cursor= is not a cursor this endpoint issued; use next_cursor verbatim.")
		}
		f.BeforeCreatedAt, f.BeforeID = createdAt, id
	}
	return f, true
}
