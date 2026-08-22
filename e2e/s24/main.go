// Command s24 is the S24 end-to-end acceptance harness (plan §S24): the full fixture
// stack — real binary, real Docker container, real git over HTTP, real MCP — with the two
// external services faked at their network edges:
//
//   - GitHub is a local server implementing the REST endpoints the forge adapter calls plus
//     git smart-HTTP via `git http-backend` (fakegithub.go), reached through
//     --github-base-url. The container clones from and pushes to it; the orchestrator opens
//     the PR on it through the real forge adapter.
//   - `claude` is a scripted bash stand-in (fakeclaude.go) baked into a derived agent image
//     (FROM the built-in base image) at /usr/local/bin/claude; repos.image_ref points the
//     runtime at it. It speaks real stream-json and calls the real MCP server.
//
// Modes:
//
//	-mode run   the scripted acceptance: setup → project → repo → ticket+criteria → agent →
//	            delegate → provisioning checklist → ask_human parks → escalation
//	            notification (>60s) → answer → resume → push → PR (with the D-9 marker) →
//	            ticket in the review column → cost recorded; then a second run stopped
//	            midway → branch preserved on the remote. Exits 0 on success.
//	-mode hold  the same stack, but the agent runs the interactive script (question, then
//	            approval, then a slow loop) and everything is left running for a browser
//	            walkthrough. Ctrl-C tears it down.
//
// Invoke through scripts/s24-e2e.sh, which builds the binary first.
package main

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/http/cookiejar"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	_ "modernc.org/sqlite"
)

const (
	derivedImage = "lexicode/s24-e2e-agent:latest"
	userEmail    = "e2e@example.com"
	userPassword = "correct horse battery staple"
	repoToken    = "e2e-fixture-token-1234567890"
)

func main() {
	log.SetFlags(log.Ltime)
	mode := flag.String("mode", "run", "run (scripted acceptance) or hold (leave running for a browser walkthrough)")
	port := flag.Int("port", 7789, "lexicode API port")
	proxyPort := flag.Int("proxy-port", 7788, "lexicode egress-proxy port")
	ghPort := flag.Int("gh-port", 7790, "fake GitHub port (must be reachable from containers)")
	flag.Parse()

	if err := run(*mode, *port, *proxyPort, *ghPort); err != nil {
		log.Fatalf("FAIL: %v", err)
	}
	if *mode == "run" {
		fmt.Println("\nPASS: S24 end-to-end acceptance complete")
	}
}

