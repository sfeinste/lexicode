package triggers

import (
	"encoding/json"
	"testing"
)

// samplePayload is a contracts §4-shaped payload the matrix evaluates against. The nil rows
// address paths that are absent from it.
func samplePayload() map[string]any {
	return parsePayload(json.RawMessage(`{
		"pr": {
			"number": 219, "title": "Add idempotency keys", "author": "dev-agent",
			"author_kind": "agent", "branch": "dev/PAY-14", "base": "main",
			"draft": false, "merged": false, "state": "open",
			"additions": 142, "deletions": 18, "files_changed": 7,
			"labels": ["payments", "agent"], "body": "", "url": "https://x/pr/219"
		},
		"check": {"conclusion": "failure", "name": "CI"},
		"empty_set": [],
		"repo": {"owner": "acme", "name": "payments", "default_branch": "main"},
		"actor": {"kind": "agent", "login": "dev-agent[bot]", "agent": "dev"}
	}`))
}

// TestOperatorMatrix is the full contracts §4.1 matrix: every operator, a true case, a false
// case, and its defined nil behaviour (an unknown path) — false for everything except
// text.is_empty (contracts §8).
func TestOperatorMatrix(t *testing.T) {
	cases := []struct {
		name  string
		field string
		op    string
		value string // JSON
		want  bool
	}{
		// ---- text ----
		{"text.is true", "pr.base", "text.is", `"main"`, true},
		{"text.is false", "pr.base", "text.is", `"dev"`, false},
		{"text.is nil", "pr.missing", "text.is", `"main"`, false},
		{"text.is_not true", "pr.base", "text.is_not", `"dev"`, true},
		{"text.is_not false", "pr.base", "text.is_not", `"main"`, false},
		{"text.is_not nil is false, not true", "pr.missing", "text.is_not", `"dev"`, false},
		{"text.contains true", "pr.title", "text.contains", `"idempotency"`, true},
		{"text.contains false", "pr.title", "text.contains", `"refund"`, false},
		{"text.contains nil", "pr.missing", "text.contains", `"x"`, false},
		{"text.not_contains true", "pr.title", "text.not_contains", `"refund"`, true},
		{"text.not_contains false", "pr.title", "text.not_contains", `"idempotency"`, false},
		{"text.not_contains nil is false, not true", "pr.missing", "text.not_contains", `"x"`, false},
		{"text.starts_with true", "pr.branch", "text.starts_with", `"dev/"`, true},
		{"text.starts_with false", "pr.branch", "text.starts_with", `"main"`, false},
		{"text.starts_with nil", "pr.missing", "text.starts_with", `"dev/"`, false},
		{"text.matches_glob true", "pr.branch", "text.matches_glob", `"dev/*"`, true},
		{"text.matches_glob false", "pr.branch", "text.matches_glob", `"release/*"`, false},
		{"text.matches_glob star does not cross a slash", "pr.url", "text.matches_glob", `"https:*"`, false},
		{"text.matches_glob malformed pattern", "pr.branch", "text.matches_glob", `"dev/["`, false},
		{"text.matches_glob nil", "pr.missing", "text.matches_glob", `"*"`, false},
		{"text.is_empty empty string", "pr.body", "text.is_empty", `null`, true},
		{"text.is_empty non-empty", "pr.title", "text.is_empty", `null`, false},
		{"text.is_empty nil is TRUE (the one exception)", "pr.missing", "text.is_empty", `null`, true},
		{"text.is_empty non-text is not empty", "pr.number", "text.is_empty", `null`, false},
		{"text.is on a number payload value", "pr.number", "text.is", `"219"`, false},

		// ---- number ----
		{"number.eq true", "pr.files_changed", "number.eq", `7`, true},
		{"number.eq false", "pr.files_changed", "number.eq", `8`, false},
		{"number.eq nil", "pr.missing", "number.eq", `7`, false},
		{"number.gt true", "pr.additions", "number.gt", `100`, true},
		{"number.gt false", "pr.additions", "number.gt", `142`, false},
		{"number.gt nil", "pr.missing", "number.gt", `0`, false},
		{"number.gte boundary", "pr.additions", "number.gte", `142`, true},
		{"number.gte false", "pr.additions", "number.gte", `143`, false},
		{"number.gte nil", "pr.missing", "number.gte", `0`, false},
		{"number.lt true", "pr.files_changed", "number.lt", `400`, true},
		{"number.lt false", "pr.files_changed", "number.lt", `7`, false},
		{"number.lt nil", "pr.missing", "number.lt", `400`, false},
		{"number.lte boundary", "pr.files_changed", "number.lte", `7`, true},
		{"number.lte false", "pr.files_changed", "number.lte", `6`, false},
		{"number.lte nil", "pr.missing", "number.lte", `400`, false},
		{"number.eq on a text payload value", "pr.title", "number.eq", `0`, false},

		// ---- enum ----
		{"enum.is true", "pr.state", "enum.is", `"open"`, true},
		{"enum.is false", "pr.state", "enum.is", `"closed"`, false},
		{"enum.is nil", "pr.missing", "enum.is", `"open"`, false},
		{"enum.is_not true", "check.conclusion", "enum.is_not", `"success"`, true},
		{"enum.is_not false", "check.conclusion", "enum.is_not", `"failure"`, false},
		{"enum.is_not nil is false, not true", "pr.missing", "enum.is_not", `"success"`, false},
		{"enum.in true", "check.conclusion", "enum.in", `["failure","timed_out"]`, true},
		{"enum.in false", "check.conclusion", "enum.in", `["success","neutral"]`, false},
		{"enum.in nil", "pr.missing", "enum.in", `["a"]`, false},

		// ---- bool ----
		{"bool.is true", "pr.draft", "bool.is", `false`, true},
		{"bool.is false", "pr.draft", "bool.is", `true`, false},
		{"bool.is nil", "pr.missing", "bool.is", `true`, false},
		{"bool.is nil vs false wanted", "pr.missing", "bool.is", `false`, false},

		// ---- set ----
		{"set.includes true", "pr.labels", "set.includes", `"payments"`, true},
		{"set.includes false", "pr.labels", "set.includes", `"docs"`, false},
		{"set.includes nil", "pr.missing", "set.includes", `"payments"`, false},
		{"set.excludes true", "pr.labels", "set.excludes", `"docs"`, true},
		{"set.excludes false", "pr.labels", "set.excludes", `"payments"`, false},
		{"set.excludes nil is false, not true", "pr.missing", "set.excludes", `"docs"`, false},
		{"set.is_empty present empty set", "empty_set", "set.is_empty", `null`, true},
		{"set.is_empty non-empty set", "pr.labels", "set.is_empty", `null`, false},
		{"set.is_empty nil is FALSE (unknown, not known-empty)", "pr.missing", "set.is_empty", `null`, false},

		// ---- actor ---- (field defaults to the actor sub-object when empty)
		{"actor.is_agent true", "", "actor.is_agent", `null`, true},
		{"actor.is_human false for an agent", "", "actor.is_human", `null`, false},
		{"actor.is by agent name", "", "actor.is", `"dev"`, true},
		{"actor.is by login", "", "actor.is", `"dev-agent[bot]"`, true},
		{"actor.is false", "", "actor.is", `"reviewer"`, false},
		{"actor.is_agent on actor.kind path", "actor.kind", "actor.is_agent", `null`, true},

		// ---- unknown operator is total: false, never a panic ----
		{"unknown operator", "pr.state", "text.regex", `".*"`, false},
	}
	payload := samplePayload()
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			raw := json.RawMessage(`{"field":` + jsonString(tc.field) + `,"op":` + jsonString(tc.op) +
				`,"value":` + tc.value + `}`)
			if got := evalConditions(raw, payload); got != tc.want {
				t.Fatalf("evalConditions(%s %s %s) = %v, want %v",
					tc.field, tc.op, tc.value, got, tc.want)
			}
		})
	}
}

