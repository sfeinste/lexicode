package github

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	gh "github.com/google/go-github/v90/github"

	"github.com/spruce/lexicode/internal/kernel"
	"github.com/spruce/lexicode/internal/kernel/ports"
)

// LEXI-9. The live bug: a PAT without GitHub's "Checks: read" permission made the check-suites
// pass 403 on every tick; one failing pass failed the whole tick, so the worker backed off
// exponentially and a 30-second poll became a 15-minute one for pull requests, reviews and
// comments too — all four of which the token could read perfectly well. Nothing surfaced it:
// the module still said `ready`, and the only trace was a WARN line in a log file.
//
// These four tests pin each half of the fix: the passes are independent, a permanent refusal
// disables itself loudly-once and says what to grant, a transient one backs off and recovers,
// and neither changes the worker's cadence.

const deniedBody = `{"message":"Resource not accessible by personal access token"}`

// healthLog is the sequence of "state|reason" transitions the harness's kernel sink recorded.
func (h *harness) healthLog() []string {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]string(nil), h.health...)
}

func (h *harness) lastHealth(t *testing.T) string {
	t.Helper()
	log := h.healthLog()
	if len(log) == 0 {
		t.Fatalf("no module health transition was reported")
	}
	return log[len(log)-1]
}

// baselineWithTwoPRs seeds the two-open-PR snapshot and takes the silent cold-start tick, so
// each test below starts from a warm, cursored poller.
func baselineWithTwoPRs(t *testing.T) *pollHarness {
	t.Helper()
	ph := newPollHarness(t)
	ph.gh.upsertPR(ghPR{
		Number: 101, Title: "Fix rounding", Body: "Fix the rounding", State: "open",
		Login: "alice", HeadRef: "feature/rounding", HeadSHA: "sha101a", BaseRef: "main",
		Labels: []string{"bug"}, Additions: 10, Deletions: 2, ChangedFiles: 1,
		CreatedAt: at(9, 0), UpdatedAt: at(10, 0),
	})
	ph.gh.upsertPR(ghPR{
		Number: 102, Title: "Refactor ledger", Body: "WIP", State: "open",
		Login: "bob", HeadRef: "feature/ledger", HeadSHA: "sha102a", BaseRef: "main",
		CreatedAt: at(9, 30), UpdatedAt: at(10, 30),
	})
	ph.tick() // baseline: records state, seeds cursors, emits nothing
	return ph
}

// activity mutates the snapshot so that all four readable resources have something to report:
// a push on 101, a review on 101, a review comment on 101, an issue comment on 102.
func (ph *pollHarness) activity() {
	ph.gh.upsertPR(ghPR{
		Number: 101, Title: "Fix rounding", Body: "Fix the rounding", State: "open",
		Login: "alice", HeadRef: "feature/rounding", HeadSHA: "sha101b", BaseRef: "main",
		Labels: []string{"bug"}, Additions: 14, Deletions: 3, ChangedFiles: 2,
		CreatedAt: at(9, 0), UpdatedAt: at(12, 10),
	})
	ph.gh.reviews = append(ph.gh.reviews, ghReview{
		ID: 9001, PR: 101, Login: "carol", State: "APPROVED", Body: "Ship it",
		SubmittedAt: at(12, 11),
	})
	ph.gh.reviewComments = append(ph.gh.reviewComments, ghComment{
		ID: 6001, Subject: 101, Login: "carol", Body: "nit", Path: "a.go", Line: 3,
		CreatedAt: at(12, 11), UpdatedAt: at(12, 11),
	})
	ph.gh.issueComments = append(ph.gh.issueComments, ghComment{
		ID: 7001, Subject: 102, Login: "alice", Body: "Needs a test",
		CreatedAt: at(12, 12), UpdatedAt: at(12, 12),
	})
	ph.gh.suites = append(ph.gh.suites, ghSuite{
		ID: 5001, HeadSHA: "sha101b", HeadBranch: "feature/rounding",
		Status: "completed", Conclusion: "failure", App: "GitHub Actions",
		UpdatedAt: at(12, 13),
	})
}

func (ph *pollHarness) cursor(t *testing.T, resource string) string {
	t.Helper()
	cur, err := ph.st.PollCursors().Get(context.Background(), ph.project.ID, resource)
	if err != nil {
		t.Fatalf("read %s cursor: %v", resource, err)
	}
	return cur.Cursor
}

