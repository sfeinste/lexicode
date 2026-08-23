package contextmod

import (
	"context"
	"encoding/json"
	"strconv"
	"strings"
	"testing"

	"github.com/spruce/lexicode/internal/domain"
	"github.com/spruce/lexicode/internal/kernel/ports"
	"github.com/spruce/lexicode/internal/kernel/store"
)

// prWorld is the graph the ticket provider walks back through when a run has no ticket of its
// own: a project with a column and an agent, and whatever tickets, runs and pull-request
// outputs a test adds.
type prWorld struct {
	st      *store.Store
	project domain.Project
	column  domain.Column
	agent   domain.Agent
	runSeq  int64
}

func seedPRWorld(t *testing.T) *prWorld {
	t.Helper()
	ctx := context.Background()
	st := openStore(t)
	p := seedProject(t, st)

	col := domain.Column{
		ID: domain.NewID(), ProjectID: p.ID, Name: "Doing",
		Category: domain.CategoryRunning, Position: 1,
		CreatedAt: domain.Now(), UpdatedAt: domain.Now(),
	}
	if err := st.Columns().Create(ctx, &col); err != nil {
		t.Fatal(err)
	}
	ag := domain.Agent{
		ID: domain.NewID(), ProjectID: p.ID, Name: "Dev", Color: "#0af",
		RuntimeID: "claude-code", Model: "claude-sonnet", Effort: "medium",
		Autonomy:      domain.AutonomyAutoGates,
		Permissions:   domain.AgentPermissions{OpenPRs: true},
		GitAuthorName: "Dev", GitAuthorEmail: "dev@agents.lexicode.local",
		ConcurrencyCap: 1, MaxWallClockSeconds: 3600, MaxSteps: 200, Enabled: true,
		CreatedAt: domain.Now(), UpdatedAt: domain.Now(),
	}
	if err := st.Agents().Create(ctx, &ag); err != nil {
		t.Fatal(err)
	}
	return &prWorld{st: st, project: p, column: col, agent: ag}
}

// ticket inserts a ticket with a description and one acceptance criterion — the two things the
// section exists to carry.
func (w *prWorld) ticket(t *testing.T, key, title, description string) domain.Ticket {
	t.Helper()
	ctx := context.Background()
	seq, err := w.st.Projects().AllocateTicketSeq(ctx, w.project.ID)
	if err != nil {
		t.Fatal(err)
	}
	tk := domain.Ticket{
		ID: domain.NewID(), ProjectID: w.project.ID, Seq: seq, Key: key, Title: title,
		Description: description, ColumnID: w.column.ID,
		Position: domain.PositionBetween(0, 0), Priority: domain.PriorityMedium,
		Origin: domain.OriginHuman, CreatedAt: domain.Now(), UpdatedAt: domain.Now(),
	}
	if err := w.st.Tickets().Create(ctx, &tk); err != nil {
		t.Fatal(err)
	}
	c := domain.Criterion{
		ID: domain.NewID(), TicketID: tk.ID, Position: 1,
		Text: "The completed run says so", UpdatedAt: domain.Now(),
	}
	if err := w.st.Criteria().Create(ctx, &c); err != nil {
		t.Fatal(err)
	}
	return tk
}

// openedPR records that a run — on ticketID, or free-floating when nil — opened prNumber.
func (w *prWorld) openedPR(t *testing.T, ticketID *string, prNumber int) domain.Run {
	t.Helper()
	ctx := context.Background()
	w.runSeq++
	run := domain.Run{
		ID: domain.NewID(), Seq: w.runSeq, ProjectID: w.project.ID, AgentID: w.agent.ID,
		TicketID: ticketID, State: domain.RunCompleted, Autonomy: domain.AutonomyAutoGates,
		Model: "claude-sonnet", Effort: "medium", Prompt: "do the thing",
		RuntimeID: "claude-code", SandboxID: "docker", QueuedAt: domain.Now(),
	}
	if err := w.st.Runs().Create(ctx, &run); err != nil {
		t.Fatal(err)
	}
	ref := strconv.Itoa(prNumber)
	out := domain.RunOutput{
		ID: domain.NewID(), RunID: run.ID, Kind: domain.OutputPullRequest, Ref: ref,
		URL: "https://github.com/acme/payments/pull/" + ref, Summary: "opened PR " + ref,
		CreatedAt: domain.Now(),
	}
	if err := w.st.RunOutputs().Append(ctx, &out); err != nil {
		t.Fatal(err)
	}
	return run
}

// reviewEvent is the event a review-addressing run is spawned by: subject `pr:N`, no ticket
// anywhere on it.
func (w *prWorld) reviewEvent(t *testing.T, prNumber int) domain.Event {
	t.Helper()
	return seedEvent(t, w.st, w.project.ID, func(e *domain.Event) {
		e.Kind = "pull_request_review"
		e.ActivityType = "submitted"
		e.ActorKind = domain.ActorHuman
		e.ActorLogin = ptrStr("spruce")
		e.SubjectNumber = ptrI64(int64(prNumber))
		e.Payload = json.RawMessage(`{"review":{"body":"address the other review"}}`)
	})
}

