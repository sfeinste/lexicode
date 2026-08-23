package guard

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/spruce/lexicode/internal/domain"
	"github.com/spruce/lexicode/internal/kernel/ports"
	"github.com/spruce/lexicode/internal/kernel/store"
)

// LoopStoppedRun is what layer 4 asks the scheduler to record when the depth limit trips: a
// run row created directly in the terminal `loop_stopped` state — no container, no cost —
// because a suppressed run that does not exist cannot be inspected (architecture §9). The
// causal chain itself is not stored on the row: it is reconstructed on demand from
// runs.cause_event_id ↔ events.cause_run_id by GET /runs/{id}/chain, which is why the run
// must carry the trigger, the cause event, the subject and the depth.
type LoopStoppedRun struct {
	ProjectID    string
	AgentID      string
	TriggerID    string
	CauseEventID string
	SubjectKey   string
	Depth        int64
	Reason       string
}

// LoopStopper is the guard's seam to the scheduler — the only writer of runs.state (data
// model invariant 4), which is why the guard cannot insert the row itself.
type LoopStopper interface {
	EnqueueLoopStopped(ctx context.Context, req LoopStoppedRun) (domain.Run, error)
}

// Options configures New.
type Options struct {
	// Store is the open, migrated database. Required.
	Store *store.Store
	// Loop creates the terminal loop-stopped run row — the scheduler in production. Nil
	// (tests, or a wiring window before the scheduler exists) records the loop_stopped
	// firing without a run row.
	Loop LoopStopper
	// Logger receives layer decisions. Nil means slog.Default().
	Logger *slog.Logger
	// Now overrides the clock in tests. Nil means time.Now.
	Now func() time.Time
}

// Layers is the real stage-3 evaluator: the skip-token escape hatch, then the five layers of
// architecture §9 in order, each defaulting on, each with its own outcome class. All state it
// consults is in the database — recent runs, the event graph, the budget ledger — so every
// window and counter survives a restart.
type Layers struct {
	st     *store.Store
	loop   LoopStopper
	logger *slog.Logger
	now    func() time.Time
}

// New builds the evaluator.
func New(opts Options) *Layers {
	logger := opts.Logger
	if logger == nil {
		logger = slog.Default()
	}
	now := opts.Now
	if now == nil {
		now = time.Now
	}
	return &Layers{st: opts.Store, loop: opts.Loop, logger: logger, now: now}
}

// config is triggers.loop_config (data model §6.1), merged over the defaults: a field absent
// from the stored JSON keeps its default, a field explicitly false/0/null is respected.
type config struct {
	ActorSuppression bool   `json:"actor_suppression"`
	DebounceSeconds  int64  `json:"debounce_seconds"`
	CancelInProgress bool   `json:"cancel_in_progress"`
	DepthLimit       int64  `json:"depth_limit"`
	DailyBudgetCents *int64 `json:"daily_budget_cents"`
}

// loadConfig merges a trigger's stored loop_config over domain.DefaultLoopConfig. There is no
// separate project-level loop-config row in the schema; the project's contribution is its
// daily budget ceiling, which the budget layer reads directly (a null rule budget inherits
// it, per §6.1).
func loadConfig(raw json.RawMessage) config {
	var cfg config
	_ = json.Unmarshal(domain.DefaultLoopConfig(), &cfg)
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &cfg); err != nil {
			// A malformed stored config falls back to the defaults whole — protection
			// must not switch off because a row is corrupt.
			cfg = config{}
			_ = json.Unmarshal(domain.DefaultLoopConfig(), &cfg)
		}
	}
	return cfg
}

// stop is a terminated verdict.
func stop(outcome domain.FiringOutcome, reason string) Verdict {
	return Verdict{Proceed: false, Outcome: outcome, Reason: reason}
}

// eventCheckSuite is the normalized event kind for a CI result (the github.poll catalog's
// "check_suite"; the string is the contract between the source's catalog and this layer).
const eventCheckSuite = "check_suite"

