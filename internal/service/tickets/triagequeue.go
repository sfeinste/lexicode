// triagequeue.go is the S31 read-and-resolve side of triage — the keyboard-driven queue
// behind /p/:key/triage. S28 (triage.go) writes tickets INTO triage; this file is how they
// leave it, one of four ways:
//
//   - accept    — the item resolves `accepted` and the ticket becomes board-visible where it
//     was parked at creation: the project's first backlog-category column, at the position
//     allocated then. Nothing moves; the §10.7 exclusion simply stops applying.
//   - duplicate — a merge. The queue ticket is archived and the survivor receives: the
//     duplicate's labels (deduplicated), its acceptance criteria (appended after the
//     survivor's own, checked state kept), every mention that pointed at the duplicate
//     (redirected in the mentions table), and a stream note carrying the duplicate's key and
//     its full provenance line — so the survivor's history shows both origins. NOT
//     transferred: the duplicate's own stream history (it stays, readable, on the archived
//     ticket), its description, priority, assignee and delegate.
//   - decline   — the item resolves `declined` and the ticket is archived (D-15 soft
//     delete), the optional reason recorded on the item and in the ticket stream.
//   - snooze    — NOT a resolution: the item stays unresolved (board-invisible, §10.7) with
//     state `snoozed`. `until` set = time-based, woken by the ticker when it passes;
//     `until` null = until new activity, woken by the bus subscriber when an event's subject
//     matches the ticket.
//
// Wake matching (snoozed-until-activity): an event wakes a ticket's item when, within the
// same project, (a) the event's subject IS the ticket (subject_kind "ticket", subject_id =
// ticket id — comments, field changes), or (b) the event carries a subject_number equal to
// the ticket's linked PR number (forge PR/issue events: comments, reviews, checks), or (c)
// the event's subject_branch equals the ticket's branch. Events of kind `triage` are ignored
// so the queue's own emissions can never wake what they just snoozed.
//
// Every verb writes the audit log, appends to the affected tickets' streams inside the
// mutation's transaction, and emits a `triage.<verb>` bus event; verbs that change what the
// board shows additionally emit the normal `ticket.*` events.
package tickets

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/spruce/lexicode/internal/domain"
	"github.com/spruce/lexicode/internal/kernel/audit"
	"github.com/spruce/lexicode/internal/kernel/auth"
	"github.com/spruce/lexicode/internal/kernel/bus"
	"github.com/spruce/lexicode/internal/kernel/store"
)

// TriageTickInterval is how often the ticker looks for time-snoozed items past due.
const TriageTickInterval = 30 * time.Second

// TriageResolvedError is a verb applied to an already-resolved item — the queue caught up
// with another tab. The HTTP layer renders it as the 409 `triage_resolved` problem.
type TriageResolvedError struct{ State domain.TriageState }

// Error names the state the item already reached.
func (e *TriageResolvedError) Error() string {
	return fmt.Sprintf("this triage item is already resolved (%s)", e.State)
}

// TriageListItem pairs a triage item with its ticket's read model — one row of the queue.
type TriageListItem struct {
	Item   domain.TriageItem
	Ticket TicketWithMeta
}

// TriageQueue returns a project's unresolved triage items — pending first, then snoozed,
// each oldest first — with their tickets. The pending count is the tab badge (actionable
// only, UI spec §2.1: snoozed items are parked by explicit choice and do not count).
func (s *Service) TriageQueue(ctx context.Context, projectKey string) ([]TriageListItem, error) {
	p, err := s.st.Projects().ByKey(ctx, projectKey)
	if err != nil {
		return nil, err
	}
	items, err := s.st.Triage().UnresolvedForProject(ctx, p.ID)
	if err != nil {
		return nil, err
	}
	cats, err := s.categoriesByColumn(ctx, p.ID)
	if err != nil {
		return nil, err
	}
	out := make([]TriageListItem, 0, len(items))
	for _, it := range items {
		tk, err := s.st.Tickets().ByID(ctx, it.TicketID)
		if err != nil {
			return nil, err
		}
		labels, err := s.st.Labels().ForTicket(ctx, tk.ID)
		if err != nil {
			return nil, err
		}
		ids := make([]string, len(labels))
		for i, l := range labels {
			ids[i] = l.ID
		}
		counts, err := s.st.Criteria().CountsForTicket(ctx, tk.ID)
		if err != nil {
			return nil, err
		}
		out = append(out, TriageListItem{Item: it, Ticket: TicketWithMeta{
			Ticket: tk, Category: cats[tk.ColumnID], LabelIDs: ids,
			CriteriaTotal: counts.Total, CriteriaChecked: counts.Checked,
		}})
	}
	return out, nil
}

