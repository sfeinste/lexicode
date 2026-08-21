package tickets_test

import (
	"context"
	"fmt"
	"math"
	"net/http"
	"sort"
	"sync"
	"testing"
)

// TestConcurrentCreatesNeverCollideOnKey is the S10 acceptance "two concurrent creates never
// collide on a key": 12 parallel goroutines create tickets and every key must be unique, with
// the sequence numbers forming exactly 1..12 — the allocator burns nothing and repeats nothing.
func TestConcurrentCreatesNeverCollideOnKey(t *testing.T) {
	e := newEnv(t)
	c, _ := e.owner()
	e.createProject(c, "CONC")

	const n = 12
	var (
		wg   sync.WaitGroup
		mu   sync.Mutex
		keys []string
		seqs []int
	)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			status, body := e.doJSON(c, "POST", "/api/v1/projects/CONC/tickets",
				fmt.Sprintf(`{"title":"parallel %d"}`, i))
			mu.Lock()
			defer mu.Unlock()
			if status != http.StatusCreated {
				t.Errorf("create %d = %d, want 201: %v", i, status, body)
				return
			}
			keys = append(keys, body["key"].(string))
			seqs = append(seqs, int(body["seq"].(float64)))
		}(i)
	}
	wg.Wait()
	if t.Failed() {
		return
	}

	seen := map[string]bool{}
	for _, k := range keys {
		if seen[k] {
			t.Fatalf("duplicate ticket key %q across concurrent creates", k)
		}
		seen[k] = true
	}
	sort.Ints(seqs)
	for i, s := range seqs {
		if s != i+1 {
			t.Fatalf("sequences = %v, want exactly 1..%d", seqs, n)
		}
	}
	if want := fmt.Sprintf("CONC-%d", seqs[len(seqs)-1]); !seen[want] {
		t.Fatalf("expected key %s to exist, got %v", want, keys)
	}
}

// TestSubticketOneLevel: a sub-ticket can never be given a child, whichever door is tried —
// the subtickets endpoint, create-with-parent, or reparenting via PATCH.
func TestSubticketOneLevel(t *testing.T) {
	e := newEnv(t)
	c, _ := e.owner()
	e.createProject(c, "SUB")
	parent := e.createTicket(c, "SUB", "parent", "")
	parentID := parent["id"].(string)

	status, body := e.doJSON(c, "POST", "/api/v1/tickets/"+parentID+"/subtickets",
		`{"titles":["one","two","three","four"]}`)
	if status != http.StatusCreated {
		t.Fatalf("subtickets = %d, want 201: %v", status, body)
	}
	created := body["tickets"].([]any)
	if len(created) != 4 {
		t.Fatalf("subtickets created %d, want 4", len(created))
	}
	childID := created[0].(map[string]any)["id"].(string)

	// Door 1: the subtickets endpoint on a child.
	status, body = e.doJSON(c, "POST", "/api/v1/tickets/"+childID+"/subtickets",
		`{"titles":["grandchild"]}`)
	if status != http.StatusConflict || body["type"] != "subticket_depth" {
		t.Fatalf("subtickets on a sub-ticket = %d %v, want 409 subticket_depth", status, body["type"])
	}

	// Door 2: creating a ticket with a sub-ticket as parent.
	status, body = e.doJSON(c, "POST", "/api/v1/projects/SUB/tickets",
		fmt.Sprintf(`{"title":"grandchild","parent_id":%q}`, childID))
	if status != http.StatusConflict || body["type"] != "subticket_depth" {
		t.Fatalf("create under a sub-ticket = %d %v, want 409 subticket_depth", status, body["type"])
	}

	// Door 3: a ticket that has children cannot itself become a child.
	other := e.createTicket(c, "SUB", "other root", "")
	status, body = e.doJSON(c, "PATCH", "/api/v1/tickets/"+parentID,
		fmt.Sprintf(`{"parent_id":%q}`, other["id"].(string)))
	if status != http.StatusConflict || body["type"] != "subticket_depth" {
		t.Fatalf("reparenting a parent = %d %v, want 409 subticket_depth", status, body["type"])
	}
}

