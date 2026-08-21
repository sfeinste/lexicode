package domain

// Nullable columns are pointers throughout this package: nil is SQL NULL, which for the
// settings-inheritance pattern (data model §1) specifically means "inherit from the workspace".

// UserPrefs is users.prefs — per-user UI state. Fields are pointers so that an absent key is
// distinguishable from an explicit choice; the frontend supplies defaults for nils.
type UserPrefs struct {
	RailCollapsed    *bool   `json:"rail_collapsed,omitempty"`
	Density          *string `json:"density,omitempty"`
	Theme            *string `json:"theme,omitempty"`
	DefaultVerbosity *int    `json:"default_verbosity,omitempty"`
}

// User is a row of users.
type User struct {
	ID           string
	Email        string
	DisplayName  string
	PasswordHash string
	Role         UserRole
	AvatarColor  string
	Prefs        UserPrefs
	ArchivedAt   *string
	CreatedAt    string
}

// Session is a row of sessions. ID is the token hash, not the token: the opaque token the
// browser holds is hashed before storage, so a database read never yields a usable credential.
type Session struct {
	ID        string
	UserID    string
	ExpiresAt string
	UserAgent *string
	CreatedAt string
}

// Invite is a row of invites. TokenHash, like Session.ID, is a hash of the one-time URL token.
type Invite struct {
	ID         string
	TokenHash  string
	Role       UserRole
	CreatedBy  string
	ExpiresAt  string
	RedeemedBy *string
	CreatedAt  string
}

// WorkspaceSettings is the single row of workspace_settings — the source of every "inherited
// from workspace" value.
type WorkspaceSettings struct {
	DefaultBranch                 string
	DefaultBranchTemplate         string
	DefaultNetworkPolicy          string
	DefaultDailyBudgetCents       int64
	DefaultContextThresholdTokens int64
	DefaultVerificationDays       int64
	MaxConcurrentContainers       int64
	PollIntervalSeconds           int64
	UpdatedAt                     string
}
