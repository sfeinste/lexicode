// The "human review → Dev addresses it" rule, exercised through the real pipeline.
//
// Its IF row is the SHIPPED condition string bootstrap writes into a fresh project
// (bootstrap.HumanReviewConditions), on the shipped event kind and activity type, with the
// shipped loop config — actor suppression ON. The event it fires on is shaped exactly as the
// poller emits a `pull_request_review`: the actor sub-object the `actor.is_human` operator
// actually reads, alongside the actor_kind column the loop guard reads.
//
// What that buys: the rule is the user's way back INTO a chain the agents are running. If
// the poller's actor vocabulary and this condition ever stop agreeing, a person's review
// silently does nothing — which is the failure the whole fix exists to end.
package triggers

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/spruce/lexicode/internal/domain"
	"github.com/spruce/lexicode/internal/service/bootstrap"
)

// emitReview publishes one `pull_request_review` event on pr:219, shaped the way
// internal/module/github's poller shapes it: actor_kind on the column, and the matching
// actor sub-object in the payload (kind/login/agent).
func (e *guardEnv) emitReview(kind domain.ActorKind, login, agentName string,
	actorID, causeRun *string,
) domain.Event {
	e.t.Helper()
	num := int64(219)
	raw, err := json.Marshal(map[string]any{
		"pr":     map[string]any{"number": 219, "title": "Add idempotency keys", "branch": "dev/PAY-14"},
		"review": map[string]any{"id": "9001", "author": login, "state": "commented", "body": "Two things."},
		"actor":  map[string]any{"kind": string(kind), "login": login, "agent": agentName},
	})
	if err != nil {
		e.t.Fatal(err)
	}
	e.clock++
	occurred := domain.FormatTime(
		time.Date(2026, 8, 21, 12, 0, 0, 0, time.UTC).Add(time.Duration(e.clock) * time.Second))
	ev := domain.Event{
		ID: domain.NewID(), ProjectID: &e.proj.ID, Source: "github.poll",
		Kind: "pull_request_review", ActivityType: "submitted",
		ActorKind: kind, ActorID: actorID, ActorLogin: &login,
		SubjectKind: "pr", SubjectNumber: &num,
		Payload: raw, CauseRunID: causeRun,
		DedupeKey: "t:" + domain.NewID(), OccurredAt: occurred, CreatedAt: domain.Now(),
	}
	if err := e.bus.Emit(e.ctx, ev); err != nil {
		e.t.Fatal(err)
	}
	return ev
}

// mkHumanReviewTrigger is the shipped suggested rule, IF row and all.
func (e *guardEnv) mkHumanReviewTrigger(devID string) domain.Trigger {
	return e.mkTriggerWithConditions("Human review → Dev addresses it",
		"pull_request_review", `["submitted"]`, string(domain.DefaultLoopConfig()),
		bootstrap.HumanReviewConditions, devID)
}

// THE acceptance: a person submits a review and the shipped rule enqueues Dev; a bot's review
// on the same rule does nothing.
//
// Actor suppression is ON (the shipped default). It must not touch this: layer 1 keys on "the
// event's actor IS the agent this rule would run", and a human is not Dev. The firing is
// asserted to be `succeeded` with a real Dev run behind it, not merely non-empty.
func TestHumanReviewRuleFiresForAPersonAndNotForABot(t *testing.T) {
	e := newGuardEnv(t)
	dev := e.mkAgent("Dev")
	tr := e.mkHumanReviewTrigger(dev.ID)

	human := e.emitReview(domain.ActorHuman, "ada", "", nil, nil)
	fr := e.firing(tr.ID, human.ID)
	if fr.Outcome != domain.FiringSucceeded || fr.RunID == nil {
		t.Fatalf("firing = %+v; a person's review must reach the rule unsuppressed", fr)
	}
	run := e.run(*fr.RunID)
	if run.AgentID != dev.ID {
		t.Fatalf("the rule enqueued agent %s, want Dev (%s)", run.AgentID, dev.ID)
	}
	if run.SubjectKey != "pr:219" {
		t.Fatalf("run subject = %q, want pr:219", run.SubjectKey)
	}

	// A bot's review: same kind, same activity type, same rule — the actor is the only
	// difference, and it is the difference the condition is made of.
	bot := e.emitReview(domain.ActorExternal, "dependabot[bot]", "", nil, nil)
	fb := e.firing(tr.ID, bot.ID)
	if fb.Outcome != domain.FiringNoAction || fb.Reason != "conditions not met" {
		t.Fatalf("bot firing = %s (%s), want no_action / conditions not met", fb.Outcome, fb.Reason)
	}
	if fb.RunID != nil {
		t.Fatalf("a bot's review started run %s", *fb.RunID)
	}
}

