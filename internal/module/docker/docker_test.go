//go:build docker

// Real-daemon tests for the Docker sandbox (story S17 acceptance). They need a running Docker
// daemon and network access; run them with:
//
//	go test -tags docker -timeout 30m ./internal/module/docker/
//
// The first run builds the agent base image (Node + claude-code install), which can take
// minutes; later runs hit the content-hash tag cache.
package docker

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/spruce/lexicode/internal/domain"
	"github.com/spruce/lexicode/internal/kernel/ports"
)

// testSink records provisioning progress and mirrors it into the test log.
type testSink struct {
	t     *testing.T
	mu    sync.Mutex
	steps map[string]ports.StepState // last state per step
	logs  []string
}

func newTestSink(t *testing.T) *testSink {
	return &testSink{t: t, steps: map[string]ports.StepState{}}
}

func (s *testSink) Step(name string, state ports.StepState, detail string) {
	s.mu.Lock()
	s.steps[name] = state
	s.mu.Unlock()
	s.t.Logf("step %-14s %-8s %s", name, state, detail)
}

func (s *testSink) Log(line string) {
	s.mu.Lock()
	s.logs = append(s.logs, line)
	s.mu.Unlock()
}

func (s *testSink) state(name string) ports.StepState {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.steps[name]
}

func newTestSandbox(t *testing.T, extraBinds ...string) *Sandbox {
	t.Helper()
	sb, err := NewSandbox("", slog.New(slog.NewTextHandler(os.Stderr, nil)))
	if err != nil {
		t.Fatalf("NewSandbox: %v", err)
	}
	sb.extraBinds = extraBinds
	if err := sb.Available(context.Background()); err != nil {
		t.Fatalf("docker daemon not available: %v", err)
	}
	return sb
}

// fixtureRepo builds a bare git repository on the host with one commit on main containing
// README.md, and returns its path. The tests bind-mount it read-only into the container and
// clone it with a file:// URL — no git daemon, no credentials, fully offline.
func fixtureRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	work := dir + "/work"
	bare := dir + "/fixture.git"

	run := func(cwd string, args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = cwd
		cmd.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME=Fixture", "GIT_AUTHOR_EMAIL=fixture@test",
			"GIT_COMMITTER_NAME=Fixture", "GIT_COMMITTER_EMAIL=fixture@test",
		)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}

	if err := os.MkdirAll(work, 0o755); err != nil {
		t.Fatal(err)
	}
	run(work, "init", "-q", "-b", "main")
	if err := os.WriteFile(work+"/README.md", []byte("hello fixture\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	run(work, "add", "README.md")
	run(work, "commit", "-q", "-m", "initial commit")
	run(dir, "clone", "-q", "--bare", work, bare)
	return bare
}

func destroyQuietly(t *testing.T, inst ports.Instance) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	if err := inst.Destroy(ctx); err != nil {
		t.Errorf("cleanup destroy: %v", err)
	}
}

// execOutput runs argv in the instance and returns (exitCode, combined output).
func execOutput(t *testing.T, inst ports.Instance, argv ...string) (int, string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	return execOutputCtx(ctx, t, inst, argv...)
}

// execOutputCtx is execOutput with the caller's deadline — an apt or npm install needs longer
// than the one-minute default.
func execOutputCtx(ctx context.Context, t *testing.T, inst ports.Instance, argv ...string) (int, string) {
	t.Helper()
	st, err := inst.Exec(ctx, argv, ports.ExecOpts{})
	if err != nil {
		t.Fatalf("Exec %v: %v", argv, err)
	}
	if err := st.Stdin.Close(); err != nil {
		t.Fatalf("closing stdin: %v", err)
	}
	var mu sync.Mutex
	var buf strings.Builder
	var wg sync.WaitGroup
	for _, r := range []io.Reader{st.Stdout, st.Stderr} {
		wg.Add(1)
		go func(r io.Reader) {
			defer wg.Done()
			b, _ := io.ReadAll(r)
			mu.Lock()
			buf.Write(b)
			mu.Unlock()
		}(r)
	}
	wg.Wait()
	code, err := st.Wait()
	if err != nil {
		t.Fatalf("Wait for %v: %v", argv, err)
	}
	return code, buf.String()
}

