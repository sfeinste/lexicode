package domain

// AgentPermissions is agents.permissions (data model §3.1). These are enforcement, not guidance
// (brief D7): they compile to Claude Code allow/deny rules and to hard refusals in the forge
// adapter and wiki service. There is deliberately no Merge field — no merge permission exists
// anywhere in the system (brief D6); do not add one.
type AgentPermissions struct {
	ReadFiles       bool `json:"read_files"`
	EditFiles       bool `json:"edit_files"`
	RunCommands     bool `json:"run_commands"`
	PushBranches    bool `json:"push_branches"`
	OpenPRs         bool `json:"open_prs"`
	CommentPRs      bool `json:"comment_prs"`
	SubmitReviews   bool `json:"submit_reviews"`
	CreateWikiPages bool `json:"create_wiki_pages"`
}

// AgentDirective is a row of agent_directives (data model §3): one immutable version of an
// agent's system prompt. The table is append-only — the diff view reads two rows.
type AgentDirective struct {
	ID            string
	AgentID       string
	Version       int64
	Body          string
	TokenEstimate int64
	AuthorID      *string
	Note          string
	CreatedAt     string
}

// Agent is a row of agents.
type Agent struct {
	ID                  string
	ProjectID           string
	Name                string
	Role                string
	Color               string
	RuntimeID           string
	Model               string
	Effort              string
	Autonomy            Autonomy
	Permissions         AgentPermissions
	GitAuthorName       string
	GitAuthorEmail      string
	ForgeLogin          *string
	ForgeTokenSecretID  *string
	ConcurrencyCap      int64
	DailyCapCents       *int64
	MaxWallClockSeconds int64
	MaxSteps            int64
	Enabled             bool
	DirectiveVersionID  *string
	ArchivedAt          *string
	CreatedAt           string
	UpdatedAt           string
}
