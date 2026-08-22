// Package projects is the projects domain service (story S08): project CRUD, archive, the
// workspace-settings row, and the settings-inheritance read model.
//
// Inheritance (data model §1): the projects table's settings columns are nullable and null means
// "inherit from workspace". This service never copies a workspace default into a project row —
// it resolves the pair at read time into {value, inherited, workspace_value} so the UI's
// InheritedField control renders without recomputing anything.
//
// Workspace settings live here rather than in their own package because they are the other half
// of the inheritance pattern: every project read resolves against them, and splitting the two
// across packages would put the pattern's two halves in different places.
//
// Every mutation writes the audit log (architecture §14) and emits an internal bus event
// (project.created, project.updated, project.archived, project.unarchived,
// workspace.settings.updated) so later stories' triggers and SSE topics can observe them.
package projects

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"regexp"
	"strings"
	"time"

	"github.com/spruce/lexicode/internal/domain"
	"github.com/spruce/lexicode/internal/kernel/audit"
	"github.com/spruce/lexicode/internal/kernel/auth"
	"github.com/spruce/lexicode/internal/kernel/bus"
	"github.com/spruce/lexicode/internal/kernel/httpx"
	"github.com/spruce/lexicode/internal/kernel/store"
	"github.com/spruce/lexicode/internal/service/board"
)

// keyPattern is the project-key shape from the schema comment (data model §2): an uppercase
// letter followed by 1–9 uppercase letters or digits.
var keyPattern = regexp.MustCompile(`^[A-Z][A-Z0-9]{1,9}$`)

// colorPattern is a #rrggbb hex color.
var colorPattern = regexp.MustCompile(`^#[0-9a-fA-F]{6}$`)

// networkPolicies is the closed set the workspace default may take (data model §2).
var networkPolicies = map[string]bool{"none": true, "allowlist": true, "open": true}

// Service is the projects service. Construct with New.
type Service struct {
	st     *store.Store
	audit  *audit.Writer
	bus    *bus.Bus
	logger *slog.Logger
	now    func() string
}

// Options configures New.
type Options struct {
	// Store is the open, migrated database. Required.
	Store *store.Store
	// Audit is the audit-log writer. Required — every mutation writes an entry.
	Audit *audit.Writer
	// Bus emits internal events for mutations. Nil (tests) skips emission.
	Bus *bus.Bus
	// Logger receives failure lines. Nil means slog.Default().
	Logger *slog.Logger
	// Now overrides the clock in tests. Nil means domain.Now.
	Now func() string
}

// New builds the service.
func New(opts Options) *Service {
	logger := opts.Logger
	if logger == nil {
		logger = slog.Default()
	}
	now := opts.Now
	if now == nil {
		now = domain.Now
	}
	return &Service{st: opts.Store, audit: opts.Audit, bus: opts.Bus, logger: logger, now: now}
}

// ValidationError carries field-level problems up to the HTTP layer as a 400 validation_failed.
type ValidationError struct{ Fields []httpx.FieldError }

// Error names the invalid fields.
func (e *ValidationError) Error() string {
	names := make([]string, len(e.Fields))
	for i, f := range e.Fields {
		names[i] = f.Field
	}
	return "invalid fields: " + strings.Join(names, ", ")
}

// dayStartUTC is the UTC midnight of the instant now() reports — where "spend today" begins.
func (s *Service) dayStartUTC() string {
	t, err := time.Parse(time.RFC3339, s.now())
	if err != nil {
		t = time.Now().UTC()
	}
	y, m, d := t.UTC().Date()
	return domain.FormatTime(time.Date(y, m, d, 0, 0, 0, 0, time.UTC))
}

// ---------------------------------------------------------------- reads -----

// ProjectWithStats pairs a project with its Home-table counters.
type ProjectWithStats struct {
	Project domain.Project
	Stats   store.ProjectStats
}

