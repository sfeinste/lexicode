package contextmod

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/spruce/lexicode/internal/domain"
	"github.com/spruce/lexicode/internal/kernel/ports"
	"github.com/spruce/lexicode/internal/kernel/store"
)

func seedEvent(t *testing.T, st *store.Store, projectID string, mutate func(*domain.Event)) domain.Event {
	t.Helper()
	now := domain.Now()
	ev := domain.Event{
		ID: domain.NewID(), ProjectID: &projectID, Source: "github.poll",
		Kind: "pull_request", ActivityType: "opened", ActorKind: domain.ActorAgent,
		SubjectKind: "pr", Payload: json.RawMessage("{}"),
		DedupeKey: "t:" + domain.NewID(), DispatchState: domain.DispatchDone,
		OccurredAt: now, CreatedAt: now,
	}
	if mutate != nil {
		mutate(&ev)
	}
	if err := st.Events().Insert(context.Background(), &ev); err != nil {
		t.Fatal(err)
	}
	return ev
}

func ptrStr(s string) *string { return &s }
func ptrI64(n int64) *int64   { return &n }

// TestEventProviderRendersPullRequest is the shape of the section a reviewer reads: prose
// first (what happened, to what, by whom), then the contracts §4 sub-objects as labelled
// fields. Never the raw payload.
func TestEventProviderRendersPullRequest(t *testing.T) {
	st := openStore(t)
	p := seedProject(t, st)
	ev := seedEvent(t, st, p.ID, func(e *domain.Event) {
		e.ActorLogin = ptrStr("spruce")
		e.SubjectNumber = ptrI64(219)
		e.SubjectBranch = ptrStr("dev/PAY-14")
		e.OccurredAt = "2026-08-20T10:11:12Z"
		e.Payload = json.RawMessage(`{
			"pr": {"number":219,"title":"Idempotency keys","author":"spruce",
			       "author_kind":"agent","branch":"dev/PAY-14","base":"main",
			       "draft":false,"merged":false,"state":"open","additions":142,
			       "deletions":18,"files_changed":7,"labels":["payments","api"],
			       "body":"Adds a replay cache.\nSecond line.","url":"https://example.test/pr/219"},
			"repo": {"owner":"acme","name":"payments","default_branch":"main"},
			"actor": {"kind":"agent","login":"spruce","agent":"Dev"}}`)
	})

	items, err := NewEventProvider(st).Resolve(context.Background(),
		ports.ContextRequest{ProjectID: p.ID, CauseEventID: ev.ID})
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 {
		t.Fatalf("items = %d, want 1", len(items))
	}
	it := items[0]
	if it.SourceKind != "event" || it.SourceRef != "pr:219" || it.Title != "What happened" || !it.Injected {
		t.Fatalf("item = %+v", it)
	}
	if it.Tokens != estimateTokens(it.Body) {
		t.Errorf("tokens = %d, want %d", it.Tokens, estimateTokens(it.Body))
	}
	want := []string{
		"This run was started by a pull_request opened event on pull request #219 (branch `dev/PAY-14`), by spruce (agent).",
		"It occurred at 2026-08-20T10:11:12Z.",
		"## Pull request",
		"- Number: 219",
		"- Title: Idempotency keys",
		"- Branch: dev/PAY-14",
		"- Base: main",
		"- Files changed: 7",
		"- Labels: payments, api",
		"## Repository",
		"- Owner: acme",
		"> Adds a replay cache.",
	}
	for _, w := range want {
		if !strings.Contains(it.Body, w) {
			t.Errorf("body is missing %q\n---\n%s", w, it.Body)
		}
	}
	if strings.Contains(it.Body, `"number"`) || strings.Contains(it.Body, "{") {
		t.Errorf("the body contains raw JSON:\n%s", it.Body)
	}
	// Numbers render as integers, not as 219.000000 — the payload is JSON, so every number
	// arrives as a float64.
	if strings.Contains(it.Body, "219.") {
		t.Errorf("a number rendered with a decimal tail:\n%s", it.Body)
	}
}

