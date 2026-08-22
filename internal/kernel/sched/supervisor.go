package sched

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/spruce/lexicode/internal/domain"
	"github.com/spruce/lexicode/internal/kernel/ports"
	"github.com/spruce/lexicode/internal/kernel/store"
)

// startSupervisor hands one admitted run to its own goroutine. The supervisor owns the run
// from provisioning to teardown; the admission loop never blocks on it.
func (s *Scheduler) startSupervisor(run domain.Run) {
	s.superviseFrom(run, nil)
}

// superviseFrom starts a supervisor, optionally over an already-reattached instance (the
// crash-recovery path skips provisioning).
func (s *Scheduler) superviseFrom(run domain.Run, reattached ports.Instance) {
	s.mu.Lock()
	if s.stopping || s.baseCtx == nil {
		s.mu.Unlock()
		return
	}
	if _, exists := s.supervisors[run.ID]; exists {
		s.mu.Unlock()
		return
	}
	ctx, cancel := context.WithCancel(s.baseCtx)
	sup := &supervisor{runID: run.ID, cancel: cancel, steer: make(chan struct{}, 1)}
	sup.inst = reattached
	s.supervisors[run.ID] = sup
	s.loops.Add(1)
	s.mu.Unlock()

	go func() {
		defer s.loops.Done()
		defer cancel()
		defer func() {
			s.mu.Lock()
			delete(s.supervisors, run.ID)
			s.mu.Unlock()
		}()
		s.supervise(ctx, sup, run, reattached != nil)
	}()
}

// requestStop records what a stop means for the outcome and signals the agent, once.
func (sup *supervisor) requestStop(ctx context.Context, kind domain.RunState, why string) {
	sup.mu.Lock()
	if sup.stopSet {
		sup.mu.Unlock()
		return
	}
	sup.stopSet = true
	sup.stopKind = kind
	sup.stopWhy = why
	handle := sup.handle
	sup.mu.Unlock()
	if handle != nil {
		go func() { _ = handle.Stop(context.WithoutCancel(ctx), why) }()
	}
}

func (sup *supervisor) stopRequested() (domain.RunState, string, bool) {
	sup.mu.Lock()
	defer sup.mu.Unlock()
	return sup.stopKind, sup.stopWhy, sup.stopSet
}

func (sup *supervisor) setHandle(h ports.Handle) (stopAlready bool, why string) {
	sup.mu.Lock()
	defer sup.mu.Unlock()
	sup.handle = h
	return sup.stopSet, sup.stopWhy
}

// supervise is one run's whole life: provision → launch → pump → terminal path → teardown.
func (s *Scheduler) supervise(ctx context.Context, sup *supervisor, run domain.Run, reattach bool) {
	agent, err := s.st.Agents().ByID(ctx, run.AgentID)
	if err != nil {
		s.finishWithoutInstance(ctx, run, domain.RunFailed, "agent missing",
			"The run's agent no longer exists.")
		return
	}

	var inst ports.Instance
	var branch string
	if reattach {
		inst = sup.inst
		if run.Branch != nil {
			branch = *run.Branch
		}
	} else {
		inst, branch, err = s.provision(ctx, sup, run, agent)
		if err != nil {
			if s.stoppingNow() {
				return // shutdown mid-provision: next boot reconciles (§10.6)
			}
			if kind, why, stopped := sup.stopRequested(); stopped {
				s.finish(ctx, sup, run, agent, inst, branch, kind, why, why)
				return
			}
			s.finish(ctx, sup, run, agent, inst, branch, domain.RunFailed,
				"provisioning failed", err.Error())
			return
		}
		if run.TicketID != nil {
			s.moveTicket(ctx, *run.TicketID, domain.CategoryRunning,
				fmt.Sprintf("run #%d started", run.Seq))
		}
	}

	sink := s.newRunSink(ctx, sup, run, agent)
	handle, err := s.launch(ctx, run, agent, inst, sink, reattach)
	if err != nil {
		if s.stoppingNow() {
			return
		}
		s.finish(ctx, sup, run, agent, inst, branch, domain.RunFailed, "launch failed", err.Error())
		return
	}
	if stopAlready, why := sup.setHandle(handle); stopAlready {
		_ = handle.Stop(context.WithoutCancel(ctx), why)
	}

	// Anything steered while the run was queued or provisioning is delivered now — the
	// composer is enabled throughout (§10.3).
	s.drainSteering(ctx, sup, run.ID)

	// Wall clock counts from started_at, surviving restarts.
	deadline := s.wallDeadline(ctx, run, agent)

	waitCh := make(chan waitOutcome, 1)
	go func() {
		res, err := handle.Wait(context.WithoutCancel(ctx))
		waitCh <- waitOutcome{res: res, err: err}
	}()

	timer := time.NewTimer(time.Until(deadline))
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			if s.stoppingNow() {
				return // shutdown: abandon, do not destroy, do not transition (§10.6)
			}
			// Not shutdown: StopRun cancelled this context while the handle was not yet
			// set (a stop raced provisioning→launch). The kill was delivered by
			// setHandle's stop-already path; wait the session out and run the terminal
			// path so the run ends as canceled, not abandoned.
			w := <-waitCh
			kind, why, message := s.outcomeOf(sup, run, w)
			s.finish(ctx, sup, run, agent, inst, branch, kind, why, message)
			return
		case <-sup.steer:
			s.drainSteering(ctx, sup, run.ID)
		case <-timer.C:
			sup.requestStop(ctx, domain.RunTimedOut, fmt.Sprintf(
				"wall clock limit of %s reached", (time.Duration(agent.MaxWallClockSeconds)*time.Second)))
		case w := <-waitCh:
			if s.stoppingNow() {
				return
			}
			kind, why, message := s.outcomeOf(sup, run, w)
			s.finish(ctx, sup, run, agent, inst, branch, kind, why, message)
			return
		}
	}
}

