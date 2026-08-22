package cron

import "github.com/spruce/lexicode/internal/kernel/ports"

// sourceID is the EventSource port ID and events.source of everything this module emits.
const sourceID = "schedule.cron"

// eventKind / activityType are the one event this source catalogs: `schedule` · `cron`
// (brief §6.6's event catalogue; contracts §4's schedule payload).
const (
	eventKind    = "schedule"
	activityType = "cron"
)

// cronFilterKey is the FilterField that carries the cron expression through the
// catalog-generated editor. The trigger schema stores the expression in its dedicated
// `cron` column (data model §6), not in the filters JSON — the editor's draft mapping
// routes this reserved key there — but cataloging it as a FilterField is what keeps the
// WHEN section fully generated: this source declares its input, no editor special case
// names the schedule event.
const cronFilterKey = "cron"

// catalog is the schedule.cron EventCatalog (contracts §2.1). One event kind, one activity
// type: the expression is trigger data, not an activity, so narrowing happens per trigger —
// each trigger's own expression addresses its own events (see doc.go).
func catalog() ports.EventCatalog {
	return ports.EventCatalog{Events: []ports.EventDescriptor{
		{
			Kind:  eventKind,
			Label: "Schedule",
			ActivityTypes: []ports.ActivityType{
				{Value: activityType, Label: "on a cron schedule",
					Help: "Fires on this rule's cron expression, evaluated in UTC. " +
						"After a restart, at most one missed firing is caught up — never a backlog."},
			},
			Filters: []ports.FilterField{
				{Key: cronFilterKey, Kind: "cron", Label: "Cron expression (UTC)"},
			},
			Fields: []ports.PayloadField{
				{Path: "schedule.cron", Type: "text"},
				{Path: "schedule.fired_at", Type: "text"},
			},
			// Debounce and cancel-in-progress key each schedule trigger on itself: a
			// schedule has no PR or ticket to be the subject.
			SubjectKey: "schedule:{{schedule.trigger_id}}",
		},
	}}
}
