package domain

// TriageItem is a row of triage_items (data model §4): the gate between automated ticket
// creation and the board. A ticket with an unresolved item (state `pending`, or `snoozed` —
// snoozing defers triage, it does not grant board entry) is invisible to the board and to
// `move_ticket` trigger actions (§10.7). Provenance is the human sentence the triage list
// renders on every row — "Created by trigger `CI failed → file a ticket` from run #482".
type TriageItem struct {
	ID              string
	TicketID        string
	Provenance      string
	SourceTriggerID *string
	SourceRunID     *string
	State           TriageState
	DuplicateOf     *string
	Reason          string
	SnoozeUntil     *string // null + state='snoozed' = "until new activity"
	ResolvedBy      *string
	ResolvedAt      *string
	CreatedAt       string
}

// Unresolved reports whether the item still keeps its ticket off the board: the ticket has
// not been accepted, merged as a duplicate or declined yet.
func (t TriageItem) Unresolved() bool {
	return t.State == TriagePending || t.State == TriageSnoozed
}
