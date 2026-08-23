package github

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

// graphQLFixture registers the /graphql route with a canned response and captures the request
// bodies it was sent, so the query and its variables can be asserted on.
func (h *harness) graphQLFixture(status int, body string) *[]graphQLRequest {
	var sent []graphQLRequest
	h.mux.HandleFunc("POST /graphql", func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		var req graphQLRequest
		_ = json.Unmarshal(raw, &req)
		h.mu.Lock()
		sent = append(sent, req)
		h.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = io.WriteString(w, body)
	})
	return &sent
}

// threadsFixture is one review's worth of GraphQL: two threads on the same review, one of them
// resolved, the open one carrying a reply.
const threadsFixture = `{"data":{"repository":{"pullRequest":{"reviewThreads":{
  "pageInfo":{"hasNextPage":false,"endCursor":""},
  "nodes":[
    {"id":"PRRT_kwone","isResolved":false,"isOutdated":false,"path":"internal/api/rate.go","line":42,
     "comments":{"totalCount":2,"nodes":[
       {"databaseId":2001,"body":"this is off by one","diffHunk":"@@ -41,3 +41,4 @@\n+\tlimit++",
        "path":"internal/api/rate.go","line":42,
        "url":"https://github.com/acme/payments/pull/17#discussion_r2001",
        "createdAt":"2026-08-09T09:00:00Z","author":{"login":"alice"},
        "pullRequestReview":{"databaseId":555},"replyTo":null},
       {"databaseId":2002,"body":"fixed in 9a1c2f","diffHunk":"@@ -41,3 +41,4 @@\n+\tlimit++",
        "path":"internal/api/rate.go","line":42,
        "url":"https://github.com/acme/payments/pull/17#discussion_r2002",
        "createdAt":"2026-08-09T10:00:00Z","author":{"login":"dev"},
        "pullRequestReview":{"databaseId":555},"replyTo":{"databaseId":2001}}]}},
    {"id":"PRRT_kwtwo","isResolved":true,"isOutdated":true,"path":"README.md","line":null,"originalLine":4,
     "comments":{"totalCount":1,"nodes":[
       {"databaseId":2003,"body":"document the header","diffHunk":"@@ -1,3 +1,4 @@\n+# Rate limits",
        "path":"README.md","line":null,"originalLine":4,
        "url":"https://github.com/acme/payments/pull/17#discussion_r2003",
        "createdAt":"2026-08-09T09:05:00Z","author":{"login":"alice"},
        "pullRequestReview":{"databaseId":555},"replyTo":null}]}}
  ]}}}}}`

// TestListReviewThreadsGraphQL: the threads come back whole — file, line, hunk, replies, the
// review they belong to, and the resolution state that exists nowhere in REST.
func TestListReviewThreadsGraphQL(t *testing.T) {
	h := newHarness(t)
	sent := h.graphQLFixture(200, threadsFixture)

	threads, err := h.forge.ListReviewThreads(ctx(), testCreds, testRepo, 17)
	if err != nil {
		t.Fatalf("ListReviewThreads: %v", err)
	}
	if len(threads) != 2 {
		t.Fatalf("got %d threads; want 2", len(threads))
	}

	open := threads[0]
	if open.ID != "PRRT_kwone" || open.Path != "internal/api/rate.go" || open.Line != 42 {
		t.Errorf("open thread anchored wrong: %+v", open)
	}
	if open.ReviewID != 555 {
		t.Errorf("open thread review = %d, want 555", open.ReviewID)
	}
	if !strings.Contains(open.DiffHunk, "@@ -41,3 +41,4 @@") {
		t.Errorf("open thread hunk = %q", open.DiffHunk)
	}
	if !open.ResolutionKnown || open.Resolved {
		t.Errorf("open thread resolution = (known %v, resolved %v), want (true, false)",
			open.ResolutionKnown, open.Resolved)
	}
	if len(open.Comments) != 2 || open.Comments[1].AuthorLogin != "dev" ||
		open.Comments[1].InReplyToID != 2001 {
		t.Errorf("open thread comments = %+v", open.Comments)
	}
	if open.Author() != "alice" {
		t.Errorf("Author() = %q, want alice", open.Author())
	}

	resolved := threads[1]
	if !resolved.Resolved || !resolved.ResolutionKnown || !resolved.Outdated {
		t.Errorf("second thread = %+v, want resolved and outdated", resolved)
	}
	if resolved.Line != 4 {
		t.Errorf("an outdated thread reports its original line: got %d, want 4", resolved.Line)
	}

	h.mu.Lock()
	defer h.mu.Unlock()
	if len(*sent) != 1 {
		t.Fatalf("sent %d GraphQL requests; want 1", len(*sent))
	}
	req := (*sent)[0]
	if !strings.Contains(req.Query, "reviewThreads") || !strings.Contains(req.Query, "isResolved") {
		t.Errorf("query does not ask for the thread resolution: %s", req.Query)
	}
	if req.Variables["owner"] != "acme" || req.Variables["name"] != "payments" ||
		req.Variables["number"] != float64(17) {
		t.Errorf("variables = %+v", req.Variables)
	}
	if _, ok := req.Variables["cursor"]; ok {
		t.Errorf("the first page must not send a cursor: %+v", req.Variables)
	}
}

