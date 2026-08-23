// Package github is the GitHub ForgeProvider adapter (story S14, contracts §2.2).
//
// Everything GitHub-shaped stays inside this package: go-github types are mapped to
// internal/domain values at the boundary and never cross the port. The write path enforces, in
// order, the acting agent's permission grant (fail closed, before any network I/O), the D-9
// actor marker on every body, and a run_outputs + audit record after every successful write.
// There is no merge, no force-push and no approve: brief D6 is an absent capability here, and
// ReviewSpec.Event == "APPROVE" is a named refusal.
package github

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	gh "github.com/google/go-github/v90/github"

	"github.com/spruce/lexicode/internal/domain"
	"github.com/spruce/lexicode/internal/kernel"
	"github.com/spruce/lexicode/internal/kernel/audit"
	"github.com/spruce/lexicode/internal/kernel/auth"
	"github.com/spruce/lexicode/internal/kernel/ports"
)

// pageSize is the per-page value every list call uses.
const pageSize = 100

// maxPages bounds pagination loops so that a pathological repository cannot spin the poller
// forever. 50 pages of 100 is far beyond anything the V1 poll window needs.
const maxPages = 50

// Forge implements ports.ForgeProvider against the GitHub REST API.
type Forge struct {
	baseURL   string
	transport *transport
	redactor  *Redactor

	logger          *slog.Logger
	loggerDefaulted bool

	perms    PermissionLookup
	record   OutputRecorder
	auditRec AuditRecorder
	health   func(state kernel.ModuleState, reason string)
	now      func() string
}

// setLogger wraps l in the token-redacting handler; every line this module emits goes through
// it, which is what makes "the clone URL is never logged" a property instead of a convention.
func (f *Forge) setLogger(l *slog.Logger) {
	f.logger = slog.New(newRedactingHandler(l.Handler(), f.redactor)).With("module", moduleName)
	f.loggerDefaulted = l == slog.Default()
}

// ID implements ports.ForgeProvider.
func (f *Forge) ID() string { return moduleName }

// client builds a go-github client for one call's credentials. The token is registered with
// the redactor first, so it is scrubbed from any log line from this moment on. go-github's own
// pre-flight rate-limit bookkeeping is disabled — the module's transport owns that policy.
func (f *Forge) client(c ports.Creds) (*gh.Client, error) {
	f.redactor.Add(c.Token)
	opts := []gh.ClientOptionsFunc{
		// condTransport sits above the rate-limit transport: it applies a context-carried
		// If-None-Match to the poller's conditional listings (etag.go) and is inert for
		// every call that does not arm one.
		gh.WithTransport(&condTransport{base: f.transport}),
		gh.WithAuthToken(c.Token),
		gh.WithDisableRateLimitCheck(),
	}
	if f.baseURL != "" {
		base := f.baseURL
		opts = append(opts, gh.WithURLs(&base, &base))
	}
	cl, err := gh.NewClient(opts...)
	if err != nil {
		return nil, fmt.Errorf("github: build client: %w", err)
	}
	return cl, nil
}

// wrapErr adds context to an API error while keeping the typed errors (*ports.RateLimitedError
// in particular) reachable through errors.Is / errors.As.
func wrapErr(action string, r domain.RepoRef, err error) error {
	return fmt.Errorf("github: %s for %s: %w", action, r, err)
}

// ------------------------------------------------------------------------------ Verify -----

