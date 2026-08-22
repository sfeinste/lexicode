// Command s39 is the brief §3 acceptance run, automated (plan §S39). It drives the whole
// product — real binary, real Docker containers, real git over HTTP, the real MCP server, the
// real GitHub poller, the real trigger engine and the real loop guard — through the eight
// steps of the canonical chain, in order, asserting each one:
//
//  1. a ticket with acceptance criteria, delegated to Dev;
//  2. Dev runs in a container, opens a pull request, the ticket moves to the review column;
//  3. the "pull request opened by an agent" trigger spawns Reviewer;
//  4. Reviewer posts a severity-tagged review;
//  5. the "changes requested" trigger spawns Dev on the same branch;
//  6. the "CI failed" trigger spawns Dev to fix;
//  7. the loop guard stops the cycle at depth 3 and the chain renders;
//  8. a human merges — and nothing else can.
//
// Only two things are faked, both at their network edge (see e2e/harness): GitHub, and the
// `claude` binary inside the container. Every event that fires a trigger is produced by the
// REAL poller reading the fake GitHub's REST API — nothing is injected onto the bus.
//
// Timings (brief §10) are measured and printed:
//
//   - connect-repo → first run in flight, wall clock, with a warm agent image;
//   - the six-step chain configured, measured as the wall time of the API calls that create
//     the four trigger rules. That is a proxy for a human doing the same thing in the trigger
//     editor and is reported as one — it measures the product's side of the work, not the
//     typing.
//
// Invoke through scripts/s39-acceptance.sh, which builds the binary first.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"syscall"
	"time"

	"github.com/spruce/lexicode/e2e/harness"
	"github.com/spruce/lexicode/internal/kernel/ports"
)

const (
	derivedImage = "lexicode/s39-e2e-agent:latest"
	userEmail    = "e2e@example.com"
	userPassword = "correct horse battery staple"
	// repoToken is the credential Lexicode stores and every product-side forge call rides on.
	repoToken = "s39-lexicode-repo-token"
	// humanToken is a person's own credential. Lexicode never sees it; it exists so that the
	// merge at the end is provably not the product's doing.
	humanToken = "s39-a-humans-personal-token"
	projectKey = "PAY"
)

// pollSeconds is what the fixture stores; the poller floors it at 10 (architecture §7), which
// is the rate this chain actually advances at.
const pollSeconds = 10

func main() {
	log.SetFlags(log.Ltime)
	port := flag.Int("port", 7799, "lexicode API port")
	proxyPort := flag.Int("proxy-port", 7798, "lexicode egress-proxy port")
	ghPort := flag.Int("gh-port", 7797, "fake GitHub port (must be reachable from containers)")
	flag.Parse()

	start := time.Now()
	report, err := run(*port, *proxyPort, *ghPort)
	if report != nil {
		report.total = time.Since(start)
	}
	if err != nil {
		report.print()
		log.Fatalf("FAIL: %v", err)
	}
	report.print()
	fmt.Printf("\nPASS: the brief §3 chain ran end to end in %s\n", time.Since(start).Round(time.Second))
}

// timings collects the two brief §10 measurements, with enough context that the numbers
// cannot be read as more than they are.
type timings struct {
	connectToInFlight time.Duration
	chainConfigured   time.Duration
	imageBuild        time.Duration
	imageWasCached    bool
	total             time.Duration
}

func (t *timings) print() {
	if t == nil {
		return
	}
	fmt.Printf("\n== brief §10 timings ==\n")
	fmt.Printf("  goal 1  connect repo → first run in flight   %s (goal: under 5 minutes)\n",
		t.connectToInFlight.Round(time.Millisecond))
	fmt.Printf("          Wall clock from POST /projects/{key}/repo to the run's state\n")
	fmt.Printf("          reaching `running` — which the scheduler sets only once the\n")
	fmt.Printf("          container is up and the agent process has started. It covers the\n")
	fmt.Printf("          product's whole ceremony: connect, write the ticket and its\n")
	fmt.Printf("          criteria, delegate, admit, pull the image, create the container,\n")
	fmt.Printf("          clone, branch, launch. It does NOT cover a human typing, and it\n")
	fmt.Printf("          does not cover the one-time agent image build below.\n")
	if t.imageWasCached {
		fmt.Printf("  aside   agent base image                     already built (cached)\n")
	} else {
		fmt.Printf("  aside   agent image build (first run only)   %s\n", t.imageBuild.Round(time.Second))
	}
	fmt.Printf("  goal 2  six-step chain configured            %s (goal: under 10 minutes)\n",
		t.chainConfigured.Round(time.Millisecond))
	fmt.Printf("          Wall clock of the four POST /projects/{key}/triggers calls that\n")
	fmt.Printf("          create the chain — the same request bodies the trigger editor\n")
	fmt.Printf("          sends. It is a proxy for the goal, and a partial one: it measures\n")
	fmt.Printf("          the product's side of configuring the chain, not the human's.\n")
	fmt.Printf("  aside   whole eight-step chain, end to end   %s\n", t.total.Round(time.Second))
	fmt.Printf("          Bounded below by the poller: four events at a 10s poll floor.\n")
}

