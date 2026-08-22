package domain

// WikiPage is a row of wiki_pages (data model §5). State distinguishes live pages from agent
// proposals; ImportedFrom carries the repo path a bootstrap import seeded the page from (D-11)
// and is what makes a re-scan idempotent — matching happens on this exact path.
type WikiPage struct {
	ID                  string
	ProjectID           string
	Slug                string
	Title               string
	ParentID            *string
	Position            float64
	OwnerID             *string
	VerifiedUntil       *string
	AgentScope          AgentScope
	ScopePaths          []string
	Tags                []string
	Body                string
	TokenEstimate       int64
	State               WikiState
	ProposedByRunID     *string
	ProposedBaseVersion *int64
	ProposalTargetID    *string
	ProposedReason      *string
	ImportedFrom        *string
	DemotedAt           *string
	DemotedFrom         *string
	ArchivedAt          *string
	CreatedAt           string
	UpdatedAt           string
}

// WikiVersion is a row of wiki_versions: one immutable snapshot of a page.
type WikiVersion struct {
	ID           string
	PageID       string
	Version      int64
	Title        string
	Body         string
	FrontMatter  map[string]any
	AuthorUserID *string
	AuthorRunID  *string
	CreatedAt    string
}
