// S22 acceptance: the scheduler exercised end to end over the testkit fake sandbox and the
// scripted runtime — no Docker, no network. The environment mirrors cmd/lexicode's wiring:
// real store, real bus, real audit, real MCP server as the token authority, the real
// module/context providers, and a stub spec builder standing in for the S19 Builder (whose
// own behaviour is covered by its S19 tests).
package sched_test

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/spruce/lexicode/internal/domain"
	"github.com/spruce/lexicode/internal/kernel/audit"
	"github.com/spruce/lexicode/internal/kernel/bus"
	"github.com/spruce/lexicode/internal/kernel/ports"
	"github.com/spruce/lexicode/internal/kernel/sched"
	"github.com/spruce/lexicode/internal/kernel/store"
	contextmod "github.com/spruce/lexicode/internal/module/context"
	"github.com/spruce/lexicode/internal/module/testkit"
	mcpsvc "github.com/spruce/lexicode/internal/service/mcp"
	ticketsvc "github.com/spruce/lexicode/internal/service/tickets"
)

// fixture is a minimal, well-formed stream-json session: init, a thought, one Bash action
// with its result, and a result with usage. Cost 5¢.
const fixtureOK = `{"type":"system","subtype":"init","cwd":"/workspace","session_id":"s","tools":["Bash"],"model":"fake"}
{"type":"assistant","message":{"id":"m1","role":"assistant","content":[{"type":"text","text":"looking around"}],"usage":{"input_tokens":10,"output_tokens":4}}}
{"type":"assistant","message":{"id":"m2","role":"assistant","content":[{"type":"tool_use","id":"t1","name":"Bash","input":{"command":"ls"}}],"usage":{"input_tokens":6,"output_tokens":8}}}
{"type":"user","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"t1","content":"README.md"}]}}
{"type":"result","subtype":"success","is_error":false,"num_turns":2,"result":"all done","total_cost_usd":0.05,"usage":{"input_tokens":16,"output_tokens":12}}
`

// fixtureFail ends in an error result: the agent gave up.
const fixtureFail = `{"type":"system","subtype":"init","cwd":"/workspace","session_id":"s","tools":["Bash"],"model":"fake"}
{"type":"assistant","message":{"id":"m1","role":"assistant","content":[{"type":"tool_use","id":"t1","name":"Bash","input":{"command":"make build"}}],"usage":{"input_tokens":5,"output_tokens":5}}}
{"type":"user","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"t1","is_error":true,"content":"make: fatal"}]}}
{"type":"result","subtype":"error_during_execution","is_error":true,"num_turns":1,"result":"could not build","total_cost_usd":0.01,"usage":{"input_tokens":5,"output_tokens":5}}
`

// stubSpecs is the SpecBuilder stand-in: a deterministic branch and the token-bearing
// mcp.json the reattach path reads back. The real S19 Builder is exercised by its own tests
// and by the docker-tagged smoke.
type stubSpecs struct{}

func (stubSpecs) Build(_ context.Context, in sched.SpecInput) (sched.SpecResult, error) {
	branch := fmt.Sprintf("dev/run-%d", in.Run.Seq)
	files := map[string][]byte{
		".lexicode/mcp.json": []byte(`{
  "mcpServers": {
    "lexicode": {
      "type": "http",
      "url": "http://lexicode-egress:3128/mcp/` + in.RunToken + `"
    }
  }
}
`),
		".lexicode/prompt.md": []byte(in.Run.Prompt),
	}
	return sched.SpecResult{
		Spec: ports.SandboxSpec{
			RunID: in.Run.ID, ProjectID: in.Project.ID, Files: files,
			Clone: ports.CloneSpec{URL: "file:///fixtures/fixture.git", Branch: branch},
		},
		Branch:       branch,
		SecretValues: []string{"sk-ant-oat01-test-secret"},
	}, nil
}

type env struct {
	t         *testing.T
	st        *store.Store
	bus       *bus.Bus
	providers []ports.ContextProvider
	sb        *testkit.Sandbox
	rt        *testkit.Scripted
	prs       sched.PROpener
	mcp       *mcpsvc.Server
	mcpSrv    *httptest.Server
	sch       *sched.Scheduler

	ownerID string
}

// options tweaks one env.
type options struct {
	fixture  string
	pace     time.Duration
	exitCode int
	// providers overrides the context-provider set (default: ticket + project). The S34
	// context tests wire all four.
	providers []ports.ContextProvider
	// prs wires the PROpener seam a completed run's pull request goes through. Nil (the
	// default) disables PR opening, as it does in production.
	prs sched.PROpener
}

func newEnv(t *testing.T, o options) *env {
	t.Helper()
	var logger *slog.Logger
	if os.Getenv("SCHED_DEBUG") != "" {
		logger = slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelDebug}))
	} else {
		logger = slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	st, err := store.Open(store.Options{Path: filepath.Join(t.TempDir(), "s22.db"), Logger: logger})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	if _, err := st.Migrate(context.Background()); err != nil {
		t.Fatal(err)
	}
	b := bus.New(bus.Options{Store: st, Logger: logger})
	if err := b.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = b.Stop(context.Background()) })
	auditW := audit.New(audit.Options{Store: st, Logger: logger})

	e := &env{t: t, st: st, bus: b}
	e.seedOwner()

	e.sb = testkit.NewSandbox(testkit.Script{})
	e.rt = &testkit.Scripted{Fixture: []byte(o.fixture), Pace: o.pace, ExitCode: o.exitCode}
	e.providers = o.providers
	e.prs = o.prs

	var schedRef *sched.Scheduler
	e.mcp = mcpsvc.New(mcpsvc.Options{
		Store: st, Bus: b, Audit: auditW, Logger: logger,
		SetRunState: func(ctx context.Context, runID string, state domain.RunState, reason string) error {
			if schedRef == nil {
				return fmt.Errorf("no scheduler")
			}
			return schedRef.SetRunState(ctx, runID, state, reason)
		},
		WaitCeiling: 30 * time.Second,
	})
	e.rt.Respond = func(ctx context.Context, _ string, elicitationID string, r ports.Response) error {
		_, err := e.mcp.Resolve(ctx, elicitationID, r, nil)
		return err
	}

	mcpMux := http.NewServeMux()
	mcpMux.Handle("/mcp/{token}", e.mcp.Handler())
	e.mcpSrv = httptest.NewServer(mcpMux)
	t.Cleanup(e.mcpSrv.Close)

	e.sch = newScheduler(t, e, auditW, logger)
	schedRef = e.sch
	return e
}

