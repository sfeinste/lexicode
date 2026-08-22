package mcp_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/spruce/lexicode/internal/domain"
	"github.com/spruce/lexicode/internal/kernel/audit"
	"github.com/spruce/lexicode/internal/kernel/auth"
	"github.com/spruce/lexicode/internal/kernel/bus"
	"github.com/spruce/lexicode/internal/kernel/httpx"
	"github.com/spruce/lexicode/internal/kernel/store"
	"github.com/spruce/lexicode/internal/service/agents"
	"github.com/spruce/lexicode/internal/service/mcp"
)

// stateRecorder is the injected RunStateSetter: what the S22 scheduler will own, recorded.
type stateRecorder struct {
	mu     sync.Mutex
	states []string // "<runID>:<state>"
}

func (r *stateRecorder) set(_ context.Context, runID string, state domain.RunState, _ string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.states = append(r.states, runID+":"+string(state))
	return nil
}

func (r *stateRecorder) list() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.states...)
}

type env struct {
	t      *testing.T
	st     *store.Store
	mcp    *mcp.Server
	states *stateRecorder
	// reviews records what submit_review handed the forge seam (S39).
	reviews *reviewRecorder
	srv     *httptest.Server
	owner   *http.Client
	userID  string
}

func newEnv(t *testing.T) *env { return newEnvWithCeiling(t, 30*time.Second) }

// newEnvWithCeiling wires store + auth + audit + bus + the MCP server and its routes,
// exactly as cmd/lexicode serves them, with an injected state recorder and wait ceiling.
func newEnvWithCeiling(t *testing.T, ceiling time.Duration) *env {
	t.Helper()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	st, err := store.Open(store.Options{Path: filepath.Join(t.TempDir(), "s21.db"), Logger: logger})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	if _, err := st.Migrate(context.Background()); err != nil {
		t.Fatal(err)
	}

	mux := httpx.NewMux(httpx.Options{Logger: logger})
	authSvc := auth.New(auth.Options{Store: st, Logger: logger})
	authSvc.Routes(mux)
	auditW := audit.New(audit.Options{Store: st, Logger: logger})
	b := bus.New(bus.Options{Store: st, Logger: logger})
	if err := b.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = b.Stop(context.Background()) })

	rec := &stateRecorder{}
	reviews := &reviewRecorder{}
	mcpSvc := mcp.New(mcp.Options{
		Store: st, Bus: b, Audit: auditW, Logger: logger,
		SetRunState:  rec.set,
		SubmitReview: reviews.submit,
		WaitCeiling:  ceiling,
	})
	mcpSvc.Routes(mux, authSvc)
	mux.Handle("/mcp/{token}", mcpSvc.Handler())
	agents.New(agents.Options{Store: st, Audit: auditW, Logger: logger}).Routes(mux, authSvc)

	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	e := &env{t: t, st: st, mcp: mcpSvc, states: rec, reviews: reviews, srv: srv}
	e.owner = e.setupOwner()
	return e
}

func (e *env) setupOwner() *http.Client {
	e.t.Helper()
	jar, err := cookiejar.New(nil)
	if err != nil {
		e.t.Fatal(err)
	}
	c := &http.Client{Jar: jar}
	status, body := e.doJSON(c, "POST", "/api/v1/auth/setup",
		`{"email":"ada@example.com","display_name":"Ada","password":"correct horse"}`)
	if status != http.StatusCreated {
		e.t.Fatalf("setup = %d: %v", status, body)
	}
	e.userID = body["id"].(string)
	return c
}

// fixtures inserts a project + agent + running run directly through the store — S22's job
// once it exists; these rows are what the scheduler would have written.
type fixtures struct {
	project domain.Project
	agent   domain.Agent
	run     domain.Run
	token   string
}

