// The "changes requested → Dev addresses them" rule, exercised end to end against the event
// that actually carries the verdict.
//
// Nothing here is a stand-in for the thing under test: the review is submitted through the
// REAL MCP server's `submit_review` tool over JSON-RPC, the event it publishes rides the REAL
// bus into the REAL engine, stage 3 is the REAL loop guard, and the rule's IF row is the
// SHIPPED condition string bootstrap writes into a fresh project. The only reduced piece is
// the run_agent action (the guard harness's testRunAgent, which calls Scheduler.Enqueue and
// nothing else — see engine_guard_test.go).
//
// What that buys: if the emitter's payload and the suggested rule's condition ever stop
// agreeing — a renamed field, a different severity vocabulary — this test fails, rather than
// a user's project quietly never continuing its review loop.
package triggers

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/spruce/lexicode/internal/domain"
	"github.com/spruce/lexicode/internal/kernel/audit"
	"github.com/spruce/lexicode/internal/service/bootstrap"
	mcpsvc "github.com/spruce/lexicode/internal/service/mcp"
)

const prNumber = 219

// reviewFixture is one reviewer run, ready to submit a review through the MCP server.
type reviewFixture struct {
	reviewer domain.Agent
	dev      domain.Agent
	run      domain.Run
	callTool func(t *testing.T, args map[string]any) map[string]any
}

// newReviewFixture wires the MCP server onto the guard env's store and bus, inserts the two
// agents and the reviewer run the way the scheduler would, and returns a way to call
// submit_review as that run.
func (e *guardEnv) newReviewFixture(t *testing.T) reviewFixture {
	t.Helper()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	reviewer := e.mkAgent("Reviewer")
	dev := e.mkAgent("Dev")

	// The event that spawned the reviewer: the poller's "pull request opened". Its payload is
	// where the emitted event's pr sub-object comes from.
	num := int64(prNumber)
	branch := "dev/PAY-14"
	e.clock++
	opened := domain.Event{
		ID: domain.NewID(), ProjectID: &e.proj.ID, Source: "github.poll",
		Kind: "pull_request", ActivityType: "opened",
		ActorKind: domain.ActorAgent, ActorID: &dev.ID,
		SubjectKind: "pr", SubjectNumber: &num, SubjectBranch: &branch,
		Payload: json.RawMessage(fmt.Sprintf(
			`{"pr":{"number":%d,"title":"Add idempotency keys","branch":%q,"base":"main",`+
				`"author_kind":"agent","labels":[]},"repo":{"owner":"acme","name":"payments"},`+
				`"actor":{"kind":"agent","login":"bot","agent":"Dev"}}`, num, branch)),
		DedupeKey:     "t:" + domain.NewID(),
		DispatchState: domain.DispatchDone,
		OccurredAt: domain.FormatTime(
			time.Date(2026, 8, 21, 12, 0, 0, 0, time.UTC).Add(time.Duration(e.clock) * time.Second)),
		CreatedAt: domain.Now(),
	}
	if err := e.st.Events().Insert(e.ctx, &opened); err != nil {
		t.Fatal(err)
	}
	run := domain.Run{
		ID: domain.NewID(), Seq: 1, ProjectID: e.proj.ID, AgentID: reviewer.ID,
		CauseEventID: &opened.ID, State: domain.RunRunning, Autonomy: domain.AutonomyAuto,
		Model: reviewer.Model, Effort: reviewer.Effort, Prompt: "review it",
		RuntimeID: "scripted", SandboxID: "fake",
		SubjectKey: fmt.Sprintf("pr:%d", prNumber), QueuedAt: domain.Now(),
	}
	if err := e.st.Runs().Create(e.ctx, &run); err != nil {
		t.Fatal(err)
	}

	// The forge seam: what service/runs.ReviewSubmitter does, minus GitHub.
	submit := func(_ context.Context, r domain.Run, n int, event, body string) (domain.Review, error) {
		return domain.Review{ID: 5002336620, PRNumber: n, State: event, AuthorLogin: "lexicode-bot"}, nil
	}
	srv := mcpsvc.New(mcpsvc.Options{
		Store: e.st, Bus: e.bus, Logger: logger,
		Audit:        audit.New(audit.Options{Store: e.st, Logger: logger}),
		SubmitReview: submit,
	})
	token, err := srv.MintToken(run.ID)
	if err != nil {
		t.Fatal(err)
	}
	mcpHTTP := httptest.NewServer(srv.Handler())
	t.Cleanup(mcpHTTP.Close)

	call := func(t *testing.T, args map[string]any) map[string]any {
		t.Helper()
		return callMCPTool(t, mcpHTTP.URL+"/mcp/"+token, "submit_review", args)
	}
	return reviewFixture{reviewer: reviewer, dev: dev, run: run, callTool: call}
}

