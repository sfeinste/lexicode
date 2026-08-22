package harness

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"net/http/cgi"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

// GitHub is the fake forge: the REST endpoints the adapter and the poller call, plus REAL git
// smart-HTTP served by `git http-backend` as a CGI. ~30 lines of CGI wiring is what makes the
// whole path real — CloneURL → clone → push → OpenPullRequest, no stubs.
//
// Two things beyond S24's version, both needed by the S39 chain:
//
//   - the poller's read surface (pull listing and detail, reviews, review comments, issue
//     comments, check suites), so triggers fire through the REAL poller rather than through
//     injected events;
//   - CI. Every request re-reads each pull request's head from the bare repository; a head
//     that moved bumps the pull's updated_at (which is what makes the poller look at its
//     reviews again) and produces a completed check suite. [GitHub.FailNextCI] makes the next
//     such suite fail — the only lever the drivers need over CI.
//
// Every request is logged, method and path, which is how the "nothing but a human merges"
// assertion is made: the merge endpoint's call count, and whose token made the call.
type GitHub struct {
	Owner  string
	Name   string
	Branch string
	// Root is the parent directory of <owner>/<name>.git.
	Root string
	// Token is the repository credential. When set, the git smart-HTTP endpoints demand it
	// as HTTP basic auth (`x-access-token:<token>`) exactly as a private repository does —
	// which is what turns "the orchestrator owns the push" from an assertion about our own
	// code into an assertion the remote enforces. Leave it empty for an anonymous fixture.
	Token string
	// Logf receives one line per interesting mutation; nil means log.Printf.
	Logf func(string, ...any)

	mu          sync.Mutex
	prs         []*PullRequest
	reviews     []Review
	suites      []CheckSuite
	comments    []Comment
	requests    []Request
	failCI      int
	nextID      int64
	gitRefusals int
}

// fixtureTime is how this fixture serializes timestamps: RFC 3339 with a FIXED three digits of
// fractional seconds.
//
// github.com serializes to whole seconds, and this fixture used to as well — but github.com
// also cannot open a pull request in the same second a repository was connected, and this
// fixture routinely does: a whole six-run chain runs here in under a minute. Truncating to
// seconds threw away precision the fixture genuinely has, and a pull request created in the
// same second as the poller's baseline then serialized as OLDER than the baseline cursor
// (which keeps sub-second precision) and was never seen again — an intermittent hang in step 3
// that got much more likely once the agent stopped doing its own push and runs got shorter.
//
// Fixed-width fractions rather than time.RFC3339Nano: Nano drops trailing zeros, so its output
// is variable-width and sortByUpdatedDesc — which compares the strings — would order
// "…:32Z" after "…:32.500Z".
const fixtureTime = "2006-01-02T15:04:05.000Z07:00"

// PullRequest is one fake pull request. HeadSHA is refreshed from the bare repository on
// every request, so a push from a container is visible to the next poll without anybody
// telling the fixture about it.
type PullRequest struct {
	Number    int
	Title     string
	Body      string
	Head      string
	Base      string
	State     string
	Draft     bool
	Merged    bool
	Author    string
	HeadSHA   string
	CreatedAt time.Time
	UpdatedAt time.Time
}

// Review is one submitted review.
type Review struct {
	ID          int64
	PRNumber    int
	Author      string
	State       string // CHANGES_REQUESTED | COMMENTED
	Body        string
	SubmittedAt time.Time
}

// CheckSuite is one CI result for a head SHA.
type CheckSuite struct {
	ID         int64
	PRNumber   int
	HeadSHA    string
	HeadBranch string
	Conclusion string
	UpdatedAt  time.Time
}

// Comment is one issue/PR conversation comment.
type Comment struct {
	ID        int64
	PRNumber  int
	Author    string
	Body      string
	CreatedAt time.Time
}

// Request is one served HTTP request: enough to assert what the product did and did not call.
type Request struct {
	Method string
	Path   string
	// Token is the bearer credential the caller presented, verbatim. Fixture tokens only —
	// nothing here ever reaches a real service.
	Token string
	At    time.Time
}

func (g *GitHub) logf(format string, args ...any) {
	if g.Logf != nil {
		g.Logf(format, args...)
		return
	}
	log.Printf(format, args...)
}

// BareDir is the path of the bare repository git serves.
func (g *GitHub) BareDir() string { return filepath.Join(g.Root, g.Owner, g.Name+".git") }