// TestDeniedCheckSuitesDoesNotStopTheOtherPasses is the acceptance for fix (1): the 403 on
// check suites must cost the tick nothing but check suites.
func TestDeniedCheckSuitesDoesNotStopTheOtherPasses(t *testing.T) {
	ph := baselineWithTwoPRs(t)
	before := map[string]string{
		resPulls:          ph.cursor(t, resPulls),
		resReviewComments: ph.cursor(t, resReviewComments),
		resIssueComments:  ph.cursor(t, resIssueComments),
		resCheckSuites:    ph.cursor(t, resCheckSuites),
	}

	ph.gh.failWith(resCheckSuites, 403, deniedBody)
	ph.activity()

	// The tick must not report failure: the worker counts these, and a counted failure is
	// what put the whole poll on a 15-minute backoff.
	if err := ph.p.tick(context.Background(), ph.project.ID); err != nil {
		t.Fatalf("tick with a denied resource returned an error: %v", err)
	}
	if ph.gh.faultHits(resCheckSuites) == 0 {
		t.Fatal("the check-suites endpoint was never called; the fixture did not fire")
	}

	// All four readable passes emitted.
	seen := map[string]bool{}
	for _, e := range ph.collected() {
		seen[e.Kind+"/"+e.ActivityType] = true
	}
	for _, want := range []string{
		"pull_request/synchronize",
		"pull_request_review/submitted",
		"pull_request_review_comment/created",
		"issue_comment/created",
	} {
		if !seen[want] {
			t.Errorf("no %s event; got %v", want, seen)
		}
	}
	if seen["check_suite/completed"] {
		t.Error("a check_suite event was emitted from an endpoint that answered 403")
	}

	// Their cursors advanced; the denied resource's did not, so nothing is skipped when the
	// token is fixed.
	for _, res := range []string{resPulls, resReviewComments, resIssueComments} {
		if ph.cursor(t, res) == before[res] {
			t.Errorf("%s cursor did not advance (still %q)", res, before[res])
		}
	}
	if got := ph.cursor(t, resCheckSuites); got != before[resCheckSuites] {
		t.Errorf("check_suites cursor advanced to %q over a 403; want %q", got, before[resCheckSuites])
	}
}

// TestDeniedResourceDisablesItselfAndSaysWhy is the acceptance for fix (2).
func TestDeniedResourceDisablesItselfAndSaysWhy(t *testing.T) {
	ph := baselineWithTwoPRs(t)
	ph.gh.failWith(resCheckSuites, 403, deniedBody)
	ph.activity()

	ph.tick()
	hitsAfterFirst := ph.gh.faultHits(resCheckSuites)
	if hitsAfterFirst == 0 {
		t.Fatal("the check-suites endpoint was never called")
	}

	// Degraded, with a reason a user can act on without opening a log file.
	last := ph.lastHealth(t)
	if !strings.HasPrefix(last, string(kernel.StateDegraded)+"|") {
		t.Fatalf("module state = %q, want degraded", last)
	}
	for _, want := range []string{
		"Polling check suites on acme/payments is disabled",
		"HTTP 403",
		"No check_suite events (CI results) will fire",
		"every other GitHub event is still polled at the normal interval",
		`the "Checks" repository permission (read access)`,
		`a classic PAT needs the "repo" scope`,
		"reconnect the repository",
	} {
		if !strings.Contains(last, want) {
			t.Errorf("degraded reason is missing %q; reason was:\n%s", want, last)
		}
	}

	// Subsequent ticks neither call the endpoint nor say anything more about it.
	for i := 0; i < 5; i++ {
		ph.clock = ph.clock.Add(30 * time.Second)
		ph.tick()
	}
	if got := ph.gh.faultHits(resCheckSuites); got != hitsAfterFirst {
		t.Errorf("the disabled resource was polled again: %d hits, want %d", got, hitsAfterFirst)
	}
	if n := strings.Count(ph.logText(), "polling disabled"); n != 1 {
		t.Errorf("the refusal was logged %d times, want exactly 1", n)
	}

	// The long re-check gets the poller back on its own once the token is fixed — no restart.
	ph.gh.clearFault(resCheckSuites)
	ph.clock = ph.clock.Add(deniedRecheck)
	ph.tick()
	if got := ph.lastHealth(t); got != string(kernel.StateReady)+"|" {
		t.Errorf("module state after recovery = %q, want ready with no reason", got)
	}
	var sawCheck bool
	for _, e := range ph.collected() {
		if e.Kind == "check_suite" {
			sawCheck = true
		}
	}
	if !sawCheck {
		t.Error("no check_suite event after the resource recovered")
	}
}

