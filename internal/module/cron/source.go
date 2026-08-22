package cron

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/spruce/lexicode/internal/domain"
	"github.com/spruce/lexicode/internal/kernel/ports"
	"github.com/spruce/lexicode/internal/kernel/store"
)

// defaultLookback bounds the backward search for the most recent scheduled minute. An
// expression that has not matched for over a year (a stale cursor plus a Feb-30 kind of
// expression) simply finds nothing — no firing beats an unbounded scan.
const defaultLookback = 366 * 24 * time.Hour

// Source is the schedule.cron event source: the ports.EventSource this module registers,
// plus the ports.TriggerVetter the trigger CRUD validates cron expressions through.
type Source struct {
	store    *store.Store
	logger   *slog.Logger
	now      func() time.Time
	lookback time.Duration

	mu     sync.Mutex
	cancel context.CancelFunc
	wg     sync.WaitGroup
}

// newSource builds the source; the module wires store and logger in Init.
func newSource() *Source {
	return &Source{
		logger:   slog.Default(),
		now:      time.Now,
		lookback: defaultLookback,
	}
}

// ID implements ports.EventSource.
func (s *Source) ID() string { return sourceID }

// Catalog implements ports.EventSource.
func (s *Source) Catalog() ports.EventCatalog { return catalog() }

// VetTrigger implements ports.TriggerVetter: a schedule trigger needs a parseable cron
// expression, refused at save time with the bad segment named. Non-schedule triggers are
// not this source's business (the CRUD's own check refuses a cron on them).
func (s *Source) VetTrigger(tr domain.Trigger) []ports.TriggerProblem {
	if tr.Event != eventKind {
		return nil
	}
	if tr.Cron == nil || strings.TrimSpace(*tr.Cron) == "" {
		return []ports.TriggerProblem{{Field: "cron",
			Message: "A cron expression is required for schedule triggers " +
				"(5 fields: minute hour day-of-month month day-of-week, evaluated in UTC)."}}
	}
	if _, err := Parse(*tr.Cron); err != nil {
		return []ports.TriggerProblem{{Field: "cron",
			Message: "Invalid cron expression — " + err.Error() + "."}}
	}
	return nil
}

// Start implements ports.EventSource: one goroutine that scans immediately (the restart
// catch-up), then on every UTC minute boundary. It returns promptly, per the port contract.
func (s *Source) Start(ctx context.Context, emit ports.Emit) error {
	if s.store == nil {
		return fmt.Errorf("cron: no store wired")
	}
	if emit == nil {
		return fmt.Errorf("cron: no emit wired")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.cancel != nil {
		return fmt.Errorf("cron: Start called twice")
	}
	// Values (not cancellation) of the boot context carry over, like the poller's Start.
	runCtx, cancel := context.WithCancel(context.WithoutCancel(ctx))
	s.cancel = cancel
	s.wg.Add(1)
	go s.run(runCtx, emit)
	return nil
}

// Stop implements ports.EventSource: cancels the scan loop and waits for it.
func (s *Source) Stop(ctx context.Context) error {
	s.mu.Lock()
	cancel := s.cancel
	s.cancel = nil
	s.mu.Unlock()
	if cancel == nil {
		return nil
	}
	cancel()
	done := make(chan struct{})
	go func() {
		s.wg.Wait()
		close(done)
	}()
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return fmt.Errorf("cron: scan loop did not stop before the deadline: %w", ctx.Err())
	}
}

// run is the scan loop: an immediate scan for catch-up, then one per minute, aligned to the
// boundary so a firing lands in its own minute rather than up to 59s late.
func (s *Source) run(ctx context.Context, emit ports.Emit) {
	defer s.wg.Done()
	s.scan(ctx, emit)
	for {
		now := s.now().UTC()
		next := now.Truncate(time.Minute).Add(time.Minute)
		timer := time.NewTimer(next.Sub(now))
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
			s.scan(ctx, emit)
		}
	}
}

// scan evaluates every enabled schedule trigger once against the current minute. It is the
// whole firing algorithm; run only decides when to call it.
func (s *Source) scan(ctx context.Context, emit ports.Emit) {
	t := s.now().UTC().Truncate(time.Minute)
	trs, err := s.store.Triggers().EnabledBySource(ctx, sourceID)
	if err != nil {
		s.logger.Error("cron: could not load schedule triggers; scan skipped",
			slog.String("error", err.Error()))
		return
	}
	for _, tr := range trs {
		if ctx.Err() != nil {
			return
		}
		s.scanTrigger(ctx, emit, tr, t)
	}
}

