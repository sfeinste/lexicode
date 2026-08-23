package bootstrap_test

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
	"github.com/spruce/lexicode/internal/kernel/secrets"
	"github.com/spruce/lexicode/internal/kernel/store"
	githubmod "github.com/spruce/lexicode/internal/module/github"
	"github.com/spruce/lexicode/internal/service/bootstrap"
	"github.com/spruce/lexicode/internal/service/projects"
)

// env wires the S15 stack the way cmd/lexicode does: store + auth + secrets + the real github
// adapter pointed at the fixture server + the bootstrap service's routes.
type env struct {
	t   *testing.T
	st  *store.Store
	sec *secrets.Store
	srv *httptest.Server
	gh  *fakeGitHub
}

func newEnv(t *testing.T, gh *fakeGitHub) *env {
	t.Helper()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	dir := t.TempDir()
	st, err := store.Open(store.Options{Path: filepath.Join(dir, "s15.db"), Logger: logger})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	if _, err := st.Migrate(context.Background()); err != nil {
		t.Fatal(err)
	}
	sec, err := secrets.Open(secrets.Options{
		Store: st, KeyPath: filepath.Join(dir, "master.key"), Logger: logger,
	})
	if err != nil {
		t.Fatal(err)
	}

	mux := httpx.NewMux(httpx.Options{Logger: logger})
	authSvc := auth.New(auth.Options{Store: st, Logger: logger})
	authSvc.Routes(mux)
	auditW := audit.New(audit.Options{Store: st, Logger: logger})

	projSvc := projects.New(projects.Options{Store: st, Audit: auditW, Logger: logger})
	projSvc.Routes(mux, authSvc)

	mod := githubmod.New(githubmod.Options{BaseURL: gh.srv.URL + "/", Logger: logger})
	forge := mod.Forge()
	svc := bootstrap.New(bootstrap.Options{
		Store: st, Secrets: sec,
		Forge: func(id string) (ports.ForgeProvider, error) {
			if id != "github" {
				return nil, fmt.Errorf("no forge %q", id)
			}
			return forge, nil
		},
		Docs: forge, Audit: auditW, Logger: logger,
	})
	svc.Routes(mux, authSvc)

	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return &env{t: t, st: st, sec: sec, srv: srv, gh: gh}
}