// List returns projects with stats, oldest first. Archived projects are excluded unless
// includeArchived — the S08 acceptance "archive hides from the default list".
func (s *Service) List(ctx context.Context, includeArchived bool) ([]ProjectWithStats, error) {
	all, err := s.st.Projects().List(ctx)
	if err != nil {
		return nil, err
	}
	stats, err := s.st.Projects().Stats(ctx, s.dayStartUTC())
	if err != nil {
		return nil, err
	}
	out := make([]ProjectWithStats, 0, len(all))
	for _, p := range all {
		if p.ArchivedAt != nil && !includeArchived {
			continue
		}
		out = append(out, ProjectWithStats{Project: p, Stats: stats[p.ID]})
	}
	return out, nil
}

// Get returns one project by key.
func (s *Service) Get(ctx context.Context, key string) (domain.Project, error) {
	return s.st.Projects().ByKey(ctx, key)
}

// Workspace returns the workspace-settings row.
func (s *Service) Workspace(ctx context.Context) (domain.WorkspaceSettings, error) {
	return s.st.Workspace().Get(ctx)
}

// ---------------------------------------------------------------- budget -----

// BudgetStatus is the project's live standing against its daily ceiling — what the header
// spend chip and the exhaustion banner render (S37). Day scoping matches the scheduler's
// admission check exactly: budget_ledger rows are keyed by UTC calendar day, so the ceiling
// resets at midnight UTC — ResetsAt names that instant.
type BudgetStatus struct {
	SpendTodayCents int64
	CeilingCents    int64
	Inherited       bool // the ceiling is the workspace default, not a project override
	Exhausted       bool // spend ≥ ceiling and the ceiling is enforcing (> 0)
	Day             string
	ResetsAt        string
}

// Budget reads the project's spend-vs-ceiling standing from budget_ledger — the same table
// admission control consults (§10.2 check 4), so the banner and the scheduler can never
// disagree. (The Home table's SpendTodayCents sums runs.cost_cents instead; the two agree in
// steady state but this one is the enforcement view.)
func (s *Service) Budget(ctx context.Context, key string) (BudgetStatus, error) {
	p, err := s.st.Projects().ByKey(ctx, key)
	if err != nil {
		return BudgetStatus{}, err
	}
	ws, err := s.st.Workspace().Get(ctx)
	if err != nil {
		return BudgetStatus{}, err
	}

	t, err := time.Parse(time.RFC3339, s.now())
	if err != nil {
		t = time.Now()
	}
	y, m, d := t.UTC().Date()
	day := time.Date(y, m, d, 0, 0, 0, 0, time.UTC)

	st := BudgetStatus{
		CeilingCents: ws.DefaultDailyBudgetCents,
		Inherited:    true,
		Day:          day.Format("2006-01-02"),
		ResetsAt:     domain.FormatTime(day.AddDate(0, 0, 1)),
	}
	if p.DailyBudgetCents != nil {
		st.CeilingCents = *p.DailyBudgetCents
		st.Inherited = false
	}
	spent, err := s.st.Budget().ProjectDay(ctx, st.Day, p.ID)
	if err != nil {
		return BudgetStatus{}, err
	}
	st.SpendTodayCents = spent
	st.Exhausted = st.CeilingCents > 0 && spent >= st.CeilingCents
	return st, nil
}

// ---------------------------------------------------------------- delete -----

// ErrProjectBusy is returned by DeleteProject while the project still has non-terminal runs.
// The HTTP layer maps it to 409: stop or finish the runs, then delete.
var ErrProjectBusy = errors.New("projects: the project has runs that have not finished")

// DeleteCounts mirrors store.ProjectDeleteCounts for callers of this package.
type DeleteCounts = store.ProjectDeleteCounts

// Counts returns what a deletion of this project would remove — the numbers the danger-zone
// confirm dialog names before anything is typed.
func (s *Service) Counts(ctx context.Context, key string) (DeleteCounts, error) {
	p, err := s.st.Projects().ByKey(ctx, key)
	if err != nil {
		return DeleteCounts{}, err
	}
	return s.st.Projects().CountProjectRows(ctx, p.ID)
}

