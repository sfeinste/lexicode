package agents_test

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

	"github.com/spruce/lexicode/internal/kernel/audit"
	"github.com/spruce/lexicode/internal/kernel/auth"
	"github.com/spruce/lexicode/internal/kernel/httpx"
	"github.com/spruce/lexicode/internal/kernel/store"
	"github.com/spruce/lexicode/internal/service/agents"
	"github.com/spruce/lexicode/internal/service/projects"
)

// env wires store + auth + audit + the projects and agents services' routes, exactly as
// cmd/lexicode serves them.
type env struct {
	t   *testing.T
	st  *store.Store
	srv *httptest.Server
}

func newEnv(t *testing.T) *env {
	t.Helper()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	st, err := store.Open(store.Options{Path: filepath.Join(t.TempDir(), "s16.db"), Logger: logger})
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
	agents.New(agents.Options{Store: st, Audit: auditW, Logger: logger}).Routes(mux, authSvc)

	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return &env{t: t, st: st, srv: srv}
}

// owner signs up the first-run owner and returns an authenticated client.
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

// S16 acceptance: saving an unchanged directive creates no new version; changed content
// appends the next version.
func TestDirectiveVersioning(t *testing.T) {
	e := newEnv(t)
	c := e.owner()
	e.project(c, "PAY")

	status, created := e.doJSON(c, "POST", "/api/v1/projects/PAY/agents",
		`{"name":"Dev","directive":"You are Dev."}`)
	if status != http.StatusCreated {
		t.Fatalf("create agent = %d: %v", status, created)
	}
	id := created["id"].(string)

	// Saving the identical body: 200, no new version, still v1.
	status, saved := e.doJSON(c, "PUT", "/api/v1/agents/"+id+"/directive",
		`{"body":"You are Dev."}`)
	if status != http.StatusOK {
		t.Fatalf("unchanged save = %d, want 200: %v", status, saved)
	}
	if saved["created"] != false || saved["version"].(float64) != 1 {
		t.Fatalf("unchanged save must be a no-op on v1, got %v", saved)
	}

	// Changed body: 201, v2 appended.
	status, saved = e.doJSON(c, "PUT", "/api/v1/agents/"+id+"/directive",
		`{"body":"You are Dev. Be brief.","note":"tightened"}`)
	if status != http.StatusCreated {
		t.Fatalf("changed save = %d, want 201: %v", status, saved)
	}
	if saved["created"] != true || saved["version"].(float64) != 2 {
		t.Fatalf("changed save must append v2, got %v", saved)
	}

	// The version list has exactly two rows, newest first, and v1's content endpoint still
	// serves the original body (append-only).
	status, list := e.doJSON(c, "GET", "/api/v1/agents/"+id+"/directives", "")
	if status != http.StatusOK {
		t.Fatalf("list directives = %d", status)
	}
	versions := list["directives"].([]any)
	if len(versions) != 2 {
		t.Fatalf("directive versions = %d, want 2", len(versions))
	}
	if v := versions[0].(map[string]any)["version"].(float64); v != 2 {
		t.Fatalf("newest version = %v, want 2", v)
	}
	status, v1 := e.doJSON(c, "GET", "/api/v1/agents/"+id+"/directives/1", "")
	if status != http.StatusOK || v1["body"] != "You are Dev." {
		t.Fatalf("v1 content = %d %v", status, v1)
	}
}

// S16 acceptance: two agents in one project cannot share a name (409 with the field named);
// the same name in a different project is fine.
func TestDuplicateNamePerProject(t *testing.T) {
	e := newEnv(t)
	c := e.owner()
	e.project(c, "PAY")
	e.project(c, "OPS")

	status, _ := e.doJSON(c, "POST", "/api/v1/projects/PAY/agents", `{"name":"Dev"}`)
	if status != http.StatusCreated {
		t.Fatalf("first create = %d, want 201", status)
	}
	status, problem := e.doJSON(c, "POST", "/api/v1/projects/PAY/agents", `{"name":"Dev"}`)
	if status != http.StatusConflict {
		t.Fatalf("duplicate create = %d, want 409: %v", status, problem)
	}
	if problem["type"] != "agent_name_taken" {
		t.Fatalf("problem type = %v, want agent_name_taken", problem["type"])
	}
	fields := problem["errors"].([]any)
	if len(fields) != 1 || fields[0].(map[string]any)["field"] != "name" {
		t.Fatalf("409 must name the field, got %v", problem["errors"])
	}
	// Same name, different project: fine.
	status, _ = e.doJSON(c, "POST", "/api/v1/projects/OPS/agents", `{"name":"Dev"}`)
	if status != http.StatusCreated {
		t.Fatalf("same name in another project = %d, want 201", status)
	}
}

