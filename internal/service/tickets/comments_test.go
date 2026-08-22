package tickets_test

import (
	"context"
	"fmt"
	"net/http"
	"testing"
)

// TestCommentWritesStreamAndMentions is the S12 backend acceptance: one POST to the stream
// writes the comment row (kind='comment', actor attribution, markdown body) and the body's
// mention tokens as mentions rows, in one transaction; mentioning an agent audits a run
// request through the scheduler seam while the runs table stays empty.
func TestCommentWritesStreamAndMentions(t *testing.T) {
	e := newEnv(t)
	c, ownerID := e.owner()
	e.createProject(c, "COM")
	tk := e.createTicket(c, "COM", "the ticket", "")
	tkID := tk["id"].(string)
	other := e.createTicket(c, "COM", "the other ticket", "")

	status, project := e.doJSON(c, "GET", "/api/v1/projects/COM", "")
	if status != http.StatusOK {
		t.Fatalf("get project = %d, want 200", status)
	}
	agentID := e.addAgent(project["id"].(string), "dev")

	body := fmt.Sprintf(
		"Please look at this, @[Ada](user:%s) — and @[dev](agent:%s) take a pass.\n\n"+
			"Related to @[COM-2](ticket:%s).",
		ownerID, agentID, other["id"].(string))
	status, resp := e.doJSON(c, "POST", "/api/v1/tickets/"+tkID+"/stream",
		fmt.Sprintf(`{"body":%q}`, body))
	if status != http.StatusCreated {
		t.Fatalf("comment = %d, want 201: %v", status, resp)
	}
	entry := resp["entry"].(map[string]any)
	if entry["kind"] != "comment" || entry["body"] != body {
		t.Fatalf("entry kind/body wrong: %v", entry)
	}
	if entry["actor_kind"] != "human" || entry["actor_id"] != ownerID {
		t.Fatalf("comment not attributed to the acting human: %v", entry)
	}

	// The stream now interleaves the system "created" row and the comment, chronologically.
	stream := e.stream(c, tkID)
	if len(stream) != 2 || stream[0]["kind"] != "field_change" || stream[1]["kind"] != "comment" {
		t.Fatalf("stream = %v, want [created, comment]", stream)
	}

	// The mentions rows exist, sourced from the comment, with the containing paragraph.
	ms, err := e.st.Mentions().ForSource(context.Background(), "comment", entry["id"].(string))
	if err != nil {
		t.Fatal(err)
	}
	if len(ms) != 3 {
		t.Fatalf("mentions = %d rows, want 3 (user, agent, ticket): %+v", len(ms), ms)
	}
	kinds := map[string]string{}
	for _, m := range ms {
		kinds[m.ToKind] = m.ToID
		if m.ContextText == "" || !m.Linked {
			t.Fatalf("mention missing context or linked flag: %+v", m)
		}
	}
	if kinds["user"] != ownerID || kinds["agent"] != agentID || kinds["ticket"] != other["id"].(string) {
		t.Fatalf("mention targets wrong: %v", kinds)
	}

	// The agent mention went through the scheduler seam (reason "@mention"), was audited,
	// and — the seam being Unscheduled until S22 — the response says so honestly and the
	// runs table is still empty.
	if e.rec.requestCount() != 1 {
		t.Fatalf("scheduler requests = %d, want 1", e.rec.requestCount())
	}
	e.rec.mu.Lock()
	req := e.rec.requests[0]
	e.rec.mu.Unlock()
	if req.AgentID != agentID || req.TicketID != tkID || req.Reason != "@mention" {
		t.Fatalf("run request = %+v, want agent/ticket/@mention", req)
	}
	rrs := resp["run_requests"].([]any)
	if len(rrs) != 1 {
		t.Fatalf("run_requests = %v, want exactly one", rrs)
	}
	rr := rrs[0].(map[string]any)
	if rr["staged"] != false || rr["agent_name"] != "dev" || rr["note"] == "" {
		t.Fatalf("run request outcome not honest: %v", rr)
	}
	runs, err := e.st.Runs().ForTicket(context.Background(), tkID)
	if err != nil {
		t.Fatal(err)
	}
	if len(runs) != 0 {
		t.Fatalf("runs table has %d rows, want 0 before S22", len(runs))
	}
	actions := e.auditActions(tkID)
	if !contains(actions, "ticket.comment") || !contains(actions, "ticket.mention_run") {
		t.Fatalf("audit actions = %v, want ticket.comment and ticket.mention_run", actions)
	}
}

