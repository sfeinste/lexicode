// Package bus is the persist-then-dispatch event bus (architecture §6, D-13, story S04).
//
// Publish writes the event to the events table first — idempotent on dedupe_key; a repeat
// returns ErrDuplicate and nothing is delivered twice — and only then fans out to in-process
// subscribers, each consuming from its own buffered channel on its own goroutine.
// events.dispatch_state records the outcome: 'done' once every matching subscriber has returned
// nil, 'failed' once any of them returned an error or panicked. Start re-dispatches rows a
// previous process left 'pending', which is why every subscriber must be idempotent (the trigger
// engine's protection is its unique index on (trigger_id, event_id)).
//
// Slow consumers are never dropped — a dropped trigger event is a missing agent run. When a
// subscriber's buffer is full the publisher logs and blocks, and the backlog stays visible in
// Stats, which is what the module status surface reports.
package bus

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"path"
	"runtime/debug"
	"sync"
	"sync/atomic"
	"time"

	"github.com/spruce/lexicode/internal/domain"
	"github.com/spruce/lexicode/internal/kernel/store"
)

// SourceInternal is events.source for events emitted by this process's own services, as opposed
// to an EventSource adapter.
const SourceInternal = "internal"

// DefaultBufferSize is each subscriber's queue depth when Options.BufferSize is zero.
const DefaultBufferSize = 64

// finalizeTimeout bounds the dispatch_state write that ends a dispatch. It runs detached from
// the publisher's context so that an outcome is recorded even during shutdown.
const finalizeTimeout = 5 * time.Second

var (
	// ErrDuplicate is returned by Publish when the event's dedupe_key is already in the events
	// table. The event was dropped, exactly as D-13 specifies; for a poller this is the normal
	// idempotency path, not a failure.
	ErrDuplicate = errors.New("duplicate event")
	// ErrStopped is returned by Publish after Stop. If the event was persisted before the bus
	// noticed, it stays 'pending' and the next boot's recovery delivers it.
	ErrStopped = errors.New("event bus is stopped")
)

// Handler consumes one event. A nil return acks the delivery; an error (or a panic, which is
// recovered and logged) marks the event's dispatch 'failed'. Handlers run on the subscriber's
// own goroutine under the context given to Start, and must be idempotent: boot recovery
// re-delivers events whose dispatch never finished.
type Handler func(ctx context.Context, e domain.Event) error

// Bus is the process-wide event bus. Construct one with New, register subscriptions during
// module Init, then Start it once every subscription exists.
type Bus struct {
	store   *store.Store
	logger  *slog.Logger
	bufSize int

	// bootedAt is when this process's bus came up; Start re-dispatches only pending rows older
	// than it, so this process's own in-flight publishes are never double-delivered.
	bootedAt string

	// hctx is the context handlers run under: Background until Start supplies the process one.
	// atomic.Value demands one concrete type across stores, hence the ctxBox.
	hctx atomic.Value

	// done aborts blocked enqueues when the bus stops.
	done chan struct{}
	// dispatches counts in-flight dispatch calls; Stop waits for them before closing queues.
	dispatches sync.WaitGroup
	// consumers counts subscriber goroutines; Stop waits for them to drain.
	consumers sync.WaitGroup

	mu      sync.Mutex
	subs    []*subscription
	started bool
	stopped bool
}

// Options configures New.
type Options struct {
	// Store is the open, migrated database. Required: persist-then-dispatch has nowhere to
	// persist without it.
	Store *store.Store
	// Logger receives slow-consumer, failure and recovery lines. Nil means slog.Default().
	Logger *slog.Logger
	// BufferSize is each subscriber's queue depth. Zero means DefaultBufferSize. A full queue
	// blocks the publisher (and logs); nothing is ever dropped.
	BufferSize int
}

// New builds a bus. Nothing is dispatched until events are published; call Start after every
// subscription is registered so boot recovery reaches all of them.
func New(opts Options) *Bus {
	logger := opts.Logger
	if logger == nil {
		logger = slog.Default()
	}
	size := opts.BufferSize
	if size <= 0 {
		size = DefaultBufferSize
	}
	b := &Bus{
		store:    opts.Store,
		logger:   logger,
		bufSize:  size,
		bootedAt: domain.Now(),
		done:     make(chan struct{}),
	}
	b.hctx.Store(ctxBox{context.Background()})
	return b
}

// Topic is the routing string subscriptions match against: "kind.activity_type", e.g.
// "ticket.created", "pull_request.synchronize".
func Topic(e domain.Event) string {
	return e.Kind + "." + e.ActivityType
}

