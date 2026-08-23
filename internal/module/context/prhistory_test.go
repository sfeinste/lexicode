package contextmod

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/spruce/lexicode/internal/domain"
	"github.com/spruce/lexicode/internal/kernel/ports"
)

// prEvent seeds one event on pull request prNumber at an explicit timestamp. The order the
// history renders in is the point of most of these tests, and domain.Now() has millisecond
// resolution — rows seeded in a loop would tie. The test picks the clock instead.
func (w *prWorld) prEvent(t *testing.T, prNumber int, at string, mutate func(*domain.Event)) domain.Event {
	t.Helper()
	return seedEvent(t, w.st, w.project.ID, func(e *domain.Event) {
		e.SubjectKind = "pr"
		e.SubjectNumber = ptrI64(int64(prNumber))
		e.OccurredAt = at
		e.CreatedAt = at
		if mutate != nil {
			mutate(e)
		}
	})
}

// review is the event `pollReviews` emits: one review, carrying only its own body.
func review(state, body string) func(*domain.Event) {
	return func(e *domain.Event) {
		e.Kind = "pull_request_review"
		e.ActivityType = "submitted"
		e.ActorKind = domain.ActorHuman
		e.ActorLogin = ptrStr("spruce")
		e.Payload = json.RawMessage(fmt.Sprintf(`{"review":{"state":%q,"body":%q}}`, state, body))
	}
}