// TestArchiveIsReversible is D-15: archive hides the ticket from the default list, keeps it
// reachable under ?archived=1, unarchive restores it, and both transitions are in the audit
// log and the ticket stream.
func TestArchiveIsReversible(t *testing.T) {
	e := newEnv(t)
	c, _ := e.owner()
	e.createProject(c, "ARC")
	tk := e.createTicket(c, "ARC", "to archive", "")
	id := tk["id"].(string)

	// A wrong active-run confirmation is refused with the typed problem naming the count.
	status, body := e.doJSON(c, "DELETE", "/api/v1/tickets/"+id+"?confirm_active_runs=3", "")
	if status != http.StatusConflict || body["type"] != "active_runs_confirmation" {
		t.Fatalf("bad confirm = %d %v, want 409 active_runs_confirmation", status, body["type"])
	}

	status, _ = e.doJSON(c, "DELETE", "/api/v1/tickets/"+id, "")
	if status != http.StatusNoContent {
		t.Fatalf("archive = %d, want 204", status)
	}
	if got := len(e.listTickets(c, "ARC", false)); got != 0 {
		t.Fatalf("default list shows %d tickets after archive, want 0", got)
	}
	all := e.listTickets(c, "ARC", true)
	if len(all) != 1 || all[0]["archived_at"] == nil {
		t.Fatalf("archived list = %v, want the archived ticket with archived_at set", all)
	}

	// Mutations on an archived ticket are refused.
	status, body = e.doJSON(c, "PATCH", "/api/v1/tickets/"+id, `{"title":"renamed"}`)
	if status != http.StatusConflict || body["type"] != "ticket_archived" {
		t.Fatalf("patch archived = %d %v, want 409 ticket_archived", status, body["type"])
	}

	status, body = e.doJSON(c, "POST", "/api/v1/tickets/"+id+"/unarchive", "")
	if status != http.StatusOK || body["archived_at"] != nil {
		t.Fatalf("unarchive = %d %v, want 200 with archived_at null", status, body["archived_at"])
	}
	if got := len(e.listTickets(c, "ARC", false)); got != 1 {
		t.Fatalf("default list shows %d tickets after unarchive, want 1", got)
	}

	actions := e.auditActions(id)
	if !contains(actions, "ticket.archive") || !contains(actions, "ticket.unarchive") {
		t.Fatalf("audit actions for ticket = %v, want both ticket.archive and ticket.unarchive", actions)
	}

	var events []string
	for _, entry := range e.stream(c, id) {
		payload, _ := entry["payload"].(map[string]any)
		if ev, ok := payload["event"].(string); ok {
			events = append(events, ev)
		}
	}
	if !contains(events, "archived") || !contains(events, "unarchived") {
		t.Fatalf("stream events = %v, want archived and unarchived", events)
	}
}

