package projects_test

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
	"testing"

	"github.com/spruce/lexicode/internal/kernel/audit"
	"github.com/spruce/lexicode/internal/kernel/auth"
	"github.com/spruce/lexicode/internal/kernel/httpx"
	"github.com/spruce/lexicode/internal/kernel/store"
	"github.com/spruce/lexicode/internal/service/projects"
)

// env is the S08 wiring under test: store + auth + audit + the projects service's routes, served
// exactly as cmd/lexicode serves them (minus the /api/ prefix wrapping, which lives in cmd).
type env struct {
	t   *testing.T
	st  *store.Store
	srv *httptest.Server
}

func newEnv(t *testing.T) *env {
	t.Helper()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	st, err := store.Open(store.Options{Path: filepath.Join(t.TempDir(), "s08.db"), Logger: logger})
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
	svc := projects.New(projects.Options{Store: st, Audit: auditW, Logger: logger})
	svc.Routes(mux, authSvc)

	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return &env{t: t, st: st, srv: srv}
}

// owner signs up the first-run owner and returns an authenticated client.
func (e *env) owner() *http.Client {
	e.t.Helper()
	c := e.client()
	resp := e.do(c, "POST", "/api/v1/auth/setup",
		`{"email":"ada@example.com","display_name":"Ada","password":"correct horse"}`)
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusCreated {
		e.t.Fatalf("setup = %d, want 201", resp.StatusCode)
	}
	return c
}

func (e *env) client() *http.Client {
	jar, err := cookiejar.New(nil)
	if err != nil {
		e.t.Fatal(err)
	}
	return &http.Client{Jar: jar}
}

func (e *env) do(c *http.Client, method, path, body string) *http.Response {
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
	return resp
}