// TestHumanReviewUnsticksALoopStoppedChain is why fix 1 and fix 2 are one change. A person
// reviewing a pull request whose chain has exhausted its depth budget must get a run out of
// it: architecture §9 says a human action on the subject resets the counter, and that reset
// probes `actor_kind = 'human'` — which no forge event could ever carry before the poller
// learned to read GitHub's user.type.
func TestHumanReviewUnsticksALoopStoppedChain(t *testing.T) {
	e := newGuardEnv(t)
	dev := e.mkAgent("Dev")
	reviewer := e.mkAgent("Reviewer")
	tDev := e.mkTrigger("review → run Dev", "pull_request_review", `["submitted"]`, pingPongConfig, dev.ID)
	tRev := e.mkTrigger("push → run Reviewer", "pull_request", `["synchronize"]`, pingPongConfig, reviewer.ID)

	// Drive pr:219 to the depth limit with an agent ping-pong.
	e1 := e.emit("pull_request_review", "submitted", domain.ActorHuman, nil, nil, "")
	r1 := e.run(*e.firing(tDev.ID, e1.ID).RunID)
	e2 := e.emit("pull_request", "synchronize", domain.ActorAgent, &dev.ID, &r1.ID, "")
	r2 := e.run(*e.firing(tRev.ID, e2.ID).RunID)
	e3 := e.emit("pull_request_review", "submitted", domain.ActorAgent, &reviewer.ID, &r2.ID, "")
	r3 := e.run(*e.firing(tDev.ID, e3.ID).RunID)
	e4 := e.emit("pull_request", "synchronize", domain.ActorAgent, &dev.ID, &r3.ID, "")
	if f := e.firing(tRev.ID, e4.ID); f.Outcome != domain.FiringLoopStopped {
		t.Fatalf("depth-3 firing = %s (%s)", f.Outcome, f.Reason)
	}

	// The person comments the only way the product offers: they review the pull request on
	// GitHub. The shipped rule runs Dev on it — at depth 0, because their review is the new
	// root of the chain, not a continuation of the stalled one.
	tHuman := e.mkHumanReviewTrigger(dev.ID)
	human := e.emitReview(domain.ActorHuman, "ada", "", nil, nil)
	fh := e.firing(tHuman.ID, human.ID)
	if fh.Outcome != domain.FiringSucceeded || fh.RunID == nil {
		t.Fatalf("human review on a loop-stopped subject = %s (%s)", fh.Outcome, fh.Reason)
	}
	if r := e.run(*fh.RunID); r.Depth != 0 || r.AgentID != dev.ID {
		t.Fatalf("human-review run = depth %d agent %s, want depth 0 Dev", r.Depth, r.AgentID)
	}

	// And the chain that was stopped is moving again: the same agent push that was
	// loop-stopped above now runs, because the human's action reset the counter.
	e5 := e.emit("pull_request", "synchronize", domain.ActorAgent, &dev.ID, &r3.ID, "")
	f5 := e.firing(tRev.ID, e5.ID)
	if f5.Outcome != domain.FiringSucceeded || f5.RunID == nil {
		t.Fatalf("post-reset firing = %s (%s)", f5.Outcome, f5.Reason)
	}
}