// TriagePendingCount is the tab badge: `pending` items only (never snoozed — the badge
// counts actionable work, UI spec §2.1).
func (s *Service) TriagePendingCount(ctx context.Context, projectKey string) (int64, error) {
	p, err := s.st.Projects().ByKey(ctx, projectKey)
	if err != nil {
		return 0, err
	}
	return s.st.Triage().CountPending(ctx, p.ID)
}

// TriageAccept resolves an item `accepted`. The ticket does not move: it was created into
// the project's first backlog-category column with a real position (S28), and accepting
// simply ends the §10.7 board exclusion — the next board query includes it there.
func (s *Service) TriageAccept(ctx context.Context, itemID string) (TriageListItem, error) {
	var item domain.TriageItem
	var tk domain.Ticket
	err := s.st.Tx(ctx, func(tx *store.Tx) error {
		var err error
		item, tk, err = s.unresolvedItem(ctx, tx, itemID)
		if err != nil {
			return err
		}
		s.resolveItem(ctx, &item, domain.TriageAccepted)
		if err := tx.Triage().Update(ctx, &item); err != nil {
			return err
		}
		col, err := tx.Columns().ByID(ctx, tk.ColumnID)
		if err != nil {
			return err
		}
		return s.appendStream(ctx, tx, tk.ID, domain.StreamStatusChange, map[string]any{
			"event": "triage_accepted", "column_id": col.ID, "category": string(col.Category),
		})
	})
	if err != nil {
		return TriageListItem{}, err
	}
	if err := s.audit.Write(ctx, "triage.accept",
		audit.Target{Kind: "triage_item", ID: item.ID, ProjectID: tk.ProjectID,
			Note: tk.Key}, nil, item); err != nil {
		return TriageListItem{}, err
	}
	s.emitTriage(ctx, "accepted", tk, item)
	// The ticket just became board-visible: the board caches invalidate on ticket events.
	s.emitTicket(ctx, "updated", tk, nil)
	meta, err := s.withMeta(ctx, tk)
	if err != nil {
		return TriageListItem{}, err
	}
	return TriageListItem{Item: item, Ticket: meta}, nil
}

