package bootstrap_test

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"testing"
)

// fakeGitHub is a hand-written GitHub REST fixture: a repository with files, open issues and a
// head commit, served the way go-github expects. The same shapes back the browser-verification
// helper; here they make "12 open issues → 12 checked candidates" a real assertion.
type fakeGitHub struct {
	srv    *httptest.Server
	owner  string
	repo   string
	branch string
	files  map[string]string // path → content
	issues []fakeIssue
	// rejectToken, when set, makes the repo read answer 401 for requests carrying this
	// token — the S37 rotate-token test's "bad new token" case.
	rejectToken string
}

type fakeIssue struct {
	Number int
	Title  string
	Body   string
	Author string
	Labels []string
}

func newFakeGitHub(t *testing.T, files map[string]string, issues []fakeIssue) *fakeGitHub {
	t.Helper()
	g := &fakeGitHub{owner: "acme", repo: "payments", branch: "main", files: files, issues: issues}
	mux := http.NewServeMux()
	base := fmt.Sprintf("/repos/%s/%s", g.owner, g.repo)

	mux.HandleFunc("GET "+base, func(w http.ResponseWriter, r *http.Request) {
		if g.rejectToken != "" && strings.Contains(r.Header.Get("Authorization"), g.rejectToken) {
			w.WriteHeader(http.StatusUnauthorized)
			writeJSON(w, map[string]any{"message": "Bad credentials"})
			return
		}
		w.Header().Set("X-OAuth-Scopes", "repo") // classic PAT with the full repo scope
		writeJSON(w, map[string]any{
			"name": g.repo, "owner": map[string]any{"login": g.owner},
			"default_branch": g.branch, "private": true,
		})
	})
	mux.HandleFunc("GET "+base+"/commits/"+g.branch, func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, map[string]any{
			"sha":    "abc123def456",
			"commit": map[string]any{"message": "Fix the flaky payment test\n\nDetails."},
		})
	})
	mux.HandleFunc("GET "+base+"/issues", func(w http.ResponseWriter, r *http.Request) {
		out := make([]map[string]any, 0, len(g.issues))
		for _, is := range g.issues {
			labels := make([]map[string]any, 0, len(is.Labels))
			for _, l := range is.Labels {
				labels = append(labels, map[string]any{"name": l})
			}
			out = append(out, map[string]any{
				"number": is.Number, "title": is.Title, "body": is.Body,
				"user":   map[string]any{"login": is.Author},
				"labels": labels,
				"html_url": fmt.Sprintf("https://github.example/%s/%s/issues/%d",
					g.owner, g.repo, is.Number),
				"created_at": "2026-08-01T00:00:00Z", "updated_at": "2026-08-02T00:00:00Z",
			})
		}
		writeJSON(w, out)
	})
	mux.HandleFunc("GET "+base+"/contents/{path...}", func(w http.ResponseWriter, r *http.Request) {
		path := r.PathValue("path")
		if content, ok := g.files[path]; ok {
			writeJSON(w, map[string]any{
				"type": "file", "encoding": "base64", "name": baseName(path), "path": path,
				"content": base64.StdEncoding.EncodeToString([]byte(content)),
			})
			return
		}
		// Directory listing: every file directly under path, plus one entry per child dir.
		var entries []map[string]any
		seenDirs := map[string]bool{}
		prefix := path + "/"
		for p := range g.files {
			if !strings.HasPrefix(p, prefix) {
				continue
			}
			rest := strings.TrimPrefix(p, prefix)
			if i := strings.IndexByte(rest, '/'); i >= 0 {
				dir := rest[:i]
				if !seenDirs[dir] {
					seenDirs[dir] = true
					entries = append(entries, map[string]any{
						"type": "dir", "name": dir, "path": prefix + dir,
					})
				}
				continue
			}
			entries = append(entries, map[string]any{
				"type": "file", "name": rest, "path": p,
			})
		}
		if len(entries) == 0 {
			w.WriteHeader(http.StatusNotFound)
			writeJSON(w, map[string]any{"message": "Not Found"})
			return
		}
		sort.Slice(entries, func(i, j int) bool {
			return entries[i]["path"].(string) < entries[j]["path"].(string)
		})
		writeJSON(w, entries)
	})

	g.srv = httptest.NewServer(mux)
	t.Cleanup(g.srv.Close)
	return g
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

func baseName(path string) string {
	if i := strings.LastIndexByte(path, '/'); i >= 0 {
		return path[i+1:]
	}
	return path
}

// fixtureFiles is the repository the tests connect: instruction docs at every detection point,
// two stacks, CI, and docs/** at depth 2.
func fixtureFiles() map[string]string {
	return map[string]string{
		"AGENTS.md":                       "# Working agreements\n\nAlways run make check.",
		"CLAUDE.md":                       "# Claude notes\n\nUse the Makefile.",
		"README.md":                       "# Payments\n\nA payment reconciliation service.\nIt matches ledger entries against bank statements.\n\n## Install\n\nnpm install",
		".github/copilot-instructions.md": "Use TypeScript strict mode.",
		".cursor/rules/frontend.mdc":      "---\ndescription: Frontend rules\nglobs: [\"web/**\", \"src/ui/**\"]\n---\nPrefer CSS modules.",
		".cursor/rules/general.mdc":       "---\ndescription: General\n---\nBe terse.",
		"docs/architecture.md":            "# Architecture\n\nHexagonal.",
		"docs/adr/001-sqlite.md":          "# ADR 1: SQLite\n\nBecause simple.",
		".github/workflows/ci.yml":        "name: ci\non: [push]\n",
		"go.mod":                          "module example.com/payments\n",
		"package.json":                    "{\"name\":\"payments\"}\n",
	}
}

// fixtureIssues returns n open issues, numbered 1..n.
func fixtureIssues(n int) []fakeIssue {
	out := make([]fakeIssue, 0, n)
	for i := 1; i <= n; i++ {
		is := fakeIssue{
			Number: i, Title: fmt.Sprintf("Issue %d", i),
			Body: fmt.Sprintf("Body of issue %d.", i), Author: "octocat",
		}
		if i%2 == 0 {
			is.Labels = []string{"bug"}
		}
		out = append(out, is)
	}
	return out
}