// exemptFromActorSuppression carves the one hole layer 1 must have.
//
// Actor suppression exists so an agent's own action cannot re-trigger the rule that runs it —
// "Dev pushed" must not fire "on a push, run Dev". It keys on the event's attributed actor,
// and the poller attributes a check suite by the branch it ran on, which is the branch of the
// agent whose work CI is judging. So the brief's own step 6, "CI failed → Dev fixes it", is
// suppressed by layer 1 on every branch Dev owns: the rule can never fire, and the only way
// to use it was to switch actor suppression off for that rule — which also switches it off
// for the loop the layer is there to stop.
//
// A check suite is not the agent acting. It is a machine's verdict ABOUT the agent's work,
// produced by CI, arriving after the agent's run has ended. Nothing an agent does in response
// can loop through layer 1 in the way layer 1 guards against, because the agent cannot emit a
// check_suite event — only pushing can, and the push events stay suppressed. The runaway case
// that remains (fix → CI fails again → fix → …) is real, and it is layers 2, 4 and 5's job:
// debounce, the depth counter and the budget all still apply to this event, unchanged.
//
// Scope: attribution kind only. This does not weaken the skip token, the depth reset, or any
// other layer, and it applies to check_suite alone — a pull_request or review event
// attributed to the agent is still suppressed.
func exemptFromActorSuppression(ev domain.Event) bool {
	return ev.Kind == eventCheckSuite
}

// Evaluate runs the escape hatch and the five layers in order. Any internal error fails
// closed for read errors on the layers that suppress (the event is let through — a broken
// database must not silently mute every trigger) and is logged; the depth layer fails safe
// (an unwalkable chain does not stop the run, it logs).
func (l *Layers) Evaluate(ctx context.Context, in Input) Verdict {
	cfg := loadConfig(in.Trigger.LoopConfig)

	// Escape hatch: a skip token anywhere in the PR body, the triggering comment or review
	// body, or the head commit message short-circuits everything (architecture §9).
	if hasSkipToken(in.Payload) {
		return stop(domain.FiringNoAction, "skip token")
	}

	// Layer 1 — actor suppression: the event's resolved actor (S25 attribution, D-9) is the
	// very agent this rule would run. Keyed on actor_id, which the poller sets to the agent
	// row's ID on any attribution hit.
	if cfg.ActorSuppression && !exemptFromActorSuppression(in.Event) &&
		in.Event.ActorKind == domain.ActorAgent && in.Event.ActorID != nil {
		for _, id := range in.RunAgentIDs {
			if id == *in.Event.ActorID {
				return stop(domain.FiringNoAction, "actor suppressed")
			}
		}
	}

	// Layer 2a — review coalescing: one review is one run. A human review arrives as a
	// summary event plus one event per inline comment, and every one of them would fire the
	// rule; since the run they cause is given the WHOLE review (the `review` context
	// provider), the fragments after the first have nothing left to add and must not each
	// start their own run. Keyed on the review id rather than on time, because the fragments
	// can straddle any window: two poll ticks, a slow listing, a restart.
	//
	// It rides on debounce_seconds rather than carrying a switch of its own: a rule with
	// debounce off has asked for one run per event, and this is the review-shaped form of
	// the same decision.
	if cfg.DebounceSeconds > 0 {
		if v, absorbed := l.coalesceReview(ctx, in); absorbed {
			return v
		}
	}

	// Layer 2 — debounce: a run already started for this (trigger, subject) inside the
	// window absorbs the firing. A database probe, not a timer: restart-safe.
	if cfg.DebounceSeconds > 0 {
		cutoff := domain.FormatTime(l.now().Add(-time.Duration(cfg.DebounceSeconds) * time.Second))
		run, err := l.st.Runs().LatestOnSubject(ctx, in.Trigger.ID, in.SubjectKey, cutoff)
		switch {
		case err == nil:
			id := run.ID
			return Verdict{
				Proceed: false, Outcome: domain.FiringDebounced,
				Reason: fmt.Sprintf("debounced: within %ds of run #%d on %s",
					cfg.DebounceSeconds, run.Seq, in.SubjectKey),
				AbsorbedByRunID: &id,
			}
		case !errors.Is(err, store.ErrNotFound):
			l.logger.Error("guard: debounce probe failed", slog.String("error", err.Error()))
		}
	}

	pass := ports.GuardPass{SubjectKey: in.SubjectKey}

	// Layer 3 — cancel in progress: a still-active run for this (trigger, subject) will be
	// superseded. The guard only elects it here; the scheduler cancels it *after* the new
	// run exists, because the cancellation reason names the new run's seq ("superseded by
	// run #N"). Cancel-then-admit ordering holds because Enqueue stops the old run before
	// waking admission for the new one.
	if cfg.CancelInProgress {
		active, err := l.st.Runs().ActiveOnSubject(ctx, in.Trigger.ID, in.SubjectKey)
		switch {
		case err == nil:
			pass.SupersededRunID = active.ID
		case !errors.Is(err, store.ErrNotFound):
			l.logger.Error("guard: cancel-in-progress probe failed", slog.String("error", err.Error()))
		}
	}

	// Layer 4 — depth counter: walk run.cause_event_id → event.cause_run_id upward, counting
	// agent-caused hops on the same subject, with the human-action reset (§9).
	if cfg.DepthLimit > 0 {
		depth, err := l.chainDepth(ctx, in.Event, in.SubjectKey)
		if err != nil {
			l.logger.Error("guard: depth walk failed; letting the firing through",
				slog.String("event", in.Event.ID), slog.String("error", err.Error()))
		} else {
			pass.Depth = depth
			if depth >= cfg.DepthLimit {
				return l.stopLoop(ctx, in, depth, cfg.DepthLimit)
			}
		}
	}

	// Layer 5 — budget: project/day, agent/day, rule/day against the ledger.
	if v, stopped := l.checkBudget(ctx, in, cfg); stopped {
		return v
	}

	return Verdict{Proceed: true, Pass: pass}
}

