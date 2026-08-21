package domain

// Every enumeration in this file mirrors a CHECK constraint in migrations/0001_init.up.sql,
// value for value. IsValid answers "would the schema accept this string"; it exists so that a
// service can reject bad input with a named error before SQLite rejects it with a generic one.

// UserRole is users.role.
type UserRole string

const (
	RoleOwner  UserRole = "owner"
	RoleMember UserRole = "member"
)

// IsValid reports whether the value is one the schema accepts.
func (r UserRole) IsValid() bool { return r == RoleOwner || r == RoleMember }

// SecretScope is secrets.scope.
type SecretScope string

const (
	SecretScopeWorkspace SecretScope = "workspace"
	SecretScopeProject   SecretScope = "project"
)

// IsValid reports whether the value is one the schema accepts.
func (s SecretScope) IsValid() bool { return s == SecretScopeWorkspace || s == SecretScopeProject }

// Autonomy is agents.autonomy, snapshotted onto runs.autonomy at launch.
type Autonomy string

const (
	AutonomySuggest     Autonomy = "suggest"
	AutonomyApproveEach Autonomy = "approve_each"
	AutonomyAutoGates   Autonomy = "auto_gates"
	AutonomyAuto        Autonomy = "auto"
)

// IsValid reports whether the value is one the schema accepts.
func (a Autonomy) IsValid() bool {
	switch a {
	case AutonomySuggest, AutonomyApproveEach, AutonomyAutoGates, AutonomyAuto:
		return true
	}
	return false
}

// PermissionDecision is agent_permission_rules.decision.
type PermissionDecision string

const (
	DecisionAllow PermissionDecision = "allow"
	DecisionDeny  PermissionDecision = "deny"
	DecisionAsk   PermissionDecision = "ask"
)

// IsValid reports whether the value is one the schema accepts.
func (d PermissionDecision) IsValid() bool {
	return d == DecisionAllow || d == DecisionDeny || d == DecisionAsk
}

// ColumnCategory is columns.category — the stable key automation reads. Code never references a
// column by its user-facing name (plan rule 3); it references this.
type ColumnCategory string

const (
	CategoryBacklog  ColumnCategory = "backlog"
	CategoryReady    ColumnCategory = "ready"
	CategoryRunning  ColumnCategory = "running"
	CategoryReview   ColumnCategory = "review"
	CategoryDone     ColumnCategory = "done"
	CategoryCanceled ColumnCategory = "canceled"
)

// IsValid reports whether the value is one the schema accepts.
func (c ColumnCategory) IsValid() bool {
	switch c {
	case CategoryBacklog, CategoryReady, CategoryRunning, CategoryReview, CategoryDone, CategoryCanceled:
		return true
	}
	return false
}

// Priority is tickets.priority.
type Priority string

const (
	PriorityNone   Priority = "none"
	PriorityLow    Priority = "low"
	PriorityMedium Priority = "medium"
	PriorityHigh   Priority = "high"
	PriorityUrgent Priority = "urgent"
)

// IsValid reports whether the value is one the schema accepts.
func (p Priority) IsValid() bool {
	switch p {
	case PriorityNone, PriorityLow, PriorityMedium, PriorityHigh, PriorityUrgent:
		return true
	}
	return false
}

// TicketOrigin is tickets.origin.
type TicketOrigin string

const (
	OriginHuman   TicketOrigin = "human"
	OriginAgent   TicketOrigin = "agent"
	OriginTrigger TicketOrigin = "trigger"
	OriginImport  TicketOrigin = "import"
)

// IsValid reports whether the value is one the schema accepts.
func (o TicketOrigin) IsValid() bool {
	switch o {
	case OriginHuman, OriginAgent, OriginTrigger, OriginImport:
		return true
	}
	return false
}

// TriageState is triage_items.state.
type TriageState string

const (
	TriagePending   TriageState = "pending"
	TriageAccepted  TriageState = "accepted"
	TriageDuplicate TriageState = "duplicate"
	TriageDeclined  TriageState = "declined"
	TriageSnoozed   TriageState = "snoozed"
)

// IsValid reports whether the value is one the schema accepts.
func (s TriageState) IsValid() bool {
	switch s {
	case TriagePending, TriageAccepted, TriageDuplicate, TriageDeclined, TriageSnoozed:
		return true
	}
	return false
}

// StreamKind is ticket_stream.kind.
type StreamKind string

const (
	StreamComment      StreamKind = "comment"
	StreamStatusChange StreamKind = "status_change"
	StreamFieldChange  StreamKind = "field_change"
	StreamRun          StreamKind = "run"
	StreamPREvent      StreamKind = "pr_event"
	StreamTriggerFired StreamKind = "trigger_fired"
	StreamProposal     StreamKind = "proposal"
)