type waitOutcome struct {
	res ports.Result
	err error
}

// outcomeOf maps a finished session onto its terminal state.
func (s *Scheduler) outcomeOf(sup *supervisor, run domain.Run, w waitOutcome) (domain.RunState, string, string) {
	if kind, why, stopped := sup.stopRequested(); stopped {
		return kind, why, why
	}
	if w.err != nil {
		return domain.RunFailed, "runtime error", w.err.Error()
	}
	if w.res.IsError || w.res.ExitCode != 0 {
		msg := w.res.ResultText
		if msg == "" {
			msg = fmt.Sprintf("the agent process exited %d", w.res.ExitCode)
		}
		return domain.RunFailed, "agent failed", msg
	}
	return domain.RunCompleted, "", w.res.ResultText
}

// wallDeadline is started_at + the agent's wall-clock limit; a missing limit defaults to an
// hour (the agents schema default).
func (s *Scheduler) wallDeadline(_ context.Context, run domain.Run, agent domain.Agent) time.Time {
	limit := time.Duration(agent.MaxWallClockSeconds) * time.Second
	if limit <= 0 {
		limit = time.Hour
	}
	start := s.opts.Now()
	if run.StartedAt != nil {
		if t, err := time.Parse(time.RFC3339, *run.StartedAt); err == nil {
			start = t
		}
	}
	return start.Add(limit)
}

// ---------------------------------------------------------------- provisioning -----