// coalesceReview is layer 2a: if some run of this trigger was already caused by an event
// belonging to the same review, this firing is one more fragment of a review that run already
// has. absorbed is false when the event names no review (everything that is not a review or a
// review comment) or when nothing has run for it yet.
func (l *Layers) coalesceReview(ctx context.Context, in Input) (Verdict, bool) {
	reviewID := reviewIDOf(in.Payload)
	// The probe is scoped to the event's own subject, so an event that names no pull request
	// (nothing the poller emits) simply falls through to the ordinary layers.
	if reviewID == "" || in.Event.ProjectID == nil || in.Event.SubjectNumber == nil {
		return Verdict{}, false
	}
	run, err := l.st.Runs().LatestForReview(ctx, in.Trigger.ID, *in.Event.ProjectID,
		in.Event.SubjectKind, *in.Event.SubjectNumber, reviewID)
	if err != nil {
		if !errors.Is(err, store.ErrNotFound) {
			l.logger.Error("guard: review coalescing probe failed", slog.String("error", err.Error()))
		}
		return Verdict{}, false
	}
	id := run.ID
	return Verdict{
		Proceed: false, Outcome: domain.FiringDebounced,
		Reason: fmt.Sprintf("debounced: review %s was already given to run #%d in full",
			reviewID, run.Seq),
		AbsorbedByRunID: &id,
	}, true
}

// reviewIDOf reads the review an event belongs to out of its normalized payload: review.id on
// a review submission, comment.review_id on an inline comment of one. "" means neither — a
// conversation comment, a push, a check suite, or a lone inline comment the forge grouped into
// no review.
func reviewIDOf(payload map[string]any) string {
	for _, path := range [][]string{{"review", "id"}, {"comment", "review_id"}} {
		v, ok := lookup(payload, path)
		if !ok {
			continue
		}
		if s, ok := v.(string); ok && strings.TrimSpace(s) != "" {
			return s
		}
	}
	return ""
}

