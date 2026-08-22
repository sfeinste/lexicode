package domain

// Repo is a row of repos (data model §2): the one repository a project is connected to.
// Provider is a ForgeProvider port ID ("github"). TokenSecretID references the PAT in the
// secret store — the token itself never appears on this struct (D-16).
type Repo struct {
	ProjectID        string
	Provider         string
	Owner            string
	Name             string
	DefaultBranch    *string // nil = inherit the workspace default
	BranchTemplate   *string
	SetupScript      string
	ImageRef         *string
	NetworkPolicy    *string // none|allowlist|open, nil = inherit
	NetworkAllowlist []string
	TokenSecretID    *string
	ConnectedAt      *string
	LastSyncedAt     *string
	HeadSHA          *string // for the Overview About card
	HeadMessage      *string
	CreatedAt        string
	UpdatedAt        string
}

// Ref is the repo as a forge call addresses it.
func (r Repo) Ref() RepoRef { return RepoRef{Owner: r.Owner, Name: r.Name} }
