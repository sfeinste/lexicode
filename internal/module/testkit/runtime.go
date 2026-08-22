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
			fake.script = Script{Stdout: s.Fixture, Pace: s.Pace, ExitCode: s.ExitCode}
		}
		rt := claudecode.NewRuntime(claudecode.Options{Grace: s.Grace, Respond: s.Respond})
		return rt.Launch(ctx, spec, inst, sink)
	}

	// No instance: replay from an internal paced stream. Stop terminates it like a signal.
	killed := make(chan struct{})
	pr := &pacedReader{lines: splitAfterNewlines(s.Fixture), pace: s.Pace, killed: killed}
	var rec discardWriter
	st := ports.Streams{
		Stdin:  &rec,
		Stdout: pr,
		Stderr: nil,
		Wait: func() (int, error) {
			<-pr.drained()
			select {
			case <-killed:
				return 143, nil
			default:
				return s.ExitCode, nil
			}
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

type discardWriter struct{}

func (discardWriter) Write(p []byte) (int, error) { return len(p), nil }
func (discardWriter) Close() error                { return nil }
