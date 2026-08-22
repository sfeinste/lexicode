package github

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/spruce/lexicode/internal/domain"
	"github.com/spruce/lexicode/internal/kernel/guard"
)

// TestPushDrivenPingPongStopsAtDepthThree is the other half of the depth-counter regression:
// the poller and the loop guard together, over one real store.
//
// Brief D5's own example — PR opened → review → address → push → PR updated → review → … — is
// driven by PUSHES, and a push carries no body of its own. The events here come out of the
// real poller reading the fake API; the guard walks the chain those events wrote. With the PR
// body winning attribution, every synchronize points back at the run that OPENED the pull
// request, the chain is one hop deep forever, and layer 4 never trips. With commit identity
// winning, the chain accumulates and the fourth hop is stopped.
//
// The scheduler is the one thing stubbed: after a proceeding verdict the test inserts the run
// row the scheduler would have inserted, carrying the subject key, the depth and the cause
// event — which is exactly what the chain walk reads.
func TestPushDrivenPingPongStopsAtDepthThree(t *testing.T) {
	ph := newPollHarness(t)
	ctx := context.Background()

	reviewer := ph.mkAgent("Reviewer")

	const (
		prNumber = 9
		subject  = "pr:9"
		branch   = "dev/pay-9-idempotency"
	)
	// The run that opened the pull request. Its marker is in the PR body forever.
	opener := ph.run(domain.RunCompleted, branch, ptrTime(time.Date(2026, 8, 1, 11, 30, 0, 0, time.UTC)))
	pr := ghPR{
		Number: prNumber, Title: "Add idempotency keys", State: "open", Login: "svc-bot",
		Body: "Automated change by agent **Dev**.\n\n" +
			domain.Actor{AgentID: ph.agent.ID, RunID: opener.ID}.Marker(),
		HeadRef: branch, HeadSHA: "sha-open", BaseRef: "main",
		CreatedAt: at(11, 0), UpdatedAt: at(11, 30),
	}
	ph.gh.upsertPR(pr)
	ph.gh.commitEmails["sha-open"] = "dev@agents.lexicode.local"
	ph.gh.commitMessages["sha-open"] = "feat: idempotency keys\n\nLexicode-Run: " + opener.ID
	ph.tick() // baseline: the PR is recorded, nothing is emitted

	// Two rules that bounce off each other, both on the shipped loop defaults except for the
	// debounce, which this test does not exercise (the fixture clock does not advance).
	const loopConfig = `{"actor_suppression":true,"debounce_seconds":0,` +
		`"cancel_in_progress":false,"depth_limit":3,"daily_budget_cents":null}`
	tDev := ph.trigger("review submitted → Dev addresses it", "pull_request_review", loopConfig)
	tRev := ph.trigger("push → Reviewer looks again", "pull_request", loopConfig)

	layers := guard.New(guard.Options{
		Store:  ph.st,
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	evaluate := func(tr domain.Trigger, agentID string, ev domain.Event) guard.Verdict {
		t.Helper()
		var payload map[string]any
		if err := json.Unmarshal(ev.Payload, &payload); err != nil {
			t.Fatal(err)
		}
		return layers.Evaluate(ctx, guard.Input{
			Event: ev, Trigger: tr, SubjectKey: subject,
			Payload: payload, RunAgentIDs: []string{agentID},
		})
	}

	// ---- hop 0: a human asks for changes. Dev runs at depth 0. -------------------------
	ph.gh.reviews = append(ph.gh.reviews, ghReview{
		ID: 5001, PR: prNumber, Login: "carol", State: "CHANGES_REQUESTED",
		Body: "Two problems worth fixing.", SubmittedAt: at(12, 0),
	})
	pr.UpdatedAt = at(12, 0)
	ph.gh.upsertPR(pr)
	ph.tick()

	e1 := ph.lastEvent("pull_request_review", "submitted")
	v := evaluate(tDev, ph.agent.ID, e1)
	if !v.Proceed || v.Pass.Depth != 0 {
		t.Fatalf("human review verdict = %+v, want proceed at depth 0", v)
	}
	r1 := ph.chainRun(ph.agent.ID, subject, e1.ID, v.Pass.Depth)

	// ---- hop 1: Dev pushes to the pull request's branch. Reviewer runs at depth 1. -----
	pr.HeadSHA = "sha-address-1"
	pr.UpdatedAt = at(12, 20)
	ph.gh.upsertPR(pr)
	ph.gh.commitEmails["sha-address-1"] = "dev@agents.lexicode.local"
	ph.gh.commitMessages["sha-address-1"] = "fix: address the review\n\nLexicode-Run: " + r1.ID
	ph.tick()

	e2 := ph.lastEvent("pull_request", "synchronize")
	if e2.CauseRunID == nil || *e2.CauseRunID != r1.ID {
		got := "nothing"
		if e2.CauseRunID != nil {
			got = *e2.CauseRunID
		}
		t.Fatalf("the push attributes to %s, want the pushing run %s (the PR body still names "+
			"the opening run %s)", got, r1.ID, opener.ID)
	}
	v = evaluate(tRev, reviewer.ID, e2)
	if !v.Proceed || v.Pass.Depth != 1 {
		t.Fatalf("push verdict = %+v, want proceed at depth 1", v)
	}
	r2 := ph.chainRun(reviewer.ID, subject, e2.ID, v.Pass.Depth)

	// ---- hop 2: Reviewer requests changes again. Dev runs at depth 2. ------------------
	ph.gh.reviews = append(ph.gh.reviews, ghReview{
		ID: 5002, PR: prNumber, Login: "svc-bot", State: "CHANGES_REQUESTED",
		Body: "Still not persisted.\n\n" +
			domain.Actor{AgentID: reviewer.ID, RunID: r2.ID}.Marker(),
		SubmittedAt: at(12, 40),
	})
	pr.UpdatedAt = at(12, 40)
	ph.gh.upsertPR(pr)
	ph.tick()

	e3 := ph.lastEvent("pull_request_review", "submitted")
	v = evaluate(tDev, ph.agent.ID, e3)
	if !v.Proceed || v.Pass.Depth != 2 {
		t.Fatalf("second review verdict = %+v, want proceed at depth 2", v)
	}
	r3 := ph.chainRun(ph.agent.ID, subject, e3.ID, v.Pass.Depth)

	// ---- hop 3: Dev pushes again. The guard stops the loop at depth 3. -----------------
	pr.HeadSHA = "sha-address-2"
	pr.UpdatedAt = at(13, 0)
	ph.gh.upsertPR(pr)
	ph.gh.commitEmails["sha-address-2"] = "dev@agents.lexicode.local"
	ph.gh.commitMessages["sha-address-2"] = "fix: sweep expired keys\n\nLexicode-Run: " + r3.ID
	ph.tick()

	e4 := ph.lastEvent("pull_request", "synchronize")
	v = evaluate(tRev, reviewer.ID, e4)
	if v.Proceed {
		t.Fatalf("fourth hop proceeded at depth %d; the cycle brief D5 names never stops",
			v.Pass.Depth)
	}
	if v.Outcome != domain.FiringLoopStopped {
		t.Fatalf("fourth hop outcome = %s (%s), want loop_stopped", v.Outcome, v.Reason)
	}
	if !strings.Contains(v.Reason, "depth 3 reached the limit of 3") ||
		!strings.Contains(v.Reason, subject) {
		t.Fatalf("stop reason = %q, want it to name depth 3 and %s", v.Reason, subject)
	}
	t.Logf("chain: %s → %s → %s → %s → %s → %s → %s → stopped: %s",
		short(e1.ID), short(r1.ID), short(e2.ID), short(r2.ID), short(e3.ID), short(r3.ID),
		short(e4.ID), v.Reason)
}

// short renders an id's tail — ULIDs share their leading characters.
func short(id string) string {
	if len(id) > 6 {
		return "…" + id[len(id)-6:]
	}
	return id
}

func ptrTime(t time.Time) *time.Time { return &t }