func (e *env) fixtures(autonomy domain.Autonomy, perms domain.AgentPermissions) fixtures {
	e.t.Helper()
	ctx := context.Background()
	now := domain.Now()
	p := domain.Project{
		ID: domain.NewID(), Key: "PAY" + domain.NewID()[20:24], Name: "Payments",
		OwnerID: e.userID, CreatedAt: now, UpdatedAt: now,
	}
	if err := e.st.Projects().Create(ctx, &p); err != nil {
		e.t.Fatal(err)
	}
	a := domain.Agent{
		ID: domain.NewID(), ProjectID: p.ID, Name: "Dev", Role: "developer",
		Color: "#888888", RuntimeID: "scripted", Model: "claude-sonnet-5", Effort: "medium",
		Autonomy: autonomy, Permissions: perms,
		GitAuthorName: "Dev", GitAuthorEmail: "dev@agents.lexicode.local",
		ConcurrencyCap: 1, MaxWallClockSeconds: 3600, MaxSteps: 100, Enabled: true,
		CreatedAt: now, UpdatedAt: now,
	}
	if err := e.st.Agents().Create(ctx, &a); err != nil {
		e.t.Fatal(err)
	}
	r := domain.Run{
		ID: domain.NewID(), Seq: 1, ProjectID: p.ID, AgentID: a.ID,
		State: domain.RunRunning, Autonomy: autonomy,
		Model: a.Model, Effort: a.Effort, Prompt: "prompt",
		RuntimeID: "scripted", SandboxID: "fake", QueuedAt: now,
	}
	if err := e.st.Runs().Create(ctx, &r); err != nil {
		e.t.Fatal(err)
	}
	token, err := e.mcp.MintToken(r.ID)
	if err != nil {
		e.t.Fatal(err)
	}
	return fixtures{project: p, agent: a, run: r, token: token}
}

// ticket inserts a column + ticket (+ criterion) for the check_criterion tests.
func (e *env) ticket(f *fixtures, couple bool) (domain.Ticket, domain.Criterion) {
	e.t.Helper()
	ctx := context.Background()
	now := domain.Now()
	col := domain.Column{
		ID: domain.NewID(), ProjectID: f.project.ID, Name: "Dev",
		Category: domain.CategoryRunning, Position: 1, CreatedAt: now, UpdatedAt: now,
	}
	if err := e.st.Columns().Create(ctx, &col); err != nil {
		e.t.Fatal(err)
	}
	tk := domain.Ticket{
		ID: domain.NewID(), ProjectID: f.project.ID, Seq: 1, Key: f.project.Key + "-1",
		Title: "Add idempotency keys", ColumnID: col.ID, Position: 1,
		Priority: domain.PriorityNone, Origin: domain.OriginHuman,
		CreatedAt: now, UpdatedAt: now,
	}
	if err := e.st.Tickets().Create(ctx, &tk); err != nil {
		e.t.Fatal(err)
	}
	c := domain.Criterion{
		ID: domain.NewID(), TicketID: tk.ID, Position: 1024,
		Text: "charges are idempotent", UpdatedAt: now,
	}
	if err := e.st.Criteria().Create(ctx, &c); err != nil {
		e.t.Fatal(err)
	}
	if couple {
		// Couple the run to the ticket, as the scheduler would have at launch.
		f.run.TicketID = &tk.ID
		if _, err := e.st.Writer().ExecContext(ctx,
			`UPDATE runs SET ticket_id = ? WHERE id = ?`, tk.ID, f.run.ID); err != nil {
			e.t.Fatal(err)
		}
	}
	return tk, c
}