// TriageDuplicate resolves an item `duplicate`, merging its ticket into ofTicketID (the
// survivor). See the package comment for exactly what transfers.
func (s *Service) TriageDuplicate(ctx context.Context, itemID, ofTicketID string) (TriageListItem, error) {
	var item domain.TriageItem
	var dup, survivor domain.Ticket
	err := s.st.Tx(ctx, func(tx *store.Tx) error {
		var err error
		item, dup, err = s.unresolvedItem(ctx, tx, itemID)
		if err != nil {
			return err
		}
		if ofTicketID == "" {
			return fieldErr("of_ticket_id", "Name the ticket this duplicates.")
		}
		if ofTicketID == dup.ID {
			return fieldErr("of_ticket_id", "A ticket cannot be a duplicate of itself.")
		}
		survivor, err = tx.Tickets().ByID(ctx, ofTicketID)
		if errors.Is(err, store.ErrNotFound) {
			return fieldErr("of_ticket_id", "No such ticket in this project.")
		}
		if err != nil {
			return err
		}
		if survivor.ProjectID != dup.ProjectID {
			return fieldErr("of_ticket_id", "No such ticket in this project.")
		}
		if survivor.ArchivedAt != nil {
			return &ArchivedError{TicketKey: survivor.Key}
		}
		// Merging into a ticket that is itself still in triage would hide the merged
		// history off-board; resolve the survivor first.
		if sit, err := tx.Triage().ByTicket(ctx, survivor.ID); err == nil && sit.Unresolved() {
			return fieldErr("of_ticket_id",
				fmt.Sprintf("%s is itself awaiting triage; resolve it first.", survivor.Key))
		} else if err != nil && !errors.Is(err, store.ErrNotFound) {
			return err
		}

		// Labels: attach the duplicate's to the survivor, deduplicated.
		have := map[string]bool{}
		svLabels, err := tx.Labels().ForTicket(ctx, survivor.ID)
		if err != nil {
			return err
		}
		for _, l := range svLabels {
			have[l.ID] = true
		}
		dupLabels, err := tx.Labels().ForTicket(ctx, dup.ID)
		if err != nil {
			return err
		}
		for _, l := range dupLabels {
			if !have[l.ID] {
				if err := tx.Labels().Attach(ctx, survivor.ID, l.ID); err != nil {
					return err
				}
			}
		}

		// Acceptance criteria: append the duplicate's after the survivor's own, checked
		// state and notes kept.
		svCrit, err := tx.Criteria().ForTicket(ctx, survivor.ID)
		if err != nil {
			return err
		}
		last := int64(0)
		if len(svCrit) > 0 {
			last = svCrit[len(svCrit)-1].Position
		}
		dupCrit, err := tx.Criteria().ForTicket(ctx, dup.ID)
		if err != nil {
			return err
		}
		for _, c := range dupCrit {
			last += criterionGap
			moved := c
			moved.ID = domain.NewID()
			moved.TicketID = survivor.ID
			moved.Position = last
			moved.UpdatedAt = s.now()
			if err := tx.Criteria().Create(ctx, &moved); err != nil {
				return err
			}
		}

		// Mentions: everything that pointed at the duplicate now points at the survivor.
		if _, err := tx.Mentions().RedirectTarget(ctx, "ticket", dup.ID, survivor.ID); err != nil {
			return err
		}

		// The survivor's stream records the merge WITH the duplicate's provenance line, so
		// one ticket carries both origins.
		if err := s.appendStream(ctx, tx, survivor.ID, domain.StreamFieldChange, map[string]any{
			"event":      "merged_from",
			"ticket_id":  dup.ID,
			"ticket_key": dup.Key,
			"provenance": item.Provenance,
			"note":       fmt.Sprintf("merged from %s created by %s", dup.Key, lowerFirst(item.Provenance)),
		}); err != nil {
			return err
		}

		// The duplicate is archived with the merge recorded.
		now := s.now()
		dup.ArchivedAt = &now
		dup.UpdatedAt = now
		if err := tx.Tickets().Update(ctx, &dup); err != nil {
			return err
		}
		if err := s.appendStream(ctx, tx, dup.ID, domain.StreamStatusChange, map[string]any{
			"event": "triage_duplicate", "of_ticket_id": survivor.ID, "of_ticket_key": survivor.Key,
		}); err != nil {
			return err
		}

		s.resolveItem(ctx, &item, domain.TriageDuplicate)
		of := survivor.ID
		item.DuplicateOf = &of
		return tx.Triage().Update(ctx, &item)
	})
	if err != nil {
		return TriageListItem{}, err
	}
	if err := s.audit.Write(ctx, "triage.duplicate",
		audit.Target{Kind: "triage_item", ID: item.ID, ProjectID: dup.ProjectID,
			Note: fmt.Sprintf("%s duplicates %s", dup.Key, survivor.Key)},
		nil, item); err != nil {
		return TriageListItem{}, err
	}
	s.emitTriage(ctx, "duplicate", dup, item)
	s.emitTicket(ctx, "archived", dup, nil)
	s.emitTicket(ctx, "updated", survivor, nil)
	meta, err := s.withMeta(ctx, dup)
	if err != nil {
		return TriageListItem{}, err
	}
	return TriageListItem{Item: item, Ticket: meta}, nil
}