func step(n int, what string) {
	log.Printf("")
	log.Printf("======== step %d — %s ========", n, what)
}

func run(port, proxyPort, ghPort int) (*timings, error) {
	t := &timings{}
	repoRoot, err := harness.FindRepoRoot()
	if err != nil {
		return t, err
	}
	work, err := os.MkdirTemp("", "lexicode-s39-*")
	if err != nil {
		return t, err
	}
	log.Printf("work dir: %s", work)

	hostIP, err := harness.DetectHostIP()
	if err != nil {
		return t, err
	}
	log.Printf("host IP (reachable from host and containers): %s", hostIP)

	// -- fake GitHub: REST + git smart-HTTP on one listener ------------------------------
	gh := &harness.GitHub{
		Root: filepath.Join(work, "git"), Owner: "acme", Name: "payments", Branch: "main",
		// The git endpoints demand the repository token, as a private repository does.
		// That is what makes this harness a proof of the credential design rather than a
		// rehearsal of it: the container, whose `origin` is tokenless from the moment the
		// clone finishes, is refused, and the orchestrator's teardown push — the token in
		// that one exec's environment as `http.extraheader` — is not.
		Token: repoToken,
	}
	if err := os.MkdirAll(filepath.Join(gh.Root, gh.Owner), 0o755); err != nil {
		return t, err
	}
	if err := gh.InitBareRepo(work); err != nil {
		return t, err
	}
	ghSrv := &http.Server{
		Addr: fmt.Sprintf("0.0.0.0:%d", ghPort), Handler: gh.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
	}
	go func() {
		if err := ghSrv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("fake github: %v", err)
		}
	}()
	defer func() { _ = ghSrv.Close() }()
	ghBase := fmt.Sprintf("http://%s:%d/", hostIP, ghPort)
	log.Printf("fake GitHub (REST + git http-backend): %s", ghBase)
	// The first push to the pull request's branch fails CI; everything after it is green.
	// Armed up front so nothing in the run depends on winning a race with a container.
	gh.FailNextCI(1)

	t.imageWasCached = harness.ImageExists(derivedImage)
	imageStart := time.Now()
	if err := harness.BuildAgentImage(repoRoot, derivedImage, fakeClaude); err != nil {
		return t, err
	}
	t.imageBuild = time.Since(imageStart)

	// -- the binary, on a fresh data dir --------------------------------------------------
	bin := filepath.Join(repoRoot, "lexicode")
	if _, err := os.Stat(bin); err != nil {
		return t, fmt.Errorf("binary %s not found; run `make build` first (scripts/s39-acceptance.sh does)", bin)
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
	serve.Stdout = harness.PrefixWriter("lexicode| ")
	serve.Stderr = harness.PrefixWriter("lexicode| ")
	if err := serve.Start(); err != nil {
		return t, err
	}
	defer func() {
		_ = serve.Process.Signal(syscall.SIGTERM)
		_, _ = serve.Process.Wait()
	}()

	base := fmt.Sprintf("http://127.0.0.1:%d", port)
	c, err := harness.NewClient(base)
	if err != nil {
		return t, err
	}
	if err := harness.WaitFor("server up", 60*time.Second, func() (bool, error) {
		resp, err := http.Get(base + "/api/v1/auth/me") //nolint:noctx // fixture poll
		if err != nil {
			return false, nil
		}
		_ = resp.Body.Close()
		return resp.StatusCode == http.StatusUnauthorized, nil
	}); err != nil {
		return t, err
	}
	dbPath := filepath.Join(dataDir, "lexicode.db")

	// -- workspace, project, agents -------------------------------------------------------
	log.Printf("== workspace, project and the agent roster ==")
	if _, err := c.Do("POST", "/api/v1/auth/setup", map[string]any{
		"email": userEmail, "display_name": "E2E", "password": userPassword,
	}, 201); err != nil {
		return t, err
	}
	if _, err := c.Do("POST", "/api/v1/projects", map[string]any{
		"key": projectKey, "name": "Payments",
	}, 201); err != nil {
		return t, err
	}
	// The poller floors the interval at 10s; the default 30 would make the chain take four
	// times as long to prove the same thing.
	if err := harness.SetPollInterval(dbPath, pollSeconds); err != nil {
		return t, err
	}
	log.Printf("poll interval set to %ds (the poller's floor)", pollSeconds)

	dev, err := newAgent(c, "Dev", "Implements tickets and addresses review findings", map[string]bool{
		"read_files": true, "edit_files": true, "run_commands": true,
		"push_branches": true, "open_prs": true, "comment_prs": true,
		// Dev submits COMMENT reviews ("addressed, please re-review"); it can never approve
		// — no permission unlocks that (brief D6).
		"submit_reviews": true,
	})
	if err != nil {
		return t, err
	}
	reviewer, err := newAgent(c, "Reviewer", "Reviews pull requests", map[string]bool{
		"read_files": true, "run_commands": true, "submit_reviews": true,
		// Deliberately absent: edit_files, push_branches, open_prs.
	})
	if err != nil {
		return t, err
	}
	log.Printf("agents: Dev=%s Reviewer=%s", dev, reviewer)

	// -- the six-step chain, configured (timing goal 2) -----------------------------------
	log.Printf("== configuring the chain ==")
	configStart := time.Now()
	triggers, err := createTriggers(c, dev, reviewer)
	if err != nil {
		return t, err
	}
	t.chainConfigured = time.Since(configStart)
	for _, name := range triggerOrder {
		log.Printf("trigger %-22s %s", name, triggers[name])
	}

	// -- step 1: connect the repo, write the ticket, delegate (timing goal 1) -------------
	step(1, "a ticket with acceptance criteria, delegated to Dev")
	connectStart := time.Now()
	if _, err := c.Do("POST", "/api/v1/projects/"+projectKey+"/repo", map[string]any{
		"owner": gh.Owner, "name": gh.Name, "token": repoToken,
	}, 200, 201); err != nil {
		return t, err
	}
	log.Printf("repo connected through the real forge adapter; the poller's worker starts now")
	// image_ref and network_policy have no API surface yet (S37 left image_ref a
	// project-settings field); the fixture writes the repos row directly and says so.
	// network 'open' keeps containers on the bridge, where the fake GitHub and the MCP
	// endpoint are both reachable without the egress proxy in the path.
	if err := harness.SetRepoSettings(dbPath, projectKey, derivedImage); err != nil {
		return t, err
	}

	ticket, err := c.Do("POST", "/api/v1/projects/"+projectKey+"/tickets", map[string]any{
		"title":       "Add idempotency keys to POST /charges",
		"description": "Duplicate webhooks double-charge. Add idempotency keys.",
	}, 201)
	if err != nil {
		return t, err
	}
	ticketID := ticket["id"].(string)
	var firstCriterion string
	for _, text := range []string{
		"A replayed webhook does not create a second charge",
		"Keys expire after 24 hours",
	} {
		crit, err := c.Do("POST", "/api/v1/tickets/"+ticketID+"/criteria",
			map[string]any{"text": text}, 200, 201)
		if err != nil {
			return t, err
		}
		if firstCriterion == "" {
			firstCriterion, _ = crit["id"].(string)
		}
	}
	log.Printf("ticket %v created with 2 acceptance criteria", ticket["key"])

	del, err := c.Do("POST", "/api/v1/tickets/"+ticketID+"/delegate", map[string]any{
		"agent_id": dev,
		"prompt": "E2E-ROLE: dev-implement\nE2E-CRITERION: " + firstCriterion +
			"\n\nImplement the ticket and push your branch.",
	}, 201)
	if err != nil {
		return t, err
	}
	run1 := del["run_id"].(string)
	log.Printf("delegated: run %s enqueued", run1)

	if err := harness.WaitFor("the first run in flight", 6*time.Minute, func() (bool, error) {
		return c.RunState(run1) == "running", nil
	}); err != nil {
		return t, err
	}
	t.connectToInFlight = time.Since(connectStart)
	log.Printf("first run in flight %s after the repo was connected", t.connectToInFlight.Round(time.Millisecond))

	// -- step 2: the PR, and the ticket in the review column ------------------------------
	step(2, "Dev runs in a container, opens a PR, the ticket moves to review")
	if err := waitCompleted(c, run1, "the delegated run"); err != nil {
		return t, err
	}
	// The provisioning checklist proves this was a real container with a real clone, not a
	// stub — interaction rule 7's checklist doubling as the acceptance's evidence.
	checklist, err := provisionSteps(c, run1)
	if err != nil {
		return t, err
	}
	log.Printf("provisioning checklist: %s", strings.Join(checklist, ", "))
	for _, want := range []string{"image", "container", "clone", "branch"} {
		if !contains(checklist, want) {
			return t, fmt.Errorf("the provisioning checklist has no successful %q step: %v", want, checklist)
		}
	}
	run1Body, err := c.Run(run1)
	if err != nil {
		return t, err
	}
	branch, _ := run1Body["branch"].(string)
	cost, _ := run1Body["cost_cents"].(float64)
	if cost <= 0 {
		return t, fmt.Errorf("the run completed without a cost figure: %s", harness.Compact(run1Body))
	}
	log.Printf("run #1 completed: branch=%s cost=%d¢", branch, int(cost))
	if !gh.BranchExists(branch) {
		return t, fmt.Errorf("branch %q is not on the remote; branches:\n%s", branch, gh.Branches())
	}

	// Security: the provisioner materializes .lexicode/ (which holds this run's live MCP
	// token) and .claude/ into the workspace root, and the agent ran a plain `git add -A`.
	// Nothing of ours may reach the user's repository — the sandbox writes both into
	// .git/info/exclude during Prepare, and this is where that is proved end to end.
	tree, err := gh.Tree(branch)
	if err != nil {
		return t, err
	}
	for _, scaffolding := range []string{".lexicode/", ".claude/"} {
		if strings.Contains(tree, scaffolding) {
			return t, fmt.Errorf(
				"the pushed branch carries the orchestrator's %s — the run's MCP token is now in "+
					"the user's repository and its history. Tree:\n%s", scaffolding, tree)
		}
	}
	log.Printf("pushed tree carries no orchestrator scaffolding (no .lexicode/, no .claude/):")
	for _, line := range strings.Split(tree, "\n") {
		log.Printf("    | %s", line)
	}

	prs := gh.PullRequests()
	if len(prs) != 1 {
		return t, fmt.Errorf("the fake GitHub has %d pull requests, want 1: %s", len(prs), harness.Compact(prs))
	}
	pr := prs[0]
	if pr.Head != branch {
		return t, fmt.Errorf("PR head = %q, want the run's branch %q", pr.Head, branch)
	}
	if !strings.Contains(pr.Body, "<!-- lexicode:actor=agent:") {
		return t, fmt.Errorf("the PR body lacks the D-9 actor marker:\n%s", pr.Body)
	}
	log.Printf("PR #%d opened: %q (head=%s, D-9 marker present)", pr.Number, pr.Title, pr.Head)

	if err := requireOutput(c, run1, "pull_request"); err != nil {
		return t, err
	}
	cat, err := c.TicketColumnCategory(projectKey, ticketID)
	if err != nil {
		return t, err
	}
	if cat != "review" {
		return t, fmt.Errorf("the ticket's column category is %q, want review", cat)
	}
	log.Printf("ticket moved to the review-category column (never by name — brief D2)")

	// -- step 3: the trigger spawns Reviewer ----------------------------------------------
	step(3, `the "PR opened by an agent" trigger spawns Reviewer`)
	run2, err := waitForTriggeredRun(c, triggers["pr-opened"], 6*time.Minute)
	if err != nil {
		return t, err
	}
	log.Printf("trigger fired through the real poller: Reviewer run %s", run2)
	if err := requireRunField(c, run2, "agent_id", reviewer); err != nil {
		return t, err
	}
	if err := requireRunField(c, run2, "subject_key", fmt.Sprintf("pr:%d", pr.Number)); err != nil {
		return t, err
	}

	// -- step 4: the severity-tagged review ------------------------------------------------
	step(4, "Reviewer posts a severity-tagged review")
	if err := waitCompleted(c, run2, "the reviewer run"); err != nil {
		return t, err
	}
	reviews := gh.Reviews()
	if len(reviews) != 1 {
		return t, fmt.Errorf("the fake GitHub has %d reviews, want 1", len(reviews))
	}
	rev := reviews[0]
	if rev.State != "CHANGES_REQUESTED" {
		return t, fmt.Errorf("review state = %q, want CHANGES_REQUESTED", rev.State)
	}
	for _, want := range []string{"[BLOCKER]", "[MINOR]", "[NIT]", "<!-- lexicode:actor=agent:"} {
		if !strings.Contains(rev.Body, want) {
			return t, fmt.Errorf("the review body lacks %q:\n%s", want, rev.Body)
		}
	}
	log.Printf("review %d submitted on PR #%d (%s), severity-tagged and marker-attributed:",
		rev.ID, rev.PRNumber, rev.State)
	for _, line := range strings.Split(rev.Body, "\n") {
		log.Printf("    | %s", line)
	}
	if err := requireOutput(c, run2, "review"); err != nil {
		return t, err
	}

	// -- step 5: changes requested spawns Dev on the same branch ---------------------------
	step(5, `the "changes requested" trigger spawns Dev on the same branch`)
	headBeforeAddress := gh.Head(pr.Head)
	run3, err := waitForTriggeredRun(c, triggers["changes-requested"], 6*time.Minute)
	if err != nil {
		return t, err
	}
	log.Printf("Dev run %s spawned to address the review", run3)
	if err := requireRunField(c, run3, "agent_id", dev); err != nil {
		return t, err
	}
	if err := waitCompleted(c, run3, "the address run"); err != nil {
		return t, err
	}
	headAfterAddress := gh.Head(pr.Head)
	if headAfterAddress == headBeforeAddress {
		return t, fmt.Errorf("the address run did not push to %s (head still %s)", pr.Head, headBeforeAddress)
	}
	if got := len(gh.PullRequests()); got != 1 {
		return t, fmt.Errorf("%d pull requests exist; the address run must reuse PR #%d, not open another", got, pr.Number)
	}
	log.Printf("PR #%d head moved %s → %s on the same branch (%s); still one pull request",
		pr.Number, headBeforeAddress[:8], headAfterAddress[:8], pr.Head)
	body, err := gh.FileAt(headAfterAddress, "src/idempotency.ts")
	if err != nil {
		return t, err
	}
	if !strings.Contains(body, "TTL_MS") {
		return t, fmt.Errorf("the addressed file does not carry the fix:\n%s", body)
	}
	// A run always gets a NEW branch, so a follow-up run that works on the pull request's
	// branch leaves its own branch unpushed and there is no pull request to open. That is a
	// no-op, not a failure: the run succeeds and a level-2 system line says so plainly.
	if note := systemActivity(c, run3, "could not open a pull request"); note != "" {
		return t, fmt.Errorf("the address run reported a PR-open FAILURE: %s", note)
	}
	note := systemActivity(c, run3, "No pull request opened")
	if note == "" {
		return t, fmt.Errorf("the address run has no system note about the pull request it "+
			"could not open; activities: %s", activityTitles(c, run3))
	}
	log.Printf("no-op, reported plainly: %s", note)

	// -- step 6: CI failed spawns Dev to fix -----------------------------------------------
	step(6, `the "CI failed" trigger spawns Dev to fix`)
	suites := gh.CheckSuites()
	if len(suites) == 0 || suites[0].Conclusion != "failure" {
		return t, fmt.Errorf("expected a failing check suite for the pushed head; suites: %s",
			harness.Compact(suites))
	}
	log.Printf("CI failed on %s (suite %d)", suites[0].HeadSHA[:8], suites[0].ID)
	runCI, err := waitForTriggeredRun(c, triggers["ci-failed"], 6*time.Minute)
	if err != nil {
		return t, err
	}
	log.Printf("Dev run %s spawned by the CI-failure trigger", runCI)
	if err := requireRunField(c, runCI, "agent_id", dev); err != nil {
		return t, err
	}
	if err := waitCompleted(c, runCI, "the CI-fix run"); err != nil {
		return t, err
	}
	log.Printf("CI-fix run completed; PR #%d head is now %s", pr.Number, gh.Head(pr.Head)[:8])

	// -- step 7: the loop guard stops the cycle at depth 3 ---------------------------------
	step(7, "the loop guard stops the cycle at depth 3 and the chain renders")
	log.Printf("the cycle continues on its own: the address run's COMMENT review re-spawns")
	log.Printf("Reviewer, whose changes-requested review would spawn Dev a third time…")
	stopped, err := waitForLoopStopped(c, triggers["changes-requested"], 8*time.Minute)
	if err != nil {
		return t, err
	}
	stoppedRun, err := c.Run(stopped)
	if err != nil {
		return t, err
	}
	if stoppedRun["state"] != "loop_stopped" {
		return t, fmt.Errorf("the guard's run is in state %v, want loop_stopped", stoppedRun["state"])
	}
	if cost, _ := stoppedRun["cost_cents"].(float64); cost != 0 || stoppedRun["prompt"] != nil {
		return t, fmt.Errorf("the loop-stopped run is not inert: %s", harness.Compact(stoppedRun))
	}
	log.Printf("run #%v: %s", stoppedRun["seq"], stoppedRun["state_reason"])
	log.Printf("it has no container, no prompt and no cost — it exists so the chain has")
	log.Printf("something to hang the explanation on (architecture §9).")
	focal, err := printChain(c, stopped)
	if err != nil {
		return t, err
	}
	// Depth is not on the run body; the chain is where the product renders it, and the
	// chain is what the loop view draws.
	if depth, _ := focal["depth"].(float64); int(depth) != 3 {
		return t, fmt.Errorf("the loop-stopped run sits at depth %v in the chain, want 3", focal["depth"])
	}
	if !strings.Contains(fmt.Sprint(stoppedRun["state_reason"]), "depth 3 reached the limit of 3") {
		return t, fmt.Errorf("the stop reason does not name the depth limit: %v", stoppedRun["state_reason"])
	}

	// -- step 8: a human merges, and nothing else can --------------------------------------
	step(8, "a human merges — and nothing else can")
	if names := forgeMethodsMatching("merge", "approve", "forcepush", "force_push"); len(names) > 0 {
		return t, fmt.Errorf("ports.ForgeProvider exposes %v; brief D6 says the capability must be absent", names)
	}
	log.Printf("structural: ports.ForgeProvider has no Merge, Approve or ForcePush method —")
	log.Printf("brief D6 is an absent capability, not a permission check.")
	if calls := gh.MergeCalls(); len(calls) != 0 {
		return t, fmt.Errorf("the product called a merge endpoint %d time(s): %s",
			len(calls), harness.Compact(calls))
	}
	log.Printf("behavioural: across %d API calls in this run, zero touched a merge endpoint.",
		len(gh.Requests()))

	if err := gh.MergeAsHuman(ghBase, humanToken, pr.Number); err != nil {
		return t, err
	}
	merged := gh.PullRequests()[0]
	if !merged.Merged || merged.State != "closed" {
		return t, fmt.Errorf("PR #%d did not merge: %s", pr.Number, harness.Compact(merged))
	}
	if got := gh.Head("main"); got != merged.HeadSHA {
		return t, fmt.Errorf("main is at %s, want the merged head %s", got, merged.HeadSHA)
	}
	calls := gh.MergeCalls()
	if len(calls) != 1 {
		return t, fmt.Errorf("expected exactly one merge call, got %d", len(calls))
	}
	if calls[0].Token != humanToken {
		return t, fmt.Errorf("the merge call carried %q, want the human's own token", calls[0].Token)
	}
	log.Printf("PR #%d merged by a human, with a token Lexicode has never held; main is now %s",
		merged.Number, gh.Head("main")[:8])

	if err := summarize(c, gh, triggers); err != nil {
		return t, err
	}
	return t, nil
}

