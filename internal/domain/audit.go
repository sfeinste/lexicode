package domain

import "encoding/json"

// AuditEntry is a row of audit_log — who did what to what, with before/after snapshots. The
// snapshot shapes vary by action, so they stay raw JSON here; nil means "not captured".
type AuditEntry struct {
	ID         string
	ProjectID  *string
	ActorKind  ActorKind
	ActorID    *string
	Action     string
	TargetKind string
	TargetID   string
	Before     json.RawMessage
	After      json.RawMessage
	Note       string
	CreatedAt  string
}