// TriageDecline resolves an item `declined` and archives its ticket, the optional reason on
// both the item and the ticket stream.
func (s *Service) TriageDecline(ctx context.Context, itemID, reason string) (TriageListItem, error) {
	reason = strings.TrimSpace(reason)
	var item domain.TriageItem
	var tk domain.Ticket
	err := s.st.Tx(ctx, func(tx *store.Tx) error {
		var err error
		item, tk, err = s.unresolvedItem(ctx, tx, itemID)
		if err != nil {
			return err
		}
		s.resolveItem(ctx, &item, domain.TriageDeclined)
		item.Reason = reason
		if err := tx.Triage().Update(ctx, &item); err != nil {
			return err
		}
		now := s.now()
		tk.ArchivedAt = &now
		tk.UpdatedAt = now
		if err := tx.Tickets().Update(ctx, &tk); err != nil {
			return err
		}
		return s.appendStream(ctx, tx, tk.ID, domain.StreamStatusChange, map[string]any{
			"event": "triage_declined", "reason": reason,
		})
	})
	if err != nil {
		return TriageListItem{}, err
	}
	note := tk.Key
	if reason != "" {
		note += ": " + reason
	}
	if err := s.audit.Write(ctx, "triage.decline",
		audit.Target{Kind: "triage_item", ID: item.ID, ProjectID: tk.ProjectID, Note: note},
		nil, item); err != nil {
		return TriageListItem{}, err
	}
	s.emitTriage(ctx, "declined", tk, item)
	s.emitTicket(ctx, "archived", tk, nil)
	meta, err := s.withMeta(ctx, tk)
	if err != nil {
		return TriageListItem{}, err
	}
	return TriageListItem{Item: item, Ticket: meta}, nil
}

// TriageSnooze parks an item: `until` set = wake at that RFC3339 instant (the ticker);
// `until` nil = wake on the next event whose subject matches the ticket (the bus
// subscriber). Snoozing is not a resolution — the ticket stays off the board (§10.7).
func (s *Service) TriageSnooze(ctx context.Context, itemID string, until *string) (TriageListItem, error) {
	if until != nil {
		t, err := domain.ParseTime(*until)
		if err != nil {
			return TriageListItem{}, fieldErr("until", "An RFC3339 timestamp, or null for until-new-activity.")
		}
		nowT, err := domain.ParseTime(s.now())
		if err != nil {
			return TriageListItem{}, err
		}
		if !t.After(nowT) {
			return TriageListItem{}, fieldErr("until", "The wake time must be in the future.")
		}
		normalized := domain.FormatTime(t)
		until = &normalized
	}
	var item domain.TriageItem
	var tk domain.Ticket
	err := s.st.Tx(ctx, func(tx *store.Tx) error {
		var err error
		item, tk, err = s.unresolvedItem(ctx, tx, itemID)
		if err != nil {
			return err
		}
		item.State = domain.TriageSnoozed
		item.SnoozeUntil = until
		if err := tx.Triage().Update(ctx, &item); err != nil {
			return err
		}
		payload := map[string]any{"event": "triage_snoozed"}
		if until != nil {
			payload["until"] = *until
		} else {
			payload["until_activity"] = true
		}
		return s.appendStream(ctx, tx, tk.ID, domain.StreamStatusChange, payload)
	})
	if err != nil {
		return TriageListItem{}, err
	}
	note := "until new activity"
	if until != nil {
		note = "until " + *until
	}
	if err := s.audit.Write(ctx, "triage.snooze",
		audit.Target{Kind: "triage_item", ID: item.ID, ProjectID: tk.ProjectID,
			Note: tk.Key + " " + note}, nil, item); err != nil {
		return TriageListItem{}, err
	}
	s.emitTriage(ctx, "snoozed", tk, item)
	meta, err := s.withMeta(ctx, tk)
	if err != nil {
		return TriageListItem{}, err
	}
	return TriageListItem{Item: item, Ticket: meta}, nil
}

// ---------------------------------------------------------------- waking -----

// SubscribeTriageWake attaches the snoozed-until-activity waker to the bus. Wired in
// cmd/lexicode before the bus starts, like every subscriber.
func (s *Service) SubscribeTriageWake(b *bus.Bus) error {
	return b.SubscribeTopic("triage-wake", "*", s.TriageWakeOnEvent)
}

// TriageWakeOnEvent is the bus handler: one event in, every matching snoozed-until-activity
// item in its project flips back to `pending`. Matching is documented in the package
// comment; `triage`-kind events are ignored so a snooze cannot wake itself.
func (s *Service) TriageWakeOnEvent(ctx context.Context, e domain.Event) error {
	if e.Kind == "triage" || e.ProjectID == nil {
		return nil
	}
	items, err := s.st.Triage().SnoozedUntilActivity(ctx, *e.ProjectID)
	if err != nil {
		return err
	}
	for _, it := range items {
		tk, err := s.st.Tickets().ByID(ctx, it.TicketID)
		if err != nil {
			s.logger.Error("triage: wake candidate load failed",
				slog.String("item", it.ID), slog.String("error", err.Error()))
			continue
		}
		if !eventMatchesTicket(e, tk) {
			continue
		}
		if err := s.wakeItem(ctx, it, tk, "new_activity"); err != nil {
			s.logger.Error("triage: wake failed",
				slog.String("item", it.ID), slog.String("error", err.Error()))
		}
	}
	return nil
}

// eventMatchesTicket is the wake predicate: the event's subject is the ticket itself, or its
// linked PR by number, or its branch.
func eventMatchesTicket(e domain.Event, tk domain.Ticket) bool {
	if e.SubjectKind == "ticket" && e.SubjectID != nil && *e.SubjectID == tk.ID {
		return true
	}
	if e.SubjectNumber != nil && tk.PRNumber != nil && *e.SubjectNumber == *tk.PRNumber {
		return true
	}
	if e.SubjectBranch != nil && tk.Branch != nil && *e.SubjectBranch == *tk.Branch {
		return true
	}
	return false
}

// WakeDueTriage is one ticker scan: every time-snoozed item past its snooze_until flips
// back to `pending`. Exported so tests drive it with a fake clock instead of sleeping.
func (s *Service) WakeDueTriage(ctx context.Context) {
	due, err := s.st.Triage().SnoozedDue(ctx, s.now())
	if err != nil {
		s.logger.Error("triage: due scan failed", slog.String("error", err.Error()))
		return
	}
	for _, it := range due {
		tk, err := s.st.Tickets().ByID(ctx, it.TicketID)
		if err != nil {
			s.logger.Error("triage: due candidate load failed",
				slog.String("item", it.ID), slog.String("error", err.Error()))
			continue
		}
		if err := s.wakeItem(ctx, it, tk, "snooze_expired"); err != nil {
			s.logger.Error("triage: wake failed",
				slog.String("item", it.ID), slog.String("error", err.Error()))
		}
	}
}

// wakeItem flips one snoozed item back to pending, with the wake reason in the stream.
func (s *Service) wakeItem(ctx context.Context, it domain.TriageItem, tk domain.Ticket, why string) error {
	err := s.st.Tx(ctx, func(tx *store.Tx) error {
		// Re-read inside the transaction: another tab may have resolved it meanwhile.
		cur, err := tx.Triage().ByID(ctx, it.ID)
		if err != nil {
			return err
		}
		if cur.State != domain.TriageSnoozed {
			return nil
		}
		cur.State = domain.TriagePending
		cur.SnoozeUntil = nil
		it = cur
		if err := tx.Triage().Update(ctx, &cur); err != nil {
			return err
		}
		return s.appendStream(ctx, tx, tk.ID, domain.StreamStatusChange, map[string]any{
			"event": "triage_woken", "reason": why,
		})
	})
	if err != nil {
		return err
	}
	if it.State != domain.TriagePending {
		return nil // lost the race; nothing woke
	}
	if err := s.audit.Write(ctx, "triage.wake",
		audit.Target{Kind: "triage_item", ID: it.ID, ProjectID: tk.ProjectID,
			Note: tk.Key + " " + why}, nil, it); err != nil {
		return err
	}
	s.emitTriage(ctx, "woken", tk, it)
	return nil
}

