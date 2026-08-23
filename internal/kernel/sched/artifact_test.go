// artifact_test.go covers the §10.5 artifact rule outcome by outcome. The rule has three
// possible endings and the run must report the one that actually happened:
//
//	committed and pushed        → "Partial work pushed to `branch`." + a partial_work output
//	committed, push failed      → says so, names the error, and writes NO partial_work row
//	nothing to commit           → says so, and writes no row either
//
// plus the fourth case that is not an outcome at all: the exec never started, because the
// container was gone by teardown. That one used to vanish — pushArtifact returned false and
// the run's message simply had no branch clause, with the reason nowhere. It is now recorded
// on the run.
//
// The three endings are driven through testkit's SideExec seam, which answers the
// scheduler's preserve exec with the `lexicode:` report a real container's script would
// print. What is under test is the scheduler's reading of that report; the script's own
// behaviour against real git is the docker-tagged credential test's job.
package sched_test

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/spruce/lexicode/internal/domain"
	"github.com/spruce/lexicode/internal/kernel/ports"
	"github.com/spruce/lexicode/internal/kernel/sched"
	"github.com/spruce/lexicode/internal/module/testkit"
)

// preserveReply builds a canned preserve-script report.
func preserveReply(lines ...string) testkit.ExecResult {
	return testkit.ExecResult{Stdout: strings.Join(lines, "\n") + "\n"}
}

// runToFailure enqueues one run that fails, with the given answer to the teardown push, and
// returns the terminal row.
func runToFailure(t *testing.T, side testkit.SideExecFunc) (*env, domain.Run) {
	t.Helper()
	return runWithPreserve(t, fixtureFail, domain.RunFailed, side)
}

// runToCompletion is runToFailure's counterpart for an agent session that SUCCEEDS. The two
// differ only in the transcript the fake runtime replays, but the terminal path does not:
// the teardown push happens for every outcome, and its failures have to surface on a green
// run exactly as loudly as on a red one.
func runToCompletion(t *testing.T, side testkit.SideExecFunc) (*env, domain.Run) {
	t.Helper()
	return runWithPreserve(t, fixtureOK, domain.RunCompleted, side)
}

// runWithPreserve enqueues one run whose teardown push is answered by side, waits for it to
// end in wantState, and returns the terminal row.
func runWithPreserve(t *testing.T, fixture string, wantState domain.RunState, side testkit.SideExecFunc) (*env, domain.Run) {
	t.Helper()
	e := newEnv(t, options{fixture: fixture})
	e.sb.SideExec = func(argv []string, env map[string]string) (testkit.ExecResult, bool) {
		if !testkit.IsTeardownPush(argv) {
			return testkit.ExecResult{}, false
		}
		return side(argv, env)
	}
	f := e.seed(1, nil, nil)
	tk := e.ticket(f, f.backlog, "doomed work")
	e.start()

	run, err := e.sch.Enqueue(context.Background(), sched.RunRequest{
		ProjectID: f.project.ID, AgentID: f.agent.ID, TicketID: tk.ID, Reason: "test",
	})
	if err != nil {
		t.Fatal(err)
	}
	waitFor(t, "run terminal", func() bool { return e.run(run.ID).State.Terminal() })
	final := e.run(run.ID)
	if final.State != wantState {
		t.Fatalf("state = %s (%s), want %s", final.State, final.ErrorMessage, wantState)
	}
	return e, final
}

// pushFailureWarnings returns the level-1 system warnings that say the branch did not reach
// the remote.
func pushFailureWarnings(t *testing.T, e *env, runID string) []domain.Activity {
	t.Helper()
	var out []domain.Activity
	for _, a := range systemActivities(t, e, runID) {
		if a.Level != 1 {
			continue
		}
		if strings.Contains(string(a.Payload), `"warning":"artifact_push_failed"`) {
			out = append(out, a)
		}
	}
	return out
}

