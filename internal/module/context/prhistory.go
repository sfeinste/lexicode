// prhistory.go is the `pr_history` context provider: everything that happened on the pull
// request before the event that started this run (architecture §11: priority 26, immediately
// after `event`).
//
// Why it exists. The `event` provider injects exactly one event — the causing one. On a pull
// request that is almost never the whole story: `pollReviews` emits one event per review
// carrying only that review's own body, review comments arrive as their own events, and the
// pull request's opening is a third. So a run spawned by the second review on a pull request
// was handed that review and no knowledge that a first one existed — it could re-litigate a
// point already settled, or "fix" something an earlier reviewer had explicitly asked for.
//
// This is the other half of what a review-addressing run needs to know: `ticket` says what the
// pull request set out to do, `pr_history` says what has been said about it since.
package contextmod

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/spruce/lexicode/internal/domain"
	"github.com/spruce/lexicode/internal/kernel/ports"
	"github.com/spruce/lexicode/internal/kernel/store"
)

// maxHistoryEvents bounds the section: a long-running pull request can carry hundreds of
// events, and the oldest of them are the least likely to still matter. When the cut bites, the
// body says how many entries it dropped rather than letting the prompt imply it saw everything.
const maxHistoryEvents = 40

// maxHistoryBodyRunes bounds one entry's prose. The causing event is rendered in full by the
// `event` provider; the entries here are recall, and a whole earlier review body at full length
// would crowd out the review this run is actually answering.
const maxHistoryBodyRunes = 2000

// PRHistoryProvider yields the pull request's earlier events when the run's causing event is
// about a pull request. A run with no causing event, one about something else, or one on a pull
// request whose first event this is, contributes nothing.
//
// Priority 26 puts it directly after `event` (25) and before `ticket` (30): what just happened,
// then what led up to it, then what the whole thing is for.
type PRHistoryProvider struct {
	st *store.Store
}

// NewPRHistoryProvider builds the provider over st.
func NewPRHistoryProvider(st *store.Store) *PRHistoryProvider { return &PRHistoryProvider{st: st} }

// ID implements ports.ContextProvider.
func (p *PRHistoryProvider) ID() string { return "pr_history" }

// Priority implements ports.ContextProvider.
func (p *PRHistoryProvider) Priority() int { return 26 }

// Resolve implements ports.ContextProvider.
func (p *PRHistoryProvider) Resolve(ctx context.Context, req ports.ContextRequest) ([]ports.ContextItem, error) {
	prNumber, err := causePRNumber(ctx, p.st, req)
	if err != nil || prNumber == 0 {
		return nil, err
	}
	events, err := p.st.Events().ForPRSubject(ctx, req.ProjectID, int64(prNumber))
	if err != nil {
		return nil, err
	}
	prior := historyBefore(events, req.CauseEventID)
	if len(prior) == 0 {
		return nil, nil
	}
	dropped := 0
	if len(prior) > maxHistoryEvents {
		dropped = len(prior) - maxHistoryEvents
		prior = prior[dropped:]
	}

	var b strings.Builder
	fmt.Fprintf(&b, "Everything that happened on pull request #%d before the event that started "+
		"this run, oldest first. That event is not repeated here — it is the section above.\n",
		prNumber)
	if dropped > 0 {
		fmt.Fprintf(&b, "\n%d older %s omitted to keep this section bounded.\n",
			dropped, plural(dropped, "entry", "entries"))
	}
	for _, ev := range prior {
		b.WriteString("\n")
		b.WriteString(historyEntry(ev))
	}

	body := strings.TrimSpace(b.String())
	reason := fmt.Sprintf("the %d %s on pull request #%d before the one that started this run",
		len(prior), plural(len(prior), "event", "events"), prNumber)
	return []ports.ContextItem{{
		SourceKind: "event",
		SourceRef:  fmt.Sprintf("pr:%d", prNumber),
		Title:      fmt.Sprintf("Pull request #%d so far", prNumber),
		Reason:     reason,
		Body:       body,
		Tokens:     estimateTokens(body),
		Injected:   true,
	}}, nil
}