// owner signs up the first-run owner, creates project PAY and returns the client.
func (e *env) owner() *http.Client {
	e.t.Helper()
	jar, err := cookiejar.New(nil)
	if err != nil {
		e.t.Fatal(err)
	}
	c := &http.Client{Jar: jar}
	if code, _ := e.doJSON(c, "POST", "/api/v1/auth/setup",
		`{"email":"ada@example.com","display_name":"Ada","password":"correct horse"}`); code != 201 {
		e.t.Fatalf("setup = %d, want 201", code)
	}
	if code, _ := e.doJSON(c, "POST", "/api/v1/projects",
		`{"key":"PAY","name":"Payments"}`); code != 201 {
		e.t.Fatalf("create project = %d, want 201", code)
	}
	return c
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

func (e *env) connect(c *http.Client) map[string]any {
	e.t.Helper()
	code, body := e.doJSON(c, "POST", "/api/v1/projects/PAY/repo",
		`{"owner":"acme","name":"payments","token":"ghp_fixturetoken1234567890"}`)
	if code != 201 {
		e.t.Fatalf("connect = %d, want 201: %v", code, body)
	}
	return body
}

func (e *env) projectID() string {
	e.t.Helper()
	p, err := e.st.Projects().ByKey(context.Background(), "PAY")
	if err != nil {
		e.t.Fatal(err)
	}
	return p.ID
}

// ---------------------------------------------------------------- connect -----

func TestConnectVerifiesAndPersists(t *testing.T) {
	e := newEnv(t, newFakeGitHub(t, fixtureFiles(), fixtureIssues(3)))
	c := e.owner()
	body := e.connect(c)

	if body["owner"] != "acme" || body["name"] != "payments" {
		t.Fatalf("repo body = %v", body)
	}
	if body["default_branch"] != "main" {
		t.Fatalf("default_branch = %v, want main", body["default_branch"])
	}
	if body["head_sha"] != "abc123def456" {
		t.Fatalf("head_sha = %v", body["head_sha"])
	}
	if body["head_message"] != "Fix the flaky payment test" {
		t.Fatalf("head_message = %v, want first line only", body["head_message"])
	}
	if body["has_token"] != true {
		t.Fatalf("has_token = %v, want true", body["has_token"])
	}

	ctx := context.Background()
	pid := e.projectID()

	// The repos row carries what the About card needs and references the secret.
	rp, err := e.st.Repos().ByProject(ctx, pid)
	if err != nil {
		t.Fatal(err)
	}
	if rp.Provider != "github" || rp.TokenSecretID == nil {
		t.Fatalf("repos row = %+v", rp)
	}

	// The PAT landed in the secret store, project scope, under GITHUB_TOKEN — and the stored
	// value round-trips (kernel-internal read; no API ever returns it).
	infos, err := e.sec.List(ctx, domain.SecretScopeProject, pid)
	if err != nil {
		t.Fatal(err)
	}
	if len(infos) != 1 || infos[0].Name != "GITHUB_TOKEN" {
		t.Fatalf("secrets = %+v, want one GITHUB_TOKEN", infos)
	}
	if infos[0].ID != *rp.TokenSecretID {
		t.Fatalf("repos.token_secret_id = %s, want %s", *rp.TokenSecretID, infos[0].ID)
	}
	val, err := e.sec.Get(ctx, infos[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	if val != "ghp_fixturetoken1234567890" {
		t.Fatalf("stored token does not round-trip")
	}

	// The overview now carries the repo for the About card.
	code, ov := e.doJSON(c, "GET", "/api/v1/projects/PAY/overview", "")
	if code != 200 {
		t.Fatalf("overview = %d", code)
	}
	repo, _ := ov["repo"].(map[string]any)
	if repo == nil || repo["head_sha"] != "abc123def456" {
		t.Fatalf("overview repo = %v", ov["repo"])
	}
}

func TestConnectValidatesRequiredFields(t *testing.T) {
	e := newEnv(t, newFakeGitHub(t, fixtureFiles(), nil))
	c := e.owner()

	code, body := e.doJSON(c, "POST", "/api/v1/projects/PAY/repo",
		`{"owner":"","name":"","token":""}`)
	if code != 400 {
		t.Fatalf("empty connect = %d, want 400: %v", code, body)
	}
	if body["type"] != "validation_failed" {
		t.Fatalf("problem type = %v", body["type"])
	}
	errs, _ := body["errors"].([]any)
	if len(errs) != 3 {
		t.Fatalf("errors = %v, want owner+name+token", body["errors"])
	}
}

// ---------------------------------------------------------------- preview -----

func TestPreviewOffersTwelveCheckedIssues(t *testing.T) {
	e := newEnv(t, newFakeGitHub(t, fixtureFiles(), fixtureIssues(12)))
	c := e.owner()
	e.connect(c)

	code, pv := e.doJSON(c, "POST", "/api/v1/projects/PAY/bootstrap/preview", "")
	if code != 200 {
		t.Fatalf("preview = %d: %v", code, pv)
	}
	issues, _ := pv["issues"].([]any)
	if len(issues) != 12 {
		t.Fatalf("issues = %d, want 12", len(issues))
	}
	for _, raw := range issues {
		is := raw.(map[string]any)
		if is["checked"] != true || is["already_imported"] != false {
			t.Fatalf("issue candidate not checked-by-default: %v", is)
		}
		if _, ok := is["labels"].([]any); !ok {
			t.Fatalf("issue %v labels = %v, want a JSON array (never null)", is["number"], is["labels"])
		}
	}

	// Docs: the detection list with the plan's proposed scopes.
	docs := map[string]map[string]any{}
	for _, raw := range pv["docs"].([]any) {
		d := raw.(map[string]any)
		docs[d["path"].(string)] = d
	}
	wantScopes := map[string]string{
		"AGENTS.md":                       "always",
		"CLAUDE.md":                       "always",
		".github/copilot-instructions.md": "always",
		"README.md":                       "auto",
		".cursor/rules/frontend.mdc":      "paths",
		".cursor/rules/general.mdc":       "auto",
		"docs/architecture.md":            "auto",
		"docs/adr/001-sqlite.md":          "auto",
	}
	for path, scope := range wantScopes {
		d, ok := docs[path]
		if !ok {
			t.Fatalf("doc %s not detected; got %v", path, pv["docs"])
		}
		if d["proposed_scope"] != scope {
			t.Fatalf("%s proposed scope = %v, want %s", path, d["proposed_scope"], scope)
		}
	}
	if len(docs) != len(wantScopes) {
		t.Fatalf("detected %d docs, want %d: %v", len(docs), len(wantScopes), pv["docs"])
	}
	globs := docs[".cursor/rules/frontend.mdc"]["scope_paths"].([]any)
	if len(globs) != 2 || globs[0] != "web/**" {
		t.Fatalf("frontend.mdc globs = %v", globs)
	}

	// CI detected → the pre-filled rules: the canonical chain's three, plus the human-review
	// rule that lets a person step back into the chain.
	triggers, _ := pv["triggers"].([]any)
	if len(triggers) != 4 {
		t.Fatalf("triggers = %v, want the four suggestions", pv["triggers"])
	}

	// The starter agents.
	agents, _ := pv["agents"].([]any)
	if len(agents) != 2 {
		t.Fatalf("agents = %v, want Dev and Reviewer", pv["agents"])
	}

	// Overview draft comes from the README's first section.
	ovc := pv["overview"].(map[string]any)
	draft := ovc["draft"].(string)
	if !strings.Contains(draft, "payment reconciliation service") {
		t.Fatalf("overview draft = %q", draft)
	}
	if strings.Contains(draft, "npm install") {
		t.Fatalf("overview draft leaked past the first section: %q", draft)
	}
	if ovc["checked"] != true {
		t.Fatalf("overview should be checked when the description is empty: %v", ovc)
	}
}

// ---------------------------------------------------------------- apply -----

func applyBody(issues []int) string {
	nums := make([]string, len(issues))
	for i, n := range issues {
		nums[i] = fmt.Sprintf("%d", n)
	}
	return fmt.Sprintf(`{
		"issues": [%s],
		"docs": [
			{"path": "AGENTS.md", "scope": "always", "paths": []},
			{"path": ".cursor/rules/frontend.mdc", "scope": "paths", "paths": ["web/**"]}
		],
		"triggers": ["agent-pr-review", "changes-requested", "ci-failed-fix", "human-review-address"],
		"agents": ["Dev", "Reviewer"],
		"overview": "A payment reconciliation service."
	}`, strings.Join(nums, ","))
}

func TestApplyCreatesExactlyTheCheckedSubset(t *testing.T) {
	e := newEnv(t, newFakeGitHub(t, fixtureFiles(), fixtureIssues(12)))
	c := e.owner()
	e.connect(c)

	code, res := e.doJSON(c, "POST", "/api/v1/projects/PAY/bootstrap/apply",
		applyBody([]int{2, 3, 5, 7, 11}))
	if code != 200 {
		t.Fatalf("apply = %d: %v", code, res)
	}
	if got := len(res["tickets_created"].([]any)); got != 5 {
		t.Fatalf("tickets created = %d, want 5 (checked 5 of 12)", got)
	}

	ctx := context.Background()
	pid := e.projectID()

	// Exactly 5 tickets, all origin=import, all in a backlog-category column, each carrying
	// the issue marker.
	tickets, err := e.st.Tickets().ForProject(ctx, pid)
	if err != nil {
		t.Fatal(err)
	}
	if len(tickets) != 5 {
		t.Fatalf("tickets = %d, want 5", len(tickets))
	}
	backlog, err := e.st.Columns().ByCategory(ctx, pid, domain.CategoryBacklog)
	if err != nil {
		t.Fatal(err)
	}
	for _, tk := range tickets {
		if tk.Origin != domain.OriginImport {
			t.Fatalf("ticket %s origin = %s, want import", tk.Key, tk.Origin)
		}
		if tk.ColumnID != backlog[0].ID {
			t.Fatalf("ticket %s not in the backlog column", tk.Key)
		}
		if !strings.Contains(tk.Description, "<!-- lexicode:import issue=") {
			t.Fatalf("ticket %s has no import marker:\n%s", tk.Key, tk.Description)
		}
	}

	// Issue 2 had the "bug" label: it exists and is attached.
	labels, err := e.st.Labels().ForProject(ctx, pid)
	if err != nil {
		t.Fatal(err)
	}
	if len(labels) != 1 || labels[0].Name != "bug" {
		t.Fatalf("labels = %+v, want the imported bug label", labels)
	}

	// Two live wiki pages with imported_from set and the chosen scopes.
	pages, err := e.st.Wiki().ForProject(ctx, pid)
	if err != nil {
		t.Fatal(err)
	}
	if len(pages) != 2 {
		t.Fatalf("wiki pages = %d, want 2", len(pages))
	}
	byPath := map[string]domain.WikiPage{}
	for _, p := range pages {
		if p.State != domain.WikiLive {
			t.Fatalf("page %s state = %s, want live (D-11)", p.Slug, p.State)
		}
		if p.ImportedFrom == nil {
			t.Fatalf("page %s has no imported_from", p.Slug)
		}
		byPath[*p.ImportedFrom] = p
	}
	if byPath["AGENTS.md"].AgentScope != domain.ScopeAlways {
		t.Fatalf("AGENTS.md scope = %s", byPath["AGENTS.md"].AgentScope)
	}
	fr := byPath[".cursor/rules/frontend.mdc"]
	if fr.AgentScope != domain.ScopePaths || len(fr.ScopePaths) != 1 {
		t.Fatalf("frontend.mdc page = %+v", fr)
	}

	// Four triggers, all created disabled.
	trs, err := e.st.Triggers().ForProject(ctx, pid)
	if err != nil {
		t.Fatal(err)
	}
	if len(trs) != 4 {
		t.Fatalf("triggers = %d, want 4", len(trs))
	}
	for _, tr := range trs {
		if tr.Enabled {
			t.Fatalf("trigger %q is enabled; suggested triggers must be created disabled", tr.Name)
		}
	}

	// Two agents, each with a version-1 directive linked.
	ags, err := e.st.Agents().ForProject(ctx, pid)
	if err != nil {
		t.Fatal(err)
	}
	if len(ags) != 2 {
		t.Fatalf("agents = %d, want 2", len(ags))
	}
	for _, a := range ags {
		if a.DirectiveVersionID == nil {
			t.Fatalf("agent %s has no directive", a.Name)
		}
		d, err := e.st.Directives().ByID(ctx, *a.DirectiveVersionID)
		if err != nil {
			t.Fatal(err)
		}
		if d.Version != 1 || d.Body == "" {
			t.Fatalf("agent %s directive = %+v", a.Name, d)
		}
		if a.Name == "Reviewer" && a.Permissions.EditFiles {
			t.Fatalf("Reviewer can edit files; the starter must be structurally read-only")
		}
	}

	// The overview draft was applied.
	p, err := e.st.Projects().ByKey(ctx, "PAY")
	if err != nil {
		t.Fatal(err)
	}
	if p.Description != "A payment reconciliation service." {
		t.Fatalf("description = %q", p.Description)
	}
}

func TestRescanMarksImportedAndApplySkipsThem(t *testing.T) {
	e := newEnv(t, newFakeGitHub(t, fixtureFiles(), fixtureIssues(12)))
	c := e.owner()
	e.connect(c)

	if code, res := e.doJSON(c, "POST", "/api/v1/projects/PAY/bootstrap/apply",
		applyBody([]int{2, 3, 5, 7, 11})); code != 200 {
		t.Fatalf("first apply = %d: %v", code, res)
	}

	// Re-scan: the five imported issues come back unchecked and labeled with their ticket
	// keys; the docs likewise; the agents and triggers read already-created.
	code, pv := e.doJSON(c, "POST", "/api/v1/projects/PAY/bootstrap/preview", "")
	if code != 200 {
		t.Fatalf("re-scan preview = %d", code)
	}
	importedCount := 0
	for _, raw := range pv["issues"].([]any) {
		is := raw.(map[string]any)
		n := int(is["number"].(float64))
		switch n {
		case 2, 3, 5, 7, 11:
			importedCount++
			if is["checked"] != false || is["already_imported"] != true {
				t.Fatalf("issue %d should be unchecked and marked imported: %v", n, is)
			}
			if key, _ := is["ticket_key"].(string); !strings.HasPrefix(key, "PAY-") {
				t.Fatalf("issue %d ticket_key = %v", n, is["ticket_key"])
			}
		default:
			if is["checked"] != true || is["already_imported"] != false {
				t.Fatalf("issue %d should still be offered checked: %v", n, is)
			}
		}
	}
	if importedCount != 5 {
		t.Fatalf("imported issues in preview = %d, want 5", importedCount)
	}
	for _, raw := range pv["docs"].([]any) {
		d := raw.(map[string]any)
		imported := d["path"] == "AGENTS.md" || d["path"] == ".cursor/rules/frontend.mdc"
		if d["already_imported"] != imported {
			t.Fatalf("doc %v already_imported = %v, want %v", d["path"], d["already_imported"], imported)
		}
		if imported && d["checked"] != false {
			t.Fatalf("imported doc %v should be unchecked", d["path"])
		}
	}
	for _, raw := range pv["triggers"].([]any) {
		tr := raw.(map[string]any)
		if tr["already_created"] != true || tr["checked"] != false {
			t.Fatalf("trigger %v should read already-created: %v", tr["name"], tr)
		}
	}
	for _, raw := range pv["agents"].([]any) {
		a := raw.(map[string]any)
		if a["already_created"] != true || a["checked"] != false {
			t.Fatalf("agent %v should read already-created: %v", a["name"], a)
		}
	}

	// A stale client re-sending the same apply creates nothing new.
	code, res := e.doJSON(c, "POST", "/api/v1/projects/PAY/bootstrap/apply",
		applyBody([]int{2, 3, 5, 7, 11}))
	if code != 200 {
		t.Fatalf("second apply = %d: %v", code, res)
	}
	if got := len(res["tickets_created"].([]any)); got != 0 {
		t.Fatalf("second apply created %d tickets, want 0", got)
	}
	if got := len(res["issues_skipped"].([]any)); got != 5 {
		t.Fatalf("second apply skipped %d issues, want 5", got)
	}
	if got := len(res["pages_created"].([]any)); got != 0 {
		t.Fatalf("second apply created %d pages, want 0", got)
	}
	if got := len(res["triggers_created"].([]any)); got != 0 {
		t.Fatalf("second apply created %d triggers, want 0", got)
	}
	if got := len(res["agents_created"].([]any)); got != 0 {
		t.Fatalf("second apply created %d agents, want 0", got)
	}

	ctx := context.Background()
	pid := e.projectID()
	tickets, err := e.st.Tickets().ForProject(ctx, pid)
	if err != nil {
		t.Fatal(err)
	}
	if len(tickets) != 5 {
		t.Fatalf("tickets after double apply = %d, want 5 (no duplicates)", len(tickets))
	}
	pages, err := e.st.Wiki().ForProject(ctx, pid)
	if err != nil {
		t.Fatal(err)
	}
	if len(pages) != 2 {
		t.Fatalf("pages after double apply = %d, want 2", len(pages))
	}
}

// ---------------------------------------------------------------- disconnect -----

func TestDisconnectKeepsImportedData(t *testing.T) {
	e := newEnv(t, newFakeGitHub(t, fixtureFiles(), fixtureIssues(12)))
	c := e.owner()
	e.connect(c)
	if code, res := e.doJSON(c, "POST", "/api/v1/projects/PAY/bootstrap/apply",
		applyBody([]int{1, 2, 3})); code != 200 {
		t.Fatalf("apply = %d: %v", code, res)
	}

	code, _ := e.doJSON(c, "DELETE", "/api/v1/projects/PAY/repo", "")
	if code != 204 {
		t.Fatalf("disconnect = %d, want 204", code)
	}

	code, status := e.doJSON(c, "GET", "/api/v1/projects/PAY/repo", "")
	if code != 200 || status["connected"] != false {
		t.Fatalf("status after disconnect = %d %v", code, status)
	}

	ctx := context.Background()
	pid := e.projectID()
	tickets, err := e.st.Tickets().ForProject(ctx, pid)
	if err != nil {
		t.Fatal(err)
	}
	if len(tickets) != 3 {
		t.Fatalf("tickets after disconnect = %d, want 3 (imported data stays)", len(tickets))
	}
	pages, err := e.st.Wiki().ForProject(ctx, pid)
	if err != nil {
		t.Fatal(err)
	}
	if len(pages) != 2 {
		t.Fatalf("wiki pages after disconnect = %d, want 2", len(pages))
	}

	// The stored token is gone with the connection.
	infos, err := e.sec.List(ctx, domain.SecretScopeProject, pid)
	if err != nil {
		t.Fatal(err)
	}
	if len(infos) != 0 {
		t.Fatalf("secrets after disconnect = %+v, want none", infos)
	}
}