// newScheduler builds a scheduler over the env's shared store/sandbox/runtime — called a
// second time by the reattach test to simulate a fresh boot.
func newScheduler(t *testing.T, e *env, auditW *audit.Writer, logger *slog.Logger) *sched.Scheduler {
	t.Helper()
	if auditW == nil {
		auditW = audit.New(audit.Options{Store: e.st, Logger: logger})
	}
	if logger == nil {
		logger = slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	// The real tickets service implements the TicketMover seam — the same wiring
	// cmd/lexicode uses for the board coupling.
	ticketsSvc := ticketsvc.New(ticketsvc.Options{
		Store: e.st, Audit: auditW, Bus: e.bus, Sched: sched.Unscheduled{}, Logger: logger,
	})
	return sched.New(sched.Options{
		Store: e.st, Bus: e.bus, Audit: auditW, Logger: logger,
		Sandbox: func(string) (ports.Sandbox, error) { return e.sb, nil },
		Runtime: func(string) (ports.AgentRuntime, error) { return e.rt, nil },
		Tickets: ticketsSvc,
		PRs:     e.prs,
		Providers: func() []ports.ContextProvider {
			if e.providers != nil {
				return e.providers
			}
			return []ports.ContextProvider{
				contextmod.NewTicketProvider(e.st), // deliberately out of order; the scheduler sorts
				contextmod.NewProjectProvider(e.st),
			}
		},
		Specs:         stubSpecs{},
		Tokens:        e.mcp,
		SandboxID:     "fake",
		AdmitInterval: 25 * time.Millisecond,
	})
}

func (e *env) seedOwner() {
	e.t.Helper()
	now := domain.Now()
	u := domain.User{
		ID: domain.NewID(), Email: "ada@example.com", DisplayName: "Ada",
		PasswordHash: "x", Role: domain.RoleOwner, AvatarColor: "#123456",
		CreatedAt: now,
	}
	if err := e.st.Users().Create(context.Background(), &u); err != nil {
		e.t.Fatal(err)
	}
	e.ownerID = u.ID
}

type fixtures struct {
	project domain.Project
	agent   domain.Agent
	backlog domain.Column
	running domain.Column
	review  domain.Column
}

// seed inserts a project, three columns (backlog, running "In Progress" with the given WIP
// limit, review) and one agent.
func (e *env) seed(agentCap int64, wip *int64, dailyCap *int64) fixtures {
	e.t.Helper()
	ctx := context.Background()
	now := domain.Now()
	p := domain.Project{
		ID: domain.NewID(), Key: "PAY" + domain.NewID()[20:24], Name: "Payments",
		OwnerID: e.ownerID, AgentGuidance: "Prefer small, reviewable changes.",
		CreatedAt: now, UpdatedAt: now,
	}
	if err := e.st.Projects().Create(ctx, &p); err != nil {
		e.t.Fatal(err)
	}
	repo := domain.Repo{
		ProjectID: p.ID, Provider: "github", Owner: "acme", Name: "payments",
		CreatedAt: now, UpdatedAt: now,
	}
	if err := e.st.Repos().Create(ctx, &repo); err != nil {
		e.t.Fatal(err)
	}
	mkCol := func(name string, cat domain.ColumnCategory, pos int64, wip *int64) domain.Column {
		c := domain.Column{
			ID: domain.NewID(), ProjectID: p.ID, Name: name, Category: cat,
			Position: pos, WIPLimit: wip, CreatedAt: now, UpdatedAt: now,
		}
		if err := e.st.Columns().Create(ctx, &c); err != nil {
			e.t.Fatal(err)
		}
		return c
	}
	backlog := mkCol("Backlog", domain.CategoryBacklog, 1, nil)
	running := mkCol("In Progress", domain.CategoryRunning, 2, wip)
	review := mkCol("In Review", domain.CategoryReview, 3, nil)

	a := domain.Agent{
		ID: domain.NewID(), ProjectID: p.ID, Name: "Dev", Role: "developer",
		Color: "#888888", RuntimeID: "scripted", Model: "fake-model", Effort: "medium",
		Autonomy: domain.AutonomyAuto, Permissions: domain.AgentPermissions{ReadFiles: true, EditFiles: true, RunCommands: true},
		GitAuthorName: "Dev", GitAuthorEmail: "dev@agents.lexicode.local",
		ConcurrencyCap: agentCap, DailyCapCents: dailyCap,
		MaxWallClockSeconds: 300, MaxSteps: 100, Enabled: true,
		CreatedAt: now, UpdatedAt: now,
	}
	if err := e.st.Agents().Create(ctx, &a); err != nil {
		e.t.Fatal(err)
	}
	d := domain.AgentDirective{
		ID: domain.NewID(), AgentID: a.ID, Version: 1,
		Body: "You are Dev. Ship small changes.", TokenEstimate: 8, CreatedAt: now,
	}
	if err := e.st.Directives().Create(ctx, &d); err != nil {
		e.t.Fatal(err)
	}
	a.DirectiveVersionID = &d.ID
	if err := e.st.Agents().Update(ctx, &a); err != nil {
		e.t.Fatal(err)
	}
	return fixtures{project: p, agent: a, backlog: backlog, running: running, review: review}
}

// ticket inserts one ticket into col.
func (e *env) ticket(f fixtures, col domain.Column, title string) domain.Ticket {
	e.t.Helper()
	ctx := context.Background()
	now := domain.Now()
	seq, err := e.st.Projects().AllocateTicketSeq(ctx, f.project.ID)
	if err != nil {
		e.t.Fatal(err)
	}
	tk := domain.Ticket{
		ID: domain.NewID(), ProjectID: f.project.ID, Seq: seq,
		Key: fmt.Sprintf("%s-%d", f.project.Key, seq), Title: title,
		Description: "Do the thing carefully.", ColumnID: col.ID,
		Position: float64(seq) * domain.PositionGap, Priority: domain.PriorityNone,
		Origin: domain.OriginHuman, CreatedAt: now, UpdatedAt: now,
	}
	if err := e.st.Tickets().Create(ctx, &tk); err != nil {
		e.t.Fatal(err)
	}
	return tk
}

func (e *env) start() {
	e.t.Helper()
	if err := e.sch.Start(context.Background()); err != nil {
		e.t.Fatal(err)
	}
	e.t.Cleanup(func() { e.sch.Stop(context.Background()) })
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

func (e *env) run(id string) domain.Run {
	e.t.Helper()
	r, err := e.st.Runs().ByID(context.Background(), id)
	if err != nil {
		e.t.Fatal(err)
	}
	return r
}

// ---------------------------------------------------------------- DoD 2: agent cap -----

// Twenty simultaneous enqueues against a 1-cap agent: exactly one runs, nineteen queue with
// the hold reason in words.
func TestConcurrencyCapHoldsUnderSimultaneousEnqueues(t *testing.T) {
	e := newEnv(t, options{fixture: fixtureOK, pace: 40 * time.Millisecond})
	f := e.seed(1, nil, nil)
	e.start()

	var wg sync.WaitGroup
	ids := make([]string, 20)
	errs := make([]error, 20)
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			run, err := e.sch.Enqueue(context.Background(), sched.RunRequest{
				ProjectID: f.project.ID, AgentID: f.agent.ID, Reason: "test",
			})
			ids[i], errs[i] = run.ID, err
		}(i)
	}
	wg.Wait()
	for i, err := range errs {
		if err != nil {
			t.Fatalf("enqueue %d: %v", i, err)
		}
	}

	// Exactly one run leaves queued while the fixture is still playing; the rest hold with
	// the §10.2 wording.
	waitFor(t, "one active + nineteen held", func() bool {
		var active, held int
		for _, id := range ids {
			r := e.run(id)
			switch {
			case r.State == domain.RunProvisioning || r.State == domain.RunRunning:
				active++
			case r.State == domain.RunQueued && r.HoldReason == "waiting: Dev is at its 1-run limit":
				held++
			}
		}
		return active == 1 && held == 19
	})

	// Seq uniqueness under concurrency: twenty distinct per-project numbers.
	seen := map[int64]bool{}
	for _, id := range ids {
		r := e.run(id)
		if seen[r.Seq] {
			t.Fatalf("duplicate run seq %d", r.Seq)
		}
		seen[r.Seq] = true
	}

	// And the cap keeps holding as runs finish: never two active at once, all complete.
	// The check must be one consistent snapshot (a single SQL statement) — twenty separate
	// reads would see run N finish and run N+1 start between them and report a phantom
	// violation.
	waitFor(t, "all runs terminal", func() bool {
		active, err := e.st.Runs().ActiveCount(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		if active > 1 {
			t.Fatalf("agent cap violated: %d active runs", active)
		}
		terminal := 0
		for _, id := range ids {
			if e.run(id).State.Terminal() {
				terminal++
			}
		}
		return terminal == len(ids)
	})
	for _, id := range ids {
		if r := e.run(id); r.State != domain.RunCompleted {
			t.Fatalf("run %s ended %s (%s), want completed", id, r.State, r.ErrorMessage)
		}
	}
}

// ---------------------------------------------------------------- DoD 3: WIP limit -----

func TestWIPLimitQueuesRatherThanExceeds(t *testing.T) {
	e := newEnv(t, options{fixture: fixtureOK, pace: 30 * time.Millisecond})
	wip := int64(4)
	f := e.seed(10, &wip, nil)
	// Fill In Progress to its limit.
	for i := 0; i < 4; i++ {
		e.ticket(f, f.running, fmt.Sprintf("busy %d", i))
	}
	tk := e.ticket(f, f.backlog, "one more thing")
	e.start()

	run, err := e.sch.Enqueue(context.Background(), sched.RunRequest{
		ProjectID: f.project.ID, AgentID: f.agent.ID, TicketID: tk.ID, Reason: "test",
	})
	if err != nil {
		t.Fatal(err)
	}
	waitFor(t, "WIP hold reason", func() bool {
		return e.run(run.ID).HoldReason == "waiting: In Progress is at 4/4"
	})
	if got := e.run(run.ID).State; got != domain.RunQueued {
		t.Fatalf("state = %s, want queued", got)
	}

	// Freeing a slot admits it, and the ticket moves into the running column — but only
	// then. (Move a blocker out by hand; the next admission pass picks the run up.)
	blockers, err := e.st.Tickets().ForColumn(context.Background(), f.running.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := e.st.Tickets().Move(context.Background(), blockers[0].ID, f.review.ID, 1, domain.Now()); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "run admitted after WIP freed", func() bool {
		s := e.run(run.ID).State
		return s != domain.RunQueued
	})
	waitFor(t, "run completed", func() bool { return e.run(run.ID).State.Terminal() })
	if got := e.run(run.ID).State; got != domain.RunCompleted {
		t.Fatalf("state = %s, want completed", got)
	}
}

// ---------------------------------------------------------------- DoD 4: reattach -----

func TestCrashReattachContinuesWithoutDuplicates(t *testing.T) {
	// A long, slow fixture: init + 30 thoughts + result, ~45ms apart.
	var b strings.Builder
	b.WriteString(`{"type":"system","subtype":"init","cwd":"/workspace","session_id":"s","tools":[],"model":"fake"}` + "\n")
	for i := 0; i < 30; i++ {
		fmt.Fprintf(&b, `{"type":"assistant","message":{"id":"m%d","role":"assistant","content":[{"type":"text","text":"thought %02d"}]}}`+"\n", i+10, i)
	}
	b.WriteString(`{"type":"result","subtype":"success","is_error":false,"num_turns":31,"result":"survived the restart","total_cost_usd":0.02,"usage":{"input_tokens":9,"output_tokens":9}}` + "\n")

	e := newEnv(t, options{fixture: b.String(), pace: 45 * time.Millisecond})
	f := e.seed(1, nil, nil)
	e.start()

	run, err := e.sch.Enqueue(context.Background(), sched.RunRequest{
		ProjectID: f.project.ID, AgentID: f.agent.ID, Reason: "test",
	})
	if err != nil {
		t.Fatal(err)
	}
	// Let it get partway: running, several activities persisted, offset recorded.
	waitFor(t, "mid-stream progress", func() bool {
		r := e.run(run.ID)
		if r.State != domain.RunRunning || r.LogOffset == 0 {
			return false
		}
		acts, err := e.st.Activities().ForRun(context.Background(), run.ID)
		if err != nil {
			t.Fatal(err)
		}
		var thoughts int
		for _, a := range acts {
			if a.Type == domain.ActivityThought {
				thoughts++
			}
		}
		return thoughts >= 3
	})

	// CRASH: the orchestrator dies. The scheduler stops (supervisors abandon, containers
	// are NOT destroyed), and the crashed process's exec stream dies with it.
	e.sch.Stop(context.Background())
	inst := e.sb.Instances()[0]
	inst.Terminate()
	time.Sleep(100 * time.Millisecond) // let the orphaned pump drain its last line

	preActs, err := e.st.Activities().ForRun(context.Background(), run.ID)
	if err != nil {
		t.Fatal(err)
	}
	pre := len(preActs)
	if r := e.run(run.ID); r.State != domain.RunRunning {
		t.Fatalf("state after crash = %s, want running (nothing may touch it)", r.State)
	}
	t.Logf("crashed with %d activities persisted, log_offset=%d", pre, e.run(run.ID).LogOffset)

	// BOOT: a new scheduler over the same store and the same (surviving) fake daemon.
	e.sch = newScheduler(t, e, nil, nil)
	if err := e.sch.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { e.sch.Stop(context.Background()) })

	waitFor(t, "resumed run completes", func() bool { return e.run(run.ID).State.Terminal() })
	final := e.run(run.ID)
	if final.State != domain.RunCompleted {
		t.Fatalf("state = %s (%s), want completed", final.State, final.ErrorMessage)
	}

	// The stream continued without duplicates: every thought appears exactly once, seqs are
	// strictly increasing and unique.
	acts, err := e.st.Activities().ForRun(context.Background(), run.ID)
	if err != nil {
		t.Fatal(err)
	}
	titles := map[string]int{}
	seqs := map[int64]bool{}
	var last int64 = -1
	for _, a := range acts {
		if seqs[a.Seq] {
			t.Fatalf("duplicate activity seq %d", a.Seq)
		}
		seqs[a.Seq] = true
		if a.Seq <= last {
			t.Fatalf("activity seqs not increasing: %d after %d", a.Seq, last)
		}
		last = a.Seq
		if a.Type == domain.ActivityThought {
			titles[a.Title]++
		}
	}
	for i := 0; i < 30; i++ {
		want := fmt.Sprintf("thought %02d", i)
		if titles[want] != 1 {
			t.Fatalf("thought %q appears %d times, want exactly 1", want, titles[want])
		}
	}
	if len(acts) <= pre {
		t.Fatalf("no activities were appended after the restart (pre=%d, post=%d)", pre, len(acts))
	}
	t.Logf("resumed: %d activities total, all 30 thoughts exactly once, final state %s", len(acts), final.State)
}

