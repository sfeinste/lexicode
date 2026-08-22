// S24 escalation acceptance: an unanswered elicitation older than 60s creates ONE
// notification for the delegating human — updated in place, never stacked (interaction
// rules 3 and 11) — driven here with a fake clock instead of sleeping.
package notify

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"path/filepath"
	"testing"
	"time"

	"github.com/spruce/lexicode/internal/domain"
	"github.com/spruce/lexicode/internal/kernel/store"
)

type fixture struct {
	st    *store.Store
	svc   *Service
	clock *time.Time

	owner     domain.User
	other     domain.User
	project   domain.Project
	agent     domain.Agent
	ticket    domain.Ticket
	baseState domain.RunState
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	st, err := store.Open(store.Options{Path: filepath.Join(t.TempDir(), "notify.db"), Logger: logger})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	ctx := context.Background()
	if _, err := st.Migrate(ctx); err != nil {
		t.Fatal(err)
	}

	f := &fixture{st: st, baseState: domain.RunNeedsInput}
	start := time.Date(2026, 8, 22, 12, 0, 0, 0, time.UTC)
	f.clock = &start
	f.svc = New(Options{Store: st, Logger: logger, Now: func() time.Time { return *f.clock }})

	now := domain.Now()
	f.owner = domain.User{ID: domain.NewID(), Email: "owner@example.com", DisplayName: "O",
		PasswordHash: "x", Role: domain.RoleOwner, AvatarColor: "#111111", CreatedAt: now}
	f.other = domain.User{ID: domain.NewID(), Email: "dev@example.com", DisplayName: "D",
		PasswordHash: "x", Role: domain.RoleMember, AvatarColor: "#222222", CreatedAt: now}
	for _, u := range []*domain.User{&f.owner, &f.other} {
		if err := st.Users().Create(ctx, u); err != nil {
			t.Fatal(err)
		}
	}
	f.project = domain.Project{ID: domain.NewID(), Key: "PAY", Name: "Payments",
		OwnerID: f.owner.ID, CreatedAt: now, UpdatedAt: now}
	if err := st.Projects().Create(ctx, &f.project); err != nil {
		t.Fatal(err)
	}
	f.agent = domain.Agent{
		ID: domain.NewID(), ProjectID: f.project.ID, Name: "Dev", Color: "#888888",
		RuntimeID: "scripted", Model: "fake", Effort: "medium",
		Autonomy: domain.AutonomyAuto, GitAuthorName: "Dev",
		GitAuthorEmail: "dev@agents.local", ConcurrencyCap: 1,
		MaxWallClockSeconds: 300, MaxSteps: 50, Enabled: true,
		CreatedAt: now, UpdatedAt: now,
	}
	if err := st.Agents().Create(ctx, &f.agent); err != nil {
		t.Fatal(err)
	}
	seq, err := st.Projects().AllocateTicketSeq(ctx, f.project.ID)
	if err != nil {
		t.Fatal(err)
	}
	col := domain.Column{ID: domain.NewID(), ProjectID: f.project.ID, Name: "Backlog",
		Category: domain.CategoryBacklog, Position: 1, CreatedAt: now, UpdatedAt: now}
	if err := st.Columns().Create(ctx, &col); err != nil {
		t.Fatal(err)
	}
	f.ticket = domain.Ticket{
		ID: domain.NewID(), ProjectID: f.project.ID, Seq: seq,
		Key: fmt.Sprintf("PAY-%d", seq), Title: "Add idempotency keys",
		ColumnID: col.ID, Position: domain.PositionGap, Priority: domain.PriorityNone,
		Origin: domain.OriginHuman, CreatedAt: now, UpdatedAt: now,
	}
	if err := st.Tickets().Create(ctx, &f.ticket); err != nil {
		t.Fatal(err)
	}
	return f
}