// IsValid reports whether the value is one the schema accepts.
func (k StreamKind) IsValid() bool {
	switch k {
	case StreamComment, StreamStatusChange, StreamFieldChange, StreamRun, StreamPREvent,
		StreamTriggerFired, StreamProposal:
		return true
	}
	return false
}

// ActorKind is who did a thing: ticket_stream.actor_kind and audit_log.actor_kind share it, and
// events.actor_kind uses the same vocabulary without a CHECK.
type ActorKind string

const (
	ActorHuman   ActorKind = "human"
	ActorAgent   ActorKind = "agent"
	ActorTrigger ActorKind = "trigger"
	ActorSystem  ActorKind = "system"
)

// IsValid reports whether the value is one the schema accepts.
func (k ActorKind) IsValid() bool {
	switch k {
	case ActorHuman, ActorAgent, ActorTrigger, ActorSystem:
		return true
	}
	return false
}

// WikiState is wiki_pages.state.
type WikiState string

const (
	WikiLive     WikiState = "live"
	WikiProposed WikiState = "proposed"
)

// IsValid reports whether the value is one the schema accepts.
func (s WikiState) IsValid() bool { return s == WikiLive || s == WikiProposed }

// AgentScope is wiki_pages.agent_scope — when a page is injected into agent context.
type AgentScope string

const (
	ScopeAlways AgentScope = "always"
	ScopeAuto   AgentScope = "auto"
	ScopePaths  AgentScope = "paths"
	ScopeManual AgentScope = "manual"
	ScopeNever  AgentScope = "never"
)

// IsValid reports whether the value is one the schema accepts.
func (s AgentScope) IsValid() bool {
	switch s {
	case ScopeAlways, ScopeAuto, ScopePaths, ScopeManual, ScopeNever:
		return true
	}
	return false
}

// MentionFromKind is mentions.from_kind.
type MentionFromKind string

const (
	MentionFromWiki    MentionFromKind = "wiki"
	MentionFromTicket  MentionFromKind = "ticket"
	MentionFromComment MentionFromKind = "comment"
)

// IsValid reports whether the value is one the schema accepts.
func (k MentionFromKind) IsValid() bool {
	return k == MentionFromWiki || k == MentionFromTicket || k == MentionFromComment
}

// MentionToKind is mentions.to_kind.
type MentionToKind string

const (
	MentionToWiki   MentionToKind = "wiki"
	MentionToTicket MentionToKind = "ticket"
	MentionToAgent  MentionToKind = "agent"
	MentionToUser   MentionToKind = "user"
)

// IsValid reports whether the value is one the schema accepts.
func (k MentionToKind) IsValid() bool {
	switch k {
	case MentionToWiki, MentionToTicket, MentionToAgent, MentionToUser:
		return true
	}
	return false
}

// FiringOutcome is trigger_firings.outcome. It is never collapsed to success/failure anywhere in
// the stack (data model §6).
type FiringOutcome string

const (
	FiringSucceeded        FiringOutcome = "succeeded"
	FiringNoAction         FiringOutcome = "no_action"
	FiringAwaitingApproval FiringOutcome = "awaiting_approval"
	FiringErrored          FiringOutcome = "errored"
	FiringDebounced        FiringOutcome = "debounced"
	FiringSuperseded       FiringOutcome = "superseded"
	FiringLoopStopped      FiringOutcome = "loop_stopped"
	FiringBudgetExceeded   FiringOutcome = "budget_exceeded"
)

// IsValid reports whether the value is one the schema accepts.
func (o FiringOutcome) IsValid() bool {
	switch o {
	case FiringSucceeded, FiringNoAction, FiringAwaitingApproval, FiringErrored,
		FiringDebounced, FiringSuperseded, FiringLoopStopped, FiringBudgetExceeded:
		return true
	}
	return false
}

// DispatchState is events.dispatch_state.
type DispatchState string

const (
	DispatchPending DispatchState = "pending"
	DispatchDone    DispatchState = "done"
	DispatchFailed  DispatchState = "failed"
)

// IsValid reports whether the value is one the schema accepts.
func (s DispatchState) IsValid() bool {
	return s == DispatchPending || s == DispatchDone || s == DispatchFailed
}

// RunState is runs.state. Only the scheduler writes it (data model §10.4).
type RunState string

const (
	RunQueued           RunState = "queued"
	RunProvisioning     RunState = "provisioning"
	RunRunning          RunState = "running"
	RunNeedsInput       RunState = "needs_input"
	RunAwaitingApproval RunState = "awaiting_approval"
	RunCompleted        RunState = "completed"
	RunFailed           RunState = "failed"
	RunTimedOut         RunState = "timed_out"
	RunCanceled         RunState = "canceled"
	RunLoopStopped      RunState = "loop_stopped"
)