func partialWorkOutputs(t *testing.T, e *env, runID string) []domain.RunOutput {
	t.Helper()
	outputs, err := e.st.RunOutputs().ForRun(context.Background(), runID)
	if err != nil {
		t.Fatal(err)
	}
	var out []domain.RunOutput
	for _, o := range outputs {
		if o.Kind == domain.OutputPartialWork {
			out = append(out, o)
		}
	}
	return out
}

func systemActivities(t *testing.T, e *env, runID string) []domain.Activity {
	t.Helper()
	acts, err := e.st.Activities().ForRun(context.Background(), runID)
	if err != nil {
		t.Fatal(err)
	}
	var out []domain.Activity
	for _, a := range acts {
		if a.Type == domain.ActivitySystem {
			out = append(out, a)
		}
	}
	return out
}

// Case 1: committed and pushed. The message names the branch and the output row exists.
func TestArtifactRulePushed(t *testing.T) {
	const sha = "1111111111111111111111111111111111111111"
	e, final := runToFailure(t, func(_ []string, env map[string]string) (testkit.ExecResult, bool) {
		return preserveReply(
			"lexicode: branch "+env["LEXICODE_BRANCH"],
			"lexicode: committed",
			"lexicode: commit "+sha+" "+env["GIT_AUTHOR_EMAIL"],
			"lexicode: trailed "+sha,
			"lexicode: pushed",
		), true
	})

	branch := *final.Branch
	want := "Partial work pushed to `" + branch + "`."
	if !strings.Contains(final.ErrorMessage, want) {
		t.Fatalf("message %q does not report the push (%q)", final.ErrorMessage, want)
	}
	rows := partialWorkOutputs(t, e, final.ID)
	if len(rows) != 1 || rows[0].Ref != branch {
		t.Fatalf("want exactly one partial_work row naming %s; got %+v", branch, rows)
	}
	t.Logf("pushed: %s", final.ErrorMessage)
}

// Case 2: the commit landed, the push did not. This is the one the old code lied about — it
// ran `git push … || true` and reported success regardless. The message must name the
// failure, and there must be NO partial_work row, because nothing reached the remote.
func TestArtifactRulePushFailedIsReportedHonestly(t *testing.T) {
	const sha = "2222222222222222222222222222222222222222"
	e, final := runToFailure(t, func(_ []string, env map[string]string) (testkit.ExecResult, bool) {
		return preserveReply(
			"lexicode: branch "+env["LEXICODE_BRANCH"],
			"lexicode: committed",
			"lexicode: commit "+sha+" "+env["GIT_AUTHOR_EMAIL"],
			"lexicode: trailed "+sha,
			"lexicode: push-failed",
			"lexicode: error remote: Permission to acme/payments.git denied to x-access-token.",
		), true
	})

	branch := *final.Branch
	msg := final.ErrorMessage
	if strings.Contains(msg, "Partial work pushed to") {
		t.Fatalf("a failed push is reported as a successful one: %q", msg)
	}
	for _, want := range []string{
		"committed on `" + branch + "`",
		"could not be pushed",
		"Permission to acme/payments.git denied",
	} {
		if !strings.Contains(msg, want) {
			t.Fatalf("message %q does not carry %q", msg, want)
		}
	}
	if rows := partialWorkOutputs(t, e, final.ID); len(rows) != 0 {
		t.Fatalf("a failed push wrote a partial_work row anyway: %+v", rows)
	}
	warnings := pushFailureWarnings(t, e, final.ID)
	if len(warnings) != 1 {
		t.Fatalf("want exactly one level-1 push-failure warning; got %d", len(warnings))
	}
	if !strings.Contains(string(warnings[0].Payload), "Permission to acme/payments.git denied") {
		t.Fatalf("the warning does not carry git's reason: %s", warnings[0].Payload)
	}
	t.Logf("push failed: %s\nwarning: %s %s", msg, warnings[0].Title, warnings[0].Payload)
}

