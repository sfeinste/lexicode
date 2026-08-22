// Package guard is stage 3 of the trigger pipeline: the five loop-protection layers and the
// budget ledger (architecture §9, story S27).
//
// S26 defines the stage's interface and ships Pass, a pass-through evaluator, so the engine's
// four-stage shape is real before the layers exist. S27 replaces the wiring's Pass with the
// full evaluator — actor suppression, debounce, cancel-in-progress, the depth counter and
// budget checks — without the engine changing: every layer speaks through Verdict.
package guard

import (
	"context"

	"github.com/spruce/lexicode/internal/domain"
)

// Input is what stage 3 evaluates: the event, the trigger it matched, and the loop-protection
// subject key derived from the event descriptor's template ("pr:219" / "ticket:PAY-14" /
// "repo"). S26's engine passes the event's subject verbatim; S27 owns the real derivation.
type Input struct {
	Event      domain.Event
	Trigger    domain.Trigger
	SubjectKey string
}

// Verdict is stage 3's answer. Proceed true means the pipeline continues to actions. Proceed
// false means the guard terminated the firing: Outcome and Reason are written to the firing
// row exactly as returned (debounced, superseded, loop_stopped, budget_exceeded, or no_action
// for actor suppression and the skip token), and AbsorbedByRunID links a debounced firing to
// the run that absorbed it.
type Verdict struct {
	Proceed         bool
	Outcome         domain.FiringOutcome
	Reason          string
	AbsorbedByRunID *string
}

// Evaluator is the stage-3 seam between the trigger engine and the loop-protection layers.
type Evaluator interface {
	Evaluate(ctx context.Context, in Input) Verdict
}

// Pass is the S26 pass-through: every firing proceeds to actions. It exists so the engine is
// testable and wireable before S27, and so tests can substitute deterministic verdicts.
type Pass struct{}

// Evaluate lets every firing through.
func (Pass) Evaluate(context.Context, Input) Verdict { return Verdict{Proceed: true} }
