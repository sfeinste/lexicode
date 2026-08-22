package sched

import (
	"context"
	"errors"
	"log/slog"
	"strconv"
	"sync"

	"github.com/spruce/lexicode/internal/domain"
	"github.com/spruce/lexicode/internal/kernel/ports"
	"github.com/spruce/lexicode/internal/kernel/store"
)

// runSink is the scheduler's ports.RunSink: it persists what the runtime adapter reports,
// rolls up usage into the run and the budget ledger, and turns every append into a bus event
// so SSE subscribers see the stream live.
//
// Sequence mapping (contracts §2.4): the adapter numbers its own launch starting at 1 and
// re-emits a sequence to update it (tool_result merge). The sink allocates the run's persisted
// numbering — provisioning rows, MCP-appended rows and adapter rows interleave — and remembers
// adapter seq → persisted seq for the merges. On reattach a fresh sink starts a fresh map; the
// resumed stream begins at the first unconsumed byte, so nothing is double-persisted.
type runSink struct {
	s     *Scheduler
	ctx   context.Context
	sup   *supervisor
	run   domain.Run
	agent domain.Agent

	mu      sync.Mutex
	seqMap  map[int64]int64
	actions int64
	capped  bool
}

func (s *Scheduler) newRunSink(ctx context.Context, sup *supervisor, run domain.Run, agent domain.Agent) *runSink {
	return &runSink{
		s:      s,
		ctx:    context.WithoutCancel(ctx),
		sup:    sup,
		run:    run,
		agent:  agent,
		seqMap: map[int64]int64{},
	}
}

// Activity implements ports.RunSink.
func (k *runSink) Activity(a domain.Activity) {
	ctx := k.ctx
	a.RunID = k.run.ID
	if a.CreatedAt == "" {
		a.CreatedAt = k.s.now()
	}
	if a.Attempt == 0 {
		a.Attempt = 1
	}

	k.mu.Lock()
	adapterSeq := a.Seq
	persisted, seen := k.seqMap[adapterSeq]
	k.mu.Unlock()

	if seen {
		a.Seq = persisted
		if err := k.s.st.Activities().Update(ctx, &a); err != nil {
			k.s.logger.Error("sched: activity update failed",
				slog.String("run", k.run.ID), slog.String("error", err.Error()))
			return
		}
	} else {
		if err := k.s.st.Activities().AppendNext(ctx, &a); err != nil {
			k.s.logger.Error("sched: activity append failed",
				slog.String("run", k.run.ID), slog.String("error", err.Error()))
			return
		}
		k.mu.Lock()
		k.seqMap[adapterSeq] = a.Seq
		newAction := a.Type == domain.ActivityAction
		if newAction {
			k.actions++
		}
		actions := k.actions
		overCap := newAction && k.agent.MaxSteps > 0 && actions > k.agent.MaxSteps && !k.capped
		if overCap {
			k.capped = true
		}
		k.mu.Unlock()

		if newAction {
			if err := k.s.st.Runs().SetStepCount(ctx, k.run.ID, actions); err != nil {
				k.s.logger.Error("sched: step count write failed",
					slog.String("run", k.run.ID), slog.String("error", err.Error()))
			}
		}
		if overCap {
			// Step cap → failed (§10.4 supervision). The stop reason becomes the outcome.
			k.sup.requestStop(ctx, domain.RunFailed,
				"step cap reached: the agent took more than "+itoa(k.agent.MaxSteps)+" steps")
		}
	}
	k.s.emitRunEvent(ctx, k.run, "activity", map[string]any{"activity": activityBody(a)})
}

// CurrentStep implements ports.RunSink: the mutable one-liner.
func (k *runSink) CurrentStep(step string) {
	if err := k.s.st.Runs().SetCurrentStep(k.ctx, k.run.ID, step); err != nil &&
		!errors.Is(err, store.ErrNotFound) {
		k.s.logger.Error("sched: current step write failed",
			slog.String("run", k.run.ID), slog.String("error", err.Error()))
	}
	k.s.emitRunEvent(k.ctx, k.run, "step", map[string]any{"step": step})
}

