package mcp_test

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"testing"

	"github.com/spruce/lexicode/internal/domain"
	"github.com/spruce/lexicode/internal/kernel/ports"
)

// reviewRecorder stands in for service/runs.ReviewSubmitter: it records what the tool asked
// the forge to write and can be made to refuse, the way the adapter refuses APPROVE and a
// missing submit_reviews grant.
type reviewRecorder struct {
	mu    sync.Mutex
	calls []reviewCall
	err   error
	// rejectRequestChanges makes the seam answer a REQUEST_CHANGES the way GitHub does for a
	// review by the pull request's own author: a 422, classified by the adapter, nothing
	// written. A COMMENT with the same body still succeeds.
	rejectRequestChanges bool
}

type reviewCall struct {
	runID    string
	prNumber int
	event    string
	body     string
}

func (r *reviewRecorder) submit(_ context.Context, run domain.Run, prNumber int, event, body string) (domain.Review, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.err != nil {
		return domain.Review{}, r.err
	}
	r.calls = append(r.calls, reviewCall{runID: run.ID, prNumber: prNumber, event: event, body: body})
	if r.rejectRequestChanges && event == "REQUEST_CHANGES" {
		return domain.Review{}, &ports.ReviewEventRejectedError{
			Event:  event,
			Detail: "Can not request changes on your own pull request",
		}
	}
	return domain.Review{ID: 7001, PRNumber: prNumber, State: event, AuthorLogin: "lexicode-bot"}, nil
}

// agentReviewEvents returns the agent_review events this run caused, oldest first.
func (e *env) agentReviewEvents(t *testing.T, runID string) []domain.Event {
	t.Helper()
	evs, err := e.st.Events().ByCauseRun(context.Background(), runID)
	if err != nil {
		t.Fatal(err)
	}
	var out []domain.Event
	for _, ev := range evs {
		if ev.Kind == "agent_review" {
			out = append(out, ev)
		}
	}
	return out
}

// reviewPayload decodes the `review` sub-object of an emitted event.
func reviewPayload(t *testing.T, ev domain.Event) map[string]any {
	t.Helper()
	var body struct {
		Review map[string]any `json:"review"`
	}
	if err := json.Unmarshal(ev.Payload, &body); err != nil {
		t.Fatal(err)
	}
	return body.Review
}

func (r *reviewRecorder) last() reviewCall {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.calls[len(r.calls)-1]
}

func (r *reviewRecorder) count() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.calls)
}

// A review with a blocker defaults to REQUEST_CHANGES, renders the severity tags, and
// records a level-1 activity.
func TestSubmitReviewSeverityTaggedBody(t *testing.T) {
	e := newEnv(t)
	f := e.fixtures(domain.AutonomyAuto, domain.AgentPermissions{SubmitReviews: true})

	isErr, result := e.callTool(f.token, "submit_review", map[string]any{
		"pr_number": 12,
		"summary":   "Two problems and a nit.",
		"findings": []map[string]any{
			{"severity": "blocker", "title": "Retry path double-charges",
				"detail": "charge() is called before the key is persisted.",
				"file":   "src/charge.ts", "line": 88},
			{"severity": "nit", "title": "Stray console.log", "file": "src/charge.ts"},
		},
	})
	if isErr {
		t.Fatalf("submit_review failed: %v", result)
	}
	if result["event"] != "REQUEST_CHANGES" {
		t.Fatalf("event = %v, want REQUEST_CHANGES (a blocker is present)", result["event"])
	}
	call := e.reviews.last()
	if call.prNumber != 12 || call.event != "REQUEST_CHANGES" || call.runID != f.run.ID {
		t.Fatalf("forge call = %+v", call)
	}
	for _, want := range []string{
		"Two problems and a nit.",
		"1 blocker · 1 nit",
		"**[BLOCKER]** Retry path double-charges — `src/charge.ts:88`",
		"**[NIT]** Stray console.log — `src/charge.ts`",
	} {
		if !strings.Contains(call.body, want) {
			t.Fatalf("review body missing %q:\n%s", want, call.body)
		}
	}

	acts, err := e.st.Activities().ForRun(context.Background(), f.run.ID)
	if err != nil {
		t.Fatal(err)
	}
	var found bool
	for _, a := range acts {
		if a.ToolName == "mcp__lexicode__submit_review" {
			found = true
			if !strings.Contains(a.Title, "PR #12") {
				t.Fatalf("activity title = %q", a.Title)
			}
		}
	}
	if !found {
		t.Fatal("no submit_review activity recorded")
	}
}