func (g *GitHub) id() int64 {
	g.nextID++
	return 6000 + g.nextID
}

// ---------------------------------------------------------------- HTTP -----

// Handler returns the mux: REST under /repos/..., everything with ".git/" in it to
// git-http-backend.
func (g *GitHub) Handler() http.Handler {
	mux := http.NewServeMux()
	base := fmt.Sprintf("/repos/%s/%s", g.Owner, g.Name)

	mux.HandleFunc("GET "+base, g.wrap(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("X-OAuth-Scopes", "repo") // a classic PAT with the full repo scope
		writeJSON(w, map[string]any{
			"name": g.Name, "owner": map[string]any{"login": g.Owner},
			"default_branch": g.Branch, "private": true,
		})
	}))
	mux.HandleFunc("GET "+base+"/commits/{ref}", g.wrap(func(w http.ResponseWriter, r *http.Request) {
		sha, msg, author := g.commit(r.PathValue("ref"))
		writeJSON(w, map[string]any{
			"sha": sha,
			"commit": map[string]any{
				"message":   msg,
				"author":    map[string]any{"email": author, "name": author},
				"committer": map[string]any{"email": author, "name": author},
			},
		})
	}))
	mux.HandleFunc("GET "+base+"/commits/{sha}/check-suites", g.wrap(func(w http.ResponseWriter, r *http.Request) {
		sha := r.PathValue("sha")
		g.mu.Lock()
		out := []map[string]any{}
		for _, s := range g.suites {
			if s.HeadSHA != sha {
				continue
			}
			out = append(out, map[string]any{
				"id": s.ID, "head_sha": s.HeadSHA, "head_branch": s.HeadBranch,
				"status": "completed", "conclusion": s.Conclusion,
				"app":        map[string]any{"name": "CI"},
				"url":        fmt.Sprintf("https://github.example/checks/%d", s.ID),
				"updated_at": s.UpdatedAt.UTC().Format(fixtureTime),
			})
		}
		g.mu.Unlock()
		writeJSON(w, map[string]any{"total_count": len(out), "check_suites": out})
	}))

	mux.HandleFunc("GET "+base+"/pulls", g.wrap(func(w http.ResponseWriter, _ *http.Request) {
		g.mu.Lock()
		out := make([]map[string]any, 0, len(g.prs))
		for _, pr := range g.prs {
			out = append(out, prJSON(pr, false))
		}
		g.mu.Unlock()
		// The adapter asks for sort=updated&direction=desc and stops at the first PR older
		// than its cursor, so the order is part of the contract.
		sortByUpdatedDesc(out)
		writeJSON(w, out)
	}))
	mux.HandleFunc("POST "+base+"/pulls", g.wrap(g.createPull))
	mux.HandleFunc("GET "+base+"/pulls/{number}", g.wrap(func(w http.ResponseWriter, r *http.Request) {
		pr := g.byNumber(atoi(r.PathValue("number")))
		if pr == nil {
			http.NotFound(w, r)
			return
		}
		writeJSON(w, prJSON(pr, true))
	}))
	mux.HandleFunc("GET "+base+"/pulls/{number}/reviews", g.wrap(func(w http.ResponseWriter, r *http.Request) {
		number := atoi(r.PathValue("number"))
		g.mu.Lock()
		out := []map[string]any{}
		for _, rev := range g.reviews {
			if rev.PRNumber != number {
				continue
			}
			out = append(out, map[string]any{
				"id": rev.ID, "state": rev.State, "body": rev.Body,
				"user":         map[string]any{"login": rev.Author},
				"html_url":     fmt.Sprintf("https://github.example/pull/%d#review-%d", number, rev.ID),
				"submitted_at": rev.SubmittedAt.UTC().Format(fixtureTime),
			})
		}
		g.mu.Unlock()
		writeJSON(w, out)
	}))
	mux.HandleFunc("POST "+base+"/pulls/{number}/reviews", g.wrap(g.createReview))
	// Repository-wide comment listings (the poller passes number 0).
	mux.HandleFunc("GET "+base+"/pulls/comments", g.wrap(func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, []map[string]any{}) // no review comments in these fixtures
	}))
	mux.HandleFunc("GET "+base+"/issues/comments", g.wrap(func(w http.ResponseWriter, _ *http.Request) {
		g.mu.Lock()
		out := []map[string]any{}
		for _, c := range g.comments {
			out = append(out, map[string]any{
				"id": c.ID, "body": c.Body,
				"user":       map[string]any{"login": c.Author},
				"issue_url":  fmt.Sprintf("https://api.github.example/repos/%s/%s/issues/%d", g.Owner, g.Name, c.PRNumber),
				"html_url":   fmt.Sprintf("https://github.example/pull/%d#comment-%d", c.PRNumber, c.ID),
				"created_at": c.CreatedAt.UTC().Format(fixtureTime),
				"updated_at": c.CreatedAt.UTC().Format(fixtureTime),
			})
		}
		g.mu.Unlock()
		writeJSON(w, out)
	}))
	mux.HandleFunc("POST "+base+"/issues/{number}/comments", g.wrap(g.createComment))
	mux.HandleFunc("GET "+base+"/issues", g.wrap(func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, []map[string]any{})
	}))
	// Bootstrap doc detection probes a list of well-known paths; "not there" is the common
	// answer and is not an error (S15). Answering 404 quietly keeps the log readable.
	mux.HandleFunc("GET "+base+"/contents/", func(w http.ResponseWriter, r *http.Request) {
		g.record(r)
		w.WriteHeader(http.StatusNotFound)
		writeJSON(w, map[string]any{"message": "Not Found"})
	})
	// The merge endpoint exists so that calling it is *possible* — which is what makes
	// "no run ever called it" an assertion rather than a tautology (brief D6).
	mux.HandleFunc("PUT "+base+"/pulls/{number}/merge", g.wrap(g.merge))

	// git smart-HTTP: every /{owner}/{repo}.git/... path goes to the real `git
	// http-backend`. GIT_HTTP_EXPORT_ALL exports every repo under Root; receive-pack is
	// enabled in the bare repo's config so a container's push lands without HTTP auth.
	execPath, err := exec.Command("git", "--exec-path").Output()
	if err != nil {
		log.Fatalf("git --exec-path: %v", err)
	}
	backend := &cgi.Handler{
		Path: filepath.Join(strings.TrimSpace(string(execPath)), "git-http-backend"),
		Env:  []string{"GIT_PROJECT_ROOT=" + g.Root, "GIT_HTTP_EXPORT_ALL=1"},
	}
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, ".git/") {
			if !g.gitAuthorized(r) {
				g.mu.Lock()
				g.gitRefusals++
				g.mu.Unlock()
				g.logf("fakegithub: 401 %s %s (no valid git credential)", r.Method, r.URL.Path)
				w.Header().Set("WWW-Authenticate", `Basic realm="lexicode-fixture"`)
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			backend.ServeHTTP(w, r)
			return
		}
		g.record(r)
		g.logf("fakegithub: unhandled %s %s", r.Method, r.URL.Path)
		http.NotFound(w, r)
	})
	return mux
}

