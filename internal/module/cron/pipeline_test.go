// pipeline_test.go is the S32 acceptance harness across the boundary: the cron source's
// events driven through the REAL bus and the REAL trigger engine, proving the per-trigger
// addressing design end to end — and the save-time validation through the real triggers
// service, proving an invalid expression is refused with the bad segment named.
package cron_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/spruce/lexicode/internal/domain"
	"github.com/spruce/lexicode/internal/kernel/audit"
	"github.com/spruce/lexicode/internal/kernel/bus"
	"github.com/spruce/lexicode/internal/kernel/ports"
	"github.com/spruce/lexicode/internal/kernel/store"
	cronmod "github.com/spruce/lexicode/internal/module/cron"
	triggersvc "github.com/spruce/lexicode/internal/service/triggers"
)

type env struct {
	t   *testing.T
	ctx context.Context
	st  *store.Store
	bus *bus.Bus
	src *cronmod.Source
	svc *triggersvc.Service

	now  time.Time
	proj domain.Project
	user domain.User
}

func newEnv(t *testing.T) *env {
	t.Helper()
	ctx := context.Background()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	st, err := store.Open(store.Options{Path: filepath.Join(t.TempDir(), "s32e.db"), Logger: logger})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	if _, err := st.Migrate(ctx); err != nil {
		t.Fatal(err)
	}

	e := &env{t: t, ctx: ctx, st: st, now: time.Date(2026, 8, 17, 8, 59, 0, 0, time.UTC)}

	mod := cronmod.New(cronmod.Options{
		Store: st, Logger: logger, Now: func() time.Time { return e.now },
	})
	e.src = mod.Source()
	sources := func() []ports.EventSource { return []ports.EventSource{e.src} }

	b := bus.New(bus.Options{Store: st, Logger: logger})
	e.bus = b
	engine := triggersvc.NewEngine(triggersvc.EngineOptions{
		Store: st, Bus: b, Logger: logger, Sources: sources,
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

	auditW := audit.New(audit.Options{Store: st, Logger: logger})
	e.svc = triggersvc.New(triggersvc.Options{
		Store: st, Audit: auditW, Logger: logger, Sources: sources,
	})

	now := domain.Now()
	e.user = domain.User{ID: domain.NewID(), Email: "ada@example.com", DisplayName: "Ada",
		PasswordHash: "x", Role: domain.RoleOwner, AvatarColor: "#fff", CreatedAt: now}
	if err := st.Users().Create(ctx, &e.user); err != nil {
		t.Fatal(err)
	}
	e.proj = domain.Project{ID: domain.NewID(), Key: "PAY", Name: "Payments", Color: "#fff",
		OwnerID: e.user.ID, CreatedAt: now, UpdatedAt: now}
	if err := st.Projects().Create(ctx, &e.proj); err != nil {
		t.Fatal(err)
	}
	return e
}

// scanAt runs one source scan at the given clock, publishing onto the real bus, then waits
// for the engine to drain.
func (e *env) scanAt(s string) {
	e.t.Helper()
	now, err := time.Parse("2006-01-02 15:04:05", s)
	if err != nil {
		e.t.Fatal(err)
	}
	e.now = now.UTC()
	emit := func(ctx context.Context, ev domain.Event) error {
		if err := e.bus.Publish(ctx, ev); err != nil && !errors.Is(err, bus.ErrDuplicate) {
			return err
		}
		return nil
	}
	e.src.ScanForTest(e.ctx, emit)
}

// waitFirings polls until the trigger has n firing rows (the engine writes them async of the
// bus ack) and returns them; it fails the test if the count never settles at n.
func (e *env) waitFirings(triggerID string, n int) []domain.TriggerFiring {
	e.t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	var last []domain.TriggerFiring
	for time.Now().Before(deadline) {
		var err error
		last, err = e.st.Firings().ForTrigger(e.ctx, triggerID, 50)
		if err != nil {
			e.t.Fatal(err)
		}
		if len(last) == n {
			return last
		}
		time.Sleep(10 * time.Millisecond)
	}
	e.t.Fatalf("trigger %s has %d firings, want %d: %+v", triggerID, len(last), n, last)
	return nil
}

func strp(s string) *string { return &s }

// TestPerTriggerMatchingThroughTheEngine: two schedule triggers with different expressions;
// each fires only on its own schedule, with exactly one firing row per (trigger, minute) —
// the addressed-event design working through the real pipeline.
func TestPerTriggerMatchingThroughTheEngine(t *testing.T) {
	e := newEnv(t)

	mk := func(name, expr string) domain.Trigger {
		tr, err := e.svc.Create(e.ctx, e.proj.Key, triggersvc.Input{
			Name:          strp(name),
			SourceID:      strp("schedule.cron"),
			Event:         strp("schedule"),
			ActivityTypes: &[]string{"cron"},
			Cron:          strp(expr),
		}, e.user.ID)
		if err != nil {
			t.Fatal(err)
		}
		return tr
	}
	a := mk("minutely", "* * * * *")
	b := mk("at-ten", "0 10 * * *")

	e.scanAt("2026-08-17 09:58:10") // baseline
	e.scanAt("2026-08-17 09:59:05") // a's minute
	e.scanAt("2026-08-17 10:00:05") // both triggers' minute

	e.waitFirings(a.ID, 2) // 09:59 and 10:00
	e.waitFirings(b.ID, 1) // 10:00 only

	// Settle, then re-read: the counts must not creep past the expectation — a firing of b
	// on a's minutes (or vice versa) is exactly the bug the addressed events prevent.
	time.Sleep(150 * time.Millisecond)
	fa := e.waitFirings(a.ID, 2)
	fb := e.waitFirings(b.ID, 1)
	// No actions configured: the outcome class is no_action with the reason in words —
	// still a firing row, which is the §8 point.
	for _, f := range append(fa, fb...) {
		if f.Outcome != domain.FiringNoAction {
			t.Errorf("firing outcome = %s, want no_action", f.Outcome)
		}
	}
	// And the cross-check that b's firing came from b's own addressed event, not a's.
	ev, err := e.st.Events().ByID(e.ctx, fb[0].EventID)
	if err != nil {
		t.Fatal(err)
	}
	if ev.SubjectID == nil || *ev.SubjectID != b.ID {
		t.Errorf("b's firing event subject = %v, want b's own id", ev.SubjectID)
	}
	var p struct {
		Schedule struct {
			Cron string `json:"cron"`
		} `json:"schedule"`
	}
	if err := json.Unmarshal(ev.Payload, &p); err != nil {
		t.Fatal(err)
	}
	if p.Schedule.Cron != "0 10 * * *" {
		t.Errorf("b's firing event carries cron %q, want b's own expression", p.Schedule.Cron)
	}
}

// TestSaveTimeValidation: the trigger CRUD refuses a bad expression with a field error
// naming the offending segment, refuses a schedule trigger without one, and accepts the
// valid form — through ports.TriggerVetter, no service→module import.
func TestSaveTimeValidation(t *testing.T) {
	e := newEnv(t)

	create := func(cron *string) error {
		_, err := e.svc.Create(e.ctx, e.proj.Key, triggersvc.Input{
			Name:          strp("standup"),
			SourceID:      strp("schedule.cron"),
			Event:         strp("schedule"),
			ActivityTypes: &[]string{"cron"},
			Cron:          cron,
		}, e.user.ID)
		return err
	}

	err := create(strp("0 25 * * *"))
	var ve *triggersvc.ValidationError
	if !errors.As(err, &ve) {
		t.Fatalf("invalid cron: err = %v, want a ValidationError", err)
	}
	found := false
	for _, f := range ve.Fields {
		if f.Field == "cron" && strings.Contains(f.Message, "hour: 25 is out of range 0-23") {
			found = true
		}
	}
	if !found {
		t.Errorf("validation fields = %+v, want a cron error naming the hour segment", ve.Fields)
	}

	if err := create(nil); !errors.As(err, &ve) {
		t.Fatalf("missing cron: err = %v, want a ValidationError", err)
	} else {
		found := false
		for _, f := range ve.Fields {
			if f.Field == "cron" && strings.Contains(f.Message, "required") {
				found = true
			}
		}
		if !found {
			t.Errorf("validation fields = %+v, want a cron-required error", ve.Fields)
		}
	}

	if err := create(strp("0 9 * * 1-5")); err != nil {
		t.Fatalf("valid cron refused: %v", err)
	}
}