// Case 5, and the reason this file grew: a run that COMPLETES and whose push does not land.
//
// Every case above ends in RunFailed, where the terminal message is the thing a human reads
// anyway. The green run is the dangerous one. Its message is the agent's own success text,
// the branch is simply absent from the remote, and until this test the ONLY trace of the
// failure was a clause appended to that message — no log line, no activity, nothing a CI log
// or a support transcript would show. That is exactly the shape of the docker smoke test's
// Linux failure: "the run completed, the bare repo has no branch, and the log says nothing."
//
// A push that does not land is the difference between a run whose work exists and a run
// whose work is gone. It must be loud on a green run too.
func TestCompletedRunWithAFailedPushSaysSoLoudly(t *testing.T) {
	const sha = "5555555555555555555555555555555555555555"
	e, final := runToCompletion(t, func(_ []string, env map[string]string) (testkit.ExecResult, bool) {
		return preserveReply(
			"lexicode: branch "+env["LEXICODE_BRANCH"],
			"lexicode: commit "+sha+" "+env["GIT_AUTHOR_EMAIL"],
			"lexicode: trailed "+sha,
			"lexicode: push-failed",
			"lexicode: error fatal: detected dubious ownership in repository at '/fixtures/fixture.git'",
		), true
	})

	msg := final.ErrorMessage
	if !strings.Contains(msg, "could not be pushed") {
		t.Errorf("a completed run's report does not mention the failed push: %q", msg)
	}
	if !strings.Contains(msg, "dubious ownership") {
		t.Errorf("a completed run's report does not name git's reason: %q", msg)
	}
	// The part that would have saved an hour of log archaeology.
	warnings := pushFailureWarnings(t, e, final.ID)
	if len(warnings) != 1 {
		t.Fatalf("a completed run's failed push recorded %d level-1 warnings, want 1 — "+
			"this is the silence the whole file is about", len(warnings))
	}
	if !strings.Contains(string(warnings[0].Payload), "dubious ownership") {
		t.Errorf("the warning does not carry git's reason: %s", warnings[0].Payload)
	}
	if rows := partialWorkOutputs(t, e, final.ID); len(rows) != 0 {
		t.Errorf("a failed push wrote a partial_work row anyway: %+v", rows)
	}
	t.Logf("completed, push failed: %s\nwarning: %s %s",
		msg, warnings[0].Title, warnings[0].Payload)
}

// The counterpart, so the assertion above cannot pass by warning on everything: a push that
// works is silent. A warning that fires on success is a warning people learn to ignore.
func TestCompletedRunWithASuccessfulPushWarnsAboutNothing(t *testing.T) {
	const sha = "6666666666666666666666666666666666666666"
	e, final := runToCompletion(t, func(_ []string, env map[string]string) (testkit.ExecResult, bool) {
		return preserveReply(
			"lexicode: branch "+env["LEXICODE_BRANCH"],
			"lexicode: commit "+sha+" "+env["GIT_AUTHOR_EMAIL"],
			"lexicode: trailed "+sha,
			"lexicode: pushed",
		), true
	})

	if strings.Contains(final.ErrorMessage, "could not be pushed") {
		t.Errorf("a successful push is reported as a failure: %q", final.ErrorMessage)
	}
	if n := len(pushFailureWarnings(t, e, final.ID)); n != 0 {
		t.Errorf("a successful push recorded %d push-failure warning(s)", n)
	}
}

// Case 3: nothing to commit and nothing to push. The run says so rather than claiming a
// branch that has nothing on it.
func TestArtifactRuleNothingToCommit(t *testing.T) {
	e, final := runToFailure(t, func(_ []string, env map[string]string) (testkit.ExecResult, bool) {
		return preserveReply(
			"lexicode: branch "+env["LEXICODE_BRANCH"],
			"lexicode: nothing",
		), true
	})

	msg := final.ErrorMessage
	if strings.Contains(msg, "pushed to") {
		t.Fatalf("an empty workspace is reported as a push: %q", msg)
	}
	if !strings.Contains(msg, "nothing to preserve") {
		t.Fatalf("message %q does not say the workspace was empty", msg)
	}
	if rows := partialWorkOutputs(t, e, final.ID); len(rows) != 0 {
		t.Fatalf("an empty workspace wrote a partial_work row: %+v", rows)
	}
	t.Logf("nothing to commit: %s", msg)
}

