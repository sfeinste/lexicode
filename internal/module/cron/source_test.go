package cron

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/spruce/lexicode/internal/domain"
	"github.com/spruce/lexicode/internal/kernel/store"
)

// harness is a Source over a real store with a controllable clock and a capture emit.
type harness struct {
	t   *testing.T
	ctx context.Context
	st  *store.Store
	src *Source

	mu     sync.Mutex
	events []domain.Event

	now  time.Time
	proj domain.Project
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	ctx := context.Background()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	st, err := store.Open(store.Options{Path: filepath.Join(t.TempDir(), "s32.db"), Logger: logger})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	if _, err := st.Migrate(ctx); err != nil {
		t.Fatal(err)
	}

	h := &harness{t: t, ctx: ctx, st: st,
		now: time.Date(2026, 8, 17, 8, 59, 30, 0, time.UTC)} // a Monday, 08:59:30 UTC
	h.src = newSource()
	h.src.store = st
	h.src.logger = logger
	h.src.now = func() time.Time { return h.now }

	now := domain.Now()
	u := domain.User{ID: domain.NewID(), Email: "ada@example.com", DisplayName: "Ada",
		PasswordHash: "x", Role: domain.RoleOwner, AvatarColor: "#fff", CreatedAt: now}
	if err := st.Users().Create(ctx, &u); err != nil {
		t.Fatal(err)
	}
	h.proj = domain.Project{ID: domain.NewID(), Key: "PAY", Name: "Payments", Color: "#fff",
		OwnerID: u.ID, CreatedAt: now, UpdatedAt: now}
	if err := st.Projects().Create(ctx, &h.proj); err != nil {
		t.Fatal(err)
	}
	return h
}

// emit is the capture Emit the tests hand to scan.
func (h *harness) emit(_ context.Context, e domain.Event) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.events = append(h.events, e)
	return nil
}

func (h *harness) emitted() []domain.Event {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]domain.Event(nil), h.events...)
}

// mkTrigger inserts an enabled schedule trigger with the expression.
func (h *harness) mkTrigger(name, expr string) domain.Trigger {
	h.t.Helper()
	now := domain.Now()
	tr := domain.Trigger{
		ID: domain.NewID(), ProjectID: h.proj.ID, Name: name, Enabled: true,
		SourceID: sourceID, Event: eventKind,
		ActivityTypes: json.RawMessage(`["cron"]`),
		Filters:       json.RawMessage(`{}`),
		Conditions:    json.RawMessage(`{"all":[]}`),
		Actions:       json.RawMessage(`[]`),
		LoopConfig:    domain.DefaultLoopConfig(),
		Cron:          &expr,
		CreatedAt:     now, UpdatedAt: now,
	}
	if err := h.st.Triggers().Create(h.ctx, &tr); err != nil {
		h.t.Fatal(err)
	}
	return tr
}

// scanAt runs one scan with the clock set to now.
func (h *harness) scanAt(now time.Time) {
	h.t.Helper()
	h.now = now
	h.src.scan(h.ctx, h.emit)
}

func mustTime(s string) time.Time {
	t, err := time.Parse("2006-01-02 15:04:05", s)
	if err != nil {
		panic(err)
	}
	return t.UTC()
}

