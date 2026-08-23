package claudecode

import (
	"context"
	"errors"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/spruce/lexicode/internal/domain"
	"github.com/spruce/lexicode/internal/kernel/ports"
)

// fakeCLI models the one behaviour of the real Claude Code CLI that every other fake in this
// repository got wrong: under `--input-format stream-json` (contracts §3.1) the process does
// NOT exit when it emits a `result`. A result ends a *turn*. The CLI then blocks reading
// stdin for the next user message, and exits only when stdin reaches EOF.
//
// So this fake holds stdout open after the fixture is written, and closes it — ending the
// process — only when its stdin is closed or a signal arrives. A strings.Reader fixture, the
// shape every pre-existing test used, ends the stream by itself and therefore cannot see the
// adapter failing to close stdin at all.
type fakeCLI struct {
	out *io.PipeWriter

	// holdAfterEOF keeps stdout open after stdin closes, so a test can inspect the window
	// between "the adapter closed stdin" and "the process is gone".
	holdAfterEOF bool

	mu       sync.Mutex
	stdin    strings.Builder
	eof      bool
	signals  []string
	exitCode int

	exit     chan struct{}
	exitOnce sync.Once
}

// newFakeCLI returns the fake and the ports.Streams to Attach over it.
func newFakeCLI() (*fakeCLI, ports.Streams) {
	pr, pw := io.Pipe()
	c := &fakeCLI{out: pw, exit: make(chan struct{})}
	return c, ports.Streams{
		Stdin:  (*fakeStdin)(c),
		Stdout: pr,
		Wait: func() (int, error) {
			<-c.exit
			c.mu.Lock()
			defer c.mu.Unlock()
			return c.exitCode, nil
		},
	}
}

// emit writes one stream-json line to stdout, the way the CLI would.
func (c *fakeCLI) emit(t *testing.T, line string) {
	t.Helper()
	if _, err := io.WriteString(c.out, line); err != nil {
		t.Fatalf("fake CLI emit: %v", err)
	}
}

// kill is the AttachOptions.Kill seam: a signalled process dies at once, exit 143.
func (c *fakeCLI) kill(_ context.Context, signal string) error {
	c.mu.Lock()
	c.signals = append(c.signals, signal)
	c.exitCode = 143
	c.mu.Unlock()
	c.quit()
	return nil
}

// quit ends the process: stdout closes, Wait unblocks.
func (c *fakeCLI) quit() {
	c.exitOnce.Do(func() {
		_ = c.out.Close()
		close(c.exit)
	})
}

func (c *fakeCLI) stdinClosed() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.eof
}

func (c *fakeCLI) stdinText() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.stdin.String()
}

func (c *fakeCLI) killSignals() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]string(nil), c.signals...)
}

// fakeStdin is the CLI's input side. Writing hands the message to the fake's inbox; closing is
// EOF, which is the only thing that makes the real CLI exit on its own.
type fakeStdin fakeCLI

func (s *fakeStdin) Write(p []byte) (int, error) {
	c := (*fakeCLI)(s)
	c.mu.Lock()
	if c.eof {
		c.mu.Unlock()
		return 0, errors.New("fake CLI: write after stdin close")
	}
	c.stdin.Write(p)
	c.mu.Unlock()
	return len(p), nil
}

func (s *fakeStdin) Close() error {
	c := (*fakeCLI)(s)
	c.mu.Lock()
	if c.eof {
		c.mu.Unlock()
		return nil
	}
	c.eof = true
	hold := c.holdAfterEOF
	c.mu.Unlock()
	if !hold {
		c.quit() // stdin EOF: the CLI exits cleanly
	}
	return nil
}

// TestResultWithEmptyQueueClosesStdinAndEndsTheSession is the regression test for the hang:
// a run that finished its work sat in `running` forever because nothing closed the agent's
// stdin, so the CLI never exited, stdout never reached EOF, Wait never returned and the
// scheduler's terminal path — push, pull request, ticket move — never ran.
//
// The session must end promptly after the final `result`, and it must end because stdin was
// closed, not because anything was killed.
func TestResultWithEmptyQueueClosesStdinAndEndsTheSession(t *testing.T) {
	cli, st := newFakeCLI()
	sink := &recordSink{}
	h := Attach(ports.RunSpec{RunID: "run-eof"}, st, sink, AttachOptions{Kill: cli.kill})

	cli.emit(t, initLine)
	cli.emit(t, resultLine)

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	res, err := h.Wait(ctx)
	if err != nil {
		t.Fatalf("Wait did not return after the result (%v): the CLI is still waiting on stdin "+
			"and the run would hang until its wall clock fired", err)
	}
	if !cli.stdinClosed() {
		t.Error("stdin was never closed; nothing would ever make the CLI exit")
	}
	if sigs := cli.killSignals(); len(sigs) != 0 {
		t.Errorf("the session was ended by signals %v, want a clean exit on stdin EOF", sigs)
	}
	if res.ExitCode != 0 || res.IsError || res.Stopped {
		t.Errorf("result = %+v, want a clean, unstopped exit", res)
	}
	if res.ResultText != "done" {
		t.Errorf("ResultText = %q, want %q", res.ResultText, "done")
	}
}

