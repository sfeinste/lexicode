package github

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/spruce/lexicode/internal/domain"
	"github.com/spruce/lexicode/internal/kernel"
	"github.com/spruce/lexicode/internal/kernel/audit"
	"github.com/spruce/lexicode/internal/kernel/ports"
)

// The tests run every port method against a recorded-fixture HTTP transport: an httptest
// server with hand-written canned JSON per endpoint, reached through a counting RoundTripper
// so that "zero network calls" is a real assertion, not an inference. Hand-written fixtures
// were chosen over a go-vcr style recorder dependency: the JSON shapes are small, stable and
// documented, and there is no cassette machinery to keep secret-free.

var (
	testRepo  = domain.RepoRef{Owner: "acme", Name: "payments"}
	testActor = domain.Actor{AgentID: "agent-7", RunID: "run-99"}
	testCreds = ports.Creds{Token: "ghp_fixturetoken1234567890"}
)

const testMarker = "<!-- lexicode:actor=agent:agent-7 run=run-99 -->"

type roundTripperFunc func(*http.Request) (*http.Response, error)

func (fn roundTripperFunc) RoundTrip(r *http.Request) (*http.Response, error) { return fn(r) }

type auditCall struct {
	action string
	target audit.Target
	after  any
}

// harness is one Forge wired to a fixture server, with every side effect captured.
type harness struct {
	t     *testing.T
	forge *Forge
	mux   *http.ServeMux
	calls atomic.Int32 // HTTP requests that actually left the client

	mu      sync.Mutex
	perms   domain.AgentPermissions
	permErr error
	outputs []domain.RunOutput
	audits  []auditCall
	health  []string
	sleeps  []time.Duration
	logs    strings.Builder
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	h := &harness{t: t, mux: http.NewServeMux()}
	srv := httptest.NewServer(h.mux)
	t.Cleanup(srv.Close)

	counting := roundTripperFunc(func(req *http.Request) (*http.Response, error) {
		h.calls.Add(1)
		return http.DefaultTransport.RoundTrip(req)
	})

	m := New(Options{
		BaseURL:   srv.URL + "/",
		Transport: counting,
		Logger:    slog.New(slog.NewTextHandler(syncWriter{h}, &slog.HandlerOptions{Level: slog.LevelDebug})),
		RetryBase: time.Millisecond,
		Permissions: func(_ context.Context, _ string) (domain.AgentPermissions, error) {
			h.mu.Lock()
			defer h.mu.Unlock()
			if h.permErr != nil {
				return domain.AgentPermissions{}, h.permErr
			}
			return h.perms, nil
		},
		RecordOutput: func(_ context.Context, out domain.RunOutput) error {
			h.mu.Lock()
			defer h.mu.Unlock()
			h.outputs = append(h.outputs, out)
			return nil
		},
		RecordAudit: func(_ context.Context, action string, target audit.Target, _, after any) error {
			h.mu.Lock()
			defer h.mu.Unlock()
			h.audits = append(h.audits, auditCall{action: action, target: target, after: after})
			return nil
		},
	})
	h.forge = m.forge
	h.forge.health = func(state kernel.ModuleState, reason string) {
		h.mu.Lock()
		defer h.mu.Unlock()
		h.health = append(h.health, string(state)+"|"+reason)
	}
	// Sleeps are recorded, never slept, so backoff and reset waits cost the suite nothing.
	h.forge.transport.sleep = func(_ context.Context, d time.Duration) error {
		h.mu.Lock()
		defer h.mu.Unlock()
		h.sleeps = append(h.sleeps, d)
		return nil
	}
	return h
}

// syncWriter serialises log writes into the harness under its lock.
type syncWriter struct{ h *harness }

func (w syncWriter) Write(p []byte) (int, error) {
	w.h.mu.Lock()
	defer w.h.mu.Unlock()
	return w.h.logs.Write(p)
}

func (h *harness) logText() string {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.logs.String()
}

