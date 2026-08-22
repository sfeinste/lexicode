package secrets_test

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
	kernelsecrets "github.com/spruce/lexicode/internal/kernel/secrets"
	"github.com/spruce/lexicode/internal/kernel/store"
	"github.com/spruce/lexicode/internal/service/projects"
	secretsvc "github.com/spruce/lexicode/internal/service/secrets"
)

// env is the S13 wiring under test: store + auth + audit + the secret store + the secrets
// service's routes, served as cmd/lexicode serves them. The projects service rides along so
// tests can create projects through the same API the app uses.
type env struct {
	t   *testing.T
	st  *store.Store
	srv *httptest.Server
}

func newEnv(t *testing.T) *env {
	t.Helper()
	dir := t.TempDir()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	st, err := store.Open(store.Options{Path: filepath.Join(dir, "s13.db"), Logger: logger})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	if _, err := st.Migrate(context.Background()); err != nil {
		t.Fatal(err)
	}

	sec, err := kernelsecrets.Open(kernelsecrets.Options{
		Store: st, KeyPath: filepath.Join(dir, "master.key"), Logger: logger,
	})
	if err != nil {
		t.Fatal(err)
	}

	mux := httpx.NewMux(httpx.Options{Logger: logger})
	authSvc := auth.New(auth.Options{Store: st, Logger: logger})
	authSvc.Routes(mux)
	auditW := audit.New(audit.Options{Store: st, Logger: logger})
	projects.New(projects.Options{Store: st, Audit: auditW, Logger: logger}).Routes(mux, authSvc)
	secretsvc.New(secretsvc.Options{
		Store: st, Secrets: sec, Audit: auditW, Logger: logger,
	}).Routes(mux, authSvc)

	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return &env{t: t, st: st, srv: srv}
}

func (e *env) owner() *http.Client {
	e.t.Helper()
	c := e.client()
	status, _ := e.doJSON(c, "POST", "/api/v1/auth/setup",
		`{"email":"ada@example.com","display_name":"Ada","password":"correct horse"}`)
	if status != http.StatusCreated {
		e.t.Fatalf("setup = %d, want 201", status)
	}
	return c
}

// member mints an invite as the owner and redeems it as a new member.
func (e *env) member(owner *http.Client) *http.Client {
	e.t.Helper()
	status, inv := e.doJSON(owner, "POST", "/api/v1/invites", "")
	if status != http.StatusCreated {
		e.t.Fatalf("invite = %d, want 201", status)
	}
	token := strings.TrimPrefix(inv["path"].(string), "/invite/")
	c := e.client()
	status, _ = e.doJSON(c, "POST", "/api/v1/invites/"+token+"/redeem",
		`{"email":"mel@example.com","display_name":"Mel","password":"correct horse too"}`)
	if status != http.StatusCreated {
		e.t.Fatalf("redeem = %d, want 201", status)
	}
	return c
}

func (e *env) project(c *http.Client, key string) {
	e.t.Helper()
	status, _ := e.doJSON(c, "POST", "/api/v1/projects",
		`{"key":"`+key+`","name":"Project `+key+`"}`)
	if status != http.StatusCreated {
		e.t.Fatalf("create project %s = %d, want 201", key, status)
	}
}

func (e *env) client() *http.Client {
	jar, err := cookiejar.New(nil)
	if err != nil {
		e.t.Fatal(err)
	}
	return &http.Client{Jar: jar}
}

// doRaw performs the request and returns status and raw body.
func (e *env) doRaw(c *http.Client, method, path, body string) (int, []byte) {
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
	return resp.StatusCode, raw
}

func (e *env) doJSON(c *http.Client, method, path, body string) (int, map[string]any) {
	e.t.Helper()
	status, raw := e.doRaw(c, method, path, body)
	var v map[string]any
	if len(bytes.TrimSpace(raw)) > 0 {
		if err := json.Unmarshal(raw, &v); err != nil {
			e.t.Fatalf("%s %s: not JSON: %v\n%s", method, path, err, raw)
		}
	}
	return status, v
}

