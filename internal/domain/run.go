package domain

import "encoding/json"

// Run is a row of runs — one agent session. Autonomy, DirectiveVersionID, Model, Effort and
// Prompt are snapshots taken at launch: editing an agent never mutates a past run's record of
// what it was told (data model §10.6).
type Run struct {
	ID                 string
	Seq                int64
	ProjectID          string
	AgentID            string
	TicketID           *string
	TriggerID          *string
	CauseEventID       *string
	ParentRunID        *string
	RequestedByUserID  *string
	State              RunState
	StateReason        string
	HoldReason         string
	Autonomy           Autonomy
	DirectiveVersionID *string
	Model              string
	Effort             string
	Prompt             string
	RuntimeID          string
	SandboxID          string
	ContainerID        *string
	InstanceID         *string
	LogOffset          int64
	Branch             *string
	BaseSHA            *string
	Depth              int64
	SubjectKey         string
	CurrentStep        string
	CostCents          int64
	TokensIn           int64
	TokensOut          int64
	TokensCacheRead    int64
	TokensCacheWrite   int64
	StepCount          int64
	ErrorMessage       string
	TakeoverNote       string
	QueuedAt           string
	StartedAt          *string
	EndedAt            *string
	AcknowledgedAt     *string
}

// UsageDelta is one increment of a run's token/cost rollup, reported by the runtime adapter
// as its stream provides usage (contracts §2.4). Fields mirror the runs usage columns; costs
// are integer cents.
type UsageDelta struct {
	TokensIn         int64
	TokensOut        int64
	TokensCacheRead  int64
	TokensCacheWrite int64
	CostCents        int64
}

// Add returns the field-wise sum of two deltas.
func (u UsageDelta) Add(v UsageDelta) UsageDelta {
	return UsageDelta{
		TokensIn:         u.TokensIn + v.TokensIn,
		TokensOut:        u.TokensOut + v.TokensOut,
		TokensCacheRead:  u.TokensCacheRead + v.TokensCacheRead,
		TokensCacheWrite: u.TokensCacheWrite + v.TokensCacheWrite,
		CostCents:        u.CostCents + v.CostCents,
	}
}

// Elicitation is a row of elicitations — one blocking question or approval a run asked a
// human, durable across restarts by design (architecture §10.6).
type Elicitation struct {
	ID          string
	RunID       string
	ActivitySeq int64
	Kind        ElicitationKind
	Request     json.RawMessage
	State       ElicitationState
	Response    json.RawMessage
	RespondedBy *string
	RespondedAt *string
	CreatedAt   string
}

// RunOutput is a row of run_outputs — one artifact a run produced: a branch, a PR, a comment,
// a review, a wiki proposal, a ticket, or preserved partial work. The forge adapter records one
// for every successful write (contracts §2.2).
type RunOutput struct {
	ID        string
	RunID     string
	Kind      RunOutputKind
	Ref       string // the artifact's stable reference: PR number, comment ID, branch name
	URL       string
	Summary   string
	CreatedAt string
}

// RunMessage is a row of run_messages — one queued steering message ("queue, don't
// interrupt"). delivered_at is stamped when the adapter accepts the message for delivery
// between tool calls.
type RunMessage struct {
	ID          string
	RunID       string
	Body        string
	AuthorID    *string
	State       RunMessageState
	CreatedAt   string
	DeliveredAt *string
}

// RunContextItem is a row of run_context_items — one labelled piece of the context stack a run
// was given, exactly what the run detail's Context panel renders (architecture §11).
type RunContextItem struct {
	ID         string
	RunID      string
	Provider   string
	SourceKind string
	SourceRef  string
	Title      string
	Reason     string
	Tokens     int64
	Position   int64
	Injected   bool
}

// Activity is a row of activities — one step of a run's transcript, keyed (run_id, seq).
// Payload is typed per tool by the runtime adapter (S19); the store carries it raw.
type Activity struct {
	RunID      string
	Seq        int64
	Type       ActivityType
	Level      int64
	ToolName   string
	GroupKey   string
	Title      string
	Payload    json.RawMessage
	OK         *bool
	Attempt    int64
	DurationMS *int64
	QueuedMS   *int64
	ModelMS    *int64
	ToolMS     *int64
	CostCents  int64
	TokensIn   int64
	TokensOut  int64
	CreatedAt  string
}
