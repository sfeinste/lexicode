package guard

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/spruce/lexicode/internal/domain"
	"github.com/spruce/lexicode/internal/kernel/store"
)

// ---------------------------------------------------------------- config -----

func TestLoadConfigMerge(t *testing.T) {
	def := loadConfig(nil)
	if !def.ActorSuppression || def.DebounceSeconds != 90 || !def.CancelInProgress ||
		def.DepthLimit != 3 || def.DailyBudgetCents != nil {
		t.Fatalf("defaults = %+v", def)
	}
	partial := loadConfig(json.RawMessage(`{"depth_limit": 5}`))
	if partial.DepthLimit != 5 || !partial.ActorSuppression || partial.DebounceSeconds != 90 {
		t.Fatalf("partial overlay = %+v", partial)
	}
	off := loadConfig(json.RawMessage(`{"actor_suppression": false, "debounce_seconds": 0}`))
	if off.ActorSuppression || off.DebounceSeconds != 0 || !off.CancelInProgress {
		t.Fatalf("explicit off = %+v", off)
	}
	// A malformed stored config must fall back to full protection, not switch it off.
	bad := loadConfig(json.RawMessage(`{broken`))
	if !bad.ActorSuppression || bad.DepthLimit != 3 {
		t.Fatalf("malformed fallback = %+v", bad)
	}
}

// ---------------------------------------------------------------- skip token -----

func TestSkipTokens(t *testing.T) {
	cases := []struct {
		payload string
		want    bool
	}{
		{`{"pr": {"body": "WIP, skip-agents please"}}`, true},
		{`{"pr": {"body": "skip-agent"}}`, true},
		{`{"pr": {"body": "chore: [skip agents]"}}`, true},
		{`{"pr": {"body": "SKIP-AGENTS"}}`, true},
		{`{"comment": {"body": "bot said skip-agents"}}`, true},
		{`{"review": {"body": "skip-agents for now"}}`, true},
		{`{"pr": {"head_commit_message": "fix typo [skip agents]"}}`, true},
		{`{"pr": {"body": "please review"}}`, false},
		{`{"pr": {"title": "skip-agents"}}`, false}, // the title is not a skip-carrying field
		{`{}`, false},
	}
	for _, c := range cases {
		var p map[string]any
		if err := json.Unmarshal([]byte(c.payload), &p); err != nil {
			t.Fatal(err)
		}
		if got := hasSkipToken(p); got != c.want {
			t.Errorf("hasSkipToken(%s) = %v, want %v", c.payload, got, c.want)
		}
	}
}

// ---------------------------------------------------------------- fixtures -----

type fix struct {
	t     *testing.T
	ctx   context.Context
	st    *store.Store
	proj  domain.Project
	agent domain.Agent
}

func newFix(t *testing.T) *fix {
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
	now := domain.Now()
	owner := domain.User{ID: domain.NewID(), Email: "ada@example.com", DisplayName: "Ada",
		PasswordHash: "x", Role: domain.RoleOwner, AvatarColor: "#123", CreatedAt: now}
	if err := st.Users().Create(ctx, &owner); err != nil {
		t.Fatal(err)
	}
	proj := domain.Project{ID: domain.NewID(), Key: "PAY", Name: "Payments", OwnerID: owner.ID,
		CreatedAt: now, UpdatedAt: now}
	if err := st.Projects().Create(ctx, &proj); err != nil {
		t.Fatal(err)
	}
	f := &fix{t: t, ctx: ctx, st: st, proj: proj}
	f.agent = f.mkAgent("Dev")
	return f
}

func (f *fix) mkAgent(name string) domain.Agent {
	f.t.Helper()
	now := domain.Now()
	a := domain.Agent{
		ID: domain.NewID(), ProjectID: f.proj.ID, Name: name, Role: "developer",
		Color: "#888", RuntimeID: "scripted", Model: "fake", Effort: "medium",
		Autonomy: domain.AutonomyAuto, GitAuthorName: name,
		GitAuthorEmail: strings.ToLower(name) + "@agents.lexicode.local",
		ConcurrencyCap: 1, MaxWallClockSeconds: 300, MaxSteps: 100, Enabled: true,
		CreatedAt: now, UpdatedAt: now,
	}
	if err := f.st.Agents().Create(f.ctx, &a); err != nil {
		f.t.Fatal(err)
	}
	return a
}