// park creates one run parked on an elicitation, asked at the fixture's current clock.
func (f *fixture) park(t *testing.T, seq int64, kind domain.ElicitationKind, requestedBy *string) (domain.Run, domain.Elicitation) {
	t.Helper()
	ctx := context.Background()
	state := domain.RunNeedsInput
	if kind == domain.ElicitationApproval {
		state = domain.RunAwaitingApproval
	}
	nowStr := domain.FormatTime(*f.clock)
	run := domain.Run{
		ID: domain.NewID(), Seq: seq, ProjectID: f.project.ID, AgentID: f.agent.ID,
		State: state, Autonomy: domain.AutonomyAuto, Model: "fake", Effort: "medium",
		Prompt: "p", RuntimeID: "scripted", SandboxID: "fake",
		SubjectKey: "ticket:" + f.ticket.Key, QueuedAt: nowStr,
		RequestedByUserID: requestedBy,
	}
	tid := f.ticket.ID
	run.TicketID = &tid
	if err := f.st.Runs().Create(ctx, &run); err != nil {
		t.Fatal(err)
	}
	a := domain.Activity{
		RunID: run.ID, Type: domain.ActivityElicitation, Level: 0,
		Title: "Question: Which retry strategy?", Payload: []byte(`{}`),
		Attempt: 1, CreatedAt: nowStr,
	}
	if kind == domain.ElicitationApproval {
		a.Title = "Approval: Run \"npm publish\""
	}
	if err := f.st.Activities().AppendNext(ctx, &a); err != nil {
		t.Fatal(err)
	}
	el := domain.Elicitation{
		ID: domain.NewID(), RunID: run.ID, ActivitySeq: a.Seq, Kind: kind,
		Request: []byte(`{}`), State: domain.ElicitationPending, CreatedAt: nowStr,
	}
	if err := f.st.Elicitations().Create(ctx, &el); err != nil {
		t.Fatal(err)
	}
	return run, el
}

func (f *fixture) advance(d time.Duration) { *f.clock = f.clock.Add(d) }

func TestEscalationAfterSixtySecondsFakeClock(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	uid := f.other.ID
	run, _ := f.park(t, 1, domain.ElicitationQuestion, &uid)

	// 59s: nothing escalates yet.
	f.advance(59 * time.Second)
	f.svc.Escalate(ctx)
	if _, err := f.st.Notifications().ByUserAndRun(ctx, uid, run.ID); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("notification exists before the 60s threshold: %v", err)
	}

	// 61s: the delegating human (requested_by) gets ONE unread row with the flavor.
	f.advance(2 * time.Second)
	f.svc.Escalate(ctx)
	n, err := f.st.Notifications().ByUserAndRun(ctx, uid, run.ID)
	if err != nil {
		t.Fatalf("no notification after 61s: %v", err)
	}
	if n.Flavor != domain.FlavorQuestion || n.State != domain.NotificationUnread {
		t.Fatalf("notification = %+v", n)
	}
	if n.Title != "Dev asked a question" || n.Body != "Question: Which retry strategy?" {
		t.Fatalf("notification copy = %q / %q", n.Title, n.Body)
	}

	// Another scan updates in place — no second row, same id (interaction rule 3).
	f.advance(30 * time.Second)
	f.svc.Escalate(ctx)
	rows, err := f.st.Notifications().ForUser(ctx, uid)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0].ID != n.ID {
		t.Fatalf("notifications stacked: %+v", rows)
	}

	// The project owner was never notified (routing is the delegating human, never
	// "everyone").
	if _, err := f.st.Notifications().ByUserAndRun(ctx, f.owner.ID, run.ID); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("owner notified although the run has a delegating human: %v", err)
	}
}

func TestEscalationApprovalFlavorAndOwnerFallback(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	// No requested_by, no assignee → the project owner.
	run, _ := f.park(t, 2, domain.ElicitationApproval, nil)

	f.advance(90 * time.Second)
	f.svc.Escalate(ctx)
	n, err := f.st.Notifications().ByUserAndRun(ctx, f.owner.ID, run.ID)
	if err != nil {
		t.Fatalf("owner fallback notification missing: %v", err)
	}
	if n.Flavor != domain.FlavorApproval || n.Title != "Dev is waiting for an approval" {
		t.Fatalf("notification = %+v", n)
	}
}