// TestTransientResourceFailureBacksOffAndRecovers is the acceptance for fix (3): a 500 is not
// a permission problem, so it backs the one resource off and disables nothing.
func TestTransientResourceFailureBacksOffAndRecovers(t *testing.T) {
	ph := baselineWithTwoPRs(t)
	ph.gh.failWith(resCheckSuites, 500, `{"message":"server error"}`)
	ph.activity()

	ph.tick()
	hits := ph.gh.faultHits(resCheckSuites)
	if hits == 0 {
		t.Fatal("the check-suites endpoint was never called")
	}
	if log := ph.healthLog(); len(log) != 0 {
		t.Errorf("a 500 degraded the module: %v", log)
	}
	if !strings.Contains(ph.logText(), "backing that resource off") {
		t.Fatalf("no per-resource backoff was logged; logs:\n%s", ph.logText())
	}

	// Backoff is 30s (the configured interval) doubled once = 60s, so a tick at +30s must
	// skip the resource entirely.
	ph.clock = ph.clock.Add(30 * time.Second)
	ph.tick()
	if got := ph.gh.faultHits(resCheckSuites); got != hits {
		t.Errorf("the backed-off resource was polled inside its window: %d hits, want %d", got, hits)
	}

	// Past the window, with the endpoint healthy again, it recovers by itself.
	ph.gh.clearFault(resCheckSuites)
	ph.clock = ph.clock.Add(31 * time.Second)
	ph.tick()
	var sawCheck bool
	for _, e := range ph.collected() {
		if e.Kind == "check_suite" {
			sawCheck = true
		}
	}
	if !sawCheck {
		t.Error("no check_suite event after the transient failure cleared")
	}
	if log := ph.healthLog(); len(log) != 0 {
		t.Errorf("a transient failure touched module health: %v", log)
	}
}

// TestWorkerIntervalUnchangedWhileAResourceIsDisabled is the user-visible bug itself: the
// worker's cadence must stay at the configured interval no matter how permanently one resource
// is refused. Before the fix these waits were 1m, 2m, 4m, 8m, 15m.
func TestWorkerIntervalUnchangedWhileAResourceIsDisabled(t *testing.T) {
	ph := baselineWithTwoPRs(t)
	ph.gh.failWith(resCheckSuites, 403, deniedBody)
	ph.activity()

	const ticks = 6
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	waits := make(chan time.Duration, 32)
	n := 0
	ph.p.after = func(d time.Duration) <-chan time.Time {
		waits <- d
		n++
		if n >= ticks {
			cancel()
		}
		ch := make(chan time.Time, 1)
		ch <- time.Time{}
		return ch
	}

	done := make(chan struct{})
	ph.p.wg.Add(1)
	go func() { defer close(done); ph.p.runWorker(ctx, ph.project.ID) }()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("the worker did not exit after its context was cancelled")
	}
	close(waits)

	got := 0
	for d := range waits {
		got++
		if d != defaultPollSeconds*time.Second {
			t.Fatalf("worker slept %s on iteration %d; want the configured %ds interval",
				d, got, defaultPollSeconds)
		}
	}
	if got < ticks {
		t.Fatalf("only %d worker iterations ran, want at least %d", got, ticks)
	}
	if strings.Contains(ph.logText(), "tick could not run") {
		t.Errorf("a partly-unreadable tick was counted as a failed tick; logs:\n%s", ph.logText())
	}
}

// ------------------------------------------------------------------ the typed classifier -----

// TestPermissionRefusalIsTypedNotStringMatched pins the adapter's one error boundary: a 403
// that is not a rate limit becomes *ports.ForbiddenError, a 403 that IS one stays
// *ports.RateLimitedError, and a 5xx stays neither. Nothing here reads GitHub's prose.
func TestPermissionRefusalIsTypedNotStringMatched(t *testing.T) {
	t.Run("permission 403", func(t *testing.T) {
		h := newHarness(t)
		h.fixture("GET /repos/acme/payments/issues", 403, deniedBody, nil)

		_, err := h.forge.ListOpenIssues(ctx(), testCreds, testRepo)
		if !errors.Is(err, ports.ErrForbidden) {
			t.Fatalf("err = %v; want ErrForbidden", err)
		}
		var fe *ports.ForbiddenError
		if !errors.As(err, &fe) {
			t.Fatalf("err = %v; want a *ports.ForbiddenError", err)
		}
		if fe.Status != 403 || fe.Resource != "/repos/acme/payments/issues" {
			t.Errorf("typed error = %+v; want the refused path and status", fe)
		}
		if fe.Detail != "Resource not accessible by personal access token" {
			t.Errorf("the forge's own words were dropped: %q", fe.Detail)
		}
		// The underlying go-github error is still reachable, so nothing that inspected it
		// before stops working.
		var er *gh.ErrorResponse
		if !errors.As(err, &er) {
			t.Error("the underlying *gh.ErrorResponse is no longer reachable through errors.As")
		}
		if errors.Is(err, ports.ErrRateLimited) {
			t.Error("a permission refusal was classified as a rate limit")
		}
	})

	t.Run("rate-limit 403 is not a refusal", func(t *testing.T) {
		h := newHarness(t)
		reset := time.Now().Add(2 * time.Hour)
		h.fixture("GET /repos/acme/payments/issues", 403,
			`{"message":"API rate limit exceeded"}`,
			map[string]string{
				"X-RateLimit-Remaining": "0",
				"X-RateLimit-Reset":     fmt.Sprintf("%d", reset.Unix()),
			})

		_, err := h.forge.ListOpenIssues(ctx(), testCreds, testRepo)
		if !errors.Is(err, ports.ErrRateLimited) {
			t.Fatalf("err = %v; want ErrRateLimited", err)
		}
		if errors.Is(err, ports.ErrForbidden) {
			t.Error("an exhausted rate limit was classified as a permanent refusal")
		}
	})

	t.Run("5xx is not a refusal", func(t *testing.T) {
		h := newHarness(t)
		h.fixture("GET /repos/acme/payments/issues", 503, `{"message":"unavailable"}`, nil)

		_, err := h.forge.ListOpenIssues(ctx(), testCreds, testRepo)
		if err == nil {
			t.Fatal("a 503 returned no error")
		}
		if errors.Is(err, ports.ErrForbidden) {
			t.Errorf("a 503 was classified as a permanent refusal: %v", err)
		}
	})
}