func (f *fix) mkTrigger(loopConfig string) domain.Trigger {
	f.t.Helper()
	now := domain.Now()
	lc := json.RawMessage(loopConfig)
	if loopConfig == "" {
		lc = domain.DefaultLoopConfig()
	}
	tr := domain.Trigger{
		ID: domain.NewID(), ProjectID: f.proj.ID, Name: "rule", Enabled: true,
		SourceID: "github.poll", Event: "pull_request",
		ActivityTypes: json.RawMessage(`["synchronize"]`), Filters: json.RawMessage(`{}`),
		Conditions: json.RawMessage(`{"all":[]}`),
		Actions:    json.RawMessage(`[{"action_id":"run_agent","params":{"agent_id":"` + f.agent.ID + `"}}]`),
		LoopConfig: lc, CreatedAt: now, UpdatedAt: now,
	}
	if err := f.st.Triggers().Create(f.ctx, &tr); err != nil {
		f.t.Fatal(err)
	}
	return tr
}

// mkRun inserts a run row directly (tests may; production rows come from the scheduler).
func (f *fix) mkRun(tr domain.Trigger, state domain.RunState, subject string, causeEvent *string, queuedAt string) domain.Run {
	f.t.Helper()
	run := domain.Run{
		ID: domain.NewID(), ProjectID: f.proj.ID, AgentID: f.agent.ID,
		State: state, Autonomy: domain.AutonomyAuto, Model: "fake", Effort: "medium",
		RuntimeID: "scripted", SandboxID: "fake",
		SubjectKey: subject, QueuedAt: queuedAt,
	}
	tid := tr.ID
	run.TriggerID = &tid
	run.CauseEventID = causeEvent
	err := f.st.Tx(f.ctx, func(tx *store.Tx) error {
		seq, err := tx.Runs().NextSeq(f.ctx, f.proj.ID)
		if err != nil {
			return err
		}
		run.Seq = seq
		return tx.Runs().Create(f.ctx, &run)
	})
	if err != nil {
		f.t.Fatal(err)
	}
	return run
}

// mkEvent inserts a pr:219 event row directly.
func (f *fix) mkEvent(kind, activity string, actor domain.ActorKind, actorID, causeRun *string, occurredAt string) domain.Event {
	f.t.Helper()
	num := int64(219)
	e := domain.Event{
		ID: domain.NewID(), ProjectID: &f.proj.ID, Source: "github.poll",
		Kind: kind, ActivityType: activity, ActorKind: actor, ActorID: actorID,
		SubjectKind: "pr", SubjectNumber: &num,
		Payload: json.RawMessage(`{"pr":{"number":219}}`), CauseRunID: causeRun,
		DedupeKey: "t:" + domain.NewID(), DispatchState: domain.DispatchDone,
		OccurredAt: occurredAt, CreatedAt: occurredAt,
	}
	if err := f.st.Events().Insert(f.ctx, &e); err != nil {
		f.t.Fatal(err)
	}
	return e
}

func (f *fix) eval(loop LoopStopper) *Layers {
	return New(Options{Store: f.st, Loop: loop,
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil))})
}

func (f *fix) input(tr domain.Trigger, ev domain.Event) Input {
	return Input{Event: ev, Trigger: tr, SubjectKey: "pr:219",
		Payload:     map[string]any{"pr": map[string]any{"number": float64(219)}},
		RunAgentIDs: []string{f.agent.ID}}
}

// stamp renders an offset from a fixed base as a stored timestamp.
func stamp(sec int) string {
	base := time.Date(2026, 8, 21, 12, 0, 0, 0, time.UTC)
	return domain.FormatTime(base.Add(time.Duration(sec) * time.Second))
}

// recorder is a LoopStopper that inserts the terminal row the way the scheduler would.
type recorder struct {
	st      *store.Store
	created []LoopStoppedRun
	runs    []domain.Run
}

func (r *recorder) EnqueueLoopStopped(ctx context.Context, req LoopStoppedRun) (domain.Run, error) {
	r.created = append(r.created, req)
	now := domain.Now()
	run := domain.Run{
		ID: domain.NewID(), ProjectID: req.ProjectID, AgentID: req.AgentID,
		State: domain.RunLoopStopped, StateReason: req.Reason,
		Autonomy: domain.AutonomyAuto, Model: "fake", Effort: "medium",
		RuntimeID: "scripted", SandboxID: "fake",
		SubjectKey: req.SubjectKey, Depth: req.Depth, QueuedAt: now, EndedAt: &now,
	}
	if req.TriggerID != "" {
		run.TriggerID = &req.TriggerID
	}
	if req.CauseEventID != "" {
		run.CauseEventID = &req.CauseEventID
	}
	err := r.st.Tx(ctx, func(tx *store.Tx) error {
		seq, err := tx.Runs().NextSeq(ctx, run.ProjectID)
		if err != nil {
			return err
		}
		run.Seq = seq
		return tx.Runs().Create(ctx, &run)
	})
	if err != nil {
		return domain.Run{}, err
	}
	r.runs = append(r.runs, run)
	return run, nil
}

