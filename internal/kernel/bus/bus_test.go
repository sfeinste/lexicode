package bus_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/spruce/lexicode/internal/domain"
	"github.com/spruce/lexicode/internal/kernel/bus"
	"github.com/spruce/lexicode/internal/kernel/store"
	"github.com/spruce/lexicode/internal/kernel/store/seed"
)

// syncBuffer is a bytes.Buffer safe to read while subscriber goroutines log into it.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// migrated opens and migrates a fresh store on a temp file.
func migrated(t *testing.T) *store.Store {
	t.Helper()
	s, err := store.Open(store.Options{
		Path:   filepath.Join(t.TempDir(), "bus.db"),
		Logger: slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil)),
	})
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	if _, err := s.Migrate(context.Background()); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	return s
}

// newBus builds a bus over st, logging into log, and stops it at test end.
func newBus(t *testing.T, st *store.Store, log *syncBuffer, bufferSize int) *bus.Bus {
	t.Helper()
	if log == nil {
		log = &syncBuffer{}
	}
	b := bus.New(bus.Options{
		Store:      st,
		Logger:     slog.New(slog.NewTextHandler(log, nil)),
		BufferSize: bufferSize,
	})
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = b.Stop(ctx)
	})
	return b
}

// evt is a minimal publishable event.
func evt(kind, activity, dedupe string) domain.Event {
	return domain.Event{
		ID:           domain.NewID(),
		Source:       "test",
		Kind:         kind,
		ActivityType: activity,
		ActorKind:    domain.ActorSystem,
		SubjectKind:  "repo",
		Payload:      json.RawMessage(`{}`),
		DedupeKey:    dedupe,
	}
}

// recorder is a subscriber that remembers what it saw.
type recorder struct {
	mu   sync.Mutex
	seen []domain.Event
}

func (r *recorder) handle(_ context.Context, e domain.Event) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.seen = append(r.seen, e)
	return nil
}

func (r *recorder) count() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.seen)
}

func (r *recorder) dedupeKeys() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	keys := make([]string, 0, len(r.seen))
	for _, e := range r.seen {
		keys = append(keys, e.DedupeKey)
	}
	return keys
}

// waitFor polls cond until it holds or the test fails.
func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

// dispatchState reads one event's dispatch_state.
func dispatchState(t *testing.T, st *store.Store, id string) domain.DispatchState {
	t.Helper()
	e, err := st.Events().ByID(context.Background(), id)
	if err != nil {
		t.Fatalf("read event %s: %v", id, err)
	}
	return e.DispatchState
}

// waitDone waits for an event's dispatch to be recorded 'done'.
func waitDone(t *testing.T, st *store.Store, id string) {
	t.Helper()
	waitFor(t, "event "+id+" to be done", func() bool {
		return dispatchState(t, st, id) == domain.DispatchDone
	})
}

func TestPublishDedupesOnKey(t *testing.T) {
	ctx := context.Background()
	st := migrated(t)
	b := newBus(t, st, nil, 0)

	rec := &recorder{}
	if err := b.SubscribeKind("tickets", "ticket", rec.handle); err != nil {
		t.Fatalf("subscribe: %v", err)
	}

	first := evt("ticket", "created", "dup-key")
	if err := b.Publish(ctx, first); err != nil {
		t.Fatalf("first publish: %v", err)
	}
	waitDone(t, st, first.ID)

	second := evt("ticket", "created", "dup-key")
	err := b.Publish(ctx, second)
	if !errors.Is(err, bus.ErrDuplicate) {
		t.Fatalf("second publish with the same dedupe_key returned %v; want ErrDuplicate", err)
	}
	if _, err := st.Events().ByID(ctx, second.ID); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("the duplicate was inserted anyway (ByID err = %v)", err)
	}
	if got := rec.count(); got != 1 {
		t.Fatalf("subscriber saw the event %d times; want exactly once", got)
	}

	if err := b.Publish(ctx, evt("ticket", "created", "")); err == nil {
		t.Fatal("publishing without a dedupe key must be rejected")
	}
}

