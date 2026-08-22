package claudecode_test

import (
	"context"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/spruce/lexicode/internal/domain"
	"github.com/spruce/lexicode/internal/kernel/ports"
	"github.com/spruce/lexicode/internal/module/claudecode"
	"github.com/spruce/lexicode/internal/module/testkit"
)

// countingSink is the minimal RunSink for the external tests.
type countingSink struct {
	mu         sync.Mutex
	activities []domain.Activity
}

func (s *countingSink) Activity(a domain.Activity) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.activities = append(s.activities, a)
}
func (s *countingSink) CurrentStep(string)              {}
func (s *countingSink) Usage(domain.UsageDelta)         {}
func (s *countingSink) Elicit(domain.Elicitation) error { return nil }
func (s *countingSink) Output(domain.RunOutput)         {}
func (s *countingSink) Offset(int64)                    {}

func (s *countingSink) count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.activities)
}

// nullProvisionSink satisfies ports.ProvisionSink.
type nullProvisionSink struct{}

func (nullProvisionSink) Step(string, ports.StepState, string) {}
func (nullProvisionSink) Log(string)                           {}

// TestStopMidStream is S20 acceptance: Stop during a live stream terminates the session —
// Wait returns a Stopped result — and no pump goroutine leaks. The full real Stop path runs:
// the adapter closes stdin, execs the pidfile SIGTERM (which the fake instance honours by
// ending the stream, as a signalled process would), and Wait collects exit 143.
//
// Leak detection is a manual check rather than goleak (not a dependency of this repo): the
// goroutine count is sampled before Launch and polled after Wait until it returns to the
// baseline, with a small allowance for runtime noise.
func TestStopMidStream(t *testing.T) {
	baseline := runtime.NumGoroutine()

	// A long, slow script: ~200 thoughts at 10ms each would run two seconds if not stopped.
	var b strings.Builder
	for range 200 {
		b.WriteString(`{"type":"assistant","message":{"id":"m","role":"assistant","content":[{"type":"text","text":"still thinking"}]}}` + "\n")
	}
	sb := testkit.NewSandbox(testkit.Script{Stdout: []byte(b.String()), Pace: 10 * time.Millisecond})

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	inst, err := sb.Prepare(ctx, ports.SandboxSpec{RunID: "run-stop"}, nullProvisionSink{})
	if err != nil {
		t.Fatalf("Prepare: %v", err)
	}

	sink := &countingSink{}
	rt := claudecode.NewRuntime(claudecode.Options{Grace: 250 * time.Millisecond})
	h, err := rt.Launch(ctx, ports.RunSpec{RunID: "run-stop", Prompt: "go", Model: "m"}, inst, sink)
	if err != nil {
		t.Fatalf("Launch: %v", err)
	}

	// Let the stream visibly flow first.
	deadline := time.Now().Add(2 * time.Second)
	for sink.count() < 3 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if sink.count() < 3 {
		t.Fatal("stream never started flowing")
	}

	if err := h.Stop(ctx, "user canceled"); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	res, err := h.Wait(ctx)
	if err != nil {
		t.Fatalf("Wait after Stop: %v", err)
	}
	if !res.Stopped || res.StopReason != "user canceled" {
		t.Errorf("result = %+v, want Stopped with the given reason", res)
	}
	if res.ExitCode != 143 {
		t.Errorf("exit code = %d, want 143 (SIGTERM)", res.ExitCode)
	}
	// The prompt reached stdin before the stop.
	if got := inst.(*testkit.Instance).StdinWrites(); !strings.Contains(got, `"text":"go"`) {
		t.Errorf("prompt not delivered on stdin: %q", got)
	}

	// A second Stop is idempotent.
	if err := h.Stop(ctx, "again"); err != nil {
		t.Fatalf("second Stop: %v", err)
	}

	// Goroutine leak check: poll until the count returns to the pre-Launch baseline
	// (plus a small allowance for unrelated runtime churn).
	leakDeadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(leakDeadline) {
		if runtime.NumGoroutine() <= baseline+2 {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	buf := make([]byte, 64*1024)
	n := runtime.Stack(buf, true)
	t.Fatalf("goroutines did not return to baseline (%d, now %d):\n%s",
		baseline, runtime.NumGoroutine(), buf[:n])
}