// ---------------------------------------------------------------- layer 1 -----

func TestActorSuppression(t *testing.T) {
	f := newFix(t)
	tr := f.mkTrigger(`{"debounce_seconds":0,"cancel_in_progress":false,"depth_limit":0}`)
	ev := f.mkEvent("pull_request", "synchronize", domain.ActorAgent, &f.agent.ID, nil, stamp(0))

	v := f.eval(nil).Evaluate(f.ctx, f.input(tr, ev))
	if v.Proceed || v.Outcome != domain.FiringNoAction || v.Reason != "actor suppressed" {
		t.Fatalf("own-agent event verdict = %+v", v)
	}

	// A different agent's event passes layer 1.
	other := f.mkAgent("Reviewer")
	ev2 := f.mkEvent("pull_request", "synchronize", domain.ActorAgent, &other.ID, nil, stamp(1))
	if v := f.eval(nil).Evaluate(f.ctx, f.input(tr, ev2)); !v.Proceed {
		t.Fatalf("other-agent event verdict = %+v", v)
	}

	// The layer can be switched off per trigger.
	trOff := f.mkTrigger(`{"actor_suppression":false,"debounce_seconds":0,"cancel_in_progress":false,"depth_limit":0}`)
	ev3 := f.mkEvent("pull_request", "synchronize", domain.ActorAgent, &f.agent.ID, nil, stamp(2))
	if v := f.eval(nil).Evaluate(f.ctx, f.input(trOff, ev3)); !v.Proceed {
		t.Fatalf("suppression-off verdict = %+v", v)
	}
}

// TestCheckSuiteExemptFromActorSuppression: a CI result is a machine's verdict about the
// agent's work, not the agent acting. The poller attributes a check suite by the branch it ran
// on — always the branch of the agent whose work is being judged — so with the exemption
// absent, "CI failed → Dev fixes it" (the brief's step 6) is suppressed by layer 1 on every
// branch Dev owns and can never fire.
func TestCheckSuiteExemptFromActorSuppression(t *testing.T) {
	f := newFix(t)
	tr := f.mkTrigger(`{"debounce_seconds":0,"cancel_in_progress":false,"depth_limit":0}`)

	// A check_suite attributed to the very agent the rule would run: NOT suppressed.
	ci := f.mkEvent("check_suite", "completed", domain.ActorAgent, &f.agent.ID, nil, stamp(0))
	if v := f.eval(nil).Evaluate(f.ctx, f.input(tr, ci)); !v.Proceed {
		t.Fatalf("check_suite attributed to the rule's own agent = %+v, want proceed", v)
	}

	// The exemption is scoped to check_suite: a pull_request event attributed to the same
	// agent is still suppressed, with actor suppression left at its default (on).
	push := f.mkEvent("pull_request", "synchronize", domain.ActorAgent, &f.agent.ID, nil, stamp(1))
	v := f.eval(nil).Evaluate(f.ctx, f.input(tr, push))
	if v.Proceed || v.Outcome != domain.FiringNoAction || v.Reason != "actor suppressed" {
		t.Fatalf("pull_request attributed to the rule's own agent = %+v, want actor suppressed", v)
	}

	// And a review event attributed to the same agent is still suppressed too.
	rev := f.mkEvent("pull_request_review", "submitted", domain.ActorAgent, &f.agent.ID, nil, stamp(2))
	if v := f.eval(nil).Evaluate(f.ctx, f.input(tr, rev)); v.Proceed {
		t.Fatalf("pull_request_review attributed to the rule's own agent = %+v, want suppressed", v)
	}
}