// Case 4: the exec never started — the container was gone by teardown. The reason has to end
// up on the run, not in a log line nobody reads.
func TestArtifactRuleUnreachableContainerIsRecorded(t *testing.T) {
	e, final := runToFailure(t, func([]string, map[string]string) (testkit.ExecResult, bool) {
		return testkit.ExecResult{
			Err: errors.New("docker: creating exec: container 9f2a is not running"),
		}, true
	})

	msg := final.ErrorMessage
	if strings.Contains(msg, "Partial work pushed to") {
		t.Fatalf("an exec that never ran is reported as a push: %q", msg)
	}
	if !strings.Contains(msg, "could not be reached") || !strings.Contains(msg, "is not running") {
		t.Fatalf("message %q does not carry the reason the work could not be preserved", msg)
	}
	var warned bool
	for _, a := range systemActivities(t, e, final.ID) {
		if a.Level == 1 && strings.Contains(a.Title, "could not be preserved") {
			warned = true
			t.Logf("warning activity: %s %s", a.Title, string(a.Payload))
		}
	}
	if !warned {
		t.Fatal("no level-1 warning recorded for the unreachable container")
	}
	if rows := partialWorkOutputs(t, e, final.ID); len(rows) != 0 {
		t.Fatalf("an unreachable container wrote a partial_work row: %+v", rows)
	}
}

// The attribution check the orchestrator can make now that it owns the push: a commit
// without this run's D-9 trailer is named in a level-1 warning, and the push still happens —
// the orchestrator records, it does not rewrite the agent's history.
func TestPushWarnsAboutCommitsMissingTheRunTrailer(t *testing.T) {
	const (
		good = "3333333333333333333333333333333333333333"
		bare = "4444444444444444444444444444444444444444"
	)
	e, final := runToFailure(t, func(_ []string, env map[string]string) (testkit.ExecResult, bool) {
		return preserveReply(
			"lexicode: branch "+env["LEXICODE_BRANCH"],
			"lexicode: commit "+good+" "+env["GIT_AUTHOR_EMAIL"],
			"lexicode: commit "+bare+" someone@else.example",
			"lexicode: trailed "+good,
			"lexicode: pushed",
		), true
	})

	if !strings.Contains(final.ErrorMessage, "Partial work pushed to") {
		t.Fatalf("the push was abandoned over an attribution warning: %q", final.ErrorMessage)
	}
	var found *domain.Activity
	for i, a := range systemActivities(t, e, final.ID) {
		if a.Level == 1 && strings.Contains(a.Title, "Lexicode-Run") {
			found = &systemActivities(t, e, final.ID)[i]
		}
	}
	if found == nil {
		t.Fatal("no level-1 warning naming the missing trailer")
	}
	payload := string(found.Payload)
	if !strings.Contains(payload, bare) {
		t.Fatalf("the warning does not name the untrailered commit %s: %s", bare, payload)
	}
	if strings.Contains(found.Title, "2 commit") {
		t.Fatalf("the trailered commit was counted as missing: %q", found.Title)
	}
	t.Logf("attribution warning: %s\n%s", found.Title, payload)
}

// reviewSpecs is the SpecBuilder answer for a run whose subject is a pull request and whose
// agent holds no push_branches grant: the workspace is the pull request's head, and the run
// creates — and therefore owns — no branch. It mirrors what the real S19 builder produces
// (internal/service/runs' workspaceRefs, tested there against the real rows); what is under
// test here is what the SCHEDULER does with an empty branch at teardown.
type reviewSpecs struct{ head string }

func (r reviewSpecs) Build(_ context.Context, in sched.SpecInput) (sched.SpecResult, error) {
	return sched.SpecResult{
		Spec: ports.SandboxSpec{
			RunID: in.Run.ID, ProjectID: in.Project.ID,
			Files: map[string][]byte{".lexicode/prompt.md": []byte(in.Run.Prompt)},
			Clone: ports.CloneSpec{URL: "file:///fixtures/fixture.git", Ref: r.head},
		},
	}, nil
}