// TestCommentValidation: an empty body is a 400, a comment on an archived ticket a 409, and
// a mention token whose target does not exist is dropped without failing the comment.
func TestCommentValidation(t *testing.T) {
	e := newEnv(t)
	c, _ := e.owner()
	e.createProject(c, "CV")
	tk := e.createTicket(c, "CV", "t", "")
	id := tk["id"].(string)

	status, body := e.doJSON(c, "POST", "/api/v1/tickets/"+id+"/stream", `{"body":"  "}`)
	if status != http.StatusBadRequest {
		t.Fatalf("empty comment = %d, want 400: %v", status, body)
	}

	status, resp := e.doJSON(c, "POST", "/api/v1/tickets/"+id+"/stream",
		`{"body":"ghost @[nobody](user:01HZZZZZZZZZZZZZZZZZZZZZZZ) here"}`)
	if status != http.StatusCreated {
		t.Fatalf("comment with dangling mention = %d, want 201: %v", status, resp)
	}
	entry := resp["entry"].(map[string]any)
	ms, err := e.st.Mentions().ForSource(context.Background(), "comment", entry["id"].(string))
	if err != nil {
		t.Fatal(err)
	}
	if len(ms) != 0 {
		t.Fatalf("dangling mention wrote %d rows, want 0", len(ms))
	}

	status, _ = e.doJSON(c, "DELETE", "/api/v1/tickets/"+id+"?confirm_active_runs=0", "")
	if status != http.StatusNoContent {
		t.Fatalf("archive = %d, want 204", status)
	}
	status, body = e.doJSON(c, "POST", "/api/v1/tickets/"+id+"/stream", `{"body":"too late"}`)
	if status != http.StatusConflict || body["type"] != "ticket_archived" {
		t.Fatalf("comment on archived = %d %v, want 409 ticket_archived", status, body["type"])
	}
}

// TestDescriptionMentionsFollowEdits: description `@` tokens produce mentions rows sourced
// from the ticket, an edit re-derives them, and removing every token clears them. A
// description mention of an agent does NOT stage a run — only comment mentions do.
func TestDescriptionMentionsFollowEdits(t *testing.T) {
	e := newEnv(t)
	c, ownerID := e.owner()
	e.createProject(c, "DM")

	status, project := e.doJSON(c, "GET", "/api/v1/projects/DM", "")
	if status != http.StatusOK {
		t.Fatalf("get project = %d", status)
	}
	agentID := e.addAgent(project["id"].(string), "reviewer")

	tk := e.createTicket(c, "DM", "with mentions",
		fmt.Sprintf(`,"description":"Owned by @[Ada](user:%s)."`, ownerID))
	id := tk["id"].(string)

	ms, err := e.st.Mentions().ForSource(context.Background(), "ticket", id)
	if err != nil {
		t.Fatal(err)
	}
	if len(ms) != 1 || ms[0].ToKind != "user" || ms[0].ToID != ownerID {
		t.Fatalf("create-description mentions = %+v, want one user row", ms)
	}

	status, _ = e.doJSON(c, "PATCH", "/api/v1/tickets/"+id,
		fmt.Sprintf(`{"description":"Now for @[reviewer](agent:%s)."}`, agentID))
	if status != http.StatusOK {
		t.Fatalf("patch description = %d, want 200", status)
	}
	ms, err = e.st.Mentions().ForSource(context.Background(), "ticket", id)
	if err != nil {
		t.Fatal(err)
	}
	if len(ms) != 1 || ms[0].ToKind != "agent" || ms[0].ToID != agentID {
		t.Fatalf("edited-description mentions = %+v, want one agent row", ms)
	}
	if e.rec.requestCount() != 0 {
		t.Fatalf("description agent mention staged %d runs, want 0", e.rec.requestCount())
	}

	status, _ = e.doJSON(c, "PATCH", "/api/v1/tickets/"+id, `{"description":"No tokens left."}`)
	if status != http.StatusOK {
		t.Fatalf("clearing patch = %d, want 200", status)
	}
	ms, err = e.st.Mentions().ForSource(context.Background(), "ticket", id)
	if err != nil {
		t.Fatal(err)
	}
	if len(ms) != 0 {
		t.Fatalf("mentions after token removal = %+v, want none", ms)
	}
}

// TestUsersDirectory: GET /users returns every non-archived member's public display fields
// and nothing else (no email, no role) — the S12 assignee picker / mention source.
func TestUsersDirectory(t *testing.T) {
	e := newEnv(t)
	c, ownerID := e.owner()

	status, body := e.doJSON(c, "GET", "/api/v1/users", "")
	if status != http.StatusOK {
		t.Fatalf("list users = %d, want 200: %v", status, body)
	}
	users := body["users"].([]any)
	if len(users) != 1 {
		t.Fatalf("users = %d rows, want 1", len(users))
	}
	u := users[0].(map[string]any)
	if u["id"] != ownerID || u["display_name"] != "Ada" {
		t.Fatalf("user row wrong: %v", u)
	}
	if u["avatar_color"] == "" {
		t.Fatalf("avatar_color missing: %v", u)
	}
	if _, leaked := u["email"]; leaked {
		t.Fatalf("email must not cross the members wire: %v", u)
	}
	if _, leaked := u["role"]; leaked {
		t.Fatalf("role must not cross the members wire: %v", u)
	}
}