// TestQueuedSteeringAtResultContinuesTheSession is the subtlety that makes the naive fix
// wrong: a `result` arrives after *every* turn, not only the last one. An undelivered
// steering message means the agent has something more to do, so the result must deliver it
// and let the session continue — contracts §3.4's between-tool-calls seam, at the widest gap
// there is. Only the result that finds an empty queue ends the session.
func TestQueuedSteeringAtResultContinuesTheSession(t *testing.T) {
	cli, st := newFakeCLI()
	sink := &recordSink{}
	h := Attach(ports.RunSpec{RunID: "run-steer-result"}, st, sink, AttachOptions{Kill: cli.kill})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// A tool call is in flight, so steering buffers rather than going straight to stdin.
	cli.emit(t, initLine)
	cli.emit(t, toolUseLine("tu1", "Bash", `{"command":"npm test"}`))
	waitUntil(t, "the action activity", func() bool { return sink.emissionCount() >= 2 })
	if err := h.Steer(ctx, "also update the changelog"); err != nil {
		t.Fatalf("Steer: %v", err)
	}

	// The turn ends with the message still queued. It must be delivered here, and the
	// session must stay alive for the agent to act on it.
	cli.emit(t, resultLine)
	waitUntil(t, "the queued steering to be delivered at the turn boundary", func() bool {
		return strings.Contains(cli.stdinText(), "also update the changelog")
	})
	if cli.stdinClosed() {
		t.Fatal("stdin was closed with steering still queued: the agent can never act on it")
	}
	short, shortCancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer shortCancel()
	if _, err := h.Wait(short); err == nil {
		t.Fatal("the session ended at the first result even though steering was queued")
	}

	// The agent works the steering message and ends its second turn with nothing queued.
	cli.emit(t, `{"type":"result","subtype":"success","is_error":false,"num_turns":2,`+
		`"result":"changelog updated","total_cost_usd":0.02,"usage":{"input_tokens":3,"output_tokens":4}}`+"\n")

	res, err := h.Wait(ctx)
	if err != nil {
		t.Fatalf("Wait did not return after the second result: %v", err)
	}
	if !cli.stdinClosed() {
		t.Error("stdin was never closed")
	}
	if sigs := cli.killSignals(); len(sigs) != 0 {
		t.Errorf("ended by signals %v, want a clean exit on stdin EOF", sigs)
	}
	if res.ResultText != "changelog updated" {
		t.Errorf("ResultText = %q, want the second turn's result", res.ResultText)
	}
	if res.IsError || res.Stopped || res.ExitCode != 0 {
		t.Errorf("result = %+v, want a clean, unstopped exit", res)
	}

	// Both turns are in the transcript; nothing was swallowed.
	var responses int
	for _, a := range sink.final() {
		if a.Type == domain.ActivityResponse {
			responses++
		}
	}
	if responses != 2 {
		t.Errorf("response activities = %d, want one per turn", responses)
	}
}

// TestSteeringAfterStdinCloseIsRejected: once the last turn closed stdin there is no longer a
// stream to write to, and a message that cannot be delivered must be refused rather than
// silently dropped. The scheduler leaves such a message queued and says so.
func TestSteeringAfterStdinCloseIsRejected(t *testing.T) {
	cli, st := newFakeCLI()
	cli.holdAfterEOF = true // freeze the window between "stdin closed" and "process gone"
	sink := &recordSink{}
	h := Attach(ports.RunSpec{RunID: "run-late-steer"}, st, sink, AttachOptions{Kill: cli.kill})

	cli.emit(t, initLine)
	cli.emit(t, resultLine)
	waitUntil(t, "the adapter to close stdin", cli.stdinClosed)

	err := h.Steer(context.Background(), "one more thing")
	if err == nil {
		t.Fatal("Steer after the final result = nil, want a refusal")
	}
	if !errors.Is(err, ErrSessionEnded) {
		t.Errorf("Steer error = %v, want it to report the session as ended", err)
	}
	if strings.Contains(cli.stdinText(), "one more thing") {
		t.Error("a message was written to a closed stdin")
	}

	cli.quit()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if _, err := h.Wait(ctx); err != nil {
		t.Fatalf("Wait: %v", err)
	}
}

// TestStopAfterResultDoesNotDoubleClose: Stop already closed stdin, and so does the terminal
// result. The two paths must be idempotent and must not race — a second close of a docker
// exec's half-closed connection is an error at best.
func TestStopAfterResultDoesNotDoubleClose(t *testing.T) {
	cli, st := newFakeCLI()
	cli.holdAfterEOF = true
	sink := &recordSink{}
	h := Attach(ports.RunSpec{RunID: "run-stop-after-result"}, st, sink,
		AttachOptions{Kill: cli.kill, Grace: 50 * time.Millisecond})

	cli.emit(t, initLine)
	cli.emit(t, resultLine)
	waitUntil(t, "the adapter to close stdin", cli.stdinClosed)

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := h.Stop(ctx, "user canceled"); err != nil {
		t.Fatalf("Stop after the final result: %v", err)
	}
	res, err := h.Wait(ctx)
	if err != nil {
		t.Fatalf("Wait: %v", err)
	}
	if !res.Stopped || res.StopReason != "user canceled" {
		t.Errorf("result = %+v, want the stop recorded", res)
	}
}
