package triggers

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"sync"

	"github.com/spruce/lexicode/internal/domain"
	"github.com/spruce/lexicode/internal/kernel/bus"
	"github.com/spruce/lexicode/internal/kernel/guard"
	"github.com/spruce/lexicode/internal/kernel/ports"
	"github.com/spruce/lexicode/internal/kernel/store"
)

// Engine is the four-stage trigger pipeline (architecture §8): match → conditions → guard →
// actions. One bus subscription feeds it every event kind; internally it serializes by project
// — one worker goroutine and one FIFO queue per project — so ordering within a project is
// deterministic while different projects proceed concurrently. (The bus runs each subscriber
// on a single goroutine, so a handler that processed inline would serialize every project
// behind every other; the per-project fan-out is what buys cross-project concurrency.)
//
// Delivery semantics: the bus handler acks once the event is queued to its project worker, so
// dispatch_state can read 'done' moments before the pipeline runs. A crash inside that window
// loses the queued evaluation — accepted for V1, because the alternative (acking after
// processing) either serializes all projects or needs a second durable queue. Everything the
// pipeline writes is idempotent on (trigger_id, event_id), so boot recovery's re-deliveries
// are always safe.
//
// Every terminal outcome of stages 2–4 writes a trigger_firings row — including `no_action`,
// with the reason in words. Stage 1 non-matches write nothing: they are not firings. Disabled
// triggers are invisible to the engine entirely (TriggersRepo.Enabled) and write no rows.
//
// Stage 3 is guard.Pass until S27 lands the five loop-protection layers. Stage 4 executes
// TriggerActions from the kernel registry; until S28 registers the five verbs, a stored
// action's ID resolves to nothing and the firing is `errored` naming the missing action — the
// pipeline logs and side-effects nothing.
type Engine struct {
	st     *store.Store
	bus    *bus.Bus
	guard  guard.Evaluator
	action func(id string) (ports.TriggerAction, error)
	kernel any
	logger *slog.Logger
	now    func() string
	qsize  int

	ctx    context.Context
	cancel context.CancelFunc

	mu      sync.Mutex
	workers map[string]chan domain.Event
	stopped bool
	wg      sync.WaitGroup
}

// EngineOptions configures NewEngine.
type EngineOptions struct {
	// Store is the open, migrated database. Required.
	Store *store.Store
	// Bus emits `trigger.fired` events for the SSE surface. Nil (tests) skips emission.
	Bus *bus.Bus
	// Guard is stage 3. Nil means guard.Pass — the documented S26 pass-through that S27
	// replaces with the five loop-protection layers.
	Guard guard.Evaluator
	// Action resolves a stored action_id to the registered TriggerAction — kernel.Action in
	// production. Nil means no actions are registered: every stored action fires as `errored`
	// naming the missing ID.
	Action func(id string) (ports.TriggerAction, error)
	// Kernel is placed on ActionContext.Kernel verbatim (contracts §2.5; see ports.ActionContext
	// for why the field is `any`). Nil is fine until S28's actions need it.
	Kernel any
	// Logger receives pipeline lines. Nil means slog.Default().
	Logger *slog.Logger
	// Now overrides the clock in tests. Nil means domain.Now.
	Now func() string
	// QueueSize is each project's queue depth. Zero means 256. A full queue blocks the bus
	// subscriber (never drops), exactly like the bus's own backpressure.
	QueueSize int
}

// NewEngine builds an engine. Call Subscribe before the bus starts, and Stop on shutdown.
func NewEngine(opts EngineOptions) *Engine {
	logger := opts.Logger
	if logger == nil {
		logger = slog.Default()
	}
	now := opts.Now
	if now == nil {
		now = domain.Now
	}
	g := opts.Guard
	if g == nil {
		g = guard.Pass{}
	}
	act := opts.Action
	if act == nil {
		act = func(id string) (ports.TriggerAction, error) {
			return nil, fmt.Errorf("no trigger actions are registered")
		}
	}
	qsize := opts.QueueSize
	if qsize <= 0 {
		qsize = 256
	}
	ctx, cancel := context.WithCancel(context.Background())
	return &Engine{
		st: opts.Store, bus: opts.Bus, guard: g, action: act, kernel: opts.Kernel,
		logger: logger, now: now, qsize: qsize,
		ctx: ctx, cancel: cancel,
		workers: make(map[string]chan domain.Event),
	}
}