// ---------------------------------------------------------------- the chain -----

// triggerOrder is the order the summary prints the rules in — the order a user builds them.
var triggerOrder = []string{"pr-opened", "changes-requested", "ci-failed", "review-addressed"}

// createTriggers writes the four rules of the canonical chain. Three are the brief's steps 3,
// 5 and 6; the fourth is the "addressed → re-review" hop that closes the cycle the loop guard
// exists to stop (brief D5's "PR opened → review → address → push → PR updated" example).
//
// Every rule keeps actor suppression ON, the shipped default — ci-failed included. CI runs on
// the agent's own branch, so the poller attributes the check suite to that agent, but layer 1
// exempts check_suite events (a CI result is a machine's verdict about the agent's work, not
// the agent acting; see internal/kernel/guard's exemptFromActorSuppression). The brief's step 6
// therefore fires under the default config, with no per-rule escape hatch.
//
// One rule overrides a loop-config default, deliberately and documented: changes-requested
// shortens the debounce from 90s to 5s. This harness compresses a chain that would take a
// human afternoon into a couple of minutes; at 90s, layer 2 would absorb the second bounce
// (correctly!) before layer 4 ever saw it.
func createTriggers(c *harness.Client, dev, reviewer string) (map[string]string, error) {
	specs := []struct {
		key  string
		body map[string]any
	}{
		{"pr-opened", map[string]any{
			"name": "PR opened by an agent → review it", "enabled": true,
			"source_id": "github.poll", "event": "pull_request",
			"activity_types": []string{"opened"},
			"conditions":     rawJSON(`{"all":[{"op":"actor.is_agent"}]}`),
			"actions": rawJSON(fmt.Sprintf(
				`[{"action_id":"run_agent","params":{"agent_id":%q,"prompt_override":%q}}]`,
				reviewer, "E2E-ROLE: reviewer\nE2E-REVIEW: request_changes\n"+
					"E2E-PR: {{pr.number}}\nE2E-BRANCH: {{pr.branch}}\n\n"+
					"Review pull request #{{pr.number}} on branch {{pr.branch}}. "+
					"Submit a review with severity-tagged findings.")),
		}},
		{"changes-requested", map[string]any{
			"name": "Changes requested → Dev addresses them", "enabled": true,
			"source_id": "github.poll", "event": "pull_request_review",
			"activity_types": []string{"submitted"},
			"conditions": rawJSON(
				`{"all":[{"field":"review.state","op":"enum.is","value":"changes_requested"}]}`),
			"loop_config": rawJSON(`{"actor_suppression":true,"debounce_seconds":5,` +
				`"cancel_in_progress":true,"depth_limit":3,"daily_budget_cents":null}`),
			"actions": rawJSON(fmt.Sprintf(
				`[{"action_id":"run_agent","params":{"agent_id":%q,"prompt_override":%q}}]`,
				dev, "E2E-ROLE: dev-address\nE2E-PR: {{pr.number}}\nE2E-BRANCH: {{pr.branch}}\n\n"+
					"Review {{review.id}} requested changes on PR #{{pr.number}}. "+
					"Work on branch {{pr.branch}} — the same branch, not a new one.")),
		}},
		{"ci-failed", map[string]any{
			"name": "CI failed → Dev fixes it", "enabled": true,
			"source_id": "github.poll", "event": "check_suite",
			"activity_types": []string{"completed"},
			"conditions": rawJSON(
				`{"all":[{"field":"check.conclusion","op":"enum.is","value":"failure"}]}`),
			// Actor suppression stays ON (the default): the guard exempts check_suite
			// events, so this rule fires on the agent's own branch without an escape hatch.
			"loop_config": rawJSON(`{"actor_suppression":true,"debounce_seconds":5,` +
				`"cancel_in_progress":true,"depth_limit":3,"daily_budget_cents":null}`),
			"actions": rawJSON(fmt.Sprintf(
				`[{"action_id":"run_agent","params":{"agent_id":%q,"prompt_override":%q}}]`,
				dev, "E2E-ROLE: dev-cifix\nE2E-PR: {{pr.number}}\nE2E-BRANCH: {{pr.branch}}\n\n"+
					"The {{check.name}} suite failed on PR #{{pr.number}}. Fix it on {{pr.branch}}.")),
		}},
		{"review-addressed", map[string]any{
			"name": "Findings addressed → re-review", "enabled": true,
			"source_id": "github.poll", "event": "pull_request_review",
			"activity_types": []string{"submitted"},
			"conditions": rawJSON(`{"all":[` +
				`{"field":"review.state","op":"enum.is","value":"commented"},` +
				`{"field":"actor.agent","op":"text.is","value":"Dev"}]}`),
			"loop_config": rawJSON(`{"actor_suppression":true,"debounce_seconds":5,` +
				`"cancel_in_progress":true,"depth_limit":3,"daily_budget_cents":null}`),
			"actions": rawJSON(fmt.Sprintf(
				`[{"action_id":"run_agent","params":{"agent_id":%q,"prompt_override":%q}}]`,
				reviewer, "E2E-ROLE: reviewer\nE2E-REVIEW: request_changes\n"+
					"E2E-PR: {{pr.number}}\nE2E-BRANCH: {{pr.branch}}\n\n"+
					"Dev says the findings on PR #{{pr.number}} are addressed. Look again.")),
		}},
	}
	out := map[string]string{}
	for _, spec := range specs {
		created, err := c.Do("POST", "/api/v1/projects/"+projectKey+"/triggers", spec.body, 201)
		if err != nil {
			return nil, fmt.Errorf("creating trigger %s: %w", spec.key, err)
		}
		out[spec.key] = created["id"].(string)
	}
	return out, nil
}