// IsValid reports whether the value is one the schema accepts.
func (s RunState) IsValid() bool {
	switch s {
	case RunQueued, RunProvisioning, RunRunning, RunNeedsInput, RunAwaitingApproval,
		RunCompleted, RunFailed, RunTimedOut, RunCanceled, RunLoopStopped:
		return true
	}
	return false
}

// Terminal reports whether the run has ended: no container is (or should be) alive for it.
func (s RunState) Terminal() bool {
	switch s {
	case RunCompleted, RunFailed, RunTimedOut, RunCanceled, RunLoopStopped:
		return true
	}
	return false
}

// ActivityType is activities.type.
type ActivityType string

const (
	ActivityThought     ActivityType = "thought"
	ActivityAction      ActivityType = "action"
	ActivityElicitation ActivityType = "elicitation"
	ActivityResponse    ActivityType = "response"
	ActivityError       ActivityType = "error"
	ActivitySystem      ActivityType = "system"
	ActivityProvision   ActivityType = "provision"
)

// IsValid reports whether the value is one the schema accepts.
func (t ActivityType) IsValid() bool {
	switch t {
	case ActivityThought, ActivityAction, ActivityElicitation, ActivityResponse,
		ActivityError, ActivitySystem, ActivityProvision:
		return true
	}
	return false
}

// ElicitationKind is elicitations.kind.
type ElicitationKind string

const (
	ElicitationQuestion ElicitationKind = "question"
	ElicitationApproval ElicitationKind = "approval"
)

// IsValid reports whether the value is one the schema accepts.
func (k ElicitationKind) IsValid() bool {
	return k == ElicitationQuestion || k == ElicitationApproval
}

// ElicitationState is elicitations.state.
type ElicitationState string

const (
	ElicitationPending  ElicitationState = "pending"
	ElicitationAnswered ElicitationState = "answered"
	ElicitationDenied   ElicitationState = "denied"
	ElicitationExpired  ElicitationState = "expired"
	ElicitationCanceled ElicitationState = "canceled"
)

// IsValid reports whether the value is one the schema accepts.
func (s ElicitationState) IsValid() bool {
	switch s {
	case ElicitationPending, ElicitationAnswered, ElicitationDenied, ElicitationExpired,
		ElicitationCanceled:
		return true
	}
	return false
}

// RunOutputKind is run_outputs.kind.
type RunOutputKind string

const (
	OutputBranch       RunOutputKind = "branch"
	OutputPullRequest  RunOutputKind = "pull_request"
	OutputComment      RunOutputKind = "comment"
	OutputReview       RunOutputKind = "review"
	OutputWikiProposal RunOutputKind = "wiki_proposal"
	OutputTicket       RunOutputKind = "ticket"
	OutputPartialWork  RunOutputKind = "partial_work"
)

// IsValid reports whether the value is one the schema accepts.
func (k RunOutputKind) IsValid() bool {
	switch k {
	case OutputBranch, OutputPullRequest, OutputComment, OutputReview, OutputWikiProposal,
		OutputTicket, OutputPartialWork:
		return true
	}
	return false
}

// RunMessageState is run_messages.state.
type RunMessageState string

const (
	MessageQueued    RunMessageState = "queued"
	MessageDelivered RunMessageState = "delivered"
	MessageDropped   RunMessageState = "dropped"
)

// IsValid reports whether the value is one the schema accepts.
func (s RunMessageState) IsValid() bool {
	return s == MessageQueued || s == MessageDelivered || s == MessageDropped
}

// NotificationFlavor is notifications.flavor.
type NotificationFlavor string

const (
	FlavorQuestion NotificationFlavor = "question"
	FlavorApproval NotificationFlavor = "approval"
	FlavorReview   NotificationFlavor = "review"
	FlavorFailure  NotificationFlavor = "failure"
)

// IsValid reports whether the value is one the schema accepts.
func (f NotificationFlavor) IsValid() bool {
	switch f {
	case FlavorQuestion, FlavorApproval, FlavorReview, FlavorFailure:
		return true
	}
	return false
}

// NotificationState is notifications.state.
type NotificationState string

const (
	NotificationUnread    NotificationState = "unread"
	NotificationRead      NotificationState = "read"
	NotificationDismissed NotificationState = "dismissed"
)

// IsValid reports whether the value is one the schema accepts.
func (s NotificationState) IsValid() bool {
	return s == NotificationUnread || s == NotificationRead || s == NotificationDismissed
}