// fixture registers one canned response.
func (h *harness) fixture(pattern string, status int, body string, header map[string]string) {
	h.mux.HandleFunc(pattern, func(w http.ResponseWriter, _ *http.Request) {
		for k, v := range header {
			w.Header().Set(k, v)
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = io.WriteString(w, body)
	})
}

func (h *harness) allowAll() {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.perms = domain.AgentPermissions{
		ReadFiles: true, EditFiles: true, RunCommands: true, PushBranches: true,
		OpenPRs: true, CommentPRs: true, SubmitReviews: true, CreateWikiPages: true,
	}
}

func (h *harness) snapshot() ([]domain.RunOutput, []auditCall) {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]domain.RunOutput(nil), h.outputs...), append([]auditCall(nil), h.audits...)
}

func ctx() context.Context { return context.Background() }

// --------------------------------------------------------------------------------- Verify -----

const repoFixture = `{"name":"payments","private":true,"default_branch":"main",
	"owner":{"login":"acme"}}`

const headCommitFixture = `{"sha":"abc123def","commit":{"message":"feat: initial import\n\nlong body"}}`

func TestVerifyClassicPAT(t *testing.T) {
	h := newHarness(t)
	h.fixture("GET /repos/acme/payments", 200, repoFixture,
		map[string]string{"X-OAuth-Scopes": "repo, read:org"})
	h.fixture("GET /repos/acme/payments/commits/main", 200, headCommitFixture, nil)

	info, err := h.forge.Verify(ctx(), testCreds, testRepo)
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	want := ports.RepoInfo{
		Owner: "acme", Name: "payments", DefaultBranch: "main", Private: true,
		HeadSHA: "abc123def", HeadMessage: "feat: initial import",
	}
	if info != want {
		t.Errorf("Verify = %+v; want %+v", info, want)
	}
}

func TestVerifyClassicPATMissingRepoScope(t *testing.T) {
	h := newHarness(t)
	h.fixture("GET /repos/acme/payments", 200, repoFixture,
		map[string]string{"X-OAuth-Scopes": "gist, read:org"})

	_, err := h.forge.Verify(ctx(), testCreds, testRepo)
	if !errors.Is(err, ports.ErrMissingScope) {
		t.Fatalf("Verify error = %v; want ErrMissingScope", err)
	}
	var scopeErr *ports.MissingScopeError
	if !errors.As(err, &scopeErr) || scopeErr.Scope != "repo" {
		t.Fatalf("error does not name the missing scope %q: %v", "repo", err)
	}
	if !strings.Contains(err.Error(), `"repo"`) {
		t.Errorf("error text should name the repo scope: %v", err)
	}
}

func TestVerifyFineGrainedTokenProbes(t *testing.T) {
	h := newHarness(t)
	// No X-OAuth-Scopes header at all: fine-grained token. The issues probe succeeds.
	h.fixture("GET /repos/acme/payments", 200, repoFixture, nil)
	h.fixture("GET /repos/acme/payments/issues", 200, `[]`, nil)
	h.fixture("GET /repos/acme/payments/commits/main", 200, headCommitFixture, nil)

	if _, err := h.forge.Verify(ctx(), testCreds, testRepo); err != nil {
		t.Fatalf("Verify with passing probes: %v", err)
	}
}

func TestVerifyFineGrainedTokenProbeFailureIsNamed(t *testing.T) {
	h := newHarness(t)
	h.fixture("GET /repos/acme/payments", 200, repoFixture, nil)
	h.fixture("GET /repos/acme/payments/issues", 403,
		`{"message":"Resource not accessible by personal access token"}`, nil)

	_, err := h.forge.Verify(ctx(), testCreds, testRepo)
	var scopeErr *ports.MissingScopeError
	if !errors.As(err, &scopeErr) {
		t.Fatalf("Verify error = %v; want a MissingScopeError", err)
	}
	if scopeErr.Scope != "issues:read" || !strings.Contains(scopeErr.Detail, "listing its issues failed") {
		t.Errorf("probe failure is not named: %+v", scopeErr)
	}
}

// ---------------------------------------------------------------------------------- reads -----

