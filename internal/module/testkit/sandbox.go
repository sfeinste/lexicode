package testkit

import (
	"bytes"
	"context"
	"errors"
	"io"
	"strings"
	"sync"
	"time"

	"github.com/spruce/lexicode/internal/kernel/ports"
)

// Script is what a fake instance's agent exec plays back: a canned stdout stream (normally
// stream-json NDJSON), paced line by line, ending with ExitCode.
type Script struct {
	// Stdout is served line by line as the exec'd process's standard output.
	Stdout []byte
	// Stderr is served alongside, all at once.
	Stderr []byte
	// Pace is the delay before each stdout line; zero streams as fast as the reader pulls.
	Pace time.Duration
	// ExitCode is the process exit after the stream drains. A killed script exits 143
	// (128+SIGTERM), like a real process.
	ExitCode int
}

// Sandbox is the in-memory ports.Sandbox, ID "fake". Every Prepare returns an Instance whose
// agent exec replays the configured Script — enough for the scheduler, trigger and steering
// tests to run the whole engine without Docker.
type Sandbox struct {
	script Script

	mu        sync.Mutex
	instances map[string]*Instance // by InstanceID, for Reattach
	prepared  []*Instance
}

// NewSandbox builds the fake sandbox; every instance it prepares replays script.
func NewSandbox(script Script) *Sandbox {
	return &Sandbox{script: script, instances: map[string]*Instance{}}
}

// ID implements ports.Sandbox.
func (s *Sandbox) ID() string { return "fake" }

// Available implements ports.Sandbox: always.
func (s *Sandbox) Available(context.Context) error { return nil }

// Prepare implements ports.Sandbox: report a minimal checklist and return a live instance.
func (s *Sandbox) Prepare(_ context.Context, spec ports.SandboxSpec, sink ports.ProvisionSink) (ports.Instance, error) {
	for _, step := range []string{"container", "workspace"} {
		sink.Step(step, ports.StepRunning, "")
		sink.Step(step, ports.StepOK, "")
	}
	inst := &Instance{
		ref: ports.InstanceRef{
			SandboxID:  s.ID(),
			InstanceID: "fake-" + spec.RunID,
			RunID:      spec.RunID,
		},
		script: s.script,
		files:  spec.Files,
		killed: make(chan struct{}),
	}
	s.mu.Lock()
	s.instances[inst.ref.InstanceID] = inst
	s.prepared = append(s.prepared, inst)
	s.mu.Unlock()
	return inst, nil
}

// Reattach implements ports.Sandbox: find a previously prepared, undestroyed instance.
func (s *Sandbox) Reattach(_ context.Context, ref ports.InstanceRef) (ports.Instance, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	inst, ok := s.instances[ref.InstanceID]
	if !ok || inst.destroyed {
		return nil, ports.ErrInstanceGone
	}
	inst.ref.LogOffset = ref.LogOffset
	return inst, nil
}

// Instances returns every instance Prepare produced, in order — the test's window into what
// the engine did.
func (s *Sandbox) Instances() []*Instance {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]*Instance(nil), s.prepared...)
}

// Instance is one fake sandbox instance. Its Exec serves the Script for the agent command,
// and understands the claudecode adapter's pidfile kill convention: an argv containing
// "kill -" terminates the playing script, so the adapter's real Stop sequence — TERM, grace,
// KILL — is exercised end to end without a process.
type Instance struct {
	ref    ports.InstanceRef
	script Script
	files  map[string][]byte

	mu        sync.Mutex
	stdin     bytes.Buffer
	killOnce  sync.Once
	killed    chan struct{}
	destroyed bool
}

// Ref implements ports.Instance.
func (i *Instance) Ref() ports.InstanceRef { return i.ref }

// Exec implements ports.Instance.
func (i *Instance) Exec(_ context.Context, argv []string, _ ports.ExecOpts) (ports.Streams, error) {
	i.mu.Lock()
	dead := i.destroyed
	i.mu.Unlock()
	if dead {
		return ports.Streams{}, errors.New("testkit: instance destroyed")
	}

	if isKill(argv) {
		i.Terminate()
		return ports.Streams{
			Stdin:  nopWriteCloser{},
			Stdout: bytes.NewReader(nil),
			Stderr: bytes.NewReader(nil),
			Wait:   func() (int, error) { return 0, nil },
		}, nil
	}

	pr := &pacedReader{lines: splitAfterNewlines(i.script.Stdout), pace: i.script.Pace, killed: i.killed}
	return ports.Streams{
		Stdin:  &recordingWriter{inst: i},
		Stdout: pr,
		Stderr: bytes.NewReader(i.script.Stderr),
		Wait: func() (int, error) {
			// The stream always ends — naturally, or cut short by a kill (the paced
			// reader returns EOF as soon as it sees the signal). A killed script exits
			// 143 (128+SIGTERM), like a real signalled process.
			<-pr.drained()
			select {
			case <-i.killed:
				return 143, nil
			default:
				return i.script.ExitCode, nil
			}
		},
	}, nil
}