// ---------------------------------------------------------------- assertions -----

func newAgent(c *harness.Client, name, role string, perms map[string]bool) (string, error) {
	created, err := c.Do("POST", "/api/v1/projects/"+projectKey+"/agents", map[string]any{
		"name": name, "role": role, "model": "claude-sonnet-5",
		"effort": "medium", "autonomy": "auto",
		"directive": "You are " + name + ". " + role + ".",
	}, 201)
	if err != nil {
		return "", err
	}
	id := created["id"].(string)
	if _, err := c.Do("PATCH", "/api/v1/agents/"+id, map[string]any{"permissions": perms}, 200); err != nil {
		return "", err
	}
	return id, nil
}

func waitCompleted(c *harness.Client, runID, what string) error {
	return harness.WaitFor(what+" to complete", 8*time.Minute, func() (bool, error) {
		st := c.RunState(runID)
		switch st {
		case "failed", "timed_out", "canceled":
			return false, fmt.Errorf("%s ended %s: %s", what, st, harness.Compact(c.LastRun))
		}
		return st == "completed", nil
	})
}

// waitForTriggeredRun waits for the trigger's first succeeded firing and returns its run.
func waitForTriggeredRun(c *harness.Client, triggerID string, timeout time.Duration) (string, error) {
	var runID string
	err := harness.WaitFor("trigger "+triggerID+" to fire", timeout, func() (bool, error) {
		body, err := c.Do("GET", "/api/v1/triggers/"+triggerID+"/firings", nil, 200)
		if err != nil {
			return false, err
		}
		for _, f := range firings(body) {
			if f["outcome"] == "succeeded" && f["run_id"] != nil {
				runID = f["run_id"].(string)
				return true, nil
			}
		}
		return false, nil
	})
	return runID, err
}