func TestListPullRequestsHonoursSince(t *testing.T) {
	h := newHarness(t)
	h.fixture("GET /repos/acme/payments/pulls", 200, `[
		{"number":42,"title":"Fix the flake","body":"b","state":"open","draft":true,
		 "user":{"login":"octocat"},"head":{"ref":"fix-42","sha":"headsha42"},
		 "base":{"ref":"main"},"html_url":"https://github.com/acme/payments/pull/42",
		 "created_at":"2026-08-01T10:00:00Z","updated_at":"2026-08-10T10:00:00Z"},
		{"number":41,"title":"Old","state":"closed","user":{"login":"bob"},
		 "created_at":"2026-06-01T10:00:00Z","updated_at":"2026-07-01T10:00:00Z"}
	]`, nil)

	since := time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)
	prs, err := h.forge.ListPullRequests(ctx(), testCreds, testRepo, since)
	if err != nil {
		t.Fatalf("ListPullRequests: %v", err)
	}
	if len(prs) != 1 {
		t.Fatalf("got %d PRs; want 1 (the pre-since PR must be cut)", len(prs))
	}
	pr := prs[0]
	if pr.Number != 42 || pr.Title != "Fix the flake" || !pr.Draft || pr.State != "open" ||
		pr.AuthorLogin != "octocat" || pr.HeadRef != "fix-42" || pr.HeadSHA != "headsha42" ||
		pr.BaseRef != "main" || pr.URL != "https://github.com/acme/payments/pull/42" {
		t.Errorf("PR mapped wrong: %+v", pr)
	}
}

func TestListReviews(t *testing.T) {
	h := newHarness(t)
	h.fixture("GET /repos/acme/payments/pulls/17/reviews", 200, `[
		{"id":1001,"user":{"login":"reviewer"},"state":"CHANGES_REQUESTED","body":"needs work",
		 "html_url":"https://github.com/acme/payments/pull/17#pullrequestreview-1001",
		 "submitted_at":"2026-08-10T12:00:00Z"}
	]`, nil)

	reviews, err := h.forge.ListReviews(ctx(), testCreds, testRepo, 17)
	if err != nil {
		t.Fatalf("ListReviews: %v", err)
	}
	if len(reviews) != 1 {
		t.Fatalf("got %d reviews; want 1", len(reviews))
	}
	rev := reviews[0]
	if rev.ID != 1001 || rev.PRNumber != 17 || rev.AuthorLogin != "reviewer" ||
		rev.State != "CHANGES_REQUESTED" || rev.Body != "needs work" {
		t.Errorf("review mapped wrong: %+v", rev)
	}
}

func TestListReviewComments(t *testing.T) {
	h := newHarness(t)
	h.fixture("GET /repos/acme/payments/pulls/comments", 200, `[
		{"id":2001,"pull_request_url":"https://api.github.com/repos/acme/payments/pulls/17",
		 "user":{"login":"reviewer"},"body":"nit: rename this","path":"internal/main.go",
		 "html_url":"https://github.com/acme/payments/pull/17#discussion_r2001",
		 "created_at":"2026-08-09T09:00:00Z","updated_at":"2026-08-09T09:30:00Z"}
	]`, nil)

	comments, err := h.forge.ListReviewComments(ctx(), testCreds, testRepo,
		time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatalf("ListReviewComments: %v", err)
	}
	if len(comments) != 1 {
		t.Fatalf("got %d comments; want 1", len(comments))
	}
	cm := comments[0]
	if cm.ID != 2001 || cm.SubjectNumber != 17 || cm.Path != "internal/main.go" ||
		cm.AuthorLogin != "reviewer" || cm.Body != "nit: rename this" {
		t.Errorf("review comment mapped wrong: %+v", cm)
	}
}