// The S17 core acceptance chain: build (or reuse) the image, prepare a container with a real
// in-container clone from a bind-mounted fixture, run the setup script, exec echo, read files
// back, then destroy twice (idempotent).
func TestPrepareExecReadFileDestroy(t *testing.T) {
	bare := fixtureRepo(t)
	sb := newTestSandbox(t, bare+":/fixture:ro")
	sink := newTestSink(t)

	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Minute)
	defer cancel()

	spec := ports.SandboxSpec{
		RunID:     "run-" + domain.NewID(),
		ProjectID: "proj-s17",
		Clone: ports.CloneSpec{
			URL:       "file:///fixture",
			Ref:       "main",
			Branch:    "agent/s17-acceptance",
			UserName:  "Agent Smith",
			UserEmail: "agent@lexicode.test",
		},
		SetupScript: "echo setup-ran > setup.txt && echo 'setup output line'",
		Env:         map[string]string{"LEXITEST": "s17-env-value"},
		Files: map[string][]byte{
			".claude/settings.json": []byte(`{"permissions":{}}`),
			".lexicode/prompt.md":   []byte("the prompt"),
		},
		Network: ports.NetworkPolicy{Mode: ports.NetworkOpen},
		Limits: ports.ResourceLimits{
			CPUs:        1,
			MemoryBytes: 512 * 1024 * 1024,
			Pids:        256,
		},
	}

	start := time.Now()
	inst, err := sb.Prepare(ctx, spec, sink)
	if err != nil {
		t.Fatalf("Prepare: %v", err)
	}
	t.Logf("Prepare took %s", time.Since(start).Round(time.Second))

	for _, step := range []string{"image", "container", "clone", "branch", "setup script"} {
		if got := sink.state(step); got != ports.StepOK {
			t.Errorf("step %q ended %q, want ok", step, got)
		}
	}

	ref := inst.Ref()
	if ref.SandboxID != "docker" || ref.InstanceID == "" || ref.RunID != spec.RunID {
		t.Errorf("unexpected ref: %+v", ref)
	}

	// exec echo, with the spec env visible.
	if code, out := execOutput(t, inst, "/bin/sh", "-c", "echo hello-$LEXITEST"); code != 0 || strings.TrimSpace(out) != "hello-s17-env-value" {
		t.Errorf("echo: exit %d, out %q", code, out)
	}

	// The clone, the branch, the identity.
	if code, out := execOutput(t, inst, "git", "rev-parse", "--abbrev-ref", "HEAD"); code != 0 || strings.TrimSpace(out) != "agent/s17-acceptance" {
		t.Errorf("branch: exit %d, out %q", code, out)
	}
	if code, out := execOutput(t, inst, "git", "config", "user.email"); code != 0 || strings.TrimSpace(out) != "agent@lexicode.test" {
		t.Errorf("git identity: exit %d, out %q", code, out)
	}

	// Files: cloned, materialized, and written by the setup script.
	readFile := func(path string) string {
		t.Helper()
		b, err := inst.ReadFile(ctx, path)
		if err != nil {
			t.Fatalf("ReadFile(%s): %v", path, err)
		}
		return string(b)
	}
	if got := readFile("README.md"); got != "hello fixture\n" {
		t.Errorf("README.md = %q", got)
	}
	if got := readFile(".claude/settings.json"); got != `{"permissions":{}}` {
		t.Errorf("settings.json = %q", got)
	}
	if got := strings.TrimSpace(readFile("setup.txt")); got != "setup-ran" {
		t.Errorf("setup.txt = %q", got)
	}

	// The substrate the spec promised. The POC posture (see the "Container posture" block in
	// sandbox.go) is a writable rootfs and uid 0; TestPOCContainerIsUsable proves the
	// consequences, this only pins the two settings so a silent revert is a failing test.
	if code, out := execOutput(t, inst, "/bin/sh", "-c", "touch /usr/local/nope && touch /etc/nope"); code != 0 {
		t.Errorf("rootfs is not writable: exit %d, out %q", code, out)
	}
	if code, _ := execOutput(t, inst, "/bin/sh", "-c", "touch /tmp/ok && touch /workspace/ok"); code != 0 {
		t.Error("/tmp or /workspace not writable")
	}
	if code, out := execOutput(t, inst, "id", "-un"); code != 0 || strings.TrimSpace(out) != "root" {
		t.Errorf("container user: exit %d, out %q (want root)", code, out)
	}

	// Destroy is idempotent.
	if err := inst.Destroy(ctx); err != nil {
		t.Fatalf("Destroy: %v", err)
	}
	if err := inst.Destroy(ctx); err != nil {
		t.Fatalf("second Destroy should be nil, got: %v", err)
	}
	if _, err := sb.Reattach(ctx, ref); !errors.Is(err, ports.ErrInstanceGone) {
		t.Errorf("Reattach after destroy: err = %v, want ErrInstanceGone", err)
	}
}

