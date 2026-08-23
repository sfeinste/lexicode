// event.go is the `event` context provider: the occurrence that caused this run, rendered as
// a labelled prompt section (contracts §2.6, architecture §11).
//
// Why it exists. A trigger-spawned run has no ticket, so before this provider the only thing
// that could tell such an agent what it was looking at was the trigger's prompt override — and
// an empty override meant a prompt with no task in it at all. The run row knew the subject
// ("pr:2") and carried a cause_event_id whose payload held the whole normalized event
// (contracts §4); none of it reached the agent. This is the general fix: a CI-failure run, a
// comment-triggered run and a review run all get told what happened before they are asked to
// act on it.
//
// It renders prose and labelled fields, never raw JSON. The payload is a machine artifact; the
// prompt is read by a model that does better with "Pull request #2 … on branch dev/PAY-14"
// than with a nested object it has to parse. Sub-objects it does not know about are still
// rendered — flattened, sorted, labelled — so an event kind added later degrades to something
// legible instead of vanishing.
package contextmod

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/spruce/lexicode/internal/domain"
	"github.com/spruce/lexicode/internal/kernel/ports"
	"github.com/spruce/lexicode/internal/kernel/store"
)

// EventProvider yields the run's causing event (architecture §11: priority 25, reason
// "the pull_request event that started this run"). A run with no cause event — a manual
// delegation, a ticket auto-start — contributes nothing.
//
// Priority 25 puts it after the standing guidance (`project` 10, `wiki` 20) and immediately
// before the `ticket` (30): guidance first, then the situation this particular run is in.
type EventProvider struct {
	st *store.Store
}

// NewEventProvider builds the provider over st.
func NewEventProvider(st *store.Store) *EventProvider { return &EventProvider{st: st} }

// ID implements ports.ContextProvider.
func (p *EventProvider) ID() string { return "event" }

// Priority implements ports.ContextProvider.
func (p *EventProvider) Priority() int { return 25 }

// Resolve implements ports.ContextProvider.
func (p *EventProvider) Resolve(ctx context.Context, req ports.ContextRequest) ([]ports.ContextItem, error) {
	if req.CauseEventID == "" {
		return nil, nil
	}
	ev, err := p.st.Events().ByID(ctx, req.CauseEventID)
	if err != nil {
		// A run whose causing event has been deleted is not a reason to fail the run: the
		// prompt simply loses a section it cannot fill.
		if errors.Is(err, store.ErrNotFound) {
			return nil, nil
		}
		return nil, err
	}
	body := renderEvent(ev)
	if strings.TrimSpace(body) == "" {
		return nil, nil
	}
	return []ports.ContextItem{{
		SourceKind: "event",
		SourceRef:  eventRef(ev),
		Title:      "What happened",
		Reason:     eventReason(ev),
		Body:       body,
		Tokens:     estimateTokens(body),
		Injected:   true,
	}}, nil
}

// eventRef is the Context panel's source reference: the subject the event is about
// ("pr:2"), falling back to the event's own id when it has no numbered subject.
func eventRef(ev domain.Event) string {
	if ev.SubjectKind != "" && ev.SubjectNumber != nil {
		return fmt.Sprintf("%s:%d", ev.SubjectKind, *ev.SubjectNumber)
	}
	if ev.SubjectKind != "" && ev.SubjectID != nil && *ev.SubjectID != "" {
		return ev.SubjectKind + ":" + *ev.SubjectID
	}
	return ev.ID
}

// eventReason is rendered verbatim in the run detail's Context panel.
func eventReason(ev domain.Event) string {
	reason := "the " + eventPhrase(ev) + " that started this run"
	if ev.SubjectKind != "" && ev.SubjectNumber != nil {
		reason += fmt.Sprintf(" (%s #%d)", ev.SubjectKind, *ev.SubjectNumber)
	}
	return reason
}

// eventPhrase names the occurrence the way a person would: "pull_request opened".
func eventPhrase(ev domain.Event) string {
	if ev.ActivityType == "" {
		return ev.Kind + " event"
	}
	return ev.Kind + " " + ev.ActivityType + " event"
}

// subObjectOrder is the order the contracts §4 sub-objects are rendered in: the thing the
// event is *about* first, then the surrounding facts. Sub-objects not in this list follow, in
// alphabetical order, so an event kind added later still renders.
var subObjectOrder = []string{"pr", "review", "comment", "check", "ticket", "run", "schedule", "repo", "actor"}

