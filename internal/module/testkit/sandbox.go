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

// ExecResult is what a scripted side exec answers with.
type ExecResult struct {
	Stdout   string
	ExitCode int
	// Err makes Exec itself fail — the "container is gone at teardown" case.
	Err error
}

// SideExecFunc overrides the canned answer for one non-agent exec (the §10.5 teardown push, a
// probe). Returning ok=false falls back to the default. It is how a test drives the artifact
// rule's three outcomes — pushed, push failed, nothing to commit — without Docker.
type SideExecFunc func(argv []string, env map[string]string) (ExecResult, bool)

// Sandbox is the in-memory ports.Sandbox, ID "fake". Every Prepare returns an Instance whose
// agent exec replays the configured Script — enough for the scheduler, trigger and steering
// tests to run the whole engine without Docker.
type Sandbox struct {
	script Script
	// SideExec scripts the answers to non-agent execs; nil means the defaults.
	SideExec SideExecFunc

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
		script:   s.script,
		sideExec: s.SideExec,
		files:    spec.Files,
		killed:   make(chan struct{}),
	}
	s.mu.Lock()
	s.instances[inst.ref.InstanceID] = inst
	s.prepared = append(s.prepared, inst)
	s.mu.Unlock()
	return inst, nil
}

// Reattach implements ports.Sandbox: find a previously prepared, undestroyed instance. A
// reattach begins a new exec life — a kill delivered to the previous process (the crashed
// orchestrator's exec) must not poison the resumed stream, so a closed kill channel is
// replaced.
func (s *Sandbox) Reattach(_ context.Context, ref ports.InstanceRef) (ports.Instance, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	inst, ok := s.instances[ref.InstanceID]
	if !ok || inst.destroyed {
		return nil, ports.ErrInstanceGone
	}
	inst.mu.Lock()
	inst.ref.LogOffset = ref.LogOffset
	select {
	case <-inst.killed:
		inst.killed = make(chan struct{})
	default:
	}
	inst.mu.Unlock()
	return inst, nil
}

// Instances returns every instance Prepare produced, in order — the test's window into what
// the engine did.
func (s *Sandbox) Instances() []*Instance {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]*Instance(nil), s.prepared...)
}

// Instance is one fake sandbox instance. Its Exec serves the Script for the agent command
// (an argv carrying the §3.1 stream-json launch shape), skipping ref.LogOffset bytes the way
// a real sandbox serves its log stream from an offset on reattach. It understands the
// claudecode adapter's pidfile kill convention: an argv containing "kill -" terminates the
// playing script, so the adapter's real Stop sequence — TERM, grace, KILL — is exercised end
// to end without a process. Any other argv (git artifact pushes, probes) is recorded and
// succeeds with empty output; Execs() is the test's window into what ran.
type Instance struct {
	ref      ports.InstanceRef
	script   Script
	sideExec SideExecFunc
	files    map[string][]byte

	mu        sync.Mutex
	stdin     bytes.Buffer
	execs     [][]string
	killed    chan struct{}
	destroyed bool
}

// Ref implements ports.Instance.
func (i *Instance) Ref() ports.InstanceRef { return i.ref }