// Crash recovery: drop the Instance value (the process "died"), Reattach by ref, and the log
// stream resumes from the recorded offset instead of re-emitting everything.
func TestReattachResumesLogStream(t *testing.T) {
	sb := newTestSandbox(t)
	sink := newTestSink(t)
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Minute)
	defer cancel()

	spec := ports.SandboxSpec{
		RunID:     "run-" + domain.NewID(),
		ProjectID: "proj-s17-reattach",
		Network:   ports.NetworkPolicy{Mode: ports.NetworkOpen},
	}
	inst, err := sb.Prepare(ctx, spec, sink)
	if err != nil {
		t.Fatalf("Prepare: %v", err)
	}
	defer destroyQuietly(t, inst)
	concrete := inst.(*Instance)

	// Writes to /proc/1/fd/1 land in the container log stream — the mechanism the runtime
	// adapter uses so its output survives a lexicode restart.
	if code, _ := execOutput(t, inst, "/bin/sh", "-c", "printf 'line-one\\nline-two\\n' > /proc/1/fd/1"); code != 0 {
		t.Fatal("could not write to the container log stream")
	}

	readLogs := func(i *Instance, offset int64) string {
		t.Helper()
		// The log write via /proc/1/fd/1 is asynchronous to the exec's exit; poll briefly.
		deadline := time.Now().Add(10 * time.Second)
		for {
			rc, err := i.Logs(ctx, offset, false)
			if err != nil {
				t.Fatalf("Logs: %v", err)
			}
			b, err := io.ReadAll(rc)
			if cerr := rc.Close(); cerr != nil {
				t.Fatalf("closing logs: %v", cerr)
			}
			if err != nil {
				t.Fatalf("reading logs: %v", err)
			}
			if len(b) > 0 || time.Now().After(deadline) {
				return string(b)
			}
			time.Sleep(200 * time.Millisecond)
		}
	}

	before := readLogs(concrete, 0)
	if before != "line-one\nline-two\n" {
		t.Fatalf("initial log stream = %q", before)
	}

	// The "crash": all we keep is the ref, with the consumed offset recorded — exactly what
	// the scheduler persists in runs.log_offset.
	ref := inst.Ref()
	ref.LogOffset = int64(len(before))

	re, err := sb.Reattach(ctx, ref)
	if err != nil {
		t.Fatalf("Reattach: %v", err)
	}
	if re.Ref().InstanceID != ref.InstanceID {
		t.Errorf("reattached ref = %+v", re.Ref())
	}

	// Exec still works on the reattached instance.
	if code, out := execOutput(t, re, "echo", "back-from-the-dead"); code != 0 || strings.TrimSpace(out) != "back-from-the-dead" {
		t.Errorf("exec after reattach: exit %d, out %q", code, out)
	}

	if code, _ := execOutput(t, re, "/bin/sh", "-c", "printf 'line-three\\n' > /proc/1/fd/1"); code != 0 {
		t.Fatal("could not write to the container log stream after reattach")
	}
	after := readLogs(re.(*Instance), ref.LogOffset)
	if after != "line-three\n" {
		t.Errorf("resumed log stream = %q, want only the new line", after)
	}
}