// Only nits: the default event is a plain comment.
func TestSubmitReviewDefaultsToCommentWithoutBlockers(t *testing.T) {
	e := newEnv(t)
	f := e.fixtures(domain.AutonomyAuto, domain.AgentPermissions{SubmitReviews: true})
	isErr, result := e.callTool(f.token, "submit_review", map[string]any{
		"pr_number": 4,
		"findings":  []map[string]any{{"severity": "nit", "title": "Name the constant"}},
	})
	if isErr {
		t.Fatalf("submit_review failed: %v", result)
	}
	if result["event"] != "COMMENT" {
		t.Fatalf("event = %v, want COMMENT", result["event"])
	}
}

// Brief D6: no argument, and no permission, makes an agent's approval possible.
func TestSubmitReviewRefusesApprove(t *testing.T) {
	e := newEnv(t)
	f := e.fixtures(domain.AutonomyAuto, domain.AgentPermissions{SubmitReviews: true})
	isErr, result := e.callTool(f.token, "submit_review", map[string]any{
		"pr_number": 3, "event": "approve",
		"findings": []map[string]any{{"severity": "nit", "title": "fine"}},
	})
	if !isErr {
		t.Fatalf("APPROVE was accepted: %v", result)
	}
	if !strings.Contains(result["text"].(string), "reserved for humans") {
		t.Fatalf("refusal = %v", result)
	}
	if e.reviews.count() != 0 {
		t.Fatal("an APPROVE reached the forge seam")
	}
}

// An unknown severity is refused before the forge is touched: the vocabulary is the contract.
func TestSubmitReviewRejectsUnknownSeverity(t *testing.T) {
	e := newEnv(t)
	f := e.fixtures(domain.AutonomyAuto, domain.AgentPermissions{SubmitReviews: true})
	isErr, result := e.callTool(f.token, "submit_review", map[string]any{
		"pr_number": 3,
		"findings":  []map[string]any{{"severity": "catastrophic", "title": "x"}},
	})
	if !isErr {
		t.Fatalf("unknown severity accepted: %v", result)
	}
	if e.reviews.count() != 0 {
		t.Fatal("an invalid review reached the forge seam")
	}
}

// The permission denial comes from the adapter, and its text reaches the agent verbatim.
func TestSubmitReviewSurfacesPermissionDenial(t *testing.T) {
	e := newEnv(t)
	f := e.fixtures(domain.AutonomyAuto, domain.AgentPermissions{})
	e.reviews.err = &ports.PermissionDeniedError{AgentID: f.agent.ID, Grant: "submit_reviews"}
	isErr, result := e.callTool(f.token, "submit_review", map[string]any{
		"pr_number": 3,
		"findings":  []map[string]any{{"severity": "major", "title": "x"}},
	})
	if !isErr {
		t.Fatalf("a denied review reported success: %v", result)
	}
	if !strings.Contains(result["text"].(string), "submit_reviews") {
		t.Fatalf("denial did not name the grant: %v", result)
	}
}

