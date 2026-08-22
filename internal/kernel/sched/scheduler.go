package sched

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/spruce/lexicode/internal/domain"
	"github.com/spruce/lexicode/internal/kernel/audit"
	"github.com/spruce/lexicode/internal/kernel/bus"
	"github.com/spruce/lexicode/internal/kernel/guard"
	"github.com/spruce/lexicode/internal/kernel/ports"
	"github.com/spruce/lexicode/internal/kernel/store"
)

// SpecBuilder assembles the SandboxSpec for one run — the S19 workspace-preparation builder,
// behind a kernel-side seam because the builder lives in internal/service/runs and the kernel
// imports nothing above itself (architecture §2.1). cmd/lexicode wires the adapter.
type SpecBuilder interface {
	Build(ctx context.Context, in SpecInput) (SpecResult, error)
}

// SpecInput is everything a SpecBuilder reads. The scheduler loads the rows.
type SpecInput struct {
	Workspace domain.WorkspaceSettings
	Project   domain.Project
	Repo      domain.Repo
	Agent     domain.Agent
	Ticket    *domain.Ticket // nil for a ticketless run
	Run       domain.Run
	RunToken  string
}

// SpecResult is a SpecBuilder's product.
type SpecResult struct {
	Spec ports.SandboxSpec
	// Branch is the run branch the spec creates; the scheduler persists it to runs.branch.
	Branch string
	// SecretValues is every secret that went into the spec; the scheduler registers them
	// with its redactor before anything executes or is logged.
	SecretValues []string
}

// TokenAuthority mints, re-registers and revokes per-run MCP tokens (D-12). The S21 MCP
// server satisfies it; cmd/lexicode wires it.
type TokenAuthority interface {
	MintToken(runID string) (string, error)
	RegisterToken(runID, token string)
	RevokeRun(runID string)
}

// ProxyRegistrar registers a run with the S18 egress proxy before its container exists and
// unregisters it at teardown. The docker module's Proxy satisfies it.
type ProxyRegistrar interface {
	Register(runID, token string, policy ports.NetworkPolicy, gitHosts ...string)
	Unregister(runID string)
}

// TicketMover is the run↔board coupling (§10.4): on start the ticket moves to the
// running-category column, on a PR output to the review-category column — category lookups,
// never names (plan rule 3). The tickets service implements it; nil disables the coupling.
type TicketMover interface {
	MoveTicketToCategory(ctx context.Context, ticketID string, category domain.ColumnCategory, note string) error
}

// PROpener opens a completed run's pull request from its pushed branch (§10.4 step 6: "on
// result, outputs are collected: pushed branch, opened PR"). The orchestrator — not the
// agent — talks to the forge, so the D-9 actor marker and the open_prs grant check are
// enforced in the adapter, never by prompt. The service layer implements it (it needs the
// forge port and the repo's stored token, which the kernel does not touch); nil disables PR
// opening. Implementations return (false, nil) when there is nothing to open — no
// permission, no branch, no ticket, or a PR already recorded.
type PROpener interface {
	OpenForRun(ctx context.Context, run domain.Run) (bool, error)
}

// Options configures New. Store, Bus and Audit are required; the seams degrade individually
// (a nil Tokens mints nothing, a nil Proxy registers nothing, a nil Tickets moves nothing) so
// tests wire exactly what they exercise.
type Options struct {
	Store  *store.Store
	Bus    *bus.Bus
	Audit  *audit.Writer
	Logger *slog.Logger

	// Sandbox and Runtime resolve port implementations by ID (kernel.Sandbox /
	// kernel.Runtime). Required for runs to start.
	Sandbox func(id string) (ports.Sandbox, error)
	Runtime func(id string) (ports.AgentRuntime, error)
	// Providers returns the registered context providers (kernel.ContextProviders); the
	// scheduler resolves them in Priority order at enqueue. Nil means no providers.
	Providers func() []ports.ContextProvider

	Specs   SpecBuilder
	Tokens  TokenAuthority
	Proxy   ProxyRegistrar
	Tickets TicketMover
	PRs     PROpener

	// SandboxID is which sandbox runs execute in ("docker" in production, "fake" in tests).
	// Empty means "docker".
	SandboxID string
	// GitHosts returns the git remote hosts a run's egress policy must allow, from its repo
	// row. Nil means none are added.
	GitHosts func(repo domain.Repo) []string

	// AdmitInterval is how often the admission pass re-evaluates queued runs besides
	// explicit wakes (a terminal run frees a slot, an enqueue arrives). Zero means 2s.
	AdmitInterval time.Duration
	// Now overrides the clock for tests.
	Now func() time.Time
}

