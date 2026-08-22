// Package agents is the agents domain service (story S16): agent CRUD, the versioned
// directive, typed permissions, autonomy, limits, roster stats and the starter-roster action.
//
// The rules this package exists to protect:
//
//   - Permissions are enforcement, never guidance (brief D7). They cross the API as the typed
//     §3.1 object — the service never accepts free-form permission JSON, and there is no merge
//     permission anywhere (brief D6).
//   - The directive is append-only (`agent_directives`): saving appends a new version ONLY
//     when the body actually changed; saving an unchanged body is a no-op that returns the
//     current version. The diff view reads two immutable rows.
//   - Names are unique per project; the collision is a 409 `agent_name_taken` naming the field.
//   - Disable (enabled=false) and archive (archived_at) are different states: a disabled or
//     archived agent disappears from delegate pickers and mention autocomplete, but stays
//     fetchable with its history intact (D-15's philosophy: disappearance without data loss).
//   - Git identity defaults from the agent name (D-9: `Reviewer <reviewer@agents.lexicode.local>`).
//   - Every mutation writes the audit log and emits a bus event.
//
// Token estimates use the documented chars/4 heuristic (EstimateTokens); the value is stored
// per version and recomputed live by the estimate endpoint while the user types.
package agents

import (
	"context"
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
)

// KnownModels is the model picker's vocabulary. The schema stores model as free TEXT (no
// CHECK), so this list is the service-level gate; extend it when new models ship.
var KnownModels = []string{"claude-opus-5", "claude-sonnet-5", "claude-haiku-4-5"}

// KnownEfforts is the thinking-effort vocabulary (schema default 'medium').
var KnownEfforts = []string{"low", "medium", "high"}

// statsWindow is the roster cards' "this week": a rolling seven days.
const statsWindow = 7 * 24 * time.Hour

// Service is the agents service. Construct with New.
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

// ---------------------------------------------------------------- errors -----

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

// NameTakenError is a duplicate agent name inside one project — the 409 `agent_name_taken`
// problem, carrying the field so the form can attach the message to the name input.
type NameTakenError struct{ Name string }

// Error names the collision.
func (e *NameTakenError) Error() string {
	return fmt.Sprintf("an agent named %q already exists in this project", e.Name)
}

// ArchivedError is a mutation on an archived agent — everything is refused until unarchive
// (which V1 does not offer; archive is the end of the line, history stays readable).
type ArchivedError struct{ Name string }

// Error names the archived agent.
func (e *ArchivedError) Error() string {
	return fmt.Sprintf("agent %s is archived", e.Name)
}

// ---------------------------------------------------------------- reads -----

// Stats is the roster-card aggregate: runs and spend over the rolling week, success rate over
// the agent's whole ended history (nil until any run has ended).
type Stats struct {
	RunsWeek       int64
	SpendWeekCents int64
	SuccessRate    *float64
}

// WithStats pairs an agent with its roster stats.
type WithStats struct {
	Agent domain.Agent
	Stats Stats
}

// List returns a project's agents, oldest first. eligibleOnly narrows to delegate-eligible
// agents: enabled and not archived — the delegate pickers' and mention autocomplete's list.
func (s *Service) List(ctx context.Context, projectKey string, eligibleOnly bool) ([]WithStats, error) {
	p, err := s.st.Projects().ByKey(ctx, projectKey)
	if err != nil {
		return nil, err
	}
	ags, err := s.st.Agents().ForProject(ctx, p.ID)
	if err != nil {
		return nil, err
	}
	stats, err := s.statsFor(ctx, p.ID)
	if err != nil {
		return nil, err
	}
	out := make([]WithStats, 0, len(ags))
	for _, a := range ags {
		if eligibleOnly && (!a.Enabled || a.ArchivedAt != nil) {
			continue
		}
		out = append(out, WithStats{Agent: a, Stats: stats[a.ID]})
	}
	return out, nil
}

// Get returns one agent with stats.
func (s *Service) Get(ctx context.Context, id string) (WithStats, error) {
	a, err := s.st.Agents().ByID(ctx, id)
	if err != nil {
		return WithStats{}, err
	}
	stats, err := s.statsFor(ctx, a.ProjectID)
	if err != nil {
		return WithStats{}, err
	}
	return WithStats{Agent: a, Stats: stats[a.ID]}, nil
}