// Without pr_number the tool reads the PR off the event that caused the run — the reviewer
// spawned by a "pull request opened" trigger never has to be told the number.
func TestSubmitReviewDefaultsPRNumberFromCausingEvent(t *testing.T) {
	e := newEnv(t)
	f := e.fixtures(domain.AutonomyAuto, domain.AgentPermissions{SubmitReviews: true})

	ctx := context.Background()
	number := int64(219)
	ev := domain.Event{
		ID: domain.NewID(), ProjectID: &f.project.ID, Source: "github.poll",
		Kind: "pull_request", ActivityType: "opened", ActorKind: domain.ActorAgent,
		SubjectKind: "pr", SubjectNumber: &number,
		Payload:       json.RawMessage(`{"pr":{"number":219}}`),
		DedupeKey:     "test:" + domain.NewID(),
		DispatchState: domain.DispatchDone,
		OccurredAt:    domain.Now(),
	}
	if err := e.st.Events().Insert(ctx, &ev); err != nil {
		t.Fatal(err)
	}
	// A trigger-spawned run: no ticket, one cause event.
	run := domain.Run{
		ID: domain.NewID(), Seq: 2, ProjectID: f.project.ID, AgentID: f.agent.ID,
		CauseEventID: &ev.ID, State: domain.RunRunning, Autonomy: domain.AutonomyAuto,
		Model: f.agent.Model, Effort: f.agent.Effort, Prompt: "review PR 219",
		RuntimeID: "scripted", SandboxID: "fake", SubjectKey: "pr:219",
		QueuedAt: domain.Now(),
	}
	if err := e.st.Runs().Create(ctx, &run); err != nil {
		t.Fatal(err)
	}
	token, err := e.mcp.MintToken(run.ID)
	if err != nil {
		t.Fatal(err)
	}

	isErr, result := e.callTool(token, "submit_review", map[string]any{
		"summary": "Looks reasonable.",
	})
	if isErr {
		t.Fatalf("submit_review failed: %v", result)
	}
	if got := e.reviews.last().prNumber; got != 219 {
		t.Fatalf("pr_number = %d, want 219 (from the causing event)", got)
	}
}

// The same defaulting through the payload rather than the typed subject column: an event
// source that fills only the normalized `pr` object (contracts §4) still tells the tool which
// pull request the run is about.
func TestSubmitReviewDefaultsPRNumberFromThePayload(t *testing.T) {
	e := newEnv(t)
	f := e.fixtures(domain.AutonomyAuto, domain.AgentPermissions{SubmitReviews: true})

	ctx := context.Background()
	ev := domain.Event{
		ID: domain.NewID(), ProjectID: &f.project.ID, Source: "github.poll",
		Kind: "check_suite", ActivityType: "completed", ActorKind: domain.ActorExternal,
		SubjectKind:   "pr", // no SubjectNumber: only the payload names the pull request
		Payload:       json.RawMessage(`{"check":{"conclusion":"failure"},"pr":{"number":407}}`),
		DedupeKey:     "test:" + domain.NewID(),
		DispatchState: domain.DispatchDone,
		OccurredAt:    domain.Now(),
	}
	if err := e.st.Events().Insert(ctx, &ev); err != nil {
		t.Fatal(err)
	}
	run := domain.Run{
		ID: domain.NewID(), Seq: 3, ProjectID: f.project.ID, AgentID: f.agent.ID,
		CauseEventID: &ev.ID, State: domain.RunRunning, Autonomy: domain.AutonomyAuto,
		Model: f.agent.Model, Effort: f.agent.Effort, Prompt: "look at the failure",
		RuntimeID: "scripted", SandboxID: "fake", SubjectKey: "pr:407",
		QueuedAt: domain.Now(),
	}
	if err := e.st.Runs().Create(ctx, &run); err != nil {
		t.Fatal(err)
	}
	token, err := e.mcp.MintToken(run.ID)
	if err != nil {
		t.Fatal(err)
	}
	isErr, result := e.callTool(token, "submit_review", map[string]any{
		"summary": "The suite failed on a flaky test.",
	})
	if isErr {
		t.Fatalf("submit_review failed: %v", result)
	}
	if got := e.reviews.last().prNumber; got != 407 {
		t.Fatalf("pr_number = %d, want 407 (from the causing event's payload)", got)
	}
}

