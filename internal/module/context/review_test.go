package contextmod

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/spruce/lexicode/internal/domain"
	"github.com/spruce/lexicode/internal/kernel/ports"
)

// fakeReviews is the forge seam: canned reviews and threads, or an outage.
type fakeReviews struct {
	reviews []domain.Review
	threads []domain.ReviewThread
	err     error

	prAsked []int // the PR numbers it was asked about, in order
}

func (f *fakeReviews) ListReviews(_ context.Context, _ ports.Creds, _ domain.RepoRef, n int) ([]domain.Review, error) {
	if f.err != nil {
		return nil, f.err
	}
	f.prAsked = append(f.prAsked, n)
	return f.reviews, nil
}

func (f *fakeReviews) ListReviewThreads(_ context.Context, _ ports.Creds, _ domain.RepoRef, n int) ([]domain.ReviewThread, error) {
	if f.err != nil {
		return nil, f.err
	}
	f.prAsked = append(f.prAsked, n)
	return f.threads, nil
}

// thread builds one inline thread with a hunk and an author comment.
func thread(id string, reviewID int64, path string, line int, body string, mutate func(*domain.ReviewThread)) domain.ReviewThread {
	t := domain.ReviewThread{
		ID: id, ReviewID: reviewID, Path: path, Line: line,
		DiffHunk:        fmt.Sprintf("@@ -%d,3 +%d,4 @@\n+\tsomething new", line-1, line-1),
		ResolutionKnown: true,
		URL:             "https://github.com/acme/payments/pull/219#discussion_r" + id,
		Comments: []domain.Comment{{
			ID: 1000 + int64(line), SubjectNumber: 219, AuthorLogin: "alice", Body: body,
			Path: path, Line: line, ReviewID: reviewID, CreatedAt: time.Now(),
		}},
	}
	if mutate != nil {
		mutate(&t)
	}
	return t
}

// reviewEvent is a `pull_request_review/submitted` event with the poller's payload shape.
func reviewEvent(reviewID, body string) *domain.Event {
	num := int64(219)
	payload := map[string]any{
		"pr":     map[string]any{"number": 219, "title": "Add rate limiting", "url": "https://github.com/acme/payments/pull/219"},
		"review": map[string]any{"id": reviewID, "author": "alice", "state": "changes_requested", "body": body},
	}
	return &domain.Event{
		ID: domain.NewID(), Kind: "pull_request_review", ActivityType: "submitted",
		SubjectKind: "pr", SubjectNumber: &num, Payload: mustRaw(payload),
	}
}

// commentEvent is one `pull_request_review_comment/created` fragment of a review.
func commentEvent(reviewID, commentID string) *domain.Event {
	num := int64(219)
	payload := map[string]any{
		"pr": map[string]any{"number": 219, "title": "Add rate limiting"},
		"comment": map[string]any{
			"id": commentID, "review_id": reviewID, "author": "alice",
			"body": "this is off by one", "path": "internal/api/rate.go", "line": 42,
		},
	}
	return &domain.Event{
		ID: domain.NewID(), Kind: "pull_request_review_comment", ActivityType: "created",
		SubjectKind: "pr", SubjectNumber: &num, Payload: mustRaw(payload),
	}
}

func mustRaw(v any) json.RawMessage {
	raw, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return raw
}

// fiveThreads is one review's worth of inline comments — the ticket's own example.
func fiveThreads(reviewID int64) []domain.ReviewThread {
	return []domain.ReviewThread{
		thread("t1", reviewID, "internal/api/rate.go", 42, "this is off by one", nil),
		thread("t2", reviewID, "internal/api/rate.go", 88, "swallow the error?", nil),
		thread("t3", reviewID, "internal/store/limits.go", 12, "index this column", nil),
		thread("t4", reviewID, "web/src/Rate.tsx", 30, "this state can go", nil),
		thread("t5", reviewID, "README.md", 4, "document the header", nil),
	}
}