// Exec implements ports.Instance.
func (i *Instance) Exec(_ context.Context, argv []string, opts ports.ExecOpts) (ports.Streams, error) {
	i.mu.Lock()
	dead := i.destroyed
	killed := i.killed
	logOffset := i.ref.LogOffset
	script := i.script
	sideExec := i.sideExec
	i.execs = append(i.execs, append([]string(nil), argv...))
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

	if !isAgentLaunch(argv) {
		// A side exec (the §10.5 teardown push, a probe): recorded above. The test's
		// SideExec answers it if it wants to; otherwise the defaults apply. Only the agent
		// launch replays the script.
		res, ok := ExecResult{}, false
		if sideExec != nil {
			res, ok = sideExec(argv, opts.Env)
		}
		if !ok {
			res = defaultSideExec(argv, opts.Env)
		}
		if res.Err != nil {
			return ports.Streams{}, res.Err
		}
		code := res.ExitCode
		return ports.Streams{
			Stdin:  nopWriteCloser{},
			Stdout: strings.NewReader(res.Stdout),
			Stderr: bytes.NewReader(nil),
			Wait:   func() (int, error) { return code, nil },
		}, nil
	}

	// The agent launch: serve the script from ref.LogOffset — the fake's version of "the
	// sandbox serves its log stream from where the last process stopped" (§10.6).
	stdout := script.Stdout
	if off := logOffset; off > 0 {
		if off >= int64(len(stdout)) {
			stdout = nil
		} else {
			stdout = stdout[off:]
		}
	}
	// stdinEOF joins this exec's stdin to its stdout: the scripted process holds stdout open
	// after the fixture runs out and ends only when its stdin closes, which is what the real
	// CLI does under --input-format stream-json.
	stdinEOF := make(chan struct{})
	pr := newPacedReader(splitAfterNewlines(stdout), script.Pace, killed, stdinEOF)
	return ports.Streams{
		Stdin:  &recordingWriter{inst: i, eof: stdinEOF},
		Stdout: pr,
		Stderr: bytes.NewReader(script.Stderr),
		Wait: func() (int, error) {
			// The stream ends when the adapter closes stdin, or is cut short by a kill.
			// A killed script exits 143 (128+SIGTERM), like a real signalled process;
			// one that ran out of input exits with the script's code.
			<-pr.drained()
			if pr.wasKilled() {
				return 143, nil
			}
			return script.ExitCode, nil
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
	i.mu.Lock()
	defer i.mu.Unlock()
	select {
	case <-i.killed:
	default:
		close(i.killed)
	}
}

// StdinWrites returns everything written to the agent exec's stdin so far — the prompt and
// any steering messages, in delivery order.
func (i *Instance) StdinWrites() string {
	i.mu.Lock()
	defer i.mu.Unlock()
	return i.stdin.String()
}

// defaultSideExec answers a side exec the test did not script. Everything succeeds silently
// except the teardown push, which reports the ordinary happy path — an uncommitted tree
// committed and pushed, with the run trailer on it — because that is what a real container
// with work in it does, and the scheduler's terminal message is rendered from these lines.
func defaultSideExec(argv []string, env map[string]string) ExecResult {
	if !isTeardownPush(argv) {
		return ExecResult{}
	}
	branch := env["LEXICODE_BRANCH"]
	const sha = "0f1e2d3c4b5a69788796a5b4c3d2e1f000000000"
	return ExecResult{Stdout: strings.Join([]string{
		"lexicode: branch " + branch,
		"lexicode: committed",
		"lexicode: commit " + sha + " " + env["GIT_AUTHOR_EMAIL"],
		"lexicode: trailed " + sha,
		"lexicode: pushed",
	}, "\n") + "\n"}
}

// IsTeardownPush reports whether an argv is the scheduler's §10.5 preserve-and-push exec —
// the hook a test's SideExec keys on.
func IsTeardownPush(argv []string) bool { return isTeardownPush(argv) }

func isTeardownPush(argv []string) bool {
	return strings.Contains(strings.Join(argv, " "), "git push origin")
}

func isKill(argv []string) bool {
	joined := strings.Join(argv, " ")
	return strings.Contains(joined, "kill -")
}

// isAgentLaunch recognises the contracts §3.1 launch shape (any runtime passing
// `--output-format stream-json` counts). Everything else is a side exec.
func isAgentLaunch(argv []string) bool {
	joined := strings.Join(argv, " ")
	return strings.Contains(joined, "stream-json")
}

// Execs returns every argv this instance has executed, in order — the fake's audit trail
// (the S22 artifact-push assertion reads it).
func (i *Instance) Execs() [][]string {
	i.mu.Lock()
	defer i.mu.Unlock()
	out := make([][]string, len(i.execs))
	copy(out, i.execs)
	return out
}

// recordingWriter is one exec's stdin: it records what the orchestrator writes, and its Close
// is the EOF that lets the scripted process exit.
type recordingWriter struct {
	inst *Instance
	eof  chan struct{}
	once sync.Once
}

func (w *recordingWriter) Write(p []byte) (int, error) {
	w.inst.mu.Lock()
	defer w.inst.mu.Unlock()
	return w.inst.stdin.Write(p)
}

func (w *recordingWriter) Close() error {
	w.once.Do(func() { close(w.eof) })
	return nil
}

type nopWriteCloser struct{}

func (nopWriteCloser) Write(p []byte) (int, error) { return len(p), nil }
func (nopWriteCloser) Close() error                { return nil }

// pacedReader serves pre-split lines with a delay before each.
//
// When the lines run out it does NOT end the stream. The real Claude Code CLI, launched with
// `--input-format stream-json` (contracts §3.1), emits its `result` and then blocks reading
// stdin for the next user message: a result ends a turn, and only EOF on stdin ends the
// process. So the reader parks until the orchestrator closes stdin — or until a kill, which
// ends it wherever it is.
//
// This is the dimension the old fake had backwards. It served the fixture and returned EOF by
// itself, so every test passed over an adapter that never closed stdin at all, which is
// exactly the hang a real run then produced.
type pacedReader struct {
	lines  [][]byte
	pace   time.Duration
	killed chan struct{}
	stdin  chan struct{} // closed when the process's stdin reaches EOF

	mu       sync.Mutex
	buf      bytes.Buffer
	idx      int
	killEnd  bool // the stream ended on a signal rather than on stdin EOF
	done     chan struct{}
	doneOnce sync.Once
}

func newPacedReader(lines [][]byte, pace time.Duration, killed, stdin chan struct{}) *pacedReader {
	if stdin == nil {
		stdin = make(chan struct{}) // never closed: only a kill can end this stream
	}
	return &pacedReader{
		lines: lines, pace: pace, killed: killed, stdin: stdin,
		done: make(chan struct{}),
	}
}

// drained is closed once the stream has ended, however it ended.
func (r *pacedReader) drained() chan struct{} { return r.done }

// wasKilled reports whether a signal, rather than stdin EOF, ended the stream.
func (r *pacedReader) wasKilled() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.killEnd
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
			r.mu.Unlock()
			// Out of fixture, but not out of process: wait for stdin to close.
			select {
			case <-r.stdin:
				r.end(false)
			case <-r.killed:
				r.end(true)
			}
			return 0, io.EOF
		}
		next := r.lines[r.idx]
		r.idx++
		r.mu.Unlock()

		if r.pace > 0 {
			select {
			case <-time.After(r.pace):
			case <-r.killed:
				r.end(true)
				return 0, io.EOF
			}
		} else {
			select {
			case <-r.killed:
				r.end(true)
				return 0, io.EOF
			default:
			}
		}

		r.mu.Lock()
		r.buf.Write(next)
		r.mu.Unlock()
	}
}

// end marks the stream finished, recording whether a signal did it.
func (r *pacedReader) end(killed bool) {
	r.mu.Lock()
	r.idx = len(r.lines)
	if killed {
		r.killEnd = true
	}
	r.mu.Unlock()
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