// TestEveryMutationHitsTheStreamWithTheActor is the S10 acceptance "every mutation appears in
// the ticket stream with the right actor": create, move, assign, label and criterion writes
// all land as stream rows attributed to the acting human.
func TestEveryMutationHitsTheStreamWithTheActor(t *testing.T) {
	e := newEnv(t)
	c, ownerID := e.owner()
	cols := e.createProject(c, "STR")
	tk := e.createTicket(c, "STR", "streamed", "")
	id := tk["id"].(string)

	ready := columnByCategory(cols, "ready")
	if ready == nil {
		t.Fatal("no ready-category column in the default set")
	}
	status, body := e.doJSON(c, "POST", "/api/v1/tickets/"+id+"/move",
		fmt.Sprintf(`{"column_id":%q}`, ready["id"].(string)))
	if status != http.StatusOK {
		t.Fatalf("move = %d: %v", status, body)
	}
	status, body = e.doJSON(c, "PATCH", "/api/v1/tickets/"+id,
		fmt.Sprintf(`{"assignee_id":%q}`, ownerID))
	if status != http.StatusOK {
		t.Fatalf("assign = %d: %v", status, body)
	}
	status, label := e.doJSON(c, "POST", "/api/v1/projects/STR/labels",
		`{"name":"bug","color":"#cc0000"}`)
	if status != http.StatusCreated {
		t.Fatalf("create label = %d: %v", status, label)
	}
	status, body = e.doJSON(c, "PUT",
		"/api/v1/tickets/"+id+"/labels/"+label["id"].(string), "")
	if status != http.StatusNoContent {
		t.Fatalf("attach label = %d: %v", status, body)
	}
	status, crit := e.doJSON(c, "POST", "/api/v1/tickets/"+id+"/criteria",
		`{"text":"it works"}`)
	if status != http.StatusCreated {
		t.Fatalf("add criterion = %d: %v", status, crit)
	}
	status, body = e.doJSON(c, "PATCH", "/api/v1/criteria/"+crit["id"].(string),
		`{"checked":true}`)
	if status != http.StatusOK {
		t.Fatalf("check criterion = %d: %v", status, body)
	}
	if body["checked_by_user_id"] != ownerID {
		t.Fatalf("checked_by_user_id = %v, want the acting human %s", body["checked_by_user_id"], ownerID)
	}

	want := []string{"created", "moved", "assigned", "label_added", "criterion_added", "criterion_checked"}
	got := map[string]map[string]any{}
	for _, entry := range e.stream(c, id) {
		payload, _ := entry["payload"].(map[string]any)
		if ev, ok := payload["event"].(string); ok {
			got[ev] = entry
		}
	}
	for _, ev := range want {
		entry, ok := got[ev]
		if !ok {
			t.Fatalf("stream is missing event %q (have %v)", ev, keysOf(got))
		}
		if entry["actor_kind"] != "human" || entry["actor_id"] != ownerID {
			t.Fatalf("event %q actor = %v/%v, want human/%s",
				ev, entry["actor_kind"], entry["actor_id"], ownerID)
		}
	}
}

func keysOf(m map[string]map[string]any) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// TestMoveWritesFractionalPositions: dropping a ticket between two neighbours writes the true
// float midpoint; when midpoints are exhausted the whole column is renormalised to gap-spaced
// positions in the same move.
func TestMoveWritesFractionalPositions(t *testing.T) {
	e := newEnv(t)
	c, _ := e.owner()
	e.createProject(c, "POS")
	a := e.createTicket(c, "POS", "a", "")
	b := e.createTicket(c, "POS", "b", "")
	x := e.createTicket(c, "POS", "x", "")
	colID := a["column_id"].(string)

	// Appends land gap-spaced.
	if a["position"].(float64) != 1024 || b["position"].(float64) != 2048 || x["position"].(float64) != 3072 {
		t.Fatalf("append positions = %v %v %v, want 1024 2048 3072",
			a["position"], b["position"], x["position"])
	}

	// Drop x between a and b: the exact midpoint.
	status, body := e.doJSON(c, "POST", "/api/v1/tickets/"+x["id"].(string)+"/move",
		fmt.Sprintf(`{"column_id":%q,"after_ticket_id":%q}`, colID, a["id"].(string)))
	if status != http.StatusOK {
		t.Fatalf("move = %d: %v", status, body)
	}
	if got := body["position"].(float64); got != 1536 {
		t.Fatalf("midpoint position = %v, want 1536", got)
	}

	// Exhaust the gap: b sits one float ulp above a, so no midpoint exists between them —
	// the move must renormalise the column instead of writing a colliding position.
	ctx := context.Background()
	next := math.Nextafter(1024, math.Inf(1))
	if err := e.st.Tickets().SetPosition(ctx, a["id"].(string), 1024, "2026-01-01T00:00:00.000Z"); err != nil {
		t.Fatal(err)
	}
	if err := e.st.Tickets().SetPosition(ctx, b["id"].(string), next, "2026-01-01T00:00:00.000Z"); err != nil {
		t.Fatal(err)
	}
	if err := e.st.Tickets().SetPosition(ctx, x["id"].(string), 4096, "2026-01-01T00:00:00.000Z"); err != nil {
		t.Fatal(err)
	}
	status, body = e.doJSON(c, "POST", "/api/v1/tickets/"+x["id"].(string)+"/move",
		fmt.Sprintf(`{"column_id":%q,"after_ticket_id":%q}`, colID, a["id"].(string)))
	if status != http.StatusOK {
		t.Fatalf("renormalising move = %d: %v", status, body)
	}
	list := e.listTickets(c, "POS", false)
	if len(list) != 3 {
		t.Fatalf("list = %d tickets, want 3", len(list))
	}
	order := []string{list[0]["title"].(string), list[1]["title"].(string), list[2]["title"].(string)}
	if order[0] != "a" || order[1] != "x" || order[2] != "b" {
		t.Fatalf("order after renormalisation = %v, want a x b", order)
	}
	for i, want := range []float64{1024, 2048, 3072} {
		if got := list[i]["position"].(float64); got != want {
			t.Fatalf("renormalised position[%d] = %v, want %v", i, got, want)
		}
	}
}