// SubscribeKind delivers every event of this kind, whatever its activity type. The name labels
// the subscription in logs and Stats and must be unique on the bus.
func (b *Bus) SubscribeKind(name, kind string, h Handler) error {
	if kind == "" {
		return errors.New("bus: SubscribeKind needs a kind")
	}
	return b.subscribe(name, "kind="+kind, h, func(e *domain.Event) bool {
		return e.Kind == kind
	})
}

// SubscribeTopic delivers every event whose Topic matches the glob pattern (path.Match syntax):
// "ticket.*" is every ticket event, "*.completed" every completion. A malformed pattern is
// rejected here rather than silently matching nothing.
func (b *Bus) SubscribeTopic(name, pattern string, h Handler) error {
	if _, err := path.Match(pattern, "probe"); err != nil {
		return fmt.Errorf("bus: bad topic pattern %q: %w", pattern, err)
	}
	return b.subscribe(name, "topic="+pattern, h, func(e *domain.Event) bool {
		ok, _ := path.Match(pattern, Topic(*e))
		return ok
	})
}

func (b *Bus) subscribe(name, match string, h Handler, matches func(*domain.Event) bool) error {
	if name == "" {
		return errors.New("bus: a subscription needs a name")
	}
	if h == nil {
		return fmt.Errorf("bus: subscription %q has a nil handler", name)
	}
	s := &subscription{
		name:    name,
		match:   match,
		matches: matches,
		handler: h,
		ch:      make(chan item, b.bufSize),
	}

	b.mu.Lock()
	if b.stopped {
		b.mu.Unlock()
		return fmt.Errorf("bus: cannot subscribe %q: %w", name, ErrStopped)
	}
	for _, existing := range b.subs {
		if existing.name == name {
			b.mu.Unlock()
			return fmt.Errorf("bus: subscription name %q is already taken; names must be unique", name)
		}
	}
	b.subs = append(b.subs, s)
	b.consumers.Add(1)
	b.mu.Unlock()

	go b.consume(s)
	return nil
}

// Start begins boot recovery: every event still 'pending' from a previous process is
// re-dispatched to the current subscribers (D-13). Call it once, after every module's Init has
// registered its subscriptions. ctx is also what handlers run under from here on.
func (b *Bus) Start(ctx context.Context) error {
	b.mu.Lock()
	if b.started {
		b.mu.Unlock()
		return errors.New("bus: Start called twice")
	}
	b.started = true
	b.mu.Unlock()
	b.hctx.Store(ctxBox{ctx})

	pending, err := b.store.Events().ListPending(ctx)
	if err != nil {
		return fmt.Errorf("bus: list pending events for boot recovery: %w", err)
	}
	redispatched := 0
	for _, e := range pending {
		// Timestamps are fixed-width RFC3339 UTC, so string order is time order. Rows at or
		// after bootedAt are this process's own publishes, still on their way to done.
		if e.CreatedAt >= b.bootedAt {
			continue
		}
		if err := b.dispatch(ctx, e); err != nil {
			return fmt.Errorf("bus: re-dispatch event %s: %w", e.ID, err)
		}
		redispatched++
	}
	if redispatched > 0 {
		b.logger.Info("bus: re-dispatched events a previous process left pending",
			slog.Int("events", redispatched))
	}
	return nil
}

// Publish persists the event and fans it out (persist then dispatch, D-13). A dedupe_key already
// in the table means this occurrence was published before: nothing is inserted, nothing is
// delivered, and the caller gets ErrDuplicate to tell apart from a real failure.
//
// The event must carry a DedupeKey. ID, timestamps and ActorKind are defaulted when empty; use
// Emit for internal events, which also derives the key and the causality edge.
//
// Publish returns once the event is queued to every matching subscriber — normally immediately,
// but a subscriber whose buffer is full blocks it (never drops; the wait is logged). Handlers
// run asynchronously; their collective outcome lands in events.dispatch_state.
func (b *Bus) Publish(ctx context.Context, e domain.Event) error {
	if e.DedupeKey == "" {
		return fmt.Errorf("bus: event kind %q has no dedupe key; every event needs one (D-3)", e.Kind)
	}
	if e.ID == "" {
		e.ID = domain.NewID()
	}
	if e.OccurredAt == "" {
		e.OccurredAt = domain.Now()
	}
	if e.CreatedAt == "" {
		e.CreatedAt = domain.Now()
	}
	if e.ActorKind == "" {
		e.ActorKind = domain.ActorSystem
	}
	e.DispatchState = domain.DispatchPending

	if err := b.store.Events().Insert(ctx, &e); err != nil {
		// Fresh ULID ids do not collide, so a unique violation here is the dedupe_key.
		if errors.Is(err, store.ErrUnique) {
			return fmt.Errorf("%w: dedupe_key %q was already published", ErrDuplicate, e.DedupeKey)
		}
		return fmt.Errorf("bus: persist event: %w", err)
	}
	return b.dispatch(ctx, e)
}