// And a run nobody triggered gets a message that says what to do, not a silent wrong number.
func TestSubmitReviewWithoutCauseEventAsksForTheNumber(t *testing.T) {
	e := newEnv(t)
	f := e.fixtures(domain.AutonomyAuto, domain.AgentPermissions{SubmitReviews: true})
	ctx := context.Background()
	run := domain.Run{
		ID: domain.NewID(), Seq: 4, ProjectID: f.project.ID, AgentID: f.agent.ID,
		State: domain.RunRunning, Autonomy: domain.AutonomyAuto,
		Model: f.agent.Model, Effort: f.agent.Effort, Prompt: "review something",
		RuntimeID: "scripted", SandboxID: "fake", QueuedAt: domain.Now(),
	}
	if err := e.st.Runs().Create(ctx, &run); err != nil {
		t.Fatal(err)
	}
	token, err := e.mcp.MintToken(run.ID)
	if err != nil {
		t.Fatal(err)
	}
	isErr, result := e.callTool(token, "submit_review", map[string]any{"summary": "hi"})
	if !isErr {
		t.Fatalf("submit_review succeeded with no pull request to submit against: %v", result)
	}
	if !strings.Contains(result["text"].(string), "pr_number") {
		t.Fatalf("the error does not say how to fix it: %v", result)
	}
}

// reviewerRun inserts the rows a reviewer run has in production: the poller's "pull request
// opened" event, and a run caused by it whose subject is that pull request. It returns the run
// and a fresh MCP token for it.
func (e *env) reviewerRun(t *testing.T, f *fixtures, number int64, branch string) (domain.Run, string) {
	t.Helper()
	ctx := context.Background()
	prPayload := fmt.Sprintf(
		`{"pr":{"number":%d,"title":"Add idempotency keys","author":"dev-bot","author_kind":"agent",`+
			`"branch":%q,"base":"main","draft":false,"merged":false,"state":"open","labels":[]},`+
			`"repo":{"owner":"acme","name":"payments","default_branch":"main"},`+
			`"actor":{"kind":"agent","login":"dev-bot","agent":"Dev"}}`, number, branch)
	ev := domain.Event{
		ID: domain.NewID(), ProjectID: &f.project.ID, Source: "github.poll",
		Kind: "pull_request", ActivityType: "opened", ActorKind: domain.ActorAgent,
		SubjectKind: "pr", SubjectNumber: &number, SubjectBranch: &branch,
		Payload:       json.RawMessage(prPayload),
		DedupeKey:     "test:" + domain.NewID(),
		DispatchState: domain.DispatchDone,
		OccurredAt:    domain.Now(),
	}
	if err := e.st.Events().Insert(ctx, &ev); err != nil {
		t.Fatal(err)
	}
	run := domain.Run{
		ID: domain.NewID(), Seq: int64(number), ProjectID: f.project.ID, AgentID: f.agent.ID,
		CauseEventID: &ev.ID, State: domain.RunRunning, Autonomy: domain.AutonomyAuto,
		Model: f.agent.Model, Effort: f.agent.Effort, Prompt: "review it",
		RuntimeID: "scripted", SandboxID: "fake",
		SubjectKey: fmt.Sprintf("pr:%d", number), QueuedAt: domain.Now(),
	}
	if err := e.st.Runs().Create(ctx, &run); err != nil {
		t.Fatal(err)
	}
	token, err := e.mcp.MintToken(run.ID)
	if err != nil {
		t.Fatal(err)
	}
	return run, token
}

// ---- the agent_review event ----------------------------------------------------------------