// TestReviewProviderAssemblesWholeReview is LEXI-10's first and second acceptance criteria:
// one run caused by a review event gets the summary AND every inline thread — file, line and
// hunk — in ONE section, however many of them there are.
func TestReviewProviderAssemblesWholeReview(t *testing.T) {
	st := openStore(t)
	p := seedProject(t, st)
	sec := openSecrets(t, st)
	connectRepo(t, st, sec, p)

	forge := &fakeReviews{threads: fiveThreads(555)}
	prov := NewReviewProvider(st, sec, forge, discardLogger())
	items, err := prov.Resolve(context.Background(), ports.ContextRequest{
		ProjectID: p.ID, CausingEvent: reviewEvent("555", "Five things before this can land."),
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 {
		t.Fatalf("items = %d, want exactly one section", len(items))
	}
	it := items[0]
	if it.SourceKind != "review" || !it.Injected || it.Tokens <= 0 {
		t.Fatalf("item = %+v, want an injected review item with a token count", it)
	}
	if it.Reason != "review on PR #219" {
		t.Errorf("Reason = %q", it.Reason)
	}
	if it.Title != "Review by alice on PR #219" {
		t.Errorf("Title = %q", it.Title)
	}
	if it.SourceRef != "pr:219#review:555" {
		t.Errorf("SourceRef = %q", it.SourceRef)
	}
	if !strings.Contains(it.Body, "Five things before this can land.") {
		t.Errorf("the summary is missing from the section:\n%s", it.Body)
	}
	for _, want := range []string{
		"internal/api/rate.go:42", "internal/api/rate.go:88", "internal/store/limits.go:12",
		"web/src/Rate.tsx:30", "README.md:4",
		"this is off by one", "swallow the error?", "index this column",
		"this state can go", "document the header",
		"@@ -41,3 +41,4 @@", "```diff",
	} {
		if !strings.Contains(it.Body, want) {
			t.Errorf("the section is missing %q:\n%s", want, it.Body)
		}
	}
	if !strings.Contains(it.Body, "All 5 open thread(s)") {
		t.Errorf("the section does not say how many threads must be addressed:\n%s", it.Body)
	}
}

// TestReviewProviderAssemblesFromACommentFragment: the run may just as well be caused by one
// of the review's comments. It must still receive the whole review — the summary comes off the
// review listing, since a comment event does not carry it.
func TestReviewProviderAssemblesFromACommentFragment(t *testing.T) {
	st := openStore(t)
	p := seedProject(t, st)
	sec := openSecrets(t, st)
	connectRepo(t, st, sec, p)

	forge := &fakeReviews{
		reviews: []domain.Review{
			{ID: 554, PRNumber: 219, AuthorLogin: "bob", State: "COMMENTED", Body: "an older review"},
			{ID: 555, PRNumber: 219, AuthorLogin: "alice", State: "CHANGES_REQUESTED",
				Body: "Five things before this can land."},
		},
		threads: fiveThreads(555),
	}
	items, err := NewReviewProvider(st, sec, forge, discardLogger()).
		Resolve(context.Background(), ports.ContextRequest{
			ProjectID: p.ID, CausingEvent: commentEvent("555", "1042"),
		})
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 {
		t.Fatalf("items = %d, want one", len(items))
	}
	body := items[0].Body
	if !strings.Contains(body, "Five things before this can land.") {
		t.Errorf("the review summary is missing:\n%s", body)
	}
	if strings.Contains(body, "an older review") {
		t.Errorf("another review's summary leaked in:\n%s", body)
	}
	if !strings.Contains(body, "README.md:4") {
		t.Errorf("the other four threads are missing:\n%s", body)
	}
	if !strings.Contains(body, "requested changes on pull request #219") {
		t.Errorf("the review state is missing:\n%s", body)
	}
}

// TestReviewProviderExcludesResolvedThreads is the third acceptance criterion: a thread a human
// has resolved on the forge is not re-sent — it is counted and named as settled instead.
func TestReviewProviderExcludesResolvedThreads(t *testing.T) {
	st := openStore(t)
	p := seedProject(t, st)
	sec := openSecrets(t, st)
	connectRepo(t, st, sec, p)

	threads := fiveThreads(555)
	threads[0].Resolved = true
	threads[3].Resolved = true

	items, err := NewReviewProvider(st, sec, &fakeReviews{threads: threads}, discardLogger()).
		Resolve(context.Background(), ports.ContextRequest{
			ProjectID: p.ID, CausingEvent: reviewEvent("555", "Five things."),
		})
	if err != nil {
		t.Fatal(err)
	}
	body := items[0].Body
	for _, gone := range []string{"this is off by one", "this state can go"} {
		if strings.Contains(body, gone) {
			t.Errorf("resolved thread %q was re-sent:\n%s", gone, body)
		}
	}
	for _, kept := range []string{"swallow the error?", "index this column", "document the header"} {
		if !strings.Contains(body, kept) {
			t.Errorf("open thread %q is missing:\n%s", kept, body)
		}
	}
	if !strings.Contains(body, "2 further thread(s) are already resolved") {
		t.Errorf("the section does not account for the resolved threads:\n%s", body)
	}
}

// TestReviewProviderAllThreadsResolved: nothing outstanding is itself the answer, and it is
// said in words rather than by an empty section.
func TestReviewProviderAllThreadsResolved(t *testing.T) {
	st := openStore(t)
	p := seedProject(t, st)
	sec := openSecrets(t, st)
	connectRepo(t, st, sec, p)

	threads := fiveThreads(555)
	for i := range threads {
		threads[i].Resolved = true
	}
	items, err := NewReviewProvider(st, sec, &fakeReviews{threads: threads}, discardLogger()).
		Resolve(context.Background(), ports.ContextRequest{
			ProjectID: p.ID, CausingEvent: reviewEvent("555", "Five things."),
		})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(items[0].Body, "is already resolved; nothing is outstanding") {
		t.Errorf("body = %s", items[0].Body)
	}
}

// TestReviewProviderUnknownResolutionSaysSo: the REST fallback cannot tell resolved from open,
// and the section admits that rather than implying every thread is still live.
func TestReviewProviderUnknownResolutionSaysSo(t *testing.T) {
	st := openStore(t)
	p := seedProject(t, st)
	sec := openSecrets(t, st)
	connectRepo(t, st, sec, p)

	threads := fiveThreads(555)
	for i := range threads {
		threads[i].ResolutionKnown = false
	}
	items, err := NewReviewProvider(st, sec, &fakeReviews{threads: threads}, discardLogger()).
		Resolve(context.Background(), ports.ContextRequest{
			ProjectID: p.ID, CausingEvent: reviewEvent("555", "Five things."),
		})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(items[0].Body, "Which threads are resolved could not be read") {
		t.Errorf("body = %s", items[0].Body)
	}
}

// TestReviewProviderNoInlineComments is the fourth criterion's first half: a review that is
// summary only still produces a section, and says the review left no inline comments.
func TestReviewProviderNoInlineComments(t *testing.T) {
	st := openStore(t)
	p := seedProject(t, st)
	sec := openSecrets(t, st)
	connectRepo(t, st, sec, p)

	items, err := NewReviewProvider(st, sec, &fakeReviews{}, discardLogger()).
		Resolve(context.Background(), ports.ContextRequest{
			ProjectID: p.ID, CausingEvent: reviewEvent("555", "Looks good; ship it after CI."),
		})
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 {
		t.Fatalf("items = %d, want one", len(items))
	}
	if !strings.Contains(items[0].Body, "Looks good; ship it after CI.") {
		t.Errorf("the summary is missing:\n%s", items[0].Body)
	}
	if !strings.Contains(items[0].Body, "left no inline comments") {
		t.Errorf("body = %s", items[0].Body)
	}
}

// TestReviewProviderLoneComment is the fourth criterion's second half: a comment that belongs
// to no review still works — its own thread, with its replies, and nothing invented around it.
func TestReviewProviderLoneComment(t *testing.T) {
	st := openStore(t)
	p := seedProject(t, st)
	sec := openSecrets(t, st)
	connectRepo(t, st, sec, p)

	lone := domain.ReviewThread{
		Path: "internal/api/rate.go", Line: 42, ResolutionKnown: true,
		DiffHunk: "@@ -41,3 +41,4 @@\n+\tlimit++",
		Comments: []domain.Comment{
			{ID: 1042, AuthorLogin: "alice", Body: "this is off by one", Path: "internal/api/rate.go", Line: 42},
			{ID: 1043, AuthorLogin: "dev", Body: "fixed in 9a1c2f", InReplyToID: 1042},
		},
	}
	other := thread("t9", 900, "web/src/Rate.tsx", 30, "unrelated thread", nil)

	items, err := NewReviewProvider(st, sec, &fakeReviews{threads: []domain.ReviewThread{other, lone}}, discardLogger()).
		Resolve(context.Background(), ports.ContextRequest{
			ProjectID: p.ID, CausingEvent: commentEvent("", "1042"),
		})
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 {
		t.Fatalf("items = %d, want one", len(items))
	}
	body := items[0].Body
	if !strings.Contains(body, "this is off by one") {
		t.Errorf("the comment itself is missing:\n%s", body)
	}
	if !strings.Contains(body, "dev (reply):** fixed in 9a1c2f") {
		t.Errorf("the reply is missing:\n%s", body)
	}
	if strings.Contains(body, "unrelated thread") {
		t.Errorf("an unrelated thread was pulled in:\n%s", body)
	}
	if items[0].SourceRef != "pr:219#comment:1042" {
		t.Errorf("SourceRef = %q", items[0].SourceRef)
	}
}

// TestReviewProviderOutdatedThreadIsMarked: an outdated thread is still worth sending, but the
// agent is told the lines moved.
func TestReviewProviderOutdatedThreadIsMarked(t *testing.T) {
	st := openStore(t)
	p := seedProject(t, st)
	sec := openSecrets(t, st)
	connectRepo(t, st, sec, p)

	threads := []domain.ReviewThread{thread("t1", 555, "internal/api/rate.go", 42, "stale point", func(t *domain.ReviewThread) {
		t.Outdated = true
	})}
	items, err := NewReviewProvider(st, sec, &fakeReviews{threads: threads}, discardLogger()).
		Resolve(context.Background(), ports.ContextRequest{
			ProjectID: p.ID, CausingEvent: reviewEvent("555", ""),
		})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(items[0].Body, "outdated") {
		t.Errorf("body = %s", items[0].Body)
	}
}

// TestReviewProviderIgnoresOtherEvents: the provider contributes nothing to a run no review
// caused — a push, a manual run, and the agent's dry context preview.
func TestReviewProviderIgnoresOtherEvents(t *testing.T) {
	st := openStore(t)
	p := seedProject(t, st)
	sec := openSecrets(t, st)
	connectRepo(t, st, sec, p)
	prov := NewReviewProvider(st, sec, &fakeReviews{threads: fiveThreads(555)}, discardLogger())

	push := &domain.Event{ID: domain.NewID(), Kind: "pull_request", ActivityType: "synchronize",
		Payload: mustRaw(map[string]any{"pr": map[string]any{"number": 219}})}
	for _, req := range []ports.ContextRequest{
		{ProjectID: p.ID},                     // manual run
		{ProjectID: p.ID, Dry: true},          // the agent preview
		{ProjectID: p.ID, CausingEvent: push}, // a push
	} {
		items, err := prov.Resolve(context.Background(), req)
		if err != nil {
			t.Fatal(err)
		}
		if len(items) != 0 {
			t.Fatalf("req %+v yielded %d items, want none", req, len(items))
		}
	}
}

// TestReviewProviderDegradesToTheFragment: a forge outage must never fail an enqueue. The run
// still gets what the event itself carried, and is told the rest could not be read.
func TestReviewProviderDegradesToTheFragment(t *testing.T) {
	st := openStore(t)
	p := seedProject(t, st)
	sec := openSecrets(t, st)
	connectRepo(t, st, sec, p)

	down := &fakeReviews{err: errors.New("github: 502 bad gateway")}
	items, err := NewReviewProvider(st, sec, down, discardLogger()).
		Resolve(context.Background(), ports.ContextRequest{
			ProjectID: p.ID, CausingEvent: commentEvent("555", "1042"),
		})
	if err != nil {
		t.Fatalf("a forge outage failed the resolve: %v", err)
	}
	if len(items) != 1 {
		t.Fatalf("items = %d, want the fragment", len(items))
	}
	body := items[0].Body
	if !strings.Contains(body, "this is off by one") {
		t.Errorf("the event's own comment is missing:\n%s", body)
	}
	if !strings.Contains(body, "could not be reached") {
		t.Errorf("the section does not admit the gap:\n%s", body)
	}
}

// TestReviewProviderWithoutARepoStillSaysWhatItKnows: no connected repository is the same
// degradation as an outage, not an error.
func TestReviewProviderWithoutARepoStillSaysWhatItKnows(t *testing.T) {
	st := openStore(t)
	p := seedProject(t, st)
	sec := openSecrets(t, st)

	items, err := NewReviewProvider(st, sec, &fakeReviews{}, discardLogger()).
		Resolve(context.Background(), ports.ContextRequest{
			ProjectID: p.ID, CausingEvent: reviewEvent("555", "Five things."),
		})
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 || !strings.Contains(items[0].Body, "Five things.") {
		t.Fatalf("items = %+v", items)
	}
}

// discardLogger keeps the providers' warning lines out of the test output.
func discardLogger() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }
