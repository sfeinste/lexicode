// S28 acceptance: the five trigger actions through the real pipeline. See env_test.go for
// what is real (everything but GitHub's write API).
package actions_test

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/spruce/lexicode/internal/domain"
	"github.com/spruce/lexicode/internal/kernel/sched"
	"github.com/spruce/lexicode/internal/kernel/secrets"
	ticketsvc "github.com/spruce/lexicode/internal/service/tickets"
	triggersvc "github.com/spruce/lexicode/internal/service/triggers"
)

// noGuardNoise switches off debounce so back-to-back test events are not absorbed; every
// other layer keeps its default.
const noGuardNoise = `{"debounce_seconds":0}`

// TestCreateTicketLandsInTriageNotOnBoard is acceptance (a): the created ticket exists, its
// pending triage row carries the provenance sentence, and the board/tickets list does NOT
// include it (data model §10.7) — S31 builds the queue UI that accepts it.
func TestCreateTicketLandsInTriageNotOnBoard(t *testing.T) {
	e := newEnv(t)
	e.mkColumn("Backlog", domain.CategoryBacklog, 1)
	if _, err := e.tick.CreateLabel(e.ctx, e.proj.Key, "bug", "#ff0000"); err != nil {
		t.Fatal(err)
	}

	tr := e.mkTrigger("CI failed → file a ticket", "pull_request", `["opened"]`, noGuardNoise,
		`[{"action_id":"create_ticket","params":{"title":"CI failed on PR {{pr.number}}","description":"See {{pr.body}}","labels":["bug","nosuch"]}}]`)

	ev := e.emit("pull_request", "opened", domain.ActorHuman, nil, nil, prPayload(219))
	f := e.firing(tr.ID, ev.ID)
	if f.Outcome != domain.FiringSucceeded {
		t.Fatalf("firing outcome = %s (%s), want succeeded", f.Outcome, f.Reason)
	}
	if !strings.Contains(f.Reason, "filed PAY-1 into triage") {
		t.Fatalf("firing reason = %q, want it to name the filed ticket", f.Reason)
	}

	tk, err := e.st.Tickets().ByKey(e.ctx, "PAY-1")
	if err != nil {
		t.Fatal(err)
	}
	if tk.Origin != domain.OriginTrigger {
		t.Fatalf("ticket origin = %s, want trigger", tk.Origin)
	}
	if tk.Title != "CI failed on PR 219" {
		t.Fatalf("ticket title = %q; interpolation did not apply", tk.Title)
	}
	labels, err := e.st.Labels().ForTicket(e.ctx, tk.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(labels) != 1 || labels[0].Name != "bug" {
		t.Fatalf("labels = %+v, want exactly the existing 'bug' label attached", labels)
	}

	item, err := e.st.Triage().ByTicket(e.ctx, tk.ID)
	if err != nil {
		t.Fatal(err)
	}
	if item.State != domain.TriagePending {
		t.Fatalf("triage state = %s, want pending", item.State)
	}
	want := "Created by trigger `CI failed → file a ticket` from a pull_request event"
	if item.Provenance != want {
		t.Fatalf("provenance = %q, want %q", item.Provenance, want)
	}
	if item.SourceTriggerID == nil || *item.SourceTriggerID != tr.ID {
		t.Fatalf("triage source_trigger_id = %v, want %s", item.SourceTriggerID, tr.ID)
	}

	// The invariant: invisible on the board list until accepted. Both the raw board-order
	// query and the tickets service list must exclude it.
	board, err := e.st.Tickets().ForProject(e.ctx, e.proj.ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, b := range board {
		if b.ID == tk.ID {
			t.Fatal("a pending-triage ticket is visible in the board query")
		}
	}
	list, err := e.tick.List(e.ctx, e.proj.Key, false)
	if err != nil {
		t.Fatal(err)
	}
	for _, it := range list {
		if it.Ticket.ID == tk.ID {
			t.Fatal("a pending-triage ticket is visible in the tickets list")
		}
	}
	// ...but it is not lost: direct reads still see it.
	if _, err := e.st.Tickets().ByID(e.ctx, tk.ID); err != nil {
		t.Fatalf("the ticket row itself must exist: %v", err)
	}
}

// TestCreateTicketProvenanceNamesCauseRun: with a causing run in the chain, the provenance is
// the plan's exact "from run #N" sentence.
func TestCreateTicketProvenanceNamesCauseRun(t *testing.T) {
	e := newEnv(t)
	e.mkColumn("Backlog", domain.CategoryBacklog, 1)
	agent := e.mkAgent("Dev")
	run, err := e.sch.Enqueue(e.ctx, sched.RunRequest{
		ProjectID: e.proj.ID, AgentID: agent.ID, Reason: "test seed",
	})
	if err != nil {
		t.Fatal(err)
	}

	tr := e.mkTrigger("file it", "pull_request", `["opened"]`, noGuardNoise,
		`[{"action_id":"create_ticket","params":{"title":"follow-up"}}]`)
	ev := e.emit("pull_request", "opened", domain.ActorAgent, &agent.ID, &run.ID, prPayload(7))
	f := e.firing(tr.ID, ev.ID)
	if f.Outcome != domain.FiringSucceeded {
		t.Fatalf("firing outcome = %s (%s), want succeeded", f.Outcome, f.Reason)
	}
	tk, err := e.st.Tickets().ByKey(e.ctx, "PAY-1")
	if err != nil {
		t.Fatal(err)
	}
	item, err := e.st.Triage().ByTicket(e.ctx, tk.ID)
	if err != nil {
		t.Fatal(err)
	}
	want := fmt.Sprintf("Created by trigger `file it` from run #%d", run.Seq)
	if item.Provenance != want {
		t.Fatalf("provenance = %q, want %q", item.Provenance, want)
	}
	if item.SourceRunID == nil || *item.SourceRunID != run.ID {
		t.Fatalf("triage source_run_id = %v, want %s", item.SourceRunID, run.ID)
	}
}

// TestMoveTicketByCategorySurvivesRename is acceptance (b): the rule stores a category, so
// renaming the review column changes nothing (brief D2).
func TestMoveTicketByCategorySurvivesRename(t *testing.T) {
	e := newEnv(t)
	e.mkColumn("Backlog", domain.CategoryBacklog, 1)
	review := e.mkColumn("Review", domain.CategoryReview, 2)

	created, err := e.tick.Create(e.ctx, e.proj.Key, ticketsvc.CreateInput{Title: "Fix checkout"})
	if err != nil {
		t.Fatal(err)
	}
	tk := created.Ticket

	tr := e.mkTrigger("to review", "pull_request", `["opened"]`, noGuardNoise,
		`[{"action_id":"move_ticket","params":{"category":"review"}}]`)

	// Rename the column AFTER the rule exists — the whole point of D2.
	review.Name = "Everything Else"
	if err := e.st.Columns().Update(e.ctx, &review); err != nil {
		t.Fatal(err)
	}

	payload := prPayload(3)
	payload["ticket"] = map[string]any{"key": tk.Key}
	ev := e.emit("pull_request", "opened", domain.ActorHuman, nil, nil, payload)
	f := e.firing(tr.ID, ev.ID)
	if f.Outcome != domain.FiringSucceeded {
		t.Fatalf("firing outcome = %s (%s), want succeeded", f.Outcome, f.Reason)
	}
	moved, err := e.st.Tickets().ByID(e.ctx, tk.ID)
	if err != nil {
		t.Fatal(err)
	}
	if moved.ColumnID != review.ID {
		t.Fatalf("ticket column = %s, want the renamed review column %s", moved.ColumnID, review.ID)
	}

	// Firing again for the same category is a no_action, not a re-move.
	ev2 := e.emit("pull_request", "opened", domain.ActorHuman, nil, nil, payload)
	f2 := e.firing(tr.ID, ev2.ID)
	if f2.Outcome != domain.FiringNoAction {
		t.Fatalf("second firing outcome = %s (%s), want no_action", f2.Outcome, f2.Reason)
	}
}

// TestMoveTicketNamedErrors: no column of the category is the named error, and a
// pending-triage ticket is invisible to move_ticket (§10 invariant 7).
func TestMoveTicketNamedErrors(t *testing.T) {
	e := newEnv(t)
	e.mkColumn("Backlog", domain.CategoryBacklog, 1)

	created, err := e.tick.Create(e.ctx, e.proj.Key, ticketsvc.CreateInput{Title: "Plain"})
	if err != nil {
		t.Fatal(err)
	}
	tr := e.mkTrigger("to canceled", "pull_request", `["opened"]`, noGuardNoise,
		`[{"action_id":"move_ticket","params":{"category":"canceled"}}]`)
	payload := prPayload(4)
	payload["ticket"] = map[string]any{"key": created.Ticket.Key}
	ev := e.emit("pull_request", "opened", domain.ActorHuman, nil, nil, payload)
	f := e.firing(tr.ID, ev.ID)
	if f.Outcome != domain.FiringErrored {
		t.Fatalf("firing outcome = %s, want errored", f.Outcome)
	}
	if !strings.Contains(f.Reason, "no canceled-category column") {
		t.Fatalf("firing reason = %q, want the named no-column error", f.Reason)
	}

	// A trigger-created (pending triage) ticket refuses the move by name.
	inTriage, err := e.tick.CreateFromTrigger(e.ctx, ticketsvc.TriggerCreateInput{
		ProjectID: e.proj.ID, Title: "hidden", Provenance: "Created by trigger `x` from a test event",
	})
	if err != nil {
		t.Fatal(err)
	}
	var pendingErr *ticketsvc.PendingTriageError
	if _, err := e.tick.TriggerMoveToCategory(e.ctx, inTriage.ID, domain.CategoryBacklog, "test"); !errors.As(err, &pendingErr) {
		t.Fatalf("moving a pending-triage ticket = %v, want PendingTriageError", err)
	}
}

// TestPostCommentSuppressedOnRepoll is acceptance (c): the action's own comment carries the
// acting agent's marker; re-polled as an event (attribution simulated the way the poller
// resolves the marker), actor suppression — guard layer 1 — drops it.
func TestPostCommentSuppressedOnRepoll(t *testing.T) {
	e := newEnv(t)
	agent := e.mkAgent("Reviewer")

	// The connected repo whose token the write path reads.
	info, _, err := e.sec.Set(e.ctx, secrets.SetInput{
		Scope: domain.SecretScopeProject, ProjectID: e.proj.ID,
		Name: "gh", Value: "tok", CreatedBy: e.owner.ID,
	})
	if err != nil {
		t.Fatal(err)
	}
	now := domain.Now()
	repo := domain.Repo{ProjectID: e.proj.ID, Provider: "github", Owner: "acme", Name: "payments",
		TokenSecretID: &info.ID, CreatedAt: now, UpdatedAt: now}
	if err := e.st.Repos().Create(e.ctx, &repo); err != nil {
		t.Fatal(err)
	}

	tr := e.mkTrigger("ping", "issue_comment", `["created"]`, noGuardNoise,
		`[{"action_id":"post_comment","params":{"agent_id":"`+agent.ID+`","body":"Thanks for PR {{pr.number}}!"}}]`)

	// A human comment fires the rule: one comment posted, marker appended.
	ev := e.emit("issue_comment", "created", domain.ActorHuman, nil, nil, prPayload(219))
	f := e.firing(tr.ID, ev.ID)
	if f.Outcome != domain.FiringSucceeded {
		t.Fatalf("firing outcome = %s (%s), want succeeded", f.Outcome, f.Reason)
	}
	posted := e.forge.posted()
	if len(posted) != 1 {
		t.Fatalf("comments posted = %d, want 1", len(posted))
	}
	if posted[0].PR != 219 || !strings.Contains(posted[0].Body, "Thanks for PR 219!") {
		t.Fatalf("posted = %+v", posted[0])
	}
	if !strings.Contains(posted[0].Body, "<!-- lexicode:actor=agent:"+agent.ID) {
		t.Fatalf("comment body lacks the D-9 marker: %q", posted[0].Body)
	}

	// The comment comes back around the poll loop. The poller's attribution resolves the
	// marker to (agent, no run) — simulated here as the resulting event columns.
	ev2 := e.emit("issue_comment", "created", domain.ActorAgent, &agent.ID, nil, prPayload(219))
	f2 := e.firing(tr.ID, ev2.ID)
	if f2.Outcome != domain.FiringNoAction || f2.Reason != "actor suppressed" {
		t.Fatalf("re-poll firing = %s (%q), want no_action 'actor suppressed'", f2.Outcome, f2.Reason)
	}
	if got := len(e.forge.posted()); got != 1 {
		t.Fatalf("comments posted after suppression = %d, want still 1", got)
	}
}

// TestRunAgentEnqueuesWithGuardPassThrough is acceptance (d): run_agent goes through
// Scheduler().Enqueue with the guard's stage-3 decision copied onto the RunRequest — subject
// key and depth on the run row, and cancel-in-progress supersession applied by the scheduler.
func TestRunAgentEnqueuesWithGuardPassThrough(t *testing.T) {
	e := newEnv(t)
	agent := e.mkAgent("Reviewer")

	tr := e.mkTrigger("review PRs", "pull_request", `["opened","synchronize"]`,
		`{"debounce_seconds":0,"cancel_in_progress":true}`,
		`[{"action_id":"run_agent","params":{"agent_id":"`+agent.ID+`","prompt_override":"Review PR {{pr.number}} carefully."}}]`)

	ev := e.emit("pull_request", "opened", domain.ActorHuman, nil, nil, prPayload(219))
	f := e.firing(tr.ID, ev.ID)
	if f.Outcome != domain.FiringSucceeded {
		t.Fatalf("firing outcome = %s (%s), want succeeded", f.Outcome, f.Reason)
	}
	if f.RunID == nil {
		t.Fatal("firing has no run id")
	}
	run := e.run(*f.RunID)
	if run.State != domain.RunQueued {
		t.Fatalf("run state = %s, want queued (scheduler never started)", run.State)
	}
	if run.AgentID != agent.ID {
		t.Fatalf("run agent = %s, want %s", run.AgentID, agent.ID)
	}
	if run.SubjectKey != "pr:219" {
		t.Fatalf("run subject_key = %q, want pr:219 (guard pass-through)", run.SubjectKey)
	}
	if run.Depth != 0 {
		t.Fatalf("run depth = %d, want 0 for a chain root", run.Depth)
	}
	if run.TriggerID == nil || *run.TriggerID != tr.ID {
		t.Fatalf("run trigger_id = %v, want %s", run.TriggerID, tr.ID)
	}
	if run.CauseEventID == nil || *run.CauseEventID != ev.ID {
		t.Fatalf("run cause_event_id = %v, want %s", run.CauseEventID, ev.ID)
	}
	if run.StateReason != "trigger review PRs" {
		t.Fatalf("run reason = %q", run.StateReason)
	}
	if !strings.Contains(run.Prompt, "Review PR 219 carefully.") {
		t.Fatal("the interpolated prompt override did not reach the run's prompt")
	}

	// Second push on the same subject: cancel-in-progress elects run #1; the scheduler
	// cancels it AFTER run #2 exists, naming its seq — SupersededRunID travelled.
	ev2 := e.emit("pull_request", "synchronize", domain.ActorHuman, nil, nil, prPayload(219))
	f2 := e.firing(tr.ID, ev2.ID)
	if f2.Outcome != domain.FiringSucceeded {
		t.Fatalf("second firing = %s (%s), want succeeded", f2.Outcome, f2.Reason)
	}
	run2 := e.run(*f2.RunID)
	first := e.run(run.ID)
	if first.State != domain.RunCanceled {
		t.Fatalf("first run state = %s, want canceled (superseded)", first.State)
	}
	wantReason := fmt.Sprintf("superseded by run #%d", run2.Seq)
	if !strings.Contains(first.StateReason, wantReason) {
		t.Fatalf("first run reason = %q, want %q", first.StateReason, wantReason)
	}
}

// TestRunAgentResolvesAgentName: the S15 bootstrap's {"agent_name": …, "prompt": …} params
// still resolve against the roster.
func TestRunAgentResolvesAgentName(t *testing.T) {
	e := newEnv(t)
	agent := e.mkAgent("Dev")
	tr := e.mkTrigger("legacy rule", "pull_request", `["opened"]`, noGuardNoise,
		`[{"action_id":"run_agent","params":{"agent_name":"Dev","prompt":""}}]`)
	ev := e.emit("pull_request", "opened", domain.ActorHuman, nil, nil, prPayload(5))
	f := e.firing(tr.ID, ev.ID)
	if f.Outcome != domain.FiringSucceeded {
		t.Fatalf("firing outcome = %s (%s), want succeeded", f.Outcome, f.Reason)
	}
	run := e.run(*f.RunID)
	if run.AgentID != agent.ID {
		t.Fatalf("run agent = %s, want the name-resolved %s", run.AgentID, agent.ID)
	}
}

// TestNotifyRoutesToDelegatingHuman is acceptance (e): the D1 ladder — the causing run's
// requester, else the run's ticket's assignee, else the project owner — through the real S24
// routing and the real in-app notifier.
func TestNotifyRoutesToDelegatingHuman(t *testing.T) {
	e := newEnv(t)
	e.mkColumn("Backlog", domain.CategoryBacklog, 1)
	agent := e.mkAgent("Dev")
	requester := e.mkUser("Bea")
	assignee := e.mkUser("Cyn")

	tr := e.mkTrigger("tell me", "pull_request", `["opened"]`, noGuardNoise,
		`[{"action_id":"notify","params":{"message":"PR {{pr.number}} needs eyes"}}]`)

	assertNotified := func(userID string, wantTitle string) {
		t.Helper()
		ns, err := e.st.Notifications().ForUser(e.ctx, userID)
		if err != nil {
			t.Fatal(err)
		}
		for _, n := range ns {
			if n.Title == wantTitle {
				if n.State != domain.NotificationUnread {
					t.Fatalf("notification state = %s, want unread", n.State)
				}
				return
			}
		}
		t.Fatalf("user %s has no notification titled %q (has %d rows)", userID, wantTitle, len(ns))
	}

	// Rung 1: the causing run's requester.
	run1, err := e.sch.Enqueue(e.ctx, sched.RunRequest{
		ProjectID: e.proj.ID, AgentID: agent.ID, Reason: "seed",
		RequestedByUserID: requester.ID,
	})
	if err != nil {
		t.Fatal(err)
	}
	ev1 := e.emit("pull_request", "opened", domain.ActorAgent, &agent.ID, &run1.ID, prPayload(1))
	f1 := e.firing(tr.ID, ev1.ID)
	if f1.Outcome != domain.FiringSucceeded {
		t.Fatalf("firing 1 = %s (%s)", f1.Outcome, f1.Reason)
	}
	assertNotified(requester.ID, "PR 1 needs eyes")

	// Rung 2: no requester; the run's ticket's assignee.
	created, err := e.tick.Create(e.ctx, e.proj.Key, ticketsvc.CreateInput{
		Title: "Assigned", AssigneeID: assignee.ID,
	})
	if err != nil {
		t.Fatal(err)
	}
	run2, err := e.sch.Enqueue(e.ctx, sched.RunRequest{
		ProjectID: e.proj.ID, AgentID: agent.ID, Reason: "seed", TicketID: created.Ticket.ID,
	})
	if err != nil {
		t.Fatal(err)
	}
	ev2 := e.emit("pull_request", "opened", domain.ActorAgent, &agent.ID, &run2.ID, prPayload(2))
	f2 := e.firing(tr.ID, ev2.ID)
	if f2.Outcome != domain.FiringSucceeded {
		t.Fatalf("firing 2 = %s (%s)", f2.Outcome, f2.Reason)
	}
	assertNotified(assignee.ID, "PR 2 needs eyes")

	// Rung 3: no causing run at all → the project owner.
	ev3 := e.emit("pull_request", "opened", domain.ActorHuman, nil, nil, prPayload(3))
	f3 := e.firing(tr.ID, ev3.ID)
	if f3.Outcome != domain.FiringSucceeded {
		t.Fatalf("firing 3 = %s (%s)", f3.Outcome, f3.Reason)
	}
	assertNotified(e.owner.ID, "PR 3 needs eyes")
}

// TestSaveTimeValidationVetsParams: the S26 trigger CRUD, now with the registry populated,
// refuses bad params through each action's Describe — and accepts the good shape.
func TestSaveTimeValidationVetsParams(t *testing.T) {
	e := newEnv(t)

	strPtr := func(s string) *string { return &s }
	rawPtr := func(s string) *json.RawMessage { r := json.RawMessage(s); return &r }
	mk := func(name, actions string) error {
		_, err := e.trg.Create(e.ctx, e.proj.Key, triggersvc.Input{
			Name: strPtr(name), SourceID: strPtr("github.poll"), Event: strPtr("pull_request"),
			ActivityTypes: &[]string{"opened"},
			Actions:       rawPtr(actions),
		}, e.owner.ID)
		return err
	}

	cases := []struct {
		name    string
		actions string
		wantErr string // "" = must save
	}{
		{"run_agent no agent", `[{"action_id":"run_agent","params":{"prompt_override":"x"}}]`, "an agent is required"},
		{"run_agent typoed key", `[{"action_id":"run_agent","params":{"agent":"x"}}]`, "unknown field"},
		{"create_ticket no title", `[{"action_id":"create_ticket","params":{}}]`, "a title is required"},
		{"move_ticket bad category", `[{"action_id":"move_ticket","params":{"category":"Review Column"}}]`, "not a column category"},
		{"post_comment no body", `[{"action_id":"post_comment","params":{"agent_id":"a1"}}]`, "a comment body is required"},
		{"notify no message", `[{"action_id":"notify","params":{}}]`, "a message is required"},
		{"all good", `[{"action_id":"create_ticket","params":{"title":"CI failed on {{pr.number}}"}},` +
			`{"action_id":"notify","params":{"message":"done"}}]`, ""},
	}
	for _, tc := range cases {
		err := mk(tc.name, tc.actions)
		if tc.wantErr == "" {
			if err != nil {
				t.Fatalf("%s: save failed: %v", tc.name, err)
			}
			continue
		}
		var verr *triggersvc.ValidationError
		if !errors.As(err, &verr) {
			t.Fatalf("%s: err = %v, want a ValidationError", tc.name, err)
		}
		if !strings.Contains(errText(verr), tc.wantErr) {
			t.Fatalf("%s: field errors = %q, want them to contain %q", tc.name, errText(verr), tc.wantErr)
		}
	}
}

// errText renders every field error message, not just the summary line.
func errText(verr *triggersvc.ValidationError) string {
	parts := make([]string, 0, len(verr.Fields))
	for _, f := range verr.Fields {
		parts = append(parts, f.Message)
	}
	return strings.Join(parts, "; ")
}