// Scheduler is the kernel-owned run engine (D-14): the only component that creates runs,
// admits them, launches them, supervises them and writes runs.state. Everything else asks.
type Scheduler struct {
	st     *store.Store
	bus    *bus.Bus
	audit  *audit.Writer
	logger *slog.Logger
	opts   Options

	wake chan struct{}

	mu          sync.Mutex
	supervisors map[string]*supervisor
	started     bool
	stopping    bool
	baseCtx     context.Context
	cancel      context.CancelFunc
	loops       sync.WaitGroup
}

// supervisor is the in-memory state of one live run.
type supervisor struct {
	runID  string
	cancel context.CancelFunc

	mu       sync.Mutex
	handle   ports.Handle
	inst     ports.Instance
	stopSet  bool
	stopKind domain.RunState // canceled | timed_out | failed — what Stop was for
	stopWhy  string
	steer    chan struct{}
}

// New builds a scheduler. Call Start to begin reconciliation and admission.
func New(opts Options) *Scheduler {
	logger := opts.Logger
	if logger == nil {
		logger = slog.Default()
	}
	if opts.Now == nil {
		opts.Now = time.Now
	}
	if opts.AdmitInterval <= 0 {
		opts.AdmitInterval = 2 * time.Second
	}
	if opts.SandboxID == "" {
		opts.SandboxID = "docker"
	}
	return &Scheduler{
		st:          opts.Store,
		bus:         opts.Bus,
		audit:       opts.Audit,
		logger:      logger,
		opts:        opts,
		wake:        make(chan struct{}, 1),
		supervisors: map[string]*supervisor{},
	}
}

// Start runs crash reconciliation (§10.6) and then the admission loop. ctx bounds every
// background goroutine the scheduler owns.
func (s *Scheduler) Start(ctx context.Context) error {
	s.mu.Lock()
	if s.started {
		s.mu.Unlock()
		return errors.New("sched: already started")
	}
	s.started = true
	s.baseCtx, s.cancel = context.WithCancel(context.WithoutCancel(ctx))
	base := s.baseCtx
	s.mu.Unlock()

	if err := s.reconcile(base); err != nil {
		s.logger.Error("sched: crash reconciliation failed", slog.String("error", err.Error()))
	}

	s.loops.Add(1)
	go func() {
		defer s.loops.Done()
		s.admitLoop(base)
	}()

	// Stop when the process context ends: supervisors abandon their runs without destroying
	// containers, so the next boot reattaches (§10.6).
	go func() {
		<-ctx.Done()
		s.Stop(context.WithoutCancel(ctx))
	}()
	return nil
}

// Stop drains the scheduler: the admission loop ends and every supervisor abandons its run.
// Running containers are deliberately NOT destroyed and run states are NOT touched — a run
// that was `running` stays `running` in the database and is reattached on the next boot
// (§10.6). Idempotent.
func (s *Scheduler) Stop(context.Context) {
	s.mu.Lock()
	if !s.started || s.stopping {
		s.mu.Unlock()
		return
	}
	s.stopping = true
	cancel := s.cancel
	s.mu.Unlock()
	cancel()
	s.loops.Wait()
}

// stoppingNow reports whether process shutdown is in progress — the supervisors' signal to
// abandon rather than terminate.
func (s *Scheduler) stoppingNow() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.stopping
}

func (s *Scheduler) now() string { return domain.FormatTime(s.opts.Now()) }

func (s *Scheduler) day() string { return s.opts.Now().UTC().Format("2006-01-02") }

// Wake nudges the admission loop; safe from any goroutine, never blocks.
func (s *Scheduler) Wake() {
	select {
	case s.wake <- struct{}{}:
	default:
	}
}

// ---------------------------------------------------------------- Requester -----

// RequestRun implements Requester: Enqueue plus nothing — admission happens asynchronously.
func (s *Scheduler) RequestRun(ctx context.Context, req RunRequest) (string, error) {
	run, err := s.Enqueue(ctx, req)
	if err != nil {
		return "", err
	}
	return run.ID, nil
}

// CancelTicketRuns implements Requester (D-15's archive path): every non-terminal run of the
// ticket is stopped with the given reason.
func (s *Scheduler) CancelTicketRuns(ctx context.Context, ticketID, reason string) (int64, error) {
	runs, err := s.st.Runs().ActiveForTicket(ctx, ticketID)
	if err != nil {
		return 0, err
	}
	var n int64
	for _, run := range runs {
		if err := s.StopRun(ctx, run.ID, reason); err != nil {
			s.logger.Error("sched: cancel-ticket-runs stop failed",
				slog.String("run", run.ID), slog.String("error", err.Error()))
			continue
		}
		n++
	}
	return n, nil
}

