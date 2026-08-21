package store

import (
	"context"
	"database/sql"
	"encoding/json"

	"github.com/spruce/lexicode/internal/domain"
)

// TicketStreamRepo appends to and reads the unified ticket stream (data model §4.1). There is
// deliberately no per-kind query: the ticket detail is one chronological read.
type TicketStreamRepo struct{ h handle }

// TicketStream returns the ticket-stream repository.
func (s *Store) TicketStream() *TicketStreamRepo { return &TicketStreamRepo{h: s.handle()} }

// TicketStream returns the ticket-stream repository bound to this transaction.
func (t *Tx) TicketStream() *TicketStreamRepo { return &TicketStreamRepo{h: t.handle()} }

const streamCols = `id, ticket_id, kind, actor_kind, actor_id, body, payload, run_id,
	edited_at, created_at`

// Append inserts one stream entry.
func (r *TicketStreamRepo) Append(ctx context.Context, e *domain.StreamEntry) error {
	_, err := r.h.w.ExecContext(ctx, `
		INSERT INTO ticket_stream (`+streamCols+`)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		e.ID, e.TicketID, string(e.Kind), string(e.ActorKind), nullStr(e.ActorID),
		e.Body, rawText(e.Payload, "{}"), nullStr(e.RunID), nullStr(e.EditedAt), e.CreatedAt)
	return mapErr(err)
}

// ForTicket returns a ticket's whole stream, oldest first — the shape the detail view renders.
func (r *TicketStreamRepo) ForTicket(ctx context.Context, ticketID string) ([]domain.StreamEntry, error) {
	rows, err := r.h.r.QueryContext(ctx, `
		SELECT `+streamCols+` FROM ticket_stream
		WHERE ticket_id = ? ORDER BY created_at, id`, ticketID)
	if err != nil {
		return nil, mapErr(err)
	}
	defer func() { _ = rows.Close() }()

	var out []domain.StreamEntry
	for rows.Next() {
		var (
			e                      domain.StreamEntry
			kind, actorKind        string
			actorID, runID, edited sql.NullString
			payload                string
		)
		err := rows.Scan(&e.ID, &e.TicketID, &kind, &actorKind, &actorID, &e.Body, &payload,
			&runID, &edited, &e.CreatedAt)
		if err != nil {
			return nil, mapErr(err)
		}
		e.Kind = domain.StreamKind(kind)
		e.ActorKind = domain.ActorKind(actorKind)
		e.ActorID = strPtr(actorID)
		e.RunID = strPtr(runID)
		e.EditedAt = strPtr(edited)
		e.Payload = json.RawMessage(payload)
		out = append(out, e)
	}
	return out, rows.Err()
}