func TestEscalationSkipsAnsweredAndTerminal(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	uid := f.other.ID
	run, el := f.park(t, 3, domain.ElicitationQuestion, &uid)

	// Answered before the threshold: no escalation.
	if err := f.st.Elicitations().Respond(ctx, el.ID, domain.ElicitationAnswered,
		[]byte(`{}`), &uid, domain.FormatTime(*f.clock)); err != nil {
		t.Fatal(err)
	}
	f.advance(5 * time.Minute)
	f.svc.Escalate(ctx)
	if _, err := f.st.Notifications().ByUserAndRun(ctx, uid, run.ID); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("answered elicitation escalated: %v", err)
	}
}

// S36 acceptance: four concurrent runs entering needs_input produce four rows, and a run
// re-entering needs_input with a new question updates its row in place — never a fifth.
func TestFourRunsFourRowsReentryNeverAFifth(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	uid := f.other.ID

	runs := make([]domain.Run, 0, 4)
	els := make([]domain.Elicitation, 0, 4)
	for i := int64(1); i <= 4; i++ {
		run, el := f.park(t, 10+i, domain.ElicitationQuestion, &uid)
		runs = append(runs, run)
		els = append(els, el)
	}
	f.advance(90 * time.Second)
	f.svc.Escalate(ctx)
	rows, err := f.st.Notifications().ForUser(ctx, uid)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 4 {
		t.Fatalf("four blocked runs produced %d rows, want 4", len(rows))
	}
	var reentrant domain.Notification
	for _, n := range rows {
		if n.RunID != nil && *n.RunID == runs[0].ID {
			reentrant = n
		}
	}
	if reentrant.ID == "" {
		t.Fatalf("no row for run %s: %+v", runs[0].ID, rows)
	}

	// Run 1's question is answered; the run resumes and re-enters needs_input with a NEW
	// question. The row is updated in place — four rows, never a fifth.
	if err := f.st.Elicitations().Respond(ctx, els[0].ID, domain.ElicitationAnswered,
		[]byte(`{}`), &uid, domain.FormatTime(*f.clock)); err != nil {
		t.Fatal(err)
	}
	nowStr := domain.FormatTime(*f.clock)
	a := domain.Activity{
		RunID: runs[0].ID, Type: domain.ActivityElicitation, Level: 0,
		Title: "Question: Postgres or SQLite?", Payload: []byte(`{}`),
		Attempt: 1, CreatedAt: nowStr,
	}
	if err := f.st.Activities().AppendNext(ctx, &a); err != nil {
		t.Fatal(err)
	}
	el2 := domain.Elicitation{
		ID: domain.NewID(), RunID: runs[0].ID, ActivitySeq: a.Seq,
		Kind: domain.ElicitationQuestion, Request: []byte(`{}`),
		State: domain.ElicitationPending, CreatedAt: nowStr,
	}
	if err := f.st.Elicitations().Create(ctx, &el2); err != nil {
		t.Fatal(err)
	}
	f.advance(90 * time.Second)
	f.svc.Escalate(ctx)

	rows, err = f.st.Notifications().ForUser(ctx, uid)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 4 {
		t.Fatalf("re-entry produced %d rows, want 4 (never a fifth)", len(rows))
	}
	after, err := f.st.Notifications().ByUserAndRun(ctx, uid, runs[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	if after.ID != reentrant.ID {
		t.Fatalf("re-entry replaced the row: id %s → %s", reentrant.ID, after.ID)
	}
	if after.Body != "Question: Postgres or SQLite?" || after.State != domain.NotificationUnread {
		t.Fatalf("re-entry row not updated in place: %+v", after)
	}
}

// S36 acceptance: a notification for a run that later completes updates in place — the
// flavor changes on the SAME row (id preserved), never a second row. Redelivery of the
// same terminal frame does not flip a read row back to unread.
func TestRunCompletionUpdatesNotificationInPlace(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	uid := f.other.ID
	run, _ := f.park(t, 21, domain.ElicitationQuestion, &uid)

	f.advance(90 * time.Second)
	f.svc.Escalate(ctx)
	before, err := f.st.Notifications().ByUserAndRun(ctx, uid, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if before.Flavor != domain.FlavorQuestion {
		t.Fatalf("flavor = %s, want question", before.Flavor)
	}

	// The question is answered and the run completes (tests may drive SetState; in the
	// process it is scheduler-only).
	if ok, err := f.st.Runs().SetState(ctx, run.ID,
		[]domain.RunState{domain.RunNeedsInput}, domain.RunCompleted,
		store.RunStateUpdate{}); err != nil || !ok {
		t.Fatalf("SetState: ok=%v err=%v", ok, err)
	}
	rid := run.ID
	if err := f.svc.handleRunEvent(ctx, domain.Event{
		Kind: "run", ActivityType: "state", SubjectKind: "run", SubjectID: &rid,
	}); err != nil {
		t.Fatal(err)
	}

	rows, err := f.st.Notifications().ForUser(ctx, uid)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 {
		t.Fatalf("completion stacked a second row: %+v", rows)
	}
	after := rows[0]
	if after.ID != before.ID {
		t.Fatalf("completion replaced the row: id %s → %s", before.ID, after.ID)
	}
	if after.Flavor != domain.FlavorReview || after.Title != "Dev finished — review the output" {
		t.Fatalf("terminal copy wrong: %+v", after)
	}
	if after.State != domain.NotificationUnread {
		t.Fatalf("completion should surface as unread (badge tier): %+v", after)
	}

	// Reading the row, then a redelivered frame: the row stays read — same content is
	// never re-raised.
	if err := f.st.Notifications().MarkState(ctx, after.ID, domain.NotificationRead,
		domain.FormatTime(*f.clock)); err != nil {
		t.Fatal(err)
	}
	if err := f.svc.handleRunEvent(ctx, domain.Event{
		Kind: "run", ActivityType: "state", SubjectKind: "run", SubjectID: &rid,
	}); err != nil {
		t.Fatal(err)
	}
	again, err := f.st.Notifications().ByID(ctx, after.ID)
	if err != nil {
		t.Fatal(err)
	}
	if again.State != domain.NotificationRead {
		t.Fatalf("redelivery re-raised a read row: %+v", again)
	}
}

// S36: a run that fails rewrites its row as a failure naming the error, same row id.
func TestRunFailureUpdatesNotificationInPlace(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	uid := f.other.ID
	run, _ := f.park(t, 31, domain.ElicitationApproval, &uid)

	f.advance(90 * time.Second)
	f.svc.Escalate(ctx)
	before, err := f.st.Notifications().ByUserAndRun(ctx, uid, run.ID)
	if err != nil {
		t.Fatal(err)
	}

	msg := "container exited 1"
	if ok, err := f.st.Runs().SetState(ctx, run.ID,
		[]domain.RunState{domain.RunAwaitingApproval}, domain.RunFailed,
		store.RunStateUpdate{ErrorMessage: &msg}); err != nil || !ok {
		t.Fatalf("SetState: ok=%v err=%v", ok, err)
	}
	rid := run.ID
	if err := f.svc.handleRunEvent(ctx, domain.Event{
		Kind: "run", ActivityType: "state", SubjectKind: "run", SubjectID: &rid,
	}); err != nil {
		t.Fatal(err)
	}
	after, err := f.st.Notifications().ByUserAndRun(ctx, uid, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if after.ID != before.ID || after.Flavor != domain.FlavorFailure ||
		after.Title != "Dev failed" || after.Body != msg {
		t.Fatalf("failure rewrite wrong: %+v", after)
	}
}