// Subscribe registers the engine on the bus. One subscription covers every kind and activity
// type — external and internal alike; stage 1 discards what no trigger listens for. Call it
// before bus.Start so boot recovery reaches the engine too.
func (e *Engine) Subscribe(b *bus.Bus) error {
	return b.SubscribeTopic("trigger-engine", "*", e.handle)
}

// errEngineStopped fails deliveries that arrive after Stop, so their dispatch is never marked
// a false 'done'.
var errEngineStopped = errors.New("triggers: engine is stopped")

// handle is the bus handler: route the event to its project's worker and ack. Events without a
// project have no triggers to evaluate; `trigger` kind events are the engine's own firing
// notifications and are never themselves triggerable — evaluating them would let a rule
// amplify its own output into a loop no unique index could stop.
func (e *Engine) handle(_ context.Context, ev domain.Event) error {
	if ev.ProjectID == nil || *ev.ProjectID == "" || ev.Kind == "trigger" {
		return nil
	}
	pid := *ev.ProjectID

	e.mu.Lock()
	if e.stopped {
		e.mu.Unlock()
		return errEngineStopped
	}
	ch, ok := e.workers[pid]
	if !ok {
		ch = make(chan domain.Event, e.qsize)
		e.workers[pid] = ch
		e.wg.Add(1)
		go e.work(pid, ch)
	}
	e.mu.Unlock()

	select {
	case ch <- ev:
		return nil
	case <-e.ctx.Done():
		return errEngineStopped
	}
}

// Stop halts the workers. Queued-but-unprocessed events are abandoned (see the delivery
// semantics in the type comment); everything already written stays written.
func (e *Engine) Stop(ctx context.Context) error {
	e.mu.Lock()
	if e.stopped {
		e.mu.Unlock()
		return nil
	}
	e.stopped = true
	e.mu.Unlock()

	e.cancel()
	done := make(chan struct{})
	go func() {
		e.wg.Wait()
		close(done)
	}()
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return fmt.Errorf("triggers: workers did not stop before the deadline: %w", ctx.Err())
	}
}

// work is one project's worker: strictly FIFO, one event fully processed before the next.
func (e *Engine) work(projectID string, ch chan domain.Event) {
	defer e.wg.Done()
	for {
		select {
		case <-e.ctx.Done():
			return
		case ev := <-ch:
			e.process(e.ctx, ev)
		}
	}
}

// process evaluates one event against every enabled trigger of its project, in trigger
// creation order.
func (e *Engine) process(ctx context.Context, ev domain.Event) {
	trs, err := e.st.Triggers().Enabled(ctx, *ev.ProjectID)
	if err != nil {
		e.logger.Error("triggers: could not load enabled triggers; event skipped",
			slog.String("event", ev.ID), slog.String("error", err.Error()))
		return
	}
	if len(trs) == 0 {
		return
	}
	payload := parsePayload(ev.Payload)
	for _, tr := range trs {
		e.fire(ctx, tr, ev, payload)
	}
}

// fire runs the four stages for one (trigger, event) pair.
func (e *Engine) fire(ctx context.Context, tr domain.Trigger, ev domain.Event, payload map[string]any) {
	// Stage 1: match. A non-match writes nothing at all.
	if !matchStage(tr, ev, payload) {
		return
	}

	// Idempotency pre-flight: a re-dispatched event must not re-execute actions. Within one
	// process the per-project worker serializes evaluations, so check-then-act cannot race;
	// the unique index underneath Create covers everything else.
	exists, err := e.st.Firings().Exists(ctx, tr.ID, ev.ID)
	if err != nil {
		e.logger.Error("triggers: firing pre-flight failed",
			slog.String("trigger", tr.ID), slog.String("event", ev.ID),
			slog.String("error", err.Error()))
		return
	}
	if exists {
		e.logger.Debug("triggers: event already fired for this trigger; skipping",
			slog.String("trigger", tr.ID), slog.String("event", ev.ID))
		return
	}

	// Stage 2: conditions — total evaluation over the normalized payload.
	if !evalConditions(tr.Conditions, payload) {
		e.writeFiring(ctx, tr, ev, outcomeOf(domain.FiringNoAction, "conditions not met"), nil)
		return
	}

	// Stage 3: guard (S27; guard.Pass until then).
	v := e.guard.Evaluate(ctx, guard.Input{Event: ev, Trigger: tr, SubjectKey: subjectKey(ev)})
	if !v.Proceed {
		e.writeFiring(ctx, tr, ev, firingOutcome{
			Outcome: v.Outcome, Reason: v.Reason, AbsorbedByRunID: v.AbsorbedByRunID,
		}, nil)
		return
	}

	// Stage 4: actions, in stored order, through the registry.
	res, warnings := e.runActions(ctx, tr, ev, payload)
	e.writeFiring(ctx, tr, ev, res, warnings)
}

