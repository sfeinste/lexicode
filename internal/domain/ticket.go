package domain

import "encoding/json"

// Ticket is a row of tickets. AssigneeID is the accountable human, DelegateAgentID the working
// agent (D1) — they are different axes, not alternatives.
type Ticket struct {
	ID               string
	ProjectID        string
	Seq              int64
	Key              string
	Title            string
	Description      string
	ColumnID         string
	Position         float64
	Priority         Priority
	AssigneeID       *string
	DelegateAgentID  *string
	ParentID         *string
	PRNumber         *int64
	PRState          *string
	PRChecks         *string
	PRAdditions      *int64
	PRDeletions      *int64
	Branch           *string
	Origin           TicketOrigin
	CreatedByUserID  *string
	CreatedByAgentID *string
	ArchivedAt       *string
	CreatedAt        string
	UpdatedAt        string
}

// StreamEntry is a row of ticket_stream — one moment in a ticket's single chronological history
// (data model §4.1). Payload's shape varies by Kind and is defined by the stories that write
// each kind; until then it stays raw here.
type StreamEntry struct {
	ID        string
	TicketID  string
	Kind      StreamKind
	ActorKind ActorKind
	ActorID   *string
	Body      string
	Payload   json.RawMessage
	RunID     *string
	EditedAt  *string
	CreatedAt string
}