// wrap records the request and refreshes derived state (heads, CI) before every REST call, so
// a handler never serves a stale view of the repository a container just pushed to.
func (g *GitHub) wrap(h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		g.record(r)
		g.syncHeads()
		h(w, r)
	}
}

// gitAuthorized checks the credential on a git smart-HTTP request. Both spellings of the
// basic scheme are accepted: git's own URL-credential retry sends "Basic", while the
// `http.extraheader` form the orchestrator's push uses sends "basic" — the scheme token is
// case-insensitive (RFC 7235) and a fixture that only accepted one would be testing its own
// spelling rather than the mechanism.
func (g *GitHub) gitAuthorized(r *http.Request) bool {
	if g.Token == "" {
		return true
	}
	scheme, cred, ok := strings.Cut(r.Header.Get("Authorization"), " ")
	if !ok || !strings.EqualFold(scheme, "basic") {
		return false
	}
	raw, err := base64.StdEncoding.DecodeString(strings.TrimSpace(cred))
	if err != nil {
		return false
	}
	_, pass, ok := strings.Cut(string(raw), ":")
	return ok && pass == g.Token
}

// GitRefusals is how many git requests were turned away for want of a credential — the
// fixture's proof that the remote really is enforcing.
func (g *GitHub) GitRefusals() int {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.gitRefusals
}

func (g *GitHub) record(r *http.Request) {
	token := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	token = strings.TrimPrefix(token, "token ")
	g.mu.Lock()
	g.requests = append(g.requests, Request{
		Method: r.Method, Path: r.URL.Path, Token: token, At: time.Now(),
	})
	g.mu.Unlock()
}

