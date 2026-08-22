// env_test.go is the S28 acceptance harness: the five actions exercised through the REAL
// trigger pipeline — real store, real bus, real engine, real guard layers, a real scheduler
// (never Started, so enqueued runs stay `queued` and assertions are deterministic), the real
// tickets and notify services behind the injected seams, and a fake forge standing in for
// GitHub's write API only.
package actions_test

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/spruce/lexicode/internal/domain"
	"github.com/spruce/lexicode/internal/kernel/audit"
	"github.com/spruce/lexicode/internal/kernel/bus"
	"github.com/spruce/lexicode/internal/kernel/guard"
	"github.com/spruce/lexicode/internal/kernel/ports"
	"github.com/spruce/lexicode/internal/kernel/sched"
	"github.com/spruce/lexicode/internal/kernel/secrets"
	"github.com/spruce/lexicode/internal/kernel/store"
	actionsmod "github.com/spruce/lexicode/internal/module/actions"
	notifymod "github.com/spruce/lexicode/internal/module/notify"
	notifysvc "github.com/spruce/lexicode/internal/service/notify"
	ticketsvc "github.com/spruce/lexicode/internal/service/tickets"
	triggersvc "github.com/spruce/lexicode/internal/service/triggers"
)

// fakeSource is the catalog the engine derives subject keys from and save-time validation
// checks WHEN clauses against.
type fakeSource struct{}

func (fakeSource) ID() string { return "github.poll" }
func (fakeSource) Catalog() ports.EventCatalog {
	return ports.EventCatalog{Events: []ports.EventDescriptor{
		{
			Kind: "pull_request", Label: "Pull request",
			ActivityTypes: []ports.ActivityType{{Value: "opened"}, {Value: "synchronize"}},
			SubjectKey:    "pr:{{pr.number}}",
		},
		{
			Kind: "issue_comment", Label: "PR comment",
			ActivityTypes: []ports.ActivityType{{Value: "created"}},
			SubjectKey:    "pr:{{pr.number}}",
		},
	}}
}
func (fakeSource) Start(context.Context, ports.Emit) error { return nil }
func (fakeSource) Stop(context.Context) error              { return nil }

// fakeForge records CommentOnPullRequest calls and appends the D-9 marker the way the real
// adapter does. The embedded interface panics on everything the tests never call.
type fakeForge struct {
	ports.ForgeProvider
	mu       sync.Mutex
	comments []postedComment
}

type postedComment struct {
	PR    int
	Body  string
	Actor domain.Actor
}

func (f *fakeForge) ID() string { return "github" }

func (f *fakeForge) CommentOnPullRequest(_ context.Context, _ ports.Creds, _ domain.RepoRef, a domain.Actor, n int, body string) (domain.Comment, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	full := body + "\n\n" + a.Marker()
	f.comments = append(f.comments, postedComment{PR: n, Body: full, Actor: a})
	return domain.Comment{ID: int64(len(f.comments)), SubjectNumber: n, Body: full}, nil
}

func (f *fakeForge) posted() []postedComment {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]postedComment(nil), f.comments...)
}

type env struct {
	t      *testing.T
	ctx    context.Context
	st     *store.Store
	bus    *bus.Bus
	sch    *sched.Scheduler
	sec    *secrets.Store
	forge  *fakeForge
	tick   *ticketsvc.Service
	notify *notifysvc.Service
	trg    *triggersvc.Service

	owner domain.User
	proj  domain.Project

	clock int
}