// ---------------------------------------------------------------- DoD 5: failure artifact -----

func TestForcedFailureLeavesPartialWork(t *testing.T) {
	e := newEnv(t, options{fixture: fixtureFail})
	f := e.seed(1, nil, nil)
	tk := e.ticket(f, f.backlog, "doomed work")
	e.start()

	run, err := e.sch.Enqueue(context.Background(), sched.RunRequest{
		ProjectID: f.project.ID, AgentID: f.agent.ID, TicketID: tk.ID, Reason: "test",
	})
	if err != nil {
		t.Fatal(err)
	}
	waitFor(t, "run failed", func() bool { return e.run(run.ID).State.Terminal() })
	final := e.run(run.ID)
	if final.State != domain.RunFailed {
		t.Fatalf("state = %s, want failed", final.State)
	}

	branch := fmt.Sprintf("dev/run-%d", final.Seq)
	wantMsg := fmt.Sprintf("Partial work pushed to `%s`.", branch)
	if !strings.Contains(final.ErrorMessage, wantMsg) {
		t.Fatalf("error message %q does not name the branch (%q)", final.ErrorMessage, wantMsg)
	}
	if !strings.Contains(final.ErrorMessage, "Failed after") {
		t.Fatalf("error message %q lacks the step count line", final.ErrorMessage)
	}

	outputs, err := e.st.RunOutputs().ForRun(context.Background(), run.ID)
	if err != nil {
		t.Fatal(err)
	}
	var partial *domain.RunOutput
	for i := range outputs {
		if outputs[i].Kind == domain.OutputPartialWork {
			partial = &outputs[i]
		}
	}
	if partial == nil || partial.Ref != branch {
		t.Fatalf("no partial_work output naming %s; outputs = %+v", branch, outputs)
	}

	// The fake sandbox recorded the §10.5 push exec.
	var pushed bool
	for _, argv := range e.sb.Instances()[0].Execs() {
		joined := strings.Join(argv, " ")
		if strings.Contains(joined, "git add -A") && strings.Contains(joined, "git push origin") &&
			strings.Contains(joined, branch) {
			pushed = true
			t.Logf("artifact exec: %v", argv)
		}
	}
	if !pushed {
		t.Fatalf("no git commit+push exec recorded; execs = %v", e.sb.Instances()[0].Execs())
	}
	// Teardown destroyed the container and the token is revoked (a second Destroy is
	// idempotent, so just assert the terminal bookkeeping happened).
	if final.EndedAt == nil {
		t.Fatal("ended_at not stamped")
	}
	t.Logf("failure message: %s", final.ErrorMessage)
}

