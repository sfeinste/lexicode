package tickets

import (
	"context"
	"encoding/json"
	"log/slog"

	"github.com/spruce/lexicode/internal/domain"
	"github.com/spruce/lexicode/internal/kernel/auth"
	"github.com/spruce/lexicode/internal/kernel/store"
)

// appendStream writes one ticket_stream row inside the mutation's transaction, with actor
// attribution from the request context — the same source the audit writer reads, so the two
// records can never disagree about who acted. Payloads carry an "event" verb plus details;
// the vocabulary is documented in the package comment's stream section.
func (s *Service) appendStream(ctx context.Context, tx *store.Tx, ticketID string, kind domain.StreamKind, payload map[string]any) error {
	raw, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	e := domain.StreamEntry{
		ID:        domain.NewID(),
		TicketID:  ticketID,
		Kind:      kind,
		ActorKind: domain.ActorSystem,
		Payload:   raw,
		CreatedAt: s.now(),
	}
	if a, ok := auth.ActorFrom(ctx); ok {
		e.ActorKind = a.Kind
		if a.ID != "" {
			id := a.ID
			e.ActorID = &id
		}
	}
	return tx.TicketStream().Append(ctx, &e)
}

// emitTicket publishes a `ticket` bus event (SSE type "ticket.<activity>", contracts §5.1) with
// the contracts §4 normalized ticket payload, plus any event-specific extras. Best-effort: the
// mutation is committed and audited by the time this runs, so a failure is logged, never
// unwound.
func (s *Service) emitTicket(ctx context.Context, activity string, tk domain.Ticket, extra map[string]any) {
	if s.bus == nil {
		return
	}
	body := map[string]any{"ticket": s.normalizedTicket(ctx, tk)}
	for k, v := range extra {
		body[k] = v
	}
	payload, err := json.Marshal(body)
	if err != nil {
		s.logger.Error("tickets: marshal event payload failed", slog.String("error", err.Error()))
		return
	}
	pid, tid := tk.ProjectID, tk.ID
	e := domain.Event{
		ProjectID: &pid, Kind: "ticket", ActivityType: activity,
		SubjectKind: "ticket", SubjectID: &tid,
		Payload: payload, OccurredAt: s.now(),
	}
	s.stampActor(ctx, &e)
	if err := s.bus.Emit(ctx, e); err != nil {
		s.logger.Error("tickets: emit failed",
			slog.String("kind", "ticket."+activity), slog.String("error", err.Error()))
	}
}

// emitLabel publishes a `label` bus event for label CRUD, project-scoped.
func (s *Service) emitLabel(ctx context.Context, activity string, l domain.Label) {
	if s.bus == nil {
		return
	}
	payload, _ := json.Marshal(map[string]any{
		"label": map[string]string{"id": l.ID, "name": l.Name, "color": l.Color},
	})
	pid, lid := l.ProjectID, l.ID
	e := domain.Event{
		ProjectID: &pid, Kind: "label", ActivityType: activity,
		SubjectKind: "label", SubjectID: &lid,
		Payload: payload, OccurredAt: s.now(),
	}
	s.stampActor(ctx, &e)
	if err := s.bus.Emit(ctx, e); err != nil {
		s.logger.Error("tickets: emit failed",
			slog.String("kind", "label."+activity), slog.String("error", err.Error()))
	}
}

func (s *Service) stampActor(ctx context.Context, e *domain.Event) {
	if a, ok := auth.ActorFrom(ctx); ok {
		e.ActorKind = a.Kind
		if a.ID != "" {
			id := a.ID
			e.ActorID = &id
		}
	}
}

// normalizedTicket renders the contracts §4 `ticket` payload sub-object: the user-visible
// field vocabulary trigger conditions and {{...}} interpolation address. `column` is the
// display name — data for humans and templates; automation still keys off `category`.
// Best-effort: a lookup failure leaves the field empty rather than losing the event.
func (s *Service) normalizedTicket(ctx context.Context, tk domain.Ticket) map[string]any {
	out := map[string]any{
		"key":      tk.Key,
		"title":    tk.Title,
		"column":   "",
		"category": "",
		"priority": string(tk.Priority),
		"assignee": "",
		"delegate": "",
		"labels":   []string{},
	}
	if col, err := s.st.Columns().ByID(ctx, tk.ColumnID); err == nil {
		out["column"] = col.Name
		out["category"] = string(col.Category)
	}
	if tk.AssigneeID != nil {
		if u, err := s.st.Users().ByID(ctx, *tk.AssigneeID); err == nil {
			out["assignee"] = u.DisplayName
		}
	}
	if tk.DelegateAgentID != nil {
		if a, err := s.st.Agents().ByID(ctx, *tk.DelegateAgentID); err == nil {
			out["delegate"] = a.Name
		}
	}
	if labels, err := s.st.Labels().ForTicket(ctx, tk.ID); err == nil {
		names := make([]string, len(labels))
		for i, l := range labels {
			names[i] = l.Name
		}
		out["labels"] = names
	}
	return out
}