// firingOutcome is what a terminated pipeline hands to writeFiring.
type firingOutcome struct {
	Outcome         domain.FiringOutcome
	Reason          string
	RunID           *string
	AbsorbedByRunID *string
}

func outcomeOf(o domain.FiringOutcome, reason string) firingOutcome {
	return firingOutcome{Outcome: o, Reason: reason}
}

// actionRef is one element of triggers.actions: ordered [{action_id, params}].
type actionRef struct {
	ActionID string          `json:"action_id"`
	Params   json.RawMessage `json:"params"`
}

// runActions executes the trigger's actions in order. The first errored or awaiting_approval
// result stops the walk and becomes the firing's outcome; otherwise the firing succeeds when
// any action succeeded, and is `no_action` in words when none had anything to do. Interpolation
// warnings from every executed action are collected for the firing row.
func (e *Engine) runActions(ctx context.Context, tr domain.Trigger, ev domain.Event, payload map[string]any) (firingOutcome, []string) {
	var refs []actionRef
	if err := json.Unmarshal(tr.Actions, &refs); err != nil {
		return outcomeOf(domain.FiringErrored, "stored actions could not be parsed: "+err.Error()), nil
	}
	if len(refs) == 0 {
		return outcomeOf(domain.FiringNoAction, "no actions configured"), nil
	}

	project, err := e.st.Projects().ByID(ctx, tr.ProjectID)
	if err != nil {
		return outcomeOf(domain.FiringErrored, "project could not be loaded: "+err.Error()), nil
	}

	var warnings []string
	ac := ports.ActionContext{
		Event: ev, Trigger: tr, Project: project, Kernel: e.kernel,
		Interp: func(tmpl string) (string, []string) {
			s, w := interpolate(tmpl, payload)
			warnings = append(warnings, w...)
			return s, w
		},
	}

	var notes []string
	var runID *string
	succeeded := false
	for _, ref := range refs {
		act, err := e.action(ref.ActionID)
		if err != nil {
			return firingOutcome{
				Outcome: domain.FiringErrored,
				Reason:  fmt.Sprintf("action %q is not registered; nothing was executed for it", ref.ActionID),
				RunID:   runID,
			}, warnings
		}
		res, err := act.Execute(ctx, ac, ref.Params)
		if err != nil {
			return firingOutcome{
				Outcome: domain.FiringErrored,
				Reason:  fmt.Sprintf("action %q failed: %s", ref.ActionID, err.Error()),
				RunID:   runID,
			}, warnings
		}
		if res.RunID != "" {
			id := res.RunID
			runID = &id
		}
		if res.Note != "" {
			notes = append(notes, res.Note)
		}
		switch res.Outcome {
		case domain.FiringSucceeded:
			succeeded = true
		case domain.FiringNoAction:
			// Nothing to do for this action; the walk continues.
		case domain.FiringAwaitingApproval, domain.FiringErrored:
			reason := strings.Join(notes, "; ")
			if reason == "" {
				reason = fmt.Sprintf("action %q ended %s", ref.ActionID, res.Outcome)
			}
			return firingOutcome{Outcome: res.Outcome, Reason: reason, RunID: runID}, warnings
		default:
			return firingOutcome{
				Outcome: domain.FiringErrored,
				Reason:  fmt.Sprintf("action %q returned unknown outcome %q", ref.ActionID, res.Outcome),
				RunID:   runID,
			}, warnings
		}
	}

	reason := strings.Join(notes, "; ")
	if succeeded {
		return firingOutcome{Outcome: domain.FiringSucceeded, Reason: reason, RunID: runID}, warnings
	}
	if reason == "" {
		reason = "every action had nothing to do"
	}
	return firingOutcome{Outcome: domain.FiringNoAction, Reason: reason, RunID: runID}, warnings
}