// TestProjectSecretLifecycle: set → list (names and dates, no values) → replace → rename →
// delete, through the HTTP API, with an audit row for every mutation and the value in no
// response and no audit snapshot.
func TestProjectSecretLifecycle(t *testing.T) {
	e := newEnv(t)
	c := e.owner()
	e.project(c, "PAY")
	const value = "ghp_wouldbealeak"

	// Set (create) → 201, no value anywhere in the response.
	status, raw := e.doRaw(c, "POST", "/api/v1/projects/PAY/secrets",
		`{"name":"GITHUB_TOKEN","value":"`+value+`"}`)
	if status != http.StatusCreated {
		t.Fatalf("set = %d, want 201\n%s", status, raw)
	}
	if bytes.Contains(raw, []byte(value)) || bytes.Contains(raw, []byte(`"value"`)) {
		t.Fatalf("set response carries a value: %s", raw)
	}
	var created struct {
		ID   string `json:"id"`
		Name string `json:"name"`
	}
	if err := json.Unmarshal(raw, &created); err != nil {
		t.Fatal(err)
	}

	// List: the name and the set-date, never the value.
	status, raw = e.doRaw(c, "GET", "/api/v1/projects/PAY/secrets", "")
	if status != http.StatusOK {
		t.Fatalf("list = %d, want 200", status)
	}
	if bytes.Contains(raw, []byte(value)) || bytes.Contains(raw, []byte(`"value"`)) {
		t.Fatalf("list response carries a value: %s", raw)
	}
	var list struct {
		Secrets []struct {
			ID        string `json:"id"`
			Name      string `json:"name"`
			UpdatedAt string `json:"updated_at"`
		} `json:"secrets"`
	}
	if err := json.Unmarshal(raw, &list); err != nil {
		t.Fatal(err)
	}
	if len(list.Secrets) != 1 || list.Secrets[0].Name != "GITHUB_TOKEN" ||
		list.Secrets[0].UpdatedAt == "" {
		t.Fatalf("list = %s, want one named secret with a set-date", raw)
	}

	// Replace: same name → 200, same id.
	status, raw = e.doRaw(c, "POST", "/api/v1/projects/PAY/secrets",
		`{"name":"GITHUB_TOKEN","value":"replacement"}`)
	if status != http.StatusOK {
		t.Fatalf("replace = %d, want 200\n%s", status, raw)
	}
	var replaced struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(raw, &replaced); err != nil {
		t.Fatal(err)
	}
	if replaced.ID != created.ID {
		t.Fatalf("replace made a new secret (%s -> %s)", created.ID, replaced.ID)
	}

	// Rename.
	status, body := e.doJSON(c, "PATCH", "/api/v1/projects/PAY/secrets/"+created.ID,
		`{"name":"GH_TOKEN"}`)
	if status != http.StatusOK || body["name"] != "GH_TOKEN" {
		t.Fatalf("rename = %d %v, want 200 with the new name", status, body)
	}

	// Delete → 204 and gone.
	if status, _ := e.doRaw(c, "DELETE", "/api/v1/projects/PAY/secrets/"+created.ID, ""); status != http.StatusNoContent {
		t.Fatalf("delete = %d, want 204", status)
	}
	status, raw = e.doRaw(c, "GET", "/api/v1/projects/PAY/secrets", "")
	if status != http.StatusOK || !bytes.Contains(raw, []byte(`"secrets":[]`)) {
		t.Fatalf("list after delete = %d %s, want an empty list", status, raw)
	}

	// Every mutation wrote an audit row, and no snapshot holds the value.
	entries, err := e.st.Audit().List(context.Background(), store.AuditFilter{})
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]bool{}
	for _, en := range entries {
		if strings.HasPrefix(en.Action, "secret.") {
			got[en.Action] = true
			if strings.Contains(string(en.Before), value) || strings.Contains(string(en.After), value) ||
				strings.Contains(string(en.Before), "replacement") || strings.Contains(string(en.After), "replacement") {
				t.Fatalf("audit entry %s snapshots a secret value", en.Action)
			}
		}
	}
	for _, want := range []string{"secret.set", "secret.rename", "secret.delete"} {
		if !got[want] {
			t.Errorf("no %s audit entry; every mutation must be audited", want)
		}
	}
}

