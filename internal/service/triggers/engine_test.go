package triggers

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/spruce/lexicode/internal/domain"
	"github.com/spruce/lexicode/internal/kernel/bus"
	"github.com/spruce/lexicode/internal/kernel/guard"
	"github.com/spruce/lexicode/internal/kernel/ports"
	"github.com/spruce/lexicode/internal/kernel/store"
)

// engineEnv is a real store, a real bus and the engine, wired the way cmd/lexicode wires them.
type engineEnv struct {
	t      *testing.T
	ctx    context.Context
	st     *store.Store
	bus    *bus.Bus
	engine *Engine
}

func newEngineEnv(t *testing.T, opts EngineOptions) *engineEnv {
	t.Helper()
	ctx := context.Background()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	st, err := store.Open(store.Options{Path: filepath.Join(t.TempDir(), "s26.db"), Logger: logger})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	if _, err := st.Migrate(ctx); err != nil {
		t.Fatal(err)
	}

	b := bus.New(bus.Options{Store: st, Logger: logger})
	opts.Store = st
	opts.Bus = b
	opts.Logger = logger
	eng := NewEngine(opts)
	if err := eng.Subscribe(b); err != nil {
		t.Fatal(err)
	}
	if err := b.Start(ctx); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		stopCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = b.Stop(stopCtx)
		_ = eng.Stop(stopCtx)
	})
	return &engineEnv{t: t, ctx: ctx, st: st, bus: b, engine: eng}
}

// project creates a user+project pair and returns the project ID.
func (e *engineEnv) project(key string) string {
	e.t.Helper()
	owner := domain.User{
		ID: domain.NewID(), Email: key + "@example.com", DisplayName: key, PasswordHash: "x",
		Role: domain.RoleOwner, AvatarColor: "#000", CreatedAt: domain.Now(),
	}
	if err := e.st.Users().Create(e.ctx, &owner); err != nil {
		e.t.Fatal(err)
	}
	p := domain.Project{
		ID: domain.NewID(), Key: key, Name: "Project " + key, Color: "#fff", OwnerID: owner.ID,
		CreatedAt: domain.Now(), UpdatedAt: domain.Now(),
	}
	if err := e.st.Projects().Create(e.ctx, &p); err != nil {
		e.t.Fatal(err)
	}
	return p.ID
}

// trigger inserts an enabled trigger straight through the repo (validation is the CRUD
// surface's concern, exercised in service_test.go).
func (e *engineEnv) trigger(projectID, name, event, activities, conditions, actions string, enabled bool) string {
	e.t.Helper()
	now := domain.Now()
	tr := domain.Trigger{
		ID: domain.NewID(), ProjectID: projectID, Name: name, Enabled: enabled,
		SourceID: "github.poll", Event: event,
		ActivityTypes: json.RawMessage(activities), Filters: json.RawMessage(`{}`),
		Conditions: json.RawMessage(conditions), Actions: json.RawMessage(actions),
		LoopConfig: domain.DefaultLoopConfig(), CreatedAt: now, UpdatedAt: now,
	}
	if err := e.st.Triggers().Create(e.ctx, &tr); err != nil {
		e.t.Fatal(err)
	}
	return tr.ID
}

// prEventFor builds a pull_request event with the given actor kind and diff size.
func prEventFor(projectID, activity string, actorKind domain.ActorKind, filesChanged int) domain.Event {
	payload := fmt.Sprintf(`{
		"pr": {"number": 219, "title": "t", "author": "someone", "branch": "dev/PAY-14",
		       "files_changed": %d, "labels": []},
		"actor": {"kind": %q, "login": "someone", "agent": "dev"}
	}`, filesChanged, actorKind)
	num := int64(219)
	return domain.Event{
		ID: domain.NewID(), ProjectID: &projectID, Source: "github.poll",
		Kind: "pull_request", ActivityType: activity, ActorKind: actorKind,
		SubjectKind: "pr", SubjectNumber: &num,
		Payload: json.RawMessage(payload), DedupeKey: "test:" + domain.NewID(),
		OccurredAt: domain.Now(), CreatedAt: domain.Now(),
	}
}

// waitFiring polls until the (trigger, event) pair has a firing row.
func (e *engineEnv) waitFiring(triggerID, eventID string) domain.TriggerFiring {
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
	e.t.Fatalf("no firing appeared for trigger %s event %s", triggerID, eventID)
	return domain.TriggerFiring{}
}

func (e *engineEnv) firingCount(triggerID string) int {
	e.t.Helper()
	firings, err := e.st.Firings().ForTrigger(e.ctx, triggerID, 500)
	if err != nil {
		e.t.Fatal(err)
	}
	return len(firings)
}