// waitForLoopStopped waits for the trigger to record a loop_stopped firing and returns the
// terminal run row the guard created for it.
func waitForLoopStopped(c *harness.Client, triggerID string, timeout time.Duration) (string, error) {
	var runID string
	err := harness.WaitFor("the loop guard to stop the cycle", timeout, func() (bool, error) {
		body, err := c.Do("GET", "/api/v1/triggers/"+triggerID+"/firings", nil, 200)
		if err != nil {
			return false, err
		}
		for _, f := range firings(body) {
			if f["outcome"] == "loop_stopped" {
				if f["run_id"] == nil {
					return false, fmt.Errorf("a loop_stopped firing carries no run: %s", harness.Compact(f))
				}
				runID = f["run_id"].(string)
				return true, nil
			}
		}
		return false, nil
	})
	return runID, err
}

func firings(body map[string]any) []map[string]any {
	raw, _ := body["firings"].([]any)
	out := make([]map[string]any, 0, len(raw))
	for _, f := range raw {
		out = append(out, f.(map[string]any))
	}
	return out
}

// provisionSteps returns the titles of a run's successful provisioning steps.
func provisionSteps(c *harness.Client, runID string) ([]string, error) {
	acts, err := c.Activities(runID)
	if err != nil {
		return nil, err
	}
	var out []string
	for _, a := range acts {
		if a.Type == "provision" && a.OK != nil && *a.OK {
			out = append(out, a.Title)
		}
	}
	return out, nil
}