// ---------------------------------------------------------------- DoD 7: happy path -----

func TestHappyPathElicitationUsageAndTicketCoupling(t *testing.T) {
	// The fixture pauses (pace) long enough for the "container" to raise an ask_human over
	// MCP mid-run, exactly as a real run would.
	e := newEnv(t, options{fixture: fixtureOK, pace: 120 * time.Millisecond})
	f := e.seed(1, nil, nil)
	tk := e.ticket(f, f.backlog, "add idempotency keys")
	// An acceptance criterion, so prompt assembly has something to include.
	crit := domain.Criterion{
		ID: domain.NewID(), TicketID: tk.ID, Position: 1,
		Text: "retries do not double-charge", UpdatedAt: domain.Now(),
	}
	if err := e.st.Criteria().Create(context.Background(), &crit); err != nil {
		t.Fatal(err)
	}
	e.start()

	run, err := e.sch.Enqueue(context.Background(), sched.RunRequest{
		ProjectID: f.project.ID, AgentID: f.agent.ID, TicketID: tk.ID,
		Reason: "delegate button", PromptOverride: "Focus on the charge endpoint.",
		RequestedByUserID: e.ownerID,
	})
	if err != nil {
		t.Fatal(err)
	}

	// The rendered prompt is a labelled stack: directive, project guidance, ticket (with
	// criteria), task override — and the context items are recorded.
	for _, want := range []string{
		"# Agent directive", "You are Dev.",
		"# Project guidance", "Prefer small, reviewable changes.",
		"# Ticket " + tk.Key, "Acceptance criteria", "retries do not double-charge",
		"# Task", "Focus on the charge endpoint.",
	} {
		if !strings.Contains(run.Prompt, want) {
			t.Fatalf("prompt lacks %q:\n%s", want, run.Prompt)
		}
	}
	items, err := e.st.RunContextItems().ForRun(context.Background(), run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 2 || items[0].Provider != "project" || items[1].Provider != "ticket" {
		t.Fatalf("context items = %+v, want [project, ticket] in priority order", items)
	}
	if items[1].Reason != "ticket "+tk.Key {
		t.Fatalf("ticket item reason = %q", items[1].Reason)
	}

	// Provisioning happened as a checklist: provision activities exist.
	waitFor(t, "run running", func() bool { return e.run(run.ID).State == domain.RunRunning })
	acts, err := e.st.Activities().ForRun(context.Background(), run.ID)
	if err != nil {
		t.Fatal(err)
	}
	var provision int
	for _, a := range acts {
		if a.Type == domain.ActivityProvision {
			provision++
		}
	}
	if provision == 0 {
		t.Fatal("no provisioning checklist activities")
	}

	// On start, the ticket moved to the running-category column.
	waitFor(t, "ticket in running column", func() bool {
		got, err := e.st.Tickets().ByID(context.Background(), tk.ID)
		if err != nil {
			t.Fatal(err)
		}
		return got.ColumnID == f.running.ID
	})

	// Mid-run, the "container" asks a question over MCP. It parks the run; answering
	// resumes it.
	token := readToken(t, e.sb.Instances()[0])
	askDone := make(chan error, 1)
	go func() {
		_, err := callAskHuman(e, token)
		askDone <- err
	}()
	waitFor(t, "needs_input", func() bool { return e.run(run.ID).State == domain.RunNeedsInput })

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
	waitFor(t, "resumed running", func() bool { return e.run(run.ID).State == domain.RunRunning })

	// The run "opens a PR" (the forge adapter writes run_outputs in production; the same
	// row here) — completion must move the ticket to the review column.
	out := domain.RunOutput{
		ID: domain.NewID(), RunID: run.ID, Kind: domain.OutputPullRequest,
		Ref: "219", URL: "https://github.com/acme/payments/pull/219",
		Summary: "PR #219", CreatedAt: domain.Now(),
	}
	if err := e.st.RunOutputs().Append(context.Background(), &out); err != nil {
		t.Fatal(err)
	}

	waitFor(t, "run completed", func() bool { return e.run(run.ID).State.Terminal() })
	final := e.run(run.ID)
	if final.State != domain.RunCompleted {
		t.Fatalf("state = %s (%s), want completed", final.State, final.ErrorMessage)
	}

	// Usage rolled up: the fixture's result carries total_cost_usd 0.05 → 5 cents, into the
	// run row and the budget ledger.
	if final.CostCents != 5 || final.TokensIn == 0 || final.TokensOut == 0 {
		t.Fatalf("usage rollup = cost %d in %d out %d, want 5/…/…",
			final.CostCents, final.TokensIn, final.TokensOut)
	}
	day := time.Now().UTC().Format("2006-01-02")
	spent, err := e.st.Budget().ProjectDay(context.Background(), day, f.project.ID)
	if err != nil {
		t.Fatal(err)
	}
	if spent != 5 {
		t.Fatalf("budget ledger = %d cents, want 5", spent)
	}

	// The PR output moved the ticket to review.
	waitFor(t, "ticket in review column", func() bool {
		got, err := e.st.Tickets().ByID(context.Background(), tk.ID)
		if err != nil {
			t.Fatal(err)
		}
		return got.ColumnID == f.review.ID
	})

	// The state-transition audit trail, verbatim.
	entries, err := e.st.Audit().List(context.Background(), store.AuditFilter{
		Action: "run.state", TargetKind: "run",
	})
	if err != nil {
		t.Fatal(err)
	}
	var trail []string
	for i := len(entries) - 1; i >= 0; i-- { // List is newest-first
		en := entries[i]
		if en.TargetID != run.ID {
			continue
		}
		trail = append(trail, fmt.Sprintf("%s  %s -> %s", en.CreatedAt,
			jsonField(en.Before, "state"), jsonField(en.After, "state")))
	}
	t.Logf("run.state audit trail for run #%d:\n  %s", final.Seq, strings.Join(trail, "\n  "))
	want := []string{"queued -> provisioning", "provisioning -> running", "running -> needs_input",
		"needs_input -> running", "running -> completed"}
	joined := strings.Join(trail, "\n")
	for _, w := range want {
		if !strings.Contains(joined, w) {
			t.Fatalf("audit trail lacks %q:\n%s", w, joined)
		}
	}
}

// ---------------------------------------------------------------- steering -----

func TestSteeringDeliveredBetweenToolCalls(t *testing.T) {
	e := newEnv(t, options{fixture: fixtureOK, pace: 80 * time.Millisecond})
	f := e.seed(1, nil, nil)
	e.start()

	run, err := e.sch.Enqueue(context.Background(), sched.RunRequest{
		ProjectID: f.project.ID, AgentID: f.agent.ID, Reason: "test",
	})
	if err != nil {
		t.Fatal(err)
	}
	// Steer while it is still queued/provisioning: the queue survives until launch.
	m := domain.RunMessage{
		ID: domain.NewID(), RunID: run.ID, Body: "also update the README",
		State: domain.MessageQueued, CreatedAt: domain.Now(),
	}
	if err := e.st.RunMessages().Create(context.Background(), &m); err != nil {
		t.Fatal(err)
	}
	e.sch.NotifySteering(run.ID)

	waitFor(t, "message delivered", func() bool {
		msgs, err := e.st.RunMessages().ForRun(context.Background(), run.ID)
		if err != nil {
			t.Fatal(err)
		}
		return len(msgs) == 1 && msgs[0].State == domain.MessageDelivered &&
			msgs[0].DeliveredAt != nil
	})
	waitFor(t, "run terminal", func() bool { return e.run(run.ID).State.Terminal() })
	if stdin := e.sb.Instances()[0].StdinWrites(); !strings.Contains(stdin, "also update the README") {
		t.Fatalf("steering message never reached the agent's stdin:\n%s", stdin)
	}
}

// ---------------------------------------------------------------- stop / cancel-ticket -----

func TestStopAndCancelTicketRuns(t *testing.T) {
	e := newEnv(t, options{fixture: fixtureOK, pace: 150 * time.Millisecond})
	f := e.seed(2, nil, nil)
	tk := e.ticket(f, f.backlog, "to be archived")
	e.start()

	run, err := e.sch.Enqueue(context.Background(), sched.RunRequest{
		ProjectID: f.project.ID, AgentID: f.agent.ID, TicketID: tk.ID, Reason: "test",
	})
	if err != nil {
		t.Fatal(err)
	}
	waitFor(t, "running", func() bool { return e.run(run.ID).State == domain.RunRunning })

	n, err := e.sch.CancelTicketRuns(context.Background(), tk.ID, "ticket archived")
	if err != nil || n != 1 {
		t.Fatalf("CancelTicketRuns = %d, %v; want 1", n, err)
	}
	waitFor(t, "canceled", func() bool { return e.run(run.ID).State.Terminal() })
	final := e.run(run.ID)
	if final.State != domain.RunCanceled || final.StateReason != "ticket archived" {
		t.Fatalf("state = %s (%q), want canceled/ticket archived", final.State, final.StateReason)
	}
	// The artifact rule ran for the canceled run too.
	if !strings.Contains(final.ErrorMessage, "Partial work pushed to") {
		t.Fatalf("canceled run left nothing behind: %q", final.ErrorMessage)
	}
}

// ---------------------------------------------------------------- budget -----

func TestBudgetExceededIsTerminal(t *testing.T) {
	e := newEnv(t, options{fixture: fixtureOK})
	cap := int64(10)
	f := e.seed(1, nil, &cap)
	// The agent already spent its daily cap.
	day := time.Now().UTC().Format("2006-01-02")
	if err := e.st.Budget().Add(context.Background(), day, f.project.ID, f.agent.ID, "", 10); err != nil {
		t.Fatal(err)
	}
	e.start()

	run, err := e.sch.Enqueue(context.Background(), sched.RunRequest{
		ProjectID: f.project.ID, AgentID: f.agent.ID, Reason: "test",
	})
	if err != nil {
		t.Fatal(err)
	}
	waitFor(t, "budget-terminal", func() bool { return e.run(run.ID).State.Terminal() })
	final := e.run(run.ID)
	if final.State != domain.RunFailed || final.StateReason != "budget exceeded" {
		t.Fatalf("state = %s (%q), want failed/budget exceeded", final.State, final.StateReason)
	}
	if !strings.Contains(final.ErrorMessage, "Budget exceeded: Dev has spent $0.10 of its $0.10 daily cap.") {
		t.Fatalf("message = %q", final.ErrorMessage)
	}
}

// ---------------------------------------------------------------- transition table -----

func TestTransitionTableRefusesIllegalEdges(t *testing.T) {
	e := newEnv(t, options{fixture: fixtureOK})
	f := e.seed(1, nil, nil)
	e.start()
	run, err := e.sch.Enqueue(context.Background(), sched.RunRequest{
		ProjectID: f.project.ID, AgentID: f.agent.ID, Reason: "test",
	})
	if err != nil {
		t.Fatal(err)
	}
	waitFor(t, "terminal", func() bool { return e.run(run.ID).State.Terminal() })

	// A terminal run cannot be parked by the elicitation seam.
	if err := e.sch.SetRunState(context.Background(), run.ID, domain.RunNeedsInput, "x"); err == nil {
		t.Fatal("parking a completed run should be refused")
	}
	// The seam refuses states it does not own.
	if err := e.sch.SetRunState(context.Background(), run.ID, domain.RunFailed, "x"); err == nil {
		t.Fatal("the elicitation seam must not reach terminal states")
	}
}

// ---------------------------------------------------------------- helpers -----

// readToken extracts the minted run token from the fake container's mcp.json.
func readToken(t *testing.T, inst *testkit.Instance) string {
	t.Helper()
	raw, err := inst.ReadFile(context.Background(), ".lexicode/mcp.json")
	if err != nil {
		t.Fatal(err)
	}
	text := string(raw)
	i := strings.LastIndex(text, "/mcp/")
	if i < 0 {
		t.Fatalf("no token in mcp.json:\n%s", text)
	}
	rest := text[i+5:]
	if j := strings.IndexAny(rest, "\"\n"); j >= 0 {
		rest = rest[:j]
	}
	return rest
}

// callAskHuman drives the MCP tool over HTTP the way the container would.
func callAskHuman(e *env, token string) (string, error) {
	body := `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"ask_human","arguments":{
		"questions":[{"question":"Which format?","header":"Format",
		"options":[{"label":"JSON","description":"application/json"},{"label":"XML","description":"text/xml"}],
		"multiSelect":false}]}}}`
	resp, err := http.Post(e.mcpSrv.URL+"/mcp/"+token, "application/json", strings.NewReader(body))
	if err != nil {
		return "", err
	}
	defer func() { _ = resp.Body.Close() }()
	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("mcp call = HTTP %d: %s", resp.StatusCode, raw)
	}
	return string(raw), nil
}