// fakeAction is a scriptable ports.TriggerAction.
type fakeAction struct {
	id      string
	execute func(ctx context.Context, ac ports.ActionContext, params json.RawMessage) (ports.ActionResult, error)
}

func (f *fakeAction) ID() string                { return f.id }
func (f *fakeAction) Label() string             { return f.id }
func (f *fakeAction) Schema() ports.ParamSchema { return ports.ParamSchema{} }
func (f *fakeAction) Describe(json.RawMessage) (string, error) {
	return f.id, nil
}
func (f *fakeAction) Execute(ctx context.Context, ac ports.ActionContext, params json.RawMessage) (ports.ActionResult, error) {
	return f.execute(ctx, ac, params)
}

func actionRegistry(actions ...*fakeAction) func(id string) (ports.TriggerAction, error) {
	return func(id string) (ports.TriggerAction, error) {
		for _, a := range actions {
			if a.id == id {
				return a, nil
			}
		}
		return nil, fmt.Errorf("no trigger action %q is registered", id)
	}
}

const agentSmallDiffRule = `{"all":[
	{"op":"actor.is_agent"},
	{"field":"pr.files_changed","op":"number.lt","value":400}]}`

// TestEngineAcceptance is the story's acceptance test: IF actor is an agent AND
// pr.files_changed < 400 on pull_request/synchronize —
//
//	agent push, small diff  → succeeded
//	human push              → no_action, reason in words
//	agent push, huge diff   → no_action, reason in words
//	non-matching kind       → no firing row at all
func TestEngineAcceptance(t *testing.T) {
	var executed sync.Map
	act := &fakeAction{id: "test.run", execute: func(_ context.Context, ac ports.ActionContext, _ json.RawMessage) (ports.ActionResult, error) {
		executed.Store(ac.Event.ID, true)
		return ports.ActionResult{Outcome: domain.FiringSucceeded, Note: "enqueued a run for Dev"}, nil
	}}
	env := newEngineEnv(t, EngineOptions{Action: actionRegistry(act)})
	pid := env.project("PAY")
	trID := env.trigger(pid, "Agent push → review", "pull_request", `["synchronize"]`,
		agentSmallDiffRule, `[{"action_id":"test.run","params":{}}]`, true)

	agentPush := prEventFor(pid, "synchronize", domain.ActorAgent, 7)
	humanPush := prEventFor(pid, "synchronize", domain.ActorHuman, 7)
	bigDiff := prEventFor(pid, "synchronize", domain.ActorAgent, 900)
	otherKind := prEventFor(pid, "created", domain.ActorAgent, 7)
	otherKind.Kind = "issue_comment"

	for _, ev := range []domain.Event{agentPush, humanPush, bigDiff, otherKind} {
		if err := env.bus.Publish(env.ctx, ev); err != nil {
			t.Fatal(err)
		}
	}
	// A sentinel published last: the project worker is FIFO, so once it has fired, every
	// earlier event has been fully evaluated.
	sentinel := prEventFor(pid, "synchronize", domain.ActorAgent, 1)
	if err := env.bus.Publish(env.ctx, sentinel); err != nil {
		t.Fatal(err)
	}
	env.waitFiring(trID, sentinel.ID)

	f := env.waitFiring(trID, agentPush.ID)
	if f.Outcome != domain.FiringSucceeded {
		t.Fatalf("agent push outcome = %s (%s), want succeeded", f.Outcome, f.Reason)
	}
	if f.Reason != "enqueued a run for Dev" {
		t.Fatalf("succeeded reason = %q, want the action's note", f.Reason)
	}
	if _, ok := executed.Load(agentPush.ID); !ok {
		t.Fatal("the action never executed for the agent push")
	}

	for name, ev := range map[string]domain.Event{"human push": humanPush, "big diff": bigDiff} {
		f := env.waitFiring(trID, ev.ID)
		if f.Outcome != domain.FiringNoAction {
			t.Fatalf("%s outcome = %s, want no_action", name, f.Outcome)
		}
		if f.Reason != "conditions not met" {
			t.Fatalf("%s reason = %q, want words, not a code", name, f.Reason)
		}
		if _, ok := executed.Load(ev.ID); ok {
			t.Fatalf("%s executed the action despite failing conditions", name)
		}
	}

	// The non-matching kind wrote nothing: not a firing, not even a no_action.
	firings, err := env.st.Firings().ForTrigger(env.ctx, trID, 100)
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range firings {
		if f.EventID == otherKind.ID {
			t.Fatalf("a non-matching event wrote a firing row: %+v", f)
		}
	}
	if got := env.firingCount(trID); got != 4 { // agent, human, big, sentinel
		t.Fatalf("firing count = %d, want 4", got)
	}
}