func TestKindAndTopicRouting(t *testing.T) {
	ctx := context.Background()
	st := migrated(t)
	b := newBus(t, st, nil, 0)

	kindSub, topicSub, completions := &recorder{}, &recorder{}, &recorder{}
	if err := b.SubscribeKind("tickets", "ticket", kindSub.handle); err != nil {
		t.Fatalf("subscribe kind: %v", err)
	}
	if err := b.SubscribeTopic("ticket-any", "ticket.*", topicSub.handle); err != nil {
		t.Fatalf("subscribe topic: %v", err)
	}
	if err := b.SubscribeTopic("completions", "*.completed", completions.handle); err != nil {
		t.Fatalf("subscribe completions: %v", err)
	}
	if err := b.SubscribeTopic("bad", "[", (&recorder{}).handle); err == nil {
		t.Fatal("a malformed topic pattern must be rejected at subscribe time")
	}

	ticket := evt("ticket", "created", "route-ticket")
	run := evt("run", "completed", "route-run")
	pr := evt("pull_request", "opened", "route-pr")
	for _, e := range []domain.Event{ticket, run, pr} {
		if err := b.Publish(ctx, e); err != nil {
			t.Fatalf("publish %s: %v", bus.Topic(e), err)
		}
	}
	waitDone(t, st, ticket.ID)
	waitDone(t, st, run.ID)
	// Nobody subscribes to pull_request events: the dispatch is vacuously done, so a boot never
	// re-delivers it to subscribers that will not want it either.
	waitDone(t, st, pr.ID)

	if got := kindSub.dedupeKeys(); len(got) != 1 || got[0] != "route-ticket" {
		t.Errorf("kind subscriber saw %v; want [route-ticket]", got)
	}
	if got := topicSub.dedupeKeys(); len(got) != 1 || got[0] != "route-ticket" {
		t.Errorf("topic subscriber saw %v; want [route-ticket]", got)
	}
	if got := completions.dedupeKeys(); len(got) != 1 || got[0] != "route-run" {
		t.Errorf("completions subscriber saw %v; want [route-run]", got)
	}
}

func TestPanickingSubscriberFailsDispatchNotBus(t *testing.T) {
	ctx := context.Background()
	st := migrated(t)
	log := &syncBuffer{}
	b := newBus(t, st, log, 0)

	rec := &recorder{}
	if err := b.SubscribeKind("panicky", "ticket", func(ctx context.Context, e domain.Event) error {
		if e.DedupeKey == "boom" {
			panic("subscriber exploded")
		}
		return rec.handle(ctx, e)
	}); err != nil {
		t.Fatalf("subscribe: %v", err)
	}

	boom := evt("ticket", "created", "boom")
	if err := b.Publish(ctx, boom); err != nil {
		t.Fatalf("publish: %v", err)
	}
	waitFor(t, "the panicked dispatch to be marked failed", func() bool {
		return dispatchState(t, st, boom.ID) == domain.DispatchFailed
	})
	if !strings.Contains(log.String(), "panicked") {
		t.Errorf("the panic was not logged:\n%s", log.String())
	}

	// The bus and the subscriber's goroutine are both still alive: the next event is delivered
	// and acked normally.
	after := evt("ticket", "created", "after-boom")
	if err := b.Publish(ctx, after); err != nil {
		t.Fatalf("publish after panic: %v", err)
	}
	waitDone(t, st, after.ID)
	if got := rec.dedupeKeys(); len(got) != 1 || got[0] != "after-boom" {
		t.Fatalf("subscriber after the panic saw %v; want [after-boom]", got)
	}

	stats := b.Stats()
	if len(stats) != 1 || stats[0].Failed != 1 || stats[0].Processed != 2 {
		t.Errorf("stats = %+v; want the panicky subscriber with Processed=2 Failed=1", stats)
	}
}

func TestOneFailingSubscriberMarksFailedOthersStillDeliver(t *testing.T) {
	ctx := context.Background()
	st := migrated(t)
	log := &syncBuffer{}
	b := newBus(t, st, log, 0)

	healthy := &recorder{}
	if err := b.SubscribeKind("failing", "ticket", func(context.Context, domain.Event) error {
		return errors.New("handler said no")
	}); err != nil {
		t.Fatalf("subscribe failing: %v", err)
	}
	if err := b.SubscribeKind("healthy", "ticket", healthy.handle); err != nil {
		t.Fatalf("subscribe healthy: %v", err)
	}

	e := evt("ticket", "created", "half-fails")
	if err := b.Publish(ctx, e); err != nil {
		t.Fatalf("publish: %v", err)
	}
	waitFor(t, "the dispatch to be marked failed", func() bool {
		return dispatchState(t, st, e.ID) == domain.DispatchFailed
	})
	waitFor(t, "the healthy subscriber to see the event", func() bool { return healthy.count() == 1 })
	if !strings.Contains(log.String(), "handler said no") {
		t.Errorf("the subscriber error was not logged:\n%s", log.String())
	}
}