func run(mode string, port, proxyPort, ghPort int) error {
	repoRoot, err := findRepoRoot()
	if err != nil {
		return err
	}
	work, err := os.MkdirTemp("", "lexicode-s24-*")
	if err != nil {
		return err
	}
	log.Printf("work dir: %s", work)

	hostIP, err := detectHostIP()
	if err != nil {
		return err
	}
	log.Printf("host IP (reachable from host and containers): %s", hostIP)

	// -- fake GitHub: REST + git smart-HTTP over one listener on all interfaces ----------
	gh := &fakeGitHub{root: filepath.Join(work, "git"), owner: "acme", repo: "payments", branch: "main"}
	if err := os.MkdirAll(filepath.Join(gh.root, gh.owner), 0o755); err != nil {
		return err
	}
	if err := gh.initBareRepo(work); err != nil {
		return err
	}
	ghAddr := fmt.Sprintf("0.0.0.0:%d", ghPort)
	ghSrv := &http.Server{Addr: ghAddr, Handler: gh.handler(), ReadHeaderTimeout: 10 * time.Second}
	go func() {
		if err := ghSrv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("fake github: %v", err)
		}
	}()
	defer func() { _ = ghSrv.Close() }()
	ghBase := fmt.Sprintf("http://%s:%d/", hostIP, ghPort)
	log.Printf("fake GitHub (REST + git http-backend): %s", ghBase)

	// -- agent image: base (built-in tag) + the fake claude baked in ---------------------
	if err := ensureImages(repoRoot); err != nil {
		return err
	}

	// -- the binary, on a fresh data dir -------------------------------------------------
	bin := filepath.Join(repoRoot, "lexicode")
	if _, err := os.Stat(bin); err != nil {
		return fmt.Errorf("binary %s not found; run `make build` first (scripts/s24-e2e.sh does)", bin)
	}
	dataDir := filepath.Join(work, "data")
	serve := exec.Command(bin, "serve",
		"--data-dir", dataDir,
		"--host", "127.0.0.1",
		"--port", fmt.Sprint(port),
		"--proxy-port", fmt.Sprint(proxyPort),
		"--github-base-url", ghBase,
		"--log-level", "info",
	)
	serve.Env = append(os.Environ(),
		"LEXICODE_OPEN_BROWSER=false",
		"CLAUDE_CODE_OAUTH_TOKEN=sk-ant-oat01-e2e-fixture-token",
	)
	serve.Stdout = prefixWriter("lexicode| ")
	serve.Stderr = prefixWriter("lexicode| ")
	if err := serve.Start(); err != nil {
		return err
	}
	defer func() {
		_ = serve.Process.Signal(syscall.SIGTERM)
		_, _ = serve.Process.Wait()
	}()

	base := fmt.Sprintf("http://127.0.0.1:%d", port)
	c, err := newClient(base)
	if err != nil {
		return err
	}
	if err := waitFor("server up", 30*time.Second, func() (bool, error) {
		resp, err := http.Get(base + "/api/v1/auth/me") //nolint:noctx // fixture poll
		if err != nil {
			return false, nil
		}
		_ = resp.Body.Close()
		return resp.StatusCode == http.StatusUnauthorized, nil
	}); err != nil {
		return err
	}

	// -- setup → project → repo → agent → ticket (all through the real API) --------------
	log.Printf("== setup and project ==")
	if _, err := c.do("POST", "/api/v1/auth/setup", map[string]any{
		"email": userEmail, "display_name": "E2E", "password": userPassword,
	}, 201); err != nil {
		return err
	}
	if _, err := c.do("POST", "/api/v1/projects", map[string]any{
		"key": "PAY", "name": "Payments",
	}, 201); err != nil {
		return err
	}
	if _, err := c.do("POST", "/api/v1/projects/PAY/repo", map[string]any{
		"owner": gh.owner, "name": gh.repo, "token": repoToken,
	}, 200, 201); err != nil {
		return err
	}
	log.Printf("repo connected through the real forge adapter against the fake GitHub")

	// The two repo settings without an API surface yet (image_ref is a project-settings
	// field in the S37 polish pass): written straight to the store, documented in the
	// report. network 'open' keeps the fixture on the bridge network where both the fake
	// GitHub and the MCP endpoint are reachable without the egress proxy in the path.
	if err := setRepoSettings(filepath.Join(dataDir, "lexicode.db"), derivedImage); err != nil {
		return err
	}
	log.Printf("repos row: image_ref=%s network_policy=open", derivedImage)

	autonomy := "auto"
	if mode == "hold" {
		autonomy = "approve_each" // so request_approval parks for the walkthrough
	}
	agent, err := c.do("POST", "/api/v1/projects/PAY/agents", map[string]any{
		"name": "Dev", "role": "Implements tickets", "model": "claude-sonnet-5",
		"effort": "medium", "autonomy": autonomy,
		"directive": "You are Dev. Implement the ticket and push your branch.",
	}, 201)
	if err != nil {
		return err
	}
	agentID := agent["id"].(string)
	if _, err := c.do("PATCH", "/api/v1/agents/"+agentID, map[string]any{
		"permissions": map[string]bool{
			"read_files": true, "edit_files": true, "run_commands": true,
			"push_branches": true, "open_prs": true,
		},
	}, 200); err != nil {
		return err
	}

	ticket, err := c.do("POST", "/api/v1/projects/PAY/tickets", map[string]any{
		"title":       "Add idempotency keys to the charge API",
		"description": "Duplicate webhooks double-charge. Add idempotency keys.",
	}, 201)
	if err != nil {
		return err
	}
	ticketID := ticket["id"].(string)
	for _, criterion := range []string{
		"Replayed webhooks do not double-charge",
		"Keys expire after 24h",
	} {
		if _, err := c.do("POST", "/api/v1/tickets/"+ticketID+"/criteria",
			map[string]any{"text": criterion}, 200, 201); err != nil {
			return err
		}
	}
	log.Printf("ticket %v created with 2 acceptance criteria", ticket["key"])

	// -- delegate ------------------------------------------------------------------------
	log.Printf("== delegate ==")
	prompt := ""
	if mode == "hold" {
		prompt = "E2E-MODE: interactive"
	}
	del, err := c.do("POST", "/api/v1/tickets/"+ticketID+"/delegate",
		map[string]any{"agent_id": agentID, "prompt": prompt}, 201)
	if err != nil {
		return err
	}
	runID := del["run_id"].(string)
	log.Printf("run %s enqueued", runID)

	if mode == "hold" {
		fmt.Printf("\n== HOLD MODE ==\n")
		fmt.Printf("dashboard:  %s\n", base)
		fmt.Printf("login:      %s / %s\n", userEmail, userPassword)
		fmt.Printf("run:        %s/p/PAY/runs/%s\n", base, runID)
		fmt.Printf("The agent will ask a question, then request an approval, then loop\n")
		fmt.Printf("slowly — answer, approve, steer, stop or take over from the UI.\n")
		fmt.Printf("Ctrl-C tears everything down.\n\n")
		sig := make(chan os.Signal, 1)
		signal.Notify(sig, os.Interrupt, syscall.SIGTERM)
		<-sig
		return nil
	}

	// -- provisioning checklist (interaction rule 7: a checklist, never a spinner) -------
	if err := waitFor("provisioning checklist activities", 5*time.Minute, func() (bool, error) {
		acts, err := c.activities(runID)
		if err != nil {
			return false, err
		}
		ok := map[string]bool{}
		for _, a := range acts {
			if a.Type == "provision" && a.OK != nil && *a.OK {
				ok[a.Title] = true
			}
		}
		return ok["container"] && ok["clone"], nil
	}); err != nil {
		return err
	}
	log.Printf("provisioning checklist recorded (container ✓, clone ✓)")

	// -- ask_human parks the run ---------------------------------------------------------
	if err := waitFor("run parked needs_input", 5*time.Minute, func() (bool, error) {
		return c.runState(runID) == "needs_input", nil
	}); err != nil {
		return err
	}
	log.Printf("run parked: needs_input (ask_human through the real MCP server)")

	// -- escalation: unanswered >60s → notification for the delegating human -------------
	log.Printf("waiting for the 60s escalation notification (interaction rule 11)…")
	if err := waitFor("escalation notification", 3*time.Minute, func() (bool, error) {
		body, err := c.do("GET", "/api/v1/notifications", nil, 200)
		if err != nil {
			return false, err
		}
		unread, _ := body["unread"].(float64)
		return unread >= 1, nil
	}); err != nil {
		return err
	}
	notif, _ := c.do("GET", "/api/v1/notifications", nil, 200)
	log.Printf("escalation notification arrived: %s", compact(notif["notifications"]))

	// The needs-you surfaces show the row, flavor in words.
	needs, err := c.do("GET", "/api/v1/projects/PAY/runs?view=needs_you", nil, 200)
	if err != nil {
		return err
	}
	rows, _ := needs["runs"].([]any)
	if len(rows) != 1 {
		return fmt.Errorf("needs_you rows = %s, want exactly the parked run", compact(needs))
	}
	if flavor := rows[0].(map[string]any)["flavor"]; flavor != "question" {
		return fmt.Errorf("needs_you flavor = %v, want question", flavor)
	}
	log.Printf("needs-you view: 1 row, flavor=question")

	// -- answer from the API (the run detail's UI posts exactly this) --------------------
	elID, err := c.pendingElicitation(runID)
	if err != nil {
		return err
	}
	if _, err := c.do("POST", "/api/v1/elicitations/"+elID+"/respond", map[string]any{
		"action":  "answer",
		"answers": map[string][]string{"Which storage should the idempotency keys use?": {"Postgres"}},
	}, 200); err != nil {
		return err
	}
	log.Printf("question answered (Postgres); run resumes")

	// -- completion: pushed branch, opened PR, ticket in review, cost recorded -----------
	if err := waitFor("run completed", 5*time.Minute, func() (bool, error) {
		st := c.runState(runID)
		if st == "failed" || st == "timed_out" || st == "canceled" {
			return false, fmt.Errorf("run ended %s: %s", st, compact(c.lastRun))
		}
		return st == "completed", nil
	}); err != nil {
		return err
	}
	runRow, err := c.do("GET", "/api/v1/runs/"+runID, nil, 200)
	if err != nil {
		return err
	}
	runBody := runRow["run"].(map[string]any)
	branch, _ := runBody["branch"].(string)
	cost, _ := runBody["cost_cents"].(float64)
	if cost <= 0 {
		return fmt.Errorf("run completed without a cost figure: %s", compact(runBody))
	}
	log.Printf("run completed: branch=%s cost=%d¢", branch, int(cost))

	if !gh.branchExists(branch) {
		return fmt.Errorf("branch %q not on the fake remote; branches:\n%s", branch, gh.branches())
	}
	log.Printf("pushed branch exists on the fake remote:\n%s", gh.branches())

	prs := gh.openPRs()
	if len(prs) != 1 {
		return fmt.Errorf("fake GitHub has %d PRs, want 1", len(prs))
	}
	if prs[0].Head != branch {
		return fmt.Errorf("PR head = %q, want %q", prs[0].Head, branch)
	}
	if !strings.Contains(prs[0].Body, "<!-- lexicode:actor=agent:") {
		return fmt.Errorf("PR body lacks the D-9 actor marker:\n%s", prs[0].Body)
	}
	log.Printf("PR #%d opened on the fake GitHub: %q (head=%s, D-9 marker present)",
		prs[0].Number, prs[0].Title, prs[0].Head)

	var prOutput bool
	for _, o := range runRow["outputs"].([]any) {
		if o.(map[string]any)["kind"] == "pull_request" {
			prOutput = true
		}
	}
	if !prOutput {
		return fmt.Errorf("run outputs lack a pull_request row: %s", compact(runRow["outputs"]))
	}

	cat, err := c.ticketColumnCategory(ticketID)
	if err != nil {
		return err
	}
	if cat != "review" {
		return fmt.Errorf("ticket column category = %q, want review", cat)
	}
	log.Printf("ticket moved to the review-category column")

	// -- second run, stopped midway: the branch still exists (§10.5) ---------------------
	log.Printf("== second run, stopped midway ==")
	del2, err := c.do("POST", "/api/v1/tickets/"+ticketID+"/delegate",
		map[string]any{"agent_id": agentID, "prompt": "E2E-MODE: stall"}, 201)
	if err != nil {
		return err
	}
	run2 := del2["run_id"].(string)
	if err := waitFor("second run mid-work", 5*time.Minute, func() (bool, error) {
		row, err := c.do("GET", "/api/v1/runs/"+run2, nil, 200)
		if err != nil {
			return false, err
		}
		r := row["run"].(map[string]any)
		return r["state"] == "running" && r["current_step"] == "working slowly on purpose", nil
	}); err != nil {
		return err
	}
	log.Printf("second run is mid-work; stopping it")
	if _, err := c.do("POST", "/api/v1/runs/"+run2+"/stop",
		map[string]any{"reason": "stopped midway by the e2e"}, 200); err != nil {
		return err
	}
	if err := waitFor("second run canceled", 2*time.Minute, func() (bool, error) {
		return c.runState(run2) == "canceled", nil
	}); err != nil {
		return err
	}
	row2, err := c.do("GET", "/api/v1/runs/"+run2, nil, 200)
	if err != nil {
		return err
	}
	branch2, _ := row2["run"].(map[string]any)["branch"].(string)
	var partial bool
	for _, o := range row2["outputs"].([]any) {
		if o.(map[string]any)["kind"] == "partial_work" {
			partial = true
		}
	}
	if !partial {
		return fmt.Errorf("stopped run has no partial_work output: %s", compact(row2["outputs"]))
	}
	if !gh.branchExists(branch2) {
		return fmt.Errorf("stopped run's branch %q not on the fake remote; branches:\n%s",
			branch2, gh.branches())
	}
	log.Printf("stopped run: canceled, partial_work recorded, branch %q preserved on the remote:\n%s",
		branch2, gh.branches())
	return nil
}