// Verify implements ports.ForgeProvider: it reads the repository, checks the token can do what
// Lexicode needs, and returns what the repos row and the About card want. Classic PATs
// advertise their scopes in X-OAuth-Scopes and are checked for "repo"; fine-grained tokens
// send no such header, so the calls themselves are the probe and a failure names what broke.
func (f *Forge) Verify(ctx context.Context, c ports.Creds, r domain.RepoRef) (ports.RepoInfo, error) {
	cl, err := f.client(c)
	if err != nil {
		return ports.RepoInfo{}, err
	}

	repo, resp, err := cl.Repositories.Get(ctx, r.Owner, r.Name)
	if err != nil {
		return ports.RepoInfo{}, wrapErr("read repository", r, err)
	}

	// A classic PAT always sends X-OAuth-Scopes (empty for a token with no scopes). Its
	// absence means a fine-grained token, which cannot be checked by name — the probes below
	// stand in.
	if scopes, present := oauthScopes(resp); present {
		if !hasScope(scopes, "repo") {
			return ports.RepoInfo{}, &ports.MissingScopeError{
				Scope: "repo",
				Detail: fmt.Sprintf("the token's scopes are [%s]; a classic PAT needs the full %q scope to read and write %s",
					strings.Join(scopes, ", "), "repo", r),
			}
		}
	} else {
		// Fine-grained token: probe the issue read the bootstrap import needs, and name the
		// probe when it fails.
		if _, _, err := cl.Issues.ListByRepo(ctx, r.Owner, r.Name, &gh.IssueListByRepoOptions{
			State:       "open",
			ListOptions: gh.ListOptions{PerPage: 1},
		}); err != nil {
			return ports.RepoInfo{}, &ports.MissingScopeError{
				Scope:  "issues:read",
				Detail: fmt.Sprintf("the fine-grained token could read %s but listing its issues failed (%v); grant the token Issues read access", r, err),
			}
		}
	}

	info := ports.RepoInfo{
		Owner:         r.Owner,
		Name:          repo.GetName(),
		DefaultBranch: repo.GetDefaultBranch(),
		Private:       repo.GetPrivate(),
	}
	if login := repo.GetOwner().GetLogin(); login != "" {
		info.Owner = login
	}

	if info.DefaultBranch != "" {
		commit, _, err := cl.Repositories.GetCommit(ctx, r.Owner, r.Name, info.DefaultBranch, &gh.ListOptions{PerPage: 1})
		if err != nil {
			return ports.RepoInfo{}, wrapErr("read head of default branch "+info.DefaultBranch, r, err)
		}
		info.HeadSHA = commit.GetSHA()
		info.HeadMessage = firstLine(commit.GetCommit().GetMessage())
	}
	return info, nil
}

// oauthScopes returns the classic-PAT scope list and whether the header was present at all.
func oauthScopes(resp *gh.Response) ([]string, bool) {
	if resp == nil {
		return nil, false
	}
	values, present := resp.Header["X-Oauth-Scopes"]
	if !present {
		return nil, false
	}
	var scopes []string
	for _, v := range values {
		for _, s := range strings.Split(v, ",") {
			if s = strings.TrimSpace(s); s != "" {
				scopes = append(scopes, s)
			}
		}
	}
	return scopes, true
}

func hasScope(scopes []string, want string) bool {
	for _, s := range scopes {
		if s == want {
			return true
		}
	}
	return false
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	return strings.TrimSpace(s)
}

// ------------------------------------------------------------------------------- reads -----

// ListPullRequests implements ports.ForgeProvider: PRs of any state updated at or after since,
// newest update first, paginated until the window is covered.
func (f *Forge) ListPullRequests(ctx context.Context, c ports.Creds, r domain.RepoRef, since time.Time) ([]domain.PullRequest, error) {
	cl, err := f.client(c)
	if err != nil {
		return nil, err
	}
	opts := &gh.PullRequestListOptions{
		State: "all", Sort: "updated", Direction: "desc",
		ListOptions: gh.ListOptions{PerPage: pageSize},
	}
	var out []domain.PullRequest
	for page := 0; page < maxPages; page++ {
		prs, resp, err := cl.PullRequests.List(ctx, r.Owner, r.Name, opts)
		if err != nil {
			return nil, wrapErr("list pull requests", r, err)
		}
		for _, pr := range prs {
			if pr.GetUpdatedAt().Before(since) {
				return out, nil // sorted by update desc: everything after this is older
			}
			out = append(out, mapPullRequest(pr))
		}
		if resp.NextPage == 0 {
			break
		}
		opts.Page = resp.NextPage
	}
	return out, nil
}