func TestBootRecoveryRedispatchesExactlyThePendingRows(t *testing.T) {
	ctx := context.Background()
	st := migrated(t)

	// A previous process persisted these and crashed mid-dispatch: two still pending, one that
	// finished. The fourth simulates a row created by the *current* process while recovery
	// runs; recovery must leave it to its own in-flight dispatch.
	insert := func(dedupe, createdAt string, state domain.DispatchState) domain.Event {
		e := evt("ticket", "created", dedupe)
		e.OccurredAt = createdAt
		e.CreatedAt = createdAt
		e.DispatchState = state
		if err := st.Events().Insert(ctx, &e); err != nil {
			t.Fatalf("insert fixture %s: %v", dedupe, err)
		}
		return e
	}
	past := "2026-01-01T00:00:00.000Z"
	pending1 := insert("crash-1", past, domain.DispatchPending)
	pending2 := insert("crash-2", past, domain.DispatchPending)
	done := insert("crash-done", past, domain.DispatchDone)
	future := insert("crash-future", "2100-01-01T00:00:00.000Z", domain.DispatchPending)

	rec := &recorder{}
	b := newBus(t, st, nil, 0)
	if err := b.SubscribeKind("tickets", "ticket", rec.handle); err != nil {
		t.Fatalf("subscribe: %v", err)
	}
	if err := b.Start(ctx); err != nil {
		t.Fatalf("start: %v", err)
	}
	if err := b.Start(ctx); err == nil {
		t.Fatal("a second Start must be refused")
	}

	waitDone(t, st, pending1.ID)
	waitDone(t, st, pending2.ID)

	got := rec.dedupeKeys()
	want := map[string]bool{"crash-1": true, "crash-2": true}
	if len(got) != 2 || !want[got[0]] || !want[got[1]] || got[0] == got[1] {
		t.Fatalf("recovery delivered %v; want exactly crash-1 and crash-2", got)
	}
	if s := dispatchState(t, st, done.ID); s != domain.DispatchDone {
		t.Errorf("the already-done row changed state to %s", s)
	}
	if s := dispatchState(t, st, future.ID); s != domain.DispatchPending {
		t.Errorf("the row created after boot was touched by recovery (state %s)", s)
	}
}

func TestSlowConsumerBlocksWithVisibleLagOthersUnaffected(t *testing.T) {
	ctx := context.Background()
	st := migrated(t)
	log := &syncBuffer{}
	b := newBus(t, st, log, 1) // queue depth 1, so the third publish must block

	release := make(chan struct{})
	slow := &recorder{}
	if err := b.SubscribeKind("slow", "ticket", func(ctx context.Context, e domain.Event) error {
		<-release
		return slow.handle(ctx, e)
	}); err != nil {
		t.Fatalf("subscribe slow: %v", err)
	}
	fast := &recorder{}
	if err := b.SubscribeKind("fast", "ticket", fast.handle); err != nil {
		t.Fatalf("subscribe fast: %v", err)
	}

	events := []domain.Event{
		evt("ticket", "created", "slow-1"),
		evt("ticket", "created", "slow-2"),
		evt("ticket", "created", "slow-3"),
	}
	published := make(chan error, 1)
	go func() {
		for _, e := range events {
			if err := b.Publish(ctx, e); err != nil {
				published <- err
				return
			}
		}
		published <- nil
	}()

	// The fast subscriber gets all three while the slow one has finished none: per-subscriber
	// goroutines mean a slow consumer starves only itself.
	waitFor(t, "the fast subscriber to see all three events", func() bool { return fast.count() == 3 })
	if got := slow.count(); got != 0 {
		t.Fatalf("slow subscriber finished %d deliveries while blocked; want 0", got)
	}

	// The backlog is observable, and the block was logged, not dropped.
	waitFor(t, "the slow subscriber's lag to be visible", func() bool {
		for _, s := range b.Stats() {
			if s.Name == "slow" && s.Lag >= 2 {
				return true
			}
		}
		return false
	})
	if b.Lag() < 2 {
		t.Errorf("Bus.Lag() = %d; want at least the slow subscriber's backlog", b.Lag())
	}
	waitFor(t, "the slow-consumer warning to be logged", func() bool {
		return strings.Contains(log.String(), "slow consumer")
	})
	select {
	case err := <-published:
		t.Fatalf("publish returned (%v) while the slow queue was full; it should be blocking", err)
	default:
	}

	// Unblock: everything drains, every dispatch completes, nothing was dropped.
	close(release)
	if err := <-published; err != nil {
		t.Fatalf("publish after unblocking: %v", err)
	}
	for _, e := range events {
		waitDone(t, st, e.ID)
	}
	waitFor(t, "the slow subscriber to catch up", func() bool { return slow.count() == 3 })
	waitFor(t, "the lag to drain to zero", func() bool { return b.Lag() == 0 })
}

