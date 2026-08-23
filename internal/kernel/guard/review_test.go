package guard

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/spruce/lexicode/internal/domain"
)

// mkReviewEvent inserts an event carrying the payload a review submission or one of its inline
// comments arrives with — the shape LEXI-10's coalescing keys on.
func (f *fix) mkReviewEvent(kind, reviewID, commentID string) domain.Event {
	f.t.Helper()
	num := int64(219)
	payload := map[string]any{"pr": map[string]any{"number": 219}}
	if kind == "pull_request_review" {
		payload["review"] = map[string]any{"id": reviewID, "body": "Five things."}
	} else {
		payload["comment"] = map[string]any{
			"id": commentID, "review_id": reviewID, "body": "this is off by one",
			"path": "internal/api/rate.go", "line": 42,
		}
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		f.t.Fatal(err)
	}
	now := domain.Now()
	e := domain.Event{
		ID: domain.NewID(), ProjectID: &f.proj.ID, Source: "github.poll",
		Kind: kind, ActivityType: "submitted", ActorKind: domain.ActorHuman,
		SubjectKind: "pr", SubjectNumber: &num, Payload: raw,
		DedupeKey: "t:" + domain.NewID(), DispatchState: domain.DispatchDone,
		OccurredAt: now, CreatedAt: now,
	}
	if err := f.st.Events().Insert(f.ctx, &e); err != nil {
		f.t.Fatal(err)
	}
	return e
}

// inputFor is f.input with the event's real payload, which is what the guard reads the review
// id out of.
func (f *fix) inputFor(tr domain.Trigger, ev domain.Event) Input {
	in := f.input(tr, ev)
	var payload map[string]any
	if err := json.Unmarshal(ev.Payload, &payload); err != nil {
		f.t.Fatal(err)
	}
	in.Payload = payload
	return in
}

// TestReviewCoalescingIsOneRunPerReview is LEXI-10's second acceptance criterion at the guard:
// a review with five inline comments arrives as six events, and only the first of them starts
// a run — the rest are absorbed by the run that was already given the whole review.
func TestReviewCoalescingIsOneRunPerReview(t *testing.T) {
	f := newFix(t)
	tr := f.mkTrigger(`{"cancel_in_progress":false,"depth_limit":0}`) // debounce 90s by default

	// The review summary event fires first and starts the run.
	summary := f.mkReviewEvent("pull_request_review", "555", "")
	if v := f.eval(nil).Evaluate(f.ctx, f.inputFor(tr, summary)); !v.Proceed {
		t.Fatalf("summary event verdict = %+v, want proceed", v)
	}
	run := f.mkRun(tr, domain.RunQueued, "pr:219", &summary.ID, domain.Now())

	// Its five inline comments arrive next; every one of them is already in that run's
	// prompt, so every one is absorbed by it.
	for i := 0; i < 5; i++ {
		ev := f.mkReviewEvent("pull_request_review_comment", "555", domain.NewID())
		v := f.eval(nil).Evaluate(f.ctx, f.inputFor(tr, ev))
		if v.Proceed {
			t.Fatalf("comment %d verdict = %+v, want absorbed", i+1, v)
		}
		if v.Outcome != domain.FiringDebounced {
			t.Fatalf("comment %d outcome = %q, want %q", i+1, v.Outcome, domain.FiringDebounced)
		}
		if v.AbsorbedByRunID == nil || *v.AbsorbedByRunID != run.ID {
			t.Fatalf("comment %d absorbed_by = %v, want %s", i+1, v.AbsorbedByRunID, run.ID)
		}
		// The run is inside the ordinary debounce window too, so the reason is what tells
		// the two apart: without layer 2a this is the time-keyed absorption, which would
		// let the sixth comment through as soon as the window passed.
		if !strings.Contains(v.Reason, "review 555 was already given") {
			t.Fatalf("comment %d reason = %q, want the review-keyed absorption", i+1, v.Reason)
		}
	}
}

// TestReviewCoalescingSurvivesTheDebounceWindow: the fragments of one review can straddle two
// poll ticks or a restart, so coalescing is keyed on the review, not on time. A run far older
// than the debounce window still absorbs its own review's comments.
func TestReviewCoalescingSurvivesTheDebounceWindow(t *testing.T) {
	f := newFix(t)
	tr := f.mkTrigger(`{"cancel_in_progress":false,"depth_limit":0}`)
	summary := f.mkReviewEvent("pull_request_review", "555", "")
	run := f.mkRun(tr, domain.RunCompleted, "pr:219", &summary.ID, stamp(0)) // long past

	ev := f.mkReviewEvent("pull_request_review_comment", "555", "9001")
	v := f.eval(nil).Evaluate(f.ctx, f.inputFor(tr, ev))
	if v.Proceed || v.AbsorbedByRunID == nil || *v.AbsorbedByRunID != run.ID {
		t.Fatalf("verdict = %+v, want absorbed by %s", v, run.ID)
	}
}

