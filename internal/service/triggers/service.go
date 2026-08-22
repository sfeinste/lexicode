package triggers

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"

	"github.com/spruce/lexicode/internal/domain"
	"github.com/spruce/lexicode/internal/kernel/audit"
	"github.com/spruce/lexicode/internal/kernel/bus"
	"github.com/spruce/lexicode/internal/kernel/httpx"
	"github.com/spruce/lexicode/internal/kernel/ports"
	"github.com/spruce/lexicode/internal/kernel/store"
)

// Service is the trigger CRUD surface (contracts §5): list/create per project, get/patch/delete
// per trigger, and the firing history. The engine (engine.go) is a separate object over the
// same package's pipeline pieces, wired independently in cmd/lexicode.
type Service struct {
	st      *store.Store
	audit   *audit.Writer
	bus     *bus.Bus
	sources func() []ports.EventSource
	action  func(id string) (ports.TriggerAction, error)
	actions func() []ports.TriggerAction
	logger  *slog.Logger
	now     func() string
}

// Options configures New.
type Options struct {
	// Store is the open, migrated database. Required.
	Store *store.Store
	// Audit is the audit-log writer. Required — every mutation writes an entry.
	Audit *audit.Writer
	// Bus emits internal events for mutations. Nil (tests) skips emission.
	Bus *bus.Bus
	// Sources lists the registered event sources — kernel.EventSources in production. Their
	// catalogs are what event kinds and activity types validate against. Nil means none are
	// registered, and no trigger can be created.
	Sources func() []ports.EventSource
	// Action resolves an action_id — kernel.Action in production. Save-time validation checks
	// params only for IDs the registry knows; unknown IDs are stored and fire as `errored`
	// (see validate.go). Nil means nothing is registered.
	Action func(id string) (ports.TriggerAction, error)
	// Actions lists every registered action — kernel.Actions in production. The
	// trigger-catalog endpoint (S29) renders the THEN picker from it. Nil means none.
	Actions func() []ports.TriggerAction
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
	sources := opts.Sources
	if sources == nil {
		sources = func() []ports.EventSource { return nil }
	}
	action := opts.Action
	if action == nil {
		action = func(id string) (ports.TriggerAction, error) {
			return nil, fmt.Errorf("no trigger actions are registered")
		}
	}
	actions := opts.Actions
	if actions == nil {
		actions = func() []ports.TriggerAction { return nil }
	}
	return &Service{st: opts.Store, audit: opts.Audit, bus: opts.Bus,
		sources: sources, action: action, actions: actions, logger: logger, now: now}
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

// InUseError is a delete refused because other rows — a ticket's provenance, a run — still
// reference the trigger. The HTTP layer renders it as a 409 `trigger_in_use`.
type InUseError struct{ Name string }

// Error says what to do instead.
func (e *InUseError) Error() string {
	return fmt.Sprintf("trigger %q is referenced by tickets or runs it created; disable it instead of deleting it", e.Name)
}

// WithHealth is one trigger plus its rule-health aggregate (last 50 firings), the recent
// outcome sequence the S29 sparkline renders, and each action's Describe() sentence for the
// THEN line of the prose card.
type WithHealth struct {
	Trigger domain.Trigger
	Health  store.Health
	// Recent is the last (up to) 20 firing outcomes, oldest first — the sparkline reads
	// left-to-right in time order.
	Recent []domain.FiringOutcome
	// ActionSummaries is one sentence per configured action, in action order — the
	// registered action's Describe() ("run agent Reviewer"), or a plain fallback naming the
	// action_id when the action is unregistered or its params no longer describe.
	ActionSummaries []string
}

// sparklineN is how many recent firings the card sparkline shows (UI spec §5.9: "last ~20").
const sparklineN = 20

// withHealth assembles the read model for one trigger.
func (s *Service) withHealth(ctx context.Context, tr domain.Trigger) (WithHealth, error) {
	h, err := s.st.Firings().HealthFor(ctx, tr.ID, 50)
	if err != nil {
		return WithHealth{}, err
	}
	recentRows, err := s.st.Firings().ForTrigger(ctx, tr.ID, sparklineN)
	if err != nil {
		return WithHealth{}, err
	}
	// ForTrigger returns newest first; the sparkline reads oldest first.
	recent := make([]domain.FiringOutcome, 0, len(recentRows))
	for i := len(recentRows) - 1; i >= 0; i-- {
		recent = append(recent, recentRows[i].Outcome)
	}
	return WithHealth{Trigger: tr, Health: h, Recent: recent,
		ActionSummaries: s.describeActions(tr.Actions)}, nil
}

// describeActions renders each stored action in words via its registered Describe. An
// unregistered ID (or params Describe refuses — possible when the module's validation
// tightened after the rule was stored) degrades to naming the action rather than erroring
// the whole list.
func (s *Service) describeActions(raw json.RawMessage) []string {
	var refs []actionRef
	if err := json.Unmarshal(raw, &refs); err != nil {
		return []string{}
	}
	out := make([]string, 0, len(refs))
	for _, ref := range refs {
		act, err := s.action(ref.ActionID)
		if err != nil {
			out = append(out, ref.ActionID+" (not registered)")
			continue
		}
		desc, err := act.Describe(ref.Params)
		if err != nil {
			out = append(out, act.Label())
			continue
		}
		out = append(out, desc)
	}
	return out
}

// Input is the create/patch body: every field optional so PATCH is a true partial. Create
// applies defaults for what is absent (source github.poll, empty filters, `{"all":[]}`
// conditions, no actions, the §6.1 loop config) and requires name and event.
type Input struct {
	Name          *string          `json:"name"`
	Enabled       *bool            `json:"enabled"`
	SourceID      *string          `json:"source_id"`
	Event         *string          `json:"event"`
	ActivityTypes *[]string        `json:"activity_types"`
	Filters       *triggerFilters  `json:"filters"`
	Conditions    *json.RawMessage `json:"conditions"`
	Actions       *json.RawMessage `json:"actions"`
	LoopConfig    *json.RawMessage `json:"loop_config"`
	// Cron applies only to `schedule` triggers; "" clears it.
	Cron *string `json:"cron"`
}

// List returns the project's triggers, oldest first, each with its health aggregate.
func (s *Service) List(ctx context.Context, projectKey string) ([]WithHealth, error) {
	p, err := s.st.Projects().ByKey(ctx, projectKey)
	if err != nil {
		return nil, err
	}
	trs, err := s.st.Triggers().ForProject(ctx, p.ID)
	if err != nil {
		return nil, err
	}
	out := make([]WithHealth, 0, len(trs))
	for _, tr := range trs {
		wh, err := s.withHealth(ctx, tr)
		if err != nil {
			return nil, err
		}
		out = append(out, wh)
	}
	return out, nil
}

// Get returns one trigger with health.
func (s *Service) Get(ctx context.Context, id string) (WithHealth, error) {
	tr, err := s.st.Triggers().ByID(ctx, id)
	if err != nil {
		return WithHealth{}, err
	}
	return s.withHealth(ctx, tr)
}

// Create validates and inserts a trigger. Unlike bootstrap's suggested rules, an explicitly
// created trigger defaults to enabled — the author is looking at it.
func (s *Service) Create(ctx context.Context, projectKey string, in Input, userID string) (domain.Trigger, error) {
	p, err := s.st.Projects().ByKey(ctx, projectKey)
	if err != nil {
		return domain.Trigger{}, err
	}

	now := s.now()
	tr := domain.Trigger{
		ID: domain.NewID(), ProjectID: p.ID,
		Enabled:       true,
		SourceID:      "github.poll",
		ActivityTypes: json.RawMessage(`[]`),
		Filters:       json.RawMessage(`{}`),
		Conditions:    json.RawMessage(`{"all":[]}`),
		Actions:       json.RawMessage(`[]`),
		LoopConfig:    domain.DefaultLoopConfig(),
		CreatedAt:     now, UpdatedAt: now,
	}
	if userID != "" {
		tr.CreatedBy = &userID
	}
	applyInput(&tr, in)
	if err := s.validate(&tr); err != nil {
		return domain.Trigger{}, err
	}
	if err := s.st.Triggers().Create(ctx, &tr); err != nil {
		return domain.Trigger{}, err
	}
	if err := s.audit.Write(ctx, "trigger.create",
		audit.Target{Kind: "trigger", ID: tr.ID, ProjectID: p.ID}, nil, snapshot(tr)); err != nil {
		return domain.Trigger{}, err
	}
	s.emitTrigger(ctx, "created", tr)
	return tr, nil
}

// Update applies a partial patch, re-validating the merged row.
func (s *Service) Update(ctx context.Context, id string, in Input) (domain.Trigger, error) {
	tr, err := s.st.Triggers().ByID(ctx, id)
	if err != nil {
		return domain.Trigger{}, err
	}
	before := snapshot(tr)
	applyInput(&tr, in)
	if err := s.validate(&tr); err != nil {
		return domain.Trigger{}, err
	}
	tr.UpdatedAt = s.now()
	if err := s.st.Triggers().Update(ctx, &tr); err != nil {
		return domain.Trigger{}, err
	}
	if err := s.audit.Write(ctx, "trigger.update",
		audit.Target{Kind: "trigger", ID: tr.ID, ProjectID: tr.ProjectID},
		before, snapshot(tr)); err != nil {
		return domain.Trigger{}, err
	}
	s.emitTrigger(ctx, "updated", tr)
	return tr, nil
}

// Delete removes the trigger and its firing history. A trigger still referenced by rows the
// engine cannot delete for it — tickets it filed, runs it started — refuses with InUseError:
// history beats tidiness (D-15's spirit), and disabling covers "make it stop".
func (s *Service) Delete(ctx context.Context, id string) error {
	tr, err := s.st.Triggers().ByID(ctx, id)
	if err != nil {
		return err
	}
	err = s.st.Tx(ctx, func(tx *store.Tx) error {
		return tx.Triggers().Delete(ctx, id)
	})
	if errors.Is(err, store.ErrForeignKey) {
		return &InUseError{Name: tr.Name}
	}
	if err != nil {
		return err
	}
	if err := s.audit.Write(ctx, "trigger.delete",
		audit.Target{Kind: "trigger", ID: tr.ID, ProjectID: tr.ProjectID},
		snapshot(tr), nil); err != nil {
		return err
	}
	s.emitTrigger(ctx, "deleted", tr)
	return nil
}

// Firings returns the trigger's firing history, newest first.
func (s *Service) Firings(ctx context.Context, id string, limit int) ([]domain.TriggerFiring, error) {
	if _, err := s.st.Triggers().ByID(ctx, id); err != nil {
		return nil, err
	}
	return s.st.Firings().ForTrigger(ctx, id, limit)
}

// applyInput merges the present fields of in over tr.
func applyInput(tr *domain.Trigger, in Input) {
	if in.Name != nil {
		tr.Name = strings.TrimSpace(*in.Name)
	}
	if in.Enabled != nil {
		tr.Enabled = *in.Enabled
	}
	if in.SourceID != nil {
		tr.SourceID = *in.SourceID
	}
	if in.Event != nil {
		tr.Event = *in.Event
	}
	if in.ActivityTypes != nil {
		b, _ := json.Marshal(*in.ActivityTypes)
		tr.ActivityTypes = b
	}
	if in.Filters != nil {
		b, _ := json.Marshal(*in.Filters)
		tr.Filters = b
	}
	if in.Conditions != nil {
		tr.Conditions = append(json.RawMessage(nil), *in.Conditions...)
	}
	if in.Actions != nil {
		tr.Actions = append(json.RawMessage(nil), *in.Actions...)
	}
	if in.LoopConfig != nil {
		tr.LoopConfig = append(json.RawMessage(nil), *in.LoopConfig...)
	}
	if in.Cron != nil {
		if *in.Cron == "" {
			tr.Cron = nil
		} else {
			c := strings.TrimSpace(*in.Cron)
			tr.Cron = &c
		}
	}
}

// emitTrigger publishes a `trigger` bus event for a CRUD mutation. The engine ignores the
// `trigger` kind entirely, so these can never feed the pipeline; they exist for the SSE
// surface. Best-effort: the mutation is committed and audited by the time this runs.
func (s *Service) emitTrigger(ctx context.Context, activity string, tr domain.Trigger) {
	if s.bus == nil {
		return
	}
	payload, err := json.Marshal(map[string]any{
		"trigger": map[string]any{"id": tr.ID, "name": tr.Name, "enabled": tr.Enabled},
	})
	if err != nil {
		return
	}
	pid, tid := tr.ProjectID, tr.ID
	e := domain.Event{
		ProjectID: &pid, Kind: "trigger", ActivityType: activity,
		ActorKind: domain.ActorSystem, SubjectKind: "trigger", SubjectID: &tid,
		Payload: payload, OccurredAt: s.now(),
	}
	if err := s.bus.Emit(ctx, e); err != nil {
		s.logger.Error("triggers: emit failed",
			slog.String("kind", "trigger."+activity), slog.String("error", err.Error()))
	}
}

// snapshot is the audit form of a trigger: the raw JSON columns inline rather than
// base64-encoded bytes.
func snapshot(tr domain.Trigger) json.RawMessage {
	b, err := json.Marshal(map[string]any{
		"id": tr.ID, "project_id": tr.ProjectID, "name": tr.Name, "enabled": tr.Enabled,
		"source_id": tr.SourceID, "event": tr.Event,
		"activity_types": json.RawMessage(tr.ActivityTypes),
		"filters":        json.RawMessage(tr.Filters),
		"conditions":     json.RawMessage(tr.Conditions),
		"actions":        json.RawMessage(tr.Actions),
		"loop_config":    json.RawMessage(tr.LoopConfig),
		"cron":           tr.Cron,
	})
	if err != nil {
		return json.RawMessage(`{}`)
	}
	return b
}