// THE regression this event exists for: a review with a blocker publishes an internal event
// carrying the severities, attributed to the REVIEWING AGENT and subject to the PULL REQUEST,
// so a trigger can fire on the reviewer's verdict rather than on the state GitHub stored.
func TestSubmitReviewEmitsAgentReviewEvent(t *testing.T) {
	e := newEnv(t)
	f := e.fixtures(domain.AutonomyAuto, domain.AgentPermissions{SubmitReviews: true})
	// The reviewer was spawned by the poller's "pull request opened" event, which is where
	// the pr sub-object of the emitted event comes from.
	run, token := e.reviewerRun(t, &f, 219, "dev/PAY-14")

	isErr, result := e.callTool(token, "submit_review", map[string]any{
		"summary": "Two problems worth fixing before this merges.",
		"findings": []map[string]any{
			{"severity": "blocker", "title": "Replays are not persisted", "file": "src/idempotency.ts", "line": 3},
			{"severity": "minor", "title": "Expiry is checked lazily", "file": "src/idempotency.ts", "line": 6},
			{"severity": "nit", "title": "Magic number", "file": "src/idempotency.ts", "line": 6},
		},
	})
	if isErr {
		t.Fatalf("submit_review failed: %v", result)
	}
	if result["max_severity"] != "blocker" {
		t.Fatalf("tool result max_severity = %v, want blocker", result["max_severity"])
	}

	evs := e.agentReviewEvents(t, run.ID)
	if len(evs) != 1 {
		t.Fatalf("agent_review events = %d, want exactly 1", len(evs))
	}
	ev := evs[0]
	if ev.ActivityType != "submitted" {
		t.Errorf("activity type = %q, want submitted", ev.ActivityType)
	}
	// Actor: the reviewing agent, by ID — what actor suppression keys on (D-9).
	if ev.ActorKind != domain.ActorAgent || ev.ActorID == nil || *ev.ActorID != f.agent.ID {
		t.Errorf("actor = %s/%v, want agent/%s", ev.ActorKind, ev.ActorID, f.agent.ID)
	}
	// Subject: the pull request, in the same shape the poller uses, so the guard derives the
	// same "pr:219" subject key for both events.
	if ev.SubjectKind != "pr" || ev.SubjectNumber == nil || *ev.SubjectNumber != 219 {
		t.Errorf("subject = %s/%v, want pr/219", ev.SubjectKind, ev.SubjectNumber)
	}
	if ev.SubjectBranch == nil || *ev.SubjectBranch != "dev/PAY-14" {
		t.Errorf("subject branch = %v, want dev/PAY-14 (a follow-on run checks it out)", ev.SubjectBranch)
	}
	// Cause: the reviewer's run, so the depth counter can walk the chain through the review.
	if ev.CauseRunID == nil || *ev.CauseRunID != run.ID {
		t.Errorf("cause run = %v, want %s", ev.CauseRunID, run.ID)
	}

	rev := reviewPayload(t, ev)
	if rev["max_severity"] != "blocker" {
		t.Errorf("review.max_severity = %v, want blocker", rev["max_severity"])
	}
	if rev["state"] != "changes_requested" || rev["intended_state"] != "changes_requested" {
		t.Errorf("review.state/intended_state = %v/%v", rev["state"], rev["intended_state"])
	}
	if rev["findings_count"] != float64(3) {
		t.Errorf("review.findings_count = %v, want 3", rev["findings_count"])
	}
	counts, _ := rev["severity_counts"].(map[string]any)
	want := map[string]float64{"blocker": 1, "major": 0, "minor": 1, "nit": 1}
	for sev, n := range want {
		if counts[sev] != n {
			t.Errorf("review.severity_counts.%s = %v, want %v", sev, counts[sev], n)
		}
	}
	// The pr sub-object is the causing event's, so a rule can address pr.branch and the rest.
	var body struct {
		PR map[string]any `json:"pr"`
	}
	if err := json.Unmarshal(ev.Payload, &body); err != nil {
		t.Fatal(err)
	}
	if body.PR["number"] != float64(219) || body.PR["branch"] != "dev/PAY-14" {
		t.Errorf("pr sub-object = %v", body.PR)
	}
}

// A review with only nits is still an event — with max_severity "nit", which is how a rule
// keyed on blockers and majors declines to fire without anything having to be absent.
func TestSubmitReviewEventCarriesTheLesserVerdicts(t *testing.T) {
	e := newEnv(t)
	f := e.fixtures(domain.AutonomyAuto, domain.AgentPermissions{SubmitReviews: true})
	run, token := e.reviewerRun(t, &f, 7, "dev/PAY-2")

	isErr, result := e.callTool(token, "submit_review", map[string]any{
		"findings": []map[string]any{{"severity": "nit", "title": "Name the constant"}},
	})
	if isErr {
		t.Fatalf("submit_review failed: %v", result)
	}
	evs := e.agentReviewEvents(t, run.ID)
	if len(evs) != 1 {
		t.Fatalf("agent_review events = %d, want 1", len(evs))
	}
	rev := reviewPayload(t, evs[0])
	if rev["max_severity"] != "nit" || rev["state"] != "commented" {
		t.Fatalf("review = %v, want a commented review whose worst severity is a nit", rev)
	}
}

