// S27 acceptance: the loop guard exercised through the real pipeline — real store, real bus,
// real engine, real guard.Layers, and a REAL scheduler as the run creator and LoopStopper. A
// stand-in run_agent action does exactly what S28's will (call Scheduler.Enqueue with the
// guard's pass-through copied onto the RunRequest, D-14) so the stage-3 → stage-4 → scheduler
// plumbing is the one under test. The scheduler is deliberately never Started: enqueued runs
// stay `queued` (live, non-terminal), which makes debounce, cancel-in-progress and the depth
// walk deterministic without pacing a fake runtime.
package triggers

import (
	"context"
	"encoding/json"
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

	"github.com/spruce/lexicode/internal/domain"
	"github.com/spruce/lexicode/internal/kernel/audit"
	"github.com/spruce/lexicode/internal/kernel/auth"
	"github.com/spruce/lexicode/internal/kernel/bus"
	"github.com/spruce/lexicode/internal/kernel/guard"
	"github.com/spruce/lexicode/internal/kernel/httpx"
	"github.com/spruce/lexicode/internal/kernel/ports"
	"github.com/spruce/lexicode/internal/kernel/sched"
	"github.com/spruce/lexicode/internal/kernel/store"
	runsvc "github.com/spruce/lexicode/internal/service/runs"
)

// guardSource is the catalog the engine derives subject keys from.
type guardSource struct{}

func (guardSource) ID() string { return "github.poll" }
func (guardSource) Catalog() ports.EventCatalog {
	return ports.EventCatalog{Events: []ports.EventDescriptor{
		{Kind: "pull_request", SubjectKey: "pr:{{pr.number}}"},
		{Kind: "pull_request_review", SubjectKey: "pr:{{pr.number}}"},
		{Kind: "issue_comment", SubjectKey: "pr:{{pr.number}}"},
	}}
}
func (guardSource) Start(context.Context, ports.Emit) error { return nil }
func (guardSource) Stop(context.Context) error              { return nil }

// testRunAgent is the S28 run_agent's shape, reduced to its contract: Enqueue and nothing
// else, with ActionContext.Guard copied verbatim onto the RunRequest.
type testRunAgent struct{ sch *sched.Scheduler }

func (testRunAgent) ID() string                               { return "run_agent" }
func (testRunAgent) Label() string                            { return "Run an agent" }
func (testRunAgent) Schema() ports.ParamSchema                { return ports.ParamSchema{} }
func (testRunAgent) Describe(json.RawMessage) (string, error) { return "run an agent", nil }

func (a testRunAgent) Execute(ctx context.Context, ac ports.ActionContext, params json.RawMessage) (ports.ActionResult, error) {
	var p struct {
		AgentID string `json:"agent_id"`
	}
	_ = json.Unmarshal(params, &p)
	run, err := a.sch.Enqueue(ctx, sched.RunRequest{
		ProjectID:       ac.Project.ID,
		AgentID:         p.AgentID,
		Reason:          "trigger " + ac.Trigger.Name,
		TriggerID:       ac.Trigger.ID,
		CauseEventID:    ac.Event.ID,
		SubjectKey:      ac.Guard.SubjectKey,
		Depth:           ac.Guard.Depth,
		SupersededRunID: ac.Guard.SupersededRunID,
	})
	if err != nil {
		return ports.ActionResult{}, err
	}
	return ports.ActionResult{Outcome: domain.FiringSucceeded, RunID: run.ID,
		Note: fmt.Sprintf("enqueued run #%d", run.Seq)}, nil
}

type guardEnv struct {
	t      *testing.T
	ctx    context.Context
	st     *store.Store
	bus    *bus.Bus
	engine *Engine
	sch    *sched.Scheduler
	srv    *httptest.Server
	client *http.Client

	userID string
	proj   domain.Project

	// clock orders event occurred_at deterministically: real events never share a
	// millisecond timestamp the way a tight test loop does, and the depth walk's
	// human-reset comparison is on occurred_at.
	clock int
}