// TestFiresOncePerMatchingMinute: after the baseline, a due trigger fires exactly once on
// its minute — a repeated scan in the same minute adds nothing — and a non-matching minute
// emits nothing at all.
func TestFiresOncePerMatchingMinute(t *testing.T) {
	h := newHarness(t)
	tr := h.mkTrigger("standup", "0 9 * * 1-5")

	// First sighting baselines silently.
	h.scanAt(mustTime("2026-08-17 08:59:30"))
	if got := h.emitted(); len(got) != 0 {
		t.Fatalf("baseline scan emitted %d events, want 0", len(got))
	}

	// 09:00 Monday matches: exactly one event.
	h.scanAt(mustTime("2026-08-17 09:00:02"))
	got := h.emitted()
	if len(got) != 1 {
		t.Fatalf("matching minute emitted %d events, want 1", len(got))
	}
	e := got[0]
	if e.Kind != "schedule" || e.ActivityType != "cron" || e.Source != "schedule.cron" {
		t.Errorf("event = %s/%s from %s, want schedule/cron from schedule.cron",
			e.Kind, e.ActivityType, e.Source)
	}
	if e.SubjectKind != "trigger" || e.SubjectID == nil || *e.SubjectID != tr.ID {
		t.Errorf("event subject = %s/%v, want trigger/%s", e.SubjectKind, e.SubjectID, tr.ID)
	}
	var p struct {
		Schedule struct {
			Cron      string `json:"cron"`
			FiredAt   string `json:"fired_at"`
			TriggerID string `json:"trigger_id"`
		} `json:"schedule"`
	}
	if err := json.Unmarshal(e.Payload, &p); err != nil {
		t.Fatal(err)
	}
	if p.Schedule.Cron != "0 9 * * 1-5" || p.Schedule.TriggerID != tr.ID {
		t.Errorf("payload = %+v, want the trigger's expression and id", p.Schedule)
	}
	if p.Schedule.FiredAt != "2026-08-17T09:00:00.000Z" {
		t.Errorf("fired_at = %q, want the scheduled minute", p.Schedule.FiredAt)
	}

	// A second scan inside the same minute (restart-in-minute) emits nothing more.
	h.scanAt(mustTime("2026-08-17 09:00:40"))
	if got := h.emitted(); len(got) != 1 {
		t.Fatalf("re-scan in the same minute emitted %d events total, want still 1", len(got))
	}

	// A non-matching minute emits nothing.
	h.scanAt(mustTime("2026-08-17 09:01:01"))
	if got := h.emitted(); len(got) != 1 {
		t.Fatalf("non-matching minute emitted %d events total, want still 1", len(got))
	}

	// The next matching day fires again, once.
	h.scanAt(mustTime("2026-08-18 09:00:01"))
	if got := h.emitted(); len(got) != 2 {
		t.Fatalf("next day's minute emitted %d events total, want 2", len(got))
	}
}

// TestRestartCatchesUpAtMostOne: three firings missed while down, one catch-up on the next
// scan — carrying the most recent missed minute — and never the other two.
func TestRestartCatchesUpAtMostOne(t *testing.T) {
	h := newHarness(t)
	h.mkTrigger("hourly", "0 * * * *") // every hour on the hour

	h.scanAt(mustTime("2026-08-17 09:59:00")) // baseline
	h.scanAt(mustTime("2026-08-17 10:00:01")) // fires 10:00
	if got := h.emitted(); len(got) != 1 {
		t.Fatalf("before the outage: %d events, want 1", len(got))
	}

	// The process is down through 11:00, 12:00 and 13:00 — three missed firings. A NEW
	// source instance (fresh process, same store) scans at 13:20.
	restarted := newSource()
	restarted.store = h.st
	restarted.logger = h.src.logger
	now := mustTime("2026-08-17 13:20:30")
	restarted.now = func() time.Time { return now }
	restarted.scan(h.ctx, h.emit)

	got := h.emitted()
	if len(got) != 2 {
		t.Fatalf("after restart: %d events total, want exactly 2 (one catch-up)", len(got))
	}
	if got[1].OccurredAt != "2026-08-17T13:00:00.000Z" {
		t.Errorf("catch-up fired_at = %q, want the most recent missed minute 13:00", got[1].OccurredAt)
	}

	// The next scans emit nothing until 14:00 comes around.
	restarted.scan(h.ctx, h.emit)
	now = mustTime("2026-08-17 13:21:30")
	restarted.scan(h.ctx, h.emit)
	if got := h.emitted(); len(got) != 2 {
		t.Fatalf("post-catch-up scans emitted %d events total, want still 2", len(got))
	}
	now = mustTime("2026-08-17 14:00:05")
	restarted.scan(h.ctx, h.emit)
	if got := h.emitted(); len(got) != 3 {
		t.Fatalf("14:00 emitted %d events total, want 3", len(got))
	}
}