// ---------------------------------------------------------------- api client -----

type client struct {
	base    string
	http    *http.Client
	lastRun map[string]any
}

func newClient(base string) (*client, error) {
	jar, err := cookiejar.New(nil)
	if err != nil {
		return nil, err
	}
	return &client{base: base, http: &http.Client{Jar: jar, Timeout: 30 * time.Second}}, nil
}

func (c *client) do(method, path string, body any, wantStatus ...int) (map[string]any, error) {
	var rd io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			return nil, err
		}
		rd = bytes.NewReader(raw)
	}
	req, err := http.NewRequest(method, c.base+path, rd) //nolint:noctx // fixture driver
	if err != nil {
		return nil, err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("%s %s: %w", method, path, err)
	}
	defer func() { _ = resp.Body.Close() }()
	raw, _ := io.ReadAll(resp.Body)
	okStatus := false
	for _, want := range wantStatus {
		if resp.StatusCode == want {
			okStatus = true
		}
	}
	if !okStatus {
		return nil, fmt.Errorf("%s %s = %d, want %v: %s", method, path, resp.StatusCode, wantStatus, raw)
	}
	out := map[string]any{}
	if len(bytes.TrimSpace(raw)) > 0 {
		if err := json.Unmarshal(raw, &out); err != nil {
			return nil, fmt.Errorf("%s %s: not JSON: %s", method, path, raw)
		}
	}
	return out, nil
}