// A wide-open custom image_ref that lacks claude fails with the named error, and the failed
// Prepare leaves no container behind.
func TestCustomImageMissingTools(t *testing.T) {
	sb := newTestSandbox(t)
	sink := newTestSink(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()

	spec := ports.SandboxSpec{
		RunID:     "run-" + domain.NewID(),
		ProjectID: "proj-s17-badimage",
		Image:     "debian:bookworm-slim", // has git? no. has claude? certainly not.
		Network:   ports.NetworkPolicy{Mode: ports.NetworkOpen},
	}
	_, err := sb.Prepare(ctx, spec, sink)
	if err == nil {
		t.Fatal("Prepare with a toolless image succeeded")
	}
	if !errors.Is(err, ports.ErrImageMissingTools) {
		t.Fatalf("err = %v, want ErrImageMissingTools", err)
	}
	var missing *ports.ImageMissingToolsError
	if !errors.As(err, &missing) {
		t.Fatalf("err %v does not carry ImageMissingToolsError", err)
	}
	found := strings.Join(missing.Missing, " ")
	if !strings.Contains(found, "claude") {
		t.Errorf("missing tools = %v, want claude named", missing.Missing)
	}
	t.Logf("named error: %v", err)
}

// Orphan sweep: a container whose run is terminal (or unknown) is removed; a container whose
// run is alive survives.
func TestOrphanSweep(t *testing.T) {
	sb := newTestSandbox(t)
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Minute)
	defer cancel()

	states := map[string]domain.RunState{
		"run-s17-live": domain.RunRunning,
		"run-s17-dead": domain.RunCompleted,
	}
	sb.runState = func(_ context.Context, runID string) (domain.RunState, bool, error) {
		st, ok := states[runID]
		return st, ok, nil
	}

	prepare := func(runID string) ports.Instance {
		t.Helper()
		inst, err := sb.Prepare(ctx, ports.SandboxSpec{
			RunID:     runID,
			ProjectID: "proj-s17-sweep",
			Network:   ports.NetworkPolicy{Mode: ports.NetworkOpen},
		}, newTestSink(t))
		if err != nil {
			t.Fatalf("Prepare(%s): %v", runID, err)
		}
		return inst
	}

	live := prepare("run-s17-live")
	defer destroyQuietly(t, live)
	dead := prepare("run-s17-dead")

	removed, err := sb.Sweep(ctx)
	if err != nil {
		t.Fatalf("Sweep: %v", err)
	}
	if removed < 1 {
		t.Errorf("Sweep removed %d containers, want at least 1", removed)
	}

	if _, err := sb.Reattach(ctx, dead.Ref()); !errors.Is(err, ports.ErrInstanceGone) {
		t.Errorf("dead run's container survived the sweep: err = %v", err)
	}
	if _, err := sb.Reattach(ctx, live.Ref()); err != nil {
		t.Errorf("live run's container was swept: %v", err)
	}
}

// The none/allowlist plumbing S18 slots into: the container joins the internal network and has
// no default route out.
func TestInternalNetworkHasNoEgress(t *testing.T) {
	sb := newTestSandbox(t)
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Minute)
	defer cancel()

	inst, err := sb.Prepare(ctx, ports.SandboxSpec{
		RunID:     "run-" + domain.NewID(),
		ProjectID: "proj-s17-netnone",
		Network:   ports.NetworkPolicy{Mode: ports.NetworkNone},
	}, newTestSink(t))
	if err != nil {
		t.Fatalf("Prepare: %v", err)
	}
	defer destroyQuietly(t, inst)

	code, out := execOutput(t, inst, "/bin/sh", "-c",
		"curl -sS --max-time 5 https://example.com >/dev/null 2>&1; echo exit=$?")
	if code != 0 {
		t.Fatalf("probe exec failed: %d", code)
	}
	if strings.TrimSpace(out) == "exit=0" {
		t.Error("container under NetworkNone reached the internet; want no egress until S18's proxy")
	}
}