// rpc posts one JSON-RPC message to the MCP endpoint and decodes the response envelope.
func (e *env) rpc(token, method string, params any) (int, map[string]any) {
	e.t.Helper()
	msg := map[string]any{"jsonrpc": "2.0", "id": 1, "method": method}
	if params != nil {
		msg["params"] = params
	}
	raw, _ := json.Marshal(msg)
	resp, err := http.Post(e.srv.URL+"/mcp/"+token, "application/json", bytes.NewReader(raw))
	if err != nil {
		e.t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, _ := io.ReadAll(resp.Body)
	var v map[string]any
	if len(bytes.TrimSpace(body)) > 0 {
		_ = json.Unmarshal(body, &v)
	}
	return resp.StatusCode, v
}

// callTool runs tools/call and returns (isError, decoded content text).
func (e *env) callTool(token, tool string, args any) (bool, map[string]any) {
	e.t.Helper()
	status, v := e.rpc(token, "tools/call", map[string]any{"name": tool, "arguments": args})
	if status != http.StatusOK {
		e.t.Fatalf("tools/call %s = HTTP %d: %v", tool, status, v)
	}
	result, ok := v["result"].(map[string]any)
	if !ok {
		e.t.Fatalf("tools/call %s: no result: %v", tool, v)
	}
	content := result["content"].([]any)[0].(map[string]any)["text"].(string)
	isErr, _ := result["isError"].(bool)
	var decoded map[string]any
	if err := json.Unmarshal([]byte(content), &decoded); err != nil {
		decoded = map[string]any{"text": content}
	}
	return isErr, decoded
}

func (e *env) doJSON(c *http.Client, method, path, body string) (int, map[string]any) {
	e.t.Helper()
	var rd io.Reader
	if body != "" {
		rd = strings.NewReader(body)
	}
	req, err := http.NewRequestWithContext(context.Background(), method, e.srv.URL+path, rd)
	if err != nil {
		e.t.Fatal(err)
	}
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.Do(req)
	if err != nil {
		e.t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	raw, _ := io.ReadAll(resp.Body)
	var v map[string]any
	if len(bytes.TrimSpace(raw)) > 0 {
		if err := json.Unmarshal(raw, &v); err != nil {
			e.t.Fatalf("%s %s: not JSON: %v\n%s", method, path, err, raw)
		}
	}
	return resp.StatusCode, v
}

// waitPending polls until the run has exactly one pending elicitation and returns it.
func (e *env) waitPending(runID string) domain.Elicitation {
	e.t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		pending, err := e.st.Elicitations().PendingForRun(context.Background(), runID)
		if err != nil {
			e.t.Fatal(err)
		}
		if len(pending) > 0 {
			return pending[len(pending)-1]
		}
		time.Sleep(5 * time.Millisecond)
	}
	e.t.Fatal("no pending elicitation appeared")
	return domain.Elicitation{}
}

const askArgsJSON = `{
  "questions": [{
    "question": "Which response format should the endpoint use?",
    "header": "Format",
    "options": [
      {"label": "JSON", "description": "application/json"},
      {"label": "XML", "description": "text/xml"}
    ],
    "multiSelect": false
  }]
}`

func askArgs(t *testing.T) map[string]any {
	t.Helper()
	var v map[string]any
	if err := json.Unmarshal([]byte(askArgsJSON), &v); err != nil {
		t.Fatal(err)
	}
	return v
}

// DoD 2: ask_human parks the run in needs_input (state recorder), responding resumes with
// the answer as the tool result, and the transcript's next activity shows the answer.
func TestAskHumanParksAndResumes(t *testing.T) {
	e := newEnv(t)
	f := e.fixtures(domain.AutonomyApproveEach, domain.AgentPermissions{ReadFiles: true})

	type outcome struct {
		isErr  bool
		result map[string]any
	}
	done := make(chan outcome, 1)
	go func() {
		isErr, result := e.callTool(f.token, "ask_human", askArgs(t))
		done <- outcome{isErr, result}
	}()

	el := e.waitPending(f.run.ID)
	if el.Kind != domain.ElicitationQuestion {
		t.Fatalf("kind = %s, want question", el.Kind)
	}
	if states := e.states.list(); len(states) == 0 ||
		states[len(states)-1] != f.run.ID+":needs_input" {
		t.Fatalf("run not parked in needs_input; states = %v", states)
	}

	status, body := e.doJSON(e.owner, "POST", "/api/v1/elicitations/"+el.ID+"/respond",
		`{"action":"answer","answers":{"Which response format should the endpoint use?":["JSON"]}}`)
	if status != http.StatusOK {
		t.Fatalf("respond = %d: %v", status, body)
	}

	out := <-done
	if out.isErr {
		t.Fatalf("ask_human returned an error result: %v", out.result)
	}
	answers, ok := out.result["answers"].(map[string]any)
	if !ok {
		t.Fatalf("tool result carries no answers: %v", out.result)
	}
	got := answers["Which response format should the endpoint use?"].([]any)
	if len(got) != 1 || got[0] != "JSON" {
		t.Fatalf("answer = %v, want [JSON]", got)
	}

	// The run resumed…
	states := e.states.list()
	if states[len(states)-1] != f.run.ID+":running" {
		t.Fatalf("run did not resume; states = %v", states)
	}
	// …and the answer is visible in the next activity after the question.
	acts, err := e.st.Activities().ForRun(context.Background(), f.run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(acts) < 2 {
		t.Fatalf("want question + answer activities, got %d", len(acts))
	}
	last := acts[len(acts)-1]
	if last.Type != domain.ActivityElicitation || !strings.Contains(last.Title, "Answered: JSON") {
		t.Fatalf("next activity = %q (%s), want an Answered: JSON elicitation row", last.Title, last.Type)
	}
	if acts[len(acts)-2].Level != 0 || !strings.Contains(acts[len(acts)-2].Title, "Question:") {
		t.Fatalf("question activity = %+v, want level-0 Question row", acts[len(acts)-2])
	}

	// The elicitation row records the responder.
	resolved, err := e.st.Elicitations().ByID(context.Background(), el.ID)
	if err != nil {
		t.Fatal(err)
	}
	if resolved.State != domain.ElicitationAnswered || resolved.RespondedBy == nil ||
		*resolved.RespondedBy != e.userID {
		t.Fatalf("elicitation = %+v, want answered by %s", resolved, e.userID)
	}

	// The run.elicitation frames were published (persist-then-dispatch: rows in events).
	var n int
	err = e.st.Reader().QueryRowContext(context.Background(),
		`SELECT COUNT(*) FROM events WHERE kind = 'run' AND activity_type = 'elicitation'
		 AND subject_id = ?`, f.run.ID).Scan(&n)
	if err != nil {
		t.Fatal(err)
	}
	if n == 0 {
		t.Fatal("no run.elicitation event was published")
	}
}

// DoD 3: a suggest-autonomy agent has every mutating approval denied with the verbatim
// message, and auto-allows nothing — a read-only request parks for a human.
func TestSuggestDeniesMutatingAndAutoAllowsNothing(t *testing.T) {
	e := newEnv(t)
	f := e.fixtures(domain.AutonomySuggest, domain.AgentPermissions{
		ReadFiles: true, EditFiles: true, RunCommands: true,
	})

	const wantMsg = "this agent is in Suggest mode; it plans, it does not act."
	for _, req := range []map[string]any{
		{"tool_name": "Bash", "input": map[string]any{"command": "npm install"}},
		{"tool_name": "Edit", "input": map[string]any{"file_path": "/workspace/src/api.ts"}},
		{"tool_name": "Write", "input": map[string]any{"file_path": "/workspace/x.ts"}},
	} {
		isErr, result := e.callTool(f.token, "request_approval", req)
		if isErr {
			t.Fatalf("request_approval(%v) tool-errored: %v", req, result)
		}
		if result["behavior"] != "deny" || result["message"] != wantMsg {
			t.Fatalf("request_approval(%v) = %v, want deny with %q", req["tool_name"], result, wantMsg)
		}
	}

	// A read-only tool is NOT auto-allowed: it parks for a human.
	done := make(chan map[string]any, 1)
	go func() {
		_, result := e.callTool(f.token, "request_approval",
			map[string]any{"tool_name": "Read", "input": map[string]any{"file_path": "/workspace/README.md"}})
		done <- result
	}()
	el := e.waitPending(f.run.ID)
	if el.Kind != domain.ElicitationApproval {
		t.Fatalf("kind = %s, want approval", el.Kind)
	}
	status, _ := e.doJSON(e.owner, "POST", "/api/v1/elicitations/"+el.ID+"/respond",
		`{"action":"deny","message":"not now"}`)
	if status != http.StatusOK {
		t.Fatalf("respond = %d", status)
	}
	result := <-done
	if result["behavior"] != "deny" || result["message"] != "not now" {
		t.Fatalf("parked read-only approval = %v, want the human's deny", result)
	}
}

// DoD 4: an agent_permission_rules row short-circuits BEFORE autonomy — a suggest agent
// with an allow rule for the tool gets allow.
func TestPermissionRuleShortCircuitsBeforeAutonomy(t *testing.T) {
	e := newEnv(t)
	f := e.fixtures(domain.AutonomySuggest, domain.AgentPermissions{RunCommands: true})

	rule := domain.AgentPermissionRule{
		ID: domain.NewID(), AgentID: f.agent.ID, Tool: "Bash", Pattern: "npm test:*",
		Decision: domain.DecisionAllow, CreatedAt: domain.Now(),
	}
	if err := e.st.PermissionRules().Create(context.Background(), &rule); err != nil {
		t.Fatal(err)
	}

	isErr, result := e.callTool(f.token, "request_approval", map[string]any{
		"tool_name": "Bash", "input": map[string]any{"command": "npm test -- --grep charge"},
	})
	if isErr {
		t.Fatalf("tool-errored: %v", result)
	}
	if result["behavior"] != "allow" {
		t.Fatalf("suggest agent with an allow rule = %v, want allow", result)
	}
	if updated, ok := result["updatedInput"].(map[string]any); !ok ||
		updated["command"] != "npm test -- --grep charge" {
		t.Fatalf("updatedInput = %v, want the original input echoed", result["updatedInput"])
	}

	// The same agent without a matching rule stays denied (autonomy still applies).
	_, result = e.callTool(f.token, "request_approval", map[string]any{
		"tool_name": "Bash", "input": map[string]any{"command": "npm publish"},
	})
	if result["behavior"] != "deny" {
		t.Fatalf("non-matching command = %v, want the suggest deny", result)
	}
}

// DoD 5: approve-with-edits returns updatedInput; remember writes exactly one rule row,
// visible in the GET, and it short-circuits the next identical request.
func TestApproveWithEditsAndRemember(t *testing.T) {
	e := newEnv(t)
	f := e.fixtures(domain.AutonomyApproveEach, domain.AgentPermissions{RunCommands: true})

	// approve_with_edits.
	done := make(chan map[string]any, 1)
	go func() {
		_, result := e.callTool(f.token, "request_approval", map[string]any{
			"tool_name": "Bash", "input": map[string]any{"command": "npm publish"},
		})
		done <- result
	}()
	el := e.waitPending(f.run.ID)
	status, _ := e.doJSON(e.owner, "POST", "/api/v1/elicitations/"+el.ID+"/respond",
		`{"action":"approve_with_edits","updated_input":{"command":"npm publish --dry-run"}}`)
	if status != http.StatusOK {
		t.Fatalf("respond = %d", status)
	}
	result := <-done
	if result["behavior"] != "allow" {
		t.Fatalf("approve_with_edits = %v, want allow", result)
	}
	if updated := result["updatedInput"].(map[string]any); updated["command"] != "npm publish --dry-run" {
		t.Fatalf("updatedInput = %v, want the edited command", updated)
	}

	// remember: exactly one rule row, allow returned, visible in the GET.
	go func() {
		_, result := e.callTool(f.token, "request_approval", map[string]any{
			"tool_name": "Bash", "input": map[string]any{"command": "npm test"},
		})
		done <- result
	}()
	el = e.waitPending(f.run.ID)
	status, respBody := e.doJSON(e.owner, "POST", "/api/v1/elicitations/"+el.ID+"/respond",
		`{"action":"remember"}`)
	if status != http.StatusOK {
		t.Fatalf("remember respond = %d: %v", status, respBody)
	}
	if respBody["rule_id"] == nil || respBody["rule_id"] == "" {
		t.Fatalf("remember returned no rule_id: %v", respBody)
	}
	result = <-done
	if result["behavior"] != "allow" {
		t.Fatalf("remember = %v, want allow", result)
	}

	rules, err := e.st.PermissionRules().ForAgent(context.Background(), f.agent.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(rules) != 1 {
		t.Fatalf("remember wrote %d rules, want exactly 1: %+v", len(rules), rules)
	}
	if rules[0].Tool != "Bash" || rules[0].Pattern != "npm test:*" ||
		rules[0].Decision != domain.DecisionAllow {
		t.Fatalf("rule = %+v, want Bash / npm test:* / allow", rules[0])
	}

	status, listBody := e.doJSON(e.owner, "GET", "/api/v1/agents/"+f.agent.ID+"/permission-rules", "")
	if status != http.StatusOK {
		t.Fatalf("GET permission-rules = %d", status)
	}
	listed := listBody["rules"].([]any)
	if len(listed) != 1 || listed[0].(map[string]any)["pattern"] != "npm test:*" {
		t.Fatalf("GET rules = %v, want the remembered rule", listBody)
	}

	// The remembered rule short-circuits the next identical request — no elicitation.
	isErr, result := e.callTool(f.token, "request_approval", map[string]any{
		"tool_name": "Bash", "input": map[string]any{"command": "npm test -- --watch=false"},
	})
	if isErr || result["behavior"] != "allow" {
		t.Fatalf("post-remember request = %v, want immediate allow", result)
	}
	if pending, _ := e.st.Elicitations().PendingForRun(context.Background(), f.run.ID); len(pending) != 0 {
		t.Fatalf("post-remember request parked anyway: %+v", pending)
	}
}

// DoD 6: a revoked or unknown token answers 404; two concurrent ask_human calls on one run
// both work.
func TestTokenRevocationAndConcurrentAsks(t *testing.T) {
	e := newEnv(t)
	f := e.fixtures(domain.AutonomyAuto, domain.AgentPermissions{ReadFiles: true})

	// Unknown token.
	resp, err := http.Post(e.srv.URL+"/mcp/deadbeef", "application/json",
		strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"ping"}`))
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("unknown token = %d, want 404", resp.StatusCode)
	}

	// Two concurrent asks with different questions: both park, both resume.
	ask := func(question string, out chan<- map[string]any) {
		args := askArgs(t)
		args["questions"].([]any)[0].(map[string]any)["question"] = question
		_, result := e.callTool(f.token, "ask_human", args)
		out <- result
	}
	out1 := make(chan map[string]any, 1)
	out2 := make(chan map[string]any, 1)
	go ask("First question?", out1)
	go ask("Second question?", out2)

	deadline := time.Now().Add(5 * time.Second)
	var pending []domain.Elicitation
	for time.Now().Before(deadline) {
		pending, _ = e.st.Elicitations().PendingForRun(context.Background(), f.run.ID)
		if len(pending) == 2 {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if len(pending) != 2 {
		t.Fatalf("want 2 concurrent pending elicitations, got %d", len(pending))
	}
	for _, el := range pending {
		status, _ := e.doJSON(e.owner, "POST", "/api/v1/elicitations/"+el.ID+"/respond",
			`{"action":"answer","text":"go ahead"}`)
		if status != http.StatusOK {
			t.Fatalf("respond = %d", status)
		}
	}
	for _, ch := range []chan map[string]any{out1, out2} {
		result := <-ch
		if result["response"] != "go ahead" {
			t.Fatalf("concurrent ask result = %v", result)
		}
	}

	// Revocation: the minted token stops working mid-flight.
	e.mcp.RevokeRun(f.run.ID)
	resp, err = http.Post(e.srv.URL+"/mcp/"+f.token, "application/json",
		strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"ping"}`))
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("revoked token = %d, want 404", resp.StatusCode)
	}
}