// syncHeads re-reads each open pull request's head from the bare repository. A head that
// moved is a push: the pull's updated_at bumps (the poller's signal to re-read its reviews)
// and CI produces a completed check suite for the new head.
func (g *GitHub) syncHeads() {
	g.mu.Lock()
	defer g.mu.Unlock()
	for _, pr := range g.prs {
		if pr.State != "open" {
			continue
		}
		sha, _, _ := g.commitLocked(pr.Head)
		if sha == "" || sha == pr.HeadSHA {
			continue
		}
		pr.HeadSHA = sha
		pr.UpdatedAt = time.Now()
		conclusion := "success"
		if g.failCI > 0 {
			g.failCI--
			conclusion = "failure"
		}
		suite := CheckSuite{
			ID: g.id(), PRNumber: pr.Number, HeadSHA: sha, HeadBranch: pr.Head,
			Conclusion: conclusion, UpdatedAt: time.Now(),
		}
		g.suites = append(g.suites, suite)
		g.logf("fakegithub: PR #%d head → %s; CI %s (suite %d)",
			pr.Number, short(sha), conclusion, suite.ID)
	}
}

func (g *GitHub) createPull(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Title, Body, Head, Base string
		Draft                   bool
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	// A PR from a branch that was never pushed fails here exactly as on github.com.
	if !g.BranchExists(body.Head) {
		w.WriteHeader(http.StatusUnprocessableEntity)
		writeJSON(w, map[string]any{
			"message": "Validation Failed: head branch " + body.Head + " does not exist",
		})
		return
	}
	sha, _, _ := g.commit(body.Head)
	now := time.Now()
	g.mu.Lock()
	pr := &PullRequest{
		Number: len(g.prs) + 1, Title: body.Title, Body: body.Body,
		Head: body.Head, Base: body.Base, State: "open", Draft: body.Draft,
		Author: "lexicode[bot]", HeadSHA: sha, CreatedAt: now, UpdatedAt: now,
	}
	g.prs = append(g.prs, pr)
	g.mu.Unlock()
	g.logf("fakegithub: PR #%d opened: %q head=%s base=%s", pr.Number, pr.Title, pr.Head, pr.Base)
	w.WriteHeader(http.StatusCreated)
	writeJSON(w, prJSON(pr, true))
}

func (g *GitHub) createReview(w http.ResponseWriter, r *http.Request) {
	number := atoi(r.PathValue("number"))
	var body struct{ Body, Event string }
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	state := strings.ToUpper(body.Event)
	switch state {
	case "REQUEST_CHANGES":
		state = "CHANGES_REQUESTED"
	case "COMMENT":
		state = "COMMENTED"
	case "APPROVE":
		// The adapter must never send this; if it ever did, the fixture would accept it and
		// the driver's assertion would catch the regression.
		state = "APPROVED"
	}
	now := time.Now()
	g.mu.Lock()
	rev := Review{
		ID: g.id(), PRNumber: number, Author: "lexicode[bot]",
		State: state, Body: body.Body, SubmittedAt: now,
	}
	g.reviews = append(g.reviews, rev)
	// A submitted review bumps the pull request, which is the poller's cue to look at its
	// reviews again (architecture §7's documented assumption, made true here).
	if pr := g.prLocked(number); pr != nil {
		pr.UpdatedAt = now
	}
	g.mu.Unlock()
	g.logf("fakegithub: review %d submitted on PR #%d: %s", rev.ID, number, state)
	w.WriteHeader(http.StatusOK)
	writeJSON(w, map[string]any{
		"id": rev.ID, "state": rev.State, "body": rev.Body,
		"user":         map[string]any{"login": rev.Author},
		"html_url":     fmt.Sprintf("https://github.example/pull/%d#review-%d", number, rev.ID),
		"submitted_at": now.UTC().Format(fixtureTime),
	})
}

func (g *GitHub) createComment(w http.ResponseWriter, r *http.Request) {
	number := atoi(r.PathValue("number"))
	var body struct{ Body string }
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	now := time.Now()
	g.mu.Lock()
	c := Comment{ID: g.id(), PRNumber: number, Author: "lexicode[bot]", Body: body.Body, CreatedAt: now}
	g.comments = append(g.comments, c)
	if pr := g.prLocked(number); pr != nil {
		pr.UpdatedAt = now
	}
	g.mu.Unlock()
	w.WriteHeader(http.StatusCreated)
	writeJSON(w, map[string]any{
		"id": c.ID, "body": c.Body, "user": map[string]any{"login": c.Author},
		"created_at": now.UTC().Format(fixtureTime),
		"updated_at": now.UTC().Format(fixtureTime),
	})
}

