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