func (c *client) runState(id string) string {
	row, err := c.do("GET", "/api/v1/runs/"+id, nil, 200)
	if err != nil {
		return ""
	}
	r, _ := row["run"].(map[string]any)
	c.lastRun = r
	st, _ := r["state"].(string)
	return st
}

type activityRow struct {
	Type  string `json:"type"`
	Title string `json:"title"`
	OK    *bool  `json:"ok"`
}

func (c *client) activities(runID string) ([]activityRow, error) {
	body, err := c.do("GET", "/api/v1/runs/"+runID+"/activities", nil, 200)
	if err != nil {
		return nil, err
	}
	raw, err := json.Marshal(body["activities"])
	if err != nil {
		return nil, err
	}
	var out []activityRow
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, err
	}
	return out, nil
}

func (c *client) pendingElicitation(runID string) (string, error) {
	row, err := c.do("GET", "/api/v1/runs/"+runID, nil, 200)
	if err != nil {
		return "", err
	}
	els, _ := row["elicitations"].([]any)
	for _, e := range els {
		el := e.(map[string]any)
		if el["state"] == "pending" {
			return el["id"].(string), nil
		}
	}
	return "", fmt.Errorf("no pending elicitation on run %s: %s", runID, compact(els))
}

func (c *client) ticketColumnCategory(ticketID string) (string, error) {
	tk, err := c.do("GET", "/api/v1/tickets/"+ticketID, nil, 200)
	if err != nil {
		return "", err
	}
	columnID, _ := tk["column_id"].(string)
	cols, err := c.do("GET", "/api/v1/projects/PAY/columns", nil, 200)
	if err != nil {
		return "", err
	}
	for _, colAny := range cols["columns"].([]any) {
		col := colAny.(map[string]any)
		if col["id"] == columnID {
			cat, _ := col["category"].(string)
			return cat, nil
		}
	}
	return "", fmt.Errorf("ticket column %q not in the column list", columnID)
}

