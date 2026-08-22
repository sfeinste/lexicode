package domain

import "encoding/json"

// Trigger is a row of triggers (data model §6). The JSON columns stay raw here: their inner
// shapes belong to the trigger engine story; S15 only needs to persist suggested rules whole,
// disabled, exactly as brief §6.3 requires.
type Trigger struct {
	ID            string
	ProjectID     string
	Name          string
	Enabled       bool
	SourceID      string
	Event         string
	ActivityTypes json.RawMessage
	Filters       json.RawMessage
	Conditions    json.RawMessage
	Actions       json.RawMessage
	LoopConfig    json.RawMessage
	Cron          *string
	CreatedBy     *string
	CreatedAt     string
	UpdatedAt     string
}

// TriggerFiring is a row of trigger_firings (data model §6): one terminal outcome of the
// four-stage pipeline for one (trigger, event) pair — including the outcomes that did nothing,
// because "the rule fired but nothing happened" having a name is the whole point (§8). The
// UNIQUE(trigger_id, event_id) index underneath is the engine's idempotency against bus
// re-dispatch.
type TriggerFiring struct {
	ID              string
	TriggerID       string
	EventID         string
	Outcome         FiringOutcome
	Reason          string // always words: "conditions not met", "actor suppressed", ...
	RunID           *string
	AbsorbedByRunID *string
	Warnings        json.RawMessage // JSON array of strings; interpolation warnings land here
	CreatedAt       string
}

// DefaultLoopConfig is the data model §6.1 default: every loop-protection layer on, depth 3.
func DefaultLoopConfig() json.RawMessage {
	return json.RawMessage(`{"actor_suppression":true,"debounce_seconds":90,` +
		`"cancel_in_progress":true,"depth_limit":3,"daily_budget_cents":null}`)
}
