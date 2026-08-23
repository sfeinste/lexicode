// reviewthreads.go reassembles a pull request's inline review conversation: the threads, with
// the file and line they anchor to, the diff hunk they were written against, every reply on
// them, and whether a human has marked them resolved (LEXI-10).
//
// It is NOT part of ports.ForgeProvider — like ReadFileIfExists and ListDir, it is an extra
// capability of this adapter that a caller reaches through a narrow seam of its own
// (contextmod.ReviewReader). The frozen port (contracts §2.2) stays as it is.
//
// Two paths, because thread resolution is not in GitHub's REST surface at all:
//
//   - GraphQL (`repository.pullRequest.reviewThreads`) is the real one: it is the only place
//     `isResolved` exists, and it returns the whole conversation in one round trip.
//   - The REST comment listing is the fallback for when GraphQL is unavailable to the token or
//     the host. It carries everything except resolution, so the threads it builds report
//     ResolutionKnown false — "not known to be resolved" is a weaker claim than "unresolved",
//     and the prompt renders it as such rather than pretending.
package github

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"

	gh "github.com/google/go-github/v90/github"

	"github.com/spruce/lexicode/internal/domain"
	"github.com/spruce/lexicode/internal/kernel/ports"
)

// threadPageSize bounds one GraphQL page of threads, and the comments read per thread. A
// review with more than 50 threads pages; a single thread with more than 50 comments is
// truncated and says so in the log — no realistic review reaches either.
const threadPageSize = 50

// maxThreadPages bounds the thread pagination the same way maxPages bounds the REST loops.
const maxThreadPages = 20

// ListReviewThreads returns pull request prNumber's inline review threads, oldest first, each
// with its comments in order. GraphQL first (the only source of thread resolution); the REST
// comment listing as a degraded fallback.
func (f *Forge) ListReviewThreads(ctx context.Context, c ports.Creds, r domain.RepoRef, prNumber int) ([]domain.ReviewThread, error) {
	threads, err := f.graphQLReviewThreads(ctx, c, r, prNumber)
	if err == nil {
		return threads, nil
	}
	if errors.Is(err, ports.ErrRateLimited) {
		// The budget is gone; a second listing would only confirm it.
		return nil, err
	}
	f.logger.Warn("github: review threads via GraphQL failed; falling back to the REST comment listing, so thread resolution will be unknown",
		slog.String("repo", r.String()), slog.Int("pr", prNumber),
		slog.String("error", err.Error()))
	return f.restReviewThreads(ctx, c, r, prNumber)
}

// ---------------------------------------------------------------------- GraphQL -----

// reviewThreadsQuery reads one page of a PR's review threads with their comments. Written out
// in full rather than built by string concatenation: the query IS the schema contract, and a
// reader should be able to check it against GitHub's docs without running the code.
const reviewThreadsQuery = `query($owner:String!,$name:String!,$number:Int!,$threads:Int!,$comments:Int!,$cursor:String){
  repository(owner:$owner,name:$name){
    pullRequest(number:$number){
      reviewThreads(first:$threads,after:$cursor){
        pageInfo{hasNextPage endCursor}
        nodes{
          id isResolved isOutdated path line originalLine
          comments(first:$comments){
            totalCount
            nodes{
              databaseId body diffHunk path line originalLine url createdAt
              author{login}
              pullRequestReview{databaseId}
              replyTo{databaseId}
            }
          }
        }
      }
    }
  }
}`

type graphQLRequest struct {
	Query     string         `json:"query"`
	Variables map[string]any `json:"variables"`
}

// graphQLError is one entry of a GraphQL response's errors array. GraphQL answers 200 with an
// errors array where REST would answer 4xx, so this is the real failure channel.
type graphQLError struct {
	Message string `json:"message"`
	Type    string `json:"type"`
}