// TestModuleHealthKeepsCausesApart guards the one-slot kernel state: the rate limit and a
// denied resource can be outstanding together, and whichever recovers first must not report
// the module ready while the other is still broken.
func TestModuleHealthKeepsCausesApart(t *testing.T) {
	var got []string
	mh := newModuleHealth(func(state kernel.ModuleState, reason string) {
		got = append(got, string(state)+"|"+reason)
	})

	mh.degrade(rateLimitKey, "rate limit spent.")
	mh.degrade(healthKey("proj-1", resCheckSuites), "checks denied.")
	if !mh.degrade(healthKey("proj-1", resIssueComments), "issue comments denied.") {
		t.Error("a second denied resource reported no change")
	}
	if mh.degrade(healthKey("proj-1", resCheckSuites), "checks denied.") {
		t.Error("re-reporting an unchanged cause reported a change; it would log every tick")
	}

	mh.recover(rateLimitKey)
	mh.recover(healthKey("proj-1", resIssueComments))
	last := got[len(got)-1]
	if last != string(kernel.StateDegraded)+"|checks denied." {
		t.Fatalf("state after partial recovery = %q; the surviving cause was lost", last)
	}

	if !mh.recover(healthKey("proj-1", resCheckSuites)) {
		t.Error("clearing the last cause reported no change")
	}
	if last := got[len(got)-1]; last != string(kernel.StateReady)+"|" {
		t.Errorf("state after full recovery = %q, want ready", last)
	}
	if mh.recover(healthKey("proj-1", resCheckSuites)) {
		t.Error("clearing an already-clear cause reported a change")
	}
}

// TestReconnectReEnablesADeniedResource: fixing the token is a reconnect, and a reconnect must
// not need a restart to take effect — it clears the denial rather than waiting out
// deniedRecheck. (Reconnect emits repo.connected, which reaches EnsureWorker; with the worker
// already running, forgetResources is the whole of what it does.)
func TestReconnectReEnablesADeniedResource(t *testing.T) {
	ph := baselineWithTwoPRs(t)
	ph.gh.failWith(resCheckSuites, 403, deniedBody)
	ph.activity()
	ph.tick()

	if ph.p.resourceDue(ph.project.ID, resCheckSuites, ph.clock) {
		t.Fatal("a denied resource is still due to be polled")
	}
	if !strings.HasPrefix(ph.lastHealth(t), string(kernel.StateDegraded)+"|") {
		t.Fatal("the denial did not degrade the module")
	}

	ph.gh.clearFault(resCheckSuites)
	ph.p.forgetResources(ph.project.ID) // what repo.connected triggers

	if !ph.p.resourceDue(ph.project.ID, resCheckSuites, ph.clock) {
		t.Error("the resource is still disabled after a reconnect")
	}
	if got := ph.lastHealth(t); got != string(kernel.StateReady)+"|" {
		t.Errorf("module state after reconnect = %q, want ready", got)
	}

	ph.tick()
	var sawCheck bool
	for _, e := range ph.collected() {
		if e.Kind == "check_suite" {
			sawCheck = true
		}
	}
	if !sawCheck {
		t.Error("no check_suite event on the tick after the reconnect")
	}
}