// Usage implements ports.RunSink: roll the delta into the run's columns and the day's budget
// ledger cell — which is exactly what admission control reads (§10.2 check 4).
func (k *runSink) Usage(d domain.UsageDelta) {
	if err := k.s.st.Runs().AddUsage(k.ctx, k.run.ID, d); err != nil {
		k.s.logger.Error("sched: usage rollup failed",
			slog.String("run", k.run.ID), slog.String("error", err.Error()))
	}
	trigger := ""
	if k.run.TriggerID != nil {
		trigger = *k.run.TriggerID
	}
	if err := k.s.st.Budget().Add(k.ctx, k.s.day(), k.run.ProjectID, k.run.AgentID,
		trigger, d.CostCents); err != nil {
		k.s.logger.Error("sched: budget ledger write failed",
			slog.String("run", k.run.ID), slog.String("error", err.Error()))
	}
	k.s.emitRunEvent(k.ctx, k.run, "usage", map[string]any{"usage": map[string]any{
		"cost_cents": d.CostCents, "tokens_in": d.TokensIn, "tokens_out": d.TokensOut,
		"tokens_cache_read": d.TokensCacheRead, "tokens_cache_write": d.TokensCacheWrite,
	}})
}

// Elicit implements ports.RunSink for runtimes that surface elicitations in their own stream.
// (Claude Code elicitations arrive through the MCP server instead; this path persists the row
// and parks the run the same way.)
func (k *runSink) Elicit(el domain.Elicitation) error {
	if el.ID == "" {
		el.ID = domain.NewID()
	}
	el.RunID = k.run.ID
	if el.CreatedAt == "" {
		el.CreatedAt = k.s.now()
	}
	if el.State == "" {
		el.State = domain.ElicitationPending
	}
	if err := k.s.st.Elicitations().Create(k.ctx, &el); err != nil {
		return err
	}
	target := domain.RunNeedsInput
	if el.Kind == domain.ElicitationApproval {
		target = domain.RunAwaitingApproval
	}
	if err := k.s.SetRunState(k.ctx, k.run.ID, target, "waiting for an answer"); err != nil {
		k.s.logger.Error("sched: elicitation park failed",
			slog.String("run", k.run.ID), slog.String("error", err.Error()))
	}
	k.s.emitRunEvent(k.ctx, k.run, "elicitation", map[string]any{"elicitation": map[string]any{
		"id": el.ID, "run_id": el.RunID, "kind": string(el.Kind), "state": string(el.State),
	}})
	return nil
}

// Output implements ports.RunSink: record the artifact; a pull request moves the ticket to
// the review column right away.
func (k *runSink) Output(o domain.RunOutput) {
	if o.ID == "" {
		o.ID = domain.NewID()
	}
	o.RunID = k.run.ID
	if o.CreatedAt == "" {
		o.CreatedAt = k.s.now()
	}
	if err := k.s.st.RunOutputs().Append(k.ctx, &o); err != nil {
		k.s.logger.Error("sched: run output write failed",
			slog.String("run", k.run.ID), slog.String("error", err.Error()))
		return
	}
	if o.Kind == domain.OutputPullRequest && k.run.TicketID != nil {
		k.s.moveTicket(k.ctx, *k.run.TicketID, domain.CategoryReview,
			"the run opened a pull request")
	}
}

// Offset implements ports.RunSink: the reattach cursor.
func (k *runSink) Offset(n int64) {
	if err := k.s.st.Runs().SetLogOffset(k.ctx, k.run.ID, n); err != nil {
		k.s.logger.Error("sched: log offset write failed",
			slog.String("run", k.run.ID), slog.String("error", err.Error()))
	}
}