type reviewThreadsResponse struct {
	Data struct {
		Repository *struct {
			PullRequest *struct {
				ReviewThreads struct {
					PageInfo struct {
						HasNextPage bool   `json:"hasNextPage"`
						EndCursor   string `json:"endCursor"`
					} `json:"pageInfo"`
					Nodes []graphQLThread `json:"nodes"`
				} `json:"reviewThreads"`
			} `json:"pullRequest"`
		} `json:"repository"`
	} `json:"data"`
	Errors []graphQLError `json:"errors"`
}

type graphQLThread struct {
	ID           string `json:"id"`
	IsResolved   bool   `json:"isResolved"`
	IsOutdated   bool   `json:"isOutdated"`
	Path         string `json:"path"`
	Line         *int   `json:"line"`
	OriginalLine *int   `json:"originalLine"`
	Comments     struct {
		TotalCount int              `json:"totalCount"`
		Nodes      []graphQLComment `json:"nodes"`
	} `json:"comments"`
}

type graphQLComment struct {
	DatabaseID   int64     `json:"databaseId"`
	Body         string    `json:"body"`
	DiffHunk     string    `json:"diffHunk"`
	Path         string    `json:"path"`
	Line         *int      `json:"line"`
	OriginalLine *int      `json:"originalLine"`
	URL          string    `json:"url"`
	CreatedAt    time.Time `json:"createdAt"`
	Author       *struct {
		Login string `json:"login"`
	} `json:"author"`
	PullRequestReview *struct {
		DatabaseID int64 `json:"databaseId"`
	} `json:"pullRequestReview"`
	ReplyTo *struct {
		DatabaseID int64 `json:"databaseId"`
	} `json:"replyTo"`
}

// graphQLReviewThreads runs the paged thread query and maps it onto the domain type.
func (f *Forge) graphQLReviewThreads(ctx context.Context, c ports.Creds, r domain.RepoRef, prNumber int) ([]domain.ReviewThread, error) {
	var out []domain.ReviewThread
	cursor := ""
	for page := 0; page < maxThreadPages; page++ {
		vars := map[string]any{
			"owner": r.Owner, "name": r.Name, "number": prNumber,
			"threads": threadPageSize, "comments": threadPageSize,
		}
		if cursor != "" {
			vars["cursor"] = cursor
		}
		var resp reviewThreadsResponse
		if err := f.graphQL(ctx, c, reviewThreadsQuery, vars, &resp); err != nil {
			return nil, wrapErr(fmt.Sprintf("list review threads on PR #%d", prNumber), r, err)
		}
		if len(resp.Errors) > 0 {
			return nil, wrapErr(fmt.Sprintf("list review threads on PR #%d", prNumber), r,
				errors.New(graphQLErrorText(resp.Errors)))
		}
		if resp.Data.Repository == nil || resp.Data.Repository.PullRequest == nil {
			return nil, wrapErr(fmt.Sprintf("list review threads on PR #%d", prNumber), r,
				errors.New("the response carried no pull request"))
		}
		threads := resp.Data.Repository.PullRequest.ReviewThreads
		for _, n := range threads.Nodes {
			if n.Comments.TotalCount > len(n.Comments.Nodes) {
				f.logger.Warn("github: review thread truncated",
					slog.String("repo", r.String()), slog.Int("pr", prNumber),
					slog.String("path", n.Path),
					slog.Int("shown", len(n.Comments.Nodes)),
					slog.Int("total", n.Comments.TotalCount))
			}
			out = append(out, mapGraphQLThread(n, prNumber))
		}
		if !threads.PageInfo.HasNextPage || threads.PageInfo.EndCursor == "" {
			break
		}
		cursor = threads.PageInfo.EndCursor
	}
	return out, nil
}

