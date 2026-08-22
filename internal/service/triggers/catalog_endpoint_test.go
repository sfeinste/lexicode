package triggers_test

import (
	"encoding/json"
	"net/http"
	"testing"
)

// TestTriggerCatalog exercises GET /projects/{key}/trigger-catalog: the merged payload
// carries every registered source's events, every registered action with its schema, and
// the full operator table with visible type prefixes.
func TestTriggerCatalog(t *testing.T) {
	e := newEnv(t)
	c := e.owner()
	e.project(c, "CAT")

	status, body := e.doJSON(c, "GET", "/api/v1/projects/CAT/trigger-catalog", "")
	if status != http.StatusOK {
		t.Fatalf("catalog = %d, want 200: %s", status, body)
	}
	var resp struct {
		Sources []struct {
			ID     string `json:"id"`
			Events []struct {
				Kind          string `json:"kind"`
				ActivityTypes []struct {
					Value string `json:"value"`
				} `json:"activity_types"`
			} `json:"events"`
		} `json:"sources"`
		Actions []struct {
			ID     string `json:"id"`
			Label  string `json:"label"`
			Schema struct {
				Fields []json.RawMessage `json:"fields"`
			} `json:"schema"`
		} `json:"actions"`
		Operators []struct {
			Op     string `json:"op"`
			Family string `json:"family"`
			Label  string `json:"label"`
			Value  string `json:"value"`
			Field  bool   `json:"field"`
		} `json:"operators"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}

	if len(resp.Sources) != 1 || resp.Sources[0].ID != "github.poll" {
		t.Fatalf("sources = %+v, want the one fake github.poll source", resp.Sources)
	}
	kinds := map[string]bool{}
	for _, ev := range resp.Sources[0].Events {
		kinds[ev.Kind] = true
	}
	if !kinds["pull_request"] || !kinds["check_suite"] {
		t.Fatalf("event kinds = %v, want pull_request and check_suite", kinds)
	}

	if len(resp.Actions) != 1 || resp.Actions[0].ID != "picky" || resp.Actions[0].Label != "Picky" {
		t.Fatalf("actions = %+v, want the one registered picky action", resp.Actions)
	}

	ops := map[string]bool{}
	for _, o := range resp.Operators {
		ops[o.Op] = true
		if o.Family == "" || o.Label == "" || o.Value == "" {
			t.Errorf("operator %q missing family/label/value: %+v", o.Op, o)
		}
	}
	for _, want := range []string{
		"text.contains", "number.lt", "enum.in", "bool.is", "set.includes", "actor.is_agent",
	} {
		if !ops[want] {
			t.Errorf("operator table is missing %q", want)
		}
	}

	// The actor operators store no field — the editor's "author is an agent" row.
	for _, o := range resp.Operators {
		if o.Family == "actor" && o.Field {
			t.Errorf("actor operator %q claims to need a field", o.Op)
		}
	}
}

// TestTriggerListCarriesSparklineAndSummaries pins the S29 list additions: health.recent
// (empty for a never-fired rule) and action_summaries from the registered action's Describe.
func TestTriggerListCarriesSparklineAndSummaries(t *testing.T) {
	e := newEnv(t)
	c := e.owner()
	e.project(c, "SPK")

	create := `{
		"name": "Picky rule",
		"event": "pull_request",
		"activity_types": ["opened"],
		"actions": [{"action_id": "picky", "params": {"agent": "Reviewer"}}]
	}`
	if status, body := e.doJSON(c, "POST", "/api/v1/projects/SPK/triggers", create); status != http.StatusCreated {
		t.Fatalf("create = %d: %s", status, body)
	}

	status, body := e.doJSON(c, "GET", "/api/v1/projects/SPK/triggers", "")
	if status != http.StatusOK {
		t.Fatalf("list = %d: %s", status, body)
	}
	var resp struct {
		Triggers []struct {
			Health struct {
				Counts      map[string]int64 `json:"counts"`
				LastFiredAt *string          `json:"last_fired_at"`
				Recent      []string         `json:"recent"`
			} `json:"health"`
			ActionSummaries []string `json:"action_summaries"`
		} `json:"triggers"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(resp.Triggers) != 1 {
		t.Fatalf("triggers = %d, want 1", len(resp.Triggers))
	}
	tr := resp.Triggers[0]
	if tr.Health.Recent == nil || len(tr.Health.Recent) != 0 {
		t.Errorf("recent = %v, want present-and-empty for a never-fired rule", tr.Health.Recent)
	}
	if tr.Health.LastFiredAt != nil {
		t.Errorf("last_fired_at = %v, want null", *tr.Health.LastFiredAt)
	}
	if len(tr.ActionSummaries) != 1 || tr.ActionSummaries[0] != "picky Reviewer" {
		t.Errorf("action_summaries = %v, want [picky Reviewer]", tr.ActionSummaries)
	}
}