// TestAutoStartColumnAuditsAndStartsNothing is brief D3 + the S10 seam: moving a delegated
// ticket into an auto_start_delegate column writes the request through the scheduler seam and
// audits the attempt — and, until S22, no run of any kind exists afterwards.
func TestAutoStartColumnAuditsAndStartsNothing(t *testing.T) {
	e := newEnv(t)
	c, _ := e.owner()
	cols := e.createProject(c, "AUTO")
	ctx := context.Background()

	p, err := e.st.Projects().ByKey(ctx, "AUTO")
	if err != nil {
		t.Fatal(err)
	}
	agentID := e.addAgent(p.ID, "dev")

	running := columnByCategory(cols, "running")
	if running == nil {
		t.Fatal("no running-category column in the default set")
	}
	col, err := e.st.Columns().ByID(ctx, running["id"].(string))
	if err != nil {
		t.Fatal(err)
	}
	col.AutoStartDelegate = true
	if err := e.st.Columns().Update(ctx, &col); err != nil {
		t.Fatal(err)
	}

	tk := e.createTicket(c, "AUTO", "delegated work",
		fmt.Sprintf(`,"delegate_agent_id":%q`, agentID))
	id := tk["id"].(string)

	status, body := e.doJSON(c, "POST", "/api/v1/tickets/"+id+"/move",
		fmt.Sprintf(`{"column_id":%q}`, col.ID))
	if status != http.StatusOK {
		t.Fatalf("move = %d: %v", status, body)
	}

	// The intent went through the seam exactly once…
	if got := e.rec.requestCount(); got != 1 {
		t.Fatalf("scheduler seam saw %d requests, want 1", got)
	}
	// …the attempt is audited…
	actions := e.auditActions(id)
	if !contains(actions, "ticket.autostart_delegate") {
		t.Fatalf("audit actions = %v, want ticket.autostart_delegate", actions)
	}
	// …and no run exists anywhere.
	runs, err := e.st.Runs().ForProject(ctx, p.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(runs) != 0 {
		t.Fatalf("runs table holds %d rows after a move, want 0 (a move never starts a run)", len(runs))
	}

	// A plain move into a non-auto column never touches the seam.
	review := columnByCategory(cols, "review")
	status, _ = e.doJSON(c, "POST", "/api/v1/tickets/"+id+"/move",
		fmt.Sprintf(`{"column_id":%q}`, review["id"].(string)))
	if status != http.StatusOK {
		t.Fatalf("second move = %d", status)
	}
	if got := e.rec.requestCount(); got != 1 {
		t.Fatalf("scheduler seam saw %d requests after a plain move, want still 1", got)
	}
}