// TestEventProviderRendersCheckSuite: the same provider is what a CI-failure run reads. It is
// not a pull-request-shaped special case.
func TestEventProviderRendersCheckSuite(t *testing.T) {
	st := openStore(t)
	p := seedProject(t, st)
	ev := seedEvent(t, st, p.ID, func(e *domain.Event) {
		e.Kind, e.ActivityType = "check_suite", "completed"
		e.SubjectNumber = ptrI64(219)
		e.SubjectBranch = ptrStr("dev/PAY-14")
		e.ActorLogin = ptrStr("GitHub Actions")
		e.Payload = json.RawMessage(`{
			"check": {"suite_id":"99","name":"CI","conclusion":"failure",
			          "url":"https://example.test/runs/99"},
			"pr": {"number":219,"branch":"dev/PAY-14"}}`)
	})
	items, err := NewEventProvider(st).Resolve(context.Background(),
		ports.ContextRequest{ProjectID: p.ID, CauseEventID: ev.ID})
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 {
		t.Fatalf("items = %d, want 1", len(items))
	}
	for _, w := range []string{
		"check_suite completed event",
		"## Check suite",
		"- Conclusion: failure",
		"- URL: https://example.test/runs/99",
		"## Pull request",
		"- Number: 219",
	} {
		if !strings.Contains(items[0].Body, w) {
			t.Errorf("body is missing %q\n---\n%s", w, items[0].Body)
		}
	}
}

// TestEventProviderUnknownSubObject: an event kind nobody has taught this provider about still
// renders legibly rather than vanishing — the reason the sub-object walk is generic.
func TestEventProviderUnknownSubObject(t *testing.T) {
	st := openStore(t)
	p := seedProject(t, st)
	ev := seedEvent(t, st, p.ID, func(e *domain.Event) {
		e.Kind, e.ActivityType = "deployment", "succeeded"
		e.SubjectKind = "deployment"
		e.SubjectID = ptrStr("d-7")
		e.Payload = json.RawMessage(`{"deployment":{"environment":"prod","version":"1.4.2"}}`)
	})
	items, err := NewEventProvider(st).Resolve(context.Background(),
		ports.ContextRequest{ProjectID: p.ID, CauseEventID: ev.ID})
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 {
		t.Fatalf("items = %d, want 1", len(items))
	}
	if items[0].SourceRef != "deployment:d-7" {
		t.Errorf("source ref = %q", items[0].SourceRef)
	}
	for _, w := range []string{"## Deployment", "- Environment: prod", "- Version: 1.4.2"} {
		if !strings.Contains(items[0].Body, w) {
			t.Errorf("body is missing %q\n---\n%s", w, items[0].Body)
		}
	}
}

// TestEventProviderSilentWithoutCause: no cause event, no section — and a cause_event_id
// pointing at a row that is gone is not a reason to fail the run.
func TestEventProviderSilentWithoutCause(t *testing.T) {
	st := openStore(t)
	p := seedProject(t, st)
	prov := NewEventProvider(st)

	items, err := prov.Resolve(context.Background(), ports.ContextRequest{ProjectID: p.ID})
	if err != nil || len(items) != 0 {
		t.Fatalf("no cause event: items = %v, err = %v", items, err)
	}
	items, err = prov.Resolve(context.Background(),
		ports.ContextRequest{ProjectID: p.ID, CauseEventID: domain.NewID()})
	if err != nil || len(items) != 0 {
		t.Fatalf("missing event row: items = %v, err = %v", items, err)
	}
}

// TestEventProviderPriority pins the slot: after the standing guidance (`project` 10,
// `wiki` 20) and before the `ticket` (30). Prompt order is provider priority order, so this
// is a rendering contract, not a detail.
func TestEventProviderPriority(t *testing.T) {
	st := openStore(t)
	got := NewEventProvider(st).Priority()
	if got <= NewProjectProvider(st).Priority() || got >= NewTicketProvider(st).Priority() {
		t.Fatalf("event priority = %d, want between project (%d) and ticket (%d)",
			got, NewProjectProvider(st).Priority(), NewTicketProvider(st).Priority())
	}
	if NewEventProvider(st).ID() != "event" {
		t.Fatalf("id = %q", NewEventProvider(st).ID())
	}
}