// stopLoop is layer 4's trip: create the terminal loop-stopped run row (never suppress — the
// loop chain view needs a row to hang the explanation on) and terminate the firing.
func (l *Layers) stopLoop(ctx context.Context, in Input, depth, limit int64) Verdict {
	reason := fmt.Sprintf("loop stopped: depth %d reached the limit of %d on %s", depth, limit, in.SubjectKey)
	v := stop(domain.FiringLoopStopped, reason)
	if l.loop == nil || len(in.RunAgentIDs) == 0 {
		// No scheduler seam, or the trigger starts no run: the firing still records the
		// stop, there is just no run row to point at.
		return v
	}
	run, err := l.loop.EnqueueLoopStopped(ctx, LoopStoppedRun{
		ProjectID:    in.Trigger.ProjectID,
		AgentID:      in.RunAgentIDs[0],
		TriggerID:    in.Trigger.ID,
		CauseEventID: in.Event.ID,
		SubjectKey:   in.SubjectKey,
		Depth:        depth,
		Reason:       reason,
	})
	if err != nil {
		l.logger.Error("guard: loop-stopped run creation failed",
			slog.String("trigger", in.Trigger.ID), slog.String("error", err.Error()))
		return v
	}
	id := run.ID
	v.RunID = &id
	return v
}

// chainDepth walks the causal chain upward from the triggering event: event.cause_run_id →
// run.cause_event_id → …, counting hops where the causing run is on the same subject. Two
// things break the walk as "the chain reset here":
//
//   - a human action on the subject (a human comment, a human push) newer than the hop being
//     considered — probed once against the events table;
//   - a hop whose run a human requested directly (requested_by_user_id set: a manual run).
//
// A subject change also breaks the walk: the chain the depth limit protects against is the
// one bouncing on a single PR or ticket.
func (l *Layers) chainDepth(ctx context.Context, ev domain.Event, subject string) (int64, error) {
	projectID := ""
	if ev.ProjectID != nil {
		projectID = *ev.ProjectID
	}
	resetAt, err := l.st.Events().NewestHumanActionAt(ctx, projectID, ev.SubjectKind, ev.SubjectNumber, ev.SubjectID)
	if err != nil {
		return 0, err
	}

	var depth int64
	cur := ev
	for hops := 0; hops < 64; hops++ { // hard cap: a cycle in the graph must not hang the pipeline
		if resetAt != "" && cur.OccurredAt <= resetAt {
			break // a human acted on the subject at or after this hop: the chain resets
		}
		if cur.CauseRunID == nil {
			break // chain root: the event was not caused by a run
		}
		run, err := l.st.Runs().ByID(ctx, *cur.CauseRunID)
		if err != nil {
			return 0, err
		}
		if run.RequestedByUserID != nil {
			break // a manual, human-requested run resets the chain (§9)
		}
		if run.SubjectKey != "" && run.SubjectKey != subject {
			break // the chain left this subject
		}
		depth++
		if run.CauseEventID == nil {
			break
		}
		cur, err = l.st.Events().ByID(ctx, *run.CauseEventID)
		if err != nil {
			return 0, err
		}
	}
	return depth, nil
}

