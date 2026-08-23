package ports

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/spruce/lexicode/internal/domain"
)

// ForgeProvider is a git host: pull requests, reviews, comments, checks and clone URLs.
// Transcribed verbatim from contracts §2.2 (story S14); the domain value types live in
// internal/domain so that no SDK type ever crosses this port.
//
// There is no Merge, no ForcePush and no Approve-as-final-say method: brief D6 is implemented
// as an absent capability, not as a permission check. ReviewSpec.Event accepts COMMENT and
// REQUEST_CHANGES only; APPROVE is rejected at the adapter with ErrSelfApprovalForbidden.
//
// Every write method, in order:
//  1. Checks the acting agent's permissions and returns ErrPermissionDenied with the missing
//     grant named.
//  2. Appends the actor marker (D-9, domain.Actor.Marker) to the body.
//  3. Writes an audit row and a run_outputs row.
type ForgeProvider interface {
	// ID is the stable identifier, e.g. "github". It must be unique across registered forges.
	ID() string

	// Verify checks that the credentials can see and write the repository, and returns what the
	// About card needs. A token missing a scope fails with an error naming the missing scope.
	Verify(ctx context.Context, c Creds, r domain.RepoRef) (RepoInfo, error)

	// Read — used by the poller and by UI surfaces.

	// ListPullRequests returns pull requests (any state) updated at or after since.
	ListPullRequests(ctx context.Context, c Creds, r domain.RepoRef, since time.Time) ([]domain.PullRequest, error)
	// ListReviews returns every submitted review on one pull request.
	ListReviews(ctx context.Context, c Creds, r domain.RepoRef, prNumber int) ([]domain.Review, error)
	// ListReviewComments returns the repository's PR review comments updated at or after since.
	ListReviewComments(ctx context.Context, c Creds, r domain.RepoRef, since time.Time) ([]domain.Comment, error)
	// ListIssueComments returns the repository's issue/PR conversation comments updated at or
	// after since.
	ListIssueComments(ctx context.Context, c Creds, r domain.RepoRef, since time.Time) ([]domain.Comment, error)
	// ListCheckSuites returns the check suites for one head SHA.
	ListCheckSuites(ctx context.Context, c Creds, r domain.RepoRef, sha string) ([]domain.CheckSuite, error)
	// ListOpenIssues returns the repository's open issues, excluding pull requests
	// (bootstrap import, S15).
	ListOpenIssues(ctx context.Context, c Creds, r domain.RepoRef) ([]domain.Issue, error)
	// ReadFile returns one file's raw bytes at ref (bootstrap doc detection, S15).
	ReadFile(ctx context.Context, c Creds, r domain.RepoRef, ref, path string) ([]byte, error)

	// Write — every method takes the acting agent so the marker (D-9) is never forgotten.

	// OpenPullRequest opens a PR. Requires the open_prs grant.
	OpenPullRequest(ctx context.Context, c Creds, r domain.RepoRef, a domain.Actor, spec PRSpec) (domain.PullRequest, error)
	// CommentOnPullRequest comments on PR n's conversation. Requires the comment_prs grant.
	CommentOnPullRequest(ctx context.Context, c Creds, r domain.RepoRef, a domain.Actor, n int, body string) (domain.Comment, error)
	// SubmitReview submits a COMMENT or REQUEST_CHANGES review on PR n. Requires the
	// submit_reviews grant. APPROVE is ErrSelfApprovalForbidden, always.
	SubmitReview(ctx context.Context, c Creds, r domain.RepoRef, a domain.Actor, n int, rev ReviewSpec) (domain.Review, error)

	// CloneURL returns an authenticated clone URL with the token embedded, for the container.
	// The result must never be logged; the adapter's logger redacts the token defensively.
	CloneURL(ctx context.Context, c Creds, r domain.RepoRef) (string, error)
}

// Creds authenticates one call. The service layer resolves the token from the secret store
// (repos.token_secret_id) and passes it down; the adapter never reads secrets itself.
type Creds struct {
	Token string
}

// RepoInfo is what Verify learned: enough for the repos row and the Overview About card.
type RepoInfo struct {
	Owner         string
	Name          string
	DefaultBranch string
	Private       bool
	HeadSHA       string // head of the default branch
	HeadMessage   string // its commit message, first line
}

// PRSpec is the input to OpenPullRequest.
type PRSpec struct {
	Title string
	Body  string
	Head  string // branch with the changes
	Base  string // branch to merge into
	Draft bool
}

// ReviewSpec is the input to SubmitReview. Event is "COMMENT" or "REQUEST_CHANGES" — nothing
// else; "APPROVE" in particular is rejected with ErrSelfApprovalForbidden (brief D6).
type ReviewSpec struct {
	Event string
	Body  string
}

// ErrSelfApprovalForbidden is returned by SubmitReview for an APPROVE event. Agents never
// approve pull requests (brief D6): approval is the human's final say, and there is no
// configuration that unlocks it.
var ErrSelfApprovalForbidden = errors.New(
	"agents cannot approve pull requests: approval is reserved for humans (brief D6)")