// TestCheckSuiteStillCountsForDepth: the exemption is layer 1 only. A check_suite event whose
// chain has already reached the depth limit is still stopped by layer 4.
func TestCheckSuiteStillCountsForDepth(t *testing.T) {
	f := newFix(t)
	tr := f.mkTrigger(`{"debounce_seconds":0,"cancel_in_progress":false,"depth_limit":1}`)
	root := f.mkEvent("check_suite", "completed", domain.ActorHuman, nil, nil, stamp(0))
	run := f.mkRun(tr, domain.RunCompleted, "pr:219", &root.ID, stamp(1))
	ci := f.mkEvent("check_suite", "completed", domain.ActorAgent, &f.agent.ID, &run.ID, stamp(2))

	rec := &recorder{st: f.st}
	v := f.eval(rec).Evaluate(f.ctx, f.input(tr, ci))
	if v.Proceed || v.Outcome != domain.FiringLoopStopped {
		t.Fatalf("check_suite at the depth limit = %+v, want loop_stopped", v)
	}
}

// ---------------------------------------------------------------- layer 2 -----

func TestDebounce(t *testing.T) {
	f := newFix(t)
	tr := f.mkTrigger(`{"cancel_in_progress":false,"depth_limit":0}`) // debounce 90s by default
	run := f.mkRun(tr, domain.RunQueued, "pr:219", nil, domain.Now())
	ev := f.mkEvent("pull_request", "synchronize", domain.ActorHuman, nil, nil, domain.Now())

	v := f.eval(nil).Evaluate(f.ctx, f.input(tr, ev))
	if v.Proceed || v.Outcome != domain.FiringDebounced {
		t.Fatalf("in-window verdict = %+v", v)
	}
	if v.AbsorbedByRunID == nil || *v.AbsorbedByRunID != run.ID {
		t.Fatalf("absorbed_by = %v, want %s", v.AbsorbedByRunID, run.ID)
	}

	// A different subject is not absorbed.
	in := f.input(tr, ev)
	in.SubjectKey = "pr:999"
	if v := f.eval(nil).Evaluate(f.ctx, in); !v.Proceed {
		t.Fatalf("other-subject verdict = %+v", v)
	}

	// A run older than the window does not absorb.
	tr2 := f.mkTrigger(`{"cancel_in_progress":false,"depth_limit":0}`)
	old := domain.FormatTime(time.Now().Add(-5 * time.Minute))
	f.mkRun(tr2, domain.RunCompleted, "pr:219", nil, old)
	if v := f.eval(nil).Evaluate(f.ctx, f.input(tr2, ev)); !v.Proceed {
		t.Fatalf("out-of-window verdict = %+v", v)
	}

	// A loop_stopped row is a guard artifact, not started work: it never absorbs.
	tr3 := f.mkTrigger(`{"cancel_in_progress":false,"depth_limit":0}`)
	f.mkRun(tr3, domain.RunLoopStopped, "pr:219", nil, domain.Now())
	if v := f.eval(nil).Evaluate(f.ctx, f.input(tr3, ev)); !v.Proceed {
		t.Fatalf("loop-stopped-absorber verdict = %+v", v)
	}
}

// ---------------------------------------------------------------- layer 3 -----

func TestCancelInProgressPassThrough(t *testing.T) {
	f := newFix(t)
	tr := f.mkTrigger(`{"debounce_seconds":0,"depth_limit":0}`) // cancel on by default
	active := f.mkRun(tr, domain.RunRunning, "pr:219", nil, domain.Now())
	ev := f.mkEvent("pull_request", "synchronize", domain.ActorHuman, nil, nil, domain.Now())

	v := f.eval(nil).Evaluate(f.ctx, f.input(tr, ev))
	if !v.Proceed {
		t.Fatalf("verdict = %+v", v)
	}
	if v.Pass.SupersededRunID != active.ID {
		t.Fatalf("superseded = %q, want %s", v.Pass.SupersededRunID, active.ID)
	}

	// Terminal runs are not superseded.
	tr2 := f.mkTrigger(`{"debounce_seconds":0,"depth_limit":0}`)
	f.mkRun(tr2, domain.RunCompleted, "pr:219", nil, domain.Now())
	if v := f.eval(nil).Evaluate(f.ctx, f.input(tr2, ev)); v.Pass.SupersededRunID != "" {
		t.Fatalf("terminal run superseded: %+v", v.Pass)
	}
}

// ---------------------------------------------------------------- layer 4 -----

