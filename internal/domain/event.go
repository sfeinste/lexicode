package domain

import "encoding/json"

// Event is a row of events — one normalized occurrence from an event source or from inside the
// process. DedupeKey is deterministic per occurrence, which is what makes polling and bus
// re-dispatch idempotent (D-3). Payload's normalized shapes are defined by contracts §4 and
// typed by the stories that produce them; the store carries it raw.
type Event struct {
	ID            string
	ProjectID     *string
	Source        string
	Kind          string
	ActivityType  string
	ActorKind     ActorKind
	ActorID       *string
	ActorLogin    *string
	ActorEmail    *string
	SubjectKind   string
	SubjectID     *string
	SubjectNumber *int64
	SubjectBranch *string
	Payload       json.RawMessage
	CauseRunID    *string
	DedupeKey     string
	DispatchState DispatchState
	OccurredAt    string
	CreatedAt     string
}