// provision is §10.3: mint the token, register the proxy, build the spec, Prepare with the
// live checklist, persist the instance, and flip the run to running.
func (s *Scheduler) provision(ctx context.Context, sup *supervisor, run domain.Run, agent domain.Agent) (ports.Instance, string, error) {
	var token string
	if s.opts.Tokens != nil {
		var err error
		token, err = s.opts.Tokens.MintToken(run.ID)
		if err != nil {
			return nil, "", fmt.Errorf("minting the run's MCP token: %w", err)
		}
	}

	ws, err := s.st.Workspace().Get(ctx)
	if err != nil {
		return nil, "", err
	}
	project, err := s.st.Projects().ByID(ctx, run.ProjectID)
	if err != nil {
		return nil, "", err
	}
	repo, err := s.st.Repos().ByProject(ctx, run.ProjectID)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return nil, "", errors.New(
				"no repository is connected to this project; connect one in project settings before delegating")
		}
		return nil, "", err
	}
	var ticket *domain.Ticket
	if run.TicketID != nil {
		if tk, err := s.st.Tickets().ByID(ctx, *run.TicketID); err == nil {
			ticket = &tk
		}
	}

	if s.opts.Proxy != nil {
		var hosts []string
		if s.opts.GitHosts != nil {
			hosts = s.opts.GitHosts(repo)
		}
		s.opts.Proxy.Register(run.ID, token, resolvePolicy(ws, repo), hosts...)
	}

	if s.opts.Specs == nil {
		return nil, "", errors.New("sched: no workspace-preparation builder is wired")
	}
	built, err := s.opts.Specs.Build(ctx, SpecInput{
		Workspace: ws, Project: project, Repo: repo, Agent: agent,
		Ticket: ticket, Run: run, RunToken: token,
	})
	if err != nil {
		return nil, "", fmt.Errorf("preparing the workspace spec: %w", err)
	}
	red := &redactor{}
	red.add(built.SecretValues...)

	sandbox, err := s.opts.Sandbox(run.SandboxID)
	if err != nil {
		return nil, built.Branch, err
	}
	sink := &provisionSink{s: s, run: run, red: red, steps: map[string]int64{}}
	inst, err := sandbox.Prepare(ctx, built.Spec, sink)
	if err != nil {
		return nil, built.Branch, fmt.Errorf("provisioning failed: %s", red.clean(err.Error()))
	}
	sup.mu.Lock()
	sup.inst = inst
	sup.mu.Unlock()

	ref := inst.Ref()
	if err := s.st.Runs().SetProvisioned(context.WithoutCancel(ctx), run.ID,
		ref.InstanceID, "", built.Branch, ""); err != nil {
		s.logger.Error("sched: persisting instance ref failed",
			slog.String("run", run.ID), slog.String("error", err.Error()))
	}
	now := s.now()
	if _, err := s.transition(ctx, run.ID, domain.RunRunning, store.RunStateUpdate{
		StartedAt: &now,
	}); err != nil {
		return inst, built.Branch, err
	}
	return inst, built.Branch, nil
}

// resolvePolicy mirrors the S19 builder's inheritance: the repo's policy, else the workspace
// default.
func resolvePolicy(ws domain.WorkspaceSettings, repo domain.Repo) ports.NetworkPolicy {
	mode := ws.DefaultNetworkPolicy
	if repo.NetworkPolicy != nil && *repo.NetworkPolicy != "" {
		mode = *repo.NetworkPolicy
	}
	p := ports.NetworkPolicy{Mode: ports.NetworkMode(mode)}
	if p.Mode == ports.NetworkAllowlist {
		p.Allow = append(p.Allow, repo.NetworkAllowlist...)
	}
	return p
}

// launch starts (or, on reattach, resumes) the agent runtime over the instance.
func (s *Scheduler) launch(ctx context.Context, run domain.Run, agent domain.Agent, inst ports.Instance, sink ports.RunSink, reattach bool) (ports.Handle, error) {
	if s.opts.Runtime == nil {
		return nil, errors.New("sched: no runtime lookup is wired")
	}
	rt, err := s.opts.Runtime(run.RuntimeID)
	if err != nil {
		return nil, err
	}
	spec := ports.RunSpec{
		RunID:       run.ID,
		Prompt:      run.Prompt,
		Model:       run.Model,
		Effort:      run.Effort,
		Autonomy:    run.Autonomy,
		Permissions: agent.Permissions,
		MaxSteps:    int(agent.MaxSteps),
	}
	if reattach {
		spec.ResumeFrom = run.LogOffset
	}
	return rt.Launch(ctx, spec, inst, sink)
}

// ---------------------------------------------------------------- steering -----

// NotifySteering nudges a run's supervisor to drain its steering queue. Unknown or not-yet-
// launched runs are fine: the queue is drained right after launch anyway.
func (s *Scheduler) NotifySteering(runID string) {
	s.mu.Lock()
	sup := s.supervisors[runID]
	s.mu.Unlock()
	if sup == nil {
		return
	}
	select {
	case sup.steer <- struct{}{}:
	default:
	}
}