// ListReviews implements ports.ForgeProvider.
func (f *Forge) ListReviews(ctx context.Context, c ports.Creds, r domain.RepoRef, prNumber int) ([]domain.Review, error) {
	cl, err := f.client(c)
	if err != nil {
		return nil, err
	}
	opts := &gh.ListOptions{PerPage: pageSize}
	var out []domain.Review
	for page := 0; page < maxPages; page++ {
		reviews, resp, err := cl.PullRequests.ListReviews(ctx, r.Owner, r.Name, prNumber, opts)
		if err != nil {
			return nil, wrapErr(fmt.Sprintf("list reviews on PR #%d", prNumber), r, err)
		}
		for _, rev := range reviews {
			out = append(out, mapReview(rev, prNumber))
		}
		if resp.NextPage == 0 {
			break
		}
		opts.Page = resp.NextPage
	}
	return out, nil
}

// ListReviewComments implements ports.ForgeProvider: the repository's PR review comments
// updated at or after since.
func (f *Forge) ListReviewComments(ctx context.Context, c ports.Creds, r domain.RepoRef, since time.Time) ([]domain.Comment, error) {
	cl, err := f.client(c)
	if err != nil {
		return nil, err
	}
	opts := &gh.PullRequestListCommentsOptions{
		Sort: "updated", Direction: "asc", Since: since,
		ListOptions: gh.ListOptions{PerPage: pageSize},
	}
	var out []domain.Comment
	for page := 0; page < maxPages; page++ {
		// Number 0 lists across the whole repository, which is what the poll window wants.
		comments, resp, err := cl.PullRequests.ListComments(ctx, r.Owner, r.Name, 0, opts)
		if err != nil {
			return nil, wrapErr("list review comments", r, err)
		}
		for _, cm := range comments {
			out = append(out, domain.Comment{
				ID:            cm.GetID(),
				SubjectNumber: trailingNumber(cm.GetPullRequestURL()),
				AuthorLogin:   cm.GetUser().GetLogin(),
				Body:          cm.GetBody(),
				Path:          cm.GetPath(),
				Line:          cm.GetLine(),
				URL:           cm.GetHTMLURL(),
				CreatedAt:     cm.GetCreatedAt().Time,
				UpdatedAt:     cm.GetUpdatedAt().Time,
			})
		}
		if resp.NextPage == 0 {
			break
		}
		opts.Page = resp.NextPage
	}
	return out, nil
}

// ListIssueComments implements ports.ForgeProvider: the repository's issue and PR conversation
// comments updated at or after since.
func (f *Forge) ListIssueComments(ctx context.Context, c ports.Creds, r domain.RepoRef, since time.Time) ([]domain.Comment, error) {
	cl, err := f.client(c)
	if err != nil {
		return nil, err
	}
	opts := &gh.IssueListCommentsOptions{
		Sort: gh.Ptr("updated"), Direction: gh.Ptr("asc"), Since: &since,
		ListOptions: gh.ListOptions{PerPage: pageSize},
	}
	var out []domain.Comment
	for page := 0; page < maxPages; page++ {
		comments, resp, err := cl.Issues.ListComments(ctx, r.Owner, r.Name, 0, opts)
		if err != nil {
			return nil, wrapErr("list issue comments", r, err)
		}
		for _, cm := range comments {
			out = append(out, domain.Comment{
				ID:            cm.GetID(),
				SubjectNumber: trailingNumber(cm.GetIssueURL()),
				AuthorLogin:   cm.GetUser().GetLogin(),
				Body:          cm.GetBody(),
				URL:           cm.GetHTMLURL(),
				CreatedAt:     cm.GetCreatedAt().Time,
				UpdatedAt:     cm.GetUpdatedAt().Time,
			})
		}
		if resp.NextPage == 0 {
			break
		}
		opts.Page = resp.NextPage
	}
	return out, nil
}

