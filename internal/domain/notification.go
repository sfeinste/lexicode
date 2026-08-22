package domain

// Notification is a row of notifications: the one attention row a run gets per user,
// updated in place — UNIQUE(user_id, run_id) makes stacking impossible at the schema level
// (interaction rule 3; architecture §12). Flavor is the §4.3 vocabulary, always rendered in
// words.
type Notification struct {
	ID        string
	UserID    string
	ProjectID string
	RunID     *string
	Flavor    NotificationFlavor
	Title     string
	Body      string
	State     NotificationState
	Pushed    bool
	CreatedAt string
	UpdatedAt string
}