// drainSteering hands every queued message to the adapter, in order, and stamps delivered_at
// when the adapter accepts it (delivery to the model happens between tool calls, per
// contracts §3.4 — "Applied after the current step"). Each delivery publishes a run.message
// frame so the composer's queued chip flips to delivered live (S24).
func (s *Scheduler) drainSteering(ctx context.Context, sup *supervisor, runID string) {
	sup.mu.Lock()
	handle := sup.handle
	sup.mu.Unlock()
	if handle == nil {
		return
	}
	msgs, err := s.st.RunMessages().QueuedForRun(ctx, runID)
	if err != nil {
		s.logger.Error("sched: steering read failed",
			slog.String("run", runID), slog.String("error", err.Error()))
		return
	}
	if len(msgs) == 0 {
		return
	}
	run, err := s.st.Runs().ByID(ctx, runID)
	if err != nil {
		s.logger.Error("sched: steering run read failed",
			slog.String("run", runID), slog.String("error", err.Error()))
		return
	}
	for _, m := range msgs {
		if err := handle.Steer(ctx, m.Body); err != nil {
			s.logger.Warn("sched: steering delivery failed; message stays queued",
				slog.String("run", runID), slog.String("error", err.Error()))
			return
		}
		at := s.now()
		if err := s.st.RunMessages().MarkDelivered(ctx, m.ID, at); err != nil &&
			!errors.Is(err, store.ErrNotFound) {
			s.logger.Error("sched: steering delivery mark failed",
				slog.String("run", runID), slog.String("error", err.Error()))
		}
		s.emitRunEvent(ctx, run, "message", map[string]any{"message": map[string]any{
			"id": m.ID, "run_id": runID, "state": "delivered", "delivered_at": at,
		}})
	}
}

// appendSystemActivity records a level-2 system line on a run — post-terminal notes like a
// failed PR open, honest in the transcript rather than lost in a log file.
func (s *Scheduler) appendSystemActivity(ctx context.Context, runID, title string) {
	a := domain.Activity{
		RunID: runID, Type: domain.ActivitySystem, Level: 2, GroupKey: "system",
		Title: truncate(title, 200), Payload: mustJSON(map[string]any{"text": title}),
		Attempt: 1, CreatedAt: s.now(),
	}
	if err := s.st.Activities().AppendNext(ctx, &a); err != nil {
		s.logger.Error("sched: system activity append failed",
			slog.String("run", runID), slog.String("error", err.Error()))
	}
}

// ---------------------------------------------------------------- stop / cancel -----

// StopRun terminates one run with the given reason (POST /runs/{id}/stop, archive-time
// cancellation, supersession). Queued runs cancel in place; live ones go through the
// supervisor so the artifact push and teardown happen (§10.5). Terminal runs are a no-op.
func (s *Scheduler) StopRun(ctx context.Context, runID, reason string) error {
	run, err := s.st.Runs().ByID(ctx, runID)
	if err != nil {
		return err
	}
	if run.State.Terminal() {
		return nil
	}
	s.mu.Lock()
	sup := s.supervisors[runID]
	s.mu.Unlock()

	if sup != nil {
		sup.requestStop(ctx, domain.RunCanceled, reason)
		sup.mu.Lock()
		handle := sup.handle
		sup.mu.Unlock()
		if handle == nil {
			// Still provisioning: cancel its context; the supervisor notices the stop
			// request and finishes as canceled.
			sup.cancel()
		}
		return nil
	}

	// No live supervisor (queued, or a parked run whose container did not survive):
	// transition directly. There is no container to preserve work from.
	if _, err := s.transition(ctx, runID, domain.RunCanceled, store.RunStateUpdate{
		StateReason: &reason,
	}); err != nil {
		return err
	}
	s.cancelPendingElicitations(ctx, runID)
	if _, err := s.st.RunMessages().DropQueued(ctx, runID); err != nil {
		s.logger.Error("sched: dropping steering queue failed",
			slog.String("run", runID), slog.String("error", err.Error()))
	}
	if s.opts.Tokens != nil {
		s.opts.Tokens.RevokeRun(runID)
	}
	s.Wake()
	return nil
}

// TakeoverRun is §10.7: store the human's note on the run, then stop it with reason
// `takeover` — the artifact push and teardown run exactly as for any stop, so the branch the
// human checks out carries whatever the agent had. The note is injected into the prompt of
// the next run on the same ticket (see assemblePrompt).
func (s *Scheduler) TakeoverRun(ctx context.Context, runID, note string) error {
	run, err := s.st.Runs().ByID(ctx, runID)
	if err != nil {
		return err
	}
	if run.State.Terminal() {
		return nil
	}
	if err := s.st.Runs().SetTakeoverNote(ctx, runID, note); err != nil {
		return err
	}
	return s.StopRun(ctx, runID, "takeover")
}