// ---------------------------------------------------------------- fixtures -----

// setRepoSettings writes image_ref and network_policy straight to the repos row. The server
// is running; SQLite WAL plus busy_timeout make a second-process write safe.
func setRepoSettings(dbPath, imageRef string) error {
	db, err := sql.Open("sqlite", dbPath+"?_pragma=busy_timeout(5000)")
	if err != nil {
		return err
	}
	defer func() { _ = db.Close() }()
	res, err := db.Exec(
		`UPDATE repos SET image_ref = ?, network_policy = 'open'
		 WHERE project_id = (SELECT id FROM projects WHERE key = 'PAY')`, imageRef)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n != 1 {
		return fmt.Errorf("repos settings update touched %d rows, want 1", n)
	}
	return nil
}

// ensureImages makes sure the built-in agent base image exists (building it from the
// embedded Dockerfile's source if not) and bakes the fake claude into the derived image.
func ensureImages(repoRoot string) error {
	dockerfilePath := filepath.Join(repoRoot, "internal", "module", "docker", "Dockerfile")
	raw, err := os.ReadFile(dockerfilePath)
	if err != nil {
		return err
	}
	baseTag := "lexicode/agent-base:" + sha256Hex(raw)[:12]

	if !imageExists(baseTag) {
		log.Printf("building the agent base image %s (first run can take minutes)…", baseTag)
		cmd := exec.Command("docker", "build", "-t", baseTag,
			"-f", dockerfilePath, filepath.Dir(dockerfilePath))
		cmd.Stdout = prefixWriter("docker| ")
		cmd.Stderr = prefixWriter("docker| ")
		if err := cmd.Run(); err != nil {
			return fmt.Errorf("building base image: %w", err)
		}
	} else {
		log.Printf("agent base image %s present", baseTag)
	}

	ctxDir, err := os.MkdirTemp("", "s24-image-*")
	if err != nil {
		return err
	}
	defer func() { _ = os.RemoveAll(ctxDir) }()
	if err := os.WriteFile(filepath.Join(ctxDir, "claude"), []byte(fakeClaude), 0o755); err != nil { //nolint:gosec // an executable fixture script
		return err
	}
	dockerfile := "FROM " + baseTag + "\nCOPY --chmod=0755 claude /usr/local/bin/claude\n"
	if err := os.WriteFile(filepath.Join(ctxDir, "Dockerfile"), []byte(dockerfile), 0o644); err != nil { //nolint:gosec // fixture
		return err
	}
	cmd := exec.Command("docker", "build", "-t", derivedImage, ctxDir)
	cmd.Stdout = prefixWriter("docker| ")
	cmd.Stderr = prefixWriter("docker| ")
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("building derived image: %w", err)
	}
	log.Printf("derived agent image %s built (fake claude at /usr/local/bin/claude)", derivedImage)
	return nil
}