// TestTicketProviderResolvesTicketThroughPullRequest is LEXI-11's third criterion: a run with
// no ticket_id, spawned by a review on PR #4, receives the ticket the run that opened #4 was
// working on — description, acceptance criteria and all.
func TestTicketProviderResolvesTicketThroughPullRequest(t *testing.T) {
	w := seedPRWorld(t)
	tk := w.ticket(t, "PAY-7", "Say so when a PR could not be opened",
		"A completed run whose pull request could not be opened must say so.")
	opener := w.openedPR(t, &tk.ID, 4)
	ev := w.reviewEvent(t, 4)

	items, err := NewTicketProvider(w.st).Resolve(context.Background(), ports.ContextRequest{
		ProjectID: w.project.ID, AgentID: w.agent.ID, CauseEventID: ev.ID,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 {
		t.Fatalf("items = %+v, want the ticket behind PR #4", items)
	}
	it := items[0]
	if it.SourceKind != "ticket" || it.SourceRef != "PAY-7" {
		t.Errorf("SourceKind/SourceRef = %q/%q, want ticket/PAY-7", it.SourceKind, it.SourceRef)
	}
	if it.Title != "Ticket PAY-7: Say so when a PR could not be opened" {
		t.Errorf("Title = %q", it.Title)
	}
	// The reason is rendered verbatim in the Context panel, so it has to say how the ticket
	// was found — this one was inferred, not assigned.
	wantReason := "ticket PAY-7, the ticket run #" + strconv.FormatInt(opener.Seq, 10) +
		" opened pull request #4 for"
	if it.Reason != wantReason {
		t.Errorf("Reason = %q, want %q", it.Reason, wantReason)
	}
	if !it.Injected || it.Tokens <= 0 {
		t.Errorf("Injected/Tokens = %v/%d, want true/>0", it.Injected, it.Tokens)
	}
	for _, want := range []string{
		"This run has no ticket of its own.",
		"Pull request #4 was opened by run #" + strconv.FormatInt(opener.Seq, 10),
		"A completed run whose pull request could not be opened must say so.",
		"## Acceptance criteria",
		"The completed run says so",
	} {
		if !strings.Contains(it.Body, want) {
			t.Errorf("Body missing %q:\n%s", want, it.Body)
		}
	}
}

// TestTicketProviderPrefersTheRunsOwnTicket: a run that has a ticket_id is unaffected — same
// reason string as before LEXI-11, and no inference sentence in the body.
func TestTicketProviderPrefersTheRunsOwnTicket(t *testing.T) {
	w := seedPRWorld(t)
	own := w.ticket(t, "PAY-1", "The run's own work", "Do this.")
	other := w.ticket(t, "PAY-7", "The PR's work", "Do that.")
	w.openedPR(t, &other.ID, 4)
	ev := w.reviewEvent(t, 4)

	items, err := NewTicketProvider(w.st).Resolve(context.Background(), ports.ContextRequest{
		ProjectID: w.project.ID, AgentID: w.agent.ID, TicketID: own.ID, CauseEventID: ev.ID,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 || items[0].SourceRef != "PAY-1" {
		t.Fatalf("items = %+v, want the run's own PAY-1", items)
	}
	if items[0].Reason != "ticket PAY-1" {
		t.Errorf("Reason = %q, want %q", items[0].Reason, "ticket PAY-1")
	}
	if strings.Contains(items[0].Body, "no ticket of its own") {
		t.Errorf("body claims an inference for a run that has its own ticket:\n%s", items[0].Body)
	}
}

// TestTicketProviderPullRequestInferenceIsNonFatal: every way the walk can come up empty is an
// absent section, never a failed enqueue — no causing event, an event about something that is
// not a pull request, and a pull request no run in this project opened.
func TestTicketProviderPullRequestInferenceIsNonFatal(t *testing.T) {
	w := seedPRWorld(t)
	tk := w.ticket(t, "PAY-7", "The PR's work", "Do that.")
	w.openedPR(t, &tk.ID, 4)
	prov := NewTicketProvider(w.st)
	ctx := context.Background()

	notAPR := seedEvent(t, w.st, w.project.ID, func(e *domain.Event) {
		e.Kind = "issues"
		e.SubjectKind = "issue"
		e.SubjectNumber = ptrI64(4)
	})
	unopened := w.reviewEvent(t, 99)

	cases := []struct {
		name string
		req  ports.ContextRequest
	}{
		{"no causing event", ports.ContextRequest{ProjectID: w.project.ID}},
		{"dry preview", ports.ContextRequest{ProjectID: w.project.ID, Dry: true}},
		{"event is not about a PR", ports.ContextRequest{ProjectID: w.project.ID, CauseEventID: notAPR.ID}},
		{"no run opened that PR", ports.ContextRequest{ProjectID: w.project.ID, CauseEventID: unopened.ID}},
		{"causing event is gone", ports.ContextRequest{ProjectID: w.project.ID, CauseEventID: domain.NewID()}},
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

// TestTicketProviderFallsBackToPayloadPRNumber: an event whose subject columns name no pull
// request still resolves through the payload's pr.number, the way mcp.Server.causePRNumber
// does — the two readers of "which PR is this event about" agree.
func TestTicketProviderFallsBackToPayloadPRNumber(t *testing.T) {
	w := seedPRWorld(t)
	tk := w.ticket(t, "PAY-7", "The PR's work", "Do that.")
	w.openedPR(t, &tk.ID, 4)
	ev := seedEvent(t, w.st, w.project.ID, func(e *domain.Event) {
		e.Kind = "pull_request_review"
		e.SubjectKind = ""
		e.SubjectNumber = nil
		e.Payload = json.RawMessage(`{"pr":{"number":4}}`)
	})

	items, err := NewTicketProvider(w.st).Resolve(context.Background(), ports.ContextRequest{
		ProjectID: w.project.ID, CauseEventID: ev.ID,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 || items[0].SourceRef != "PAY-7" {
		t.Fatalf("items = %+v, want PAY-7 resolved through the payload", items)
	}
}
