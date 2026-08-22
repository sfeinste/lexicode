// S31 acceptance: the triage queue and its four verbs, over the S28 write side.
package tickets_test

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/spruce/lexicode/internal/domain"
	"github.com/spruce/lexicode/internal/kernel/audit"
	"github.com/spruce/lexicode/internal/service/tickets"
)

// triageSvc builds a second tickets.Service over the test env's store for the calls the
// HTTP surface does not carry (CreateFromTrigger, the wake handlers). now overrides the
// clock; nil keeps the real one.
func triageSvc(e *env, now func() string) *tickets.Service {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	return tickets.New(tickets.Options{
		Store: e.st, Audit: audit.New(audit.Options{Store: e.st, Logger: logger}),
		Logger: logger, Now: now,
	})
}

// projectID resolves a project key to its id through the store.
func (e *env) projectID(key string) string {
	e.t.Helper()
	p, err := e.st.Projects().ByKey(context.Background(), key)
	if err != nil {
		e.t.Fatal(err)
	}
	return p.ID
}

// TestTriageAcceptMakesBoardVisible: a trigger-created ticket is invisible to the board
// until `accept`, then appears in the backlog-category column it was parked in — DoD (2).
func TestTriageAcceptMakesBoardVisible(t *testing.T) {
	e := newEnv(t)
	c, _ := e.owner()
	cols := e.createProject(c, "PAY")
	backlog := columnByCategory(cols, "backlog")
	projectID := e.projectID("PAY")
	svc := triageSvc(e, nil)

	tk, err := svc.CreateFromTrigger(context.Background(), tickets.TriggerCreateInput{
		ProjectID: projectID, Title: "CI failed on PR 219",
		Provenance: "Created by trigger `CI failed → file a ticket` from run #482",
	})
	if err != nil {
		t.Fatal(err)
	}

	// Not on the board before acceptance.
	for _, row := range e.listTickets(c, "PAY", false) {
		if row["id"] == tk.ID {
			t.Fatal("a pending-triage ticket is visible in the tickets list")
		}
	}

	// The queue shows it, provenance verbatim.
	status, body := e.doJSON(c, "GET", "/api/v1/projects/PAY/triage", "")
	if status != http.StatusOK {
		t.Fatalf("GET triage = %d: %v", status, body)
	}
	items, _ := body["items"].([]any)
	if len(items) != 1 {
		t.Fatalf("triage items = %d, want 1", len(items))
	}
	row := items[0].(map[string]any)
	if row["provenance"] != "Created by trigger `CI failed → file a ticket` from run #482" {
		t.Fatalf("provenance = %q, not verbatim", row["provenance"])
	}
	itemID, _ := row["id"].(string)

	// Accept over HTTP — the real route, membership and all.
	status, out := e.doJSON(c, "POST", "/api/v1/triage/"+itemID+"/accept", "")
	if status != http.StatusOK {
		t.Fatalf("accept = %d: %v", status, out)
	}
	if out["state"] != "accepted" {
		t.Fatalf("state after accept = %v, want accepted", out["state"])
	}
	if out["resolved_by"] == nil || out["resolved_at"] == nil {
		t.Fatalf("accept did not stamp the resolver: %v", out)
	}

	// Now on the board, in the backlog-category column it was created into.
	var found map[string]any
	for _, r := range e.listTickets(c, "PAY", false) {
		if r["id"] == tk.ID {
			found = r
		}
	}
	if found == nil {
		t.Fatal("accepted ticket is still invisible to the board")
	}
	if found["category"] != "backlog" || found["column_id"] != backlog["id"] {
		t.Fatalf("accepted ticket landed in %v/%v, want the backlog column %v",
			found["category"], found["column_id"], backlog["id"])
	}
	if pos, _ := found["position"].(float64); pos <= 0 {
		t.Fatalf("accepted ticket position = %v, want a real board position", found["position"])
	}

	// A second verb on the resolved item is the typed 409.
	status, prob := e.doJSON(c, "POST", "/api/v1/triage/"+itemID+"/decline", `{}`)
	if status != http.StatusConflict || prob["type"] != "triage_resolved" {
		t.Fatalf("verb on resolved item = %d %v, want 409 triage_resolved", status, prob)
	}

	// The stream and audit both recorded the accept.
	var accepted bool
	for _, s := range e.stream(c, tk.ID) {
		if strings.Contains(fmt.Sprint(s["payload"]), "triage_accepted") {
			accepted = true
		}
	}
	if !accepted {
		t.Fatal("no triage_accepted stream row")
	}
	if got := e.auditActions(itemID); len(got) == 0 || got[len(got)-1] != "triage.accept" {
		t.Fatalf("audit actions for item = %v, want triage.accept", got)
	}
}