func TestListIssueComments(t *testing.T) {
	h := newHarness(t)
	h.fixture("GET /repos/acme/payments/issues/comments", 200, `[
		{"id":2101,"issue_url":"https://api.github.com/repos/acme/payments/issues/9",
		 "user":{"login":"alice"},"body":"any update?",
		 "html_url":"https://github.com/acme/payments/issues/9#issuecomment-2101",
		 "created_at":"2026-08-08T09:00:00Z","updated_at":"2026-08-08T09:00:00Z"}
	]`, nil)

	comments, err := h.forge.ListIssueComments(ctx(), testCreds, testRepo,
		time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatalf("ListIssueComments: %v", err)
	}
	if len(comments) != 1 || comments[0].SubjectNumber != 9 || comments[0].Path != "" ||
		comments[0].AuthorLogin != "alice" {
		t.Errorf("issue comments mapped wrong: %+v", comments)
	}
}

func TestListCheckSuites(t *testing.T) {
	h := newHarness(t)
	h.fixture("GET /repos/acme/payments/commits/abc123def/check-suites", 200, `{
		"total_count":1,
		"check_suites":[{"id":3001,"head_sha":"abc123def","head_branch":"main",
			"status":"completed","conclusion":"failure",
			"url":"https://api.github.com/repos/acme/payments/check-suites/3001",
			"app":{"name":"GitHub Actions"}}]
	}`, nil)

	suites, err := h.forge.ListCheckSuites(ctx(), testCreds, testRepo, "abc123def")
	if err != nil {
		t.Fatalf("ListCheckSuites: %v", err)
	}
	if len(suites) != 1 {
		t.Fatalf("got %d suites; want 1", len(suites))
	}
	cs := suites[0]
	if cs.ID != 3001 || cs.HeadSHA != "abc123def" || cs.Status != "completed" ||
		cs.Conclusion != "failure" || cs.App != "GitHub Actions" || cs.HeadBranch != "main" {
		t.Errorf("check suite mapped wrong: %+v", cs)
	}
}

func TestListOpenIssuesExcludesPullRequests(t *testing.T) {
	h := newHarness(t)
	h.fixture("GET /repos/acme/payments/issues", 200, `[
		{"number":9,"title":"Login flakes on retry","body":"steps to reproduce…",
		 "user":{"login":"alice"},"labels":[{"name":"bug"},{"name":"auth"}],
		 "html_url":"https://github.com/acme/payments/issues/9",
		 "created_at":"2026-07-01T09:00:00Z","updated_at":"2026-08-01T09:00:00Z"},
		{"number":10,"title":"A PR, not an issue",
		 "pull_request":{"url":"https://api.github.com/repos/acme/payments/pulls/10"},
		 "user":{"login":"bob"}}
	]`, nil)

	issues, err := h.forge.ListOpenIssues(ctx(), testCreds, testRepo)
	if err != nil {
		t.Fatalf("ListOpenIssues: %v", err)
	}
	if len(issues) != 1 {
		t.Fatalf("got %d issues; want 1 (the PR must be filtered out)", len(issues))
	}
	is := issues[0]
	if is.Number != 9 || is.Title != "Login flakes on retry" || is.AuthorLogin != "alice" ||
		len(is.Labels) != 2 || is.Labels[0] != "bug" {
		t.Errorf("issue mapped wrong: %+v", is)
	}
}

func TestReadFile(t *testing.T) {
	h := newHarness(t)
	// base64("hello agents\n")
	h.fixture("GET /repos/acme/payments/contents/AGENTS.md", 200, `{
		"type":"file","encoding":"base64","name":"AGENTS.md","path":"AGENTS.md",
		"content":"aGVsbG8gYWdlbnRzCg=="
	}`, nil)

	data, err := h.forge.ReadFile(ctx(), testCreds, testRepo, "main", "AGENTS.md")
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if string(data) != "hello agents\n" {
		t.Errorf("ReadFile = %q; want %q", data, "hello agents\n")
	}
}

// --------------------------------------------------------------------------------- writes -----