// ReadFile implements ports.Instance over the spec's materialised Files.
func (i *Instance) ReadFile(_ context.Context, path string) ([]byte, error) {
	if b, ok := i.files[path]; ok {
		return b, nil
	}
	if b, ok := i.files[strings.TrimPrefix(path, "/workspace/")]; ok {
		return b, nil
	}
	return nil, errors.New("testkit: no such file: " + path)
}

// Destroy implements ports.Instance. Idempotent.
func (i *Instance) Destroy(context.Context) error {
	i.Terminate()
	i.mu.Lock()
	i.destroyed = true
	i.mu.Unlock()
	return nil
}

// Terminate ends the playing script, as a signal would. Safe to call repeatedly.
func (i *Instance) Terminate() {
	i.killOnce.Do(func() { close(i.killed) })
}

// StdinWrites returns everything written to the agent exec's stdin so far — the prompt and
// any steering messages, in delivery order.
func (i *Instance) StdinWrites() string {
	i.mu.Lock()
	defer i.mu.Unlock()
	return i.stdin.String()
}

func isKill(argv []string) bool {
	joined := strings.Join(argv, " ")
	return strings.Contains(joined, "kill -")
}

type recordingWriter struct{ inst *Instance }

func (w *recordingWriter) Write(p []byte) (int, error) {
	w.inst.mu.Lock()
	defer w.inst.mu.Unlock()
	return w.inst.stdin.Write(p)
}

func (w *recordingWriter) Close() error { return nil }

type nopWriteCloser struct{}

func (nopWriteCloser) Write(p []byte) (int, error) { return len(p), nil }
func (nopWriteCloser) Close() error                { return nil }

// pacedReader serves pre-split lines with a delay before each, ending early when killed.
type pacedReader struct {
	lines  [][]byte
	pace   time.Duration
	killed chan struct{}

	mu       sync.Mutex
	buf      bytes.Buffer
	idx      int
	done     chan struct{}
	doneOnce sync.Once
}

func (r *pacedReader) drained() chan struct{} {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.done == nil {
		r.done = make(chan struct{})
		if r.idx >= len(r.lines) && r.buf.Len() == 0 {
			r.doneOnce.Do(func() { close(r.done) })
		}
	}
	return r.done
}

func (r *pacedReader) Read(p []byte) (int, error) {
	for {
		r.mu.Lock()
		if r.buf.Len() > 0 {
			n, _ := r.buf.Read(p)
			r.mu.Unlock()
			return n, nil
		}
		if r.idx >= len(r.lines) {
			r.finishLocked()
			r.mu.Unlock()
			return 0, io.EOF
		}
		next := r.lines[r.idx]
		r.idx++
		r.mu.Unlock()

		if r.pace > 0 {
			select {
			case <-time.After(r.pace):
			case <-r.killed:
				r.mu.Lock()
				r.idx = len(r.lines)
				r.finishLocked()
				r.mu.Unlock()
				return 0, io.EOF
			}
		} else {
			select {
			case <-r.killed:
				r.mu.Lock()
				r.idx = len(r.lines)
				r.finishLocked()
				r.mu.Unlock()
				return 0, io.EOF
			default:
			}
		}

		r.mu.Lock()
		r.buf.Write(next)
		r.mu.Unlock()
	}
}

// finishLocked marks the stream drained. mu held.
func (r *pacedReader) finishLocked() {
	if r.done == nil {
		r.done = make(chan struct{})
	}
	r.doneOnce.Do(func() { close(r.done) })
}

func splitAfterNewlines(b []byte) [][]byte {
	var out [][]byte
	for len(b) > 0 {
		i := bytes.IndexByte(b, '\n')
		if i < 0 {
			out = append(out, b)
			break
		}
		out = append(out, b[:i+1])
		b = b[i+1:]
	}
	return out
}