// statsFor computes every agent's stats for a project in one runs-table query.
func (s *Service) statsFor(ctx context.Context, projectID string) (map[string]Stats, error) {
	since := s.weekAgo()
	raw, err := s.st.Runs().StatsForProjectAgents(ctx, projectID, since)
	if err != nil {
		return nil, err
	}
	out := make(map[string]Stats, len(raw))
	for id, r := range raw {
		st := Stats{RunsWeek: r.RunsSince, SpendWeekCents: r.SpendCentsSince}
		if r.Ended > 0 {
			rate := float64(r.Succeeded) / float64(r.Ended)
			st.SuccessRate = &rate
		}
		out[id] = st
	}
	return out, nil
}

// weekAgo renders the stats window's start in the store's RFC3339 UTC format. Falls back to
// wall clock when the injected test clock is not parseable.
func (s *Service) weekAgo() string {
	t, err := time.Parse(time.RFC3339, s.now())
	if err != nil {
		t = time.Now().UTC()
	}
	return t.Add(-statsWindow).UTC().Format("2006-01-02T15:04:05Z")
}

// ---------------------------------------------------------------- create -----

// CreateInput is what a new agent needs. Everything but Name has a default.
type CreateInput struct {
	Name      string
	Role      string
	Color     string
	Model     string
	Effort    string
	Autonomy  string
	Directive string
}

// defaultColors is the palette new agents cycle through, keyed by roster size.
var defaultColors = []string{
	"#5b8def", "#d16ba5", "#3fa07a", "#c98a3d", "#8f6fd6", "#4aa3b8", "#c96b5a", "#7a8f3f",
}

var colorPattern = regexp.MustCompile(`^#[0-9a-fA-F]{6}$`)

// Create inserts an agent with a version-1 directive (possibly empty). Defaults: color from
// the palette, model claude-sonnet-5, effort medium, autonomy approve_each, Dev-like
// permissions, git identity from the name (D-9), limits from the schema defaults.
func (s *Service) Create(ctx context.Context, projectKey string, in CreateInput) (WithStats, error) {
	p, err := s.st.Projects().ByKey(ctx, projectKey)
	if err != nil {
		return WithStats{}, err
	}

	in.Name = strings.TrimSpace(in.Name)
	var errs []httpx.FieldError
	if in.Name == "" {
		errs = append(errs, httpx.FieldError{Field: "name", Message: "Name is required."})
	} else if len(in.Name) > 60 {
		errs = append(errs, httpx.FieldError{Field: "name", Message: "At most 60 characters."})
	}
	if in.Model == "" {
		in.Model = "claude-sonnet-5"
	}
	if !isKnown(in.Model, KnownModels) {
		errs = append(errs, httpx.FieldError{Field: "model",
			Message: "One of " + strings.Join(KnownModels, ", ") + "."})
	}
	if in.Effort == "" {
		in.Effort = "medium"
	}
	if !isKnown(in.Effort, KnownEfforts) {
		errs = append(errs, httpx.FieldError{Field: "effort", Message: "One of low, medium, high."})
	}
	if in.Autonomy == "" {
		in.Autonomy = string(domain.AutonomyApproveEach)
	}
	if !domain.Autonomy(in.Autonomy).IsValid() {
		errs = append(errs, httpx.FieldError{Field: "autonomy",
			Message: "One of suggest, approve_each, auto_gates, auto."})
	}
	if in.Color != "" && !colorPattern.MatchString(in.Color) {
		errs = append(errs, httpx.FieldError{Field: "color", Message: "A #rrggbb hex color."})
	}
	if len(errs) > 0 {
		return WithStats{}, &ValidationError{Fields: errs}
	}
	if in.Color == "" {
		existing, err := s.st.Agents().ForProject(ctx, p.ID)
		if err != nil {
			return WithStats{}, err
		}
		in.Color = defaultColors[len(existing)%len(defaultColors)]
	}

	now := s.now()
	a := domain.Agent{
		ID: domain.NewID(), ProjectID: p.ID,
		Name: in.Name, Role: strings.TrimSpace(in.Role), Color: in.Color,
		RuntimeID: "claude-code", Model: in.Model, Effort: in.Effort,
		Autonomy: domain.Autonomy(in.Autonomy),
		Permissions: domain.AgentPermissions{
			ReadFiles: true, EditFiles: true, RunCommands: true,
			PushBranches: true, OpenPRs: true, CommentPRs: true,
			SubmitReviews: false, CreateWikiPages: true,
		},
		GitAuthorName:  in.Name,
		GitAuthorEmail: GitEmailFor(in.Name),
		ConcurrencyCap: 1, MaxWallClockSeconds: 3600, MaxSteps: 200,
		Enabled:   true,
		CreatedAt: now, UpdatedAt: now,
	}
	err = CreateWithDirective(ctx, s.st, s.audit, &a, in.Directive,
		"Initial directive", s.actorUserID(ctx), now)
	if errors.Is(err, store.ErrUnique) {
		return WithStats{}, &NameTakenError{Name: in.Name}
	}
	if err != nil {
		return WithStats{}, err
	}
	s.emitAgent(ctx, "created", a)
	return WithStats{Agent: a}, nil
}