// ListCheckSuites implements ports.ForgeProvider.
func (f *Forge) ListCheckSuites(ctx context.Context, c ports.Creds, r domain.RepoRef, sha string) ([]domain.CheckSuite, error) {
	cl, err := f.client(c)
	if err != nil {
		return nil, err
	}
	opts := &gh.ListCheckSuiteOptions{ListOptions: gh.ListOptions{PerPage: pageSize}}
	var out []domain.CheckSuite
	for page := 0; page < maxPages; page++ {
		results, resp, err := cl.Checks.ListCheckSuitesForRef(ctx, r.Owner, r.Name, sha, opts)
		if err != nil {
			return nil, wrapErr("list check suites for "+sha, r, err)
		}
		for _, cs := range results.CheckSuites {
			out = append(out, domain.CheckSuite{
				ID:         cs.GetID(),
				HeadSHA:    cs.GetHeadSHA(),
				HeadBranch: cs.GetHeadBranch(),
				Status:     cs.GetStatus(),
				Conclusion: cs.GetConclusion(),
				App:        cs.GetApp().GetName(),
				URL:        cs.GetURL(),
				UpdatedAt:  cs.GetUpdatedAt().Time,
			})
		}
		if resp.NextPage == 0 {
			break
		}
		opts.Page = resp.NextPage
	}
	return out, nil
}

// ListOpenIssues implements ports.ForgeProvider. GitHub's issue listing includes pull requests;
// they are filtered out here because an importable ticket is not a PR.
func (f *Forge) ListOpenIssues(ctx context.Context, c ports.Creds, r domain.RepoRef) ([]domain.Issue, error) {
	cl, err := f.client(c)
	if err != nil {
		return nil, err
	}
	opts := &gh.IssueListByRepoOptions{State: "open", ListOptions: gh.ListOptions{PerPage: pageSize}}
	var out []domain.Issue
	for page := 0; page < maxPages; page++ {
		issues, resp, err := cl.Issues.ListByRepo(ctx, r.Owner, r.Name, opts)
		if err != nil {
			return nil, wrapErr("list open issues", r, err)
		}
		for _, is := range issues {
			if is.IsPullRequest() {
				continue
			}
			issue := domain.Issue{
				Number:      is.GetNumber(),
				Title:       is.GetTitle(),
				Body:        is.GetBody(),
				AuthorLogin: is.GetUser().GetLogin(),
				URL:         is.GetHTMLURL(),
				CreatedAt:   is.GetCreatedAt().Time,
				UpdatedAt:   is.GetUpdatedAt().Time,
			}
			for _, l := range is.Labels {
				issue.Labels = append(issue.Labels, l.GetName())
			}
			out = append(out, issue)
		}
		if resp.NextPage == 0 {
			break
		}
		// IssueListByRepoOptions embeds both cursor and offset pagination; name the offset one.
		opts.ListOptions.Page = resp.NextPage
	}
	return out, nil
}

// ReadFile implements ports.ForgeProvider: one file's bytes at ref.
func (f *Forge) ReadFile(ctx context.Context, c ports.Creds, r domain.RepoRef, ref, path string) ([]byte, error) {
	cl, err := f.client(c)
	if err != nil {
		return nil, err
	}
	file, dir, _, err := cl.Repositories.GetContents(ctx, r.Owner, r.Name, path,
		&gh.RepositoryContentGetOptions{Ref: ref})
	if err != nil {
		return nil, wrapErr("read file "+path+" at "+ref, r, err)
	}
	if file == nil || dir != nil {
		return nil, fmt.Errorf("github: %s in %s is a directory, not a file", path, r)
	}
	content, err := file.GetContent()
	if err != nil {
		return nil, wrapErr("decode file "+path, r, err)
	}
	return []byte(content), nil
}

// ReadFileIfExists is ReadFile with an existence answer: absent files come back (nil, false,
// nil) instead of a wrapped 404. Like ListDir below it is NOT part of ports.ForgeProvider —
// bootstrap doc detection probes a list of well-known paths, and "not there" is the common
// case, not an error. Reached through the bootstrap service's DocLister seam.
func (f *Forge) ReadFileIfExists(ctx context.Context, c ports.Creds, r domain.RepoRef, ref, path string) ([]byte, bool, error) {
	cl, err := f.client(c)
	if err != nil {
		return nil, false, err
	}
	file, dir, resp, err := cl.Repositories.GetContents(ctx, r.Owner, r.Name, path,
		&gh.RepositoryContentGetOptions{Ref: ref})
	if err != nil {
		if resp != nil && resp.StatusCode == 404 {
			return nil, false, nil
		}
		return nil, false, wrapErr("read file "+path+" at "+ref, r, err)
	}
	if file == nil || dir != nil {
		return nil, false, nil // a directory is not the instruction file being probed for
	}
	content, err := file.GetContent()
	if err != nil {
		return nil, false, wrapErr("decode file "+path, r, err)
	}
	return []byte(content), true, nil
}