// TestTwoTriggersEachOwnSchedule: two schedule triggers with different expressions each get
// their own addressed events, on their own minutes only.
func TestTwoTriggersEachOwnSchedule(t *testing.T) {
	h := newHarness(t)
	a := h.mkTrigger("every-minute", "* * * * *")
	b := h.mkTrigger("on-the-hour", "0 10 * * *")

	h.scanAt(mustTime("2026-08-17 09:58:20")) // baseline for both
	h.scanAt(mustTime("2026-08-17 09:59:01")) // a only
	h.scanAt(mustTime("2026-08-17 10:00:01")) // both

	byTrigger := map[string][]domain.Event{}
	for _, e := range h.emitted() {
		byTrigger[*e.SubjectID] = append(byTrigger[*e.SubjectID], e)
	}
	if len(byTrigger[a.ID]) != 2 {
		t.Errorf("trigger a got %d events, want 2 (09:59 and 10:00)", len(byTrigger[a.ID]))
	}
	if len(byTrigger[b.ID]) != 1 {
		t.Errorf("trigger b got %d events, want 1 (10:00)", len(byTrigger[b.ID]))
	}
	if len(byTrigger) != 2 {
		t.Errorf("events for %d distinct triggers, want 2", len(byTrigger))
	}
	for id, evs := range byTrigger {
		want := map[string]string{a.ID: "* * * * *", b.ID: "0 10 * * *"}[id]
		for _, e := range evs {
			var p struct {
				Schedule struct {
					Cron string `json:"cron"`
				} `json:"schedule"`
			}
			if err := json.Unmarshal(e.Payload, &p); err != nil {
				t.Fatal(err)
			}
			if p.Schedule.Cron != want {
				t.Errorf("trigger %s event carries cron %q, want its own %q", id, p.Schedule.Cron, want)
			}
		}
	}
}

// TestDisabledAndForeignTriggersNeverFire: a disabled schedule trigger and a github trigger
// are invisible to the scan.
func TestDisabledAndForeignTriggersNeverFire(t *testing.T) {
	h := newHarness(t)
	tr := h.mkTrigger("off", "* * * * *")
	tr.Enabled = false
	if err := h.st.Triggers().Update(h.ctx, &tr); err != nil {
		t.Fatal(err)
	}
	now := domain.Now()
	gh := domain.Trigger{
		ID: domain.NewID(), ProjectID: h.proj.ID, Name: "gh", Enabled: true,
		SourceID: "github.poll", Event: "pull_request",
		Actions: json.RawMessage(`[]`), LoopConfig: domain.DefaultLoopConfig(),
		CreatedAt: now, UpdatedAt: now,
	}
	if err := h.st.Triggers().Create(h.ctx, &gh); err != nil {
		t.Fatal(err)
	}

	h.scanAt(mustTime("2026-08-17 09:00:00"))
	h.scanAt(mustTime("2026-08-17 09:01:00"))
	if got := h.emitted(); len(got) != 0 {
		t.Fatalf("emitted %d events, want 0", len(got))
	}
}

// TestNewTriggerBaselinesWithoutFiringThePast: a trigger created long after its would-have
// fired minutes never fires for the past, even on its first matching scan interval edge.
func TestNewTriggerBaselinesWithoutFiringThePast(t *testing.T) {
	h := newHarness(t)
	h.mkTrigger("daily", "0 9 * * *")

	// First scan happens at 09:30 — half an hour after today's would-be firing.
	h.scanAt(mustTime("2026-08-17 09:30:00"))
	if got := h.emitted(); len(got) != 0 {
		t.Fatalf("baseline emitted %d events, want 0", len(got))
	}
	// Nothing until tomorrow 09:00.
	h.scanAt(mustTime("2026-08-17 10:00:00"))
	if got := h.emitted(); len(got) != 0 {
		t.Fatalf("emitted %d events before the next scheduled minute, want 0", len(got))
	}
	h.scanAt(mustTime("2026-08-18 09:00:00"))
	if got := h.emitted(); len(got) != 1 {
		t.Fatalf("emitted %d events, want 1 at the next scheduled minute", len(got))
	}
}

// TestStartStop: the goroutine lifecycle starts, refuses a double start, and stops cleanly
// (exercised under -race by the definition of done).
func TestStartStop(t *testing.T) {
	h := newHarness(t)
	h.mkTrigger("minutely", "* * * * *")

	if err := h.src.Start(h.ctx, h.emit); err != nil {
		t.Fatal(err)
	}
	if err := h.src.Start(h.ctx, h.emit); err == nil {
		t.Fatal("second Start succeeded, want an error")
	}
	stopCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := h.src.Stop(stopCtx); err != nil {
		t.Fatal(err)
	}
	// Stop again is a no-op.
	if err := h.src.Stop(stopCtx); err != nil {
		t.Fatal(err)
	}
}