// TestEngineIdempotency: re-delivering the same event (the boot-recovery path) neither
// re-executes actions nor writes a second firing — the UNIQUE(trigger_id, event_id) index and
// the pre-flight both hold.
func TestEngineIdempotency(t *testing.T) {
	var executions int
	var mu sync.Mutex
	act := &fakeAction{id: "test.run", execute: func(context.Context, ports.ActionContext, json.RawMessage) (ports.ActionResult, error) {
		mu.Lock()
		executions++
		mu.Unlock()
		return ports.ActionResult{Outcome: domain.FiringSucceeded}, nil
	}}
	env := newEngineEnv(t, EngineOptions{Action: actionRegistry(act)})
	pid := env.project("PAY")
	trID := env.trigger(pid, "rule", "pull_request", `["synchronize"]`,
		`{"all":[]}`, `[{"action_id":"test.run","params":{}}]`, true)

	ev := prEventFor(pid, "synchronize", domain.ActorAgent, 7)
	if err := env.bus.Publish(env.ctx, ev); err != nil {
		t.Fatal(err)
	}
	env.waitFiring(trID, ev.ID)

	// Re-dispatch the same persisted event, exactly as bus boot recovery would.
	if err := env.engine.handle(env.ctx, ev); err != nil {
		t.Fatal(err)
	}
	sentinel := prEventFor(pid, "synchronize", domain.ActorAgent, 1)
	if err := env.bus.Publish(env.ctx, sentinel); err != nil {
		t.Fatal(err)
	}
	env.waitFiring(trID, sentinel.ID)

	mu.Lock()
	got := executions
	mu.Unlock()
	if got != 2 { // once per distinct event; the re-dispatch executed nothing
		t.Fatalf("action executed %d times, want 2", got)
	}
	if n := env.firingCount(trID); n != 2 {
		t.Fatalf("firing count = %d, want 2 (one per distinct event)", n)
	}

	// And the unique index itself, beneath the pre-flight: a second insert for the same
	// (trigger, event) pair inserts nothing.
	dup := domain.TriggerFiring{
		ID: domain.NewID(), TriggerID: trID, EventID: ev.ID,
		Outcome: domain.FiringSucceeded, CreatedAt: domain.Now(),
	}
	inserted, err := env.st.Firings().Create(env.ctx, &dup)
	if err != nil {
		t.Fatal(err)
	}
	if inserted {
		t.Fatal("the unique index let a second firing through for the same (trigger, event)")
	}
}

// TestEngineInterpolationWarnings: an unknown {{path}} in an action's template renders as ""
// and the warning is visible on the firing row.
func TestEngineInterpolationWarnings(t *testing.T) {
	act := &fakeAction{id: "test.run", execute: func(_ context.Context, ac ports.ActionContext, _ json.RawMessage) (ports.ActionResult, error) {
		rendered, _ := ac.Interp("review {{pr.nonexistent}} please")
		if rendered != "review  please" {
			return ports.ActionResult{}, fmt.Errorf("interp rendered %q", rendered)
		}
		return ports.ActionResult{Outcome: domain.FiringSucceeded}, nil
	}}
	env := newEngineEnv(t, EngineOptions{Action: actionRegistry(act)})
	pid := env.project("PAY")
	trID := env.trigger(pid, "rule", "pull_request", `["synchronize"]`,
		`{"all":[]}`, `[{"action_id":"test.run","params":{}}]`, true)

	ev := prEventFor(pid, "synchronize", domain.ActorAgent, 7)
	if err := env.bus.Publish(env.ctx, ev); err != nil {
		t.Fatal(err)
	}
	f := env.waitFiring(trID, ev.ID)
	if f.Outcome != domain.FiringSucceeded {
		t.Fatalf("outcome = %s (%s), want succeeded", f.Outcome, f.Reason)
	}
	var warnings []string
	if err := json.Unmarshal(f.Warnings, &warnings); err != nil {
		t.Fatal(err)
	}
	if len(warnings) != 1 || !strings.Contains(warnings[0], "unknown path {{pr.nonexistent}}") {
		t.Fatalf("firing warnings = %v, want one naming pr.nonexistent", warnings)
	}
}