// chain builds the ping-pong ancestry: e1 (human) → r1 → e2 → r2 → e3 → r3, then the
// triggering event e4 caused by r3. Returns e4.
func (f *fix) chain(tr domain.Trigger) domain.Event {
	f.t.Helper()
	e1 := f.mkEvent("pull_request_review", "submitted", domain.ActorHuman, nil, nil, stamp(0))
	r1 := f.mkRun(tr, domain.RunCompleted, "pr:219", &e1.ID, stamp(1))
	e2 := f.mkEvent("pull_request", "synchronize", domain.ActorAgent, &f.agent.ID, &r1.ID, stamp(2))
	r2 := f.mkRun(tr, domain.RunCompleted, "pr:219", &e2.ID, stamp(3))
	e3 := f.mkEvent("pull_request_review", "submitted", domain.ActorAgent, &f.agent.ID, &r2.ID, stamp(4))
	r3 := f.mkRun(tr, domain.RunCompleted, "pr:219", &e3.ID, stamp(5))
	return f.mkEvent("pull_request", "synchronize", domain.ActorAgent, &f.agent.ID, &r3.ID, stamp(6))
}

func TestDepthCounterStopsAtLimit(t *testing.T) {
	f := newFix(t)
	tr := f.mkTrigger(`{"actor_suppression":false,"debounce_seconds":0,"cancel_in_progress":false}`)
	e4 := f.chain(tr)

	rec := &recorder{st: f.st}
	v := f.eval(rec).Evaluate(f.ctx, f.input(tr, e4))
	if v.Proceed || v.Outcome != domain.FiringLoopStopped {
		t.Fatalf("verdict = %+v", v)
	}
	if len(rec.created) != 1 || rec.created[0].Depth != 3 || rec.created[0].SubjectKey != "pr:219" {
		t.Fatalf("loop-stopped request = %+v", rec.created)
	}
	if v.RunID == nil || *v.RunID != rec.runs[0].ID {
		t.Fatalf("verdict run = %v", v.RunID)
	}
	if !strings.Contains(v.Reason, "depth 3") {
		t.Fatalf("reason = %q", v.Reason)
	}
}

func TestDepthBelowLimitPasses(t *testing.T) {
	f := newFix(t)
	tr := f.mkTrigger(`{"actor_suppression":false,"debounce_seconds":0,"cancel_in_progress":false,"depth_limit":4}`)
	e4 := f.chain(tr)

	v := f.eval(nil).Evaluate(f.ctx, f.input(tr, e4))
	if !v.Proceed || v.Pass.Depth != 3 {
		t.Fatalf("verdict = %+v", v)
	}
}

func TestHumanActionResetsDepth(t *testing.T) {
	f := newFix(t)
	tr := f.mkTrigger(`{"actor_suppression":false,"debounce_seconds":0,"cancel_in_progress":false}`)
	e4 := f.chain(tr)

	// Depth-3 subject: stopped.
	rec := &recorder{st: f.st}
	if v := f.eval(rec).Evaluate(f.ctx, f.input(tr, e4)); v.Proceed || v.Outcome != domain.FiringLoopStopped {
		t.Fatalf("pre-reset verdict = %+v", v)
	}

	// A human comments on the subject; the same causal ancestry no longer counts.
	f.mkEvent("issue_comment", "created", domain.ActorHuman, nil, nil, stamp(7))
	r3ID := *e4.CauseRunID
	e5 := f.mkEvent("pull_request", "synchronize", domain.ActorAgent, &f.agent.ID, &r3ID, stamp(8))
	v := f.eval(rec).Evaluate(f.ctx, f.input(tr, e5))
	if !v.Proceed {
		t.Fatalf("post-reset verdict = %+v", v)
	}
	if v.Pass.Depth != 1 {
		t.Fatalf("post-reset depth = %d, want 1", v.Pass.Depth)
	}
}

func TestManualRunBreaksChain(t *testing.T) {
	f := newFix(t)
	tr := f.mkTrigger(`{"actor_suppression":false,"debounce_seconds":0,"cancel_in_progress":false}`)
	// e1 → r1 (manual: requested by a human) → e2: depth walks to r1 and stops without
	// counting it — a human-requested run is a chain reset, not a chain link.
	e1 := f.mkEvent("pull_request_review", "submitted", domain.ActorAgent, &f.agent.ID, nil, stamp(0))
	uid := f.proj.OwnerID
	r1 := domain.Run{
		ID: domain.NewID(), ProjectID: f.proj.ID, AgentID: f.agent.ID,
		State: domain.RunCompleted, Autonomy: domain.AutonomyAuto, Model: "fake",
		Effort: "medium", RuntimeID: "scripted", SandboxID: "fake",
		SubjectKey: "pr:219", CauseEventID: &e1.ID, RequestedByUserID: &uid,
		QueuedAt: stamp(1),
	}
	if err := f.st.Tx(f.ctx, func(tx *store.Tx) error {
		seq, err := tx.Runs().NextSeq(f.ctx, f.proj.ID)
		if err != nil {
			return err
		}
		r1.Seq = seq
		return tx.Runs().Create(f.ctx, &r1)
	}); err != nil {
		t.Fatal(err)
	}
	e2 := f.mkEvent("pull_request", "synchronize", domain.ActorAgent, &f.agent.ID, &r1.ID, stamp(2))

	v := f.eval(nil).Evaluate(f.ctx, f.input(tr, e2))
	if !v.Proceed || v.Pass.Depth != 0 {
		t.Fatalf("verdict = %+v", v)
	}
}

