// S31 acceptance over the S28 pipeline: a ticket filed by the REAL create_ticket action —
// event → trigger engine → action → tickets service — never appears on the board before
// acceptance, and accepting it puts it in the backlog-category column it was parked in.
package actions_test

import (
	"testing"

	"github.com/spruce/lexicode/internal/domain"
)

func TestTriageAcceptThroughRealAction(t *testing.T) {
	e := newEnv(t)
	backlog := e.mkColumn("Backlog", domain.CategoryBacklog, 1)

	tr := e.mkTrigger("CI failed → file a ticket", "pull_request", `["opened"]`, noGuardNoise,
		`[{"action_id":"create_ticket","params":{"title":"CI failed on PR {{pr.number}}"}}]`)
	ev := e.emit("pull_request", "opened", domain.ActorHuman, nil, nil, prPayload(219))
	f := e.firing(tr.ID, ev.ID)
	if f.Outcome != domain.FiringSucceeded {
		t.Fatalf("firing outcome = %s (%s), want succeeded", f.Outcome, f.Reason)
	}
	tk, err := e.st.Tickets().ByKey(e.ctx, "PAY-1")
	if err != nil {
		t.Fatal(err)
	}

	// Never on the board before acceptance.
	list, err := e.tick.List(e.ctx, e.proj.Key, false)
	if err != nil {
		t.Fatal(err)
	}
	for _, it := range list {
		if it.Ticket.ID == tk.ID {
			t.Fatal("a pending-triage ticket is visible on the board before acceptance")
		}
	}

	// Accept through the S31 service verb.
	item, err := e.st.Triage().ByTicket(e.ctx, tk.ID)
	if err != nil {
		t.Fatal(err)
	}
	accepted, err := e.tick.TriageAccept(e.ctx, item.ID)
	if err != nil {
		t.Fatal(err)
	}
	if accepted.Item.State != domain.TriageAccepted {
		t.Fatalf("item state = %s, want accepted", accepted.Item.State)
	}

	// Now on the board, in the backlog-category column, with a real position.
	list, err = e.tick.List(e.ctx, e.proj.Key, false)
	if err != nil {
		t.Fatal(err)
	}
	var found bool
	for _, it := range list {
		if it.Ticket.ID == tk.ID {
			found = true
			if it.Ticket.ColumnID != backlog.ID || it.Category != domain.CategoryBacklog {
				t.Fatalf("accepted ticket in column %s (%s), want the backlog column %s",
					it.Ticket.ColumnID, it.Category, backlog.ID)
			}
			if it.Ticket.Position <= 0 {
				t.Fatalf("accepted ticket position = %v, want > 0", it.Ticket.Position)
			}
		}
	}
	if !found {
		t.Fatal("accepted ticket is still invisible to the board")
	}

	// And move_ticket works on it now — the §10.7 exclusion ended with the accept.
	if _, err := e.tick.TriggerMoveToCategory(e.ctx, tk.ID, domain.CategoryBacklog, "test"); err != nil {
		t.Fatalf("move after accept = %v, want it allowed", err)
	}
}