// TestEngineUnregisteredAction: with no actions registered (the S26 reality until S28), a
// stored action fires as `errored`, naming the missing ID — logged and side-effect free, never
// silently dropped.
func TestEngineUnregisteredAction(t *testing.T) {
	env := newEngineEnv(t, EngineOptions{}) // Action nil: nothing registered
	pid := env.project("PAY")
	trID := env.trigger(pid, "rule", "pull_request", `["synchronize"]`,
		`{"all":[]}`, `[{"action_id":"run_agent","params":{}}]`, true)

	ev := prEventFor(pid, "synchronize", domain.ActorAgent, 7)
	if err := env.bus.Publish(env.ctx, ev); err != nil {
		t.Fatal(err)
	}
	f := env.waitFiring(trID, ev.ID)
	if f.Outcome != domain.FiringErrored {
		t.Fatalf("outcome = %s, want errored", f.Outcome)
	}
	if !strings.Contains(f.Reason, `action "run_agent" is not registered`) {
		t.Fatalf("reason = %q, want it to name the missing action", f.Reason)
	}
}

// TestEngineEmptyActions: a matching, condition-passing rule with no actions is `no_action`
// with the reason in words.
func TestEngineEmptyActions(t *testing.T) {
	env := newEngineEnv(t, EngineOptions{})
	pid := env.project("PAY")
	trID := env.trigger(pid, "rule", "pull_request", `["synchronize"]`,
		`{"all":[]}`, `[]`, true)

	ev := prEventFor(pid, "synchronize", domain.ActorAgent, 7)
	if err := env.bus.Publish(env.ctx, ev); err != nil {
		t.Fatal(err)
	}
	f := env.waitFiring(trID, ev.ID)
	if f.Outcome != domain.FiringNoAction || f.Reason != "no actions configured" {
		t.Fatalf("outcome = %s (%q), want no_action with words", f.Outcome, f.Reason)
	}
}

// TestEngineGuardVerdict: a guard that terminates the pipeline writes its outcome and reason
// verbatim, and the actions never run — the stage-3 seam S27 fills.
func TestEngineGuardVerdict(t *testing.T) {
	act := &fakeAction{id: "test.run", execute: func(context.Context, ports.ActionContext, json.RawMessage) (ports.ActionResult, error) {
		t.Error("actions ran despite a terminal guard verdict")
		return ports.ActionResult{Outcome: domain.FiringSucceeded}, nil
	}}
	env := newEngineEnv(t, EngineOptions{
		Action: actionRegistry(act),
		Guard: verdictGuard{v: guard.Verdict{
			Outcome: domain.FiringDebounced, Reason: "within the 90s debounce window",
		}},
	})
	pid := env.project("PAY")
	trID := env.trigger(pid, "rule", "pull_request", `["synchronize"]`,
		`{"all":[]}`, `[{"action_id":"test.run","params":{}}]`, true)

	ev := prEventFor(pid, "synchronize", domain.ActorAgent, 7)
	if err := env.bus.Publish(env.ctx, ev); err != nil {
		t.Fatal(err)
	}
	f := env.waitFiring(trID, ev.ID)
	if f.Outcome != domain.FiringDebounced || f.Reason != "within the 90s debounce window" {
		t.Fatalf("outcome = %s (%q), want the guard's verdict verbatim", f.Outcome, f.Reason)
	}
}

type verdictGuard struct{ v guard.Verdict }

func (g verdictGuard) Evaluate(context.Context, guard.Input) guard.Verdict { return g.v }

// TestEngineDisabledTriggerWritesNothing: the engine skips disabled triggers entirely — no
// evaluation, no firing rows.
func TestEngineDisabledTriggerWritesNothing(t *testing.T) {
	act := &fakeAction{id: "test.run", execute: func(context.Context, ports.ActionContext, json.RawMessage) (ports.ActionResult, error) {
		return ports.ActionResult{Outcome: domain.FiringSucceeded}, nil
	}}
	env := newEngineEnv(t, EngineOptions{Action: actionRegistry(act)})
	pid := env.project("PAY")
	disabledID := env.trigger(pid, "disabled rule", "pull_request", `["synchronize"]`,
		`{"all":[]}`, `[{"action_id":"test.run","params":{}}]`, false)
	enabledID := env.trigger(pid, "enabled rule", "pull_request", `["synchronize"]`,
		`{"all":[]}`, `[{"action_id":"test.run","params":{}}]`, true)

	ev := prEventFor(pid, "synchronize", domain.ActorAgent, 7)
	if err := env.bus.Publish(env.ctx, ev); err != nil {
		t.Fatal(err)
	}
	env.waitFiring(enabledID, ev.ID) // the same event fully processed
	if n := env.firingCount(disabledID); n != 0 {
		t.Fatalf("disabled trigger wrote %d firing rows, want 0", n)
	}
}