func TestOpenPullRequestAppendsMarkerAndRecords(t *testing.T) {
	h := newHarness(t)
	h.allowAll()

	var sent map[string]any
	h.mux.HandleFunc("POST /repos/acme/payments/pulls", func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&sent); err != nil {
			t.Errorf("decode PR create body: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(201)
		_, _ = io.WriteString(w, `{"number":55,"title":"Add retries","state":"open",
			"body":"…","user":{"login":"lexicode"},
			"head":{"ref":"agent/retries","sha":"newsha"},"base":{"ref":"main"},
			"html_url":"https://github.com/acme/payments/pull/55"}`)
	})

	pr, err := h.forge.OpenPullRequest(ctx(), testCreds, testRepo, testActor, ports.PRSpec{
		Title: "Add retries", Body: "This PR adds retries.", Head: "agent/retries", Base: "main",
	})
	if err != nil {
		t.Fatalf("OpenPullRequest: %v", err)
	}
	if pr.Number != 55 || pr.URL != "https://github.com/acme/payments/pull/55" {
		t.Errorf("PR mapped wrong: %+v", pr)
	}

	body, _ := sent["body"].(string)
	if !strings.HasSuffix(body, "This PR adds retries.\n\n"+testMarker) {
		t.Errorf("PR body must end with the D-9 marker; got %q", body)
	}
	if sent["head"] != "agent/retries" || sent["base"] != "main" {
		t.Errorf("head/base sent wrong: %v", sent)
	}

	outputs, audits := h.snapshot()
	if len(outputs) != 1 || outputs[0].Kind != domain.OutputPullRequest ||
		outputs[0].Ref != "55" || outputs[0].RunID != testActor.RunID {
		t.Errorf("run output not recorded correctly: %+v", outputs)
	}
	if len(audits) != 1 || audits[0].action != "forge.pr.open" {
		t.Errorf("audit row not recorded correctly: %+v", audits)
	}
}

func TestCommentOnPullRequestAppendsMarkerAndRecords(t *testing.T) {
	h := newHarness(t)
	h.allowAll()

	var sent map[string]any
	h.mux.HandleFunc("POST /repos/acme/payments/issues/55/comments", func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&sent); err != nil {
			t.Errorf("decode comment body: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(201)
		_, _ = io.WriteString(w, `{"id":9001,"user":{"login":"lexicode"},
			"body":"CI is green now.",
			"html_url":"https://github.com/acme/payments/pull/55#issuecomment-9001",
			"created_at":"2026-08-20T10:00:00Z","updated_at":"2026-08-20T10:00:00Z"}`)
	})

	cm, err := h.forge.CommentOnPullRequest(ctx(), testCreds, testRepo, testActor, 55, "CI is green now.")
	if err != nil {
		t.Fatalf("CommentOnPullRequest: %v", err)
	}
	if cm.ID != 9001 || cm.SubjectNumber != 55 {
		t.Errorf("comment mapped wrong: %+v", cm)
	}
	body, _ := sent["body"].(string)
	if !strings.HasSuffix(body, "CI is green now.\n\n"+testMarker) {
		t.Errorf("comment body must end with the D-9 marker; got %q", body)
	}
	outputs, audits := h.snapshot()
	if len(outputs) != 1 || outputs[0].Kind != domain.OutputComment || outputs[0].Ref != "9001" {
		t.Errorf("run output not recorded correctly: %+v", outputs)
	}
	if len(audits) != 1 || audits[0].action != "forge.pr.comment" {
		t.Errorf("audit row not recorded correctly: %+v", audits)
	}
}

