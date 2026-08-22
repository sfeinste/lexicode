package testkit

import (
	"context"
	"sync"
	"time"

	"github.com/spruce/lexicode/internal/kernel/ports"
	"github.com/spruce/lexicode/internal/module/claudecode"
)

// Scripted is the replay ports.AgentRuntime, ID "scripted": Launch plays a fixture
// stream-json session through the real claudecode parser, so every activity, level, payload
// and usage rollup is produced by the same code path as a live run — without a container or
// an API call. The fixture is served with configurable pacing; Stop ends it the way a signal
// would.
type Scripted struct {
	// Fixture is the NDJSON stream-json session to replay.
	Fixture []byte
	// Pace is the delay before each fixture line; zero replays as fast as possible.
	Pace time.Duration
	// ExitCode is the fake process exit after the fixture drains.
	ExitCode int
	// Respond is handed through to the claudecode session (the S21 seam).
	Respond claudecode.RespondFunc
	// Grace is the Stop grace period; zero means the adapter default. Tests that exercise
	// the TERM→KILL escalation set it low.
	Grace time.Duration
}

// ID implements ports.AgentRuntime.
func (s *Scripted) ID() string { return "scripted" }

// Caps implements ports.AgentRuntime: mirror the real runtime so nothing degrades in tests.
func (s *Scripted) Caps() ports.Caps {
	return ports.Caps{Steering: true, Elicitation: true, Approvals: true, CostReporting: true}
}

// Launch implements ports.AgentRuntime. When inst is one of this package's fake instances
// (or anything else exec-able), the fixture is served through inst.Exec so stdin writes are
// observable on the instance; a nil inst replays the fixture from an internal stream.
func (s *Scripted) Launch(ctx context.Context, spec ports.RunSpec, inst ports.Instance, sink ports.RunSink) (ports.Handle, error) {
	if inst != nil {
		// Serve through the instance: reuse the real adapter's whole Launch path, overriding
		// only the script that plays. Fake instances ignore argv (except kills), so the
		// claudecode command line runs against this fixture.
		if fake, ok := inst.(*Instance); ok {
			fake.mu.Lock()
			fake.script = Script{Stdout: s.Fixture, Pace: s.Pace, ExitCode: s.ExitCode}
			fake.mu.Unlock()
		}
		rt := claudecode.NewRuntime(claudecode.Options{Grace: s.Grace, Respond: s.Respond})
		return rt.Launch(ctx, spec, inst, sink)
	}

	// No instance: replay from an internal paced stream. Like the real CLI it stays open once
	// the fixture runs out and exits when the adapter closes stdin; Stop terminates it like a
	// signal.
	killed := make(chan struct{})
	stdinEOF := make(chan struct{})
	pr := newPacedReader(splitAfterNewlines(s.Fixture), s.Pace, killed, stdinEOF)
	st := ports.Streams{
		Stdin:  &eofSignalWriter{eof: stdinEOF},
		Stdout: pr,
		Stderr: nil,
		Wait: func() (int, error) {
			<-pr.drained()
			if pr.wasKilled() {
				return 143, nil
			}
			return s.ExitCode, nil
		},
	}
	var killOnce sync.Once
	kill := func(context.Context, string) error {
		killOnce.Do(func() { close(killed) })
		return nil
	}
	return claudecode.Attach(spec, st, sink, claudecode.AttachOptions{
		Kill:    kill,
		Grace:   s.Grace,
		Respond: s.Respond,
	}), nil
}

// eofSignalWriter discards what the adapter writes but honours the one thing that matters:
// closing it is the stdin EOF that lets the scripted process exit.
type eofSignalWriter struct {
	eof  chan struct{}
	once sync.Once
}

func (w *eofSignalWriter) Write(p []byte) (int, error) { return len(p), nil }

func (w *eofSignalWriter) Close() error {
	w.once.Do(func() { close(w.eof) })
	return nil
}