// DeleteProject hard-deletes a project after the S37 typed confirmation: confirm must equal
// the project key exactly — enforced here, not in the UI, so no client can skip it. Every
// dependent row goes in one transaction (store.DeleteProjectCascade); the project's audit
// history survives detached to workspace scope, and the deletion itself is audited at
// workspace level — an entry that, by construction, outlives the project.
func (s *Service) DeleteProject(ctx context.Context, key, confirm string) (DeleteCounts, error) {
	p, err := s.st.Projects().ByKey(ctx, key)
	if err != nil {
		return DeleteCounts{}, err
	}
	if confirm != p.Key {
		return DeleteCounts{}, &ValidationError{Fields: []httpx.FieldError{{
			Field:   "confirm",
			Message: fmt.Sprintf("Type the project key %s to confirm deletion.", p.Key),
		}}}
	}
	active, err := s.st.Runs().NonTerminalCountForProject(ctx, p.ID)
	if err != nil {
		return DeleteCounts{}, err
	}
	if active > 0 {
		return DeleteCounts{}, ErrProjectBusy
	}

	var counts DeleteCounts
	if err := s.st.Tx(ctx, func(tx *store.Tx) error {
		counts, err = tx.DeleteProjectCascade(ctx, p.ID)
		return err
	}); err != nil {
		return DeleteCounts{}, err
	}

	// Workspace-level (no ProjectID): the entry must not reference the row it records the
	// death of.
	if err := s.audit.Write(ctx, "project.delete",
		audit.Target{Kind: "project", ID: p.ID,
			Note: fmt.Sprintf("deleted project %s (%s)", p.Key, p.Name)},
		p, map[string]int64{
			"tickets": counts.Tickets, "runs": counts.Runs, "wiki_pages": counts.WikiPages,
		}); err != nil {
		return DeleteCounts{}, err
	}
	s.emitDeleted(ctx, p)
	return counts, nil
}

// emitDeleted publishes project.deleted without a project scope — an event row scoped to the
// deleted project would violate the events.project_id foreign key.
func (s *Service) emitDeleted(ctx context.Context, p domain.Project) {
	if s.bus == nil {
		return
	}
	payload, _ := json.Marshal(map[string]any{
		"project": map[string]string{"id": p.ID, "key": p.Key, "name": p.Name},
	})
	e := domain.Event{Kind: "project.deleted", SubjectKind: "project",
		Payload: payload, OccurredAt: s.now()}
	stampActor(ctx, &e)
	if err := s.bus.Emit(ctx, e); err != nil {
		s.logger.Error("projects: emit failed",
			slog.String("kind", "project.deleted"), slog.String("error", err.Error()))
	}
}

// ---------------------------------------------------------------- create -----

// CreateInput is what a new project needs. Color defaults when empty.
type CreateInput struct {
	Key         string
	Name        string
	Description string
	Color       string
	// OwnerID is the creating user (from the request context).
	OwnerID string
}

// defaultColors is the palette a project color is picked from when the creator does not choose.
var defaultColors = []string{"#7c5cff", "#00a884", "#ff8a3d", "#5b8def", "#d16ba5", "#e5484d"}

