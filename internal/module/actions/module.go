package actions

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"

	"github.com/spruce/lexicode/internal/domain"
	"github.com/spruce/lexicode/internal/kernel"
	"github.com/spruce/lexicode/internal/kernel/ports"
	"github.com/spruce/lexicode/internal/kernel/sched"
	"github.com/spruce/lexicode/internal/kernel/secrets"
	"github.com/spruce/lexicode/internal/kernel/store"
)

// TicketSeam is the two tickets-service behaviours the module needs but must not import
// (module → kernel/ports → domain): wired in cmd/lexicode to the S10 tickets service.
type TicketSeam struct {
	// CreateInTriage creates the ticket row and its pending triage item in one transaction
	// (tickets.CreateFromTrigger).
	CreateInTriage func(ctx context.Context, in TriageCreate) (domain.Ticket, error)
	// MoveToCategory is the category move with the S28 semantics: pending-triage refusal and
	// brief D3's auto-start-on-entry exception (tickets.TriggerMoveToCategory). moved=false
	// with a nil error is the no-op case — already in the category, or archived.
	MoveToCategory func(ctx context.Context, ticketID string, cat domain.ColumnCategory, note string) (moved bool, err error)
}

// TriageCreate is CreateInTriage's input, mirrored from tickets.TriggerCreateInput so the
// module names no service type; the cmd/lexicode closure translates.
type TriageCreate struct {
	ProjectID   string
	Title       string
	Description string
	LabelNames  []string
	Provenance  string
	TriggerID   string
	RunID       string
}

// NotifySeam is the S24 notify service's routing rule, injected for the same reason: the
// `notify` action must route exactly like run escalation does (brief D1), not with a copy of
// the rule that can drift.
type NotifySeam struct {
	// RouteRun resolves the delegating human for a run: requested_by → ticket assignee →
	// project owner (notify.Service.RouteTo).
	RouteRun func(ctx context.Context, run domain.Run) (string, error)
}

// Deps is what every action gets to work with. In production Init fills it from the kernel;
// tests construct it directly and hand the actions to a trigger engine.
type Deps struct {
	Store  *store.Store
	Logger *slog.Logger
	// Scheduler is kernel.Scheduler — late-bound, because cmd/lexicode attaches the
	// scheduler after module Init. run_agent calls Scheduler().Enqueue and nothing else
	// (D-14).
	Scheduler func() *sched.Scheduler
	// Forge resolves a forge provider by ID (kernel.Forge) — post_comment's write path.
	Forge func(id string) (ports.ForgeProvider, error)
	// Notifier resolves a notifier by ID (kernel.Notifier) — notify delivers through
	// "inapp" in V1.
	Notifier func(id string) (ports.Notifier, error)
	// Secrets reads the repo token for forge writes.
	Secrets *secrets.Store
	Tickets TicketSeam
	Notify  NotifySeam
}

// All returns the five S28 actions over one dependency set, in the THEN dropdown's order.
func All(d Deps) []ports.TriggerAction {
	if d.Logger == nil {
		d.Logger = slog.Default()
	}
	return []ports.TriggerAction{
		&runAgent{d: d},
		&createTicket{d: d},
		&moveTicket{d: d},
		&postComment{d: d},
		&notifyAction{d: d},
	}
}

// Options configures New: only the injected service seams — everything else comes from the
// kernel at Init.
type Options struct {
	Tickets TicketSeam
	Notify  NotifySeam
}

// Module is the actions module. Construct with New, register with kernel.RegisterModule.
type Module struct {
	opts Options
}

// New builds the module.
func New(opts Options) *Module { return &Module{opts: opts} }

// Name implements kernel.Module.
func (m *Module) Name() string { return "actions" }

// Init registers the five trigger actions. No I/O.
func (m *Module) Init(k *kernel.Kernel) error {
	d := Deps{
		Store:     k.Store(),
		Logger:    k.Logger(),
		Scheduler: k.Scheduler,
		Forge:     k.Forge,
		Notifier:  k.Notifier,
		Secrets:   k.Secrets(),
		Tickets:   m.opts.Tickets,
		Notify:    m.opts.Notify,
	}
	for _, a := range All(d) {
		if err := k.RegisterAction(a); err != nil {
			return err
		}
	}
	return nil
}

// Start implements kernel.Module. The module has no background work.
func (m *Module) Start(context.Context) error { return nil }

// Stop implements kernel.Module.
func (m *Module) Stop(context.Context) error { return nil }

// ---------------------------------------------------------------- shared helpers -----

// decodeParams decodes a params blob strictly: an unknown key is an error, which save-time
// validation (triggers.validateActions → Describe) turns into a field error naming it — a
// typoed key must fail at save, not silently no-op at fire time.
func decodeParams(raw json.RawMessage, into any) error {
	if len(raw) == 0 {
		return nil
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(into); err != nil {
		return fmt.Errorf("params: %w", err)
	}
	return nil
}

// resolveAgent resolves the acting agent from params: agent_id primary (the S28 schema),
// agent_name fallback (the S15 bootstrap's suggested rules were written before agents existed
// to have IDs). The agent must belong to the project.
func resolveAgent(ctx context.Context, st *store.Store, projectID, agentID, agentName string) (domain.Agent, error) {
	if agentID != "" {
		a, err := st.Agents().ByID(ctx, agentID)
		if err != nil {
			return domain.Agent{}, fmt.Errorf("no agent %q exists; pick one from the project's roster", agentID)
		}
		if a.ProjectID != projectID {
			return domain.Agent{}, fmt.Errorf("agent %q belongs to another project", a.Name)
		}
		return a, nil
	}
	agents, err := st.Agents().ForProject(ctx, projectID)
	if err != nil {
		return domain.Agent{}, err
	}
	for _, a := range agents {
		if a.Name == agentName {
			return a, nil
		}
	}
	return domain.Agent{}, fmt.Errorf("the project has no agent named %q", agentName)
}

// agentLabel is Describe's best-effort human name for an agent reference: the stored name
// when the rule carries one, the roster name when the ID resolves, the raw ID otherwise —
// Describe never fails on a reference that might resolve at fire time (a deleted agent must
// not brick the rule card; the firing history reports it in words instead).
func agentLabel(st *store.Store, agentID, agentName string) string {
	if agentName != "" {
		return agentName
	}
	if st != nil {
		if a, err := st.Agents().ByID(context.Background(), agentID); err == nil {
			return a.Name
		}
	}
	return agentID
}

// causeRunID is the run that caused the triggering event, or "".
func causeRunID(ev domain.Event) string {
	if ev.CauseRunID != nil {
		return *ev.CauseRunID
	}
	return ""
}