// Emit publishes an internal event on behalf of a service ("ticket.created", "run.completed",
// ...). It fills what internal events share: Source "internal", a fresh ULID, the causality
// edge from the context (WithCauseRun, architecture §6.3), and the dedupe key.
//
// Dedupe keys are deterministic per occurrence. An external source derives its key from the
// upstream event's identity; an internal one-shot event has no upstream — its occurrence is
// created exactly once, right here — so its identity is its own fresh ULID and the key is
// "internal:<id>". That satisfies the schema's uniqueness and keeps boot recovery re-delivering
// the stored row rather than re-inserting it, which is all the key must protect for a one-shot.
// A caller that can retry emission (an at-least-once job, say) must set DedupeKey itself from
// its natural key so the retry collapses onto the first attempt.
func (b *Bus) Emit(ctx context.Context, e domain.Event) error {
	if e.ID == "" {
		e.ID = domain.NewID()
	}
	if e.Source == "" {
		e.Source = SourceInternal
	}
	if e.DedupeKey == "" {
		e.DedupeKey = SourceInternal + ":" + e.ID
	}
	if e.CauseRunID == nil {
		if runID, ok := CauseRun(ctx); ok {
			e.CauseRunID = &runID
		}
	}
	return b.Publish(ctx, e)
}

// dispatch fans one persisted event out to every matching subscriber and arranges for the
// dispatch_state to be recorded once they have all reported.
func (b *Bus) dispatch(ctx context.Context, e domain.Event) error {
	b.mu.Lock()
	if b.stopped {
		b.mu.Unlock()
		// Persisted but not delivered: the row stays pending and the next boot recovers it.
		return fmt.Errorf("bus: event %s stays pending: %w", e.ID, ErrStopped)
	}
	var matched []*subscription
	for _, s := range b.subs {
		if s.matches(&e) {
			matched = append(matched, s)
		}
	}
	b.dispatches.Add(1)
	b.mu.Unlock()
	defer b.dispatches.Done()

	if len(matched) == 0 {
		// Vacuously acked. Anything else would re-dispatch the event on every boot forever,
		// waiting for a subscriber that does not exist.
		b.finalize(e.ID, domain.DispatchDone)
		return nil
	}

	tr := &tracker{bus: b, eventID: e.ID}
	tr.remaining.Store(int64(len(matched)))
	it := item{event: e, tracker: tr}

	// First pass without blocking, so that one slow consumer does not delay delivery to the
	// others; only then wait on the stragglers.
	var full []*subscription
	for _, s := range matched {
		select {
		case s.ch <- it:
			s.enqueued.Add(1)
		default:
			full = append(full, s)
		}
	}
	for _, s := range full {
		b.logger.Warn("bus: slow consumer, publish is blocking (never dropping)",
			slog.String("subscriber", s.name),
			slog.String("event", e.ID),
			slog.String("topic", Topic(e)),
			slog.Int64("lag", s.lag()))
		select {
		case s.ch <- it:
			s.enqueued.Add(1)
		case <-ctx.Done():
			// The row keeps whatever state the already-queued deliveries reach; with this
			// subscriber undelivered that is 'pending' or 'failed', never a false 'done' —
			// the tracker's count includes the deliveries that will now never happen.
			b.logger.Warn("bus: publish abandoned by its context; event will be recovered on next boot",
				slog.String("subscriber", s.name), slog.String("event", e.ID))
			return fmt.Errorf("bus: enqueue event %s to %q: %w", e.ID, s.name, ctx.Err())
		case <-b.done:
			b.logger.Warn("bus: stopped while enqueueing; event will be recovered on next boot",
				slog.String("subscriber", s.name), slog.String("event", e.ID))
			return fmt.Errorf("bus: enqueue event %s to %q: %w", e.ID, s.name, ErrStopped)
		}
	}
	return nil
}

// Stop stops the bus: publishers get ErrStopped, queued deliveries drain, and Stop returns when
// every subscriber goroutine has exited or ctx expires. Safe to call twice.
func (b *Bus) Stop(ctx context.Context) error {
	b.mu.Lock()
	if b.stopped {
		b.mu.Unlock()
		return nil
	}
	b.stopped = true
	subs := append([]*subscription(nil), b.subs...)
	b.mu.Unlock()

	close(b.done)       // aborts publishers blocked on a full queue
	b.dispatches.Wait() // no dispatch is mid-enqueue past this point
	for _, s := range subs {
		close(s.ch) // consumers exit after draining what is queued
	}

	drained := make(chan struct{})
	go func() {
		b.consumers.Wait()
		close(drained)
	}()
	select {
	case <-drained:
		return nil
	case <-ctx.Done():
		return fmt.Errorf("bus: subscribers did not drain before the deadline: %w", ctx.Err())
	}
}