// StartTriageTicker runs the time-snooze scan every TriageTickInterval until ctx ends.
func (s *Service) StartTriageTicker(ctx context.Context) {
	s.tickMu.Lock()
	if s.tickDone != nil {
		s.tickMu.Unlock()
		return
	}
	done := make(chan struct{})
	s.tickDone = done
	s.tickMu.Unlock()

	go func() {
		defer close(done)
		t := time.NewTicker(TriageTickInterval)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				s.WakeDueTriage(context.WithoutCancel(ctx))
			}
		}
	}()
}

// WaitTriageTicker blocks until the ticker goroutine has exited (shutdown hygiene).
func (s *Service) WaitTriageTicker() {
	s.tickMu.Lock()
	done := s.tickDone
	s.tickMu.Unlock()
	if done != nil {
		<-done
	}
}

// ---------------------------------------------------------------- helpers -----

// unresolvedItem loads an item and its ticket, refusing already-resolved items.
func (s *Service) unresolvedItem(ctx context.Context, tx *store.Tx, itemID string) (domain.TriageItem, domain.Ticket, error) {
	item, err := tx.Triage().ByID(ctx, itemID)
	if err != nil {
		return domain.TriageItem{}, domain.Ticket{}, err
	}
	if !item.Unresolved() {
		return domain.TriageItem{}, domain.Ticket{}, &TriageResolvedError{State: item.State}
	}
	tk, err := tx.Tickets().ByID(ctx, item.TicketID)
	if err != nil {
		return domain.TriageItem{}, domain.Ticket{}, err
	}
	if tk.ArchivedAt != nil {
		return domain.TriageItem{}, domain.Ticket{}, &ArchivedError{TicketKey: tk.Key}
	}
	return item, tk, nil
}

// resolveItem stamps a terminal state and the resolver (when the actor is a signed-in
// human — resolved_by references users).
func (s *Service) resolveItem(ctx context.Context, it *domain.TriageItem, state domain.TriageState) {
	it.State = state
	it.SnoozeUntil = nil
	now := s.now()
	it.ResolvedAt = &now
	if a, ok := auth.ActorFrom(ctx); ok && a.Kind == domain.ActorHuman && a.ID != "" {
		id := a.ID
		it.ResolvedBy = &id
	}
}

// emitTriage publishes a `triage.<activity>` bus event (SSE, contracts §5.1): the queue and
// the tab badge re-render from these. Best-effort, post-commit, like every emission here.
func (s *Service) emitTriage(ctx context.Context, activity string, tk domain.Ticket, item domain.TriageItem) {
	if s.bus == nil {
		return
	}
	payload, err := json.Marshal(map[string]any{
		"ticket": s.normalizedTicket(ctx, tk),
		"triage": map[string]any{
			"id": item.ID, "provenance": item.Provenance, "state": string(item.State),
			"duplicate_of": item.DuplicateOf, "reason": item.Reason,
			"snooze_until": item.SnoozeUntil,
		},
	})
	if err != nil {
		s.logger.Error("tickets: marshal triage payload failed", slog.String("error", err.Error()))
		return
	}
	pid, tid := tk.ProjectID, tk.ID
	e := domain.Event{
		ProjectID: &pid, Kind: "triage", ActivityType: activity,
		SubjectKind: "ticket", SubjectID: &tid,
		Payload: payload, OccurredAt: s.now(),
	}
	s.stampActor(ctx, &e)
	if err := s.bus.Emit(ctx, e); err != nil {
		s.logger.Error("tickets: emit failed",
			slog.String("kind", "triage."+activity), slog.String("error", err.Error()))
	}
}

// lowerFirst lowercases the first rune — provenance lines start "Created by …" and the merge
// note embeds them mid-sentence.
func lowerFirst(s string) string {
	if s == "" {
		return s
	}
	return strings.ToLower(s[:1]) + s[1:]
}
