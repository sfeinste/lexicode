package sched

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"

	"github.com/spruce/lexicode/internal/domain"
	"github.com/spruce/lexicode/internal/kernel/audit"
	"github.com/spruce/lexicode/internal/kernel/store"
)

// legalTransitions is the run state machine (architecture §10.1) as a table: from → the set of
// states one transition may reach. Terminal states have no row — nothing leaves them.
// `loop_stopped` is a creation state (S27 creates such runs terminal), so it appears nowhere.
var legalTransitions = map[domain.RunState][]domain.RunState{
	domain.RunQueued: {
		domain.RunProvisioning, domain.RunCanceled, domain.RunFailed,
	},
	domain.RunProvisioning: {
		domain.RunRunning, domain.RunFailed, domain.RunCanceled,
	},
	domain.RunRunning: {
		domain.RunNeedsInput, domain.RunAwaitingApproval,
		domain.RunCompleted, domain.RunFailed, domain.RunTimedOut, domain.RunCanceled,
	},
	// A parked run can resume, end cleanly (the process exits while a question is open),
	// or be terminated — by cancel, wall clock, or a container that died under it.
	domain.RunNeedsInput: {
		domain.RunRunning, domain.RunCompleted, domain.RunFailed,
		domain.RunTimedOut, domain.RunCanceled,
	},
	domain.RunAwaitingApproval: {
		domain.RunRunning, domain.RunCompleted, domain.RunFailed,
		domain.RunTimedOut, domain.RunCanceled,
	},
}

func transitionLegal(from, to domain.RunState) bool {
	for _, t := range legalTransitions[from] {
		if t == to {
			return true
		}
	}
	return false
}

// IllegalTransitionError names the refused edge.
type IllegalTransitionError struct{ From, To domain.RunState }

func (e *IllegalTransitionError) Error() string {
	return fmt.Sprintf("sched: illegal run transition %s → %s", e.From, e.To)
}

// transition is the ONE place runs.state changes (data model invariant 4): check the table,
// guard-update the row, audit the edge, publish the run.state event. A transition to the
// state the run is already in is an idempotent no-op. u's nil fields are untouched.
func (s *Scheduler) transition(ctx context.Context, runID string, to domain.RunState, u store.RunStateUpdate) (domain.Run, error) {
	ctx = context.WithoutCancel(ctx) // a state change, once decided, must land
	for {
		run, err := s.st.Runs().ByID(ctx, runID)
		if err != nil {
			return domain.Run{}, err
		}
		if run.State == to {
			return run, nil
		}
		if !transitionLegal(run.State, to) {
			return run, &IllegalTransitionError{From: run.State, To: to}
		}
		if to.Terminal() && u.EndedAt == nil {
			now := s.now()
			u.EndedAt = &now
		}
		ok, err := s.st.Runs().SetState(ctx, runID, []domain.RunState{run.State}, to, u)
		if err != nil {
			return domain.Run{}, err
		}
		if !ok {
			continue // lost a transition race; re-read and re-decide
		}

		after, err := s.st.Runs().ByID(ctx, runID)
		if err != nil {
			after = run
			after.State = to
		}
		if err := s.audit.Write(ctx, "run.state",
			audit.Target{Kind: "run", ID: runID, ProjectID: run.ProjectID, Note: after.StateReason},
			map[string]any{"state": string(run.State)},
			map[string]any{"state": string(to), "state_reason": after.StateReason,
				"error_message": after.ErrorMessage}); err != nil {
			s.logger.Error("sched: run.state audit failed",
				slog.String("run", runID), slog.String("error", err.Error()))
		}
		s.emitRunState(ctx, after)
		return after, nil
	}
}

// SetRunState is the seam the MCP server drives (mcp.RunStateSetter): elicitations park a run
// in needs_input / awaiting_approval and a response resumes it. Only those three targets are
// accepted here — everything else belongs to the scheduler's own paths.
//
// It is also where the wall clock stops and starts (D-12). A parked run is not working, and
// the wall clock exists to bound work: charging a run for the hours a question sat in
// somebody's inbox is what left a slow answer with no budget left to act on. The transition
// is what tells the supervisor; a run with no live supervisor simply has no clock to pause.
func (s *Scheduler) SetRunState(ctx context.Context, runID string, state domain.RunState, reason string) error {
	switch state {
	case domain.RunNeedsInput, domain.RunAwaitingApproval, domain.RunRunning:
	default:
		return fmt.Errorf("sched: state %s is not reachable through the elicitation seam", state)
	}
	if _, err := s.transition(ctx, runID, state, store.RunStateUpdate{StateReason: &reason}); err != nil {
		return err
	}
	s.notePark(runID, state)
	return nil
}

// mustJSON marshals a value the scheduler controls; a failure is a programming error rendered
// honestly rather than panicking.
func mustJSON(v any) json.RawMessage {
	b, err := json.Marshal(v)
	if err != nil {
		return json.RawMessage(fmt.Sprintf(`{"marshal_error":%q}`, err.Error()))
	}
	return b
}
