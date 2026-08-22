package mcp_test

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/spruce/lexicode/internal/domain"
	"github.com/spruce/lexicode/internal/kernel/ports"
	"github.com/spruce/lexicode/internal/module/testkit"
)

// The claudecode Handle.Respond seam (S20) routes into mcp.Resolve: a session launched by
// the scripted runtime on the fake sandbox can deliver an answer to the blocked MCP call,
// exactly as the cmd wiring connects them.
func TestScriptedRuntimeRespondSeam(t *testing.T) {
	e := newEnv(t)
	f := e.fixtures(domain.AutonomyApproveEach, domain.AgentPermissions{})

	// A minimal stream-json session: the agent calls ask_human and ends.
	fixture := strings.Join([]string{
		`{"type":"system","subtype":"init","session_id":"s1","tools":[],"cwd":"/workspace"}`,
		`{"type":"assistant","message":{"content":[{"type":"text","text":"I need a decision."}]}}`,
		`{"type":"result","subtype":"success","result":"done","total_cost_usd":0.01}`,
	}, "\n") + "\n"

	rt := &testkit.Scripted{
		Fixture: []byte(fixture),
		Respond: func(ctx context.Context, runID, elicitationID string, r ports.Response) error {
			_, err := e.mcp.Resolve(ctx, elicitationID, r, nil)
			return err
		},
	}
	sb := testkit.NewSandbox(testkit.Script{})
	inst, err := sb.Prepare(context.Background(), ports.SandboxSpec{RunID: f.run.ID}, nopSink{})
	if err != nil {
		t.Fatal(err)
	}
	handle, err := rt.Launch(context.Background(), ports.RunSpec{
		RunID: f.run.ID, Prompt: "go", Model: "claude-sonnet-5",
	}, inst, recSink{})
	if err != nil {
		t.Fatal(err)
	}

	// The "container" side blocks on ask_human over HTTP while the session runs.
	done := make(chan map[string]any, 1)
	go func() {
		_, result := e.callTool(f.token, "ask_human", askArgs(t))
		done <- result
	}()
	el := e.waitPending(f.run.ID)

	// A human answers through the session handle — the S20 seam, wired as cmd/lexicode
	// wires it.
	err = handle.Respond(context.Background(), el.ID,
		ports.Response{Answers: map[string][]string{"Which response format should the endpoint use?": {"JSON"}}})
	if err != nil {
		t.Fatalf("Handle.Respond: %v", err)
	}
	result := <-done
	if answers, ok := result["answers"].(map[string]any); !ok ||
		answers["Which response format should the endpoint use?"].([]any)[0] != "JSON" {
		t.Fatalf("blocked call result = %v", result)
	}
	if _, err := handle.Wait(context.Background()); err != nil {
		t.Fatalf("Wait: %v", err)
	}
}

type nopSink struct{}

func (nopSink) Step(string, ports.StepState, string) {}
func (nopSink) Log(string)                           {}

type recSink struct{}

func (recSink) Activity(domain.Activity)        {}
func (recSink) CurrentStep(string)              {}
func (recSink) Usage(domain.UsageDelta)         {}
func (recSink) Elicit(domain.Elicitation) error { return nil }
func (recSink) Output(domain.RunOutput)         {}
func (recSink) Offset(int64)                    {}