// ---------------------------------------------------------------- terminal path -----

// finishWithoutInstance ends a run that never had (or lost) its container.
func (s *Scheduler) finishWithoutInstance(ctx context.Context, run domain.Run, kind domain.RunState, reason, message string) {
	s.finish(ctx, nil, run, domain.Agent{}, nil, "", kind, reason, message)
}

// finish is the terminal path (§10.5): preserve partial work, tear down, and record the
// outcome. It is the only way a supervised run ends.
func (s *Scheduler) finish(ctx context.Context, sup *supervisor, run domain.Run, agent domain.Agent, inst ports.Instance, branch string, kind domain.RunState, reason, message string) {
	ctx = context.WithoutCancel(ctx)

	// The push (§10.5, and the D-9 amendment): the ORCHESTRATOR pushes, for every outcome,
	// because the agent's container holds no credential that could. For a run that ended
	// badly this is also the failure-artifact rule — whatever is in the workspace is
	// committed as wip first, so a failed run never leaves nothing behind. For a run that
	// completed it is how its branch reaches the remote at all, which is what the pull
	// request is then opened from.
	preserved := s.preserveAndPush(ctx, run, agent, inst, branch)
	if preserved.pushed && kind != domain.RunCompleted {
		s.recordPartialWork(ctx, run, preserved.branch)
	}

	fresh, err := s.st.Runs().ByID(ctx, run.ID)
	if err == nil {
		run = fresh
	}
	finalMessage := s.terminalMessage(kind, run, message, preserved)

	// Teardown: revoke the MCP token, unregister the proxy, destroy the container.
	if s.opts.Tokens != nil {
		s.opts.Tokens.RevokeRun(run.ID)
	}
	if s.opts.Proxy != nil {
		s.opts.Proxy.Unregister(run.ID)
	}
	if inst != nil {
		if err := inst.Destroy(ctx); err != nil {
			s.logger.Error("sched: container destroy failed",
				slog.String("run", run.ID), slog.String("error", err.Error()))
		}
	}

	s.cancelPendingElicitations(ctx, run.ID)
	if _, err := s.st.RunMessages().DropQueued(ctx, run.ID); err != nil {
		s.logger.Error("sched: dropping steering queue failed",
			slog.String("run", run.ID), slog.String("error", err.Error()))
	}

	after, err := s.transition(ctx, run.ID, kind, store.RunStateUpdate{
		StateReason:  &reason,
		ErrorMessage: &finalMessage,
	})
	if err != nil {
		s.logger.Error("sched: terminal transition failed",
			slog.String("run", run.ID), slog.String("state", string(kind)),
			slog.String("error", err.Error()))
	}

	// §10.4 step 6 — outputs are collected. The orchestrator opens the PR from the pushed
	// branch when the agent's grants allow it and no PR is recorded yet; the forge adapter
	// enforces the open_prs grant and appends the D-9 marker.
	if kind == domain.RunCompleted && s.opts.PRs != nil {
		if opened, err := s.opts.PRs.OpenForRun(ctx, after); err != nil {
			s.logger.Error("sched: opening the run's pull request failed",
				slog.String("run", run.ID), slog.String("error", err.Error()))
			s.appendSystemActivity(ctx, run.ID,
				"could not open a pull request: "+err.Error())
		} else if opened {
			s.logger.Info("sched: pull request opened for run", slog.String("run", run.ID))
		}
	}

	// Ticket coupling: a completed run that opened a PR moves its ticket to the
	// review-category column (category lookup, never a name).
	if kind == domain.RunCompleted && after.TicketID != nil {
		if outputs, err := s.st.RunOutputs().ForRun(ctx, run.ID); err == nil {
			for _, o := range outputs {
				if o.Kind == domain.OutputPullRequest {
					s.moveTicket(ctx, *after.TicketID, domain.CategoryReview,
						fmt.Sprintf("run #%d opened a pull request", run.Seq))
					break
				}
			}
		}
	}
	s.Wake() // a freed slot may admit the next queued run
}

