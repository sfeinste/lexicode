package domain

// PollCursor is a row of poll_cursors (data model §7, architecture §7): the GitHub poller's
// per-(project, resource) position. Cursor is the last upstream `updated_at` seen (RFC3339);
// Etag rides If-None-Match so an unchanged listing costs no rate limit. BaselineDone records
// that the cold-start baseline pass ran — until it has, the poller records state and emits
// nothing.
type PollCursor struct {
	ProjectID    string
	Resource     string // "pulls" | "review_comments" | "issue_comments" | "check_suites"
	Cursor       string
	Etag         string
	BaselineDone bool
	LastPolledAt *string
}

// PollPRState is a row of poll_pr_state: the per-PR snapshot the poller diffs against to derive
// activity types (opened vs synchronize vs ready_for_review vs closed — architecture §7).
// ReviewCursor is the last review `submitted_at` seen for this PR.
type PollPRState struct {
	ProjectID    string
	Number       int64
	HeadSHA      string
	State        string // "open" | "closed"
	Draft        bool
	UpdatedAt    string
	ReviewCursor string
	// Additions/Deletions are the PR's line counts from the poller's detail read (S37's
	// diff-size warning joins run outputs against them). Nil = the detail read has not
	// happened for this PR yet.
	Additions *int64
	Deletions *int64
}
