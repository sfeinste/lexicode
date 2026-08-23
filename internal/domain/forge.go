package domain

import (
	"fmt"
	"time"
)

// This file is the forge vocabulary (contracts §2.2, story S14): what a git host looks like from
// inside Lexicode. Every ForgeProvider adapter translates its wire types into these — no
// go-github (or other SDK) type ever crosses the port. Unlike the row types elsewhere in this
// package, timestamps here are time.Time: these values never touch SQLite directly, and the
// poller's whole job is comparing them against a cursor.

// RepoRef names one repository on a forge: repos.owner / repos.name.
type RepoRef struct {
	Owner string
	Name  string
}

// String renders "owner/name", the form every error message and log line uses.
func (r RepoRef) String() string { return r.Owner + "/" + r.Name }

// Actor is the agent a forge write is performed on behalf of. Every ForgeProvider write method
// takes one so that the D-9 marker can never be forgotten: the adapter builds it from these two
// IDs and appends it to every body it sends.
type Actor struct {
	AgentID string
	RunID   string
}

// Marker is the D-9 machine-readable marker appended to every PR, comment and review body an
// agent authors. Actor suppression (D5 layer 1) matches this exact string, so the format is
// load-bearing: change it and past events stop being attributable.
func (a Actor) Marker() string {
	return fmt.Sprintf("<!-- lexicode:actor=agent:%s run=%s -->", a.AgentID, a.RunID)
}

// PullRequest is one pull request as the poller and UI surfaces see it.
type PullRequest struct {
	Number      int
	Title       string
	Body        string
	State       string // "open" | "closed"
	Draft       bool
	Merged      bool
	AuthorLogin string
	AuthorEmail string // often empty; suppression falls back to the marker and branch name
	HeadRef     string
	HeadSHA     string
	BaseRef     string
	Labels      []string
	URL         string
	CreatedAt   time.Time
	UpdatedAt   time.Time

	// Additions, Deletions and ChangedFiles are populated only by the single-PR read
	// (Forge.GetPullRequest) — GitHub's list endpoint does not carry them. The poller
	// fetches the detail for PRs it is about to emit events for, so the normalized
	// pr.additions / pr.deletions / pr.files_changed fields (contracts §4) are accurate.
	Additions    int
	Deletions    int
	ChangedFiles int
}

// Review is one submitted pull-request review.
type Review struct {
	ID          int64
	PRNumber    int
	AuthorLogin string
	State       string // "APPROVED" | "CHANGES_REQUESTED" | "COMMENTED" | "DISMISSED"
	Body        string
	URL         string
	SubmittedAt time.Time
}

// Comment is one comment — a PR review comment (Path set) or an issue/PR conversation comment
// (Path empty). SubjectNumber is the issue or PR the comment belongs to; 0 when the listing
// endpoint did not carry it.
type Comment struct {
	ID            int64
	SubjectNumber int
	AuthorLogin   string
	Body          string
	Path          string // review comments only: the file the comment anchors to
	Line          int    // review comments only: the line the comment anchors to; 0 otherwise
	URL           string
	CreatedAt     time.Time
	UpdatedAt     time.Time

	// ReviewID is the review this comment was submitted as part of; 0 for conversation
	// comments and for hosts that do not group comments into reviews. It is what lets a
	// whole review be reassembled from the comment fragments the poller emits (LEXI-10).
	ReviewID int64
	// InReplyToID is the review comment this one replies to; 0 when the comment starts its
	// own thread. Threading a review is exactly following this pointer.
	InReplyToID int64
	// DiffHunk is the few lines of diff the forge anchors the comment to. Review comments
	// only; empty otherwise.
	DiffHunk string
}

// ReviewThread is one inline conversation on a pull request: the file and line it anchors to,
// the diff hunk it was written against, every comment on it in order, and whether a human has
// marked it resolved on the forge. It is the unit an agent has to act on — a review is a
// summary plus N of these (LEXI-10).
//
// Resolution is not in GitHub's REST surface, so an adapter that could only reach REST reports
// Resolved false and ResolutionKnown false: "not known to be resolved" and "known to be
// unresolved" are different claims, and a prompt that confuses them re-litigates settled
// threads.
type ReviewThread struct {
	// ID is the forge's own thread identifier when it has one (GitHub's GraphQL node id);
	// empty when threads were reassembled from comments alone.
	ID string
	// ReviewID is the review the thread's first comment belongs to; 0 when unknown.
	ReviewID int64
	Path     string
	Line     int
	DiffHunk string
	// Outdated is true when the lines the thread anchors to have since changed.
	Outdated bool
	// Resolved is true when the thread is marked resolved on the forge; meaningful only
	// when ResolutionKnown is true.
	Resolved        bool
	ResolutionKnown bool
	URL             string
	Comments        []Comment
}

// Author returns the login that opened the thread, or "" for an empty thread.
func (t ReviewThread) Author() string {
	if len(t.Comments) == 0 {
		return ""
	}
	return t.Comments[0].AuthorLogin
}

// CheckSuite is one CI check suite for a head SHA.
type CheckSuite struct {
	ID         int64
	HeadSHA    string
	HeadBranch string
	Status     string // "queued" | "in_progress" | "completed"
	Conclusion string // "success" | "failure" | … ; empty until completed
	App        string // the app that owns the suite, e.g. "GitHub Actions"
	URL        string
	UpdatedAt  time.Time // when the suite last changed; the poller's check_suites cursor
}

// DirEntry is one entry of a repository directory listing, as bootstrap doc detection (S15)
// consumes it. It is deliberately minimal: enough to walk .cursor/rules/*, docs/** (depth 2)
// and .github/workflows/* — not a general tree API.
type DirEntry struct {
	Name string // base name, "deploy.md"
	Path string // repo-relative path, "docs/deploy.md"
	Type string // "file" | "dir"
}

// Issue is one open issue as the bootstrap import (S15) offers it. Pull requests are excluded
// by the adapter — on GitHub every PR is an issue, but an importable ticket is not a PR.
type Issue struct {
	Number      int
	Title       string
	Body        string
	AuthorLogin string
	Labels      []string
	URL         string
	CreatedAt   time.Time
	UpdatedAt   time.Time
}
