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
	URL         string
	CreatedAt   time.Time
	UpdatedAt   time.Time
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
	URL           string
	CreatedAt     time.Time
	UpdatedAt     time.Time
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