// callMCPTool posts one tools/call and returns the tool's decoded JSON result, failing the
// test if the call itself errored.
func callMCPTool(t *testing.T, url, tool string, args map[string]any) map[string]any {
	t.Helper()
	msg, err := json.Marshal(map[string]any{
		"jsonrpc": "2.0", "id": 1, "method": "tools/call",
		"params": map[string]any{"name": tool, "arguments": args},
	})
	if err != nil {
		t.Fatal(err)
	}
	resp, err := http.Post(url, "application/json", bytes.NewReader(msg))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("%s = HTTP %d: %s", tool, resp.StatusCode, raw)
	}
	var env struct {
		Result struct {
			IsError bool `json:"isError"`
			Content []struct {
				Text string `json:"text"`
			} `json:"content"`
		} `json:"result"`
	}
	if err := json.Unmarshal(raw, &env); err != nil {
		t.Fatalf("%s: %v (%s)", tool, err, raw)
	}
	if len(env.Result.Content) == 0 {
		t.Fatalf("%s returned no content: %s", tool, raw)
	}
	if env.Result.IsError {
		t.Fatalf("%s failed: %s", tool, env.Result.Content[0].Text)
	}
	var out map[string]any
	if err := json.Unmarshal([]byte(env.Result.Content[0].Text), &out); err != nil {
		t.Fatalf("%s result is not JSON: %s", tool, env.Result.Content[0].Text)
	}
	return out
}

// mkChangesRequestedTrigger is the shipped suggested rule: bootstrap's own condition string,
// on bootstrap's own event kind and activity type, with the shipped loop config — actor
// suppression ON, which is the half of this that has to be proved rather than assumed.
func (e *guardEnv) mkChangesRequestedTrigger(devID string) domain.Trigger {
	return e.mkTriggerWithConditions("Changes requested → Dev addresses them",
		"agent_review", `["submitted"]`, string(domain.DefaultLoopConfig()),
		bootstrap.ChangesRequestedConditions, devID)
}

// mkTriggerWithConditions is mkTrigger with an IF row.
func (e *guardEnv) mkTriggerWithConditions(name, event, activities, loopConfig, conditions, agentID string) domain.Trigger {
	e.t.Helper()
	tr := e.mkTrigger(name, event, activities, loopConfig, agentID)
	tr.Conditions = json.RawMessage(conditions)
	tr.UpdatedAt = domain.Now()
	if err := e.st.Triggers().Update(e.ctx, &tr); err != nil {
		e.t.Fatal(err)
	}
	return tr
}