// ListDir returns one directory's entries at ref, or an empty listing when the path does not
// exist. It is NOT part of ports.ForgeProvider: the frozen port (contracts §2.2) has ReadFile
// only, and bootstrap doc detection (S15) needs a bounded directory listing for
// .cursor/rules/*, docs/** and .github/workflows/*. Rather than widening the frozen port, the
// concrete adapter exposes this extra method and cmd/lexicode hands it to the bootstrap
// service through its narrow DocLister seam — flagged in the S15 report as a candidate for a
// future port revision.
func (f *Forge) ListDir(ctx context.Context, c ports.Creds, r domain.RepoRef, ref, path string) ([]domain.DirEntry, error) {
	cl, err := f.client(c)
	if err != nil {
		return nil, err
	}
	file, dir, resp, err := cl.Repositories.GetContents(ctx, r.Owner, r.Name, path,
		&gh.RepositoryContentGetOptions{Ref: ref})
	if err != nil {
		if resp != nil && resp.StatusCode == 404 {
			return nil, nil // an absent directory is "nothing detected", not a failure
		}
		return nil, wrapErr("list directory "+path+" at "+ref, r, err)
	}
	if file != nil {
		return nil, fmt.Errorf("github: %s in %s is a file, not a directory", path, r)
	}
	out := make([]domain.DirEntry, 0, len(dir))
	for _, e := range dir {
		out = append(out, domain.DirEntry{
			Name: e.GetName(),
			Path: e.GetPath(),
			Type: e.GetType(), // "file" | "dir"
		})
	}
	return out, nil
}

// ------------------------------------------------------------------------------ writes -----
//
// Every write follows contracts §2.2 in order: (1) the grant check, before any network I/O, so
// a denied write costs nothing; (2) the D-9 marker appended to the body; (3) a run_outputs row
// and an audit row after the forge accepted the write.

// OpenPullRequest implements ports.ForgeProvider. Requires the open_prs grant.
func (f *Forge) OpenPullRequest(ctx context.Context, c ports.Creds, r domain.RepoRef, a domain.Actor, spec ports.PRSpec) (domain.PullRequest, error) {
	if err := f.checkGrant(ctx, a, "open_prs", func(p domain.AgentPermissions) bool { return p.OpenPRs }); err != nil {
		return domain.PullRequest{}, err
	}
	cl, err := f.client(c)
	if err != nil {
		return domain.PullRequest{}, err
	}
	created, _, err := cl.PullRequests.Create(ctx, r.Owner, r.Name, gh.CreatePullRequest{
		Title: gh.Ptr(spec.Title),
		Head:  spec.Head,
		Base:  spec.Base,
		Body:  gh.Ptr(withMarker(spec.Body, a)),
		Draft: gh.Ptr(spec.Draft),
	})
	if err != nil {
		return domain.PullRequest{}, wrapErr("open pull request", r, err)
	}
	pr := mapPullRequest(created)
	f.recordWrite(ctx, r, a, "forge.pr.open", domain.OutputPullRequest,
		strconv.Itoa(pr.Number), pr.URL, fmt.Sprintf("opened PR #%d: %s", pr.Number, pr.Title))
	return pr, nil
}

