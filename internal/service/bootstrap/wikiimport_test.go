package bootstrap_test

import (
	"context"
	"net/http"
	"testing"
)

// S35 acceptance: the wiki import is re-runnable and idempotent on imported_from —
// importing twice does not duplicate, and the second preview marks the imported files.
func TestWikiImportPreviewAndIdempotentImport(t *testing.T) {
	e := newEnv(t, newFakeGitHub(t, fixtureFiles(), nil))
	c := e.owner()
	e.connect(c)

	// Preview: the detected docs, none imported yet, all checked.
	code, res := e.doJSON(c, "POST", "/api/v1/projects/PAY/wiki/import", `{"preview":true}`)
	if code != http.StatusOK {
		t.Fatalf("preview = %d: %v", code, res)
	}
	docs := res["docs"].([]any)
	if len(docs) == 0 {
		t.Fatalf("preview detected no docs")
	}
	byPath := map[string]map[string]any{}
	for _, raw := range docs {
		d := raw.(map[string]any)
		byPath[d["path"].(string)] = d
		if d["already_imported"].(bool) {
			t.Fatalf("fresh preview marks %v as imported", d["path"])
		}
	}
	if _, ok := byPath["AGENTS.md"]; !ok {
		t.Fatalf("AGENTS.md not detected: %v", byPath)
	}
	if byPath["AGENTS.md"]["proposed_scope"] != "always" {
		t.Fatalf("AGENTS.md proposed scope = %v, want always", byPath["AGENTS.md"]["proposed_scope"])
	}

	// Import a checked subset of two, one with a human-adjusted scope.
	code, res = e.doJSON(c, "POST", "/api/v1/projects/PAY/wiki/import",
		`{"files":[{"path":"AGENTS.md","scope":"always"},
		           {"path":"docs/architecture.md","scope":"manual"}]}`)
	if code != http.StatusOK {
		t.Fatalf("import = %d: %v", code, res)
	}
	if got := len(res["pages_created"].([]any)); got != 2 {
		t.Fatalf("pages created = %d, want 2: %v", got, res)
	}

	pid := e.projectID()
	pages, err := e.st.Wiki().ForProject(context.Background(), pid)
	if err != nil {
		t.Fatal(err)
	}
	if len(pages) != 2 {
		t.Fatalf("wiki pages = %d, want 2", len(pages))
	}
	for _, p := range pages {
		if p.ImportedFrom == nil || string(p.State) != "live" {
			t.Fatalf("imported page = %+v, want live with imported_from", p)
		}
	}

	// Second preview marks the two as already imported (unchecked, slug attached).
	code, res = e.doJSON(c, "POST", "/api/v1/projects/PAY/wiki/import", `{"preview":true}`)
	if code != http.StatusOK {
		t.Fatalf("second preview = %d", code)
	}
	marked := 0
	for _, raw := range res["docs"].([]any) {
		d := raw.(map[string]any)
		if d["already_imported"].(bool) {
			marked++
			if d["checked"].(bool) {
				t.Fatalf("already-imported %v still checked", d["path"])
			}
			if d["page_slug"] == "" {
				t.Fatalf("already-imported %v carries no slug", d["path"])
			}
		}
	}
	if marked != 2 {
		t.Fatalf("marked imported = %d, want 2", marked)
	}

	// Importing the same files again creates nothing — skipped, not duplicated.
	code, res = e.doJSON(c, "POST", "/api/v1/projects/PAY/wiki/import",
		`{"files":[{"path":"AGENTS.md","scope":"always"},
		           {"path":"docs/architecture.md","scope":"manual"}]}`)
	if code != http.StatusOK {
		t.Fatalf("re-import = %d: %v", code, res)
	}
	if got := len(res["pages_created"].([]any)); got != 0 {
		t.Fatalf("re-import created %d pages, want 0", got)
	}
	if got := len(res["docs_skipped"].([]any)); got != 2 {
		t.Fatalf("re-import skipped %d, want 2", got)
	}
	pages, err = e.st.Wiki().ForProject(context.Background(), pid)
	if err != nil || len(pages) != 2 {
		t.Fatalf("wiki pages after re-import = %d (err %v), want 2", len(pages), err)
	}

	// The import left audit entries.
	var n int
	if err := e.st.Reader().QueryRowContext(context.Background(),
		`SELECT COUNT(*) FROM audit_log WHERE action = 'wiki.import'`).Scan(&n); err != nil || n != 2 {
		t.Fatalf("wiki.import audit rows = %d (err %v), want 2", n, err)
	}
}

// A project with no connected repository gets the 404 problem, not a 500.
func TestWikiImportWithoutRepo(t *testing.T) {
	e := newEnv(t, newFakeGitHub(t, fixtureFiles(), nil))
	c := e.owner() // project PAY exists, no repo connected

	code, res := e.doJSON(c, "POST", "/api/v1/projects/PAY/wiki/import", `{"preview":true}`)
	if code != http.StatusNotFound {
		t.Fatalf("preview without repo = %d, want 404: %v", code, res)
	}
}