// CreateProject inserts a project and makes the creator its owner and first member. A duplicate
// key comes back as a field-level ValidationError on "key" (S08 acceptance). The settings
// columns start null — inheriting from workspace. The S09 default board columns are created in
// the same transaction (board.CreateDefaults), so a project is never observable without a board.
func (s *Service) CreateProject(ctx context.Context, in CreateInput) (domain.Project, error) {
	in.Key = strings.ToUpper(strings.TrimSpace(in.Key))
	in.Name = strings.TrimSpace(in.Name)

	var errs []httpx.FieldError
	if !keyPattern.MatchString(in.Key) {
		errs = append(errs, httpx.FieldError{Field: "key",
			Message: "2–10 characters: an uppercase letter, then uppercase letters or digits."})
	}
	if in.Name == "" {
		errs = append(errs, httpx.FieldError{Field: "name", Message: "Name is required."})
	}
	if in.Color == "" {
		in.Color = defaultColors[len(in.Key)%len(defaultColors)]
	} else if !colorPattern.MatchString(in.Color) {
		errs = append(errs, httpx.FieldError{Field: "color", Message: "Use a #rrggbb color."})
	}
	if len(errs) > 0 {
		return domain.Project{}, &ValidationError{Fields: errs}
	}

	now := s.now()
	p := domain.Project{
		ID: domain.NewID(), Key: in.Key, Name: in.Name, Description: in.Description,
		Color: in.Color, OwnerID: in.OwnerID, CreatedAt: now, UpdatedAt: now,
	}
	err := s.st.Tx(ctx, func(tx *store.Tx) error {
		if err := tx.Projects().Create(ctx, &p); err != nil {
			return err
		}
		if err := tx.Projects().AddMember(ctx, p.ID, in.OwnerID); err != nil {
			return err
		}
		// The S09 default board, in the same transaction: a project is never observable
		// without its columns. Covered by the project.create audit entry below.
		cols := board.DefaultColumns(p.ID, now)
		for i := range cols {
			if err := tx.Columns().Create(ctx, &cols[i]); err != nil {
				return err
			}
		}
		return nil
	})
	if errors.Is(err, store.ErrUnique) {
		return domain.Project{}, &ValidationError{Fields: []httpx.FieldError{
			{Field: "key", Message: fmt.Sprintf("The key %s is already taken.", in.Key)},
		}}
	}
	if err != nil {
		return domain.Project{}, err
	}

	if err := s.audit.Write(ctx, "project.create",
		audit.Target{Kind: "project", ID: p.ID, ProjectID: p.ID}, nil, p); err != nil {
		return domain.Project{}, err
	}
	s.emit(ctx, "project.created", p)
	return p, nil
}

// ---------------------------------------------------------------- update -----

// UpdatePatch is a PATCH /projects/{key} body: pointers and OptInts distinguish absent from
// present. The three OptInt fields are the inheritable settings — null reverts to inherit.
type UpdatePatch struct {
	Name          *string
	Description   *string
	Color         *string
	OwnerID       *string
	AgentGuidance *string
	// Archived flips archive state: true stamps archived_at, false clears it.
	Archived               *bool
	DailyBudgetCents       OptInt
	ContextThresholdTokens OptInt
	VerificationDays       OptInt
	PRSizeWarningLines     OptInt
}

