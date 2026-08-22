// triagehttp.go is the HTTP surface of the S31 triage queue (contracts §5):
//
//	GET  /api/v1/projects/{key}/triage       the queue: unresolved items + counts
//	POST /api/v1/triage/{id}/accept
//	POST /api/v1/triage/{id}/duplicate       {of_ticket_id}
//	POST /api/v1/triage/{id}/decline         {reason?}
//	POST /api/v1/triage/{id}/snooze          {until?: RFC3339 | null}
//
// Registered from Routes (http.go) so the whole tickets surface mounts in one call.
package tickets

import (
	"errors"
	"net/http"

	"github.com/spruce/lexicode/internal/domain"
	"github.com/spruce/lexicode/internal/kernel/auth"
	"github.com/spruce/lexicode/internal/kernel/httpx"
)

// triageRoutes registers the queue endpoints; called from Routes.
func (s *Service) triageRoutes(mux httpx.Registrar, a *auth.Service) {
	member := func(h http.HandlerFunc) http.Handler {
		return a.RequireAuth(a.RequireProjectMember(h))
	}
	viaItem := func(h http.HandlerFunc) http.Handler {
		return a.RequireAuth(s.requireMember(a, s.projectOfTriageItem, h))
	}
	mux.Handle("GET /api/v1/projects/{key}/triage", member(s.handleTriageList))
	mux.Handle("POST /api/v1/triage/{id}/accept", viaItem(s.handleTriageAccept))
	mux.Handle("POST /api/v1/triage/{id}/duplicate", viaItem(s.handleTriageDuplicate))
	mux.Handle("POST /api/v1/triage/{id}/decline", viaItem(s.handleTriageDecline))
	mux.Handle("POST /api/v1/triage/{id}/snooze", viaItem(s.handleTriageSnooze))
}

func (s *Service) projectOfTriageItem(r *http.Request) (string, error) {
	it, err := s.st.Triage().ByID(r.Context(), r.PathValue("id"))
	if err != nil {
		return "", err
	}
	tk, err := s.st.Tickets().ByID(r.Context(), it.TicketID)
	return tk.ProjectID, err
}

// ---------------------------------------------------------------- bodies -----

// triageItemBody is one triage row: the item plus its ticket. Provenance is the verbatim
// human sentence the queue renders on every row.
type triageItemBody struct {
	ID              string     `json:"id"`
	TicketID        string     `json:"ticket_id"`
	Provenance      string     `json:"provenance"`
	SourceTriggerID *string    `json:"source_trigger_id"`
	SourceRunID     *string    `json:"source_run_id"`
	State           string     `json:"state"`
	DuplicateOf     *string    `json:"duplicate_of"`
	Reason          string     `json:"reason"`
	SnoozeUntil     *string    `json:"snooze_until"`
	ResolvedBy      *string    `json:"resolved_by"`
	ResolvedAt      *string    `json:"resolved_at"`
	CreatedAt       string     `json:"created_at"`
	Ticket          ticketBody `json:"ticket"`
}

func toTriageItemBody(li TriageListItem) triageItemBody {
	it := li.Item
	return triageItemBody{
		ID: it.ID, TicketID: it.TicketID, Provenance: it.Provenance,
		SourceTriggerID: it.SourceTriggerID, SourceRunID: it.SourceRunID,
		State: string(it.State), DuplicateOf: it.DuplicateOf, Reason: it.Reason,
		SnoozeUntil: it.SnoozeUntil, ResolvedBy: it.ResolvedBy, ResolvedAt: it.ResolvedAt,
		CreatedAt: it.CreatedAt, Ticket: toTicketBody(li.Ticket),
	}
}

// triageListBody is the queue: unresolved items (pending first, then snoozed, oldest first)
// plus the two counts. `pending_count` is the tab badge — actionable only; snoozed items
// never count toward it (UI spec §2.1).
type triageListBody struct {
	Items        []triageItemBody `json:"items"`
	PendingCount int64            `json:"pending_count"`
	SnoozedCount int64            `json:"snoozed_count"`
}

// ---------------------------------------------------------------- handlers -----

func (s *Service) handleTriageList(w http.ResponseWriter, r *http.Request) {
	items, err := s.TriageQueue(r.Context(), r.PathValue("key"))
	if err != nil {
		s.writeError(w, err)
		return
	}
	body := triageListBody{Items: make([]triageItemBody, 0, len(items))}
	for _, li := range items {
		body.Items = append(body.Items, toTriageItemBody(li))
		switch li.Item.State {
		case domain.TriagePending:
			body.PendingCount++
		case domain.TriageSnoozed:
			body.SnoozedCount++
		}
	}
	httpx.WriteJSON(w, http.StatusOK, body)
}

func (s *Service) handleTriageAccept(w http.ResponseWriter, r *http.Request) {
	li, err := s.TriageAccept(r.Context(), r.PathValue("id"))
	if err != nil {
		s.writeTriageError(w, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, toTriageItemBody(li))
}

type triageDuplicateBody struct {
	OfTicketID string `json:"of_ticket_id"`
}

func (s *Service) handleTriageDuplicate(w http.ResponseWriter, r *http.Request) {
	body, ok := httpx.DecodeJSON[triageDuplicateBody](w, r)
	if !ok {
		return
	}
	li, err := s.TriageDuplicate(r.Context(), r.PathValue("id"), body.OfTicketID)
	if err != nil {
		s.writeTriageError(w, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, toTriageItemBody(li))
}

type triageDeclineBody struct {
	Reason string `json:"reason"`
}

func (s *Service) handleTriageDecline(w http.ResponseWriter, r *http.Request) {
	body, ok := httpx.DecodeJSON[triageDeclineBody](w, r)
	if !ok {
		return
	}
	li, err := s.TriageDecline(r.Context(), r.PathValue("id"), body.Reason)
	if err != nil {
		s.writeTriageError(w, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, toTriageItemBody(li))
}

// triageSnoozeBody distinguishes absent from null the same way either way: both mean
// "until new activity"; a string means "until this instant".
type triageSnoozeBody struct {
	Until *string `json:"until"`
}

func (s *Service) handleTriageSnooze(w http.ResponseWriter, r *http.Request) {
	body, ok := httpx.DecodeJSON[triageSnoozeBody](w, r)
	if !ok {
		return
	}
	li, err := s.TriageSnooze(r.Context(), r.PathValue("id"), body.Until)
	if err != nil {
		s.writeTriageError(w, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, toTriageItemBody(li))
}

// writeTriageError adds the triage-specific 409 on top of the shared mapping.
func (s *Service) writeTriageError(w http.ResponseWriter, err error) {
	var resolved *TriageResolvedError
	if errors.As(err, &resolved) {
		httpx.WriteProblem(w, http.StatusConflict, "triage_resolved",
			"Already resolved",
			"This triage item was already resolved ("+string(resolved.State)+"); refresh the queue.")
		return
	}
	s.writeError(w, err)
}