// merge fast-forwards the base branch to the pull request's head in the bare repository — a
// real merge for the linear history these fixtures produce.
func (g *GitHub) merge(w http.ResponseWriter, r *http.Request) {
	number := atoi(r.PathValue("number"))
	pr := g.byNumber(number)
	if pr == nil {
		http.NotFound(w, r)
		return
	}
	out, err := exec.Command("git", "--git-dir", g.BareDir(),
		"update-ref", "refs/heads/"+pr.Base, pr.HeadSHA).CombinedOutput()
	if err != nil {
		http.Error(w, fmt.Sprintf("merge failed: %v\n%s", err, out), http.StatusInternalServerError)
		return
	}
	g.mu.Lock()
	pr.State, pr.Merged, pr.UpdatedAt = "closed", true, time.Now()
	g.mu.Unlock()
	g.logf("fakegithub: PR #%d merged into %s at %s", number, pr.Base, short(pr.HeadSHA))
	writeJSON(w, map[string]any{"merged": true, "sha": pr.HeadSHA})
}

// ---------------------------------------------------------------- reads for the driver -----

// PullRequests returns a snapshot of every pull request.
func (g *GitHub) PullRequests() []PullRequest {
	g.syncHeads()
	g.mu.Lock()
	defer g.mu.Unlock()
	out := make([]PullRequest, 0, len(g.prs))
	for _, pr := range g.prs {
		out = append(out, *pr)
	}
	return out
}

// Reviews returns a snapshot of every submitted review.
func (g *GitHub) Reviews() []Review {
	g.mu.Lock()
	defer g.mu.Unlock()
	return append([]Review(nil), g.reviews...)
}

// CheckSuites returns a snapshot of every check suite CI produced.
func (g *GitHub) CheckSuites() []CheckSuite {
	g.mu.Lock()
	defer g.mu.Unlock()
	return append([]CheckSuite(nil), g.suites...)
}

// Requests returns a snapshot of every request served.
func (g *GitHub) Requests() []Request {
	g.mu.Lock()
	defer g.mu.Unlock()
	return append([]Request(nil), g.requests...)
}

// MergeCalls returns every request made to a merge endpoint, whoever made it.
func (g *GitHub) MergeCalls() []Request {
	var out []Request
	for _, r := range g.Requests() {
		if strings.HasSuffix(r.Path, "/merge") {
			out = append(out, r)
		}
	}
	return out
}

// FailNextCI makes the next n check suites fail. CI is otherwise green.
func (g *GitHub) FailNextCI(n int) {
	g.mu.Lock()
	g.failCI = n
	g.mu.Unlock()
	g.logf("fakegithub: CI armed to fail the next %d suite(s)", n)
}

// MergeAsHuman performs the merge the way a human does it — a direct API call with a personal
// token that Lexicode has never seen. The product has no merge path at all (brief D6, the
// forge port has no Merge method), so this is the only way a pull request can close.
func (g *GitHub) MergeAsHuman(baseURL, token string, number int) error {
	url := fmt.Sprintf("%srepos/%s/%s/pulls/%d/merge", baseURL, g.Owner, g.Name, number)
	req, err := http.NewRequest(http.MethodPut, url, strings.NewReader(`{"merge_method":"merge"}`)) //nolint:noctx // fixture
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("merge returned HTTP %d", resp.StatusCode)
	}
	return nil
}

// ---------------------------------------------------------------- git -----

// commit returns a ref's sha, first message line and author email from the bare repository.
func (g *GitHub) commit(ref string) (sha, message, author string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.commitLocked(ref)
}

func (g *GitHub) commitLocked(ref string) (sha, message, author string) {
	dir := g.BareDir()
	shaOut, _ := exec.Command("git", "--git-dir", dir, "rev-parse", ref).Output()
	msgOut, _ := exec.Command("git", "--git-dir", dir, "log", "-1", "--format=%s", ref).Output()
	authorOut, _ := exec.Command("git", "--git-dir", dir, "log", "-1", "--format=%ae", ref).Output()
	return strings.TrimSpace(string(shaOut)), strings.TrimSpace(string(msgOut)),
		strings.TrimSpace(string(authorOut))
}