// UpdateProject applies a patch. Archive transitions get their own audit action and event so
// the log reads as what happened, not as a diff puzzle.
func (s *Service) UpdateProject(ctx context.Context, key string, patch UpdatePatch) (domain.Project, error) {
	before, err := s.st.Projects().ByKey(ctx, key)
	if err != nil {
		return domain.Project{}, err
	}
	p := before

	var errs []httpx.FieldError
	if patch.Name != nil {
		if n := strings.TrimSpace(*patch.Name); n == "" {
			errs = append(errs, httpx.FieldError{Field: "name", Message: "Name is required."})
		} else {
			p.Name = n
		}
	}
	if patch.Description != nil {
		p.Description = *patch.Description
	}
	if patch.Color != nil {
		if !colorPattern.MatchString(*patch.Color) {
			errs = append(errs, httpx.FieldError{Field: "color", Message: "Use a #rrggbb color."})
		} else {
			p.Color = *patch.Color
		}
	}
	if patch.AgentGuidance != nil {
		p.AgentGuidance = *patch.AgentGuidance
	}
	if patch.OwnerID != nil {
		if _, err := s.st.Users().ByID(ctx, *patch.OwnerID); err != nil {
			if errors.Is(err, store.ErrNotFound) {
				errs = append(errs, httpx.FieldError{Field: "owner_id", Message: "No such user."})
			} else {
				return domain.Project{}, err
			}
		} else {
			p.OwnerID = *patch.OwnerID
		}
	}
	for _, f := range []struct {
		name string
		opt  OptInt
	}{
		{"daily_budget_cents", patch.DailyBudgetCents},
		{"context_threshold_tokens", patch.ContextThresholdTokens},
		{"verification_days", patch.VerificationDays},
		{"pr_size_warning_lines", patch.PRSizeWarningLines},
	} {
		if f.opt.Set && !f.opt.Null && f.opt.Value < 0 {
			errs = append(errs, httpx.FieldError{Field: f.name, Message: "Must be zero or more."})
		}
	}
	if len(errs) > 0 {
		return domain.Project{}, &ValidationError{Fields: errs}
	}

	p.DailyBudgetCents = patch.DailyBudgetCents.apply(p.DailyBudgetCents)
	p.ContextThresholdTokens = patch.ContextThresholdTokens.apply(p.ContextThresholdTokens)
	p.VerificationDays = patch.VerificationDays.apply(p.VerificationDays)
	p.PRSizeWarningLines = patch.PRSizeWarningLines.apply(p.PRSizeWarningLines)

	action, event := "project.update", "project.updated"
	if patch.Archived != nil {
		if *patch.Archived && p.ArchivedAt == nil {
			at := s.now()
			p.ArchivedAt = &at
			action, event = "project.archive", "project.archived"
		} else if !*patch.Archived && p.ArchivedAt != nil {
			p.ArchivedAt = nil
			action, event = "project.unarchive", "project.unarchived"
		}
	}

	p.UpdatedAt = s.now()
	if err := s.st.Projects().Update(ctx, &p); err != nil {
		return domain.Project{}, err
	}
	if err := s.audit.Write(ctx, action,
		audit.Target{Kind: "project", ID: p.ID, ProjectID: p.ID}, before, p); err != nil {
		return domain.Project{}, err
	}
	s.emit(ctx, event, p)
	return p, nil
}

// WorkspacePatch is a PUT /workspace/settings body: every field optional, absent = unchanged.
// PUT is the contracts §5 verb; partial bodies are accepted so the settings screen can autosave
// one control at a time.
type WorkspacePatch struct {
	DefaultBranch                 *string
	DefaultBranchTemplate         *string
	DefaultNetworkPolicy          *string
	DefaultDailyBudgetCents       *int64
	DefaultContextThresholdTokens *int64
	DefaultVerificationDays       *int64
	MaxConcurrentContainers       *int64
	PollIntervalSeconds           *int64
	PRSizeWarningLines            *int64
}

