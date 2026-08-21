package tickets_test

import (
	"fmt"
	"net/http"
	"testing"
)

// TestCriteriaOrderedChecklist: criteria append in order, reorder via after_id (null = top),
// uncheck clears attribution, and delete removes the row — with stream rows for the history
// moments (add, check, uncheck, remove) but not for reorders.
func TestCriteriaOrderedChecklist(t *testing.T) {
	e := newEnv(t)
	c, _ := e.owner()
	e.createProject(c, "CRIT")
	tk := e.createTicket(c, "CRIT", "with criteria", "")
	id := tk["id"].(string)

	ids := make([]string, 3)
	for i, text := range []string{"first", "second", "third"} {
		status, body := e.doJSON(c, "POST", "/api/v1/tickets/"+id+"/criteria",
			fmt.Sprintf(`{"text":%q}`, text))
		if status != http.StatusCreated {
			t.Fatalf("add criterion = %d: %v", status, body)
		}
		ids[i] = body["id"].(string)
	}

	// Reorder: third to the top.
	status, body := e.doJSON(c, "PATCH", "/api/v1/criteria/"+ids[2], `{"after_id":null}`)
	if status != http.StatusOK {
		t.Fatalf("reorder = %d: %v", status, body)
	}
	status, detail := e.doJSON(c, "GET", "/api/v1/tickets/"+id, "")
	if status != http.StatusOK {
		t.Fatalf("get = %d", status)
	}
	crit := detail["criteria"].([]any)
	order := []string{
		crit[0].(map[string]any)["text"].(string),
		crit[1].(map[string]any)["text"].(string),
		crit[2].(map[string]any)["text"].(string),
	}
	if order[0] != "third" || order[1] != "first" || order[2] != "second" {
		t.Fatalf("checklist order = %v, want third first second", order)
	}

	// Check then uncheck: attribution appears, then clears.
	status, body = e.doJSON(c, "PATCH", "/api/v1/criteria/"+ids[0], `{"checked":true}`)
	if status != http.StatusOK || body["checked"] != true || body["checked_by_user_id"] == nil {
		t.Fatalf("check = %d %v", status, body)
	}
	status, body = e.doJSON(c, "PATCH", "/api/v1/criteria/"+ids[0], `{"checked":false}`)
	if status != http.StatusOK || body["checked"] != false || body["checked_by_user_id"] != nil {
		t.Fatalf("uncheck = %d %v, want checked false with no attribution", status, body)
	}

	status, _ = e.doJSON(c, "DELETE", "/api/v1/criteria/"+ids[1], "")
	if status != http.StatusNoContent {
		t.Fatalf("delete criterion = %d, want 204", status)
	}
	status, detail = e.doJSON(c, "GET", "/api/v1/tickets/"+id, "")
	if status != http.StatusOK || len(detail["criteria"].([]any)) != 2 {
		t.Fatalf("after delete: %d criteria, want 2", len(detail["criteria"].([]any)))
	}

	var events []string
	for _, entry := range e.stream(c, id) {
		payload, _ := entry["payload"].(map[string]any)
		if ev, ok := payload["event"].(string); ok {
			events = append(events, ev)
		}
	}
	for _, want := range []string{"criterion_added", "criterion_checked", "criterion_unchecked", "criterion_removed"} {
		if !contains(events, want) {
			t.Fatalf("stream events = %v, missing %q", events, want)
		}
	}
}

// TestLabelsProjectScoped: label CRUD with colours, duplicate names refused per project,
// attach/detach idempotent, and deleting a label detaches it from every ticket.
func TestLabelsProjectScoped(t *testing.T) {
	e := newEnv(t)
	c, _ := e.owner()
	e.createProject(c, "LAB")
	tk := e.createTicket(c, "LAB", "wearing labels", "")
	id := tk["id"].(string)

	status, label := e.doJSON(c, "POST", "/api/v1/projects/LAB/labels",
		`{"name":"bug","color":"#cc0000"}`)
	if status != http.StatusCreated {
		t.Fatalf("create label = %d: %v", status, label)
	}
	labelID := label["id"].(string)

	status, body := e.doJSON(c, "POST", "/api/v1/projects/LAB/labels",
		`{"name":"bug","color":"#00cc00"}`)
	if status != http.StatusBadRequest {
		t.Fatalf("duplicate label name = %d, want 400: %v", status, body)
	}
	status, body = e.doJSON(c, "POST", "/api/v1/projects/LAB/labels",
		`{"name":"ugly","color":"red"}`)
	if status != http.StatusBadRequest {
		t.Fatalf("bad color = %d, want 400: %v", status, body)
	}

	// Attach twice: idempotent, one stream row.
	for i := 0; i < 2; i++ {
		status, _ = e.doJSON(c, "PUT", "/api/v1/tickets/"+id+"/labels/"+labelID, "")
		if status != http.StatusNoContent {
			t.Fatalf("attach #%d = %d, want 204", i+1, status)
		}
	}
	added := 0
	for _, entry := range e.stream(c, id) {
		payload, _ := entry["payload"].(map[string]any)
		if payload["event"] == "label_added" {
			added++
		}
	}
	if added != 1 {
		t.Fatalf("stream has %d label_added rows after double attach, want 1", added)
	}

	status, body = e.doJSON(c, "PATCH", "/api/v1/labels/"+labelID, `{"name":"defect"}`)
	if status != http.StatusOK || body["name"] != "defect" {
		t.Fatalf("rename label = %d %v", status, body)
	}

	// Delete the label: it disappears from the ticket too.
	status, _ = e.doJSON(c, "DELETE", "/api/v1/labels/"+labelID, "")
	if status != http.StatusNoContent {
		t.Fatalf("delete label = %d, want 204", status)
	}
	status, detail := e.doJSON(c, "GET", "/api/v1/tickets/"+id, "")
	if status != http.StatusOK || len(detail["labels"].([]any)) != 0 {
		t.Fatalf("ticket still wears %d labels after label delete, want 0", len(detail["labels"].([]any)))
	}
	status, body = e.doJSON(c, "GET", "/api/v1/projects/LAB/labels", "")
	if status != http.StatusOK || len(body["labels"].([]any)) != 0 {
		t.Fatalf("project still lists %d labels, want 0", len(body["labels"].([]any)))
	}
}