// DoD 8: re-asking the identical question while it is unanswered reuses the elicitation row,
// and both blocked calls get the one answer.
func TestIdempotentReAsk(t *testing.T) {
	e := newEnv(t)
	f := e.fixtures(domain.AutonomyApproveEach, domain.AgentPermissions{})

	out := make(chan map[string]any, 2)
	go func() { _, r := e.callTool(f.token, "ask_human", askArgs(t)); out <- r }()
	first := e.waitPending(f.run.ID)

	go func() { _, r := e.callTool(f.token, "ask_human", askArgs(t)); out <- r }()
	// Give the re-ask a moment to land, then assert it created nothing new.
	time.Sleep(100 * time.Millisecond)
	pending, err := e.st.Elicitations().PendingForRun(context.Background(), f.run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(pending) != 1 || pending[0].ID != first.ID {
		t.Fatalf("re-ask did not reuse the row: %+v", pending)
	}

	status, _ := e.doJSON(e.owner, "POST", "/api/v1/elicitations/"+first.ID+"/respond",
		`{"action":"answer","answers":{"Which response format should the endpoint use?":["XML"]}}`)
	if status != http.StatusOK {
		t.Fatalf("respond = %d", status)
	}
	for i := 0; i < 2; i++ {
		result := <-out
		answers, ok := result["answers"].(map[string]any)
		if !ok {
			t.Fatalf("call %d result = %v, want answers", i, result)
		}
		if got := answers["Which response format should the endpoint use?"].([]any); got[0] != "XML" {
			t.Fatalf("call %d answer = %v", i, got)
		}
	}
}

// auto autonomy: permissions grant → allow; permissions missing → deny without asking.
// auto_gates: destructive commands park, harmless ones auto-allow.
func TestAutoAndAutoGates(t *testing.T) {
	e := newEnv(t)
	auto := e.fixtures(domain.AutonomyAuto, domain.AgentPermissions{RunCommands: true})

	isErr, result := e.callTool(auto.token, "request_approval", map[string]any{
		"tool_name": "Bash", "input": map[string]any{"command": "npm test"},
	})
	if isErr || result["behavior"] != "allow" {
		t.Fatalf("auto + run_commands = %v, want allow", result)
	}
	_, result = e.callTool(auto.token, "request_approval", map[string]any{
		"tool_name": "Edit", "input": map[string]any{"file_path": "/workspace/x.ts"},
	})
	if result["behavior"] != "deny" {
		t.Fatalf("auto without edit_files = %v, want deny without asking", result)
	}
	if pending, _ := e.st.Elicitations().PendingForRun(context.Background(), auto.run.ID); len(pending) != 0 {
		t.Fatalf("auto denial parked anyway: %+v", pending)
	}

	gates := e.fixtures(domain.AutonomyAutoGates, domain.AgentPermissions{RunCommands: true})
	_, result = e.callTool(gates.token, "request_approval", map[string]any{
		"tool_name": "Bash", "input": map[string]any{"command": "go test ./..."},
	})
	if result["behavior"] != "allow" {
		t.Fatalf("auto_gates harmless command = %v, want allow", result)
	}

	// Destructive: parks with the six card fields present.
	done := make(chan map[string]any, 1)
	go func() {
		_, r := e.callTool(gates.token, "request_approval", map[string]any{
			"tool_name": "Bash", "input": map[string]any{"command": "rm -rf /workspace/dist && git push --force"},
		})
		done <- r
	}()
	el := e.waitPending(gates.run.ID)
	var card map[string]any
	if err := json.Unmarshal(el.Request, &card); err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{"action", "scope", "impact", "reason", "alternatives", "recovery"} {
		if v, ok := card[field].(string); !ok || v == "" {
			t.Fatalf("approval card lacks %q: %v", field, card)
		}
	}
	if !strings.Contains(card["impact"].(string), "destructive") {
		t.Fatalf("impact = %q, want the destructive heuristic named", card["impact"])
	}
	status, _ := e.doJSON(e.owner, "POST", "/api/v1/elicitations/"+el.ID+"/respond",
		`{"action":"approve"}`)
	if status != http.StatusOK {
		t.Fatalf("respond = %d", status)
	}
	if result := <-done; result["behavior"] != "allow" {
		t.Fatalf("approved destructive = %v", result)
	}
	if states := e.states.list(); !contains(states, gates.run.ID+":awaiting_approval") {
		t.Fatalf("run never entered awaiting_approval: %v", states)
	}
}

func contains(list []string, want string) bool {
	for _, s := range list {
		if s == want {
			return true
		}
	}
	return false
}