// systemActivity returns the first system activity containing needle, or "".
func systemActivity(c *harness.Client, runID, needle string) string {
	acts, err := c.Activities(runID)
	if err != nil {
		return ""
	}
	for _, a := range acts {
		if strings.Contains(a.Title, needle) {
			return a.Title
		}
	}
	return ""
}

// activityTitles renders a run's activity titles for a failure message.
func activityTitles(c *harness.Client, runID string) string {
	acts, err := c.Activities(runID)
	if err != nil {
		return err.Error()
	}
	titles := make([]string, 0, len(acts))
	for _, a := range acts {
		titles = append(titles, fmt.Sprintf("%s/%d %q", a.Type, a.Level, a.Title))
	}
	return strings.Join(titles, ", ")
}

func contains(list []string, want string) bool {
	for _, s := range list {
		if strings.Contains(s, want) {
			return true
		}
	}
	return false
}

func requireRunField(c *harness.Client, runID, field, want string) error {
	row, err := c.Run(runID)
	if err != nil {
		return err
	}
	if got, _ := row[field].(string); got != want {
		return fmt.Errorf("run %s: %s = %q, want %q", runID, field, got, want)
	}
	return nil
}

func requireOutput(c *harness.Client, runID, kind string) error {
	outputs, err := c.Outputs(runID)
	if err != nil {
		return err
	}
	for _, o := range outputs {
		if o["kind"] == kind {
			return nil
		}
	}
	return fmt.Errorf("run %s produced no %s output: %s", runID, kind, harness.Compact(outputs))
}