func TestSetStepUpdatesRunAndEmits(t *testing.T) {
	e := newEnv(t)
	f := e.fixtures(domain.AutonomyAuto, domain.AgentPermissions{})

	isErr, result := e.callTool(f.token, "set_step", map[string]any{
		"step": "editing src/api/charge.ts", "index": 4, "total": 9,
	})
	if isErr {
		t.Fatalf("set_step errored: %v", result)
	}
	run, err := e.st.Runs().ByID(context.Background(), f.run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if run.CurrentStep != "editing src/api/charge.ts" {
		t.Fatalf("current_step = %q", run.CurrentStep)
	}
	var n int
	err = e.st.Reader().QueryRowContext(context.Background(),
		`SELECT COUNT(*) FROM events WHERE kind = 'run' AND activity_type = 'step'
		 AND subject_id = ?`, f.run.ID).Scan(&n)
	if err != nil || n == 0 {
		t.Fatalf("run.step event rows = %d (err %v), want ≥ 1", n, err)
	}
	acts, err := e.st.Activities().ForRun(context.Background(), f.run.ID)
	if err != nil || len(acts) == 0 {
		t.Fatalf("no set_step activity: %v", err)
	}
	if got := acts[len(acts)-1].Title; got != "Step 4/9: editing src/api/charge.ts" {
		t.Fatalf("activity title = %q", got)
	}
}

func TestProposeWikiPageStaysProposed(t *testing.T) {
	e := newEnv(t)
	f := e.fixtures(domain.AutonomyAuto, domain.AgentPermissions{CreateWikiPages: true})

	isErr, result := e.callTool(f.token, "propose_wiki_page", map[string]any{
		"title": "Database migrations", "slug": "database-migrations",
		"body": "Always use the migration tool.", "agent_scope": "auto",
		"reason": "You corrected me twice about migrations",
	})
	if isErr {
		t.Fatalf("propose errored: %v", result)
	}
	pageID := result["page_id"].(string)

	pages, err := e.st.Wiki().ForProject(context.Background(), f.project.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(pages) != 1 {
		t.Fatalf("pages = %d, want 1", len(pages))
	}
	page := pages[0]
	if page.ID != pageID || page.State != domain.WikiProposed ||
		page.ProposedByRunID == nil || *page.ProposedByRunID != f.run.ID {
		t.Fatalf("page = %+v, want a proposed page attributed to the run", page)
	}
	if page.ProposedReason == nil || *page.ProposedReason != "You corrected me twice about migrations" {
		t.Fatalf("proposed_reason = %v, want the tool's reason persisted (S35 provenance)", page.ProposedReason)
	}

	// Zero live pages: the proposal is never auto-written.
	for _, p := range pages {
		if p.State == domain.WikiLive {
			t.Fatalf("a live page appeared: %+v", p)
		}
	}

	// An edit proposal targets the page and records the base version, under its own slug.
	// (First make the target live, as a human-accepted page would be.)
	if _, err := e.st.Writer().ExecContext(context.Background(),
		`UPDATE wiki_pages SET state = 'live' WHERE id = ?`, pageID); err != nil {
		t.Fatal(err)
	}
	isErr, result = e.callTool(f.token, "propose_wiki_page", map[string]any{
		"title": "Database migrations", "body": "Updated guidance.",
		"reason": "stale", "edits_slug": "database-migrations",
	})
	if isErr {
		t.Fatalf("edit propose errored: %v", result)
	}
	editPage, err := e.st.Wiki().BySlug(context.Background(), f.project.ID, result["slug"].(string))
	if err != nil {
		t.Fatal(err)
	}
	if editPage.ProposalTargetID == nil || *editPage.ProposalTargetID != pageID ||
		editPage.ProposedBaseVersion == nil || *editPage.ProposedBaseVersion != 1 ||
		editPage.Slug == "database-migrations" {
		t.Fatalf("edit proposal = %+v, want target %s at base version 1 under a distinct slug",
			editPage, pageID)
	}

	// wiki.proposed events were published; the run recorded outputs.
	var n int
	if err := e.st.Reader().QueryRowContext(context.Background(),
		`SELECT COUNT(*) FROM events WHERE kind = 'wiki' AND activity_type = 'proposed'`).
		Scan(&n); err != nil || n != 2 {
		t.Fatalf("wiki.proposed events = %d (err %v), want 2", n, err)
	}
	outputs, err := e.st.RunOutputs().ForRun(context.Background(), f.run.ID)
	if err != nil || len(outputs) != 2 {
		t.Fatalf("run outputs = %d (err %v), want 2 wiki proposals", len(outputs), err)
	}

	// D7 enforcement: an agent without create_wiki_pages is refused in the service.
	f2 := e.fixtures(domain.AutonomyAuto, domain.AgentPermissions{})
	isErr, result = e.callTool(f2.token, "propose_wiki_page", map[string]any{
		"title": "Nope", "body": "nope", "reason": "nope",
	})
	if !isErr || !strings.Contains(result["text"].(string), "create_wiki_pages") {
		t.Fatalf("permissionless propose = (%v, %v), want a refusal naming the permission", isErr, result)
	}
}

func TestCheckCriterion(t *testing.T) {
	e := newEnv(t)
	f := e.fixtures(domain.AutonomyAuto, domain.AgentPermissions{})
	_, crit := e.ticket(&f, true)

	isErr, result := e.callTool(f.token, "check_criterion", map[string]any{
		"criterion_id": crit.ID, "met": true, "note": "covered by charge.test.ts:88",
	})
	if isErr {
		t.Fatalf("check errored: %v", result)
	}
	got, err := e.st.Criteria().ByID(context.Background(), crit.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !got.Checked || got.Note != "covered by charge.test.ts:88" ||
		got.CheckedByRunID == nil || *got.CheckedByRunID != f.run.ID || got.CheckedByUserID != nil {
		t.Fatalf("criterion = %+v, want checked by the run", got)
	}

	// A criterion of a different ticket (another run's project entirely) is refused.
	other := e.fixtures(domain.AutonomyAuto, domain.AgentPermissions{})
	_, otherCrit := e.ticket(&other, true)
	isErr, result = e.callTool(f.token, "check_criterion", map[string]any{
		"criterion_id": otherCrit.ID, "met": true,
	})
	if !isErr || !strings.Contains(result["text"].(string), "different ticket") {
		t.Fatalf("foreign criterion = (%v, %v), want a refusal", isErr, result)
	}
	if got, _ := e.st.Criteria().ByID(context.Background(), otherCrit.ID); got.Checked {
		t.Fatal("foreign criterion was checked anyway")
	}
}

func TestProtocolBasics(t *testing.T) {
	e := newEnv(t)
	f := e.fixtures(domain.AutonomyAuto, domain.AgentPermissions{})

	// initialize echoes the requested protocol version and names the server.
	status, v := e.rpc(f.token, "initialize", map[string]any{
		"protocolVersion": "2025-03-26",
		"capabilities":    map[string]any{},
		"clientInfo":      map[string]any{"name": "claude-code", "version": "2.0"},
	})
	if status != http.StatusOK {
		t.Fatalf("initialize = %d", status)
	}
	result := v["result"].(map[string]any)
	if result["protocolVersion"] != "2025-03-26" {
		t.Fatalf("protocolVersion = %v", result["protocolVersion"])
	}
	if result["serverInfo"].(map[string]any)["name"] != "lexicode" {
		t.Fatalf("serverInfo = %v", result["serverInfo"])
	}

	// notifications/initialized: 202, no body.
	resp, err := http.Post(e.srv.URL+"/mcp/"+f.token, "application/json",
		strings.NewReader(`{"jsonrpc":"2.0","method":"notifications/initialized"}`))
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("notification = %d, want 202", resp.StatusCode)
	}

	// tools/list names exactly the five contract tools.
	_, v = e.rpc(f.token, "tools/list", nil)
	tools := v["result"].(map[string]any)["tools"].([]any)
	names := map[string]bool{}
	for _, tl := range tools {
		names[tl.(map[string]any)["name"].(string)] = true
	}
	for _, want := range []string{"ask_human", "set_step", "propose_wiki_page", "check_criterion",
		"request_approval", "submit_review"} {
		if !names[want] {
			t.Fatalf("tools/list lacks %s: %v", want, names)
		}
	}
	if len(tools) != 6 {
		t.Fatalf("tools/list has %d tools, want 6", len(tools))
	}

	// GET (server-initiated stream) is 405; DELETE (session end) is a 200 no-op.
	req, _ := http.NewRequestWithContext(context.Background(), http.MethodGet, e.srv.URL+"/mcp/"+f.token, nil)
	if resp, err = http.DefaultClient.Do(req); err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusMethodNotAllowed {
		t.Fatalf("GET = %d, want 405", resp.StatusCode)
	}

	// A second respond to an already-answered elicitation conflicts.
	done := make(chan struct{})
	go func() {
		e.callTool(f.token, "ask_human", askArgs(t))
		close(done)
	}()
	el := e.waitPending(f.run.ID)
	if status, _ := e.doJSON(e.owner, "POST", "/api/v1/elicitations/"+el.ID+"/respond",
		`{"action":"answer","text":"one"}`); status != http.StatusOK {
		t.Fatalf("first respond = %d", status)
	}
	<-done
	status, _ = e.doJSON(e.owner, "POST", "/api/v1/elicitations/"+el.ID+"/respond",
		`{"action":"answer","text":"two"}`)
	if status != http.StatusConflict {
		t.Fatalf("second respond = %d, want 409", status)
	}
}

// The blocked call expires at the ceiling: the row goes `expired` and the tool call ends
// with an honest error rather than hanging forever.
func TestAskHumanCeilingExpires(t *testing.T) {
	e := newEnvWithCeiling(t, 150*time.Millisecond)
	f := e.fixtures(domain.AutonomyApproveEach, domain.AgentPermissions{})

	start := time.Now()
	isErr, result := e.callTool(f.token, "ask_human", askArgs(t))
	if !isErr {
		t.Fatalf("expired ask returned a non-error result: %v", result)
	}
	if time.Since(start) > 5*time.Second {
		t.Fatal("expiry took far longer than the ceiling")
	}
	pending, err := e.st.Elicitations().PendingForRun(context.Background(), f.run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(pending) != 0 {
		t.Fatalf("elicitation still pending after expiry: %+v", pending)
	}
}