// scanTrigger fires one trigger for the most recent scheduled minute since its cursor —
// which is the current minute when the schedule is on time, one missed minute after a
// restart, and nothing otherwise.
func (s *Source) scanTrigger(ctx context.Context, emit ports.Emit, tr domain.Trigger, t time.Time) {
	if tr.Event != eventKind || tr.Cron == nil {
		return // not this source's shape; save-time validation refuses these
	}
	expr, err := Parse(*tr.Cron)
	if err != nil {
		// Save-time validation refuses invalid expressions; a row that predates it (or was
		// edited by hand) is logged and skipped, never fired.
		s.logger.Warn("cron: stored expression does not parse; trigger skipped",
			slog.String("trigger", tr.ID), slog.String("error", err.Error()))
		return
	}

	cursors := s.store.PollCursors()
	resource := cursorResource(tr.ID)
	cur, err := cursors.Get(ctx, tr.ProjectID, resource)
	if err != nil && !isNotFound(err) {
		s.logger.Error("cron: cursor read failed; trigger skipped this minute",
			slog.String("trigger", tr.ID), slog.String("error", err.Error()))
		return
	}

	// First sighting (or an unreadable cursor): baseline at the current minute and emit
	// nothing — a new schedule trigger never fires for the past (the poller's cold-start
	// rule, architecture §7).
	since := time.Time{}
	if err == nil {
		since, err = domain.ParseTime(cur.Cursor)
		if err != nil {
			s.logger.Warn("cron: cursor is unreadable; re-baselining",
				slog.String("trigger", tr.ID), slog.String("cursor", cur.Cursor))
			since = time.Time{}
		}
	}
	if since.IsZero() {
		s.writeCursor(ctx, tr, t)
		return
	}

	fireAt, ok := latestMatch(expr, since, t, s.lookback)
	if !ok {
		return
	}
	if err := emit(ctx, s.event(tr, fireAt)); err != nil {
		// Cursor untouched: the next minute's scan retries this firing, and the dedupe key
		// makes the retry collapse if the event was in fact persisted.
		s.logger.Error("cron: emit failed; will retry next minute",
			slog.String("trigger", tr.ID), slog.String("error", err.Error()))
		return
	}
	s.writeCursor(ctx, tr, fireAt)
}

// latestMatch is the most recent minute in (since, until] the expression matches, searching
// backward from until and giving up beyond the lookback bound.
func latestMatch(expr Expr, since, until time.Time, lookback time.Duration) (time.Time, bool) {
	floor := until.Add(-lookback)
	if since.After(floor) {
		floor = since
	}
	for m := until; m.After(floor); m = m.Add(-time.Minute) {
		if expr.Matches(m) {
			return m, true
		}
	}
	return time.Time{}, false
}

// event is one schedule · cron occurrence, addressed to its trigger (see doc.go).
func (s *Source) event(tr domain.Trigger, fireAt time.Time) domain.Event {
	pid, tid := tr.ProjectID, tr.ID
	firedAt := domain.FormatTime(fireAt)
	payload, err := json.Marshal(map[string]any{
		"schedule": map[string]any{
			"cron":       *tr.Cron,
			"fired_at":   firedAt,
			"trigger_id": tr.ID,
		},
	})
	if err != nil {
		payload = json.RawMessage(`{}`)
	}
	return domain.Event{
		ProjectID:    &pid,
		Source:       sourceID,
		Kind:         eventKind,
		ActivityType: activityType,
		ActorKind:    domain.ActorSystem,
		SubjectKind:  "trigger",
		SubjectID:    &tid,
		Payload:      payload,
		DedupeKey:    dedupe(tr.ID, *tr.Cron, firedAt),
		OccurredAt:   firedAt,
	}
}

// writeCursor records the last handled scheduled minute for one trigger.
func (s *Source) writeCursor(ctx context.Context, tr domain.Trigger, at time.Time) {
	polled := domain.FormatTime(s.now())
	c := domain.PollCursor{
		ProjectID: tr.ProjectID, Resource: cursorResource(tr.ID),
		Cursor: domain.FormatTime(at), BaselineDone: true, LastPolledAt: &polled,
	}
	if err := s.store.PollCursors().Upsert(ctx, &c); err != nil {
		s.logger.Error("cron: cursor write failed",
			slog.String("trigger", tr.ID), slog.String("error", err.Error()))
	}
}

// cursorResource is the poll_cursors resource key for one trigger's schedule cursor.
func cursorResource(triggerID string) string { return "cron:" + triggerID }

// dedupe is the deterministic per-occurrence key: one per (trigger, expression, minute), so
// restarts and emit retries collapse onto the bus's unique index.
func dedupe(triggerID, expr, minute string) string {
	sum := sha256.Sum256([]byte(sourceID + "|" + triggerID + "|" + expr + "|" + minute))
	return hex.EncodeToString(sum[:])
}

func isNotFound(err error) bool { return errors.Is(err, store.ErrNotFound) }
