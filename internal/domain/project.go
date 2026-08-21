package domain

// Project is a row of projects. The nullable numeric settings mean "inherit from workspace"
// when nil — never copy a workspace default into them (data model §1).
type Project struct {
	ID                     string
	Key                    string
	Name                   string
	Description            string
	Color                  string
	OwnerID                string
	AgentGuidance          string
	DailyBudgetCents       *int64
	ContextThresholdTokens *int64
	VerificationDays       *int64
	TicketSeq              int64
	ArchivedAt             *string
	CreatedAt              string
	UpdatedAt              string
}

// Column is a row of columns. Automation reads Category, never Name (plan rule 3).
type Column struct {
	ID                string
	ProjectID         string
	Name              string
	Category          ColumnCategory
	Position          int64
	WIPLimit          *int64
	AutoStartDelegate bool
	CreatedAt         string
	UpdatedAt         string
}