// doJSON performs the request and decodes the response into a generic map, returning the status.
func (e *env) doJSON(c *http.Client, method, path, body string) (int, map[string]any) {
	e.t.Helper()
	resp := e.do(c, method, path, body)
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

func TestCreateProjectDuplicateKeyIsAFieldError(t *testing.T) {
	e := newEnv(t)
	c := e.owner()

	status, _ := e.doJSON(c, "POST", "/api/v1/projects", `{"key":"PAY","name":"Payments"}`)
	if status != http.StatusCreated {
		t.Fatalf("first create = %d, want 201", status)
	}

	status, body := e.doJSON(c, "POST", "/api/v1/projects", `{"key":"PAY","name":"Payments II"}`)
	if status != http.StatusBadRequest {
		t.Fatalf("duplicate create = %d, want 400", status)
	}
	if body["type"] != "validation_failed" {
		t.Fatalf("problem type = %v, want validation_failed", body["type"])
	}
	errs, _ := body["errors"].([]any)
	if len(errs) != 1 {
		t.Fatalf("errors = %v, want exactly one", body["errors"])
	}
	fe := errs[0].(map[string]any)
	if fe["field"] != "key" {
		t.Fatalf("field = %v, want key", fe["field"])
	}
}

func TestCreateProjectValidatesKeyShape(t *testing.T) {
	e := newEnv(t)
	c := e.owner()
	for _, bad := range []string{"p", "1AB", "TOOLONGKEYX", "pa-y", ""} {
		status, body := e.doJSON(c, "POST", "/api/v1/projects",
			`{"key":`+strconvQuote(bad)+`,"name":"X"}`)
		if status != http.StatusBadRequest {
			t.Fatalf("key %q: status = %d, want 400", bad, status)
		}
		if body["type"] != "validation_failed" {
			t.Fatalf("key %q: type = %v", bad, body["type"])
		}
	}
	// Lowercase input is normalized, not rejected.
	status, body := e.doJSON(c, "POST", "/api/v1/projects", `{"key":"pay","name":"Payments"}`)
	if status != http.StatusCreated || body["key"] != "PAY" {
		t.Fatalf("lowercase key: status=%d key=%v, want 201 PAY", status, body["key"])
	}
}

func strconvQuote(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}

func TestInheritanceResolvesFollowsAndReverts(t *testing.T) {
	e := newEnv(t)
	c := e.owner()

	status, body := e.doJSON(c, "POST", "/api/v1/projects", `{"key":"PAY","name":"Payments"}`)
	if status != http.StatusCreated {
		t.Fatalf("create = %d, want 201", status)
	}

	budget := func(b map[string]any) map[string]any {
		return b["settings"].(map[string]any)["daily_budget_cents"].(map[string]any)
	}

	// Null column → inherited:true with the workspace default (2000 from the migration).
	if got := budget(body); got["inherited"] != true || got["value"].(float64) != 2000 ||
		got["workspace_value"].(float64) != 2000 {
		t.Fatalf("fresh project budget = %v, want inherited 2000", got)
	}

	// Override flips inherited and keeps workspace_value visible.
	status, body = e.doJSON(c, "PATCH", "/api/v1/projects/PAY", `{"daily_budget_cents":5000}`)
	if status != http.StatusOK {
		t.Fatalf("override = %d, want 200", status)
	}
	if got := budget(body); got["inherited"] != false || got["value"].(float64) != 5000 ||
		got["workspace_value"].(float64) != 2000 {
		t.Fatalf("overridden budget = %v, want value 5000 over workspace 2000", got)
	}

	// Clearing (null) reverts to inherit...
	status, body = e.doJSON(c, "PATCH", "/api/v1/projects/PAY", `{"daily_budget_cents":null}`)
	if status != http.StatusOK {
		t.Fatalf("clear = %d, want 200", status)
	}
	if got := budget(body); got["inherited"] != true || got["value"].(float64) != 2000 {
		t.Fatalf("cleared budget = %v, want inherited 2000", got)
	}

	// ...and follows a subsequent workspace change (never a frozen copy, data model §1).
	status, _ = e.doJSON(c, "PUT", "/api/v1/workspace/settings", `{"default_daily_budget_cents":750}`)
	if status != http.StatusOK {
		t.Fatalf("workspace update = %d, want 200", status)
	}
	status, body = e.doJSON(c, "GET", "/api/v1/projects/PAY", "")
	if status != http.StatusOK {
		t.Fatalf("get = %d, want 200", status)
	}
	if got := budget(body); got["inherited"] != true || got["value"].(float64) != 750 ||
		got["workspace_value"].(float64) != 750 {
		t.Fatalf("post-workspace-change budget = %v, want inherited 750", got)
	}
}

func TestArchiveHidesFromDefaultListUnarchiveRestores(t *testing.T) {
	e := newEnv(t)
	c := e.owner()

	for _, k := range []string{"PAY", "OPS"} {
		if status, _ := e.doJSON(c, "POST", "/api/v1/projects",
			`{"key":"`+k+`","name":"`+k+`"}`); status != http.StatusCreated {
			t.Fatalf("create %s = %d, want 201", k, status)
		}
	}

	keys := func(includeArchived bool) []string {
		path := "/api/v1/projects"
		if includeArchived {
			path += "?archived=1"
		}
		status, body := e.doJSON(c, "GET", path, "")
		if status != http.StatusOK {
			t.Fatalf("list = %d, want 200", status)
		}
		var out []string
		for _, p := range body["projects"].([]any) {
			out = append(out, p.(map[string]any)["key"].(string))
		}
		return out
	}

	if status, body := e.doJSON(c, "PATCH", "/api/v1/projects/OPS", `{"archived":true}`); status != http.StatusOK {
		t.Fatalf("archive = %d (%v), want 200", status, body)
	} else if body["archived_at"] == nil {
		t.Fatal("archived_at is null after archive")
	}
	if got := keys(false); len(got) != 1 || got[0] != "PAY" {
		t.Fatalf("default list after archive = %v, want [PAY]", got)
	}
	if got := keys(true); len(got) != 2 {
		t.Fatalf("archived-included list = %v, want both", got)
	}

	if status, body := e.doJSON(c, "PATCH", "/api/v1/projects/OPS", `{"archived":false}`); status != http.StatusOK {
		t.Fatalf("unarchive = %d, want 200", status)
	} else if body["archived_at"] != nil {
		t.Fatal("archived_at still set after unarchive")
	}
	if got := keys(false); len(got) != 2 {
		t.Fatalf("default list after unarchive = %v, want both", got)
	}
}

func TestWorkspaceSettingsAreOwnerOnly(t *testing.T) {
	e := newEnv(t)
	owner := e.owner()

	// Mint an invite and redeem it as a member.
	resp := e.do(owner, "POST", "/api/v1/invites", "")
	var inv struct {
		Path string `json:"path"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&inv); err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	token := strings.TrimPrefix(inv.Path, "/invite/")
	member := e.client()
	status, _ := e.doJSON(member, "POST", "/api/v1/invites/"+token+"/redeem",
		`{"email":"theo@example.com","display_name":"Theo","password":"another horse"}`)
	if status != http.StatusCreated {
		t.Fatalf("redeem = %d, want 201", status)
	}

	if status, _ := e.doJSON(member, "GET", "/api/v1/workspace/settings", ""); status != http.StatusForbidden {
		t.Fatalf("member GET workspace settings = %d, want 403", status)
	}
	if status, _ := e.doJSON(member, "PUT", "/api/v1/workspace/settings",
		`{"default_branch":"trunk"}`); status != http.StatusForbidden {
		t.Fatalf("member PUT workspace settings = %d, want 403", status)
	}
	status, body := e.doJSON(owner, "PUT", "/api/v1/workspace/settings", `{"default_branch":"trunk"}`)
	if status != http.StatusOK || body["default_branch"] != "trunk" {
		t.Fatalf("owner PUT = %d %v, want 200 trunk", status, body["default_branch"])
	}
}

func TestProjectRoutesRequireMembership(t *testing.T) {
	e := newEnv(t)
	owner := e.owner()
	if status, _ := e.doJSON(owner, "POST", "/api/v1/projects",
		`{"key":"PAY","name":"Payments"}`); status != http.StatusCreated {
		t.Fatal("create failed")
	}

	// Anonymous is refused.
	anon := e.client()
	if status, _ := e.doJSON(anon, "GET", "/api/v1/projects/PAY", ""); status != http.StatusUnauthorized {
		t.Fatalf("anonymous get = %d, want 401", status)
	}
	// Overview answers for a member (the owner passes RequireProjectMember by role).
	status, body := e.doJSON(owner, "GET", "/api/v1/projects/PAY/overview", "")
	if status != http.StatusOK {
		t.Fatalf("overview = %d, want 200", status)
	}
	if body["owner"].(map[string]any)["display_name"] != "Ada" {
		t.Fatalf("overview owner = %v, want Ada", body["owner"])
	}
	if body["repo"] != nil {
		t.Fatalf("overview repo = %v, want null before S14", body["repo"])
	}
}

func TestMutationsWriteAudit(t *testing.T) {
	e := newEnv(t)
	c := e.owner()
	if status, _ := e.doJSON(c, "POST", "/api/v1/projects",
		`{"key":"PAY","name":"Payments"}`); status != http.StatusCreated {
		t.Fatal("create failed")
	}
	if status, _ := e.doJSON(c, "PATCH", "/api/v1/projects/PAY",
		`{"archived":true}`); status != http.StatusOK {
		t.Fatal("archive failed")
	}
	if status, _ := e.doJSON(c, "PUT", "/api/v1/workspace/settings",
		`{"default_verification_days":30}`); status != http.StatusOK {
		t.Fatal("workspace update failed")
	}

	entries, err := e.st.Audit().List(context.Background(), store.AuditFilter{Limit: 50})
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]bool{}
	for _, en := range entries {
		got[en.Action] = true
		if en.ActorID == nil {
			t.Fatalf("audit entry %s has no actor", en.Action)
		}
	}
	for _, want := range []string{"project.create", "project.archive", "workspace.settings.update"} {
		if !got[want] {
			t.Fatalf("audit log is missing %q; have %v", want, got)
		}
	}
}