// A review with no findings at all: max_severity is the value "none", not a missing path, so
// `review.max_severity enum.is_not none` is a condition a user can actually write.
func TestSubmitReviewEventSaysNoneWhenThereAreNoFindings(t *testing.T) {
	e := newEnv(t)
	f := e.fixtures(domain.AutonomyAuto, domain.AgentPermissions{SubmitReviews: true})
	run, token := e.reviewerRun(t, &f, 8, "dev/PAY-3")

	isErr, result := e.callTool(token, "submit_review", map[string]any{"summary": "Looks good."})
	if isErr {
		t.Fatalf("submit_review failed: %v", result)
	}
	rev := reviewPayload(t, e.agentReviewEvents(t, run.ID)[0])
	if rev["max_severity"] != "none" || rev["findings_count"] != float64(0) {
		t.Fatalf("review = %v, want max_severity none and no findings", rev)
	}
}

// D-9's 422, end to end: the forge refuses REQUEST_CHANGES from the pull request's own author,
// the tool retries the same body as a COMMENT, and BOTH facts survive — GitHub holds a
// comment, and the run output and the emitted event both say changes were requested.
func TestSubmitReviewFallsBackToCommentAndKeepsTheIntent(t *testing.T) {
	e := newEnv(t)
	e.reviews.rejectRequestChanges = true
	f := e.fixtures(domain.AutonomyAuto, domain.AgentPermissions{SubmitReviews: true})
	run, token := e.reviewerRun(t, &f, 219, "dev/PAY-14")

	isErr, result := e.callTool(token, "submit_review", map[string]any{
		"summary":  "This double-charges.",
		"findings": []map[string]any{{"severity": "blocker", "title": "Replays are not persisted"}},
	})
	if isErr {
		t.Fatalf("submit_review failed: %v", result)
	}
	if result["event"] != "COMMENT" || result["intended_event"] != "REQUEST_CHANGES" {
		t.Fatalf("result event/intended = %v/%v, want COMMENT/REQUEST_CHANGES",
			result["event"], result["intended_event"])
	}
	if e.reviews.count() != 2 {
		t.Fatalf("forge calls = %d, want 2 (the refused REQUEST_CHANGES, then the COMMENT)", e.reviews.count())
	}
	if first := e.reviews.calls[0].event; first != "REQUEST_CHANGES" {
		t.Fatalf("first forge call = %q, want the attempt that GitHub will one day accept", first)
	}
	if e.reviews.last().body != e.reviews.calls[0].body {
		t.Fatal("the fallback posted a different body than the review that was refused")
	}

	rev := reviewPayload(t, e.agentReviewEvents(t, run.ID)[0])
	if rev["state"] != "commented" {
		t.Errorf("review.state = %v, want commented — what GitHub actually stored", rev["state"])
	}
	if rev["intended_state"] != "changes_requested" {
		t.Errorf("review.intended_state = %v, want changes_requested — what the reviewer asked for",
			rev["intended_state"])
	}
	// The severities are untouched by the downgrade: this is the whole point.
	if rev["max_severity"] != "blocker" {
		t.Errorf("review.max_severity = %v, want blocker", rev["max_severity"])
	}

	acts, err := e.st.Activities().ForRun(context.Background(), run.ID)
	if err != nil {
		t.Fatal(err)
	}
	var title string
	for _, a := range acts {
		if a.ToolName == "mcp__lexicode__submit_review" {
			title = a.Title
		}
	}
	if !strings.Contains(title, "request changes") || !strings.Contains(title, "posted as a comment") {
		t.Fatalf("activity title = %q; it must record what was intended, not only what landed", title)
	}
}