func TestSubmitReviewRequestChanges(t *testing.T) {
	h := newHarness(t)
	h.allowAll()

	var sent map[string]any
	h.mux.HandleFunc("POST /repos/acme/payments/pulls/55/reviews", func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&sent); err != nil {
			t.Errorf("decode review body: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(200)
		_, _ = io.WriteString(w, `{"id":7001,"user":{"login":"lexicode"},
			"state":"CHANGES_REQUESTED","body":"Please add a test.",
			"html_url":"https://github.com/acme/payments/pull/55#pullrequestreview-7001",
			"submitted_at":"2026-08-20T11:00:00Z"}`)
	})

	rev, err := h.forge.SubmitReview(ctx(), testCreds, testRepo, testActor, 55,
		ports.ReviewSpec{Event: "REQUEST_CHANGES", Body: "Please add a test."})
	if err != nil {
		t.Fatalf("SubmitReview: %v", err)
	}
	if rev.ID != 7001 || rev.PRNumber != 55 || rev.State != "CHANGES_REQUESTED" {
		t.Errorf("review mapped wrong: %+v", rev)
	}
	if sent["event"] != "REQUEST_CHANGES" {
		t.Errorf("event sent wrong: %v", sent["event"])
	}
	body, _ := sent["body"].(string)
	if !strings.HasSuffix(body, "Please add a test.\n\n"+testMarker) {
		t.Errorf("review body must end with the D-9 marker; got %q", body)
	}
	outputs, audits := h.snapshot()
	if len(outputs) != 1 || outputs[0].Kind != domain.OutputReview || outputs[0].Ref != "7001" {
		t.Errorf("run output not recorded correctly: %+v", outputs)
	}
	if len(audits) != 1 || audits[0].action != "forge.pr.review" {
		t.Errorf("audit row not recorded correctly: %+v", audits)
	}
}

func TestSubmitReviewApproveIsForbidden(t *testing.T) {
	h := newHarness(t) // no fixtures on purpose: this must not reach the network
	h.allowAll()       // even with every grant, APPROVE is refused

	for _, event := range []string{"APPROVE", "approve", " Approve "} {
		_, err := h.forge.SubmitReview(ctx(), testCreds, testRepo, testActor, 55,
			ports.ReviewSpec{Event: event, Body: "lgtm"})
		if !errors.Is(err, ports.ErrSelfApprovalForbidden) {
			t.Errorf("SubmitReview(%q) = %v; want ErrSelfApprovalForbidden", event, err)
		}
	}
	if n := h.calls.Load(); n != 0 {
		t.Errorf("APPROVE made %d HTTP calls; want 0", n)
	}
	outputs, audits := h.snapshot()
	if len(outputs) != 0 || len(audits) != 0 {
		t.Errorf("APPROVE must record nothing; got %d outputs, %d audits", len(outputs), len(audits))
	}
}

func TestWritePermissionDeniedMakesZeroNetworkCalls(t *testing.T) {
	h := newHarness(t) // perms are all-false by default; no fixtures on purpose

	writes := []struct {
		grant string
		call  func() error
	}{
		{"open_prs", func() error {
			_, err := h.forge.OpenPullRequest(ctx(), testCreds, testRepo, testActor,
				ports.PRSpec{Title: "t", Head: "h", Base: "b"})
			return err
		}},
		{"comment_prs", func() error {
			_, err := h.forge.CommentOnPullRequest(ctx(), testCreds, testRepo, testActor, 1, "hi")
			return err
		}},
		{"submit_reviews", func() error {
			_, err := h.forge.SubmitReview(ctx(), testCreds, testRepo, testActor, 1,
				ports.ReviewSpec{Event: "COMMENT", Body: "hi"})
			return err
		}},
	}
	for _, w := range writes {
		err := w.call()
		if !errors.Is(err, ports.ErrPermissionDenied) {
			t.Errorf("%s: err = %v; want ErrPermissionDenied", w.grant, err)
			continue
		}
		var denied *ports.PermissionDeniedError
		if !errors.As(err, &denied) || denied.Grant != w.grant || denied.AgentID != testActor.AgentID {
			t.Errorf("%s: error does not name the grant and agent: %v", w.grant, err)
		}
		if !strings.Contains(err.Error(), w.grant) {
			t.Errorf("%s: error text should name the grant: %v", w.grant, err)
		}
	}
	if n := h.calls.Load(); n != 0 {
		t.Errorf("denied writes made %d HTTP calls; want 0", n)
	}
	outputs, audits := h.snapshot()
	if len(outputs) != 0 || len(audits) != 0 {
		t.Errorf("denied writes must record nothing; got %d outputs, %d audits", len(outputs), len(audits))
	}
}