// checkBudget is layer 5: three scopes against budget_ledger, cheapest ceiling first in the
// order the architecture lists them — project/day (the project ceiling, defaulting to the
// workspace ceiling), agent/day (the would-run agent's cap), rule/day (the trigger's
// loop_config cap; null inherits the project ceiling, which the project scope already
// enforces).
func (l *Layers) checkBudget(ctx context.Context, in Input, cfg config) (Verdict, bool) {
	day := domain.Day(l.now())

	project, err := l.st.Projects().ByID(ctx, in.Trigger.ProjectID)
	if err != nil {
		l.logger.Error("guard: budget project read failed", slog.String("error", err.Error()))
		return Verdict{}, false
	}
	ceiling := int64(0)
	if ws, err := l.st.Workspace().Get(ctx); err == nil {
		ceiling = ws.DefaultDailyBudgetCents
	}
	if project.DailyBudgetCents != nil {
		ceiling = *project.DailyBudgetCents
	}
	if ceiling > 0 {
		spent, err := l.st.Budget().ProjectDay(ctx, day, project.ID)
		if err != nil {
			l.logger.Error("guard: budget project-day read failed", slog.String("error", err.Error()))
		} else if spent >= ceiling {
			return stop(domain.FiringBudgetExceeded, fmt.Sprintf(
				"budget exceeded: project %s has spent %s of its %s daily budget",
				project.Name, cents(spent), cents(ceiling))), true
		}
	}

	for _, agentID := range in.RunAgentIDs {
		agent, err := l.st.Agents().ByID(ctx, agentID)
		if err != nil || agent.DailyCapCents == nil || *agent.DailyCapCents <= 0 {
			continue
		}
		spent, err := l.st.Budget().AgentDay(ctx, day, agentID)
		if err != nil {
			l.logger.Error("guard: budget agent-day read failed", slog.String("error", err.Error()))
			continue
		}
		if spent >= *agent.DailyCapCents {
			return stop(domain.FiringBudgetExceeded, fmt.Sprintf(
				"budget exceeded: agent %s has spent %s of its %s daily cap",
				agent.Name, cents(spent), cents(*agent.DailyCapCents))), true
		}
	}

	if cfg.DailyBudgetCents != nil && *cfg.DailyBudgetCents > 0 {
		spent, err := l.st.Budget().TriggerDay(ctx, day, in.Trigger.ID)
		if err != nil {
			l.logger.Error("guard: budget rule-day read failed", slog.String("error", err.Error()))
		} else if spent >= *cfg.DailyBudgetCents {
			return stop(domain.FiringBudgetExceeded, fmt.Sprintf(
				"budget exceeded: this rule has spent %s of its %s daily cap",
				cents(spent), cents(*cfg.DailyBudgetCents))), true
		}
	}

	return Verdict{}, false
}

// cents renders integer cents as dollars for reasons in words.
func cents(c int64) string {
	return fmt.Sprintf("$%d.%02d", c/100, c%100)
}

// skipTokens are the escape-hatch spellings (architecture §9). The scan is case-insensitive;
// "skip-agent" is a substring of "skip-agents", so both spellings hit on one token, but all
// three are listed because the list is the documentation.
var skipTokens = []string{"skip-agents", "skip-agent", "[skip agents]"}

// skipPaths are where a skip token counts: the PR body, the triggering comment or review
// body, and the head commit message (which rides the synchronize payload as
// pr.head_commit_message).
var skipPaths = [][]string{
	{"pr", "body"},
	{"comment", "body"},
	{"review", "body"},
	{"pr", "head_commit_message"},
}

// hasSkipToken scans the payload's skip-carrying fields.
func hasSkipToken(payload map[string]any) bool {
	for _, path := range skipPaths {
		v, ok := lookup(payload, path)
		if !ok {
			continue
		}
		s, ok := v.(string)
		if !ok {
			continue
		}
		low := strings.ToLower(s)
		for _, tok := range skipTokens {
			if strings.Contains(low, tok) {
				return true
			}
		}
	}
	return false
}

// lookup walks a nested map path.
func lookup(m map[string]any, path []string) (any, bool) {
	var cur any = m
	for _, key := range path {
		obj, ok := cur.(map[string]any)
		if !ok {
			return nil, false
		}
		cur, ok = obj[key]
		if !ok {
			return nil, false
		}
	}
	return cur, true
}