func jsonField(raw []byte, key string) string {
	s := string(raw)
	i := strings.Index(s, `"`+key+`":"`)
	if i < 0 {
		return "?"
	}
	rest := s[i+len(key)+4:]
	if j := strings.IndexByte(rest, '"'); j >= 0 {
		return rest[:j]
	}
	return "?"
}

// ---------------------------------------------------------------- wall clock / step cap -----

func TestWallClockTimeoutIsTerminal(t *testing.T) {
	// A fixture that would take ~4s against a 1-second wall clock.
	var b strings.Builder
	b.WriteString(`{"type":"system","subtype":"init","cwd":"/workspace","session_id":"s","tools":[],"model":"fake"}` + "\n")
	for i := 0; i < 10; i++ {
		fmt.Fprintf(&b, `{"type":"assistant","message":{"id":"w%d","role":"assistant","content":[{"type":"text","text":"slow step %d"}]}}`+"\n", i, i)
	}
	b.WriteString(`{"type":"result","subtype":"success","is_error":false,"num_turns":1,"result":"never reached","usage":{}}` + "\n")

	e := newEnv(t, options{fixture: b.String(), pace: 400 * time.Millisecond})
	f := e.seed(1, nil, nil)
	// One-second wall clock.
	f.agent.MaxWallClockSeconds = 1
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
	waitFor(t, "timed out", func() bool { return e.run(run.ID).State.Terminal() })
	final := e.run(run.ID)
	if final.State != domain.RunTimedOut {
		t.Fatalf("state = %s (%s), want timed_out", final.State, final.ErrorMessage)
	}
	if !strings.Contains(final.StateReason, "wall clock limit") {
		t.Fatalf("reason = %q", final.StateReason)
	}
	// The artifact rule ran on the way out.
	if !strings.Contains(final.ErrorMessage, "Partial work pushed to") {
		t.Fatalf("timed-out run left nothing behind: %q", final.ErrorMessage)
	}
}

