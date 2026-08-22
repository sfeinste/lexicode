package triggers_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spruce/lexicode/internal/domain"
	"github.com/spruce/lexicode/internal/kernel/audit"
	"github.com/spruce/lexicode/internal/kernel/auth"
	"github.com/spruce/lexicode/internal/kernel/httpx"
	"github.com/spruce/lexicode/internal/kernel/ports"
	"github.com/spruce/lexicode/internal/kernel/store"
	"github.com/spruce/lexicode/internal/service/projects"
	"github.com/spruce/lexicode/internal/service/triggers"
)

// fakeSource is a registered EventSource with a github.poll-shaped catalog, so validation has
// something real to check kinds and activity types against.
type fakeSource struct{ id string }

func (s fakeSource) ID() string { return s.id }
func (s fakeSource) Catalog() ports.EventCatalog {
	return ports.EventCatalog{Events: []ports.EventDescriptor{
		{
			Kind: "pull_request", Label: "Pull request",
			ActivityTypes: []ports.ActivityType{
				{Value: "opened"}, {Value: "synchronize"},
				{Value: "ready_for_review"}, {Value: "closed"},
			},
			Filters: []ports.FilterField{
				{Key: "branches", Kind: "glob-list"},
				{Key: "paths", Kind: "glob-list"},
				{Key: "labels", Kind: "label-list"},
			},
			SubjectKey: "pr:{{pr.number}}",
		},
		{
			Kind: "check_suite", Label: "Check suite",
			ActivityTypes: []ports.ActivityType{{Value: "completed"}},
			Filters:       []ports.FilterField{{Key: "branches", Kind: "glob-list"}},
			SubjectKey:    "pr:{{pr.number}}",
		},
	}}
}
func (s fakeSource) Start(context.Context, ports.Emit) error { return nil }
func (s fakeSource) Stop(context.Context) error              { return nil }

// pickyAction is a registered action whose Describe rejects params without an "agent" key —
// exercising save-time param validation for known IDs.
type pickyAction struct{}

func (pickyAction) ID() string                { return "picky" }
func (pickyAction) Label() string             { return "Picky" }
func (pickyAction) Schema() ports.ParamSchema { return ports.ParamSchema{} }
func (pickyAction) Describe(params json.RawMessage) (string, error) {
	var p struct {
		Agent string `json:"agent"`
	}
	if err := json.Unmarshal(params, &p); err != nil || p.Agent == "" {
		return "", fmt.Errorf("an agent is required")
	}
	return "picky " + p.Agent, nil
}
func (pickyAction) Execute(context.Context, ports.ActionContext, json.RawMessage) (ports.ActionResult, error) {
	return ports.ActionResult{Outcome: domain.FiringSucceeded}, nil
}

type env struct {
	t   *testing.T
	st  *store.Store
	srv *httptest.Server
}

func newEnv(t *testing.T) *env {
	t.Helper()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	st, err := store.Open(store.Options{Path: filepath.Join(t.TempDir(), "s26http.db"), Logger: logger})
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
	projects.New(projects.Options{Store: st, Audit: auditW, Logger: logger}).Routes(mux, authSvc)
	svc := triggers.New(triggers.Options{
		Store: st, Audit: auditW, Logger: logger,
		Sources: func() []ports.EventSource { return []ports.EventSource{fakeSource{id: "github.poll"}} },
		Action: func(id string) (ports.TriggerAction, error) {
			if id == "picky" {
				return pickyAction{}, nil
			}
			return nil, fmt.Errorf("no trigger action %q is registered", id)
		},
	})
	svc.Routes(mux, authSvc)

	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return &env{t: t, st: st, srv: srv}
}

func (e *env) owner() *http.Client {
	e.t.Helper()
	jar, err := cookiejar.New(nil)
	if err != nil {
		e.t.Fatal(err)
	}
	c := &http.Client{Jar: jar}
	status, _ := e.doJSON(c, "POST", "/api/v1/auth/setup",
		`{"email":"ada@example.com","display_name":"Ada","password":"correct horse"}`)
	if status != http.StatusCreated {
		e.t.Fatalf("setup = %d, want 201", status)
	}
	return c
}

func (e *env) project(c *http.Client, key string) {
	e.t.Helper()
	status, _ := e.doJSON(c, "POST", "/api/v1/projects",
		fmt.Sprintf(`{"key":%q,"name":"Project %s"}`, key, key))
	if status != http.StatusCreated {
		e.t.Fatalf("create project %s = %d, want 201", key, status)
	}
}