// activityBody is the run.activity frame payload.
func activityBody(a domain.Activity) map[string]any {
	return map[string]any{
		"run_id": a.RunID, "seq": a.Seq, "type": string(a.Type), "level": a.Level,
		"tool_name": a.ToolName, "group_key": a.GroupKey, "title": a.Title,
		"ok": a.OK, "attempt": a.Attempt, "created_at": a.CreatedAt,
	}
}

// ---------------------------------------------------------------- provisioning sink -----

// provisionSink turns Sandbox.Prepare's discrete steps into activity rows the user watches
// fill in (§10.3 — a checklist, never a spinner) and provision.step SSE frames. A step's row
// is updated in place as it moves pending → running → ok/failed; log lines append at the
// verbose level, secret-scrubbed.
type provisionSink struct {
	s   *Scheduler
	run domain.Run
	red *redactor

	mu    sync.Mutex
	steps map[string]int64 // step name → activity seq
}

// Step implements ports.ProvisionSink.
func (p *provisionSink) Step(name string, state ports.StepState, detail string) {
	ctx := context.WithoutCancel(context.Background())
	detail = p.red.clean(detail)
	var ok *bool
	switch state {
	case ports.StepOK:
		t := true
		ok = &t
	case ports.StepFailed:
		f := false
		ok = &f
	}
	a := domain.Activity{
		RunID:     p.run.ID,
		Type:      domain.ActivityProvision,
		Level:     1,
		GroupKey:  "provision",
		Title:     name,
		Payload:   mustJSON(map[string]any{"step": name, "state": string(state), "detail": detail}),
		OK:        ok,
		Attempt:   1,
		CreatedAt: p.s.now(),
	}

	p.mu.Lock()
	seq, seen := p.steps[name]
	p.mu.Unlock()
	if seen {
		a.Seq = seq
		if err := p.s.st.Activities().Update(ctx, &a); err != nil {
			p.s.logger.Error("sched: provision step update failed",
				slog.String("run", p.run.ID), slog.String("error", err.Error()))
		}
	} else {
		if err := p.s.st.Activities().AppendNext(ctx, &a); err != nil {
			p.s.logger.Error("sched: provision step append failed",
				slog.String("run", p.run.ID), slog.String("error", err.Error()))
			return
		}
		p.mu.Lock()
		p.steps[name] = a.Seq
		p.mu.Unlock()
	}
	p.emitStep(ctx, name, string(state), detail)
}

// Log implements ports.ProvisionSink: the verbose stream under the running step.
func (p *provisionSink) Log(line string) {
	ctx := context.WithoutCancel(context.Background())
	line = p.red.clean(line)
	a := domain.Activity{
		RunID:     p.run.ID,
		Type:      domain.ActivityProvision,
		Level:     2,
		GroupKey:  "provision",
		Title:     truncate(line, 200),
		Payload:   mustJSON(map[string]any{"line": line}),
		Attempt:   1,
		CreatedAt: p.s.now(),
	}
	if err := p.s.st.Activities().AppendNext(ctx, &a); err != nil {
		p.s.logger.Error("sched: provision log append failed",
			slog.String("run", p.run.ID), slog.String("error", err.Error()))
	}
}

// emitStep publishes the provision.step frame (topic run:<id>).
func (p *provisionSink) emitStep(ctx context.Context, name, state, detail string) {
	if p.s.bus == nil {
		return
	}
	pid, rid := p.run.ProjectID, p.run.ID
	e := domain.Event{
		ProjectID: &pid, Kind: "provision", ActivityType: "step",
		SubjectKind: "run", SubjectID: &rid,
		ActorKind:  domain.ActorSystem,
		Payload:    mustJSON(map[string]any{"step": map[string]any{"name": name, "state": state, "detail": detail}}),
		OccurredAt: p.s.now(),
	}
	if err := p.s.bus.Emit(ctx, e); err != nil {
		p.s.logger.Error("sched: provision.step emit failed", slog.String("error", err.Error()))
	}
}

func truncate(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n-1]) + "…"
}

func itoa(n int64) string { return strconv.FormatInt(n, 10) }