// printChain renders GET /runs/{id}/chain the way the loop-chain view does: the causal
// alternation of runs and events, oldest first, with the stopped run last.
func printChain(c *harness.Client, runID string) (map[string]any, error) {
	body, err := c.Do("GET", "/api/v1/runs/"+runID+"/chain", nil, 200)
	if err != nil {
		return nil, err
	}
	entries, _ := body["chain"].([]any)
	if len(entries) == 0 {
		return nil, fmt.Errorf("the chain for run %s is empty", runID)
	}
	log.Printf("")
	log.Printf("    the causal chain, as GET /runs/{id}/chain renders it:")
	runs := 0
	var focal map[string]any
	for _, e := range entries {
		entry := e.(map[string]any)
		switch entry["type"] {
		case "run":
			r := entry["run"].(map[string]any)
			runs++
			marker := " "
			if r["focus"] == true {
				marker = "*"
				focal = r
			}
			log.Printf("    %s run #%-3v %-9v depth=%-2v subject=%-8v %-12v %v",
				marker, r["seq"], r["agent_name"], r["depth"], r["subject_key"],
				r["state"], r["state_reason"])
		case "event":
			ev := entry["event"].(map[string]any)
			log.Printf("       └─ event %v/%v on %v, actor %v",
				ev["kind"], ev["activity_type"], ev["subject"], ev["actor_kind"])
		}
	}
	log.Printf("")
	if runs < 4 {
		return nil, fmt.Errorf("the chain has %d run hops, want at least 4 (three agent hops plus the stop)", runs)
	}
	if focal == nil {
		return nil, fmt.Errorf("the chain does not mark a focal run: %s", harness.Compact(entries))
	}
	return focal, nil
}