// ReviewEventRejectedError is returned by SubmitReview when the forge accepted the request
// but refused the review EVENT — GitHub answers 422 to REQUEST_CHANGES from the pull
// request's own author, which under D-9 (one project PAT, so every agent is the same GitHub
// user) is what a reviewer agent reviewing a Dev agent's pull request always is. Nothing was
// written; the caller may retry the same body as a COMMENT.
//
// It exists so the retry can be conditioned on the forge's actual verdict rather than on a
// substring of an error message, and so that "what was intended" stays distinguishable from
// "what GitHub accepted" all the way up to the run output.
type ReviewEventRejectedError struct {
	Event  string // the event the forge refused, e.g. "REQUEST_CHANGES"
	Detail string // the forge's own explanation, when it gave one
}

func (e *ReviewEventRejectedError) Error() string {
	msg := fmt.Sprintf("the forge refused a %s review", e.Event)
	if e.Detail != "" {
		msg += ": " + e.Detail
	}
	return msg
}

// ErrReviewEventRejected matches any ReviewEventRejectedError under errors.Is.
var ErrReviewEventRejected = errors.New("forge refused the review event")

func (e *ReviewEventRejectedError) Is(target error) bool { return target == ErrReviewEventRejected }

// PermissionDeniedError is returned by a forge write when the acting agent lacks the named
// grant. It matches ErrPermissionDenied under errors.Is. The check happens before any network
// call: a denied write costs nothing and leaks nothing.
type PermissionDeniedError struct {
	AgentID string
	Grant   string // the agents.permissions key that is false, e.g. "open_prs"
}

func (e *PermissionDeniedError) Error() string {
	return fmt.Sprintf("agent %s does not have the %q permission; grant it in the agent's Permissions settings",
		e.AgentID, e.Grant)
}

// ErrPermissionDenied matches any PermissionDeniedError under errors.Is.
var ErrPermissionDenied = errors.New("forge write permission denied")

func (e *PermissionDeniedError) Is(target error) bool { return target == ErrPermissionDenied }

// MissingScopeError is returned by Verify when the token cannot do what Lexicode needs. Scope
// names the missing grant ("repo" for a classic PAT); Detail says what probe failed for tokens
// that do not advertise scopes.
type MissingScopeError struct {
	Scope  string
	Detail string
}

func (e *MissingScopeError) Error() string {
	msg := fmt.Sprintf("the token is missing the %q scope", e.Scope)
	if e.Detail != "" {
		msg += ": " + e.Detail
	}
	return msg
}

// ErrMissingScope matches any MissingScopeError under errors.Is.
var ErrMissingScope = errors.New("token is missing a required scope")

func (e *MissingScopeError) Is(target error) bool { return target == ErrMissingScope }

// RateLimitedError is returned when the forge's rate limit is exhausted and the reset is too
// far away to sleep through. The adapter also reports its module degraded until a request
// succeeds again. Callers should not retry before Reset.
type RateLimitedError struct {
	Reset time.Time
}

func (e *RateLimitedError) Error() string {
	return fmt.Sprintf("forge rate limit exhausted; resets at %s", e.Reset.UTC().Format(time.RFC3339))
}

// ErrRateLimited matches any RateLimitedError under errors.Is.
var ErrRateLimited = errors.New("forge rate limit exhausted")

func (e *RateLimitedError) Is(target error) bool { return target == ErrRateLimited }

// ForbiddenError is returned when the forge accepted the credential but refused it sight of a
// resource: the token is valid and the request well-formed, it simply may not read this. It is
// the permanent counterpart to RateLimitedError — retrying changes nothing until a human
// re-grants the token, so a caller polling on a timer should stop asking rather than fail on
// every tick (story LEXI-9; the poller disables the affected resource and reports its module
// degraded).
//
// Status is the forge's own answer: 403 for "not permitted", 404 for a resource a token
// without access is not even told exists, 401 for a credential the forge no longer accepts.
// Err keeps the adapter's underlying error reachable through errors.As, so nothing that used
// to inspect it stops working.
type ForbiddenError struct {
	Resource string // what was refused, in the forge's own terms (an API path)
	Status   int    // 401, 403 or 404
	Detail   string // the forge's message, verbatim, when it sent one
	Err      error  // the underlying adapter error
}

func (e *ForbiddenError) Error() string {
	msg := fmt.Sprintf("the credential cannot access %s (HTTP %d)", e.Resource, e.Status)
	if e.Detail != "" {
		msg += ": " + e.Detail
	}
	return msg
}

func (e *ForbiddenError) Unwrap() error { return e.Err }

// ErrForbidden matches any ForbiddenError under errors.Is.
var ErrForbidden = errors.New("forge denied the credential access to the resource")

func (e *ForbiddenError) Is(target error) bool { return target == ErrForbidden }