// ----------------------------------------------------------------------- clone URL, logging -----

func TestCloneURLTokenNeverAppearsInLogs(t *testing.T) {
	h := newHarness(t)

	// A default forge (no BaseURL override) clones from github.com.
	plain := New(Options{}).Forge()
	url, err := plain.CloneURL(ctx(), testCreds, testRepo)
	if err != nil {
		t.Fatalf("CloneURL: %v", err)
	}
	want := "https://x-access-token:" + testCreds.Token + "@github.com/acme/payments.git"
	if url != want {
		t.Fatalf("CloneURL = %q; want %q", url, want)
	}

	// A BaseURL override (GHE, fixture servers — S24) redirects the clone to the same
	// scheme+host the API calls hit.
	overridden, err := h.forge.CloneURL(ctx(), testCreds, testRepo)
	if err != nil {
		t.Fatalf("CloneURL (override): %v", err)
	}
	if base := h.forge.baseURL; !strings.Contains(overridden, strings.TrimSuffix(strings.TrimPrefix(base, "http://"), "/")) {
		t.Fatalf("CloneURL (override) = %q; want the fixture host from %q", overridden, base)
	}
	url = overridden // exercise the redaction path below with the overridden URL too

	// Simulate the accident the redactor exists for: the URL (and the raw token) logged
	// through the module's logger, as a message, an attr, and inside an error value.
	h.forge.logger.Info("cloning "+url, slog.String("url", url))
	h.forge.logger.Error("clone failed", slog.Any("err", fmt.Errorf("authenticating to %s: denied", url)))
	h.forge.logger.Warn("token dump", slog.String("token", testCreds.Token))

	logs := h.logText()
	if logs == "" {
		t.Fatal("no logs captured; the assertion below would be vacuous")
	}
	if strings.Contains(logs, testCreds.Token) {
		t.Fatalf("the token leaked into the logs:\n%s", logs)
	}
	if !strings.Contains(logs, redactedPlaceholder) {
		t.Errorf("expected the redaction placeholder in the logs:\n%s", logs)
	}
}

// -------------------------------------------------------------------------- module lifecycle -----

func TestModuleRegistersForgeAndReportsHealth(t *testing.T) {
	k := kernel.New(kernel.Options{Logger: discardLogger()})
	m := New(Options{
		Permissions: func(context.Context, string) (domain.AgentPermissions, error) {
			return domain.AgentPermissions{}, nil
		},
		RecordOutput: func(context.Context, domain.RunOutput) error { return nil },
		RecordAudit:  func(context.Context, string, audit.Target, any, any) error { return nil },
	})
	if err := k.RegisterModule(m); err != nil {
		t.Fatalf("RegisterModule: %v", err)
	}
	if err := k.Init(); err != nil {
		t.Fatalf("Init: %v", err)
	}
	f, err := k.Forge("github")
	if err != nil {
		t.Fatalf("Forge lookup after Init: %v", err)
	}
	if f.ID() != "github" {
		t.Errorf("forge ID = %q; want github", f.ID())
	}

	// With nothing to verify at boot, the module reports ready.
	mods := k.Modules()
	if len(mods) != 1 || mods[0].Name != "github" || mods[0].State != kernel.StateReady {
		t.Fatalf("module status after Init = %+v; want github ready", mods)
	}

	// A runtime rate-limit exhaustion reaches GET /api/v1/system/modules through
	// kernel.SetModuleState.
	m.forge.health(kernel.StateDegraded, "GitHub API rate limit exhausted; resets at soon")
	mods = k.Modules()
	if mods[0].State != kernel.StateDegraded || !strings.Contains(mods[0].Reason, "rate limit") {
		t.Fatalf("module status after degrade = %+v; want degraded with reason", mods)
	}
	m.forge.health(kernel.StateReady, "")
	if mods = k.Modules(); mods[0].State != kernel.StateReady {
		t.Fatalf("module status after recovery = %+v; want ready", mods)
	}
}

func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}