// CommentOnPullRequest implements ports.ForgeProvider. Requires the comment_prs grant.
func (f *Forge) CommentOnPullRequest(ctx context.Context, c ports.Creds, r domain.RepoRef, a domain.Actor, n int, body string) (domain.Comment, error) {
	if err := f.checkGrant(ctx, a, "comment_prs", func(p domain.AgentPermissions) bool { return p.CommentPRs }); err != nil {
		return domain.Comment{}, err
	}
	cl, err := f.client(c)
	if err != nil {
		return domain.Comment{}, err
	}
	created, _, err := cl.Issues.CreateComment(ctx, r.Owner, r.Name, n,
		&gh.IssueComment{Body: gh.Ptr(withMarker(body, a))})
	if err != nil {
		return domain.Comment{}, wrapErr(fmt.Sprintf("comment on PR #%d", n), r, err)
	}
	comment := domain.Comment{
		ID:            created.GetID(),
		SubjectNumber: n,
		AuthorLogin:   created.GetUser().GetLogin(),
		Body:          created.GetBody(),
		URL:           created.GetHTMLURL(),
		CreatedAt:     created.GetCreatedAt().Time,
		UpdatedAt:     created.GetUpdatedAt().Time,
	}
	f.recordWrite(ctx, r, a, "forge.pr.comment", domain.OutputComment,
		strconv.FormatInt(comment.ID, 10), comment.URL, fmt.Sprintf("commented on PR #%d", n))
	return comment, nil
}

// SubmitReview implements ports.ForgeProvider. Requires the submit_reviews grant. APPROVE is
// refused before anything else — including the grant check — because no permission unlocks it:
// like the absent merge method, self-approval is a capability this system does not have
// (brief D6), not a setting.
func (f *Forge) SubmitReview(ctx context.Context, c ports.Creds, r domain.RepoRef, a domain.Actor, n int, rev ports.ReviewSpec) (domain.Review, error) {
	event := strings.ToUpper(strings.TrimSpace(rev.Event))
	switch event {
	case "APPROVE":
		return domain.Review{}, ports.ErrSelfApprovalForbidden
	case "COMMENT", "REQUEST_CHANGES":
		// allowed
	default:
		return domain.Review{}, fmt.Errorf(
			"github: review event %q is not supported; use COMMENT or REQUEST_CHANGES", rev.Event)
	}
	if err := f.checkGrant(ctx, a, "submit_reviews", func(p domain.AgentPermissions) bool { return p.SubmitReviews }); err != nil {
		return domain.Review{}, err
	}
	cl, err := f.client(c)
	if err != nil {
		return domain.Review{}, err
	}
	created, resp, err := cl.PullRequests.CreateReview(ctx, r.Owner, r.Name, n, &gh.PullRequestReviewRequest{
		Body:  gh.Ptr(withMarker(rev.Body, a)),
		Event: gh.Ptr(event),
	})
	if err != nil {
		// 422 on a review is GitHub refusing the EVENT, not the request: the canonical case
		// is REQUEST_CHANGES from the pull request's own author, which under D-9's single
		// project PAT every agent reviewing another agent's work is. Nothing was written,
		// and the caller can retry the same body as a COMMENT — so this one status is
		// classified rather than wrapped, and the caller decides.
		if resp != nil && resp.StatusCode == http.StatusUnprocessableEntity {
			return domain.Review{}, &ports.ReviewEventRejectedError{
				Event:  event,
				Detail: reviewRejectionDetail(err),
			}
		}
		return domain.Review{}, wrapErr(fmt.Sprintf("submit review on PR #%d", n), r, err)
	}
	review := mapReview(created, n)
	f.recordWrite(ctx, r, a, "forge.pr.review", domain.OutputReview,
		strconv.FormatInt(review.ID, 10), review.URL,
		fmt.Sprintf("submitted a %s review on PR #%d", event, n))
	return review, nil
}

// reviewRejectionDetail pulls GitHub's own words out of a 422 so the run output says why the
// event was refused rather than just that it was.
func reviewRejectionDetail(err error) string {
	var er *gh.ErrorResponse
	if !errors.As(err, &er) {
		return ""
	}
	parts := make([]string, 0, len(er.Errors)+1)
	if msg := strings.TrimSpace(er.Message); msg != "" {
		parts = append(parts, msg)
	}
	for _, e := range er.Errors {
		if m := strings.TrimSpace(e.Message); m != "" {
			parts = append(parts, m)
		}
	}
	return strings.Join(parts, "; ")
}