// TestANewReviewIsANewRun: coalescing must not swallow the next review. A second review on the
// same pull request has its own id and gets its own run.
func TestANewReviewIsANewRun(t *testing.T) {
	f := newFix(t)
	tr := f.mkTrigger(`{"debounce_seconds":1,"cancel_in_progress":false,"depth_limit":0}`)
	first := f.mkReviewEvent("pull_request_review", "555", "")
	f.mkRun(tr, domain.RunCompleted, "pr:219", &first.ID, stamp(0))

	second := f.mkReviewEvent("pull_request_review", "556", "")
	if v := f.eval(nil).Evaluate(f.ctx, f.inputFor(tr, second)); !v.Proceed {
		t.Fatalf("second review verdict = %+v, want proceed", v)
	}
}

// TestLoneCommentIsNotCoalesced: a comment the forge grouped into no review carries no review
// id, so there is nothing to coalesce on and the ordinary layers decide (LEXI-10's fourth
// criterion, guard half).
func TestLoneCommentIsNotCoalesced(t *testing.T) {
	f := newFix(t)
	tr := f.mkTrigger(`{"debounce_seconds":1,"cancel_in_progress":false,"depth_limit":0}`)
	reviewed := f.mkReviewEvent("pull_request_review", "555", "")
	f.mkRun(tr, domain.RunCompleted, "pr:219", &reviewed.ID, stamp(0))

	lone := f.mkReviewEvent("pull_request_review_comment", "", "9002")
	if v := f.eval(nil).Evaluate(f.ctx, f.inputFor(tr, lone)); !v.Proceed {
		t.Fatalf("lone comment verdict = %+v, want proceed", v)
	}
}

// TestReviewCoalescingRespectsDebounceOff: a rule with debounce switched off has asked for one
// run per event, and coalescing does not override that.
func TestReviewCoalescingRespectsDebounceOff(t *testing.T) {
	f := newFix(t)
	tr := f.mkTrigger(`{"debounce_seconds":0,"cancel_in_progress":false,"depth_limit":0}`)
	summary := f.mkReviewEvent("pull_request_review", "555", "")
	f.mkRun(tr, domain.RunQueued, "pr:219", &summary.ID, domain.Now())

	ev := f.mkReviewEvent("pull_request_review_comment", "555", "9003")
	if v := f.eval(nil).Evaluate(f.ctx, f.inputFor(tr, ev)); !v.Proceed {
		t.Fatalf("verdict = %+v, want proceed with debounce off", v)
	}
}

// TestReviewCoalescingIsPerTrigger: another rule's run does not absorb this rule's firing —
// two agents watching the same review each get their own run.
func TestReviewCoalescingIsPerTrigger(t *testing.T) {
	f := newFix(t)
	mine := f.mkTrigger(`{"cancel_in_progress":false,"depth_limit":0}`)
	other := f.mkTrigger(`{"cancel_in_progress":false,"depth_limit":0}`)
	summary := f.mkReviewEvent("pull_request_review", "555", "")
	f.mkRun(other, domain.RunQueued, "pr:219", &summary.ID, domain.Now())

	ev := f.mkReviewEvent("pull_request_review_comment", "555", "9004")
	if v := f.eval(nil).Evaluate(f.ctx, f.inputFor(mine, ev)); !v.Proceed {
		t.Fatalf("verdict = %+v, want proceed for a different trigger", v)
	}
}

// TestReviewIDOf pins the two payload shapes a review id can arrive in, and the shapes that
// carry none.
func TestReviewIDOf(t *testing.T) {
	cases := []struct {
		name    string
		payload string
		want    string
	}{
		{"review submission", `{"review":{"id":"555"}}`, "555"},
		{"inline comment", `{"comment":{"review_id":"555"}}`, "555"},
		{"comment outside a review", `{"comment":{"review_id":""}}`, ""},
		{"conversation comment", `{"comment":{"id":"7"}}`, ""},
		{"a push", `{"pr":{"number":219}}`, ""},
		{"a numeric id is not a string id", `{"review":{"id":555}}`, ""},
	}
	for _, c := range cases {
		var payload map[string]any
		if err := json.Unmarshal([]byte(c.payload), &payload); err != nil {
			t.Fatal(err)
		}
		if got := reviewIDOf(payload); got != c.want {
			t.Errorf("%s: reviewIDOf(%s) = %q, want %q", c.name, c.payload, got, c.want)
		}
	}
}
