package sched_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/spruce/lexicode/internal/domain"
	"github.com/spruce/lexicode/internal/kernel/ports"
	"github.com/spruce/lexicode/internal/kernel/sched"
)

// TestWallClockDoesNotRunWhileParkedOnAHuman is the acceptance for D-12's wall-clock pause.
//
// The pathology it fixes: a run's wall clock bounded two different things at once — how long
// the agent may work, and how long a human has to answer its question. A question asked at
// minute 55 of an hour-long budget got five minutes of human time and then killed the run,
// and a slow answer ate the budget for acting on it. The clock exists to bound agent work,
// and a run parked in needs_input is not working, so the supervisor stops charging it.
//
// The shape here is that pathology in miniature: a two-second budget, a run parked for
// longer than the whole budget, and a session that still finishes normally afterwards.
// Without the pause the same run is timed_out before the answer ever lands —
// TestWallClockTimeoutIsTerminal is the other half, proving the clock still bites while the
// agent is actually working.
func TestWallClockDoesNotRunWhileParkedOnAHuman(t *testing.T) {
	// Five stream lines at 700ms: about 3.5 seconds of "work" against a 2-second budget,
	// with a 2.8-second park in the middle that must not be charged to it.
	const (
		pace      = 700 * time.Millisecond
		wallClock = 2 * time.Second
		parkFor   = 2800 * time.Millisecond
	)

	e := newEnv(t, options{fixture: fixtureOK, pace: pace})
	f := e.seed(1, nil, nil)
	f.agent.MaxWallClockSeconds = int64(wallClock / time.Second)
	if err := e.st.Agents().Update(context.Background(), &f.agent); err != nil {
		t.Fatal(err)
	}
	e.start()

	run, err := e.sch.Enqueue(context.Background(), sched.RunRequest{
		ProjectID: f.project.ID, AgentID: f.agent.ID, Reason: "test",
	})
	if err != nil {
		t.Fatal(err)
	}
	waitFor(t, "run running", func() bool { return e.run(run.ID).State == domain.RunRunning })

	token := readToken(t, e.sb.Instances()[0])
	askDone := make(chan error, 1)
	go func() {
		_, err := callAskHuman(e, token)
		askDone <- err
	}()
	waitFor(t, "needs_input", func() bool { return e.run(run.ID).State == domain.RunNeedsInput })

	// Sit on the question for longer than the entire wall-clock budget.
	parkedAt := time.Now()
	time.Sleep(parkFor)
	if got := e.run(run.ID); got.State != domain.RunNeedsInput {
		t.Fatalf("state after %s parked = %s (%s), want needs_input — the wall clock was "+
			"charged for time the run spent waiting on a person",
			time.Since(parkedAt).Round(time.Millisecond), got.State, got.StateReason)
	}

	pending, err := e.st.Elicitations().PendingForRun(context.Background(), run.ID)
	if err != nil || len(pending) != 1 {
		t.Fatalf("pending elicitations = %v, %v", pending, err)
	}
	if _, err := e.mcp.Resolve(context.Background(), pending[0].ID,
		ports.Response{Answers: map[string][]string{"Which format?": {"JSON"}}}, &e.ownerID); err != nil {
		t.Fatal(err)
	}
	if err := <-askDone; err != nil {
		t.Fatalf("ask_human call failed: %v", err)
	}

	waitFor(t, "run terminal", func() bool { return e.run(run.ID).State.Terminal() })
	final := e.run(run.ID)
	if final.State != domain.RunCompleted {
		t.Fatalf("state = %s (%s), want completed; the parked time was charged to the "+
			"working budget", final.State, final.StateReason)
	}
	if strings.Contains(final.StateReason, "wall clock") {
		t.Fatalf("state reason mentions the wall clock: %q", final.StateReason)
	}
}
