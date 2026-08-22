package triggers

import (
	"encoding/json"
	"fmt"
	"path"
	"regexp"
	"sort"
	"strings"

	"github.com/spruce/lexicode/internal/domain"
	"github.com/spruce/lexicode/internal/kernel/httpx"
	"github.com/spruce/lexicode/internal/kernel/ports"
)

// Save-time validation. The rules, and where they deliberately stop:
//
//   - Event kind and activity types must exist in the stored source's Catalog (contracts §2.1)
//     — the trigger editor is generated from catalogs, so a rule the editor could not have
//     built is refused. This means internal kinds (`ticket`, `run`) are not storable until a
//     source catalogs them, which is the plan's sequencing, not an accident.
//   - The condition tree is checked structurally: known §4.1 operators, syntactically valid
//     field paths, one shape per node, bounded depth and size. Field paths are NOT checked
//     against the catalog: evaluation is nil-safe by design, and refusing unknown paths would
//     turn every future payload addition into a migration.
//   - Action IDs are validated against the registry only when the registry knows them — then
//     the action's own Describe vets the params. An unregistered ID is stored (S28 ships the
//     actions after this story) and fires as `errored` naming the missing action, so a typo is
//     visible in the firing history rather than silently dropped, and a rule authored before
//     its action module loads starts working the moment the module registers.
const (
	maxConditionDepth = 10
	maxConditionNodes = 100
	maxActions        = 10
)