// TestScopeIsolation: a project secret is not reachable through another project's path, and
// workspace routes are owner-only.
func TestScopeIsolation(t *testing.T) {
	e := newEnv(t)
	owner := e.owner()
	e.project(owner, "PAY")
	e.project(owner, "WEB")

	status, body := e.doJSON(owner, "POST", "/api/v1/projects/PAY/secrets",
		`{"name":"TOKEN_A","value":"a"}`)
	if status != http.StatusCreated {
		t.Fatalf("set = %d", status)
	}
	id := body["id"].(string)

	// The PAY secret is invisible to WEB's routes: rename and delete answer 404.
	if status, _ := e.doJSON(owner, "PATCH", "/api/v1/projects/WEB/secrets/"+id,
		`{"name":"STOLEN"}`); status != http.StatusNotFound {
		t.Fatalf("cross-project rename = %d, want 404", status)
	}
	if status, _ := e.doRaw(owner, "DELETE", "/api/v1/projects/WEB/secrets/"+id, ""); status != http.StatusNotFound {
		t.Fatalf("cross-project delete = %d, want 404", status)
	}

	// A project route cannot touch a workspace secret either.
	status, wbody := e.doJSON(owner, "POST", "/api/v1/workspace/secrets",
		`{"name":"SHARED_KEY","value":"w"}`)
	if status != http.StatusCreated {
		t.Fatalf("workspace set = %d", status)
	}
	wid := wbody["id"].(string)
	if status, _ := e.doRaw(owner, "DELETE", "/api/v1/projects/PAY/secrets/"+wid, ""); status != http.StatusNotFound {
		t.Fatalf("project delete of workspace secret = %d, want 404", status)
	}

	// Workspace secrets are owner-only; a plain member is refused.
	member := e.member(owner)
	if status, _ := e.doRaw(member, "GET", "/api/v1/workspace/secrets", ""); status != http.StatusForbidden {
		t.Fatalf("member workspace list = %d, want 403", status)
	}
	if status, _ := e.doJSON(member, "POST", "/api/v1/workspace/secrets",
		`{"name":"NOPE","value":"x"}`); status != http.StatusForbidden {
		t.Fatalf("member workspace set = %d, want 403", status)
	}
	// And a non-member of PAY cannot list its secrets.
	if status, _ := e.doRaw(member, "GET", "/api/v1/projects/PAY/secrets", ""); status != http.StatusForbidden {
		t.Fatalf("non-member project list = %d, want 403", status)
	}
}

// TestValidation: names must be env-var shaped, values required, duplicates rejected on
// rename.
func TestValidation(t *testing.T) {
	e := newEnv(t)
	c := e.owner()
	e.project(c, "PAY")

	for _, bad := range []string{
		`{"name":"lower_case","value":"x"}`,
		`{"name":"1STARTS_WITH_DIGIT","value":"x"}`,
		`{"name":"HAS SPACE","value":"x"}`,
		`{"name":"OK_NAME","value":""}`,
	} {
		if status, body := e.doJSON(c, "POST", "/api/v1/projects/PAY/secrets", bad); status != http.StatusBadRequest {
			t.Errorf("set %s = %d %v, want 400", bad, status, body)
		}
	}

	e.doJSON(c, "POST", "/api/v1/projects/PAY/secrets", `{"name":"FIRST","value":"1"}`)
	status, body := e.doJSON(c, "POST", "/api/v1/projects/PAY/secrets", `{"name":"SECOND","value":"2"}`)
	if status != http.StatusCreated {
		t.Fatalf("set SECOND = %d", status)
	}
	id := body["id"].(string)
	if status, body := e.doJSON(c, "PATCH", "/api/v1/projects/PAY/secrets/"+id,
		`{"name":"FIRST"}`); status != http.StatusBadRequest {
		t.Fatalf("rename onto taken name = %d %v, want 400", status, body)
	}
}