func newEnv(t *testing.T) *env {
	t.Helper()
	ctx := context.Background()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	dir := t.TempDir()
	st, err := store.Open(store.Options{Path: filepath.Join(dir, "s28.db"), Logger: logger})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	if _, err := st.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	b := bus.New(bus.Options{Store: st, Logger: logger})
	auditW := audit.New(audit.Options{Store: st, Logger: logger})
	sec, err := secrets.Open(secrets.Options{
		Store: st, KeyPath: filepath.Join(dir, "master.key"), Logger: logger,
	})
	if err != nil {
		t.Fatal(err)
	}

	// The scheduler is the run creator and the guard's LoopStopper. Never Started.
	scheduler := sched.New(sched.Options{
		Store: st, Bus: b, Audit: auditW, Logger: logger, SandboxID: "fake",
	})

	tickSvc := ticketsvc.New(ticketsvc.Options{Store: st, Audit: auditW, Bus: b, Logger: logger})
	ntfSvc := notifysvc.New(notifysvc.Options{Store: st, Bus: b, Logger: logger})
	forge := &fakeForge{}
	inapp := notifymod.NewInApp(ntfSvc.DeliverInApp)

	// The module's Deps exactly as cmd/lexicode wires them, minus the kernel indirection.
	deps := actionsmod.Deps{
		Store:     st,
		Logger:    logger,
		Scheduler: func() *sched.Scheduler { return scheduler },
		Forge: func(id string) (ports.ForgeProvider, error) {
			if id == forge.ID() {
				return forge, nil
			}
			return nil, fmt.Errorf("no forge %q", id)
		},
		Notifier: func(id string) (ports.Notifier, error) {
			if id == inapp.ID() {
				return inapp, nil
			}
			return nil, fmt.Errorf("no notifier %q", id)
		},
		Secrets: sec,
		Tickets: actionsmod.TicketSeam{
			CreateInTriage: func(ctx context.Context, in actionsmod.TriageCreate) (domain.Ticket, error) {
				return tickSvc.CreateFromTrigger(ctx, ticketsvc.TriggerCreateInput{
					ProjectID: in.ProjectID, Title: in.Title, Description: in.Description,
					LabelNames: in.LabelNames, Provenance: in.Provenance,
					SourceTriggerID: in.TriggerID, SourceRunID: in.RunID,
				})
			},
			MoveToCategory: tickSvc.TriggerMoveToCategory,
		},
		Notify: actionsmod.NotifySeam{RouteRun: ntfSvc.RouteTo},
	}
	registry := map[string]ports.TriggerAction{}
	for _, a := range actionsmod.All(deps) {
		registry[a.ID()] = a
	}
	lookup := func(id string) (ports.TriggerAction, error) {
		if a, ok := registry[id]; ok {
			return a, nil
		}
		return nil, fmt.Errorf("no action %q", id)
	}
	sources := func() []ports.EventSource { return []ports.EventSource{fakeSource{}} }

	loopGuard := guard.New(guard.Options{Store: st, Loop: scheduler, Logger: logger})
	engine := triggersvc.NewEngine(triggersvc.EngineOptions{
		Store: st, Bus: b, Logger: logger,
		Guard: loopGuard, Sources: sources, Action: lookup,
	})
	if err := engine.Subscribe(b); err != nil {
		t.Fatal(err)
	}
	if err := b.Start(ctx); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		stopCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = b.Stop(stopCtx)
		_ = engine.Stop(stopCtx)
	})

	trgSvc := triggersvc.New(triggersvc.Options{
		Store: st, Audit: auditW, Logger: logger, Sources: sources, Action: lookup,
	})

	e := &env{t: t, ctx: ctx, st: st, bus: b, sch: scheduler, sec: sec,
		forge: forge, tick: tickSvc, notify: ntfSvc, trg: trgSvc}

	now := domain.Now()
	e.owner = domain.User{
		ID: domain.NewID(), Email: "ada@example.com", DisplayName: "Ada", PasswordHash: "x",
		Role: domain.RoleOwner, AvatarColor: "#7c5cff", CreatedAt: now,
	}
	if err := st.Users().Create(ctx, &e.owner); err != nil {
		t.Fatal(err)
	}
	e.proj = domain.Project{ID: domain.NewID(), Key: "PAY", Name: "Payments", Color: "#fff",
		OwnerID: e.owner.ID, CreatedAt: now, UpdatedAt: now}
	if err := st.Projects().Create(ctx, &e.proj); err != nil {
		t.Fatal(err)
	}
	return e
}

func (e *env) mkUser(name string) domain.User {
	e.t.Helper()
	u := domain.User{
		ID: domain.NewID(), Email: name + "@example.com", DisplayName: name, PasswordHash: "x",
		Role: domain.RoleMember, AvatarColor: "#888", CreatedAt: domain.Now(),
	}
	if err := e.st.Users().Create(e.ctx, &u); err != nil {
		e.t.Fatal(err)
	}
	return u
}

