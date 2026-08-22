package store

import (
	"context"
	"database/sql"

	"github.com/spruce/lexicode/internal/domain"
)

// TriageRepo reads and writes triage_items — the gate between automated ticket creation and
// the board (data model §4). UNIQUE(ticket_id) means a ticket has at most one triage item;
// §10.7's board invariant is enforced in TicketsRepo.ForProject, not in six callers. S28
// ships Create and ByTicket (the create_ticket action and the move_ticket guard); the triage
// list surface and the resolution verbs arrive with S31.
type TriageRepo struct{ h handle }

// Triage returns the triage-items repository.
func (s *Store) Triage() *TriageRepo { return &TriageRepo{h: s.handle()} }

// Triage returns the triage-items repository bound to this transaction.
func (t *Tx) Triage() *TriageRepo { return &TriageRepo{h: t.handle()} }

const triageCols = `id, ticket_id, provenance, source_trigger_id, source_run_id, state,
	duplicate_of, reason, snooze_until, resolved_by, resolved_at, created_at`

// Create inserts a triage item. A second item for the same ticket surfaces as ErrUnique.
func (r *TriageRepo) Create(ctx context.Context, it *domain.TriageItem) error {
	_, err := r.h.w.ExecContext(ctx, `
		INSERT INTO triage_items (`+triageCols+`)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		it.ID, it.TicketID, it.Provenance, nullStr(it.SourceTriggerID), nullStr(it.SourceRunID),
		string(it.State), nullStr(it.DuplicateOf), it.Reason, nullStr(it.SnoozeUntil),
		nullStr(it.ResolvedBy), nullStr(it.ResolvedAt), it.CreatedAt)
	return mapErr(err)
}

// ByTicket returns the ticket's triage item, or ErrNotFound — most tickets never had one.
func (r *TriageRepo) ByTicket(ctx context.Context, ticketID string) (domain.TriageItem, error) {
	return scanTriageItem(r.h.r.QueryRowContext(ctx,
		`SELECT `+triageCols+` FROM triage_items WHERE ticket_id = ?`, ticketID))
}

func scanTriageItem(row rowScanner) (domain.TriageItem, error) {
	var (
		it                                                 domain.TriageItem
		trigID, runID, dup, snooze, resolvedBy, resolvedAt sql.NullString
		state                                              string
	)
	err := row.Scan(&it.ID, &it.TicketID, &it.Provenance, &trigID, &runID, &state,
		&dup, &it.Reason, &snooze, &resolvedBy, &resolvedAt, &it.CreatedAt)
	if err != nil {
		return domain.TriageItem{}, mapErr(err)
	}
	it.SourceTriggerID = strPtr(trigID)
	it.SourceRunID = strPtr(runID)
	it.State = domain.TriageState(state)
	it.DuplicateOf = strPtr(dup)
	it.SnoozeUntil = strPtr(snooze)
	it.ResolvedBy = strPtr(resolvedBy)
	it.ResolvedAt = strPtr(resolvedAt)
	return it, nil
}