// TestPRHistoryProviderGivesWhatCameBefore is the half of LEXI-11 that is not the ticket: a run
// spawned by the SECOND review on pull request #4 is told that the pull request was opened, that
// a first review asked for something, and that a comment hangs off a specific line — none of
// which is in the event that started it.
func TestPRHistoryProviderGivesWhatCameBefore(t *testing.T) {
	w := seedPRWorld(t)
	w.prEvent(t, 4, "2026-08-23T10:00:00.000Z", func(e *domain.Event) {
		e.Kind = "pull_request"
		e.ActivityType = "opened"
		e.ActorKind = domain.ActorAgent
		e.ActorLogin = ptrStr("dev-bot")
		e.Payload = json.RawMessage(
			`{"pr":{"number":4,"state":"open","body":"Automated change for PAY-7."}}`)
	})
	w.prEvent(t, 4, "2026-08-23T11:00:00.000Z",
		review("changes_requested", "The retry loop never terminates."))
	w.prEvent(t, 4, "2026-08-23T11:30:00.000Z", func(e *domain.Event) {
		e.Kind = "pull_request_review_comment"
		e.ActivityType = "created"
		e.ActorKind = domain.ActorHuman
		e.ActorLogin = ptrStr("spruce")
		e.Payload = json.RawMessage(
			`{"comment":{"path":"internal/retry.go","line":42,"body":"This bound is off by one."}}`)
	})
	cause := w.prEvent(t, 4, "2026-08-23T12:00:00.000Z",
		review("changes_requested", "Still not fixed."))

	items, err := NewPRHistoryProvider(w.st).Resolve(context.Background(), ports.ContextRequest{
		ProjectID: w.project.ID, AgentID: w.agent.ID, CauseEventID: cause.ID,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 {
		t.Fatalf("items = %+v, want the history of PR #4", items)
	}
	it := items[0]
	if it.SourceKind != "event" || it.SourceRef != "pr:4" {
		t.Errorf("SourceKind/SourceRef = %q/%q, want event/pr:4", it.SourceKind, it.SourceRef)
	}
	if it.Title != "Pull request #4 so far" {
		t.Errorf("Title = %q", it.Title)
	}
	// The reason is rendered verbatim in the Context panel, so it has to say what was counted.
	wantReason := "the 3 events on pull request #4 before the one that started this run"
	if it.Reason != wantReason {
		t.Errorf("Reason = %q, want %q", it.Reason, wantReason)
	}
	if !it.Injected || it.Tokens <= 0 {
		t.Errorf("Injected/Tokens = %v/%d, want true/>0", it.Injected, it.Tokens)
	}
	for _, want := range []string{
		"Automated change for PAY-7.",
		"The retry loop never terminates.",
		"state: changes_requested",
		"This bound is off by one.",
		"path: internal/retry.go",
		"line: 42",
		"by spruce (human)",
		"2026-08-23T11:00:00.000Z",
	} {
		if !strings.Contains(it.Body, want) {
			t.Errorf("Body missing %q:\n%s", want, it.Body)
		}
	}
	// The causing review is the `event` provider's section. Repeating it here would spend
	// tokens telling the agent twice what it already read once.
	if strings.Contains(it.Body, "Still not fixed.") {
		t.Errorf("history repeats the causing event:\n%s", it.Body)
	}
	// Oldest first: the opening, then the first review, then the comment that answered it.
	opened := strings.Index(it.Body, "Automated change for PAY-7.")
	reviewed := strings.Index(it.Body, "The retry loop never terminates.")
	commented := strings.Index(it.Body, "This bound is off by one.")
	if opened >= reviewed || reviewed >= commented {
		t.Errorf("entries out of order (opened %d, reviewed %d, commented %d):\n%s",
			opened, reviewed, commented, it.Body)
	}
}

// TestPRHistoryProviderIsSilentWithNothingToSay: every way the lookup comes up empty is an
// absent section, never a failed enqueue — and a pull request whose causing event is its first
// has no history to give.
func TestPRHistoryProviderIsSilentWithNothingToSay(t *testing.T) {
	w := seedPRWorld(t)
	prov := NewPRHistoryProvider(w.st)
	ctx := context.Background()

	notAPR := seedEvent(t, w.st, w.project.ID, func(e *domain.Event) {
		e.Kind = "issues"
		e.SubjectKind = "issue"
		e.SubjectNumber = ptrI64(4)
	})
	first := w.prEvent(t, 4, "2026-08-23T10:00:00.000Z",
		review("changes_requested", "The retry loop never terminates."))

	cases := []struct {
		name string
		req  ports.ContextRequest
	}{
		{"no causing event", ports.ContextRequest{ProjectID: w.project.ID}},
		{"event is not about a PR", ports.ContextRequest{ProjectID: w.project.ID, CauseEventID: notAPR.ID}},
		{"causing event is gone", ports.ContextRequest{ProjectID: w.project.ID, CauseEventID: domain.NewID()}},
		{"nothing came before it", ports.ContextRequest{ProjectID: w.project.ID, CauseEventID: first.ID}},
	}
	for _, c := range cases {
		items, err := prov.Resolve(ctx, c.req)
		if err != nil {
			t.Errorf("%s: err = %v, want nil", c.name, err)
		}
		if len(items) != 0 {
			t.Errorf("%s: items = %+v, want none", c.name, items)
		}
	}
}

// TestPRHistoryProviderScopesTheListing: pull request numbers are per-repository, so another
// project's #4 is a different pull request. `trigger` events are Lexicode's own bookkeeping and
// are not something that happened on the pull request either.
func TestPRHistoryProviderScopesTheListing(t *testing.T) {
	w := seedPRWorld(t)
	ctx := context.Background()

	other := domain.Project{
		ID: domain.NewID(), Key: "OPS", Name: "Ops", OwnerID: w.project.OwnerID,
		Color: "#654321", CreatedAt: domain.Now(), UpdatedAt: domain.Now(),
	}
	if err := w.st.Projects().Create(ctx, &other); err != nil {
		t.Fatal(err)
	}
	seedEvent(t, w.st, other.ID, func(e *domain.Event) {
		e.SubjectKind = "pr"
		e.SubjectNumber = ptrI64(4)
		e.OccurredAt = "2026-08-23T09:00:00.000Z"
		e.CreatedAt = e.OccurredAt
		review("approved", "another project's pull request")(e)
	})
	w.prEvent(t, 4, "2026-08-23T09:30:00.000Z", func(e *domain.Event) {
		e.Kind = "trigger"
		e.ActivityType = "fired"
		e.Payload = json.RawMessage(`{"review":{"body":"a firing notification"}}`)
	})
	w.prEvent(t, 5, "2026-08-23T09:45:00.000Z", review("approved", "a different pull request"))
	w.prEvent(t, 4, "2026-08-23T10:00:00.000Z", review("changes_requested", "the real first review"))
	cause := w.prEvent(t, 4, "2026-08-23T12:00:00.000Z", review("changes_requested", "the second"))

	items, err := NewPRHistoryProvider(w.st).Resolve(ctx, ports.ContextRequest{
		ProjectID: w.project.ID, CauseEventID: cause.ID,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 {
		t.Fatalf("items = %+v, want the one earlier review on this project's #4", items)
	}
	if !strings.Contains(items[0].Body, "the real first review") {
		t.Errorf("Body missing this project's own review:\n%s", items[0].Body)
	}
	for _, unwanted := range []string{
		"another project's pull request",
		"a firing notification",
		"a different pull request",
	} {
		if strings.Contains(items[0].Body, unwanted) {
			t.Errorf("Body leaked %q:\n%s", unwanted, items[0].Body)
		}
	}
	if items[0].Reason != "the 1 event on pull request #4 before the one that started this run" {
		t.Errorf("Reason = %q", items[0].Reason)
	}
}

// TestPRHistoryProviderBoundsTheSection: a pull request with more history than the cap keeps the
// most recent entries and says how many it dropped. A truncated section that claimed to be the
// whole history would be worse than no section.
func TestPRHistoryProviderBoundsTheSection(t *testing.T) {
	w := seedPRWorld(t)
	total := maxHistoryEvents + 5
	for i := 0; i < total; i++ {
		w.prEvent(t, 4, fmt.Sprintf("2026-08-23T10:%02d:00.000Z", i),
			review("commented", fmt.Sprintf("review-marker-%02d", i)))
	}
	cause := w.prEvent(t, 4, "2026-08-23T11:00:00.000Z", review("changes_requested", "the latest"))

	items, err := NewPRHistoryProvider(w.st).Resolve(context.Background(), ports.ContextRequest{
		ProjectID: w.project.ID, CauseEventID: cause.ID,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 {
		t.Fatalf("items = %+v, want one bounded section", items)
	}
	body := items[0].Body
	if !strings.Contains(body, "5 older entries omitted") {
		t.Errorf("Body does not admit what it dropped:\n%s", body)
	}
	if !strings.Contains(body, "review-marker-05") || !strings.Contains(body, fmt.Sprintf("review-marker-%02d", total-1)) {
		t.Errorf("Body dropped the wrong end of the history:\n%s", body)
	}
	for i := 0; i < 5; i++ {
		if strings.Contains(body, fmt.Sprintf("review-marker-%02d", i)) {
			t.Errorf("Body kept dropped entry %d:\n%s", i, body)
		}
	}
}

// TestTruncateProseCutsOnARuneBoundary: a review body long enough to trim is trimmed, and the
// result is still valid text — the cut is by rune, not by byte.
func TestTruncateProseCutsOnARuneBoundary(t *testing.T) {
	long := strings.Repeat("é", maxHistoryBodyRunes+10)
	got := truncateProse(long)
	if !strings.HasSuffix(got, "…(truncated)") {
		t.Fatalf("truncateProse did not mark the cut: %q", got[len(got)-20:])
	}
	kept := strings.TrimSuffix(got, "\n\n…(truncated)")
	if len([]rune(kept)) != maxHistoryBodyRunes {
		t.Errorf("kept %d runes, want %d", len([]rune(kept)), maxHistoryBodyRunes)
	}
	if short := "still short"; truncateProse(short) != short {
		t.Errorf("truncateProse trimmed a short body")
	}
}