func (e *env) doJSON(c *http.Client, method, path, body string) (int, []byte) {
	e.t.Helper()
	var rd io.Reader
	if body != "" {
		rd = bytes.NewReader([]byte(body))
	}
	req, err := http.NewRequest(method, e.srv.URL+path, rd)
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
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		e.t.Fatal(err)
	}
	return resp.StatusCode, b
}

const validCreate = `{
	"name": "Agent push → review",
	"event": "pull_request",
	"activity_types": ["synchronize"],
	"filters": {"branches": ["dev/*"]},
	"conditions": {"all": [
		{"op": "actor.is_agent"},
		{"field": "pr.files_changed", "op": "number.lt", "value": 400}
	]},
	"actions": [{"action_id": "run_agent", "params": {"agent_name": "Reviewer"}}]
}`

func TestTriggerCRUD(t *testing.T) {
	e := newEnv(t)
	c := e.owner()
	e.project(c, "PAY")

	// Create — an unregistered action_id is stored (S28 ships the actions), the rest is
	// validated now.
	status, body := e.doJSON(c, "POST", "/api/v1/projects/PAY/triggers", validCreate)
	if status != http.StatusCreated {
		t.Fatalf("create = %d: %s", status, body)
	}
	var created struct {
		ID         string          `json:"id"`
		Name       string          `json:"name"`
		Enabled    bool            `json:"enabled"`
		SourceID   string          `json:"source_id"`
		Conditions json.RawMessage `json:"conditions"`
		LoopConfig json.RawMessage `json:"loop_config"`
	}
	if err := json.Unmarshal(body, &created); err != nil {
		t.Fatal(err)
	}
	if !created.Enabled || created.SourceID != "github.poll" {
		t.Fatalf("created = %+v, want enabled with the default source", created)
	}
	var lc map[string]any
	if err := json.Unmarshal(created.LoopConfig, &lc); err != nil || lc["depth_limit"] != float64(3) {
		t.Fatalf("loop_config = %s, want the §6.1 default", created.LoopConfig)
	}

	// List includes the row with a health aggregate (empty so far).
	status, body = e.doJSON(c, "GET", "/api/v1/projects/PAY/triggers", "")
	if status != http.StatusOK {
		t.Fatalf("list = %d: %s", status, body)
	}
	var list struct {
		Triggers []struct {
			ID     string `json:"id"`
			Health *struct {
				Counts      map[string]int64 `json:"counts"`
				LastFiredAt *string          `json:"last_fired_at"`
			} `json:"health"`
		} `json:"triggers"`
	}
	if err := json.Unmarshal(body, &list); err != nil {
		t.Fatal(err)
	}
	if len(list.Triggers) != 1 || list.Triggers[0].ID != created.ID || list.Triggers[0].Health == nil {
		t.Fatalf("list = %s", body)
	}

	// Patch: disable and rename; absent fields unchanged.
	status, body = e.doJSON(c, "PATCH", "/api/v1/triggers/"+created.ID,
		`{"enabled": false, "name": "renamed"}`)
	if status != http.StatusOK {
		t.Fatalf("patch = %d: %s", status, body)
	}
	var patched struct {
		Name       string          `json:"name"`
		Enabled    bool            `json:"enabled"`
		Conditions json.RawMessage `json:"conditions"`
	}
	if err := json.Unmarshal(body, &patched); err != nil {
		t.Fatal(err)
	}
	if patched.Enabled || patched.Name != "renamed" || !bytes.Contains(patched.Conditions, []byte("actor.is_agent")) {
		t.Fatalf("patched = %+v (%s)", patched, patched.Conditions)
	}

	// Audit carries both mutations.
	entries, err := e.st.Audit().List(context.Background(), store.AuditFilter{TargetKind: "trigger"})
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	for _, en := range entries {
		seen[en.Action] = true
	}
	for _, action := range []string{"trigger.create", "trigger.update"} {
		if !seen[action] {
			t.Fatalf("audit log is missing %s; have %v", action, seen)
		}
	}

	// Delete, then 404.
	if status, body = e.doJSON(c, "DELETE", "/api/v1/triggers/"+created.ID, ""); status != http.StatusNoContent {
		t.Fatalf("delete = %d: %s", status, body)
	}
	if status, _ = e.doJSON(c, "GET", "/api/v1/triggers/"+created.ID, ""); status != http.StatusNotFound {
		t.Fatalf("get after delete = %d, want 404", status)
	}
}

