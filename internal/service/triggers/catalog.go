package triggers

import (
	"net/http"
	"sort"

	"github.com/spruce/lexicode/internal/kernel/httpx"
	"github.com/spruce/lexicode/internal/kernel/ports"
)

// GET /projects/{key}/trigger-catalog (contracts §5, S29): the one payload the trigger
// editor is generated from. It merges, per request:
//
//   - every registered EventSource's Catalog() — the WHEN options (event kinds, activity
//     types, filters) and the IF/interpolation field vocabulary (each descriptor's `fields`
//     doubles as the {{...}} picker's list for that event kind);
//   - every registered TriggerAction's {id, label, Schema()} — the THEN options;
//   - the static contracts §4.1 operator table — the IF operator dropdowns, type-prefixed,
//     so the frontend never hardcodes an operator.
//
// A new event source or action appears in the editor with no frontend change — the S32
// acceptance ("the cron source shows up by itself") rides on this endpoint.

// catalogResponse is the whole editor payload.
type catalogResponse struct {
	Sources   []catalogSource   `json:"sources"`
	Actions   []catalogAction   `json:"actions"`
	Operators []catalogOperator `json:"operators"`
}

// catalogSource is one EventSource's contribution, keyed by its port ID.
type catalogSource struct {
	ID     string                  `json:"id"`
	Events []ports.EventDescriptor `json:"events"`
}

// catalogAction is one TriggerAction's contribution.
type catalogAction struct {
	ID     string            `json:"id"`
	Label  string            `json:"label"`
	Schema ports.ParamSchema `json:"schema"`
}

// catalogOperator is one contracts §4.1 operator, with everything the IF row needs to
// render it: the family is the visible type prefix ("(text) contains"), Value says what
// input the right-hand side takes, and Field says whether a payload path is stored
// (actor.* operators imply the actor sub-object and store none).
type catalogOperator struct {
	Op     string `json:"op"`
	Family string `json:"family"` // "text" | "number" | "enum" | "bool" | "set" | "actor"
	Label  string `json:"label"`
	// Value is the right-hand input kind: "text" | "number" | "bool" | "enum" |
	// "enum_list" | "none".
	Value string `json:"value"`
	// Field is false for operators with an implied field (actor.*).
	Field bool `json:"field"`
}

// operatorCatalog is the §4.1 table, in dropdown order per family. It must stay in lockstep
// with the `operators` evaluation map (conditions.go); TestOperatorCatalogComplete pins that.
var operatorCatalog = []catalogOperator{
	{Op: "text.is", Family: "text", Label: "is", Value: "text", Field: true},
	{Op: "text.is_not", Family: "text", Label: "is not", Value: "text", Field: true},
	{Op: "text.contains", Family: "text", Label: "contains", Value: "text", Field: true},
	{Op: "text.not_contains", Family: "text", Label: "does not contain", Value: "text", Field: true},
	{Op: "text.starts_with", Family: "text", Label: "starts with", Value: "text", Field: true},
	{Op: "text.matches_glob", Family: "text", Label: "matches glob", Value: "text", Field: true},
	{Op: "text.is_empty", Family: "text", Label: "is empty", Value: "none", Field: true},

	{Op: "number.eq", Family: "number", Label: "equals", Value: "number", Field: true},
	{Op: "number.gt", Family: "number", Label: "greater than", Value: "number", Field: true},
	{Op: "number.gte", Family: "number", Label: "at least", Value: "number", Field: true},
	{Op: "number.lt", Family: "number", Label: "less than", Value: "number", Field: true},
	{Op: "number.lte", Family: "number", Label: "at most", Value: "number", Field: true},

	{Op: "enum.is", Family: "enum", Label: "is", Value: "enum", Field: true},
	{Op: "enum.is_not", Family: "enum", Label: "is not", Value: "enum", Field: true},
	{Op: "enum.in", Family: "enum", Label: "is one of", Value: "enum_list", Field: true},

	{Op: "bool.is", Family: "bool", Label: "is", Value: "bool", Field: true},

	{Op: "set.includes", Family: "set", Label: "includes", Value: "text", Field: true},
	{Op: "set.excludes", Family: "set", Label: "does not include", Value: "text", Field: true},
	{Op: "set.is_empty", Family: "set", Label: "is empty", Value: "none", Field: true},

	{Op: "actor.is_agent", Family: "actor", Label: "is an agent", Value: "none", Field: false},
	{Op: "actor.is_human", Family: "actor", Label: "is a human", Value: "none", Field: false},
	{Op: "actor.is", Family: "actor", Label: "is", Value: "text", Field: false},
}

func (s *Service) handleCatalog(w http.ResponseWriter, r *http.Request) {
	// Membership was already checked against the path's project; the catalog itself is not
	// project-scoped — sources and actions register at boot, workspace-wide.
	resp := catalogResponse{
		Sources:   []catalogSource{},
		Actions:   []catalogAction{},
		Operators: operatorCatalog,
	}
	for _, src := range s.sources() {
		resp.Sources = append(resp.Sources, catalogSource{ID: src.ID(), Events: src.Catalog().Events})
	}
	sort.Slice(resp.Sources, func(i, j int) bool { return resp.Sources[i].ID < resp.Sources[j].ID })
	for _, act := range s.actions() {
		resp.Actions = append(resp.Actions, catalogAction{ID: act.ID(), Label: act.Label(), Schema: act.Schema()})
	}
	sort.Slice(resp.Actions, func(i, j int) bool { return resp.Actions[i].ID < resp.Actions[j].ID })
	httpx.WriteJSON(w, http.StatusOK, resp)
}