// ---------------------------------------------------------------- Enqueue -----

// validRequest rejects a malformed request before anything is written.
func (s *Scheduler) loadRequestRows(ctx context.Context, req RunRequest) (domain.Agent, *domain.Ticket, error) {
	if req.ProjectID == "" || req.AgentID == "" {
		return domain.Agent{}, nil, errors.New("sched: a run request needs a project and an agent")
	}
	agent, err := s.st.Agents().ByID(ctx, req.AgentID)
	if err != nil {
		return domain.Agent{}, nil, fmt.Errorf("sched: no such agent %s: %w", req.AgentID, err)
	}
	if agent.ProjectID != req.ProjectID {
		return domain.Agent{}, nil, errors.New("sched: agent belongs to a different project")
	}
	if !agent.Enabled || agent.ArchivedAt != nil {
		return domain.Agent{}, nil, fmt.Errorf("sched: agent %s is disabled", agent.Name)
	}
	var ticket *domain.Ticket
	if req.TicketID != "" {
		tk, err := s.st.Tickets().ByID(ctx, req.TicketID)
		if err != nil {
			return domain.Agent{}, nil, fmt.Errorf("sched: no such ticket %s: %w", req.TicketID, err)
		}
		if tk.ProjectID != req.ProjectID {
			return domain.Agent{}, nil, errors.New("sched: ticket belongs to a different project")
		}
		if tk.ArchivedAt != nil {
			return domain.Agent{}, nil, fmt.Errorf("sched: ticket %s is archived", tk.Key)
		}
		ticket = &tk
	}
	return agent, ticket, nil
}

// Enqueue is THE entry point (D-14): create the run row — queued, with its snapshotted
// directive, rendered prompt and recorded context stack — audit it, publish the run event,
// and wake admission. Callers: the delegate endpoint, @mention, column auto-start, and the
// run_agent trigger action (S28).
func (s *Scheduler) Enqueue(ctx context.Context, req RunRequest) (domain.Run, error) {
	agent, ticket, err := s.loadRequestRows(ctx, req)
	if err != nil {
		return domain.Run{}, err
	}

	// Directive snapshot: what this run was actually told, immune to later edits.
	var directiveBody string
	if agent.DirectiveVersionID != nil {
		d, err := s.st.Directives().ByID(ctx, *agent.DirectiveVersionID)
		if err != nil {
			return domain.Run{}, fmt.Errorf("sched: loading directive snapshot: %w", err)
		}
		directiveBody = d.Body
	}

	prompt, items, err := s.assemblePrompt(ctx, agent, ticket, directiveBody, req)
	if err != nil {
		return domain.Run{}, err
	}

	now := s.now()
	run := domain.Run{
		ID:                 domain.NewID(),
		ProjectID:          req.ProjectID,
		AgentID:            agent.ID,
		State:              domain.RunQueued,
		Autonomy:           agent.Autonomy,
		DirectiveVersionID: agent.DirectiveVersionID,
		Model:              agent.Model,
		Effort:             agent.Effort,
		Prompt:             prompt,
		RuntimeID:          agent.RuntimeID,
		SandboxID:          s.opts.SandboxID,
		StateReason:        req.Reason,
		SubjectKey:         subjectKey(ticket),
		Depth:              req.Depth,
		QueuedAt:           now,
	}
	if req.SubjectKey != "" {
		// The guard's descriptor-derived subject ("pr:219") wins over the ticket fallback:
		// it is what debounce and cancel-in-progress key on (S27).
		run.SubjectKey = req.SubjectKey
	}
	if req.TicketID != "" {
		run.TicketID = &req.TicketID
	}
	if req.TriggerID != "" {
		run.TriggerID = &req.TriggerID
	}
	if req.CauseEventID != "" {
		run.CauseEventID = &req.CauseEventID
	}
	if req.ParentRunID != "" {
		run.ParentRunID = &req.ParentRunID
	}
	if req.RequestedByUserID != "" {
		run.RequestedByUserID = &req.RequestedByUserID
	}

	err = s.st.Tx(ctx, func(tx *store.Tx) error {
		seq, err := tx.Runs().NextSeq(ctx, run.ProjectID)
		if err != nil {
			return err
		}
		run.Seq = seq
		if err := tx.Runs().Create(ctx, &run); err != nil {
			return err
		}
		for i := range items {
			items[i].RunID = run.ID
			items[i].Position = int64(i + 1)
			if items[i].ID == "" {
				items[i].ID = domain.NewID()
			}
			if err := tx.RunContextItems().Create(ctx, &items[i]); err != nil {
				return err
			}
		}
		// The ticket stream's RunSessionCard row (data model §4.1) exists from the moment
		// the run does.
		if ticket != nil {
			rid := run.ID
			return tx.TicketStream().Append(ctx, &domain.StreamEntry{
				ID: domain.NewID(), TicketID: ticket.ID, Kind: domain.StreamRun,
				ActorKind: domain.ActorAgent, ActorID: &agent.ID,
				Payload:   mustJSON(map[string]any{"run_seq": run.Seq, "reason": req.Reason}),
				RunID:     &rid,
				CreatedAt: now,
			})
		}
		return nil
	})
	if err != nil {
		return domain.Run{}, err
	}

	if err := s.audit.Write(ctx, "run.enqueue",
		audit.Target{Kind: "run", ID: run.ID, ProjectID: run.ProjectID, Note: req.Reason},
		nil, map[string]any{"seq": run.Seq, "agent_id": run.AgentID, "ticket_id": req.TicketID,
			"state": string(run.State)}); err != nil {
		return domain.Run{}, err
	}

	// Context budget (architecture §11 step 3): when the always-scoped wiki items alone
	// exceed the project's threshold, the run still proceeds — a warning lands on the
	// transcript and the context meter reads amber client-side over the same numbers.
	s.warnContextBudget(ctx, run, items)

	// Cancel-in-progress (S27, architecture §9 layer 3): the guard elected a still-active
	// run for the same (trigger, subject); now that the new run and its seq exist, cancel
	// the old one with a reason naming its replacement, and retro-mark its firing
	// `superseded` pointing at the new run — before waking admission, so cancel-then-admit
	// ordering holds. (The admission ticker can still race a pass already in flight; the
	// worst case is the new run waiting one interval for the freed slot.)
	if req.SupersededRunID != "" {
		reason := fmt.Sprintf("superseded by run #%d", run.Seq)
		if err := s.StopRun(ctx, req.SupersededRunID, reason); err != nil {
			s.logger.Error("sched: superseded-run stop failed",
				slog.String("run", req.SupersededRunID), slog.String("error", err.Error()))
		}
		if req.TriggerID != "" {
			if err := s.st.Firings().Supersede(ctx, req.TriggerID, req.SupersededRunID, run.ID, reason); err != nil {
				s.logger.Error("sched: firing supersede failed",
					slog.String("run", req.SupersededRunID), slog.String("error", err.Error()))
			}
		}
	}

	s.emitRunState(ctx, run)
	s.Wake()
	return run, nil
}