func TestTriggerValidation(t *testing.T) {
	e := newEnv(t)
	c := e.owner()
	e.project(c, "PAY")

	cases := []struct {
		name  string
		body  string
		field string // expected errors[].field
	}{
		{"unknown event kind",
			`{"name":"x","event":"deployment"}`, "event"},
		{"activity type not in catalog",
			`{"name":"x","event":"pull_request","activity_types":["merged"]}`, "activity_types"},
		{"unknown source",
			`{"name":"x","event":"pull_request","source_id":"gitlab.hooks"}`, "source_id"},
		{"unknown operator",
			`{"name":"x","event":"pull_request","conditions":{"all":[{"field":"pr.title","op":"text.regex","value":"a"}]}}`,
			"conditions"},
		{"bad field path",
			`{"name":"x","event":"pull_request","conditions":{"all":[{"field":"pr..title","op":"text.is","value":"a"}]}}`,
			"conditions"},
		{"expression in field path",
			`{"name":"x","event":"pull_request","conditions":{"all":[{"field":"pr.title | upper","op":"text.is","value":"a"}]}}`,
			"conditions"},
		{"node with two shapes",
			`{"name":"x","event":"pull_request","conditions":{"all":[],"field":"pr.title"}}`,
			"conditions"},
		{"missing name", `{"event":"pull_request"}`, "name"},
		{"malformed glob filter",
			`{"name":"x","event":"pull_request","filters":{"branches":["dev/["]}}`, "filters"},
		{"filter the event does not offer",
			`{"name":"x","event":"check_suite","filters":{"paths":["src/*"]}}`, "filters"},
		{"cron outside schedule",
			`{"name":"x","event":"pull_request","cron":"0 9 * * 1"}`, "cron"},
		{"loop_config with a typoed key",
			`{"name":"x","event":"pull_request","loop_config":{"debounce_secs":90}}`, "loop_config"},
		{"registered action with bad params",
			`{"name":"x","event":"pull_request","actions":[{"action_id":"picky","params":{}}]}`, "actions"},
		{"action without an id",
			`{"name":"x","event":"pull_request","actions":[{"params":{}}]}`, "actions"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			status, body := e.doJSON(c, "POST", "/api/v1/projects/PAY/triggers", tc.body)
			if status != http.StatusBadRequest {
				t.Fatalf("create = %d, want 400: %s", status, body)
			}
			var p struct {
				Errors []struct {
					Field string `json:"field"`
				} `json:"errors"`
			}
			if err := json.Unmarshal(body, &p); err != nil {
				t.Fatal(err)
			}
			found := false
			for _, fe := range p.Errors {
				if fe.Field == tc.field {
					found = true
				}
			}
			if !found {
				t.Fatalf("no error names field %q: %s", tc.field, body)
			}
		})
	}

	// A registered action with good params is accepted.
	status, body := e.doJSON(c, "POST", "/api/v1/projects/PAY/triggers",
		`{"name":"ok","event":"pull_request","actions":[{"action_id":"picky","params":{"agent":"Dev"}}]}`)
	if status != http.StatusCreated {
		t.Fatalf("create with valid picky params = %d: %s", status, body)
	}
}