// S16 acceptance: disabling an agent removes it from the delegate-eligible list but leaves
// the agent fetchable with its history intact.
func TestDisableRemovesFromEligibleList(t *testing.T) {
	e := newEnv(t)
	c := e.owner()
	e.project(c, "PAY")

	status, res := e.doJSON(c, "POST", "/api/v1/projects/PAY/agents/starter", "")
	if status != http.StatusOK {
		t.Fatalf("starter roster = %d: %v", status, res)
	}
	if got := len(res["created"].([]any)); got != 2 {
		t.Fatalf("starter created %d agents, want 2 (Dev + Reviewer)", got)
	}

	status, list := e.doJSON(c, "GET", "/api/v1/projects/PAY/agents?eligible=1", "")
	if status != http.StatusOK || len(list["agents"].([]any)) != 2 {
		t.Fatalf("eligible before disable = %d %v", status, list)
	}
	var reviewerID string
	for _, raw := range list["agents"].([]any) {
		a := raw.(map[string]any)
		if a["name"] == "Reviewer" {
			reviewerID = a["id"].(string)
		}
	}
	if reviewerID == "" {
		t.Fatal("no Reviewer in the roster")
	}

	status, _ = e.doJSON(c, "PATCH", "/api/v1/agents/"+reviewerID, `{"enabled":false}`)
	if status != http.StatusOK {
		t.Fatalf("disable = %d, want 200", status)
	}

	status, list = e.doJSON(c, "GET", "/api/v1/projects/PAY/agents?eligible=1", "")
	if status != http.StatusOK {
		t.Fatalf("eligible list = %d", status)
	}
	eligible := list["agents"].([]any)
	if len(eligible) != 1 || eligible[0].(map[string]any)["name"] != "Dev" {
		t.Fatalf("eligible after disable = %v, want only Dev", eligible)
	}

	// The full roster still lists both, the agent is still fetchable, and its directive
	// history is intact.
	status, list = e.doJSON(c, "GET", "/api/v1/projects/PAY/agents", "")
	if status != http.StatusOK || len(list["agents"].([]any)) != 2 {
		t.Fatalf("full roster after disable = %d %v", status, list)
	}
	status, ag := e.doJSON(c, "GET", "/api/v1/agents/"+reviewerID, "")
	if status != http.StatusOK || ag["enabled"] != false {
		t.Fatalf("disabled agent fetch = %d %v", status, ag)
	}
	status, dirs := e.doJSON(c, "GET", "/api/v1/agents/"+reviewerID+"/directives", "")
	if status != http.StatusOK || len(dirs["directives"].([]any)) != 1 {
		t.Fatalf("disabled agent history = %d %v", status, dirs)
	}
}

// The identity defaults follow D-9: git author name is the agent name, email is the lowercase
// slug at agents.lexicode.local.
func TestGitIdentityDefaults(t *testing.T) {
	e := newEnv(t)
	c := e.owner()
	e.project(c, "PAY")

	status, a := e.doJSON(c, "POST", "/api/v1/projects/PAY/agents", `{"name":"Code Reviewer"}`)
	if status != http.StatusCreated {
		t.Fatalf("create = %d", status)
	}
	if a["git_author_name"] != "Code Reviewer" {
		t.Fatalf("git_author_name = %v", a["git_author_name"])
	}
	if a["git_author_email"] != "code-reviewer@agents.lexicode.local" {
		t.Fatalf("git_author_email = %v", a["git_author_email"])
	}
}