func TestStepCapFailsTheRun(t *testing.T) {
	// Three actions against a 1-step cap.
	var b strings.Builder
	b.WriteString(`{"type":"system","subtype":"init","cwd":"/workspace","session_id":"s","tools":["Bash"],"model":"fake"}` + "\n")
	for i := 0; i < 3; i++ {
		fmt.Fprintf(&b, `{"type":"assistant","message":{"id":"a%d","role":"assistant","content":[{"type":"tool_use","id":"t%d","name":"Bash","input":{"command":"step %d"}}]}}`+"\n", i, i, i)
		fmt.Fprintf(&b, `{"type":"user","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"t%d","content":"ok"}]}}`+"\n", i)
	}
	b.WriteString(`{"type":"result","subtype":"success","is_error":false,"num_turns":3,"result":"done","usage":{}}` + "\n")

	e := newEnv(t, options{fixture: b.String(), pace: 60 * time.Millisecond})
	f := e.seed(1, nil, nil)
	f.agent.MaxSteps = 1
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
	waitFor(t, "step-capped", func() bool { return e.run(run.ID).State.Terminal() })
	final := e.run(run.ID)
	if final.State != domain.RunFailed || !strings.Contains(final.StateReason, "step cap") {
		t.Fatalf("state = %s (%q), want failed/step cap", final.State, final.StateReason)
	}
}