// Starter creates the starter roster (Dev + Reviewer) in a project, skipping names that
// already exist. Returns the created agents' names.
func (s *Service) Starter(ctx context.Context, projectKey string) ([]string, error) {
	p, err := s.st.Projects().ByKey(ctx, projectKey)
	if err != nil {
		return nil, err
	}
	existing := map[string]bool{}
	ags, err := s.st.Agents().ForProject(ctx, p.ID)
	if err != nil {
		return nil, err
	}
	for _, a := range ags {
		existing[a.Name] = true
	}
	var created []string
	for _, cand := range StarterCandidates(nil) {
		if existing[cand.Name] {
			continue
		}
		now := s.now()
		a := StarterAgent(cand, p.ID, now)
		if err := CreateWithDirective(ctx, s.st, s.audit, &a, cand.Directive,
			"Starter directive", s.actorUserID(ctx), now); err != nil {
			return nil, err
		}
		s.emitAgent(ctx, "created", a)
		created = append(created, a.Name)
	}
	return created, nil
}

// actorUserID returns the requesting human's ID, or "" for non-human actors.
func (s *Service) actorUserID(ctx context.Context) string {
	if a, ok := auth.ActorFrom(ctx); ok && a.Kind == domain.ActorHuman {
		return a.ID
	}
	return ""
}

// ---------------------------------------------------------------- update -----

// OptInt is a tri-state *int64 for PATCH bodies: absent, null (clear), or a value.
type OptInt struct {
	Set   bool
	Null  bool
	Value int64
}

// UpdatePatch is a PATCH /agents/{id} body: absent fields are unchanged. Permissions replace
// wholesale — the §3.1 object is small and typed, so partial permission patches would only
// invite drift.
type UpdatePatch struct {
	Name                *string
	Role                *string
	Color               *string
	Model               *string
	Effort              *string
	Autonomy            *string
	Permissions         *domain.AgentPermissions
	GitAuthorName       *string
	GitAuthorEmail      *string
	Enabled             *bool
	ConcurrencyCap      *int64
	DailyCapCents       OptInt // null clears → inherit the project/workspace default
	MaxWallClockSeconds *int64
	MaxSteps            *int64
}