// EnqueueLoopStopped implements guard.LoopStopper (S27): record a run the loop guard's depth
// counter refused to start — created directly in the terminal `loop_stopped` state, with no
// container, no prompt and no cost, referencing the trigger and the cause event so
// GET /runs/{id}/chain can reconstruct the causal chain the visualization renders
// (architecture §9: the run is created, never suppressed). It lives here because only the
// scheduler writes runs.state (data model invariant 4).
func (s *Scheduler) EnqueueLoopStopped(ctx context.Context, req guard.LoopStoppedRun) (domain.Run, error) {
	if req.ProjectID == "" || req.AgentID == "" {
		return domain.Run{}, errors.New("sched: a loop-stopped run needs a project and an agent")
	}
	agent, err := s.st.Agents().ByID(ctx, req.AgentID)
	if err != nil {
		return domain.Run{}, fmt.Errorf("sched: no such agent %s: %w", req.AgentID, err)
	}
	now := s.now()
	run := domain.Run{
		ID:                 domain.NewID(),
		ProjectID:          req.ProjectID,
		AgentID:            agent.ID,
		State:              domain.RunLoopStopped,
		StateReason:        req.Reason,
		Autonomy:           agent.Autonomy,
		DirectiveVersionID: agent.DirectiveVersionID,
		Model:              agent.Model,
		Effort:             agent.Effort,
		RuntimeID:          agent.RuntimeID,
		SandboxID:          s.opts.SandboxID,
		SubjectKey:         req.SubjectKey,
		Depth:              req.Depth,
		QueuedAt:           now,
		EndedAt:            &now,
	}
	if req.TriggerID != "" {
		run.TriggerID = &req.TriggerID
	}
	if req.CauseEventID != "" {
		run.CauseEventID = &req.CauseEventID
	}
	err = s.st.Tx(ctx, func(tx *store.Tx) error {
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
	if err := s.audit.Write(ctx, "run.loop_stopped",
		audit.Target{Kind: "run", ID: run.ID, ProjectID: run.ProjectID, Note: req.Reason},
		nil, map[string]any{"seq": run.Seq, "agent_id": run.AgentID,
			"trigger_id": req.TriggerID, "depth": req.Depth,
			"subject_key": req.SubjectKey}); err != nil {
		return domain.Run{}, err
	}
	s.emitRunState(ctx, run)
	return run, nil
}

// subjectKey is the guard key (§9): the ticket when one exists, else the repo. PR-scoped keys
// arrive with the loop guard (S27), which derives them from event descriptors.
func subjectKey(ticket *domain.Ticket) string {
	if ticket != nil {
		return "ticket:" + ticket.Key
	}
	return "repo"
}

// ---------------------------------------------------------------- run events -----

// emitRunState publishes the run.state bus event — the one feed for SSE (topic run:<id>)
// and, later, trigger evaluation. Best-effort: the mutation is committed by the time this
// runs; a failure is logged, never unwound.
func (s *Scheduler) emitRunState(ctx context.Context, run domain.Run) {
	s.emitRunEvent(ctx, run, "state", map[string]any{"run": s.normalizedRun(ctx, run)})
}

func (s *Scheduler) emitRunEvent(ctx context.Context, run domain.Run, activity string, body map[string]any) {
	if s.bus == nil {
		return
	}
	pid, rid := run.ProjectID, run.ID
	e := domain.Event{
		ProjectID: &pid, Kind: "run", ActivityType: activity,
		SubjectKind: "run", SubjectID: &rid,
		ActorKind:  domain.ActorSystem,
		Payload:    mustJSON(body),
		OccurredAt: s.now(),
	}
	if err := s.bus.Emit(context.WithoutCancel(ctx), e); err != nil {
		s.logger.Error("sched: emit failed",
			slog.String("kind", "run."+activity), slog.String("error", err.Error()))
	}
}

// runEventBody is the run summary every run.state frame carries.
func runEventBody(run domain.Run) map[string]any {
	return map[string]any{
		"id": run.ID, "seq": run.Seq, "project_id": run.ProjectID, "agent_id": run.AgentID,
		"ticket_id": run.TicketID, "state": string(run.State),
		"state_reason": run.StateReason, "hold_reason": run.HoldReason,
		"error_message": run.ErrorMessage, "current_step": run.CurrentStep,
		"cost_cents": run.CostCents, "started_at": run.StartedAt, "ended_at": run.EndedAt,
	}
}

// normalizedRun is runEventBody plus the contracts §4 `run` vocabulary — the user-visible
// field names trigger conditions and {{...}} interpolation address (run.agent, run.status,
// run.duration_seconds, run.ticket_key; story S25 aligned this additively so the SSE fields
// existing clients read stay untouched). Lookups are best-effort: a failure leaves the field
// empty rather than losing the event.
func (s *Scheduler) normalizedRun(ctx context.Context, run domain.Run) map[string]any {
	body := runEventBody(run)
	body["status"] = string(run.State)
	body["agent"] = ""
	if a, err := s.st.Agents().ByID(ctx, run.AgentID); err == nil {
		body["agent"] = a.Name
	}
	body["ticket_key"] = ""
	if run.TicketID != nil {
		if tk, err := s.st.Tickets().ByID(ctx, *run.TicketID); err == nil {
			body["ticket_key"] = tk.Key
		}
	}
	var dur int64
	if run.StartedAt != nil && run.EndedAt != nil {
		if start, err := domain.ParseTime(*run.StartedAt); err == nil {
			if end, err := domain.ParseTime(*run.EndedAt); err == nil && end.After(start) {
				dur = int64(end.Sub(start).Seconds())
			}
		}
	}
	body["duration_seconds"] = dur
	return body
}

// providersInOrder returns the registered context providers sorted by Priority, ties by ID.
func (s *Scheduler) providersInOrder() []ports.ContextProvider {
	if s.opts.Providers == nil {
		return nil
	}
	provs := s.opts.Providers()
	sort.SliceStable(provs, func(i, j int) bool {
		if provs[i].Priority() != provs[j].Priority() {
			return provs[i].Priority() < provs[j].Priority()
		}
		return provs[i].ID() < provs[j].ID()
	})
	return provs
}

// redactor is the kernel-side secret scrubber for provisioning output. (The service-layer
// Redactor cannot be imported here; the mechanism is three lines.)
type redactor struct {
	mu     sync.RWMutex
	values []string
}

func (r *redactor) add(values ...string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, v := range values {
		if len(v) >= 4 { // too-short values would redact innocent substrings
			r.values = append(r.values, v)
		}
	}
}

func (r *redactor) clean(s string) string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	for _, v := range r.values {
		s = strings.ReplaceAll(s, v, "•••")
	}
	return s
}