// ---------------------------------------------------------------- LEXI-7: PR outcome -----

// prOpenerFunc is the PROpener seam as a function, so each case below scripts one answer
// where production wires runs.PROpener.
type prOpenerFunc func(ctx context.Context, run domain.Run) (bool, error)

func (f prOpenerFunc) OpenForRun(ctx context.Context, run domain.Run) (bool, error) {
	return f(ctx, run)
}

// TestCompletedRunOutcomeStatesPRFailure is LEXI-7. A run that completed but could not open
// its pull request used to report unqualified success: the reason was recorded as a level-2
// (verbose-only) activity, so at the default verbosity a user saw a green outcome, no pull
// request, and no explanation anywhere on screen. The outcome line now carries the reason and
// the branch that WAS pushed — while the two quiet cases stay quiet: a pull request that
// opened, and a run with nothing to open (the deliberate no-op PROpener answers (false, nil)
// for).
func TestCompletedRunOutcomeStatesPRFailure(t *testing.T) {
	// What runs.PROpener hands the scheduler for the 403 seen live on run #9: the raw API
	// error plus the likely cause.
	forbidden := errors.New("the repository token is not allowed to open pull requests — it " +
		"most likely lacks the `Pull requests: write` permission (`Contents: write` alone is " +
		"not enough); the forge said: github: open pull request for acme/payments: " +
		"POST https://api.github.com/repos/acme/payments/pulls: 403 " +
		"Resource not accessible by personal access token []")

	cases := []struct {
		name   string
		opened bool
		err    error
		// want are the substrings the outcome line must carry beyond the agent's own result
		// text; empty means the outcome must be the result text and nothing more.
		want []string
	}{
		{name: "pull request opened", opened: true},
		{name: "nothing to open", opened: false},
		{
			name: "pull request refused 403",
			err:  forbidden,
			want: []string{
				"The pull request could not be opened",
				"so the work stays on",
				"Pull requests: write",
				"Resource not accessible by personal access token",
			},
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var called atomic.Bool
			e := newEnv(t, options{
				fixture: fixtureOK,
				prs: prOpenerFunc(func(_ context.Context, _ domain.Run) (bool, error) {
					defer called.Store(true)
					return c.opened, c.err
				}),
			})
			f := e.seed(1, nil, nil)
			e.start()

			run, err := e.sch.Enqueue(context.Background(), sched.RunRequest{
				ProjectID: f.project.ID, AgentID: f.agent.ID, Reason: "test",
			})
			if err != nil {
				t.Fatal(err)
			}
			waitFor(t, "run completed", func() bool { return e.run(run.ID).State.Terminal() })
			waitFor(t, "the PR opener ran", called.Load)
			// The outcome-line append lands just after OpenForRun returns; give the negative
			// assertions something to fail against rather than a race they always win.
			time.Sleep(100 * time.Millisecond)

			final := e.run(run.ID)
			if final.State != domain.RunCompleted {
				t.Fatalf("state = %s (%s), want completed", final.State, final.ErrorMessage)
			}
			if final.Branch == nil || *final.Branch == "" {
				t.Fatal("the completed run has no branch; the outcome line has nothing to name")
			}
			if len(c.want) == 0 {
				if final.ErrorMessage != "all done" {
					t.Fatalf("outcome line = %q, want the agent's result text alone (%q)",
						final.ErrorMessage, "all done")
				}
			} else {
				for _, w := range append(c.want, "`"+*final.Branch+"`") {
					if !strings.Contains(final.ErrorMessage, w) {
						t.Errorf("outcome line does not carry %q:\n  %s", w, final.ErrorMessage)
					}
				}
				if !strings.HasPrefix(final.ErrorMessage, "all done.") {
					t.Errorf("outcome line dropped the agent's own result text:\n  %s",
						final.ErrorMessage)
				}
			}
			t.Logf("outcome line: %s", final.ErrorMessage)

			// The transcript says the same thing, at level 0 — the verbosity a user reads by
			// default — and nowhere at all when there was nothing to say.
			acts, err := e.st.Activities().ForRun(context.Background(), run.ID)
			if err != nil {
				t.Fatal(err)
			}
			var errActs []domain.Activity
			for _, a := range acts {
				if a.Type == domain.ActivityError {
					errActs = append(errActs, a)
				}
			}
			if len(c.want) == 0 {
				if len(errActs) != 0 {
					t.Fatalf("a quiet case wrote %d error activities: %+v", len(errActs), errActs)
				}
				return
			}
			if len(errActs) != 1 {
				t.Fatalf("got %d error activities, want exactly one: %+v", len(errActs), errActs)
			}
			a := errActs[0]
			if a.Level != 0 {
				t.Errorf("error activity level = %d, want 0 (level 2 is verbose-only)", a.Level)
			}
			if a.OK == nil || *a.OK {
				t.Errorf("the failure is not marked failed: %+v", a)
			}
			if !strings.Contains(a.Title, "could not be opened") ||
				!strings.Contains(a.Title, *final.Branch) {
				t.Errorf("error activity title = %q, want the reason and the branch", a.Title)
			}
		})
	}
}
