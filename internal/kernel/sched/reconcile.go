package sched

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"

	"github.com/spruce/lexicode/internal/domain"
	"github.com/spruce/lexicode/internal/kernel/ports"
	"github.com/spruce/lexicode/internal/kernel/store"
)

// reconcile is boot crash recovery (§10.6). The four cases:
//
//  1. provisioning/running with a live container → reattach: re-register the run's MCP token
//     (read back from the container, since the file was written once at Prepare), resume the
//     stream from runs.log_offset, and supervise as if nothing happened.
//  2. provisioning/running with no (or a dead) container → terminal `failed`, reason
//     "orchestrator restarted"; when the run had a branch, its last pushed state is recorded
//     as partial work — there is no container left to commit from.
//  3. containers labelled with this instance but no matching non-terminal run → the S17
//     orphan sweeper removes them (module/docker Sweep, on Start and hourly). Nothing to do
//     here; the sweeper reads run states from the same store.
//  4. needs_input / awaiting_approval → the run row is untouched — elicitations are durable
//     by design. When the container survived, the token and stream are reattached so an
//     answer resumes it; when it did not, the run stays parked for a human to stop.
func (s *Scheduler) reconcile(ctx context.Context) error {
	runs, err := s.st.Runs().ByStates(ctx,
		domain.RunProvisioning, domain.RunRunning,
		domain.RunNeedsInput, domain.RunAwaitingApproval)
	if err != nil {
		return err
	}
	for _, run := range runs {
		s.reconcileOne(ctx, run)
	}
	return nil
}

func (s *Scheduler) reconcileOne(ctx context.Context, run domain.Run) {
	parked := run.State == domain.RunNeedsInput || run.State == domain.RunAwaitingApproval

	inst := s.tryReattach(ctx, run)
	if inst == nil {
		if parked {
			// Case 4 without a container: the row stays parked (§10.6 — "unchanged").
			s.logger.Warn("sched: parked run's container did not survive; leaving it parked",
				slog.String("run", run.ID), slog.String("state", string(run.State)))
			return
		}
		// Case 2: dead container.
		reason := "orchestrator restarted"
		message := "The orchestrator restarted and the run's container was gone."
		if run.Branch != nil && *run.Branch != "" {
			message += fmt.Sprintf(" Work pushed before the restart is on `%s`.", *run.Branch)
			out := domain.RunOutput{
				ID: domain.NewID(), RunID: run.ID, Kind: domain.OutputPartialWork,
				Ref: *run.Branch, Summary: "Work pushed before the orchestrator restart.",
				CreatedAt: s.now(),
			}
			if err := s.st.RunOutputs().Append(ctx, &out); err != nil {
				s.logger.Error("sched: partial_work output write failed",
					slog.String("run", run.ID), slog.String("error", err.Error()))
			}
		}
		if _, err := s.transition(ctx, run.ID, domain.RunFailed, store.RunStateUpdate{
			StateReason: &reason, ErrorMessage: &message,
		}); err != nil {
			s.logger.Error("sched: restart-failure transition failed",
				slog.String("run", run.ID), slog.String("error", err.Error()))
		}
		s.cancelPendingElicitations(ctx, run.ID)
		if _, err := s.st.RunMessages().DropQueued(ctx, run.ID); err != nil {
			s.logger.Error("sched: dropping steering queue failed",
				slog.String("run", run.ID), slog.String("error", err.Error()))
		}
		return
	}

	// Case 1 (and case 4 with a surviving container): reattach.
	s.reregisterToken(ctx, run, inst)
	if s.opts.Proxy != nil {
		// Best effort: the proxy registry did not survive the restart either.
		ws, werr := s.st.Workspace().Get(ctx)
		repo, rerr := s.st.Repos().ByProject(ctx, run.ProjectID)
		if werr == nil && rerr == nil {
			var hosts []string
			if s.opts.GitHosts != nil {
				hosts = s.opts.GitHosts(repo)
			}
			s.opts.Proxy.Register(run.ID, s.tokenFor(run, inst), resolvePolicy(ws, repo), hosts...)
		}
	}
	if run.State == domain.RunProvisioning {
		// The container exists, so Prepare finished (or nearly); the agent launch below
		// starts (or resumes) the session. The row moves to running with a fresh start.
		now := s.now()
		if after, err := s.transition(ctx, run.ID, domain.RunRunning,
			store.RunStateUpdate{StartedAt: &now}); err == nil {
			run = after
		}
	}
	s.logger.Info("sched: reattached run",
		slog.String("run", run.ID), slog.String("state", string(run.State)),
		slog.Int64("log_offset", run.LogOffset))
	s.superviseFrom(run, inst)
}

// tryReattach finds a run's surviving instance, or nil.
func (s *Scheduler) tryReattach(ctx context.Context, run domain.Run) ports.Instance {
	if run.InstanceID == nil || *run.InstanceID == "" || s.opts.Sandbox == nil {
		return nil
	}
	sandbox, err := s.opts.Sandbox(run.SandboxID)
	if err != nil {
		s.logger.Error("sched: reattach sandbox lookup failed",
			slog.String("run", run.ID), slog.String("error", err.Error()))
		return nil
	}
	inst, err := sandbox.Reattach(ctx, ports.InstanceRef{
		SandboxID:  run.SandboxID,
		InstanceID: *run.InstanceID,
		RunID:      run.ID,
		LogOffset:  run.LogOffset,
	})
	if err != nil {
		if !errors.Is(err, ports.ErrInstanceGone) {
			s.logger.Error("sched: reattach failed",
				slog.String("run", run.ID), slog.String("error", err.Error()))
		}
		return nil
	}
	return inst
}

// reregisterToken re-registers the run's MCP token with the token authority: the container's
// .lexicode/mcp.json was written once at Prepare, so the surviving container still presents
// that token — minting a new one would lock it out (§10.6, mcp.RegisterToken).
func (s *Scheduler) reregisterToken(ctx context.Context, run domain.Run, inst ports.Instance) {
	if s.opts.Tokens == nil {
		return
	}
	token := s.readTokenBack(ctx, run, inst)
	if token == "" {
		return
	}
	s.opts.Tokens.RegisterToken(run.ID, token)
}

// tokenFor returns the reattached run's token (for proxy re-registration), best effort.
func (s *Scheduler) tokenFor(run domain.Run, inst ports.Instance) string {
	return s.readTokenBack(context.Background(), run, inst)
}

// readTokenBack extracts the run token from the container's MCP client config: the last path
// segment of the one server URL in /workspace/.lexicode/mcp.json.
func (s *Scheduler) readTokenBack(ctx context.Context, run domain.Run, inst ports.Instance) string {
	raw, err := inst.ReadFile(ctx, ".lexicode/mcp.json")
	if err != nil {
		s.logger.Warn("sched: could not read the run token back from the container",
			slog.String("run", run.ID), slog.String("error", err.Error()))
		return ""
	}
	// The file is the S19 builder's byte-stable shape: `"url": ".../mcp/<token>"`.
	text := string(raw)
	i := strings.LastIndex(text, "/mcp/")
	if i < 0 {
		return ""
	}
	rest := text[i+len("/mcp/"):]
	if j := strings.IndexAny(rest, "\"' \n"); j >= 0 {
		rest = rest[:j]
	}
	return strings.TrimSpace(rest)
}