// CloneURL implements ports.ForgeProvider: the x-access-token form the container clones with.
// The token is registered with the redactor before the URL exists, so even a caller that logs
// the result through this module's logger emits the placeholder. When the module runs against
// a BaseURL override (GitHub Enterprise, or a fixture server speaking git smart-HTTP), the
// clone URL is derived from that override's scheme and host instead of github.com — the same
// host the API calls hit is the host git talks to.
func (f *Forge) CloneURL(_ context.Context, c ports.Creds, r domain.RepoRef) (string, error) {
	f.redactor.Add(c.Token)
	if c.Token == "" {
		return "", fmt.Errorf("github: no token to build a clone URL for %s", r)
	}
	scheme, host := "https", "github.com"
	if f.baseURL != "" {
		u, err := url.Parse(f.baseURL)
		if err != nil || u.Host == "" {
			return "", fmt.Errorf("github: cannot derive a clone host from base URL %q", f.baseURL)
		}
		scheme, host = u.Scheme, u.Host
	}
	return fmt.Sprintf("%s://x-access-token:%s@%s/%s/%s.git",
		scheme, c.Token, host, r.Owner, r.Name), nil
}

// ----------------------------------------------------------------------------- helpers -----

// checkGrant is write step 1: resolve the acting agent's permissions and fail closed. It runs
// before any client is built, so a denied write provably makes zero network calls.
func (f *Forge) checkGrant(ctx context.Context, a domain.Actor, grant string, allowed func(domain.AgentPermissions) bool) error {
	if f.perms == nil {
		return fmt.Errorf("github: no permission lookup is configured; refusing the write for agent %s", a.AgentID)
	}
	perms, err := f.perms(ctx, a.AgentID)
	if err != nil {
		return fmt.Errorf("github: resolve permissions of agent %s: %w", a.AgentID, err)
	}
	if !allowed(perms) {
		return &ports.PermissionDeniedError{AgentID: a.AgentID, Grant: grant}
	}
	return nil
}

// withMarker is write step 2: the D-9 actor marker, appended after a blank line.
func withMarker(body string, a domain.Actor) string {
	body = strings.TrimRight(body, "\n")
	if body == "" {
		return a.Marker()
	}
	return body + "\n\n" + a.Marker()
}

// recordWrite is write step 3: the run_outputs row and the audit row. The forge write already
// happened, so failures here are logged loudly rather than returned — surfacing an error now
// would invite a retry that duplicates the PR or comment.
func (f *Forge) recordWrite(ctx context.Context, r domain.RepoRef, a domain.Actor, action string, kind domain.RunOutputKind, ref, url, summary string) {
	if f.record != nil {
		out := domain.RunOutput{
			ID: domain.NewID(), RunID: a.RunID, Kind: kind,
			Ref: ref, URL: url, Summary: summary, CreatedAt: f.now(),
		}
		if err := f.record(ctx, out); err != nil {
			f.logger.Error("github: forge write succeeded but the run output was not recorded",
				slog.String("action", action), slog.String("ref", ref),
				slog.String("error", err.Error()))
		}
	}
	if f.auditRec != nil {
		target := audit.Target{
			Kind: string(kind), ID: ref,
			Note: fmt.Sprintf("agent %s, run %s, repo %s", a.AgentID, a.RunID, r),
		}
		after := map[string]string{
			"url": url, "summary": summary, "agent_id": a.AgentID, "run_id": a.RunID,
		}
		// Attribution (D-9, S37 acceptance): the entry names the AGENT as the actor, never
		// the human whose token the write rode on. The audit writer reads the actor from the
		// context, so the agent is stamped here — the one place every forge write passes.
		if a.AgentID != "" {
			ctx = auth.WithActor(ctx, auth.Actor{Kind: domain.ActorAgent, ID: a.AgentID})
		}
		if err := f.auditRec(ctx, action, target, nil, after); err != nil {
			f.logger.Error("github: forge write succeeded but the audit row was not written",
				slog.String("action", action), slog.String("ref", ref),
				slog.String("error", err.Error()))
		}
	}
}