func TestEmitSetsCauseRunSourceAndDedupe(t *testing.T) {
	ctx := context.Background()
	st := migrated(t)
	d, err := seed.Apply(ctx, st)
	if err != nil {
		t.Fatalf("seed: %v", err)
	}
	run := d.Runs[0]

	b := newBus(t, st, nil, 0)
	rec := &recorder{}
	if err := b.SubscribeTopic("run-completions", "run.completed", rec.handle); err != nil {
		t.Fatalf("subscribe: %v", err)
	}

	if err := b.Emit(bus.WithCauseRun(ctx, run.ID), domain.Event{
		ProjectID:    &d.Project.ID,
		Kind:         "run",
		ActivityType: "completed",
		ActorKind:    domain.ActorAgent,
		SubjectKind:  "run",
		SubjectID:    &run.ID,
		Payload:      json.RawMessage(fmt.Sprintf(`{"run":{"id":%q}}`, run.ID)),
	}); err != nil {
		t.Fatalf("emit: %v", err)
	}

	waitFor(t, "the internal event to arrive", func() bool { return rec.count() == 1 })
	rec.mu.Lock()
	got := rec.seen[0]
	rec.mu.Unlock()

	if got.Source != bus.SourceInternal {
		t.Errorf("Source = %q; want %q", got.Source, bus.SourceInternal)
	}
	if got.CauseRunID == nil || *got.CauseRunID != run.ID {
		t.Errorf("CauseRunID = %v; want the run from WithCauseRun (%s)", got.CauseRunID, run.ID)
	}
	if got.DedupeKey != bus.SourceInternal+":"+got.ID {
		t.Errorf("DedupeKey = %q; want the documented internal:<ulid> shape", got.DedupeKey)
	}
	waitDone(t, st, got.ID)

	// Without WithCauseRun the causality edge stays empty rather than inventing one.
	if err := b.Emit(ctx, domain.Event{
		Kind: "ticket", ActivityType: "created", SubjectKind: "ticket",
	}); err != nil {
		t.Fatalf("emit without cause: %v", err)
	}
	waitFor(t, "the uncaused event to be recorded", func() bool {
		pending, err := st.Events().ListPending(context.Background())
		return err == nil && len(pending) == 0
	})
}

func TestStoppedBusRefusesWork(t *testing.T) {
	ctx := context.Background()
	st := migrated(t)
	b := newBus(t, st, nil, 0)
	if err := b.SubscribeKind("tickets", "ticket", (&recorder{}).handle); err != nil {
		t.Fatalf("subscribe: %v", err)
	}
	if err := b.SubscribeKind("tickets", "ticket", (&recorder{}).handle); err == nil {
		t.Fatal("duplicate subscription names must be rejected")
	}

	if err := b.Stop(ctx); err != nil {
		t.Fatalf("stop: %v", err)
	}
	if err := b.Stop(ctx); err != nil {
		t.Fatalf("second stop must be a no-op, got: %v", err)
	}
	if err := b.Publish(ctx, evt("ticket", "created", "too-late")); !errors.Is(err, bus.ErrStopped) {
		t.Fatalf("publish after stop returned %v; want ErrStopped", err)
	}
	if err := b.SubscribeKind("late", "ticket", (&recorder{}).handle); !errors.Is(err, bus.ErrStopped) {
		t.Fatalf("subscribe after stop returned %v; want ErrStopped", err)
	}
}