func newGuardEnv(t *testing.T) *guardEnv {
	t.Helper()
	ctx := context.Background()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	st, err := store.Open(store.Options{Path: filepath.Join(t.TempDir(), "s27.db"), Logger: logger})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	if _, err := st.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	b := bus.New(bus.Options{Store: st, Logger: logger})
	auditW := audit.New(audit.Options{Store: st, Logger: logger})

	// The scheduler is the run creator and the guard's LoopStopper. Never Started.
	scheduler := sched.New(sched.Options{
		Store: st, Bus: b, Audit: auditW, Logger: logger, SandboxID: "fake",
	})
	loopGuard := guard.New(guard.Options{Store: st, Loop: scheduler, Logger: logger})

	e := &guardEnv{t: t, ctx: ctx, st: st, bus: b, sch: scheduler}
	e.engine = NewEngine(EngineOptions{
		Store: st, Bus: b, Logger: logger,
		Guard:   loopGuard,
		Sources: func() []ports.EventSource { return []ports.EventSource{guardSource{}} },
		Action: func(id string) (ports.TriggerAction, error) {
			if id == "run_agent" {
				return testRunAgent{sch: scheduler}, nil
			}
			return nil, fmt.Errorf("no action %q", id)
		},
	})
	if err := e.engine.Subscribe(b); err != nil {
		t.Fatal(err)
	}
	if err := b.Start(ctx); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		stopCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = b.Stop(stopCtx)
		_ = e.engine.Stop(stopCtx)
	})

	// The runs HTTP surface, for GET /runs/{id}/chain.
	mux := httpx.NewMux(httpx.Options{Logger: logger})
	authSvc := auth.New(auth.Options{Store: st, Logger: logger})
	authSvc.Routes(mux)
	runsSvc := runsvc.New(runsvc.Options{Store: st, Audit: auditW, Sched: scheduler, Logger: logger})
	runsSvc.Routes(mux, authSvc)
	e.srv = httptest.NewServer(mux)
	t.Cleanup(e.srv.Close)
	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatal(err)
	}
	e.client = &http.Client{Jar: jar}
	resp, err := e.client.Post(e.srv.URL+"/api/v1/auth/setup", "application/json",
		strings.NewReader(`{"email":"ada@example.com","display_name":"Ada","password":"correct horse"}`))
	if err != nil {
		t.Fatal(err)
	}
	var setup struct {
		ID string `json:"id"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&setup); err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("auth setup = %d", resp.StatusCode)
	}
	e.userID = setup.ID

	now := domain.Now()
	e.proj = domain.Project{ID: domain.NewID(), Key: "PAY", Name: "Payments",
		OwnerID: e.userID, CreatedAt: now, UpdatedAt: now}
	if err := st.Projects().Create(ctx, &e.proj); err != nil {
		t.Fatal(err)
	}
	return e
}

func (e *guardEnv) mkAgent(name string) domain.Agent {
	e.t.Helper()
	now := domain.Now()
	a := domain.Agent{
		ID: domain.NewID(), ProjectID: e.proj.ID, Name: name, Role: "developer",
		Color: "#888", RuntimeID: "scripted", Model: "fake", Effort: "medium",
		Autonomy: domain.AutonomyAuto, GitAuthorName: name,
		GitAuthorEmail: strings.ToLower(name) + "@agents.lexicode.local",
		ConcurrencyCap: 4, MaxWallClockSeconds: 300, MaxSteps: 100, Enabled: true,
		CreatedAt: now, UpdatedAt: now,
	}
	if err := e.st.Agents().Create(e.ctx, &a); err != nil {
		e.t.Fatal(err)
	}
	return a
}

// mkTrigger inserts an enabled trigger with one run_agent action and the given loop config.
func (e *guardEnv) mkTrigger(name, event, activities, loopConfig string, agentID string) domain.Trigger {
	e.t.Helper()
	now := domain.Now()
	tr := domain.Trigger{
		ID: domain.NewID(), ProjectID: e.proj.ID, Name: name, Enabled: true,
		SourceID: "github.poll", Event: event,
		ActivityTypes: json.RawMessage(activities), Filters: json.RawMessage(`{}`),
		Conditions: json.RawMessage(`{"all":[]}`),
		Actions:    json.RawMessage(`[{"action_id":"run_agent","params":{"agent_id":"` + agentID + `"}}]`),
		LoopConfig: json.RawMessage(loopConfig), CreatedAt: now, UpdatedAt: now,
	}
	if err := e.st.Triggers().Create(e.ctx, &tr); err != nil {
		e.t.Fatal(err)
	}
	return tr
}

// emit publishes one pr:219 event and returns it. Each emission occurs one second after the
// previous one on the test's own clock.
func (e *guardEnv) emit(kind, activity string, actor domain.ActorKind, actorID, causeRun *string, prBody string) domain.Event {
	e.t.Helper()
	num := int64(219)
	payload := map[string]any{"pr": map[string]any{"number": 219, "body": prBody}}
	raw, err := json.Marshal(payload)
	if err != nil {
		e.t.Fatal(err)
	}
	e.clock++
	occurred := domain.FormatTime(
		time.Date(2026, 8, 21, 12, 0, 0, 0, time.UTC).Add(time.Duration(e.clock) * time.Second))
	ev := domain.Event{
		ID: domain.NewID(), ProjectID: &e.proj.ID, Source: "github.poll",
		Kind: kind, ActivityType: activity, ActorKind: actor, ActorID: actorID,
		SubjectKind: "pr", SubjectNumber: &num,
		Payload: raw, CauseRunID: causeRun,
		DedupeKey: "t:" + domain.NewID(), OccurredAt: occurred,
		CreatedAt: domain.Now(),
	}
	if err := e.bus.Emit(e.ctx, ev); err != nil {
		e.t.Fatal(err)
	}
	return ev
}

// firing waits for the (trigger, event) firing row.
func (e *guardEnv) firing(triggerID, eventID string) domain.TriggerFiring {
	e.t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		firings, err := e.st.Firings().ForTrigger(e.ctx, triggerID, 100)
		if err != nil {
			e.t.Fatal(err)
		}
		for _, f := range firings {
			if f.EventID == eventID {
				return f
			}
		}
		time.Sleep(5 * time.Millisecond)
	}
	e.t.Fatalf("no firing for trigger %s event %s", triggerID, eventID)
	return domain.TriggerFiring{}
}

func (e *guardEnv) run(id string) domain.Run {
	e.t.Helper()
	r, err := e.st.Runs().ByID(e.ctx, id)
	if err != nil {
		e.t.Fatal(err)
	}
	return r
}

// pingPongConfig switches off debounce and cancel-in-progress so the depth counter is the
// layer under test; actor suppression stays on (the two rules run different agents, so it
// never trips) and depth_limit keeps its default of 3.
const pingPongConfig = `{"debounce_seconds":0,"cancel_in_progress":false}`

// TestGuardPingPongStopsAtDepthThree is THE S27 acceptance (brief D5, §9 risk #1): a
// review → run → push → run → review → run → push chain stops at depth 3 with a terminal
// loop-stopped run whose /chain endpoint lists the exact sequence.
func TestGuardPingPongStopsAtDepthThree(t *testing.T) {
	e := newGuardEnv(t)
	dev := e.mkAgent("Dev")
	reviewer := e.mkAgent("Reviewer")
	// Dev runs when a review is submitted; Reviewer runs when the PR is pushed.
	tDev := e.mkTrigger("review → run Dev", "pull_request_review", `["submitted"]`, pingPongConfig, dev.ID)
	tRev := e.mkTrigger("push → run Reviewer", "pull_request", `["synchronize"]`, pingPongConfig, reviewer.ID)

	// A human review starts the chain.
	e1 := e.emit("pull_request_review", "submitted", domain.ActorHuman, nil, nil, "")
	f1 := e.firing(tDev.ID, e1.ID)
	if f1.Outcome != domain.FiringSucceeded || f1.RunID == nil {
		t.Fatalf("firing 1 = %+v", f1)
	}
	r1 := e.run(*f1.RunID)
	if r1.Depth != 0 || r1.SubjectKey != "pr:219" {
		t.Fatalf("r1 depth/subject = %d/%s", r1.Depth, r1.SubjectKey)
	}

	// Dev pushes (caused by r1) → Reviewer runs at depth 1.
	e2 := e.emit("pull_request", "synchronize", domain.ActorAgent, &dev.ID, &r1.ID, "")
	f2 := e.firing(tRev.ID, e2.ID)
	if f2.Outcome != domain.FiringSucceeded || f2.RunID == nil {
		t.Fatalf("firing 2 = %+v", f2)
	}
	r2 := e.run(*f2.RunID)
	if r2.Depth != 1 {
		t.Fatalf("r2 depth = %d", r2.Depth)
	}

	// Reviewer reviews (caused by r2) → Dev runs at depth 2.
	e3 := e.emit("pull_request_review", "submitted", domain.ActorAgent, &reviewer.ID, &r2.ID, "")
	f3 := e.firing(tDev.ID, e3.ID)
	if f3.Outcome != domain.FiringSucceeded || f3.RunID == nil {
		t.Fatalf("firing 3 = %+v", f3)
	}
	r3 := e.run(*f3.RunID)
	if r3.Depth != 2 {
		t.Fatalf("r3 depth = %d", r3.Depth)
	}

	// Dev pushes again (caused by r3): depth would be 3 — the guard stops the loop, and the
	// stopped run is CREATED, terminal, with no container and no cost.
	e4 := e.emit("pull_request", "synchronize", domain.ActorAgent, &dev.ID, &r3.ID, "")
	f4 := e.firing(tRev.ID, e4.ID)
	if f4.Outcome != domain.FiringLoopStopped {
		t.Fatalf("firing 4 outcome = %s (%s)", f4.Outcome, f4.Reason)
	}
	if f4.RunID == nil {
		t.Fatal("loop-stopped firing has no run row")
	}
	rs := e.run(*f4.RunID)
	if rs.State != domain.RunLoopStopped || rs.Depth != 3 || rs.SubjectKey != "pr:219" {
		t.Fatalf("stopped run = state %s depth %d subject %s", rs.State, rs.Depth, rs.SubjectKey)
	}
	if rs.ContainerID != nil || rs.CostCents != 0 || rs.EndedAt == nil {
		t.Fatalf("stopped run is not a costless terminal row: %+v", rs)
	}

	// GET /runs/{id}/chain lists the exact sequence.
	resp, err := e.client.Get(e.srv.URL + "/api/v1/runs/" + rs.ID + "/chain")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("chain status = %d", resp.StatusCode)
	}
	var chain struct {
		Chain []struct {
			Type  string `json:"type"`
			Event *struct {
				ID string `json:"id"`
			} `json:"event"`
			Run *struct {
				ID    string `json:"id"`
				State string `json:"state"`
				Depth int64  `json:"depth"`
				Focus bool   `json:"focus"`
			} `json:"run"`
		} `json:"chain"`
	}
	raw := new(strings.Builder)
	dec := json.NewDecoder(io.TeeReader(resp.Body, raw))
	if err := dec.Decode(&chain); err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, entry := range chain.Chain {
		switch entry.Type {
		case "event":
			got = append(got, "event:"+entry.Event.ID)
		case "run":
			got = append(got, "run:"+entry.Run.ID)
		}
	}
	want := []string{
		"event:" + e1.ID, "run:" + r1.ID,
		"event:" + e2.ID, "run:" + r2.ID,
		"event:" + e3.ID, "run:" + r3.ID,
		"event:" + e4.ID, "run:" + rs.ID,
	}
	if strings.Join(got, " ") != strings.Join(want, " ") {
		t.Fatalf("chain sequence =\n%v\nwant\n%v", got, want)
	}
	if last := chain.Chain[len(chain.Chain)-1].Run; last == nil || !last.Focus || last.State != "loop_stopped" {
		t.Fatalf("chain tail = %+v", last)
	}
	t.Logf("chain JSON:\n%s", raw.String())

	// The chain walks both directions: viewed from the FIRST run, the same sequence comes
	// back via the downward half (events.cause_run_id → runs.cause_event_id).
	resp2, err := e.client.Get(e.srv.URL + "/api/v1/runs/" + r1.ID + "/chain")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp2.Body.Close() }()
	var down struct {
		Chain []struct {
			Type  string `json:"type"`
			Event *struct {
				ID string `json:"id"`
			} `json:"event"`
			Run *struct {
				ID    string `json:"id"`
				Focus bool   `json:"focus"`
			} `json:"run"`
		} `json:"chain"`
	}
	if err := json.NewDecoder(resp2.Body).Decode(&down); err != nil {
		t.Fatal(err)
	}
	var got2 []string
	for _, entry := range down.Chain {
		switch entry.Type {
		case "event":
			got2 = append(got2, "event:"+entry.Event.ID)
		case "run":
			got2 = append(got2, "run:"+entry.Run.ID)
		}
	}
	if strings.Join(got2, " ") != strings.Join(want, " ") {
		t.Fatalf("chain from r1 =\n%v\nwant\n%v", got2, want)
	}
	if first := down.Chain[1].Run; first == nil || first.ID != r1.ID || !first.Focus {
		t.Fatalf("chain-from-r1 focus = %+v", first)
	}
}

// TestGuardBurstDebounce: five pushes inside the 90s window yield one run and four
// `debounced` firings all pointing at it.
func TestGuardBurstDebounce(t *testing.T) {
	e := newGuardEnv(t)
	dev := e.mkAgent("Dev")
	tr := e.mkTrigger("push → run Dev", "pull_request", `["synchronize"]`,
		string(domain.DefaultLoopConfig()), dev.ID)

	first := e.emit("pull_request", "synchronize", domain.ActorHuman, nil, nil, "")
	f1 := e.firing(tr.ID, first.ID)
	if f1.Outcome != domain.FiringSucceeded || f1.RunID == nil {
		t.Fatalf("firing 1 = %+v", f1)
	}
	for i := 0; i < 4; i++ {
		ev := e.emit("pull_request", "synchronize", domain.ActorHuman, nil, nil, "")
		f := e.firing(tr.ID, ev.ID)
		if f.Outcome != domain.FiringDebounced {
			t.Fatalf("burst firing %d outcome = %s (%s)", i+2, f.Outcome, f.Reason)
		}
		if f.AbsorbedByRunID == nil || *f.AbsorbedByRunID != *f1.RunID {
			t.Fatalf("burst firing %d absorbed_by = %v, want %s", i+2, f.AbsorbedByRunID, *f1.RunID)
		}
	}
}

// TestGuardActorSuppression: an agent's own push does not re-trigger its own rule.
func TestGuardActorSuppression(t *testing.T) {
	e := newGuardEnv(t)
	dev := e.mkAgent("Dev")
	tr := e.mkTrigger("push → run Dev", "pull_request", `["synchronize"]`,
		string(domain.DefaultLoopConfig()), dev.ID)

	ev := e.emit("pull_request", "synchronize", domain.ActorAgent, &dev.ID, nil, "")
	f := e.firing(tr.ID, ev.ID)
	if f.Outcome != domain.FiringNoAction || f.Reason != "actor suppressed" {
		t.Fatalf("firing = %s (%s)", f.Outcome, f.Reason)
	}
	if f.RunID != nil {
		t.Fatal("a suppressed firing must not start a run")
	}
}

// TestGuardHumanCommentResetsDepth: a human comment on a depth-3 subject resets the counter;
// the next agent event runs.
func TestGuardHumanCommentResetsDepth(t *testing.T) {
	e := newGuardEnv(t)
	dev := e.mkAgent("Dev")
	reviewer := e.mkAgent("Reviewer")
	tDev := e.mkTrigger("review → run Dev", "pull_request_review", `["submitted"]`, pingPongConfig, dev.ID)
	tRev := e.mkTrigger("push → run Reviewer", "pull_request", `["synchronize"]`, pingPongConfig, reviewer.ID)

	// Drive the subject to depth 3 (same shape as the ping-pong test).
	e1 := e.emit("pull_request_review", "submitted", domain.ActorHuman, nil, nil, "")
	r1 := e.run(*e.firing(tDev.ID, e1.ID).RunID)
	e2 := e.emit("pull_request", "synchronize", domain.ActorAgent, &dev.ID, &r1.ID, "")
	r2 := e.run(*e.firing(tRev.ID, e2.ID).RunID)
	e3 := e.emit("pull_request_review", "submitted", domain.ActorAgent, &reviewer.ID, &r2.ID, "")
	r3 := e.run(*e.firing(tDev.ID, e3.ID).RunID)
	e4 := e.emit("pull_request", "synchronize", domain.ActorAgent, &dev.ID, &r3.ID, "")
	if f := e.firing(tRev.ID, e4.ID); f.Outcome != domain.FiringLoopStopped {
		t.Fatalf("depth-3 firing = %s (%s)", f.Outcome, f.Reason)
	}

	// A human comments on the subject. (No trigger listens for it; its existence in the
	// event log is what resets the chain.)
	e.emit("issue_comment", "created", domain.ActorHuman, nil, nil, "")

	// The same causal ancestry no longer counts: the next agent push runs.
	e5 := e.emit("pull_request", "synchronize", domain.ActorAgent, &dev.ID, &r3.ID, "")
	f5 := e.firing(tRev.ID, e5.ID)
	if f5.Outcome != domain.FiringSucceeded || f5.RunID == nil {
		t.Fatalf("post-reset firing = %s (%s)", f5.Outcome, f5.Reason)
	}
	if r := e.run(*f5.RunID); r.Depth != 1 {
		t.Fatalf("post-reset depth = %d, want 1", r.Depth)
	}
}

// TestGuardSkipToken: `skip-agents` in the PR body suppresses with reason `skip token`.
func TestGuardSkipToken(t *testing.T) {
	e := newGuardEnv(t)
	dev := e.mkAgent("Dev")
	tr := e.mkTrigger("push → run Dev", "pull_request", `["synchronize"]`,
		string(domain.DefaultLoopConfig()), dev.ID)

	ev := e.emit("pull_request", "synchronize", domain.ActorHuman, nil, nil,
		"WIP — skip-agents until Monday")
	f := e.firing(tr.ID, ev.ID)
	if f.Outcome != domain.FiringNoAction || f.Reason != "skip token" {
		t.Fatalf("firing = %s (%s)", f.Outcome, f.Reason)
	}
}

// TestGuardCancelInProgress: a second event while a run is active cancels the first run with
// reason `superseded by run #N`, the new run proceeds, and the absorbed firing is re-marked
// `superseded` pointing at the new run.
func TestGuardCancelInProgress(t *testing.T) {
	e := newGuardEnv(t)
	dev := e.mkAgent("Dev")
	tr := e.mkTrigger("push → run Dev", "pull_request", `["synchronize"]`,
		`{"debounce_seconds":0}`, dev.ID)

	ev1 := e.emit("pull_request", "synchronize", domain.ActorHuman, nil, nil, "")
	f1 := e.firing(tr.ID, ev1.ID)
	if f1.Outcome != domain.FiringSucceeded || f1.RunID == nil {
		t.Fatalf("firing 1 = %+v", f1)
	}
	r1 := e.run(*f1.RunID)
	if r1.State != domain.RunQueued {
		t.Fatalf("r1 state = %s", r1.State)
	}

	ev2 := e.emit("pull_request", "synchronize", domain.ActorHuman, nil, nil, "")
	f2 := e.firing(tr.ID, ev2.ID)
	if f2.Outcome != domain.FiringSucceeded || f2.RunID == nil {
		t.Fatalf("firing 2 = %+v", f2)
	}
	r2 := e.run(*f2.RunID)
	if r2.State != domain.RunQueued {
		t.Fatalf("r2 state = %s", r2.State)
	}

	r1 = e.run(r1.ID)
	wantReason := fmt.Sprintf("superseded by run #%d", r2.Seq)
	if r1.State != domain.RunCanceled || r1.StateReason != wantReason {
		t.Fatalf("r1 = %s (%q), want canceled (%q)", r1.State, r1.StateReason, wantReason)
	}

	f1 = e.firing(tr.ID, ev1.ID)
	if f1.Outcome != domain.FiringSuperseded {
		t.Fatalf("firing 1 outcome after supersession = %s", f1.Outcome)
	}
	if f1.AbsorbedByRunID == nil || *f1.AbsorbedByRunID != r2.ID {
		t.Fatalf("firing 1 absorbed_by = %v, want %s", f1.AbsorbedByRunID, r2.ID)
	}
}

// TestGuardBudgetRuleCap: with a 2¢ rule/day cap, the third firing of the day — after 2¢ of
// recorded spend — terminates as `budget_exceeded`.
func TestGuardBudgetRuleCap(t *testing.T) {
	e := newGuardEnv(t)
	dev := e.mkAgent("Dev")
	tr := e.mkTrigger("push → run Dev", "pull_request", `["synchronize"]`,
		`{"debounce_seconds":0,"cancel_in_progress":false,"daily_budget_cents":2}`, dev.ID)
	day := domain.Day(time.Now())

	for i := 0; i < 2; i++ {
		ev := e.emit("pull_request", "synchronize", domain.ActorHuman, nil, nil, "")
		f := e.firing(tr.ID, ev.ID)
		if f.Outcome != domain.FiringSucceeded {
			t.Fatalf("firing %d = %s (%s)", i+1, f.Outcome, f.Reason)
		}
		// The run's spend lands in the ledger the way the scheduler's usage sink rolls it
		// up in production: one cent per run here.
		if err := e.st.Budget().Add(e.ctx, day, e.proj.ID, dev.ID, tr.ID, 1); err != nil {
			t.Fatal(err)
		}
	}

	ev3 := e.emit("pull_request", "synchronize", domain.ActorHuman, nil, nil, "")
	f3 := e.firing(tr.ID, ev3.ID)
	if f3.Outcome != domain.FiringBudgetExceeded {
		t.Fatalf("third firing = %s (%s)", f3.Outcome, f3.Reason)
	}
	if !strings.Contains(f3.Reason, "rule") {
		t.Fatalf("reason = %q", f3.Reason)
	}
}