// subObjectLabel is the human heading for each known sub-object.
var subObjectLabel = map[string]string{
	"pr":       "Pull request",
	"review":   "Review",
	"comment":  "Comment",
	"check":    "Check suite",
	"ticket":   "Ticket",
	"run":      "Run",
	"schedule": "Schedule",
	"repo":     "Repository",
	"actor":    "Actor",
}

// fieldOrder is the reading order within a known sub-object: identity first, then the facts
// that qualify it. Fields not listed follow, alphabetically — a payload field added to
// contracts §4 later still renders without a change here.
var fieldOrder = map[string][]string{
	"pr": {"number", "title", "author", "author_kind", "branch", "base", "state", "draft",
		"merged", "files_changed", "additions", "deletions", "labels", "url", "body"},
	// The severity fields are the agent_review event's (internal/service/mcp/review.go); a
	// poller review event simply has none of them, and orderedFields skips what is absent.
	"review": {"id", "author", "state", "intended_state", "max_severity", "findings_count",
		"severity_counts", "body"},
	"comment":  {"id", "author", "path", "line", "body"},
	"check":    {"name", "conclusion", "suite_id", "url"},
	"ticket":   {"key", "title", "category", "column", "priority", "assignee", "delegate", "labels"},
	"run":      {"seq", "agent", "status", "ticket_key", "duration_seconds", "cost_cents", "id"},
	"repo":     {"owner", "name", "default_branch"},
	"actor":    {"kind", "login", "agent"},
	"schedule": {"cron", "fired_at"},
}

// fieldLabel is the human label for the payload paths worth a nicer name than their key.
var fieldLabel = map[string]string{
	"author_kind":         "author kind",
	"files_changed":       "files changed",
	"default_branch":      "default branch",
	"head_commit_message": "head commit message",
	"ticket_key":          "ticket key",
	"cost_cents":          "cost (cents)",
	"duration_seconds":    "duration (seconds)",
	"fired_at":            "fired at",
	"url":                 "URL",
	"id":                  "ID",
	"suite_id":            "suite ID",
	"intended_state":      "intended state",
	"max_severity":        "worst severity",
	"findings_count":      "findings",
	"severity_counts":     "findings by severity",
	"seq":                 "run number",
}

// bodyFields are the payload fields whose value is prose rather than a datum: they are
// rendered as an indented block under their own heading instead of on a "- key: value" line.
var bodyFields = map[string]bool{"body": true, "detail": true, "head_commit_message": true}

// renderEvent turns one event row into the prompt section an agent reads: a lead sentence
// saying what happened to what by whom, then the populated contracts §4 sub-objects as
// labelled fields.
func renderEvent(ev domain.Event) string {
	var b strings.Builder

	b.WriteString(leadSentence(ev))
	b.WriteString("\n")

	var payload map[string]any
	if len(ev.Payload) > 0 {
		_ = json.Unmarshal(ev.Payload, &payload)
	}
	for _, key := range subObjectKeys(payload) {
		sub, ok := payload[key].(map[string]any)
		if !ok || len(sub) == 0 {
			continue
		}
		section := renderSubObject(key, sub)
		if section == "" {
			continue
		}
		b.WriteString("\n")
		b.WriteString(section)
	}
	return strings.TrimRight(b.String(), "\n") + "\n"
}

// leadSentence is the one line that answers "what happened, to what, by whom" without the
// reader having to scan a field list.
func leadSentence(ev domain.Event) string {
	var b strings.Builder
	b.WriteString("This run was started by a ")
	b.WriteString(eventPhrase(ev))
	if subject := subjectPhrase(ev); subject != "" {
		b.WriteString(" on ")
		b.WriteString(subject)
	}
	if actor := actorPhrase(ev); actor != "" {
		b.WriteString(", ")
		b.WriteString(actor)
	}
	b.WriteString(".")
	if ev.OccurredAt != "" {
		b.WriteString(" It occurred at " + ev.OccurredAt + ".")
	}
	if ev.Source != "" {
		b.WriteString(" Source: " + ev.Source + ".")
	}
	return b.String()
}

// subjectPhrase names the event's subject in words: "pull request #2 (branch dev/PAY-14)".
func subjectPhrase(ev domain.Event) string {
	if ev.SubjectKind == "" {
		return ""
	}
	name := ev.SubjectKind
	if name == "pr" {
		name = "pull request"
	}
	var b strings.Builder
	b.WriteString(name)
	switch {
	case ev.SubjectNumber != nil:
		fmt.Fprintf(&b, " #%d", *ev.SubjectNumber)
	case ev.SubjectID != nil && *ev.SubjectID != "":
		b.WriteString(" " + *ev.SubjectID)
	}
	if ev.SubjectBranch != nil && *ev.SubjectBranch != "" {
		b.WriteString(" (branch `" + *ev.SubjectBranch + "`)")
	}
	return b.String()
}