// agentReviewEvent returns the one agent_review event the run published.
func (e *guardEnv) agentReviewEvent(t *testing.T, runID string) domain.Event {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		evs, err := e.st.Events().ByCauseRun(e.ctx, runID)
		if err != nil {
			t.Fatal(err)
		}
		for _, ev := range evs {
			if ev.Kind == "agent_review" {
				return ev
			}
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("run %s published no agent_review event", runID)
	return domain.Event{}
}

// THE acceptance: a reviewer agent submits a review with a blocker, and the shipped
// "changes requested" rule enqueues Dev on the same pull request.
//
// Actor suppression is ON, the shipped default, and the event's actor is the Reviewer agent.
// It must not suppress a rule that runs Dev — layer 1 keys on "the agent this rule would run",
// not on "an agent did something". If that ever inverts, the review loop dies silently, so the
// firing is asserted to be `succeeded` with a real run behind it rather than merely non-empty.
func TestChangesRequestedRuleFiresOnTheAgentReviewEvent(t *testing.T) {
	e := newGuardEnv(t)
	f := e.newReviewFixture(t)
	tr := e.mkChangesRequestedTrigger(f.dev.ID)

	result := f.callTool(t, map[string]any{
		"summary": "Two problems worth fixing before this merges.",
		"findings": []map[string]any{
			{"severity": "blocker", "title": "Replays are not persisted", "file": "src/idempotency.ts", "line": 3},
			{"severity": "nit", "title": "Magic number", "file": "src/idempotency.ts", "line": 6},
		},
	})
	if result["max_severity"] != "blocker" {
		t.Fatalf("submit_review result = %v", result)
	}

	ev := e.agentReviewEvent(t, f.run.ID)
	if ev.ActorID == nil || *ev.ActorID != f.reviewer.ID {
		t.Fatalf("the event's actor is %v, want the Reviewer agent %s", ev.ActorID, f.reviewer.ID)
	}

	fr := e.firing(tr.ID, ev.ID)
	if fr.Outcome != domain.FiringSucceeded || fr.RunID == nil {
		t.Fatalf("firing = %+v; a Reviewer-caused event must not suppress a rule that runs Dev", fr)
	}
	run := e.run(*fr.RunID)
	if run.AgentID != f.dev.ID {
		t.Fatalf("the rule enqueued agent %s, want Dev (%s)", run.AgentID, f.dev.ID)
	}
	// The guard derived the same subject key the poller's events on this pull request get,
	// from the emitter's subject columns rather than a catalog template — which is what keeps
	// debounce, cancel-in-progress and the depth counter counting one pull request.
	if run.SubjectKey != fmt.Sprintf("pr:%d", prNumber) {
		t.Fatalf("subject key = %q, want pr:%d", run.SubjectKey, prNumber)
	}
	// Depth 1: the review is one agent-caused hop from the run that produced it. The counter
	// treats this event exactly like a poller event, which is the point.
	if run.Depth != 1 {
		t.Fatalf("depth = %d, want 1 (the reviewer run is one hop up the chain)", run.Depth)
	}
}

// A review whose worst finding is a nit does not start a Dev run. The condition is the
// reviewer's own verdict, so "nothing blocking" is a real answer and not an absence.
func TestChangesRequestedRuleIgnoresANitOnlyReview(t *testing.T) {
	e := newGuardEnv(t)
	f := e.newReviewFixture(t)
	tr := e.mkChangesRequestedTrigger(f.dev.ID)

	f.callTool(t, map[string]any{
		"summary":  "Reads well.",
		"findings": []map[string]any{{"severity": "nit", "title": "Name the constant"}},
	})
	ev := e.agentReviewEvent(t, f.run.ID)
	fr := e.firing(tr.ID, ev.ID)
	if fr.Outcome != domain.FiringNoAction || fr.RunID != nil {
		t.Fatalf("firing = %+v, want no_action with no run", fr)
	}
}

// One review, two events — and the rule fires once.
//
// Both events exist for the same review by design: this one, published the moment the tool
// succeeds, and the poller's `pull_request_review` a tick later. They are different rows with
// different IDs, so the firings unique index on (trigger_id, event_id) cannot be what stops a
// double run. What stops it is that a trigger names ONE event kind: the poller's event does
// not match this rule at stage 1, and writes no firing row at all.
//
// The unique index still does its own job — a re-dispatched event (boot recovery) is skipped —
// and that is asserted here too, because the two protections cover different failures.
func TestOneReviewCannotFireTheRuleTwice(t *testing.T) {
	e := newGuardEnv(t)
	f := e.newReviewFixture(t)
	tr := e.mkChangesRequestedTrigger(f.dev.ID)

	f.callTool(t, map[string]any{
		"summary":  "This double-charges.",
		"findings": []map[string]any{{"severity": "blocker", "title": "Replays are not persisted"}},
	})
	ev := e.agentReviewEvent(t, f.run.ID)
	if fr := e.firing(tr.ID, ev.ID); fr.Outcome != domain.FiringSucceeded {
		t.Fatalf("firing = %+v", fr)
	}

	// 1. The poller's event for the SAME review, with the state GitHub stored. A different
	//    kind, so the rule never sees it.
	poller := e.emit("pull_request_review", "submitted", domain.ActorAgent, &f.reviewer.ID, &f.run.ID, "")

	// 2. The internal event delivered a second time, as boot recovery would.
	e.engine.process(e.ctx, ev)

	firings, err := e.st.Firings().ForTrigger(e.ctx, tr.ID, 100)
	if err != nil {
		t.Fatal(err)
	}
	if len(firings) != 1 {
		t.Fatalf("the rule fired %d times for one review: %+v", len(firings), firings)
	}
	if firings[0].EventID != ev.ID {
		t.Fatalf("the firing is for event %s, want the agent_review event %s", firings[0].EventID, ev.ID)
	}
	if firings[0].EventID == poller.ID {
		t.Fatal("the rule fired on the poller's event as well as its own")
	}
	runs, err := e.st.Runs().ByCauseEvent(e.ctx, ev.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(runs) != 1 {
		t.Fatalf("one review started %d Dev runs, want 1", len(runs))
	}
}
