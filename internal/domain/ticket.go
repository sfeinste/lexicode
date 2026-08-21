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

// Criterion is a row of acceptance_criteria — one ordered checklist item on a ticket. Position
// is a gapped integer (same scheme as board columns); Checked carries attribution: a human check
// stamps CheckedByUserID, an agent check stamps CheckedByRunID (the run is the agent's identity
// for the act). At most one of the two is set.
type Criterion struct {
	ID              string
	TicketID        string
	Position        int64
	Text            string
	Checked         bool
	CheckedByRunID  *string
	CheckedByUserID *string
	Note            string
	UpdatedAt       string
}

// Label is a row of labels — a project-scoped name + colour. Tickets reference labels through
// ticket_labels; a label has no cross-project meaning.
type Label struct {
	ID        string
	ProjectID string
	Name      string
	Color     string
}