var fieldPathPattern = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*(\.[A-Za-z_][A-Za-z0-9_]*)*$`)

// validate checks the merged row and returns a *ValidationError naming every problem at once.
func (s *Service) validate(tr *domain.Trigger) error {
	var errs []httpx.FieldError
	add := func(field, msg string) { errs = append(errs, httpx.FieldError{Field: field, Message: msg}) }

	if tr.Name == "" {
		add("name", "Name is required.")
	} else if len(tr.Name) > 120 {
		add("name", "At most 120 characters.")
	}

	desc := s.validateWhen(tr, add)
	s.validateFilters(tr, desc, add)
	validateConditions(tr.Conditions, add)
	s.validateActions(tr.Actions, add)
	validateLoopConfig(tr.LoopConfig, add)

	if tr.Cron != nil && tr.Event != "schedule" {
		add("cron", "Cron applies only to schedule triggers.")
	}

	if len(errs) > 0 {
		return &ValidationError{Fields: errs}
	}
	return nil
}

// validateWhen checks source, event kind and activity types against the registered catalogs,
// returning the matched descriptor (nil when anything upstream of it failed).
func (s *Service) validateWhen(tr *domain.Trigger, add func(field, msg string)) *ports.EventDescriptor {
	var src ports.EventSource
	var known []string
	for _, es := range s.sources() {
		known = append(known, es.ID())
		if es.ID() == tr.SourceID {
			src = es
		}
	}
	if src == nil {
		if len(known) == 0 {
			add("source_id", "No event sources are registered.")
		} else {
			sort.Strings(known)
			add("source_id", fmt.Sprintf("Unknown event source %q; registered: %s.",
				tr.SourceID, strings.Join(known, ", ")))
		}
		return nil
	}

	catalog := src.Catalog()
	var desc *ports.EventDescriptor
	var kinds []string
	for i := range catalog.Events {
		kinds = append(kinds, catalog.Events[i].Kind)
		if catalog.Events[i].Kind == tr.Event {
			desc = &catalog.Events[i]
		}
	}
	if tr.Event == "" {
		add("event", "Event kind is required.")
		return nil
	}
	if desc == nil {
		add("event", fmt.Sprintf("Source %q has no event kind %q; known: %s.",
			tr.SourceID, tr.Event, strings.Join(kinds, ", ")))
		return nil
	}

	var acts []string
	if err := json.Unmarshal(tr.ActivityTypes, &acts); err != nil {
		add("activity_types", "A JSON array of activity types.")
		return desc
	}
	for _, a := range acts {
		found := false
		for _, at := range desc.ActivityTypes {
			if at.Value == a {
				found = true
				break
			}
		}
		if !found {
			var values []string
			for _, at := range desc.ActivityTypes {
				values = append(values, at.Value)
			}
			add("activity_types", fmt.Sprintf("%q is not an activity type of %s; known: %s.",
				a, tr.Event, strings.Join(values, ", ")))
		}
	}
	return desc
}

// validateFilters checks structure and glob syntax, and that each used filter is one the
// descriptor offers.
func (s *Service) validateFilters(tr *domain.Trigger, desc *ports.EventDescriptor, add func(field, msg string)) {
	var f triggerFilters
	dec := json.NewDecoder(strings.NewReader(string(tr.Filters)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&f); err != nil {
		add("filters", "An object with branches, paths and labels lists.")
		return
	}
	offered := map[string]bool{}
	if desc != nil {
		for _, ff := range desc.Filters {
			offered[ff.Key] = true
		}
	}
	check := func(key string, patterns []string, glob bool) {
		if len(patterns) == 0 {
			return
		}
		if desc != nil && !offered[key] {
			add("filters", fmt.Sprintf("The %s filter does not apply to %s events.", key, tr.Event))
		}
		for _, p := range patterns {
			if p == "" {
				add("filters", fmt.Sprintf("The %s filter has an empty entry.", key))
				continue
			}
			if glob {
				if _, err := path.Match(p, "probe"); err != nil {
					add("filters", fmt.Sprintf("%q is not a valid glob (path.Match syntax).", p))
				}
			}
		}
	}
	check("branches", f.Branches, true)
	check("paths", f.Paths, true)
	check("labels", f.Labels, false)
}

// validateConditions checks the tree: each node is exactly one of {all}, {any} or
// {field, op, value}; operators come from contracts §4.1; field paths are dotted identifiers
// (or absent for actor.* operators, which default to the actor sub-object); depth and node
// count are bounded so a stored rule can never be pathological to walk.
func validateConditions(raw json.RawMessage, add func(field, msg string)) {
	nodes := 0
	var walk func(raw json.RawMessage, at string, depth int)
	walk = func(raw json.RawMessage, at string, depth int) {
		nodes++
		if nodes > maxConditionNodes {
			return
		}
		if depth > maxConditionDepth {
			add("conditions", fmt.Sprintf("%s: nested deeper than %d levels.", at, maxConditionDepth))
			return
		}
		var m map[string]json.RawMessage
		if err := json.Unmarshal(raw, &m); err != nil {
			add("conditions", fmt.Sprintf("%s: must be a JSON object.", at))
			return
		}
		if branch, ok := m["all"]; ok {
			validateBranch(m, branch, at, "all", depth, walk, add)
			return
		}
		if branch, ok := m["any"]; ok {
			validateBranch(m, branch, at, "any", depth, walk, add)
			return
		}
		validateLeaf(m, at, add)
	}
	walk(raw, "conditions", 1)
	if nodes > maxConditionNodes {
		add("conditions", fmt.Sprintf("More than %d nodes.", maxConditionNodes))
	}
}

func validateBranch(m map[string]json.RawMessage, branch json.RawMessage, at, kind string, depth int,
	walk func(json.RawMessage, string, int), add func(field, msg string)) {
	if len(m) != 1 {
		add("conditions", fmt.Sprintf("%s: a %q node carries nothing else.", at, kind))
		return
	}
	var children []json.RawMessage
	if err := json.Unmarshal(branch, &children); err != nil {
		add("conditions", fmt.Sprintf("%s: %q must be an array of nodes.", at, kind))
		return
	}
	for i, c := range children {
		walk(c, fmt.Sprintf("%s.%s[%d]", at, kind, i), depth+1)
	}
}

func validateLeaf(m map[string]json.RawMessage, at string, add func(field, msg string)) {
	for k := range m {
		if k != "field" && k != "op" && k != "value" {
			add("conditions", fmt.Sprintf("%s: unknown key %q — a node is {all}, {any} or {field, op, value}.", at, k))
			return
		}
	}
	var op string
	if raw, ok := m["op"]; !ok || json.Unmarshal(raw, &op) != nil || op == "" {
		add("conditions", fmt.Sprintf("%s: an op is required.", at))
		return
	}
	if _, known := operators[op]; !known {
		add("conditions", fmt.Sprintf("%s: unknown operator %q.", at, op))
		return
	}
	var field string
	if raw, ok := m["field"]; ok {
		if json.Unmarshal(raw, &field) != nil {
			add("conditions", fmt.Sprintf("%s: field must be a string.", at))
			return
		}
	}
	if field == "" {
		// Only actor.* operators have an implied field (the actor sub-object).
		if !strings.HasPrefix(op, "actor.") {
			add("conditions", fmt.Sprintf("%s: a field is required for %s.", at, op))
		}
		return
	}
	if !fieldPathPattern.MatchString(field) {
		add("conditions", fmt.Sprintf("%s: %q is not a payload path (dotted identifiers).", at, field))
	}
}

// validateActions checks the ordered [{action_id, params}] list. Registered IDs get their
// params vetted by the action's Describe; unregistered IDs are stored deliberately (see the
// package note above).
func (s *Service) validateActions(raw json.RawMessage, add func(field, msg string)) {
	var refs []actionRef
	dec := json.NewDecoder(strings.NewReader(string(raw)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&refs); err != nil {
		add("actions", "A JSON array of {action_id, params}.")
		return
	}
	if len(refs) > maxActions {
		add("actions", fmt.Sprintf("At most %d actions.", maxActions))
		return
	}
	for i, ref := range refs {
		if ref.ActionID == "" {
			add("actions", fmt.Sprintf("actions[%d]: action_id is required.", i))
			continue
		}
		if len(ref.Params) > 0 && !json.Valid(ref.Params) {
			add("actions", fmt.Sprintf("actions[%d]: params must be JSON.", i))
			continue
		}
		act, err := s.action(ref.ActionID)
		if err != nil {
			continue // not registered yet; validated at fire time as `errored`
		}
		if _, err := act.Describe(ref.Params); err != nil {
			add("actions", fmt.Sprintf("actions[%d] (%s): %s", i, ref.ActionID, err.Error()))
		}
	}
}

// loopConfig is data model §6.1, decoded strictly so a typoed key is a save-time error rather
// than a silently-defaulted layer.
type loopConfig struct {
	ActorSuppression bool   `json:"actor_suppression"`
	DebounceSeconds  int64  `json:"debounce_seconds"`
	CancelInProgress bool   `json:"cancel_in_progress"`
	DepthLimit       int64  `json:"depth_limit"`
	DailyBudgetCents *int64 `json:"daily_budget_cents"`
}

func validateLoopConfig(raw json.RawMessage, add func(field, msg string)) {
	var lc loopConfig
	dec := json.NewDecoder(strings.NewReader(string(raw)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&lc); err != nil {
		add("loop_config", "The §6.1 shape: actor_suppression, debounce_seconds, cancel_in_progress, depth_limit, daily_budget_cents.")
		return
	}
	if lc.DebounceSeconds < 0 {
		add("loop_config", "debounce_seconds must be >= 0.")
	}
	if lc.DepthLimit < 0 {
		add("loop_config", "depth_limit must be >= 0.")
	}
	if lc.DailyBudgetCents != nil && *lc.DailyBudgetCents < 0 {
		add("loop_config", "daily_budget_cents must be >= 0 or null.")
	}
}
