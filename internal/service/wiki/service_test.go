package wiki_test

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
	"github.com/spruce/lexicode/internal/kernel/store"
	"github.com/spruce/lexicode/internal/service/projects"
	"github.com/spruce/lexicode/internal/service/wiki"
)

// env wires store + auth + audit + the projects and wiki services' routes, exactly as
// cmd/lexicode serves them.
type env struct {
	t   *testing.T
	st  *store.Store
	svc *wiki.Service
	srv *httptest.Server
}

func newEnv(t *testing.T) *env {
	t.Helper()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	st, err := store.Open(store.Options{Path: filepath.Join(t.TempDir(), "s33.db"), Logger: logger})
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
	svc := wiki.New(wiki.Options{Store: st, Audit: auditW, Logger: logger})
	svc.Routes(mux, authSvc)

	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return &env{t: t, st: st, svc: svc, srv: srv}
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

// createPage POSTs a page and returns its id.
func (e *env) createPage(c *http.Client, key, body string) (string, map[string]any) {
	e.t.Helper()
	status, v := e.doJSON(c, "POST", "/api/v1/projects/"+key+"/wiki", body)
	if status != http.StatusCreated {
		e.t.Fatalf("create page = %d, want 201: %v", status, v)
	}
	return v["id"].(string), v
}

// S33 acceptance: a page cannot be nested three deep — a third level is refused with the
// typed 409 wiki_depth_exceeded problem, both on create and on re-parent.
func TestThreeDeepNestingRejected(t *testing.T) {
	e := newEnv(t)
	c := e.owner()
	e.project(c, "PAY")

	rootID, _ := e.createPage(c, "PAY", `{"title":"Conventions"}`)
	childID, _ := e.createPage(c, "PAY",
		fmt.Sprintf(`{"title":"Error handling","parent_id":%q}`, rootID))

	// Third level on create: refused.
	status, v := e.doJSON(c, "POST", "/api/v1/projects/PAY/wiki",
		fmt.Sprintf(`{"title":"Retries","parent_id":%q}`, childID))
	if status != http.StatusConflict {
		t.Fatalf("three-deep create = %d, want 409: %v", status, v)
	}
	if v["type"] != "wiki_depth_exceeded" {
		t.Fatalf("problem type = %v, want wiki_depth_exceeded", v["type"])
	}

	// Re-parenting a page that has children under another page: refused too.
	otherID, _ := e.createPage(c, "PAY", `{"title":"Deploys"}`)
	status, v = e.doJSON(c, "PATCH", "/api/v1/wiki/"+rootID,
		fmt.Sprintf(`{"parent_id":%q}`, otherID))
	if status != http.StatusConflict || v["type"] != "wiki_depth_exceeded" {
		t.Fatalf("re-parent page-with-children = %d %v, want 409 wiki_depth_exceeded", status, v)
	}

	// And re-parenting under a second-level page: refused.
	status, v = e.doJSON(c, "PATCH", "/api/v1/wiki/"+otherID,
		fmt.Sprintf(`{"parent_id":%q}`, childID))
	if status != http.StatusConflict || v["type"] != "wiki_depth_exceeded" {
		t.Fatalf("re-parent under child = %d %v, want 409 wiki_depth_exceeded", status, v)
	}

	// A legal second level still works.
	e.createPage(c, "PAY", fmt.Sprintf(`{"title":"Rollbacks","parent_id":%q}`, otherID))
}

// Search finds a page by body text, and keeps finding it after the body changes (the FTS
// index is trigger-synced on update — the S15 index only covered inserts).
func TestSearchFindsBodyText(t *testing.T) {
	e := newEnv(t)
	c := e.owner()
	e.project(c, "PAY")

	id, _ := e.createPage(c, "PAY",
		`{"title":"Conventions","body":"Always use idempotency keys on the charge endpoint."}`)
	e.createPage(c, "PAY", `{"title":"Deploys","body":"Ship on Tuesdays."}`)

	status, v := e.doJSON(c, "GET", "/api/v1/projects/PAY/wiki/search?q=idempotency", "")
	if status != http.StatusOK {
		t.Fatalf("search = %d: %v", status, v)
	}
	results := v["results"].([]any)
	if len(results) != 1 {
		t.Fatalf("results = %d, want 1: %v", len(results), results)
	}
	hit := results[0].(map[string]any)
	if hit["id"] != id || hit["title"] != "Conventions" {
		t.Fatalf("hit = %v", hit)
	}
	if !strings.Contains(hit["body_snippet"].(string), "\x01idempotency\x02") {
		t.Fatalf("snippet lacks marked match: %q", hit["body_snippet"])
	}

	// Update the body; the old term must stop matching and the new one start.
	status, _ = e.doJSON(c, "PATCH", "/api/v1/wiki/"+id,
		`{"body":"Charge requests carry a dedupe token."}`)
	if status != http.StatusOK {
		t.Fatalf("patch = %d", status)
	}
	status, v = e.doJSON(c, "GET", "/api/v1/projects/PAY/wiki/search?q=idempotency", "")
	if status != http.StatusOK || len(v["results"].([]any)) != 0 {
		t.Fatalf("stale term still matches after update: %v", v["results"])
	}
	status, v = e.doJSON(c, "GET", "/api/v1/projects/PAY/wiki/search?q=dedupe", "")
	if status != http.StatusOK || len(v["results"].([]any)) != 1 {
		t.Fatalf("new term does not match after update: %v", v["results"])
	}

	// FTS metacharacters in user input must not error.
	status, _ = e.doJSON(c, "GET", "/api/v1/projects/PAY/wiki/search?q=%22charge%20-AND%20(", "")
	if status != http.StatusOK {
		t.Fatalf("quoted metacharacters = %d, want 200", status)
	}
}

// S33 acceptance: renaming a page updates inbound links (the stored labels of wiki tokens in
// other pages' bodies) and keeps backlink paragraphs correct (mention context re-derived
// after the rewrite).
func TestRenameRewritesInboundLabels(t *testing.T) {
	e := newEnv(t)
	c := e.owner()
	e.project(c, "PAY")

	targetID, _ := e.createPage(c, "PAY", `{"title":"API runbook"}`)
	sourceID, _ := e.createPage(c, "PAY", fmt.Sprintf(
		`{"title":"Oncall","body":"When paged, read @[API runbook](wiki:%s) first.\n\nThen escalate."}`,
		targetID))

	status, v := e.doJSON(c, "PATCH", "/api/v1/wiki/"+targetID, `{"title":"Payments runbook"}`)
	if status != http.StatusOK {
		t.Fatalf("rename = %d: %v", status, v)
	}
	if v["slug"] != "payments-runbook" {
		t.Fatalf("slug = %v, want payments-runbook", v["slug"])
	}

	// The source page's stored body now reads the new label; the id keeps the link.
	status, src := e.doJSON(c, "GET", "/api/v1/wiki/"+sourceID, "")
	if status != http.StatusOK {
		t.Fatalf("get source = %d", status)
	}
	body := src["page"].(map[string]any)["body"].(string)
	want := fmt.Sprintf("@[Payments runbook](wiki:%s)", targetID)
	if !strings.Contains(body, want) {
		t.Fatalf("source body not rewritten: %q", body)
	}
	if strings.Contains(body, "API runbook") {
		t.Fatalf("stale label survived: %q", body)
	}

	// The target's backlink paragraph reads the new label too.
	status, tgt := e.doJSON(c, "GET", "/api/v1/wiki/"+targetID, "")
	if status != http.StatusOK {
		t.Fatalf("get target = %d", status)
	}
	backlinks := tgt["backlinks"].([]any)
	if len(backlinks) != 1 {
		t.Fatalf("backlinks = %d, want 1: %v", len(backlinks), backlinks)
	}
	g := backlinks[0].(map[string]any)
	if g["source_kind"] != "wiki" || g["source_id"] != sourceID || g["title"] != "Oncall" {
		t.Fatalf("backlink group = %v", g)
	}
	para := g["paragraphs"].([]any)[0].(string)
	if para != "When paged, read "+want+" first." {
		t.Fatalf("backlink paragraph = %q", para)
	}
}

// S33 acceptance shape: versions append on content change only. A body edit appends; saving
// identical content mints nothing; front-matter-only edits mint nothing.
func TestVersionAppendOnContentChangeOnly(t *testing.T) {
	e := newEnv(t)
	c := e.owner()
	e.project(c, "PAY")

	id, _ := e.createPage(c, "PAY", `{"title":"Conventions","body":"v1 body"}`)
	version := func() float64 {
		status, v := e.doJSON(c, "GET", "/api/v1/wiki/"+id, "")
		if status != http.StatusOK {
			t.Fatalf("get = %d", status)
		}
		return v["version"].(float64)
	}
	if got := version(); got != 1 {
		t.Fatalf("initial version = %v, want 1", got)
	}

	// Content change → version 2.
	if status, _ := e.doJSON(c, "PATCH", "/api/v1/wiki/"+id, `{"body":"v2 body"}`); status != 200 {
		t.Fatalf("patch = %d", status)
	}
	if got := version(); got != 2 {
		t.Fatalf("after body change = %v, want 2", got)
	}

	// Identical content → no new version.
	if status, _ := e.doJSON(c, "PATCH", "/api/v1/wiki/"+id, `{"body":"v2 body","title":"Conventions"}`); status != 200 {
		t.Fatalf("patch = %d", status)
	}
	if got := version(); got != 2 {
		t.Fatalf("after no-op save = %v, want 2", got)
	}

	// Front-matter-only edits (scope, tags, verified_until) → no new version.
	if status, _ := e.doJSON(c, "PATCH", "/api/v1/wiki/"+id,
		`{"agent_scope":"always","tags":["conventions"],"verified_until":"2027-01-01"}`); status != 200 {
		t.Fatalf("patch = %d", status)
	}
	if got := version(); got != 2 {
		t.Fatalf("after front-matter edit = %v, want 2", got)
	}

	// Title change is a content change → version 3.
	if status, _ := e.doJSON(c, "PATCH", "/api/v1/wiki/"+id, `{"title":"House conventions"}`); status != 200 {
		t.Fatalf("patch = %d", status)
	}
	if got := version(); got != 3 {
		t.Fatalf("after title change = %v, want 3", got)
	}
}

// Unlinked mentions: a page whose plain text contains the target's title appears in the
// disclosure; text inside a mention token's label does not count.
func TestUnlinkedMentions(t *testing.T) {
	e := newEnv(t)
	c := e.owner()
	e.project(c, "PAY")

	targetID, _ := e.createPage(c, "PAY", `{"title":"API runbook"}`)
	plainID, _ := e.createPage(c, "PAY",
		`{"title":"Oncall","body":"Check the API runbook before paging anyone.\n\nUnrelated paragraph."}`)
	// This page only references the target through a real token — not unlinked.
	e.createPage(c, "PAY", fmt.Sprintf(
		`{"title":"Linked only","body":"See @[API runbook](wiki:%s)."}`, targetID))

	status, v := e.doJSON(c, "GET", "/api/v1/wiki/"+targetID, "")
	if status != http.StatusOK {
		t.Fatalf("get = %d", status)
	}
	unlinked := v["unlinked_mentions"].([]any)
	if len(unlinked) != 1 {
		t.Fatalf("unlinked = %d, want 1: %v", len(unlinked), unlinked)
	}
	u := unlinked[0].(map[string]any)
	if u["page_id"] != plainID || u["title"] != "Oncall" {
		t.Fatalf("unlinked = %v", u)
	}
	if u["paragraph"] != "Check the API runbook before paging anyone." {
		t.Fatalf("paragraph = %q", u["paragraph"])
	}
}

// Mentions in wiki bodies write rows with the full containing paragraph, and archive clears
// the page's outbound mentions.
func TestWikiMentionsAndArchive(t *testing.T) {
	e := newEnv(t)
	c := e.owner()
	e.project(c, "PAY")

	targetID, _ := e.createPage(c, "PAY", `{"title":"API runbook"}`)
	srcID, _ := e.createPage(c, "PAY", fmt.Sprintf(
		`{"title":"Oncall","body":"Intro.\n\nRead @[API runbook](wiki:%s) twice.\n\nOutro."}`, targetID))

	ms, err := e.st.Mentions().ForSource(context.Background(), "wiki", srcID)
	if err != nil {
		t.Fatal(err)
	}
	if len(ms) != 1 || ms[0].ToKind != "wiki" || ms[0].ToID != targetID {
		t.Fatalf("mentions = %+v", ms)
	}
	if ms[0].ContextText != fmt.Sprintf("Read @[API runbook](wiki:%s) twice.", targetID) {
		t.Fatalf("context = %q", ms[0].ContextText)
	}

	// Archive the source: outbound mentions are cleared, the target's backlinks empty.
	if status, _ := e.doJSON(c, "DELETE", "/api/v1/wiki/"+srcID, ""); status != http.StatusNoContent {
		t.Fatalf("archive = %d, want 204", status)
	}
	ms, err = e.st.Mentions().ForSource(context.Background(), "wiki", srcID)
	if err != nil {
		t.Fatal(err)
	}
	if len(ms) != 0 {
		t.Fatalf("mentions after archive = %+v", ms)
	}
	status, v := e.doJSON(c, "GET", "/api/v1/wiki/"+targetID, "")
	if status != http.StatusOK || len(v["backlinks"].([]any)) != 0 {
		t.Fatalf("backlinks after source archive = %v", v["backlinks"])
	}
	// And the archived page is out of the tree and search.
	status, v = e.doJSON(c, "GET", "/api/v1/projects/PAY/wiki", "")
	if status != http.StatusOK || len(v["pages"].([]any)) != 1 {
		t.Fatalf("tree after archive = %v", v["pages"])
	}
	status, v = e.doJSON(c, "GET", "/api/v1/projects/PAY/wiki/search?q=Oncall", "")
	if status != http.StatusOK || len(v["results"].([]any)) != 0 {
		t.Fatalf("search after archive = %v", v["results"])
	}
}

// Fractional drag ordering: PATCH position places a page between its siblings.
func TestFractionalReorder(t *testing.T) {
	e := newEnv(t)
	c := e.owner()
	e.project(c, "PAY")

	aID, a := e.createPage(c, "PAY", `{"title":"A"}`)
	_, b := e.createPage(c, "PAY", `{"title":"B"}`)
	_, cc := e.createPage(c, "PAY", `{"title":"C"}`)
	_ = aID
	posB := b["position"].(float64)
	posC := cc["position"].(float64)
	if !(a["position"].(float64) < posB && posB < posC) {
		t.Fatalf("creation order positions: %v %v %v", a["position"], posB, posC)
	}

	// Drag A between B and C.
	mid := (posB + posC) / 2
	status, moved := e.doJSON(c, "PATCH", "/api/v1/wiki/"+aID,
		fmt.Sprintf(`{"position":%g}`, mid))
	if status != http.StatusOK {
		t.Fatalf("move = %d", status)
	}
	if moved["position"].(float64) != mid {
		t.Fatalf("position = %v, want %v", moved["position"], mid)
	}

	status, v := e.doJSON(c, "GET", "/api/v1/projects/PAY/wiki", "")
	if status != http.StatusOK {
		t.Fatalf("list = %d", status)
	}
	var order []string
	for _, p := range v["pages"].([]any) {
		order = append(order, p.(map[string]any)["title"].(string))
	}
	if strings.Join(order, ",") != "B,A,C" {
		t.Fatalf("tree order = %v, want B,A,C", order)
	}
}

// seedPages bulk-creates n live pages straight through the store (one transaction) — the
// performance corpus.
func seedPages(t *testing.T, st *store.Store, projectID string, n int) {
	t.Helper()
	err := st.Tx(context.Background(), func(tx *store.Tx) error {
		for i := 0; i < n; i++ {
			body := fmt.Sprintf(
				"Page %d covers deployment steps, retries and the flux capacitor of area %d. "+
					strings.Repeat("Conventions matter; prefer boring technology. ", 20), i, i)
			p := domain.WikiPage{
				ID: domain.NewID(), ProjectID: projectID,
				Slug: fmt.Sprintf("seed-%d", i), Title: fmt.Sprintf("Seed page %d", i),
				Position: float64(i), AgentScope: domain.ScopeAuto,
				ScopePaths: []string{}, Tags: []string{}, Body: body,
				State: domain.WikiLive, CreatedAt: domain.Now(), UpdatedAt: domain.Now(),
			}
			if err := tx.Wiki().CreatePage(context.Background(), &p); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}