// TestEnginePerProjectSerialization: events of one project are processed strictly in order
// (a slow action delays the successor), while another project's events proceed concurrently
// rather than queuing behind them.
func TestEnginePerProjectSerialization(t *testing.T) {
	var mu sync.Mutex
	var order []string
	release := make(chan struct{})
	firstSeen := make(chan struct{})
	var once sync.Once

	act := &fakeAction{id: "test.run", execute: func(_ context.Context, ac ports.ActionContext, _ json.RawMessage) (ports.ActionResult, error) {
		if ac.Project.Key == "AAA" {
			once.Do(func() { close(firstSeen) })
			<-release // AAA's worker is stuck until the test releases it
		}
		mu.Lock()
		order = append(order, ac.Event.ID)
		mu.Unlock()
		return ports.ActionResult{Outcome: domain.FiringSucceeded}, nil
	}}
	env := newEngineEnv(t, EngineOptions{Action: actionRegistry(act)})
	pidA := env.project("AAA")
	pidB := env.project("BBB")
	trA := env.trigger(pidA, "rule", "pull_request", `["synchronize"]`,
		`{"all":[]}`, `[{"action_id":"test.run","params":{}}]`, true)
	trB := env.trigger(pidB, "rule", "pull_request", `["synchronize"]`,
		`{"all":[]}`, `[{"action_id":"test.run","params":{}}]`, true)

	a1 := prEventFor(pidA, "synchronize", domain.ActorAgent, 1)
	a2 := prEventFor(pidA, "synchronize", domain.ActorAgent, 2)
	b1 := prEventFor(pidB, "synchronize", domain.ActorAgent, 3)
	for _, ev := range []domain.Event{a1, a2, b1} {
		if err := env.bus.Publish(env.ctx, ev); err != nil {
			t.Fatal(err)
		}
	}

	// AAA's first event is mid-action and blocked; BBB must complete anyway.
	select {
	case <-firstSeen:
	case <-time.After(5 * time.Second):
		t.Fatal("AAA's action never started")
	}
	env.waitFiring(trB, b1.ID) // concurrent: not queued behind AAA's stuck worker
	mu.Lock()
	sawA := stringIn(order, a1.ID) || stringIn(order, a2.ID)
	mu.Unlock()
	if sawA {
		t.Fatal("an AAA event completed while its action was supposed to be blocked")
	}

	close(release)
	env.waitFiring(trA, a1.ID)
	env.waitFiring(trA, a2.ID)
	mu.Lock()
	defer mu.Unlock()
	// order now holds b1 plus a1,a2; a1 must precede a2 — same-project FIFO.
	ia1, ia2 := -1, -1
	for i, id := range order {
		if id == a1.ID {
			ia1 = i
		}
		if id == a2.ID {
			ia2 = i
		}
	}
	if ia1 == -1 || ia2 == -1 || ia1 > ia2 {
		t.Fatalf("same-project order broken: %v (a1=%d a2=%d)", order, ia1, ia2)
	}
}

// TestEngineIgnoresTriggerKind: `trigger` events (the engine's own firing notifications and
// the CRUD surface's mutations) are never evaluated — the self-amplification hole is closed by
// construction.
func TestEngineIgnoresTriggerKind(t *testing.T) {
	env := newEngineEnv(t, EngineOptions{})
	pid := env.project("PAY")
	// A trigger listening on kind "trigger" (unstorable through the API, but the engine must
	// not rely on that).
	trID := env.trigger(pid, "self-feeding", "trigger", `[]`, `{"all":[]}`, `[]`, true)

	tid := trID
	ev := domain.Event{
		ID: domain.NewID(), ProjectID: &pid, Source: "internal",
		Kind: "trigger", ActivityType: "fired", ActorKind: domain.ActorSystem,
		SubjectKind: "trigger", SubjectID: &tid,
		Payload: json.RawMessage(`{}`), DedupeKey: "test:" + domain.NewID(),
		OccurredAt: domain.Now(), CreatedAt: domain.Now(),
	}
	if err := env.bus.Publish(env.ctx, ev); err != nil {
		t.Fatal(err)
	}
	// Process a normal event afterwards to know the engine is alive, then assert nothing fired.
	tr2 := env.trigger(pid, "normal", "pull_request", `[]`, `{"all":[]}`, `[]`, true)
	ev2 := prEventFor(pid, "synchronize", domain.ActorAgent, 1)
	if err := env.bus.Publish(env.ctx, ev2); err != nil {
		t.Fatal(err)
	}
	env.waitFiring(tr2, ev2.ID)
	if n := env.firingCount(trID); n != 0 {
		t.Fatalf("a trigger-kind event produced %d firings, want 0", n)
	}
}