// actorPhrase names who did it, and whether that "who" is a person, an agent or something
// outside Lexicode — the distinction the whole loop-protection design turns on.
func actorPhrase(ev domain.Event) string {
	login := ""
	if ev.ActorLogin != nil {
		login = strings.TrimSpace(*ev.ActorLogin)
	}
	kind := string(ev.ActorKind)
	switch {
	case login != "" && kind != "":
		return fmt.Sprintf("by %s (%s)", login, kind)
	case login != "":
		return "by " + login
	case kind != "":
		return "by " + kind + " actor"
	}
	return ""
}

// subObjectKeys is subObjectOrder, then whatever else the payload carries, alphabetically.
func subObjectKeys(payload map[string]any) []string {
	known := map[string]bool{}
	for _, k := range subObjectOrder {
		known[k] = true
	}
	var extra []string
	for k := range payload {
		if !known[k] {
			extra = append(extra, k)
		}
	}
	sort.Strings(extra)
	return append(append([]string{}, subObjectOrder...), extra...)
}

// renderSubObject renders one contracts §4 sub-object as a labelled field list. Empty values
// are dropped: a field list full of blanks is noise, and the agent is meant to read this.
func renderSubObject(key string, sub map[string]any) string {
	label := subObjectLabel[key]
	if label == "" {
		label = strings.ToUpper(key[:1]) + key[1:]
	}

	fields := orderedFields(key, sub)

	var lines, blocks strings.Builder
	for _, f := range fields {
		text := renderField(sub[f])
		if text == "" {
			continue
		}
		name := fieldLabel[f]
		if name == "" {
			name = strings.ReplaceAll(f, "_", " ")
		}
		if bodyFields[f] {
			fmt.Fprintf(&blocks, "\n%s:\n\n%s\n", capitalize(name), indent(text, "> "))
			continue
		}
		fmt.Fprintf(&lines, "- %s: %s\n", capitalize(name), text)
	}
	if lines.Len() == 0 && blocks.Len() == 0 {
		return ""
	}
	return "## " + label + "\n\n" + lines.String() + blocks.String()
}

// orderedFields is sub's keys in fieldOrder[key] order, with anything unlisted appended
// alphabetically.
func orderedFields(key string, sub map[string]any) []string {
	placed := map[string]bool{}
	var out []string
	for _, f := range fieldOrder[key] {
		if _, ok := sub[f]; ok {
			out = append(out, f)
			placed[f] = true
		}
	}
	var rest []string
	for f := range sub {
		if !placed[f] {
			rest = append(rest, f)
		}
	}
	sort.Strings(rest)
	return append(out, rest...)
}

// renderField formats one payload value for a human. It is deliberately not renderValue from
// the trigger interpolator: this surface wants "none" over an empty JSON array, and a list
// over `["a","b"]`.
func renderField(v any) string {
	switch t := v.(type) {
	case nil:
		return ""
	case string:
		return strings.TrimSpace(t)
	case bool:
		return strconv.FormatBool(t)
	case float64:
		return strconv.FormatFloat(t, 'f', -1, 64)
	case []any:
		var parts []string
		for _, e := range t {
			if s := renderField(e); s != "" {
				parts = append(parts, s)
			}
		}
		return strings.Join(parts, ", ")
	case map[string]any:
		var parts []string
		keys := make([]string, 0, len(t))
		for k := range t {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			if s := renderField(t[k]); s != "" {
				parts = append(parts, k+"="+s)
			}
		}
		return strings.Join(parts, ", ")
	default:
		b, err := json.Marshal(t)
		if err != nil {
			return ""
		}
		return string(b)
	}
}

func capitalize(s string) string {
	if s == "" {
		return s
	}
	return strings.ToUpper(s[:1]) + s[1:]
}

// indent prefixes every line of text, so a multi-paragraph PR body cannot be mistaken for
// the prompt's own instructions.
func indent(text, prefix string) string {
	lines := strings.Split(strings.TrimRight(text, "\n"), "\n")
	for i, l := range lines {
		lines[i] = strings.TrimRight(prefix+l, " ")
	}
	return strings.Join(lines, "\n")
}