// BranchExists reports whether the bare repository has the branch.
func (g *GitHub) BranchExists(name string) bool {
	out, err := exec.Command("git", "--git-dir", g.BareDir(), "branch", "--list", name).Output()
	return err == nil && strings.TrimSpace(string(out)) != ""
}

// Branches renders `git branch --list` for failure messages.
func (g *GitHub) Branches() string {
	out, _ := exec.Command("git", "--git-dir", g.BareDir(), "branch", "--list").Output()
	return strings.TrimSpace(string(out))
}

// Head returns a branch's current sha.
func (g *GitHub) Head(ref string) string {
	sha, _, _ := g.commit(ref)
	return sha
}

// Tree lists every path a ref's commit carries, for asserting what an agent did — and did not
// — commit.
func (g *GitHub) Tree(ref string) (string, error) {
	out, err := exec.Command("git", "--git-dir", g.BareDir(), "ls-tree", "-r",
		"--name-only", ref).Output()
	if err != nil {
		return "", fmt.Errorf("git ls-tree %s: %w", ref, err)
	}
	return strings.TrimSpace(string(out)), nil
}

// FileAt returns one file's contents at a ref, for asserting what an agent actually wrote.
func (g *GitHub) FileAt(ref, path string) (string, error) {
	out, err := exec.Command("git", "--git-dir", g.BareDir(), "show", ref+":"+path).Output()
	if err != nil {
		return "", fmt.Errorf("git show %s:%s: %w", ref, path, err)
	}
	return string(out), nil
}

// InitBareRepo creates <Root>/<Owner>/<Name>.git with one commit on the default branch and
// push enabled, seeding it from a scratch working tree under workDir.
func (g *GitHub) InitBareRepo(workDir string) error {
	bare := g.BareDir()
	for _, argv := range [][]string{
		{"git", "init", "--bare", "--initial-branch=" + g.Branch, bare},
		{"git", "--git-dir", bare, "config", "http.receivepack", "true"},
	} {
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
printf 'export function charge(amount) {\n  return post("/charges", { amount });\n}\n' > src/charge.ts
git add -A
git commit -q -m "initial import"
git push -q %[3]s %[1]s
`, g.Branch, seed, bare)
	if out, err := exec.Command("/bin/sh", "-c", script).CombinedOutput(); err != nil {
		return fmt.Errorf("seeding bare repo: %v\n%s", err, out)
	}
	return nil
}

// ---------------------------------------------------------------- helpers -----

func (g *GitHub) byNumber(n int) *PullRequest {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.prLocked(n)
}

func (g *GitHub) prLocked(n int) *PullRequest {
	for _, pr := range g.prs {
		if pr.Number == n {
			return pr
		}
	}
	return nil
}

// prJSON renders a pull request. detail adds the size counters only the single-PR read
// carries on github.com, which is exactly the distinction the poller relies on.
func prJSON(pr *PullRequest, detail bool) map[string]any {
	out := map[string]any{
		"number": pr.Number, "title": pr.Title, "body": pr.Body,
		"state": pr.State, "draft": pr.Draft, "merged": pr.Merged,
		"user":       map[string]any{"login": pr.Author},
		"head":       map[string]any{"ref": pr.Head, "sha": pr.HeadSHA},
		"base":       map[string]any{"ref": pr.Base},
		"labels":     []any{},
		"html_url":   fmt.Sprintf("https://github.example/pull/%d", pr.Number),
		"created_at": pr.CreatedAt.UTC().Format(fixtureTime),
		"updated_at": pr.UpdatedAt.UTC().Format(fixtureTime),
	}
	if detail {
		out["additions"] = 12
		out["deletions"] = 3
		out["changed_files"] = 2
	}
	return out
}

func sortByUpdatedDesc(rows []map[string]any) {
	for i := 1; i < len(rows); i++ {
		for j := i; j > 0 && rows[j]["updated_at"].(string) > rows[j-1]["updated_at"].(string); j-- {
			rows[j], rows[j-1] = rows[j-1], rows[j]
		}
	}
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(v); err != nil {
		log.Printf("fakegithub: encode: %v", err)
	}
}

func atoi(s string) int {
	n, _ := strconv.Atoi(s)
	return n
}

func short(sha string) string {
	if len(sha) > 8 {
		return sha[:8]
	}
	return sha
}