// mapGraphQLThread turns one GraphQL thread into the domain value. The thread's file, line,
// hunk and review are taken from its first comment when the thread node does not carry them —
// an outdated thread reports a null line, and the original line is the honest answer.
func mapGraphQLThread(n graphQLThread, prNumber int) domain.ReviewThread {
	t := domain.ReviewThread{
		ID:              n.ID,
		Path:            n.Path,
		Line:            firstInt(n.Line, n.OriginalLine),
		Outdated:        n.IsOutdated,
		Resolved:        n.IsResolved,
		ResolutionKnown: true,
	}
	for _, cn := range n.Comments.Nodes {
		cm := domain.Comment{
			ID:            cn.DatabaseID,
			SubjectNumber: prNumber,
			Body:          cn.Body,
			Path:          cn.Path,
			Line:          firstInt(cn.Line, cn.OriginalLine),
			URL:           cn.URL,
			CreatedAt:     cn.CreatedAt,
			UpdatedAt:     cn.CreatedAt,
			DiffHunk:      cn.DiffHunk,
		}
		if cn.Author != nil {
			cm.AuthorLogin = cn.Author.Login
		}
		if cn.PullRequestReview != nil {
			cm.ReviewID = cn.PullRequestReview.DatabaseID
		}
		if cn.ReplyTo != nil {
			cm.InReplyToID = cn.ReplyTo.DatabaseID
		}
		t.Comments = append(t.Comments, cm)
	}
	if len(t.Comments) > 0 {
		first := t.Comments[0]
		t.ReviewID = first.ReviewID
		t.DiffHunk = first.DiffHunk
		t.URL = first.URL
		if t.Path == "" {
			t.Path = first.Path
		}
		if t.Line == 0 {
			t.Line = first.Line
		}
	}
	return t
}

func firstInt(values ...*int) int {
	for _, v := range values {
		if v != nil && *v != 0 {
			return *v
		}
	}
	return 0
}

// graphQLErrorText renders a GraphQL errors array as one sentence.
func graphQLErrorText(errs []graphQLError) string {
	msgs := make([]string, 0, len(errs))
	for _, e := range errs {
		msg := e.Message
		if e.Type != "" {
			msg = e.Type + ": " + msg
		}
		msgs = append(msgs, msg)
	}
	return strings.Join(msgs, "; ")
}

// graphQL performs one GraphQL POST through the module's transport, so the rate-limit and
// retry policy that governs every REST call governs this one too. The token is registered with
// the redactor before it is put on the wire.
func (f *Forge) graphQL(ctx context.Context, c ports.Creds, query string, vars map[string]any, out any) error {
	f.redactor.Add(c.Token)
	endpoint, err := f.graphQLURL()
	if err != nil {
		return err
	}
	body, err := json.Marshal(graphQLRequest{Query: query, Variables: vars})
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	if c.Token != "" {
		req.Header.Set("Authorization", "Bearer "+c.Token)
	}

	resp, err := (&http.Client{Transport: f.transport}).Do(req)
	if err != nil {
		// http.Client wraps every transport error in *url.Error; unwrap so that the typed
		// *ports.RateLimitedError the transport returns stays reachable with errors.Is.
		var uerr *url.Error
		if errors.As(err, &uerr) && uerr.Err != nil {
			return uerr.Err
		}
		return err
	}
	defer func() { _ = resp.Body.Close() }()

	payload, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return err
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("graphql: %s: %s", resp.Status, firstLine(string(payload)))
	}
	if err := json.Unmarshal(payload, out); err != nil {
		return fmt.Errorf("graphql: decoding the response: %w", err)
	}
	return nil
}

// graphQLURL is the GraphQL endpoint for this adapter's base URL: api.github.com by default,
// and the sibling of a GitHub Enterprise REST base (…/api/v3/ → …/api/graphql). A fixture
// server's base URL becomes its /graphql route, which is what the tests serve.
func (f *Forge) graphQLURL() (string, error) {
	if f.baseURL == "" {
		return "https://api.github.com/graphql", nil
	}
	u, err := url.Parse(f.baseURL)
	if err != nil {
		return "", fmt.Errorf("github: base URL %q is not a URL: %w", f.baseURL, err)
	}
	path := strings.TrimSuffix(u.Path, "/")
	path = strings.TrimSuffix(path, "/v3")
	u.Path = path + "/graphql"
	return u.String(), nil
}

// ------------------------------------------------------------------------- REST -----

