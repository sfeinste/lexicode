// Package guard is stage 3 of the trigger pipeline: the five loop-protection layers and the
// budget ledger (architecture §9, story S27).
//
// S26 defined the stage's interface and shipped Pass, a pass-through evaluator, so the
// engine's four-stage shape was real before the layers existed. S27 adds Layers, the full
// evaluator — skip-token escape hatch, actor suppression, debounce, cancel-in-progress, the
// depth counter with the human-action reset, and budget checks — without the engine changing:
// every layer speaks through Verdict.
package guard

import (
	"context"

	"github.com/spruce/lexicode/internal/domain"
	"github.com/spruce/lexicode/internal/kernel/ports"
)

// Input is what stage 3 evaluates: the event, the trigger it matched, the loop-protection
// subject key derived from the event descriptor's template ("pr:219" / "ticket:PAY-14" /
// "repo"), the parsed normalized payload (for the skip-token scan), and the IDs of the agents
// this trigger would act as (for actor suppression and the budget's agent/day scope). The
// engine owns the derivation of all four.
type Input struct {
	Event      domain.Event
	Trigger    domain.Trigger
	SubjectKey string
	// Payload is the event's normalized payload, parsed. The guard scans it for skip tokens
	// (pr.body, comment.body, review.body, pr.head_commit_message); nil is legal and means
	// nothing to scan.
	Payload map[string]any
	// RunAgentIDs are the agents the trigger would act as, in action order: the agents its
	// run_agent actions would run, plus the acting agents of its post_comment actions (S28)
	// — a comment comes back attributed to its agent and must not re-fire its own rule.
	// Empty means the trigger acts as nobody: actor suppression, depth and the agent budget
	// scope have nothing to key on and those layers pass.
	RunAgentIDs []string
}

// Verdict is stage 3's answer. Proceed true means the pipeline continues to actions — with
// Pass carrying what stage 4's run_agent must hand the scheduler (the derived subject key,
// the computed chain depth, and the active run to supersede, if any). Proceed false means the
// guard terminated the firing: Outcome and Reason are written to the firing row exactly as
// returned (debounced, loop_stopped, budget_exceeded, or no_action for actor suppression and
// the skip token), AbsorbedByRunID links a debounced firing to the run that absorbed it, and
// RunID is the terminal loop-stopped run row created when layer 4 tripped (architecture §9:
// created, never suppressed — the loop chain view needs a row to hang the explanation on).
type Verdict struct {
	Proceed         bool
	Outcome         domain.FiringOutcome
	Reason          string
	AbsorbedByRunID *string
	RunID           *string
	// Pass is the stage-3 → stage-4 pass-through, meaningful only when Proceed is true. The
	// engine copies it onto ActionContext.Guard; run_agent copies it into the RunRequest so
	// the scheduler stores the subject key and depth on the run and performs the
	// cancel-in-progress supersession once the new run (and its seq) exists.
	Pass ports.GuardPass
}

// Evaluator is the stage-3 seam between the trigger engine and the loop-protection layers.
type Evaluator interface {
	Evaluate(ctx context.Context, in Input) Verdict
}

// Pass is the S26 pass-through: every firing proceeds to actions, with the subject key
// forwarded so runs still record it. It remains as the documented no-op for tests wired
// without the layers.
type Pass struct{}

// Evaluate lets every firing through.
func (Pass) Evaluate(_ context.Context, in Input) Verdict {
	return Verdict{Proceed: true, Pass: ports.GuardPass{SubjectKey: in.SubjectKey}}
}