// TestReviewRunPushesNothing is the third lock on "a review run must not push to the pull
// request author's branch". The first is the push_branches grant; the second is that such a
// run has no branch at all. This asserts the outcome both of them are for: the teardown push
// exec never runs, runs.branch stays NULL, and no partial_work row claims otherwise.
func TestReviewRunPushesNothing(t *testing.T) {
	const head = "dev/PAY-14-idempotency"
	e := newEnv(t, options{fixture: fixtureOK, specs: reviewSpecs{head: head}})

	var pushes int32
	e.sb.SideExec = func(argv []string, _ map[string]string) (testkit.ExecResult, bool) {
		if testkit.IsTeardownPush(argv) {
			atomic.AddInt32(&pushes, 1)
		}
		return testkit.ExecResult{}, false
	}

	f := e.seed(1, nil, nil)
	// A reviewer: reads and comments, never pushes.
	ctx := context.Background()
	agent := f.agent
	agent.Name = "Reviewer"
	agent.Permissions = domain.AgentPermissions{
		ReadFiles: true, RunCommands: true, SubmitReviews: true,
	}
	if err := e.st.Agents().Update(ctx, &agent); err != nil {
		t.Fatal(err)
	}
	e.start()

	run, err := e.sch.Enqueue(ctx, sched.RunRequest{
		ProjectID: f.project.ID, AgentID: agent.ID,
		Reason: "trigger Agent PR opened → run Reviewer", SubjectKey: "pr:219",
	})
	if err != nil {
		t.Fatal(err)
	}
	waitFor(t, "run terminal", func() bool { return e.run(run.ID).State.Terminal() })
	final := e.run(run.ID)
	if final.State != domain.RunCompleted {
		t.Fatalf("state = %s (%s), want completed", final.State, final.ErrorMessage)
	}

	if n := atomic.LoadInt32(&pushes); n != 0 {
		t.Errorf("the teardown push ran %d time(s) for a review run", n)
	}
	if final.Branch != nil {
		t.Errorf("runs.branch = %q, want NULL: the run owns no branch", *final.Branch)
	}
	if rows := partialWorkOutputs(t, e, final.ID); len(rows) != 0 {
		t.Errorf("partial_work rows for a run that pushed nothing: %+v", rows)
	}
	// And nothing in the run's report may claim a push happened.
	if strings.Contains(final.ErrorMessage, head) {
		t.Errorf("the run's message mentions the PR author's branch: %q", final.ErrorMessage)
	}
}

// TestEmptyBranchAlonePreventsThePush isolates the second lock. With the push_branches grant
// ON but no branch on the run, the teardown push must still do nothing — otherwise the only
// thing standing between a review run and a force-write to someone else's pull request branch
// would be one boolean on the agent row.
func TestEmptyBranchAlonePreventsThePush(t *testing.T) {
	e := newEnv(t, options{fixture: fixtureOK, specs: reviewSpecs{head: "dev/PAY-14"}})

	var pushes int32
	e.sb.SideExec = func(argv []string, _ map[string]string) (testkit.ExecResult, bool) {
		if testkit.IsTeardownPush(argv) {
			atomic.AddInt32(&pushes, 1)
		}
		return testkit.ExecResult{}, false
	}
	f := e.seed(1, nil, nil) // the seeded Dev agent HAS push_branches
	if !f.agent.Permissions.PushBranches {
		t.Fatal("fixture drift: the seeded agent is expected to hold push_branches")
	}
	e.start()

	run, err := e.sch.Enqueue(context.Background(), sched.RunRequest{
		ProjectID: f.project.ID, AgentID: f.agent.ID, Reason: "test", SubjectKey: "pr:219",
	})
	if err != nil {
		t.Fatal(err)
	}
	waitFor(t, "run terminal", func() bool { return e.run(run.ID).State.Terminal() })
	if n := atomic.LoadInt32(&pushes); n != 0 {
		t.Fatalf("the teardown push ran %d time(s) for a run with no branch", n)
	}
}