// restCommentsFixture is the same conversation as GitHub's REST listing carries it: reply
// pointers and diff hunks, but nothing at all about resolution.
const restCommentsFixture = `[
  {"id":2001,"pull_request_review_id":555,"user":{"login":"alice"},"body":"this is off by one",
   "path":"internal/api/rate.go","line":42,"diff_hunk":"@@ -41,3 +41,4 @@\n+\tlimit++",
   "html_url":"https://github.com/acme/payments/pull/17#discussion_r2001",
   "created_at":"2026-08-09T09:00:00Z","updated_at":"2026-08-09T09:00:00Z"},
  {"id":2003,"pull_request_review_id":555,"user":{"login":"alice"},"body":"document the header",
   "path":"README.md","line":0,"original_line":4,"diff_hunk":"@@ -1,3 +1,4 @@\n+# Rate limits",
   "html_url":"https://github.com/acme/payments/pull/17#discussion_r2003",
   "created_at":"2026-08-09T09:05:00Z","updated_at":"2026-08-09T09:05:00Z"},
  {"id":2002,"pull_request_review_id":556,"in_reply_to_id":2001,"user":{"login":"dev"},
   "body":"fixed in 9a1c2f","path":"internal/api/rate.go","line":42,
   "diff_hunk":"@@ -41,3 +41,4 @@\n+\tlimit++",
   "html_url":"https://github.com/acme/payments/pull/17#discussion_r2002",
   "created_at":"2026-08-09T10:00:00Z","updated_at":"2026-08-09T10:00:00Z"}
]`

// TestListReviewThreadsFallsBackToREST: when GraphQL is not available to the token, the threads
// are rebuilt from the REST comment listing — everything except resolution, which is reported
// as unknown rather than guessed at.
func TestListReviewThreadsFallsBackToREST(t *testing.T) {
	h := newHarness(t)
	h.graphQLFixture(200, `{"data":{"repository":null},"errors":[
		{"type":"FORBIDDEN","message":"Resource not accessible by personal access token"}]}`)
	h.fixture("GET /repos/acme/payments/pulls/17/comments", 200, restCommentsFixture, nil)

	threads, err := h.forge.ListReviewThreads(ctx(), testCreds, testRepo, 17)
	if err != nil {
		t.Fatalf("ListReviewThreads: %v", err)
	}
	if len(threads) != 2 {
		t.Fatalf("got %d threads; want 2 (the reply joins its parent)", len(threads))
	}
	first := threads[0]
	if len(first.Comments) != 2 || first.Comments[1].ID != 2002 {
		t.Errorf("the reply did not join its thread: %+v", first.Comments)
	}
	if first.Path != "internal/api/rate.go" || first.Line != 42 || first.ReviewID != 555 {
		t.Errorf("thread anchored wrong: %+v", first)
	}
	if !strings.Contains(first.DiffHunk, "@@ -41,3 +41,4 @@") {
		t.Errorf("hunk = %q", first.DiffHunk)
	}
	for _, th := range threads {
		if th.ResolutionKnown {
			t.Errorf("REST cannot know resolution, but the thread claims it does: %+v", th)
		}
	}
	if threads[1].Line != 4 {
		t.Errorf("a moved comment falls back to its original line: got %d", threads[1].Line)
	}
	if !strings.Contains(h.logText(), "falling back to the REST comment listing") {
		t.Errorf("the degradation was not logged:\n%s", h.logText())
	}
}

// TestGraphQLURL pins the endpoint derivation: api.github.com by default, the sibling of a
// GitHub Enterprise REST base, and a fixture server's own /graphql route.
func TestGraphQLURL(t *testing.T) {
	cases := []struct{ base, want string }{
		{"", "https://api.github.com/graphql"},
		{"https://github.acme.com/api/v3/", "https://github.acme.com/api/graphql"},
		{"http://127.0.0.1:8080/", "http://127.0.0.1:8080/graphql"},
	}
	for _, c := range cases {
		f := &Forge{baseURL: c.base}
		got, err := f.graphQLURL()
		if err != nil {
			t.Fatalf("graphQLURL(%q): %v", c.base, err)
		}
		if got != c.want {
			t.Errorf("graphQLURL(%q) = %q, want %q", c.base, got, c.want)
		}
	}
}

// TestListReviewCommentsCarriesTheReview: the repo-wide listing the poller drives now carries
// the review a comment belongs to, which is what lets the fragments be reassembled (LEXI-10).
func TestListReviewCommentsCarriesTheReview(t *testing.T) {
	h := newHarness(t)
	h.fixture("GET /repos/acme/payments/pulls/comments", 200, `[
		{"id":2001,"pull_request_review_id":555,"in_reply_to_id":1999,
		 "pull_request_url":"https://api.github.com/repos/acme/payments/pulls/17",
		 "user":{"login":"reviewer"},"body":"nit: rename this","path":"internal/main.go","line":12,
		 "diff_hunk":"@@ -11,2 +11,3 @@\n+\tx := 1",
		 "html_url":"https://github.com/acme/payments/pull/17#discussion_r2001",
		 "created_at":"2026-08-09T09:00:00Z","updated_at":"2026-08-09T09:30:00Z"}
	]`, nil)

	comments, err := h.forge.ListReviewComments(ctx(), testCreds, testRepo, parseFixtureTime(t))
	if err != nil {
		t.Fatalf("ListReviewComments: %v", err)
	}
	if len(comments) != 1 {
		t.Fatalf("got %d comments; want 1", len(comments))
	}
	cm := comments[0]
	if cm.ReviewID != 555 || cm.InReplyToID != 1999 || cm.SubjectNumber != 17 {
		t.Errorf("review/reply/subject mapped wrong: %+v", cm)
	}
	if !strings.Contains(cm.DiffHunk, "x := 1") {
		t.Errorf("diff hunk = %q", cm.DiffHunk)
	}
}

// parseFixtureTime is the `since` every listing fixture is written against.
func parseFixtureTime(t *testing.T) time.Time {
	t.Helper()
	return time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)
}