func TestTriggerFiringsEndpoint(t *testing.T) {
	e := newEnv(t)
	c := e.owner()
	e.project(c, "PAY")

	status, body := e.doJSON(c, "POST", "/api/v1/projects/PAY/triggers", validCreate)
	if status != http.StatusCreated {
		t.Fatalf("create = %d: %s", status, body)
	}
	var created struct {
		ID        string `json:"id"`
		ProjectID string `json:"project_id"`
	}
	if err := json.Unmarshal(body, &created); err != nil {
		t.Fatal(err)
	}

	// Two firing rows straight through the repo: history is history however it got there.
	ctx := context.Background()
	for i, outcome := range []domain.FiringOutcome{domain.FiringSucceeded, domain.FiringNoAction} {
		ev := domain.Event{
			ID: domain.NewID(), ProjectID: &created.ProjectID, Source: "github.poll",
			Kind: "pull_request", ActivityType: "synchronize", ActorKind: domain.ActorAgent,
			SubjectKind: "pr", Payload: json.RawMessage(`{}`),
			DedupeKey: fmt.Sprintf("t:%d", i), DispatchState: domain.DispatchDone,
			OccurredAt: domain.Now(), CreatedAt: domain.Now(),
		}
		if err := e.st.Events().Insert(ctx, &ev); err != nil {
			t.Fatal(err)
		}
		f := domain.TriggerFiring{
			ID: domain.NewID(), TriggerID: created.ID, EventID: ev.ID,
			Outcome: outcome, Reason: "conditions not met",
			Warnings:  json.RawMessage(`["unknown path {{pr.x}} rendered as empty"]`),
			CreatedAt: fmt.Sprintf("2026-08-2%dT00:00:00Z", i),
		}
		if _, err := e.st.Firings().Create(ctx, &f); err != nil {
			t.Fatal(err)
		}
	}

	status, body = e.doJSON(c, "GET", "/api/v1/triggers/"+created.ID+"/firings", "")
	if status != http.StatusOK {
		t.Fatalf("firings = %d: %s", status, body)
	}
	var out struct {
		Firings []struct {
			Outcome  string          `json:"outcome"`
			Reason   string          `json:"reason"`
			Warnings json.RawMessage `json:"warnings"`
		} `json:"firings"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		t.Fatal(err)
	}
	if len(out.Firings) != 2 {
		t.Fatalf("firings = %s", body)
	}
	// Newest first.
	if out.Firings[0].Outcome != string(domain.FiringNoAction) {
		t.Fatalf("order/outcome wrong: %s", body)
	}
	if !strings.Contains(string(out.Firings[0].Warnings), "unknown path") {
		t.Fatalf("warnings not surfaced: %s", body)
	}

	// The health aggregate on GET reflects both outcomes.
	status, body = e.doJSON(c, "GET", "/api/v1/triggers/"+created.ID, "")
	if status != http.StatusOK {
		t.Fatalf("get = %d", status)
	}
	var got struct {
		Health struct {
			Counts      map[string]int64 `json:"counts"`
			LastFiredAt *string          `json:"last_fired_at"`
		} `json:"health"`
	}
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatal(err)
	}
	if got.Health.Counts["succeeded"] != 1 || got.Health.Counts["no_action"] != 1 || got.Health.LastFiredAt == nil {
		t.Fatalf("health = %s", body)
	}

	// limit is validated.
	if status, _ = e.doJSON(c, "GET", "/api/v1/triggers/"+created.ID+"/firings?limit=0", ""); status != http.StatusBadRequest {
		t.Fatalf("limit=0 = %d, want 400", status)
	}
}

// TestTriggerDeleteInUse: a trigger a run points at cannot be deleted — 409 with "disable it
// instead" — because deleting it would orphan the run's provenance (D-15's spirit).
func TestTriggerDeleteInUse(t *testing.T) {
	e := newEnv(t)
	c := e.owner()
	e.project(c, "PAY")
	status, body := e.doJSON(c, "POST", "/api/v1/projects/PAY/triggers", validCreate)
	if status != http.StatusCreated {
		t.Fatalf("create = %d: %s", status, body)
	}
	var created struct {
		ID        string `json:"id"`
		ProjectID string `json:"project_id"`
	}
	if err := json.Unmarshal(body, &created); err != nil {
		t.Fatal(err)
	}

	ctx := context.Background()
	agent := domain.Agent{
		ID: domain.NewID(), ProjectID: created.ProjectID, Name: "Dev", Color: "#0af",
		RuntimeID: "claude-code", Model: "claude-sonnet", Effort: "medium",
		Autonomy: domain.AutonomyAutoGates, GitAuthorName: "Dev",
		GitAuthorEmail: "dev@agents.lexicode.local", ConcurrencyCap: 1,
		MaxWallClockSeconds: 3600, MaxSteps: 200, Enabled: true,
		CreatedAt: domain.Now(), UpdatedAt: domain.Now(),
	}
	if err := e.st.Agents().Create(ctx, &agent); err != nil {
		t.Fatal(err)
	}
	trigID := created.ID
	run := domain.Run{
		ID: domain.NewID(), Seq: 1, ProjectID: created.ProjectID, AgentID: agent.ID,
		TriggerID: &trigID, State: domain.RunFailed, Autonomy: domain.AutonomyAutoGates,
		Model: "claude-sonnet", Effort: "medium", RuntimeID: "claude-code",
		SandboxID: "docker", SubjectKey: "pr:219", QueuedAt: domain.Now(),
	}
	if err := e.st.Runs().Create(ctx, &run); err != nil {
		t.Fatal(err)
	}

	status, body = e.doJSON(c, "DELETE", "/api/v1/triggers/"+created.ID, "")
	if status != http.StatusConflict {
		t.Fatalf("delete of an in-use trigger = %d, want 409: %s", status, body)
	}
	if !bytes.Contains(body, []byte("disable it instead")) {
		t.Fatalf("problem does not say what to do instead: %s", body)
	}
}

func TestTriggerRoutesRequireAuth(t *testing.T) {
	e := newEnv(t)
	c := e.owner()
	e.project(c, "PAY")
	anon := &http.Client{}
	if status, _ := e.doJSON(anon, "GET", "/api/v1/projects/PAY/triggers", ""); status != http.StatusUnauthorized {
		t.Fatalf("anonymous list = %d, want 401", status)
	}
	if status, _ := e.doJSON(anon, "GET", "/api/v1/triggers/nope", ""); status != http.StatusUnauthorized {
		t.Fatalf("anonymous get = %d, want 401", status)
	}
}