// restReviewThreads rebuilds the threads from the PR's review comments alone: every comment
// that replies to another joins that comment's thread, everything else starts one. Resolution
// is not in this data, so every thread it returns says so (ResolutionKnown false).
func (f *Forge) restReviewThreads(ctx context.Context, c ports.Creds, r domain.RepoRef, prNumber int) ([]domain.ReviewThread, error) {
	cl, err := f.client(c)
	if err != nil {
		return nil, err
	}
	opts := &gh.PullRequestListCommentsOptions{
		Sort: "created", Direction: "asc",
		ListOptions: gh.ListOptions{PerPage: pageSize},
	}
	var comments []domain.Comment
	for page := 0; page < maxPages; page++ {
		batch, resp, err := cl.PullRequests.ListComments(ctx, r.Owner, r.Name, prNumber, opts)
		if err != nil {
			return nil, wrapErr(fmt.Sprintf("list review comments on PR #%d", prNumber), r, err)
		}
		for _, cm := range batch {
			comments = append(comments, mapReviewComment(cm, prNumber))
		}
		if resp.NextPage == 0 {
			break
		}
		opts.Page = resp.NextPage
	}
	return threadsFromComments(comments), nil
}

// mapReviewComment maps one go-github review comment onto the domain value. subjectNumber
// overrides the URL-derived number when the caller already knows it (the per-PR listing).
func mapReviewComment(cm *gh.PullRequestComment, subjectNumber int) domain.Comment {
	number := subjectNumber
	if number == 0 {
		number = trailingNumber(cm.GetPullRequestURL())
	}
	return domain.Comment{
		ID:            cm.GetID(),
		SubjectNumber: number,
		AuthorLogin:   cm.GetUser().GetLogin(),
		Body:          cm.GetBody(),
		Path:          cm.GetPath(),
		Line:          commentLine(cm),
		URL:           cm.GetHTMLURL(),
		CreatedAt:     cm.GetCreatedAt().Time,
		UpdatedAt:     cm.GetUpdatedAt().Time,
		ReviewID:      cm.GetPullRequestReviewID(),
		InReplyToID:   cm.GetInReplyTo(),
		DiffHunk:      cm.GetDiffHunk(),
	}
}

// commentLine is the line the comment anchors to: the current line, or the original line when
// the diff has moved on and GitHub reports none.
func commentLine(cm *gh.PullRequestComment) int {
	if l := cm.GetLine(); l != 0 {
		return l
	}
	return cm.GetOriginalLine()
}

// threadsFromComments groups review comments into threads by their reply pointers, oldest
// thread first, comments inside a thread oldest first.
func threadsFromComments(comments []domain.Comment) []domain.ReviewThread {
	byID := make(map[int64]domain.Comment, len(comments))
	for _, cm := range comments {
		byID[cm.ID] = cm
	}
	// rootOf follows the reply chain to the comment that started the thread, with a hard hop
	// cap so that a cycle in the data cannot hang the caller.
	rootOf := func(cm domain.Comment) int64 {
		cur := cm
		for hops := 0; hops < 64; hops++ {
			if cur.InReplyToID == 0 {
				return cur.ID
			}
			parent, ok := byID[cur.InReplyToID]
			if !ok {
				return cur.InReplyToID // the parent is outside this listing; still one thread
			}
			cur = parent
		}
		return cur.ID
	}

	order := make([]int64, 0, len(comments))
	grouped := make(map[int64][]domain.Comment, len(comments))
	for _, cm := range comments {
		root := rootOf(cm)
		if _, seen := grouped[root]; !seen {
			order = append(order, root)
		}
		grouped[root] = append(grouped[root], cm)
	}

	out := make([]domain.ReviewThread, 0, len(order))
	for _, root := range order {
		group := grouped[root]
		sort.SliceStable(group, func(i, j int) bool {
			return group[i].CreatedAt.Before(group[j].CreatedAt)
		})
		first := group[0]
		out = append(out, domain.ReviewThread{
			ReviewID:        first.ReviewID,
			Path:            first.Path,
			Line:            first.Line,
			DiffHunk:        first.DiffHunk,
			URL:             first.URL,
			ResolutionKnown: false, // REST does not carry it; say so rather than guess
			Comments:        group,
		})
	}
	return out
}
