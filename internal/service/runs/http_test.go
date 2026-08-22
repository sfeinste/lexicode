// S22/S24 HTTP-level acceptance: the run endpoints over a REAL scheduler — a queued run's
// GET shows the specific hold reason (§10.2: the UI says which limit, by name — never a
// bare spinner), steering queues, stop cancels with artifacts, delegate enqueues, takeover
// stops with reason `takeover` and injects the note into the next run on the ticket
// (§10.7), the needs-you view computes the §4.3 flavor in words, and acknowledge dismisses
// a terminal failure from the needs-you surfaces.
package runs_test

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"encoding/json"

	"github.com/spruce/lexicode/internal/domain"
	"github.com/spruce/lexicode/internal/kernel/audit"
	"github.com/spruce/lexicode/internal/kernel/auth"
	"github.com/spruce/lexicode/internal/kernel/bus"
	"github.com/spruce/lexicode/internal/kernel/httpx"
	"github.com/spruce/lexicode/internal/kernel/ports"
	"github.com/spruce/lexicode/internal/kernel/sched"
	"github.com/spruce/lexicode/internal/kernel/store"
	contextmod "github.com/spruce/lexicode/internal/module/context"
	"github.com/spruce/lexicode/internal/module/testkit"
	runsvc "github.com/spruce/lexicode/internal/service/runs"
	ticketsvc "github.com/spruce/lexicode/internal/service/tickets"
)

// fixtureSlow is a session that stays alive long enough to be observed running.
const fixtureSlow = `{"type":"system","subtype":"init","cwd":"/workspace","session_id":"s","tools":[],"model":"fake"}
{"type":"assistant","message":{"id":"m1","role":"assistant","content":[{"type":"text","text":"working"}]}}
{"type":"assistant","message":{"id":"m2","role":"assistant","content":[{"type":"text","text":"still working"}]}}
{"type":"result","subtype":"success","is_error":false,"num_turns":2,"result":"done","total_cost_usd":0.01,"usage":{"input_tokens":2,"output_tokens":2}}
`

type stubSpecs struct{}

func (stubSpecs) Build(_ context.Context, in sched.SpecInput) (sched.SpecResult, error) {
	branch := fmt.Sprintf("dev/run-%d", in.Run.Seq)
	return sched.SpecResult{
		Spec:   ports.SandboxSpec{RunID: in.Run.ID, ProjectID: in.Project.ID},
		Branch: branch,
	}, nil
}

type env struct {
	t      *testing.T
	st     *store.Store
	sch    *sched.Scheduler
	srv    *httptest.Server
	owner  *http.Client
	userID string

	project domain.Project
	agent   domain.Agent
	backlog domain.Column
	ticket  domain.Ticket
}

func newEnv(t *testing.T, pace time.Duration) *env {
	t.Helper()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	st, err := store.Open(store.Options{Path: filepath.Join(t.TempDir(), "s22http.db"), Logger: logger})
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
	mux := httpx.NewMux(httpx.Options{Logger: logger})
	authSvc := auth.New(auth.Options{Store: st, Logger: logger})
	authSvc.Routes(mux)

	sb := testkit.NewSandbox(testkit.Script{})
	rt := &testkit.Scripted{Fixture: []byte(fixtureSlow), Pace: pace}

	var schedRef *sched.Scheduler
	ticketsSvc := ticketsvc.New(ticketsvc.Options{
		Store: st, Audit: auditW, Bus: b,
		Sched: lateReq{&schedRef}, Logger: logger,
	})
	ticketsSvc.Routes(mux, authSvc)

	scheduler := sched.New(sched.Options{
		Store: st, Bus: b, Audit: auditW, Logger: logger,
		Sandbox: func(string) (ports.Sandbox, error) { return sb, nil },
		Runtime: func(string) (ports.AgentRuntime, error) { return rt, nil },
		Providers: func() []ports.ContextProvider {
			return []ports.ContextProvider{
				contextmod.NewProjectProvider(st), contextmod.NewTicketProvider(st),
			}
		},
		Specs: stubSpecs{}, Tickets: ticketsSvc,
		SandboxID: "fake", AdmitInterval: 25 * time.Millisecond,
	})
	schedRef = scheduler
	if err := scheduler.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { scheduler.Stop(context.Background()) })

	runsSvc := runsvc.New(runsvc.Options{Store: st, Audit: auditW, Sched: scheduler, Logger: logger})
	runsSvc.Routes(mux, authSvc)

	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	e := &env{t: t, st: st, sch: scheduler, srv: srv}
	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatal(err)
	}
	e.owner = &http.Client{Jar: jar}
	status, body := e.doJSON("POST", "/api/v1/auth/setup",
		`{"email":"ada@example.com","display_name":"Ada","password":"correct horse"}`)
	if status != http.StatusCreated {
		t.Fatalf("setup = %d: %v", status, body)
	}
	e.userID = body["id"].(string)
	e.seed()
	return e
}

// lateReq forwards to the scheduler constructed after the tickets service (the cmd wiring's
// pattern).
type lateReq struct{ s **sched.Scheduler }

func (l lateReq) RequestRun(ctx context.Context, req sched.RunRequest) (string, error) {
	if *l.s == nil {
		return "", sched.ErrNotImplemented
	}
	return (*l.s).RequestRun(ctx, req)
}

func (l lateReq) CancelTicketRuns(ctx context.Context, ticketID, reason string) (int64, error) {
	if *l.s == nil {
		return 0, sched.ErrNotImplemented
	}
	return (*l.s).CancelTicketRuns(ctx, ticketID, reason)
}

func (e *env) seed() {
	e.t.Helper()
	ctx := context.Background()
	now := domain.Now()
	e.project = domain.Project{
		ID: domain.NewID(), Key: "PAY", Name: "Payments", OwnerID: e.userID,
		CreatedAt: now, UpdatedAt: now,
	}
	if err := e.st.Projects().Create(ctx, &e.project); err != nil {
		e.t.Fatal(err)
	}
	repo := domain.Repo{ProjectID: e.project.ID, Provider: "github", Owner: "acme",
		Name: "payments", CreatedAt: now, UpdatedAt: now}
	if err := e.st.Repos().Create(ctx, &repo); err != nil {
		e.t.Fatal(err)
	}
	for i, c := range []struct {
		name string
		cat  domain.ColumnCategory
	}{{"Backlog", domain.CategoryBacklog}, {"In Progress", domain.CategoryRunning},
		{"In Review", domain.CategoryReview}} {
		col := domain.Column{ID: domain.NewID(), ProjectID: e.project.ID, Name: c.name,
			Category: c.cat, Position: int64(i + 1), CreatedAt: now, UpdatedAt: now}
		if err := e.st.Columns().Create(ctx, &col); err != nil {
			e.t.Fatal(err)
		}
		if c.cat == domain.CategoryBacklog {
			e.backlog = col
		}
	}
	e.agent = domain.Agent{
		ID: domain.NewID(), ProjectID: e.project.ID, Name: "Dev", Color: "#888888",
		RuntimeID: "scripted", Model: "fake-model", Effort: "medium",
		Autonomy: domain.AutonomyAuto, Permissions: domain.AgentPermissions{ReadFiles: true},
		GitAuthorName: "Dev", GitAuthorEmail: "dev@agents.lexicode.local",
		ConcurrencyCap: 1, MaxWallClockSeconds: 300, MaxSteps: 100, Enabled: true,
		CreatedAt: now, UpdatedAt: now,
	}
	if err := e.st.Agents().Create(ctx, &e.agent); err != nil {
		e.t.Fatal(err)
	}
	seq, err := e.st.Projects().AllocateTicketSeq(ctx, e.project.ID)
	if err != nil {
		e.t.Fatal(err)
	}
	e.ticket = domain.Ticket{
		ID: domain.NewID(), ProjectID: e.project.ID, Seq: seq,
		Key: fmt.Sprintf("PAY-%d", seq), Title: "Add idempotency keys",
		ColumnID: e.backlog.ID, Position: domain.PositionGap, Priority: domain.PriorityNone,
		Origin: domain.OriginHuman, CreatedAt: now, UpdatedAt: now,
	}
	if err := e.st.Tickets().Create(ctx, &e.ticket); err != nil {
		e.t.Fatal(err)
	}
}

func (e *env) doJSON(method, path, body string) (int, map[string]any) {
	e.t.Helper()
	var rd io.Reader
	if body != "" {
		rd = strings.NewReader(body)
	}
	req, err := http.NewRequestWithContext(context.Background(), method, e.srv.URL+path, rd)
	if err != nil {
		e.t.Fatal(err)
	}
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := e.owner.Do(req)
	if err != nil {
		e.t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	raw, _ := io.ReadAll(resp.Body)
	var v map[string]any
	if len(strings.TrimSpace(string(raw))) > 0 {
		if err := json.Unmarshal(raw, &v); err != nil {
			e.t.Fatalf("%s %s: not JSON: %v\n%s", method, path, err, raw)
		}
	}
	return resp.StatusCode, v
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

// DoD 6: delegate over HTTP, then the second (queued) run's GET carries the specific hold
// reason in words.
func TestDelegateAndQueuedRunShowsHoldReason(t *testing.T) {
	e := newEnv(t, 60*time.Millisecond)

	// First delegate: admitted, occupies Dev's single slot.
	status, body := e.doJSON("POST", "/api/v1/tickets/"+e.ticket.ID+"/delegate",
		`{"agent_id":"`+e.agent.ID+`"}`)
	if status != http.StatusCreated {
		t.Fatalf("delegate = %d: %v", status, body)
	}
	first := body["run_id"].(string)
	waitFor(t, "first run active", func() bool {
		r, err := e.st.Runs().ByID(context.Background(), first)
		return err == nil && (r.State == domain.RunProvisioning || r.State == domain.RunRunning)
	})

	// Second delegate: queued behind the 1-run limit.
	status, body = e.doJSON("POST", "/api/v1/tickets/"+e.ticket.ID+"/delegate",
		`{"agent_id":"`+e.agent.ID+`","prompt":"second attempt"}`)
	if status != http.StatusCreated {
		t.Fatalf("delegate = %d: %v", status, body)
	}
	second := body["run_id"].(string)

	waitFor(t, "hold reason on GET", func() bool {
		status, body := e.doJSON("GET", "/api/v1/runs/"+second, "")
		if status != http.StatusOK {
			t.Fatalf("GET run = %d: %v", status, body)
		}
		run := body["run"].(map[string]any)
		return run["state"] == "queued" &&
			run["hold_reason"] == "waiting: Dev is at its 1-run limit"
	})
	t.Logf("GET /runs/%s → state=queued hold_reason=%q", second,
		"waiting: Dev is at its 1-run limit")

	// The list endpoint filters by state.
	status, body = e.doJSON("GET", "/api/v1/projects/PAY/runs?status=queued", "")
	if status != http.StatusOK {
		t.Fatalf("list = %d: %v", status, body)
	}
	runs := body["runs"].([]any)
	if len(runs) != 1 || runs[0].(map[string]any)["id"] != second {
		t.Fatalf("queued list = %v, want just the held run", runs)
	}

	// Steering the queued run is allowed (§10.3: the composer works before launch).
	status, body = e.doJSON("POST", "/api/v1/runs/"+second+"/messages",
		`{"body":"remember the retries"}`)
	if status != http.StatusCreated {
		t.Fatalf("steer = %d: %v", status, body)
	}

	// Stop the queued run: straight to canceled, no container ever existed.
	status, body = e.doJSON("POST", "/api/v1/runs/"+second+"/stop", `{"reason":"changed my mind"}`)
	if status != http.StatusOK {
		t.Fatalf("stop = %d: %v", status, body)
	}
	run := body["run"].(map[string]any)
	if run["state"] != "canceled" || run["state_reason"] != "changed my mind" {
		t.Fatalf("stopped run = %v", run)
	}

	// Activities endpoint serves the transcript.
	waitFor(t, "first run terminal", func() bool {
		r, err := e.st.Runs().ByID(context.Background(), first)
		return err == nil && r.State.Terminal()
	})
	status, body = e.doJSON("GET", "/api/v1/runs/"+first+"/activities", "")
	if status != http.StatusOK || len(body["activities"].([]any)) == 0 {
		t.Fatalf("activities = %d: %v", status, body)
	}
}

// S24: take over a live run — canceled with reason `takeover`, the artifact push recorded,
// the checkout block returned, and the note injected into the next run on the same ticket
// (§10.7).
func TestTakeoverStoresNoteAndInjectsIntoNextRun(t *testing.T) {
	e := newEnv(t, 400*time.Millisecond) // slow enough to be taken over mid-session

	status, body := e.doJSON("POST", "/api/v1/tickets/"+e.ticket.ID+"/delegate",
		`{"agent_id":"`+e.agent.ID+`"}`)
	if status != http.StatusCreated {
		t.Fatalf("delegate = %d: %v", status, body)
	}
	first := body["run_id"].(string)
	waitFor(t, "first run running", func() bool {
		r, err := e.st.Runs().ByID(context.Background(), first)
		return err == nil && r.State == domain.RunRunning
	})

	status, body = e.doJSON("POST", "/api/v1/runs/"+first+"/takeover",
		`{"note":"I renamed the retry helper and fixed the config loader myself."}`)
	if status != http.StatusOK {
		t.Fatalf("takeover = %d: %v", status, body)
	}
	if got := body["checkout"]; got != "git fetch origin && git checkout dev/run-1" {
		t.Fatalf("checkout block = %v", got)
	}

	waitFor(t, "taken-over run terminal", func() bool {
		r, err := e.st.Runs().ByID(context.Background(), first)
		return err == nil && r.State.Terminal()
	})
	after, err := e.st.Runs().ByID(context.Background(), first)
	if err != nil {
		t.Fatal(err)
	}
	if after.State != domain.RunCanceled || after.StateReason != "takeover" {
		t.Fatalf("run after takeover = %s (%s)", after.State, after.StateReason)
	}
	if !strings.Contains(after.TakeoverNote, "renamed the retry helper") {
		t.Fatalf("takeover note not stored: %q", after.TakeoverNote)
	}

	// The §10.5 artifact push ran: partial work is recorded against the run branch.
	status, body = e.doJSON("GET", "/api/v1/runs/"+first, "")
	if status != http.StatusOK {
		t.Fatalf("GET run = %d: %v", status, body)
	}
	var partial bool
	for _, o := range body["outputs"].([]any) {
		if o.(map[string]any)["kind"] == "partial_work" {
			partial = true
		}
	}
	if !partial {
		t.Fatalf("no partial_work output after takeover: %v", body["outputs"])
	}

	// The next run on the same ticket reads the note in its prompt — the prompt is the
	// run's first stdin message (contracts §3.1).
	status, body = e.doJSON("POST", "/api/v1/tickets/"+e.ticket.ID+"/delegate",
		`{"agent_id":"`+e.agent.ID+`"}`)
	if status != http.StatusCreated {
		t.Fatalf("second delegate = %d: %v", status, body)
	}
	second := body["run_id"].(string)
	next, err := e.st.Runs().ByID(context.Background(), second)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(next.Prompt, "# Human takeover") ||
		!strings.Contains(next.Prompt, "renamed the retry helper") {
		t.Fatalf("takeover note not injected into the next run's prompt:\n%s", next.Prompt)
	}
	if !strings.Contains(next.Prompt, "took over run #1") {
		t.Fatalf("takeover section does not name the taken-over run:\n%s", next.Prompt)
	}
}

// S24: the needs-you view computes the §4.3 flavor in words — question first, then
// approval, then failure — and acknowledge dismisses a terminal failure from it.
func TestNeedsYouViewAndAcknowledge(t *testing.T) {
	e := newEnv(t, time.Millisecond)
	ctx := context.Background()
	now := domain.Now()

	mkRun := func(seq int64, state domain.RunState) string {
		run := domain.Run{
			ID: domain.NewID(), Seq: seq, ProjectID: e.project.ID, AgentID: e.agent.ID,
			State: state, Autonomy: domain.AutonomyAuto, Model: "fake-model",
			Effort: "medium", Prompt: "p", RuntimeID: "scripted", SandboxID: "fake",
			SubjectKey: "ticket:" + e.ticket.Key, QueuedAt: now,
		}
		tid := e.ticket.ID
		run.TicketID = &tid
		if err := e.st.Runs().Create(ctx, &run); err != nil {
			t.Fatal(err)
		}
		return run.ID
	}
	failed := mkRun(101, domain.RunFailed)
	question := mkRun(102, domain.RunNeedsInput)
	approval := mkRun(103, domain.RunAwaitingApproval)
	live := mkRun(104, domain.RunRunning) // never a needs-you row

	status, body := e.doJSON("GET", "/api/v1/projects/PAY/runs?view=needs_you", "")
	if status != http.StatusOK {
		t.Fatalf("needs_you = %d: %v", status, body)
	}
	rows := body["runs"].([]any)
	var got []string
	for _, r := range rows {
		row := r.(map[string]any)
		got = append(got, fmt.Sprintf("%s:%s", row["id"], row["flavor"]))
		if row["ticket_key"] != e.ticket.Key || row["agent"] != "Dev" ||
			row["project_key"] != "PAY" {
			t.Fatalf("row joins wrong: %v", row)
		}
	}
	want := []string{question + ":question", approval + ":approval", failed + ":failure"}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("needs_you rows = %v, want %v", got, want)
	}

	// /inbox renders the same query workspace-wide.
	status, body = e.doJSON("GET", "/api/v1/inbox", "")
	if status != http.StatusOK || len(body["runs"].([]any)) != 3 {
		t.Fatalf("inbox = %d: %v", status, body)
	}

	// A live run cannot be acknowledged.
	status, _ = e.doJSON("POST", "/api/v1/runs/"+live+"/acknowledge", "")
	if status != http.StatusConflict {
		t.Fatalf("acknowledge live = %d, want 409", status)
	}

	// Acknowledging the failure dismisses it from the surfaces.
	status, body = e.doJSON("POST", "/api/v1/runs/"+failed+"/acknowledge", "")
	if status != http.StatusOK {
		t.Fatalf("acknowledge = %d: %v", status, body)
	}
	if body["run"].(map[string]any)["acknowledged_at"] == nil {
		t.Fatalf("acknowledged_at not set: %v", body["run"])
	}
	status, body = e.doJSON("GET", "/api/v1/projects/PAY/runs?view=needs_you", "")
	if status != http.StatusOK || len(body["runs"].([]any)) != 2 {
		t.Fatalf("needs_you after acknowledge = %d: %v", status, body)
	}
}