// consume is one subscriber's goroutine: deliver, count, report to the event's tracker.
func (b *Bus) consume(s *subscription) {
	defer b.consumers.Done()
	for it := range s.ch {
		err := b.deliver(s, it.event)
		if err != nil {
			s.failed.Add(1)
			b.logger.Error("bus: subscriber failed; dispatch will be marked failed",
				slog.String("subscriber", s.name),
				slog.String("event", it.event.ID),
				slog.String("topic", Topic(it.event)),
				slog.String("error", err.Error()))
		}
		s.processed.Add(1)
		it.tracker.report(err)
	}
}

// deliver runs the handler with panic containment: a panicking subscriber fails this dispatch
// and is logged with its stack, and the bus (and the subscriber's own goroutine) live on.
func (b *Bus) deliver(s *subscription, e domain.Event) (err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("subscriber %q panicked: %v\n%s", s.name, r, debug.Stack())
		}
	}()
	box, _ := b.hctx.Load().(ctxBox)
	return s.handler(box.ctx, e)
}

// finalize records a dispatch outcome. It runs detached from any request context: the outcome
// of work already done should be recorded even mid-shutdown.
func (b *Bus) finalize(eventID string, state domain.DispatchState) {
	ctx, cancel := context.WithTimeout(context.Background(), finalizeTimeout)
	defer cancel()
	if err := b.store.Events().SetDispatchState(ctx, eventID, state); err != nil {
		b.logger.Error("bus: could not record dispatch state",
			slog.String("event", eventID),
			slog.String("state", string(state)),
			slog.String("error", err.Error()))
	}
}

// ctxBox gives atomic.Value the single concrete type it requires, whatever the context inside.
type ctxBox struct{ ctx context.Context }

// tracker is one event's outstanding-ack counter. The subscriber that reports last flips the
// row: 'done' if everyone returned nil, 'failed' otherwise. If an enqueue was abandoned the
// count never reaches zero and the row stays 'pending' for the next boot — never a false done.
type tracker struct {
	bus       *Bus
	eventID   string
	remaining atomic.Int64
	anyFailed atomic.Bool
}

func (t *tracker) report(err error) {
	if err != nil {
		t.anyFailed.Store(true)
	}
	if t.remaining.Add(-1) != 0 {
		return
	}
	state := domain.DispatchDone
	if t.anyFailed.Load() {
		state = domain.DispatchFailed
	}
	t.bus.finalize(t.eventID, state)
}

// subscription is one registered consumer: its matcher, its queue, and its counters.
type subscription struct {
	name    string
	match   string // human-readable matcher, for Stats
	matches func(*domain.Event) bool
	handler Handler
	ch      chan item

	enqueued  atomic.Int64 // accepted into the queue
	processed atomic.Int64 // handler returned (nil or not)
	failed    atomic.Int64 // handler returned an error or panicked
}

// lag is how many accepted deliveries the handler has not finished yet.
func (s *subscription) lag() int64 { return s.enqueued.Load() - s.processed.Load() }

// item is one queued delivery.
type item struct {
	event   domain.Event
	tracker *tracker
}

// SubscriberStats is one subscription's health, as the module status surface reports it.
type SubscriberStats struct {
	// Name is the subscription's unique name.
	Name string `json:"name"`
	// Match is the human-readable matcher: "kind=ticket" or "topic=ticket.*".
	Match string `json:"match"`
	// Lag is how many accepted deliveries the handler has not finished yet. A persistently
	// non-zero lag is a slow consumer holding publishers up.
	Lag int64 `json:"lag"`
	// Processed counts deliveries the handler has finished, failures included.
	Processed int64 `json:"processed"`
	// Failed counts deliveries that returned an error or panicked.
	Failed int64 `json:"failed"`
}

// Stats reports every subscription's counters, in subscription order.
func (b *Bus) Stats() []SubscriberStats {
	b.mu.Lock()
	subs := append([]*subscription(nil), b.subs...)
	b.mu.Unlock()

	out := make([]SubscriberStats, 0, len(subs))
	for _, s := range subs {
		out = append(out, SubscriberStats{
			Name:      s.name,
			Match:     s.match,
			Lag:       s.lag(),
			Processed: s.processed.Load(),
			Failed:    s.failed.Load(),
		})
	}
	return out
}

// Lag is the total backlog across every subscription — the single number a status endpoint
// shows before drilling into Stats.
func (b *Bus) Lag() int64 {
	var total int64
	for _, s := range b.Stats() {
		total += s.Lag
	}
	return total
}