// mapPullRequest converts a go-github PR at the port boundary.
func mapPullRequest(pr *gh.PullRequest) domain.PullRequest {
	labels := make([]string, 0, len(pr.Labels))
	for _, l := range pr.Labels {
		labels = append(labels, l.GetName())
	}
	return domain.PullRequest{
		Number:       pr.GetNumber(),
		Title:        pr.GetTitle(),
		Body:         pr.GetBody(),
		State:        pr.GetState(),
		Draft:        pr.GetDraft(),
		Merged:       pr.GetMerged() || pr.MergedAt != nil,
		AuthorLogin:  pr.GetUser().GetLogin(),
		HeadRef:      pr.GetHead().GetRef(),
		HeadSHA:      pr.GetHead().GetSHA(),
		BaseRef:      pr.GetBase().GetRef(),
		Labels:       labels,
		URL:          pr.GetHTMLURL(),
		CreatedAt:    pr.GetCreatedAt().Time,
		UpdatedAt:    pr.GetUpdatedAt().Time,
		Additions:    pr.GetAdditions(),
		Deletions:    pr.GetDeletions(),
		ChangedFiles: pr.GetChangedFiles(),
	}
}

// mapReview converts a go-github review at the port boundary.
func mapReview(rev *gh.PullRequestReview, prNumber int) domain.Review {
	return domain.Review{
		ID:          rev.GetID(),
		PRNumber:    prNumber,
		AuthorLogin: rev.GetUser().GetLogin(),
		State:       rev.GetState(),
		Body:        rev.GetBody(),
		URL:         rev.GetHTMLURL(),
		SubmittedAt: rev.GetSubmittedAt().Time,
	}
}

// trailingNumber parses the issue/PR number off the end of an API URL like
// ".../repos/o/r/pulls/17". Zero when the URL does not end in a number.
func trailingNumber(url string) int {
	i := strings.LastIndexByte(url, '/')
	if i < 0 || i == len(url)-1 {
		return 0
	}
	n, err := strconv.Atoi(url[i+1:])
	if err != nil {
		return 0
	}
	return n
}

// ------------------------------------------------------------------- poller extras -----

// GetPullRequest reads one pull request in full. Unlike the list endpoint, the detail carries
// additions/deletions/changed_files, which the normalized pr payload (contracts §4) exposes.
// Poller-only capability on the concrete adapter, like ListDir — the frozen ForgeProvider port
// does not carry it.
func (f *Forge) GetPullRequest(ctx context.Context, c ports.Creds, r domain.RepoRef, number int) (domain.PullRequest, error) {
	cl, err := f.client(c)
	if err != nil {
		return domain.PullRequest{}, err
	}
	pr, _, err := cl.PullRequests.Get(ctx, r.Owner, r.Name, number)
	if err != nil {
		return domain.PullRequest{}, wrapErr(fmt.Sprintf("read PR #%d", number), r, err)
	}
	return mapPullRequest(pr), nil
}

// CommitEmails returns the author email, committer email and message of one commit — the
// emails are the D-9 attribution signal for pushes (an agent's commits carry its
// git_author_email), and the message is where the loop guard's skip token may ride
// (`pr.head_commit_message`, S27). One API read serves both. Poller-only capability on the
// concrete adapter.
func (f *Forge) CommitEmails(ctx context.Context, c ports.Creds, r domain.RepoRef, sha string) (author, committer, message string, err error) {
	cl, err := f.client(c)
	if err != nil {
		return "", "", "", err
	}
	commit, _, err := cl.Repositories.GetCommit(ctx, r.Owner, r.Name, sha, &gh.ListOptions{PerPage: 1})
	if err != nil {
		return "", "", "", wrapErr("read commit "+sha, r, err)
	}
	inner := commit.GetCommit()
	return inner.GetAuthor().GetEmail(), inner.GetCommitter().GetEmail(), inner.GetMessage(), nil
}