func imageExists(tag string) bool {
	return exec.Command("docker", "image", "inspect", tag).Run() == nil
}

// ---------------------------------------------------------------- helpers -----

func findRepoRoot() (string, error) {
	dir, err := os.Getwd()
	if err != nil {
		return "", err
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", fmt.Errorf("go.mod not found above %s; run from inside the repo", dir)
		}
		dir = parent
	}
}

// detectHostIP finds a non-loopback IPv4 the Docker VM can dial — the fake GitHub must be
// reachable both from this process (forge API calls) and from inside containers (git).
func detectHostIP() (string, error) {
	for _, iface := range []string{"en0", "en1"} {
		out, err := exec.Command("ipconfig", "getifaddr", iface).Output()
		if err == nil && len(bytes.TrimSpace(out)) > 0 {
			return string(bytes.TrimSpace(out)), nil
		}
	}
	addrs, err := net.InterfaceAddrs()
	if err != nil {
		return "", err
	}
	for _, addr := range addrs {
		if ipNet, ok := addr.(*net.IPNet); ok && !ipNet.IP.IsLoopback() {
			if v4 := ipNet.IP.To4(); v4 != nil {
				return v4.String(), nil
			}
		}
	}
	return "", fmt.Errorf("no non-loopback IPv4 address found; containers could not reach the fake GitHub")
}

func waitFor(what string, timeout time.Duration, cond func() (bool, error)) error {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		ok, err := cond()
		if err != nil {
			return fmt.Errorf("waiting for %s: %w", what, err)
		}
		if ok {
			return nil
		}
		time.Sleep(500 * time.Millisecond)
	}
	return fmt.Errorf("timed out waiting for %s", what)
}

func compact(v any) string {
	raw, err := json.Marshal(v)
	if err != nil {
		return fmt.Sprint(v)
	}
	return string(raw)
}

func sha256Hex(b []byte) string {
	sum := sha256Sum(b)
	const hexdigits = "0123456789abcdef"
	out := make([]byte, 0, len(sum)*2)
	for _, c := range sum {
		out = append(out, hexdigits[c>>4], hexdigits[c&0xf])
	}
	return string(out)
}

// prefixWriter returns a writer that prefixes every line — subprocess output stays
// readable inside the harness log.
func prefixWriter(prefix string) io.Writer {
	pr, pw := io.Pipe()
	go func() {
		buf := make([]byte, 4096)
		line := []byte{}
		for {
			n, err := pr.Read(buf)
			if n > 0 {
				line = append(line, buf[:n]...)
				for {
					i := bytes.IndexByte(line, '\n')
					if i < 0 {
						break
					}
					fmt.Printf("%s%s\n", prefix, line[:i])
					line = line[i+1:]
				}
			}
			if err != nil {
				if len(line) > 0 {
					fmt.Printf("%s%s\n", prefix, line)
				}
				return
			}
		}
	}()
	return pw
}