// historyBefore is the events that came before the causing one. The causing event is the
// boundary, not a member: the `event` provider already renders it in full, and anything after
// it (a review submitted while this run was being enqueued) is not history the run acted on.
//
// An event that named its pull request only in its payload is not in this list at all, since
// the query matches the subject columns. Then there is no boundary to find and the whole
// listing is returned — still the pull request's history, just without the exclusion.
func historyBefore(events []domain.Event, causeID string) []domain.Event {
	for i, ev := range events {
		if ev.ID == causeID {
			return events[:i]
		}
	}
	return events
}

// historySubjects is the payload sub-object each event kind's words live in, most specific
// first. `pr` is only consulted for a `pull_request` event: every PR-shaped event carries the
// pull request's body, and repeating the description under each of them would be noise.
var historySubjects = []string{"review", "comment", "check"}

// historyQualifiers is what to say about an entry besides who and when — the fields that change
// what an earlier event MEANT. A review's state is the difference between "approved" and
// "changes requested"; a comment's path and line are what it is attached to.
var historyQualifiers = map[string][]string{
	"review":  {"state", "intended_state", "max_severity", "findings_count"},
	"comment": {"path", "line"},
	"check":   {"name", "conclusion"},
	"pr":      {"state", "branch"},
}

// historyBodyFields is the prose field of each sub-object, in the order it is looked for.
var historyBodyFields = []string{"body", "detail"}

// historyEntry renders one earlier event: a heading saying what happened, by whom and when,
// the qualifiers that change its meaning, then whatever words a person wrote.
func historyEntry(ev domain.Event) string {
	var b strings.Builder

	// eventPhrase's trailing " event" reads as filler in a heading that is already a list of
	// events; the lead sentence of the `event` provider's section needs it, this does not.
	b.WriteString("### " + strings.TrimSuffix(eventPhrase(ev), " event"))
	if actor := actorPhrase(ev); actor != "" {
		b.WriteString(" " + actor)
	}
	if ev.OccurredAt != "" {
		b.WriteString(" — " + ev.OccurredAt)
	}
	b.WriteString("\n")

	key, sub := historySubject(ev)
	if key == "" {
		return b.String()
	}
	if quals := historyQualifierLine(key, sub); quals != "" {
		b.WriteString("\n" + quals + "\n")
	}
	if prose := historyProse(sub); prose != "" {
		b.WriteString("\n" + indent(prose, "> ") + "\n")
	}
	return b.String()
}

// historySubject picks the payload sub-object this event is about.
func historySubject(ev domain.Event) (string, map[string]any) {
	var payload map[string]any
	if len(ev.Payload) > 0 {
		_ = json.Unmarshal(ev.Payload, &payload)
	}
	order := historySubjects
	if ev.Kind == "pull_request" {
		order = append(append([]string{}, historySubjects...), "pr")
	}
	for _, key := range order {
		if sub, ok := payload[key].(map[string]any); ok && len(sub) > 0 {
			return key, sub
		}
	}
	return "", nil
}

// historyQualifierLine renders the qualifiers on one line — "state: changes_requested,
// findings: 3" — because an entry in a list is scanned, not read. Absent fields are skipped.
func historyQualifierLine(key string, sub map[string]any) string {
	var parts []string
	for _, f := range historyQualifiers[key] {
		text := renderField(sub[f])
		if text == "" {
			continue
		}
		name := fieldLabel[f]
		if name == "" {
			name = strings.ReplaceAll(f, "_", " ")
		}
		parts = append(parts, name+": "+text)
	}
	return strings.Join(parts, ", ")
}

// historyProse is the words a person wrote, truncated. It is quoted by the caller so a past
// review body cannot be mistaken for this prompt's own instructions.
func historyProse(sub map[string]any) string {
	for _, f := range historyBodyFields {
		if text := strings.TrimSpace(renderField(sub[f])); text != "" {
			return truncateProse(text)
		}
	}
	return ""
}

// truncateProse cuts on a rune boundary and says that it cut.
func truncateProse(s string) string {
	r := []rune(s)
	if len(r) <= maxHistoryBodyRunes {
		return s
	}
	return strings.TrimRight(string(r[:maxHistoryBodyRunes]), " \n") + "\n\n…(truncated)"
}

// plural picks a word for n.
func plural(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}
