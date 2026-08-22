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

// triageColsPrefixed is triageCols qualified with the `ti` alias, for queries that join
// tickets (whose id/created_at would otherwise be ambiguous).
const triageColsPrefixed = `ti.id, ti.ticket_id, ti.provenance, ti.source_trigger_id,
	ti.source_run_id, ti.state, ti.duplicate_of, ti.reason, ti.snooze_until,
	ti.resolved_by, ti.resolved_at, ti.created_at`

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

// ByID returns one triage item, or ErrNotFound.
func (r *TriageRepo) ByID(ctx context.Context, id string) (domain.TriageItem, error) {
	return scanTriageItem(r.h.r.QueryRowContext(ctx,
		`SELECT `+triageCols+` FROM triage_items WHERE id = ?`, id))
}

// Update rewrites a triage item's mutable columns: state, duplicate_of, reason,
// snooze_until and the resolution stamp. Provenance and the source references are
// immutable history — an UPDATE never touches them.
func (r *TriageRepo) Update(ctx context.Context, it *domain.TriageItem) error {
	res, err := r.h.w.ExecContext(ctx, `
		UPDATE triage_items
		SET state = ?, duplicate_of = ?, reason = ?, snooze_until = ?,
		    resolved_by = ?, resolved_at = ?
		WHERE id = ?`,
		string(it.State), nullStr(it.DuplicateOf), it.Reason, nullStr(it.SnoozeUntil),
		nullStr(it.ResolvedBy), nullStr(it.ResolvedAt), it.ID)
	if err != nil {
		return mapErr(err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

// UnresolvedForProject returns a project's unresolved triage items — the S31 queue:
// `pending` first, then `snoozed`, each oldest first. Items whose ticket was archived out
// from under them are excluded; there is nothing left to triage.
func (r *TriageRepo) UnresolvedForProject(ctx context.Context, projectID string) ([]domain.TriageItem, error) {
	rows, err := r.h.r.QueryContext(ctx, `
		SELECT `+triageColsPrefixed+` FROM triage_items ti
		JOIN tickets t ON t.id = ti.ticket_id
		WHERE t.project_id = ? AND t.archived_at IS NULL
		  AND ti.state IN ('pending','snoozed')
		ORDER BY CASE ti.state WHEN 'pending' THEN 0 ELSE 1 END, ti.created_at, ti.id`,
		projectID)
	if err != nil {
		return nil, mapErr(err)
	}
	defer func() { _ = rows.Close() }()
	return collectTriageItems(rows)
}

// CountPending returns how many `pending` items a project has — the triage tab badge.
// Snoozed items are deliberately excluded: the badge counts actionable work only (UI spec
// §2.1), and a snoozed item is parked by explicit choice.
func (r *TriageRepo) CountPending(ctx context.Context, projectID string) (int64, error) {
	var n int64
	err := r.h.r.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM triage_items ti
		JOIN tickets t ON t.id = ti.ticket_id
		WHERE t.project_id = ? AND t.archived_at IS NULL AND ti.state = 'pending'`,
		projectID).Scan(&n)
	return n, mapErr(err)
}

// SnoozedDue returns every time-snoozed item whose snooze_until has passed (RFC3339 UTC
// strings compare lexicographically) — the ticker's scan, across all projects.
func (r *TriageRepo) SnoozedDue(ctx context.Context, now string) ([]domain.TriageItem, error) {
	rows, err := r.h.r.QueryContext(ctx, `
		SELECT `+triageColsPrefixed+` FROM triage_items ti
		JOIN tickets t ON t.id = ti.ticket_id
		WHERE t.archived_at IS NULL AND ti.state = 'snoozed'
		  AND ti.snooze_until IS NOT NULL AND ti.snooze_until <= ?
		ORDER BY ti.snooze_until, ti.id`, now)
	if err != nil {
		return nil, mapErr(err)
	}
	defer func() { _ = rows.Close() }()
	return collectTriageItems(rows)
}

// SnoozedUntilActivity returns a project's snoozed-until-activity items (state `snoozed`,
// snooze_until NULL) — the wake subscriber's candidates for one incoming event.
func (r *TriageRepo) SnoozedUntilActivity(ctx context.Context, projectID string) ([]domain.TriageItem, error) {
	rows, err := r.h.r.QueryContext(ctx, `
		SELECT `+triageColsPrefixed+` FROM triage_items ti
		JOIN tickets t ON t.id = ti.ticket_id
		WHERE t.project_id = ? AND t.archived_at IS NULL
		  AND ti.state = 'snoozed' AND ti.snooze_until IS NULL
		ORDER BY ti.created_at, ti.id`, projectID)
	if err != nil {
		return nil, mapErr(err)
	}
	defer func() { _ = rows.Close() }()
	return collectTriageItems(rows)
}

func collectTriageItems(rows *sql.Rows) ([]domain.TriageItem, error) {
	var out []domain.TriageItem
	for rows.Next() {
		it, err := scanTriageItem(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, it)
	}
	return out, mapErr(rows.Err())
}