// TestActorOperatorsOnNilActor: every actor operator is false when the payload has no actor.
func TestActorOperatorsOnNilActor(t *testing.T) {
	empty := map[string]any{}
	for _, op := range []string{"actor.is_agent", "actor.is_human", "actor.is"} {
		raw := json.RawMessage(`{"op":"` + op + `","value":"dev"}`)
		if evalConditions(raw, empty) {
			t.Fatalf("%s on a payload without an actor = true, want false", op)
		}
	}
}

// TestConditionTreeCombinators covers all/any nesting and the vacuous cases.
func TestConditionTreeCombinators(t *testing.T) {
	payload := samplePayload()
	cases := []struct {
		name string
		tree string
		want bool
	}{
		{"empty all is vacuously true (the schema default)", `{"all":[]}`, true},
		{"empty any is vacuously false", `{"any":[]}`, false},
		{"all of two true", `{"all":[
			{"op":"actor.is_agent"},
			{"field":"pr.files_changed","op":"number.lt","value":400}]}`, true},
		{"all short-circuits false", `{"all":[
			{"op":"actor.is_human"},
			{"field":"pr.files_changed","op":"number.lt","value":400}]}`, false},
		{"any rescues", `{"any":[
			{"op":"actor.is_human"},
			{"field":"pr.state","op":"enum.is","value":"open"}]}`, true},
		{"nested", `{"all":[
			{"op":"actor.is_agent"},
			{"any":[
				{"field":"pr.labels","op":"set.includes","value":"payments"},
				{"field":"pr.draft","op":"bool.is","value":true}]}]}`, true},
		{"malformed json is false, never a panic", `{"all":`, false},
		{"empty raw is true", ``, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := evalConditions(json.RawMessage(tc.tree), payload); got != tc.want {
				t.Fatalf("evalConditions(%s) = %v, want %v", tc.tree, got, tc.want)
			}
		})
	}
}

// TestOperatorMatrixIsComplete pins the evaluator to the full contracts §4.1 vocabulary: a
// missing operator here fails before an incomplete dropdown ships.
func TestOperatorMatrixIsComplete(t *testing.T) {
	want := []string{
		"text.is", "text.is_not", "text.contains", "text.not_contains", "text.starts_with",
		"text.matches_glob", "text.is_empty",
		"number.eq", "number.gt", "number.gte", "number.lt", "number.lte",
		"enum.is", "enum.is_not", "enum.in",
		"bool.is",
		"set.includes", "set.excludes", "set.is_empty",
		"actor.is_agent", "actor.is_human", "actor.is",
	}
	for _, op := range want {
		if _, ok := operators[op]; !ok {
			t.Errorf("operator %q from contracts §4.1 is not implemented", op)
		}
	}
	if len(operators) != len(want) {
		t.Errorf("evaluator has %d operators, contracts §4.1 has %d — the two must not drift",
			len(operators), len(want))
	}
}

func jsonString(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}