// UpdateWorkspace rewrites the single workspace_settings row. Projects with null settings
// columns follow the new values immediately — that is the inheritance pattern working.
func (s *Service) UpdateWorkspace(ctx context.Context, patch WorkspacePatch) (domain.WorkspaceSettings, error) {
	before, err := s.st.Workspace().Get(ctx)
	if err != nil {
		return domain.WorkspaceSettings{}, err
	}
	ws := before

	var errs []httpx.FieldError
	if patch.DefaultBranch != nil {
		if b := strings.TrimSpace(*patch.DefaultBranch); b == "" {
			errs = append(errs, httpx.FieldError{Field: "default_branch", Message: "A branch name is required."})
		} else {
			ws.DefaultBranch = b
		}
	}
	if patch.DefaultBranchTemplate != nil {
		if t := strings.TrimSpace(*patch.DefaultBranchTemplate); t == "" {
			errs = append(errs, httpx.FieldError{Field: "default_branch_template", Message: "A template is required."})
		} else {
			ws.DefaultBranchTemplate = t
		}
	}
	if patch.DefaultNetworkPolicy != nil {
		if !networkPolicies[*patch.DefaultNetworkPolicy] {
			errs = append(errs, httpx.FieldError{Field: "default_network_policy", Message: "One of none, allowlist, open."})
		} else {
			ws.DefaultNetworkPolicy = *patch.DefaultNetworkPolicy
		}
	}
	setNonNeg := func(field string, dst *int64, v *int64) {
		if v == nil {
			return
		}
		if *v < 0 {
			errs = append(errs, httpx.FieldError{Field: field, Message: "Must be zero or more."})
			return
		}
		*dst = *v
	}
	setPos := func(field string, dst *int64, v *int64) {
		if v == nil {
			return
		}
		if *v < 1 {
			errs = append(errs, httpx.FieldError{Field: field, Message: "Must be at least 1."})
			return
		}
		*dst = *v
	}
	setNonNeg("default_daily_budget_cents", &ws.DefaultDailyBudgetCents, patch.DefaultDailyBudgetCents)
	setNonNeg("pr_size_warning_lines", &ws.PRSizeWarningLines, patch.PRSizeWarningLines)
	setNonNeg("default_context_threshold_tokens", &ws.DefaultContextThresholdTokens, patch.DefaultContextThresholdTokens)
	setPos("default_verification_days", &ws.DefaultVerificationDays, patch.DefaultVerificationDays)
	setPos("max_concurrent_containers", &ws.MaxConcurrentContainers, patch.MaxConcurrentContainers)
	setPos("poll_interval_seconds", &ws.PollIntervalSeconds, patch.PollIntervalSeconds)
	if len(errs) > 0 {
		return domain.WorkspaceSettings{}, &ValidationError{Fields: errs}
	}

	ws.UpdatedAt = s.now()
	if err := s.st.Workspace().Update(ctx, &ws); err != nil {
		return domain.WorkspaceSettings{}, err
	}
	if err := s.audit.Write(ctx, "workspace.settings.update",
		audit.Target{Kind: "workspace_settings", ID: "1"}, before, ws); err != nil {
		return domain.WorkspaceSettings{}, err
	}
	s.emitWorkspace(ctx)
	return ws, nil
}

// ---------------------------------------------------------------- events -----

// emit publishes an internal project event on the bus. Emission is best-effort: the mutation is
// committed and audited by the time this runs, so a bus failure is logged, never unwound.
func (s *Service) emit(ctx context.Context, kind string, p domain.Project) {
	if s.bus == nil {
		return
	}
	payload, _ := json.Marshal(map[string]any{
		"project": map[string]string{"id": p.ID, "key": p.Key, "name": p.Name},
	})
	pid := p.ID
	e := domain.Event{
		ProjectID: &pid, Kind: kind, SubjectKind: "project", SubjectID: &pid,
		Payload: payload, OccurredAt: s.now(),
	}
	stampActor(ctx, &e)
	if err := s.bus.Emit(ctx, e); err != nil {
		s.logger.Error("projects: emit failed",
			slog.String("kind", kind), slog.String("error", err.Error()))
	}
}

// emitWorkspace publishes workspace.settings.updated (no project scope).
func (s *Service) emitWorkspace(ctx context.Context) {
	if s.bus == nil {
		return
	}
	e := domain.Event{Kind: "workspace.settings.updated", SubjectKind: "workspace_settings",
		OccurredAt: s.now()}
	stampActor(ctx, &e)
	if err := s.bus.Emit(ctx, e); err != nil {
		s.logger.Error("projects: emit failed",
			slog.String("kind", "workspace.settings.updated"), slog.String("error", err.Error()))
	}
}

// stampActor copies the request's actor (auth.RequireAuth put it on the context) onto the
// event, so project events attribute to the human who caused them rather than to "system".
func stampActor(ctx context.Context, e *domain.Event) {
	if a, ok := auth.ActorFrom(ctx); ok {
		e.ActorKind = a.Kind
		if a.ID != "" {
			id := a.ID
			e.ActorID = &id
		}
	}
}