// ---------------------------------------------------------------- layer 5 -----

func TestBudgetRuleCap(t *testing.T) {
	f := newFix(t)
	tr := f.mkTrigger(`{"debounce_seconds":0,"cancel_in_progress":false,"depth_limit":0,"daily_budget_cents":2}`)
	ev := f.mkEvent("pull_request", "synchronize", domain.ActorHuman, nil, nil, domain.Now())
	day := domain.Day(time.Now())

	if v := f.eval(nil).Evaluate(f.ctx, f.input(tr, ev)); !v.Proceed {
		t.Fatalf("under-cap verdict = %+v", v)
	}
	if err := f.st.Budget().Add(f.ctx, day, f.proj.ID, f.agent.ID, tr.ID, 2); err != nil {
		t.Fatal(err)
	}
	v := f.eval(nil).Evaluate(f.ctx, f.input(tr, ev))
	if v.Proceed || v.Outcome != domain.FiringBudgetExceeded {
		t.Fatalf("at-cap verdict = %+v", v)
	}
	if !strings.Contains(v.Reason, "rule") {
		t.Fatalf("reason = %q", v.Reason)
	}
}

func TestBudgetProjectCeiling(t *testing.T) {
	f := newFix(t)
	ceiling := int64(10)
	f.proj.DailyBudgetCents = &ceiling
	if err := f.st.Projects().Update(f.ctx, &f.proj); err != nil {
		t.Fatal(err)
	}
	tr := f.mkTrigger(`{"debounce_seconds":0,"cancel_in_progress":false,"depth_limit":0}`)
	ev := f.mkEvent("pull_request", "synchronize", domain.ActorHuman, nil, nil, domain.Now())
	if err := f.st.Budget().Add(f.ctx, domain.Day(time.Now()), f.proj.ID, "", "", 10); err != nil {
		t.Fatal(err)
	}
	v := f.eval(nil).Evaluate(f.ctx, f.input(tr, ev))
	if v.Proceed || v.Outcome != domain.FiringBudgetExceeded || !strings.Contains(v.Reason, "project") {
		t.Fatalf("verdict = %+v", v)
	}
}

func TestBudgetAgentCap(t *testing.T) {
	f := newFix(t)
	cap := int64(5)
	f.agent.DailyCapCents = &cap
	if err := f.st.Agents().Update(f.ctx, &f.agent); err != nil {
		t.Fatal(err)
	}
	tr := f.mkTrigger(`{"debounce_seconds":0,"cancel_in_progress":false,"depth_limit":0}`)
	ev := f.mkEvent("pull_request", "synchronize", domain.ActorHuman, nil, nil, domain.Now())
	if err := f.st.Budget().Add(f.ctx, domain.Day(time.Now()), f.proj.ID, f.agent.ID, "", 5); err != nil {
		t.Fatal(err)
	}
	v := f.eval(nil).Evaluate(f.ctx, f.input(tr, ev))
	if v.Proceed || v.Outcome != domain.FiringBudgetExceeded || !strings.Contains(v.Reason, "agent") {
		t.Fatalf("verdict = %+v", v)
	}
}

// ---------------------------------------------------------------- escape hatch -----

func TestSkipTokenShortCircuitsEverything(t *testing.T) {
	f := newFix(t)
	// Every layer would have something to say; the token wins before any of them run.
	tr := f.mkTrigger("")
	f.mkRun(tr, domain.RunRunning, "pr:219", nil, domain.Now())
	ev := f.mkEvent("pull_request", "synchronize", domain.ActorAgent, &f.agent.ID, nil, domain.Now())

	in := f.input(tr, ev)
	in.Payload = map[string]any{"pr": map[string]any{"number": float64(219), "body": "skip-agents"}}
	v := f.eval(nil).Evaluate(f.ctx, in)
	if v.Proceed || v.Outcome != domain.FiringNoAction || v.Reason != "skip token" {
		t.Fatalf("verdict = %+v", v)
	}
}