// terminalMessage renders the outcome line, naming the preserved branch (§10.5's exact shape:
// "Failed after 6 steps. Partial work pushed to `dev/PAY-14-idempotency-keys`.").
//
// Every clause is a fact from preserveOutcome. A push that failed says so and names the
// error; a workspace with nothing in it says nothing at all. The version this replaced ran
// `git push … || true` and then claimed "Partial work pushed to `branch`" whatever happened,
// which is the one thing a terminal message must never do.
func (s *Scheduler) terminalMessage(kind domain.RunState, run domain.Run, message string, p preserveOutcome) string {
	var b strings.Builder
	switch kind {
	case domain.RunCompleted:
		b.WriteString(message)
		// A completed run's own text is the message; the only thing worth adding is a push
		// that did not happen, because the pull request will be missing and the reason
		// belongs next to it.
		if p.attempted && p.failure != "" {
			fmt.Fprintf(&b, " The branch could not be pushed: %s.", strings.TrimSuffix(p.failure, "."))
		}
		return strings.TrimSpace(b.String())
	case domain.RunFailed:
		fmt.Fprintf(&b, "Failed after %d steps.", run.StepCount)
	case domain.RunTimedOut:
		fmt.Fprintf(&b, "Timed out after %d steps.", run.StepCount)
	case domain.RunCanceled:
		b.WriteString("Canceled.")
	}
	if message != "" {
		b.WriteString(" " + strings.TrimSuffix(message, ".") + ".")
	}
	switch {
	case !p.attempted:
	case p.pushed:
		fmt.Fprintf(&b, " Partial work pushed to `%s`.", p.branch)
	case p.failure != "" && p.committed:
		fmt.Fprintf(&b, " Partial work was committed on `%s` but could not be pushed: %s.",
			p.branch, strings.TrimSuffix(p.failure, "."))
	case p.failure != "":
		fmt.Fprintf(&b, " Partial work could not be preserved: %s.",
			strings.TrimSuffix(p.failure, "."))
	case p.nothing:
		b.WriteString(" There was nothing to preserve: the workspace held no changes.")
	}
	return b.String()
}

// recordPartialWork writes the §10.5 output row. It is written only when a push actually
// landed, so the row and the terminal message cannot disagree.
func (s *Scheduler) recordPartialWork(ctx context.Context, run domain.Run, branch string) {
	out := domain.RunOutput{
		ID: domain.NewID(), RunID: run.ID, Kind: domain.OutputPartialWork,
		Ref: branch, Summary: "Partial work pushed to `" + branch + "`.",
		CreatedAt: s.now(),
	}
	if err := s.st.RunOutputs().Append(ctx, &out); err != nil {
		s.logger.Error("sched: partial_work output write failed",
			slog.String("run", run.ID), slog.String("error", err.Error()))
	}
}

// cancelPendingElicitations closes a terminated run's open questions so nothing waits on a
// run that ended.
func (s *Scheduler) cancelPendingElicitations(ctx context.Context, runID string) {
	pending, err := s.st.Elicitations().PendingForRun(ctx, runID)
	if err != nil {
		s.logger.Error("sched: pending elicitation read failed",
			slog.String("run", runID), slog.String("error", err.Error()))
		return
	}
	for _, el := range pending {
		if err := s.st.Elicitations().Respond(ctx, el.ID, domain.ElicitationCanceled,
			nil, nil, s.now()); err != nil && !errors.Is(err, store.ErrNotFound) {
			s.logger.Error("sched: elicitation cancel failed",
				slog.String("elicitation", el.ID), slog.String("error", err.Error()))
		}
	}
}

// moveTicket is the board coupling, through the seam. Failures are logged, never fatal to
// the run — a board hiccup must not kill an agent session.
func (s *Scheduler) moveTicket(ctx context.Context, ticketID string, cat domain.ColumnCategory, note string) {
	if s.opts.Tickets == nil {
		return
	}
	if err := s.opts.Tickets.MoveTicketToCategory(context.WithoutCancel(ctx), ticketID, cat, note); err != nil {
		s.logger.Error("sched: ticket move failed",
			slog.String("ticket", ticketID), slog.String("category", string(cat)),
			slog.String("error", err.Error()))
	}
}