// forgeMethodsMatching reports any ForgeProvider method whose name contains one of the needles
// — the compile-time half of brief D6, asserted at run time so the acceptance names it.
func forgeMethodsMatching(needles ...string) []string {
	var out []string
	iface := reflect.TypeOf((*ports.ForgeProvider)(nil)).Elem()
	for i := 0; i < iface.NumMethod(); i++ {
		name := strings.ToLower(iface.Method(i).Name)
		for _, needle := range needles {
			if strings.Contains(name, needle) {
				out = append(out, iface.Method(i).Name)
			}
		}
	}
	return out
}

// summarize prints the final tally: every rule's outcome counts and every run the chain
// produced, so the acceptance output stands on its own as evidence.
func summarize(c *harness.Client, gh *harness.GitHub, triggers map[string]string) error {
	log.Printf("")
	log.Printf("======== summary ========")
	for _, key := range triggerOrder {
		body, err := c.Do("GET", "/api/v1/triggers/"+triggers[key], nil, 200)
		if err != nil {
			return err
		}
		health, _ := body["health"].(map[string]any)
		counts, _ := health["counts"].(map[string]any)
		log.Printf("  rule %-20s %v", key, harness.Compact(counts))
	}
	agentsBody, err := c.Do("GET", "/api/v1/projects/"+projectKey+"/agents", nil, 200)
	if err != nil {
		return err
	}
	names := map[string]string{}
	for _, a := range agentsBody["agents"].([]any) {
		agent := a.(map[string]any)
		names[agent["id"].(string)] = agent["name"].(string)
	}
	runsBody, err := c.Do("GET", "/api/v1/projects/"+projectKey+"/runs", nil, 200)
	if err != nil {
		return err
	}
	rows, _ := runsBody["runs"].([]any)
	log.Printf("  %d runs:", len(rows))
	for i := len(rows) - 1; i >= 0; i-- {
		r := rows[i].(map[string]any)
		log.Printf("    #%-3v %-9v %-12v %-8v %v",
			r["seq"], names[r["agent_id"].(string)], r["state"], r["subject_key"], r["state_reason"])
	}
	log.Printf("  fake GitHub: %d pull requests, %d reviews, %d check suites, %d API calls",
		len(gh.PullRequests()), len(gh.Reviews()), len(gh.CheckSuites()), len(gh.Requests()))
	return nil
}

func rawJSON(s string) json.RawMessage { return json.RawMessage(s) }