// Update applies a patch.
func (s *Service) Update(ctx context.Context, id string, patch UpdatePatch) (WithStats, error) {
	before, err := s.st.Agents().ByID(ctx, id)
	if err != nil {
		return WithStats{}, err
	}
	if before.ArchivedAt != nil {
		return WithStats{}, &ArchivedError{Name: before.Name}
	}
	a := before

	var errs []httpx.FieldError
	if patch.Name != nil {
		if n := strings.TrimSpace(*patch.Name); n == "" {
			errs = append(errs, httpx.FieldError{Field: "name", Message: "Name is required."})
		} else if len(n) > 60 {
			errs = append(errs, httpx.FieldError{Field: "name", Message: "At most 60 characters."})
		} else {
			a.Name = n
		}
	}
	if patch.Role != nil {
		a.Role = strings.TrimSpace(*patch.Role)
	}
	if patch.Color != nil {
		if !colorPattern.MatchString(*patch.Color) {
			errs = append(errs, httpx.FieldError{Field: "color", Message: "A #rrggbb hex color."})
		} else {
			a.Color = *patch.Color
		}
	}
	if patch.Model != nil {
		if !isKnown(*patch.Model, KnownModels) {
			errs = append(errs, httpx.FieldError{Field: "model",
				Message: "One of " + strings.Join(KnownModels, ", ") + "."})
		} else {
			a.Model = *patch.Model
		}
	}
	if patch.Effort != nil {
		if !isKnown(*patch.Effort, KnownEfforts) {
			errs = append(errs, httpx.FieldError{Field: "effort", Message: "One of low, medium, high."})
		} else {
			a.Effort = *patch.Effort
		}
	}
	if patch.Autonomy != nil {
		if !domain.Autonomy(*patch.Autonomy).IsValid() {
			errs = append(errs, httpx.FieldError{Field: "autonomy",
				Message: "One of suggest, approve_each, auto_gates, auto."})
		} else {
			a.Autonomy = domain.Autonomy(*patch.Autonomy)
		}
	}
	if patch.Permissions != nil {
		a.Permissions = *patch.Permissions
	}
	if patch.GitAuthorName != nil {
		if n := strings.TrimSpace(*patch.GitAuthorName); n == "" {
			errs = append(errs, httpx.FieldError{Field: "git_author_name",
				Message: "Git author name is required."})
		} else {
			a.GitAuthorName = n
		}
	}
	if patch.GitAuthorEmail != nil {
		e := strings.TrimSpace(*patch.GitAuthorEmail)
		if e == "" || !strings.Contains(e, "@") {
			errs = append(errs, httpx.FieldError{Field: "git_author_email",
				Message: "A valid email address."})
		} else {
			a.GitAuthorEmail = e
		}
	}
	if patch.Enabled != nil {
		a.Enabled = *patch.Enabled
	}
	if patch.ConcurrencyCap != nil {
		if *patch.ConcurrencyCap < 1 {
			errs = append(errs, httpx.FieldError{Field: "concurrency_cap", Message: "At least 1."})
		} else {
			a.ConcurrencyCap = *patch.ConcurrencyCap
		}
	}
	if patch.DailyCapCents.Set {
		if patch.DailyCapCents.Null {
			a.DailyCapCents = nil
		} else if patch.DailyCapCents.Value < 0 {
			errs = append(errs, httpx.FieldError{Field: "daily_cap_cents", Message: "Zero or more."})
		} else {
			v := patch.DailyCapCents.Value
			a.DailyCapCents = &v
		}
	}
	if patch.MaxWallClockSeconds != nil {
		if *patch.MaxWallClockSeconds < 60 {
			errs = append(errs, httpx.FieldError{Field: "max_wall_clock_seconds",
				Message: "At least 60 seconds."})
		} else {
			a.MaxWallClockSeconds = *patch.MaxWallClockSeconds
		}
	}
	if patch.MaxSteps != nil {
		if *patch.MaxSteps < 1 {
			errs = append(errs, httpx.FieldError{Field: "max_steps", Message: "At least 1."})
		} else {
			a.MaxSteps = *patch.MaxSteps
		}
	}
	if len(errs) > 0 {
		return WithStats{}, &ValidationError{Fields: errs}
	}

	a.UpdatedAt = s.now()
	err = s.st.Agents().Update(ctx, &a)
	if errors.Is(err, store.ErrUnique) {
		return WithStats{}, &NameTakenError{Name: a.Name}
	}
	if err != nil {
		return WithStats{}, err
	}
	if err := s.audit.Write(ctx, "agent.update",
		audit.Target{Kind: "agent", ID: a.ID, ProjectID: a.ProjectID}, before, a); err != nil {
		return WithStats{}, err
	}
	s.emitAgent(ctx, "updated", a)
	return s.Get(ctx, a.ID)
}

// Archive stamps archived_at and disables the agent. History (runs, directives, mentions)
// stays readable; the agent disappears from rosters' active section and every picker.
func (s *Service) Archive(ctx context.Context, id string) error {
	before, err := s.st.Agents().ByID(ctx, id)
	if err != nil {
		return err
	}
	if before.ArchivedAt != nil {
		return nil // idempotent
	}
	a := before
	now := s.now()
	a.ArchivedAt = &now
	a.Enabled = false
	a.UpdatedAt = now
	if err := s.st.Agents().Update(ctx, &a); err != nil {
		return err
	}
	if err := s.audit.Write(ctx, "agent.archive",
		audit.Target{Kind: "agent", ID: a.ID, ProjectID: a.ProjectID}, before, a); err != nil {
		return err
	}
	s.emitAgent(ctx, "archived", a)
	return nil
}

func isKnown(v string, list []string) bool {
	for _, k := range list {
		if v == k {
			return true
		}
	}
	return false
}