// TestTriageDuplicateMerges: `2` on a duplicate leaves ONE ticket with both provenances —
// the survivor's stream carries the duplicate's provenance line — and the duplicate ticket
// is archived; labels, criteria and mentions transfer. DoD (3).
func TestTriageDuplicateMerges(t *testing.T) {
	e := newEnv(t)
	c, _ := e.owner()
	e.createProject(c, "PAY")
	projectID := e.projectID("PAY")
	svc := triageSvc(e, nil)
	ctx := context.Background()

	// The survivor: a human ticket with one criterion of its own.
	survivor := e.createTicket(c, "PAY", "Fix checkout timeout", "")
	survivorID := survivor["id"].(string)
	if _, err := svc.AddCriterion(ctx, survivorID, "existing criterion"); err != nil {
		t.Fatal(err)
	}

	// The duplicate arrives from a trigger, with a label and two criteria.
	label, err := svc.CreateLabel(ctx, "PAY", "bug", "#ff0000")
	if err != nil {
		t.Fatal(err)
	}
	dup, err := svc.CreateFromTrigger(ctx, tickets.TriggerCreateInput{
		ProjectID: projectID, Title: "Checkout is broken",
		LabelNames: []string{"bug"},
		Provenance: "Created by trigger `CI failed → file a ticket` from run #482",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.AddCriterion(ctx, dup.ID, "repro documented"); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.AddCriterion(ctx, dup.ID, "fix verified on staging"); err != nil {
		t.Fatal(err)
	}
	// A mention pointing at the duplicate, to be redirected.
	if err := e.st.Mentions().ReplaceForSource(ctx, "wiki", "page-1", []domain.Mention{{
		ProjectID: projectID, FromKind: "wiki", FromID: "page-1",
		ToKind: "ticket", ToID: dup.ID, Linked: true, ContextText: "see the broken checkout",
	}}); err != nil {
		t.Fatal(err)
	}
	item, err := e.st.Triage().ByTicket(ctx, dup.ID)
	if err != nil {
		t.Fatal(err)
	}

	status, out := e.doJSON(c, "POST", "/api/v1/triage/"+item.ID+"/duplicate",
		fmt.Sprintf(`{"of_ticket_id":%q}`, survivorID))
	if status != http.StatusOK {
		t.Fatalf("duplicate = %d: %v", status, out)
	}
	if out["state"] != "duplicate" || out["duplicate_of"] != survivorID {
		t.Fatalf("item after duplicate = %v", out)
	}

	// The duplicate ticket is archived.
	archived, err := e.st.Tickets().ByID(ctx, dup.ID)
	if err != nil {
		t.Fatal(err)
	}
	if archived.ArchivedAt == nil {
		t.Fatal("duplicate ticket was not archived")
	}

	// The survivor's stream carries the merge WITH the duplicate's provenance — one ticket,
	// both provenances.
	var merged, hasProvenance bool
	for _, s := range e.stream(c, survivorID) {
		p := fmt.Sprint(s["payload"])
		if strings.Contains(p, "merged_from") && strings.Contains(p, dup.Key) {
			merged = true
			if strings.Contains(p, "Created by trigger `CI failed → file a ticket` from run #482") {
				hasProvenance = true
			}
		}
	}
	if !merged || !hasProvenance {
		t.Fatalf("survivor stream merged=%v provenance=%v, want both", merged, hasProvenance)
	}
	// ...and the duplicate's stream names the survivor.
	var pointsAtSurvivor bool
	for _, s := range e.stream(c, dup.ID) {
		if strings.Contains(fmt.Sprint(s["payload"]), "triage_duplicate") &&
			strings.Contains(fmt.Sprint(s["payload"]), survivor["key"].(string)) {
			pointsAtSurvivor = true
		}
	}
	if !pointsAtSurvivor {
		t.Fatal("duplicate's stream does not record the merge target")
	}

	// Labels transferred (deduplicated), criteria appended after the survivor's own,
	// mentions redirected.
	labels, err := e.st.Labels().ForTicket(ctx, survivorID)
	if err != nil {
		t.Fatal(err)
	}
	if len(labels) != 1 || labels[0].ID != label.ID {
		t.Fatalf("survivor labels = %+v, want the transferred bug label", labels)
	}
	crit, err := e.st.Criteria().ForTicket(ctx, survivorID)
	if err != nil {
		t.Fatal(err)
	}
	if len(crit) != 3 || crit[0].Text != "existing criterion" ||
		crit[1].Text != "repro documented" || crit[2].Text != "fix verified on staging" {
		texts := make([]string, len(crit))
		for i, cr := range crit {
			texts[i] = cr.Text
		}
		t.Fatalf("survivor criteria = %v, want the duplicate's appended after its own", texts)
	}
	ms, err := e.st.Mentions().ForTarget(ctx, "ticket", survivorID)
	if err != nil {
		t.Fatal(err)
	}
	if len(ms) != 1 || ms[0].ContextText != "see the broken checkout" {
		t.Fatalf("mentions at survivor = %+v, want the redirected one", ms)
	}
	if got := e.auditActions(item.ID); len(got) == 0 || got[len(got)-1] != "triage.duplicate" {
		t.Fatalf("audit actions = %v, want triage.duplicate", got)
	}
}

// TestTriageDeclineArchivesWithReason: `3` cancels — the item resolves declined, the ticket
// is archived, and the reason lands on the item and in the stream.
func TestTriageDeclineArchivesWithReason(t *testing.T) {
	e := newEnv(t)
	c, _ := e.owner()
	e.createProject(c, "PAY")
	svc := triageSvc(e, nil)
	ctx := context.Background()

	tk, err := svc.CreateFromTrigger(ctx, tickets.TriggerCreateInput{
		ProjectID: e.projectID("PAY"), Title: "noise", Provenance: "Created by trigger `x`",
	})
	if err != nil {
		t.Fatal(err)
	}
	item, err := e.st.Triage().ByTicket(ctx, tk.ID)
	if err != nil {
		t.Fatal(err)
	}
	status, out := e.doJSON(c, "POST", "/api/v1/triage/"+item.ID+"/decline",
		`{"reason":"flaky test, not a bug"}`)
	if status != http.StatusOK {
		t.Fatalf("decline = %d: %v", status, out)
	}
	if out["state"] != "declined" || out["reason"] != "flaky test, not a bug" {
		t.Fatalf("item after decline = %v", out)
	}
	archived, err := e.st.Tickets().ByID(ctx, tk.ID)
	if err != nil {
		t.Fatal(err)
	}
	if archived.ArchivedAt == nil {
		t.Fatal("declined ticket was not archived")
	}
	var declined bool
	for _, s := range e.stream(c, tk.ID) {
		p := fmt.Sprint(s["payload"])
		if strings.Contains(p, "triage_declined") && strings.Contains(p, "flaky test, not a bug") {
			declined = true
		}
	}
	if !declined {
		t.Fatal("no triage_declined stream row carrying the reason")
	}
}

// TestTriageSnoozeUntilActivityWakes: a snoozed-until-activity item reappears when its PR
// gets a comment (an event whose subject_number matches the ticket's linked PR) — DoD (4a).
func TestTriageSnoozeUntilActivityWakes(t *testing.T) {
	e := newEnv(t)
	c, _ := e.owner()
	e.createProject(c, "PAY")
	projectID := e.projectID("PAY")
	svc := triageSvc(e, nil)
	ctx := context.Background()

	tk, err := svc.CreateFromTrigger(ctx, tickets.TriggerCreateInput{
		ProjectID: projectID, Title: "flaky suite", Provenance: "Created by trigger `x`",
	})
	if err != nil {
		t.Fatal(err)
	}
	// Link a PR to the ticket, the way the forge stories do.
	pr := int64(219)
	tk.PRNumber = &pr
	if err := e.st.Tickets().Update(ctx, &tk); err != nil {
		t.Fatal(err)
	}
	item, err := e.st.Triage().ByTicket(ctx, tk.ID)
	if err != nil {
		t.Fatal(err)
	}

	// Snooze until new activity: state snoozed, snooze_until NULL.
	status, out := e.doJSON(c, "POST", "/api/v1/triage/"+item.ID+"/snooze", `{"until":null}`)
	if status != http.StatusOK {
		t.Fatalf("snooze = %d: %v", status, out)
	}
	if out["state"] != "snoozed" || out["snooze_until"] != nil {
		t.Fatalf("item after snooze = %v, want snoozed with null until", out)
	}
	// Snoozed items stay off the board.
	for _, r := range e.listTickets(c, "PAY", false) {
		if r["id"] == tk.ID {
			t.Fatal("a snoozed ticket is visible on the board")
		}
	}

	// An unrelated PR's comment does NOT wake it.
	other := int64(7)
	unrelated := domain.Event{
		ID: domain.NewID(), ProjectID: &projectID, Source: "github",
		Kind: "issue_comment", ActivityType: "created", ActorKind: domain.ActorHuman,
		SubjectKind: "pr", SubjectNumber: &other, OccurredAt: domain.Now(),
	}
	if err := svc.TriageWakeOnEvent(ctx, unrelated); err != nil {
		t.Fatal(err)
	}
	after, err := e.st.Triage().ByID(ctx, item.ID)
	if err != nil {
		t.Fatal(err)
	}
	if after.State != domain.TriageSnoozed {
		t.Fatalf("unrelated event woke the item: state = %s", after.State)
	}

	// A comment on the ticket's own PR wakes it: snoozed → pending.
	comment := domain.Event{
		ID: domain.NewID(), ProjectID: &projectID, Source: "github",
		Kind: "issue_comment", ActivityType: "created", ActorKind: domain.ActorHuman,
		SubjectKind: "pr", SubjectNumber: &pr, OccurredAt: domain.Now(),
	}
	if err := svc.TriageWakeOnEvent(ctx, comment); err != nil {
		t.Fatal(err)
	}
	after, err = e.st.Triage().ByID(ctx, item.ID)
	if err != nil {
		t.Fatal(err)
	}
	if after.State != domain.TriagePending {
		t.Fatalf("state after matching event = %s, want pending", after.State)
	}
	var woken bool
	for _, s := range e.stream(c, tk.ID) {
		p := fmt.Sprint(s["payload"])
		if strings.Contains(p, "triage_woken") && strings.Contains(p, "new_activity") {
			woken = true
		}
	}
	if !woken {
		t.Fatal("no triage_woken stream row")
	}
}

// TestTriageTimedSnoozeWakesViaTicker: a time-snoozed item wakes once the (faked) clock
// passes snooze_until — DoD (4b), driven through the exported scan, no sleeping.
func TestTriageTimedSnoozeWakesViaTicker(t *testing.T) {
	e := newEnv(t)
	c, _ := e.owner()
	e.createProject(c, "PAY")
	base := time.Date(2026, 8, 21, 12, 0, 0, 0, time.UTC)
	clock := base
	svc := triageSvc(e, func() string { return domain.FormatTime(clock) })
	ctx := context.Background()

	tk, err := svc.CreateFromTrigger(ctx, tickets.TriggerCreateInput{
		ProjectID: e.projectID("PAY"), Title: "later", Provenance: "Created by trigger `x`",
	})
	if err != nil {
		t.Fatal(err)
	}
	item, err := e.st.Triage().ByTicket(ctx, tk.ID)
	if err != nil {
		t.Fatal(err)
	}
	until := domain.FormatTime(base.Add(24 * time.Hour))
	if _, err := svc.TriageSnooze(ctx, item.ID, &until); err != nil {
		t.Fatal(err)
	}

	// Before the deadline the scan is a no-op.
	svc.WakeDueTriage(ctx)
	mid, err := e.st.Triage().ByID(ctx, item.ID)
	if err != nil {
		t.Fatal(err)
	}
	if mid.State != domain.TriageSnoozed {
		t.Fatalf("scan before the deadline changed state to %s", mid.State)
	}

	// Advance past the deadline: the scan wakes it.
	clock = base.Add(25 * time.Hour)
	svc.WakeDueTriage(ctx)
	after, err := e.st.Triage().ByID(ctx, item.ID)
	if err != nil {
		t.Fatal(err)
	}
	if after.State != domain.TriagePending || after.SnoozeUntil != nil {
		t.Fatalf("state after due scan = %s (until %v), want pending/nil", after.State, after.SnoozeUntil)
	}
	var woken bool
	for _, s := range e.stream(c, tk.ID) {
		p := fmt.Sprint(s["payload"])
		if strings.Contains(p, "triage_woken") && strings.Contains(p, "snooze_expired") {
			woken = true
		}
	}
	if !woken {
		t.Fatal("no triage_woken (snooze_expired) stream row")
	}
}

// TestTriageBadgeCountsPendingOnly: the badge count is `pending` only — snoozed items are
// parked and never count — DoD (5).
func TestTriageBadgeCountsPendingOnly(t *testing.T) {
	e := newEnv(t)
	c, _ := e.owner()
	e.createProject(c, "PAY")
	projectID := e.projectID("PAY")
	svc := triageSvc(e, nil)
	ctx := context.Background()

	for i := range 2 {
		if _, err := svc.CreateFromTrigger(ctx, tickets.TriggerCreateInput{
			ProjectID: projectID, Title: fmt.Sprintf("pending %d", i),
			Provenance: "Created by trigger `x`",
		}); err != nil {
			t.Fatal(err)
		}
	}
	tk, err := svc.CreateFromTrigger(ctx, tickets.TriggerCreateInput{
		ProjectID: projectID, Title: "parked", Provenance: "Created by trigger `x`",
	})
	if err != nil {
		t.Fatal(err)
	}
	item, err := e.st.Triage().ByTicket(ctx, tk.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.TriageSnooze(ctx, item.ID, nil); err != nil {
		t.Fatal(err)
	}

	status, body := e.doJSON(c, "GET", "/api/v1/projects/PAY/triage", "")
	if status != http.StatusOK {
		t.Fatalf("GET triage = %d: %v", status, body)
	}
	if body["pending_count"] != float64(2) || body["snoozed_count"] != float64(1) {
		t.Fatalf("counts = %v/%v, want 2 pending, 1 snoozed",
			body["pending_count"], body["snoozed_count"])
	}
	// The list orders pending before snoozed.
	items, _ := body["items"].([]any)
	if len(items) != 3 {
		t.Fatalf("items = %d, want 3", len(items))
	}
	if items[0].(map[string]any)["state"] != "pending" ||
		items[2].(map[string]any)["state"] != "snoozed" {
		t.Fatalf("list order wrong: %v", items)
	}
	n, err := svc.TriagePendingCount(ctx, "PAY")
	if err != nil {
		t.Fatal(err)
	}
	if n != 2 {
		t.Fatalf("TriagePendingCount = %d, want 2 (pending only, never snoozed)", n)
	}
}