func (e *env) mkAgent(name string) domain.Agent {
	e.t.Helper()
	now := domain.Now()
	a := domain.Agent{
		ID: domain.NewID(), ProjectID: e.proj.ID, Name: name, Role: "developer",
		Color: "#888", RuntimeID: "scripted", Model: "fake", Effort: "medium",
		Autonomy: domain.AutonomyAuto, GitAuthorName: name,
		GitAuthorEmail: name + "@agents.lexicode.local",
		ConcurrencyCap: 4, MaxWallClockSeconds: 300, MaxSteps: 100, Enabled: true,
		CreatedAt: now, UpdatedAt: now,
	}
	if err := e.st.Agents().Create(e.ctx, &a); err != nil {
		e.t.Fatal(err)
	}
	return a
}

func (e *env) mkColumn(name string, cat domain.ColumnCategory, pos int64) domain.Column {
	e.t.Helper()
	now := domain.Now()
	c := domain.Column{ID: domain.NewID(), ProjectID: e.proj.ID, Name: name, Category: cat,
		Position: pos, CreatedAt: now, UpdatedAt: now}
	if err := e.st.Columns().Create(e.ctx, &c); err != nil {
		e.t.Fatal(err)
	}
	return c
}

// mkTrigger inserts an enabled trigger straight through the repo.
func (e *env) mkTrigger(name, event, activities, loopConfig, actions string) domain.Trigger {
	e.t.Helper()
	now := domain.Now()
	tr := domain.Trigger{
		ID: domain.NewID(), ProjectID: e.proj.ID, Name: name, Enabled: true,
		SourceID: "github.poll", Event: event,
		ActivityTypes: json.RawMessage(activities), Filters: json.RawMessage(`{}`),
		Conditions: json.RawMessage(`{"all":[]}`),
		Actions:    json.RawMessage(actions),
		LoopConfig: json.RawMessage(loopConfig), CreatedAt: now, UpdatedAt: now,
	}
	if err := e.st.Triggers().Create(e.ctx, &tr); err != nil {
		e.t.Fatal(err)
	}
	return tr
}

// emit publishes one event with a deterministic, strictly increasing occurred_at.
func (e *env) emit(kind, activity string, actor domain.ActorKind, actorID, causeRun *string, payload map[string]any) domain.Event {
	e.t.Helper()
	raw, err := json.Marshal(payload)
	if err != nil {
		e.t.Fatal(err)
	}
	e.clock++
	occurred := domain.FormatTime(
		time.Date(2026, 8, 21, 12, 0, 0, 0, time.UTC).Add(time.Duration(e.clock) * time.Second))
	ev := domain.Event{
		ID: domain.NewID(), ProjectID: &e.proj.ID, Source: "github.poll",
		Kind: kind, ActivityType: activity, ActorKind: actor, ActorID: actorID,
		SubjectKind: "pr",
		Payload:     raw, CauseRunID: causeRun,
		DedupeKey: "t:" + domain.NewID(), OccurredAt: occurred,
		CreatedAt: domain.Now(),
	}
	if pr, ok := payload["pr"].(map[string]any); ok {
		if n, ok := pr["number"].(int); ok {
			num := int64(n)
			ev.SubjectNumber = &num
		}
	}
	if err := e.bus.Emit(e.ctx, ev); err != nil {
		e.t.Fatal(err)
	}
	return ev
}

// prPayload is the usual pull_request payload.
func prPayload(number int) map[string]any {
	return map[string]any{"pr": map[string]any{"number": number, "body": "hello"}}
}

// firing waits for the (trigger, event) firing row.
func (e *env) firing(triggerID, eventID string) domain.TriggerFiring {
	e.t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		firings, err := e.st.Firings().ForTrigger(e.ctx, triggerID, 100)
		if err != nil {
			e.t.Fatal(err)
		}
		for _, f := range firings {
			if f.EventID == eventID {
				return f
			}
		}
		time.Sleep(5 * time.Millisecond)
	}
	e.t.Fatalf("no firing for trigger %s event %s", triggerID, eventID)
	return domain.TriggerFiring{}
}

func (e *env) run(id string) domain.Run {
	e.t.Helper()
	r, err := e.st.Runs().ByID(e.ctx, id)
	if err != nil {
		e.t.Fatal(err)
	}
	return r
}