// writeFiring records the terminal outcome — every terminal outcome, including the ones that
// did nothing — and emits the `trigger.fired` bus event the SSE surface renders live.
func (e *Engine) writeFiring(ctx context.Context, tr domain.Trigger, ev domain.Event, res firingOutcome, warnings []string) {
	if warnings == nil {
		warnings = []string{}
	}
	wjson, err := json.Marshal(warnings)
	if err != nil {
		wjson = []byte("[]")
	}
	f := domain.TriggerFiring{
		ID: domain.NewID(), TriggerID: tr.ID, EventID: ev.ID,
		Outcome: res.Outcome, Reason: res.Reason,
		RunID: res.RunID, AbsorbedByRunID: res.AbsorbedByRunID,
		Warnings: wjson, CreatedAt: e.now(),
	}
	inserted, err := e.st.Firings().Create(ctx, &f)
	if err != nil {
		e.logger.Error("triggers: could not record firing",
			slog.String("trigger", tr.ID), slog.String("event", ev.ID),
			slog.String("outcome", string(res.Outcome)), slog.String("error", err.Error()))
		return
	}
	if !inserted {
		// A concurrent recording won the unique index; theirs is the firing of record.
		return
	}
	e.logger.Info("triggers: fired",
		slog.String("trigger", tr.ID), slog.String("trigger_name", tr.Name),
		slog.String("event", ev.ID), slog.String("outcome", string(res.Outcome)),
		slog.String("reason", res.Reason))
	e.emitFired(ctx, tr, ev, f)
}

// emitFired publishes the `trigger.fired` internal event (contracts §5.1's SSE type).
// Best-effort: the firing row is already the record. The engine itself never evaluates
// `trigger` kind events, so this cannot feed back into the pipeline.
func (e *Engine) emitFired(ctx context.Context, tr domain.Trigger, ev domain.Event, f domain.TriggerFiring) {
	if e.bus == nil {
		return
	}
	payload, err := json.Marshal(map[string]any{
		"trigger": map[string]any{"id": tr.ID, "name": tr.Name},
		"firing": map[string]any{
			"id": f.ID, "event_id": f.EventID, "outcome": f.Outcome,
			"reason": f.Reason, "run_id": f.RunID, "created_at": f.CreatedAt,
		},
	})
	if err != nil {
		return
	}
	pid, tid := tr.ProjectID, tr.ID
	out := domain.Event{
		ProjectID: &pid, Kind: "trigger", ActivityType: "fired",
		ActorKind: domain.ActorSystem, SubjectKind: "trigger", SubjectID: &tid,
		Payload: payload, OccurredAt: e.now(),
		// Deterministic per firing: a re-emission after a partial failure collapses onto the
		// first attempt instead of minting a second occurrence.
		DedupeKey: "trigger.fired:" + f.ID,
	}
	if err := e.bus.Emit(ctx, out); err != nil && !errors.Is(err, bus.ErrDuplicate) {
		e.logger.Error("triggers: emit trigger.fired failed",
			slog.String("firing", f.ID), slog.String("error", err.Error()))
	}
}

// subjectKey is the loop-protection subject in words: "pr:219" / "ticket:PAY-14" / "repo"
// (architecture §9). S27 owns the real derivation from the event descriptor's template; this
// stands in with the event's subject columns, which agree with the descriptors S25 ships.
func subjectKey(ev domain.Event) string {
	switch {
	case ev.SubjectKind == "" || ev.SubjectKind == "repo":
		return "repo"
	case ev.SubjectNumber != nil:
		return ev.SubjectKind + ":" + strconv.FormatInt(*ev.SubjectNumber, 10)
	case ev.SubjectID != nil:
		return ev.SubjectKind + ":" + *ev.SubjectID
	default:
		return ev.SubjectKind
	}
}

// parsePayload decodes the normalized payload; a malformed one — impossible for events our own
// sources emitted — evaluates like an empty payload, all paths nil.
func parsePayload(raw json.RawMessage) map[string]any {
	out := map[string]any{}
	if len(raw) > 0 {
		_ = json.Unmarshal(raw, &out)
	}
	return out
}
