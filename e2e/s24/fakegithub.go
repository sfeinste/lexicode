package main

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"net/http/cgi"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// fakeGitHub is the S15 fixture pattern grown a git backbone (the S24 story's decision (a)):
// the GitHub REST endpoints the forge adapter calls — repository read, head commit, PR
// create — plus REAL git smart-HTTP served by `git http-backend` as a CGI, so the container
// clones from and pushes to the same host the API calls hit. ~30 lines of CGI wiring makes
// the whole path real: CloneURL → clone → push → OpenPullRequest, no stubs.
type fakeGitHub struct {
	root   string // parent dir of <owner>/<repo>.git
	owner  string
	repo   string
	branch string

	mu  sync.Mutex
	prs []prRecord
}

type prRecord struct {
	Number int
	Title  string
	Body   string
	Head   string
	Base   string
}

func (g *fakeGitHub) bareDir() string {
	return filepath.Join(g.root, g.owner, g.repo+".git")
}

func (g *fakeGitHub) handler() http.Handler {
	mux := http.NewServeMux()
	base := fmt.Sprintf("/repos/%s/%s", g.owner, g.repo)

	mux.HandleFunc("GET "+base, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("X-OAuth-Scopes", "repo") // classic PAT with the full repo scope
		writeJSON(w, map[string]any{
			"name": g.repo, "owner": map[string]any{"login": g.owner},
			"default_branch": g.branch, "private": true,
		})
	})
	mux.HandleFunc("GET "+base+"/commits/{ref}", func(w http.ResponseWriter, r *http.Request) {
		sha, msg := g.head(r.PathValue("ref"))
		writeJSON(w, map[string]any{
			"sha":    sha,
			"commit": map[string]any{"message": msg},
		})
	})
	mux.HandleFunc("POST "+base+"/pulls", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Title string `json:"title"`
			Body  string `json:"body"`
			Head  string `json:"head"`
			Base  string `json:"base"`
			Draft bool   `json:"draft"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		// A PR from a branch that was never pushed is a real failure, like on github.com.
		if !g.branchExists(body.Head) {
			w.WriteHeader(http.StatusUnprocessableEntity)
			writeJSON(w, map[string]any{"message": "Validation Failed: head branch " + body.Head + " does not exist"})
			return
		}
		g.mu.Lock()
		number := len(g.prs) + 1
		g.prs = append(g.prs, prRecord{
			Number: number, Title: body.Title, Body: body.Body,
			Head: body.Head, Base: body.Base,
		})
		g.mu.Unlock()
		log.Printf("fakegithub: PR #%d opened: %q head=%s base=%s", number, body.Title, body.Head, body.Base)
		w.WriteHeader(http.StatusCreated)
		writeJSON(w, map[string]any{
			"number": number, "title": body.Title, "body": body.Body,
			"state": "open", "draft": body.Draft,
			"user":       map[string]any{"login": "lexicode[bot]"},
			"head":       map[string]any{"ref": body.Head, "sha": ""},
			"base":       map[string]any{"ref": body.Base},
			"html_url":   fmt.Sprintf("https://github.example/%s/%s/pull/%d", g.owner, g.repo, number),
			"created_at": time.Now().UTC().Format(time.RFC3339),
			"updated_at": time.Now().UTC().Format(time.RFC3339),
		})
	})

	// git smart-HTTP: every /{owner}/{repo}.git/... path goes to the real `git
	// http-backend`. GIT_HTTP_EXPORT_ALL exports every repo under root; receive-pack is
	// enabled in the bare repo's config so the agent's push lands without HTTP auth.
	execPath, err := exec.Command("git", "--exec-path").Output()
	if err != nil {
		log.Fatalf("git --exec-path: %v", err)
	}
	backend := &cgi.Handler{
		Path: filepath.Join(strings.TrimSpace(string(execPath)), "git-http-backend"),
		Env: []string{
			"GIT_PROJECT_ROOT=" + g.root,
			"GIT_HTTP_EXPORT_ALL=1",
		},
	}
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, ".git/") {
			backend.ServeHTTP(w, r)
			return
		}
		log.Printf("fakegithub: unhandled %s %s", r.Method, r.URL.Path)
		http.NotFound(w, r)
	})
	return mux
}

// head returns the ref's commit sha and first message line from the real bare repo.
func (g *fakeGitHub) head(ref string) (string, string) {
	sha, _ := exec.Command("git", "--git-dir", g.bareDir(), "rev-parse", ref).Output()
	msg, _ := exec.Command("git", "--git-dir", g.bareDir(), "log", "-1", "--format=%s", ref).Output()
	return strings.TrimSpace(string(sha)), strings.TrimSpace(string(msg))
}

func (g *fakeGitHub) branchExists(name string) bool {
	out, err := exec.Command("git", "--git-dir", g.bareDir(), "branch", "--list", name).Output()
	return err == nil && strings.TrimSpace(string(out)) != ""
}

func (g *fakeGitHub) branches() string {
	out, _ := exec.Command("git", "--git-dir", g.bareDir(), "branch", "--list").Output()
	return strings.TrimSpace(string(out))
}

func (g *fakeGitHub) openPRs() []prRecord {
	g.mu.Lock()
	defer g.mu.Unlock()
	return append([]prRecord(nil), g.prs...)
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(v); err != nil {
		log.Printf("fakegithub: encode: %v", err)
	}
}

// initBareRepo creates <root>/<owner>/<repo>.git with one commit on main and push enabled.
func (g *fakeGitHub) initBareRepo(workDir string) error {
	bare := g.bareDir()
	steps := [][]string{
		{"git", "init", "--bare", "--initial-branch=" + g.branch, bare},
		{"git", "--git-dir", bare, "config", "http.receivepack", "true"},
	}
	for _, argv := range steps {
		if out, err := exec.Command(argv[0], argv[1:]...).CombinedOutput(); err != nil {
			return fmt.Errorf("%v: %v\n%s", argv, err, out)
		}
	}
	seed := filepath.Join(workDir, "seed")
	script := fmt.Sprintf(`set -e
git init -q --initial-branch=%[1]s %[2]s
cd %[2]s
git config user.email seed@example.com
git config user.name Seed
echo "# payments" > README.md
mkdir -p src
echo "export function charge() {}" > src/charge.ts
git add -A
git commit -q -m "initial import"
git push -q %[3]s %[1]s
`, g.branch, seed, bare)
	if out, err := exec.Command("/bin/sh", "-c", script).CombinedOutput(); err != nil {
		return fmt.Errorf("seeding bare repo: %v\n%s", err, out)
	}
	return nil
}
