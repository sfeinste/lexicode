package bootstrap_test

import (
	"context"
	"encoding/json"
	"regexp"
	"strings"
	"testing"

	githubmod "github.com/spruce/lexicode/internal/module/github"
	"github.com/spruce/lexicode/internal/service/bootstrap"
)

// runAgentAction is the shape of the one action a suggested trigger carries.
type runAgentAction struct {
	ActionID string `json:"action_id"`
	Params   struct {
		AgentName string `json:"agent_name"`
		Prompt    string `json:"prompt"`
	} `json:"params"`
}

func triggerActions(t *testing.T, raw []byte) runAgentAction {
	t.Helper()
	var actions []runAgentAction
	if err := json.Unmarshal(raw, &actions); err != nil {
		t.Fatalf("the trigger's actions column is not valid JSON (%v): %s", err, raw)
	}
	if len(actions) != 1 || actions[0].ActionID != "run_agent" {
		t.Fatalf("actions = %+v, want exactly one run_agent", actions)
	}
	return actions[0]
}

// TestSuggestedTriggersCarryARealPrompt is the regression for the reviewer that was handed no
// task at all. Bootstrap used to write `"prompt":""` into both suggested rules; a
// trigger-spawned run has no ticket, so an empty override left the run's prompt with no
// "# Task" section whatsoever — and the agent did something competent and unrelated.
func TestSuggestedTriggersCarryARealPrompt(t *testing.T) {
	e := newEnv(t, newFakeGitHub(t, fixtureFiles(), fixtureIssues(2)))
	c := e.owner()
	e.connect(c)

	code, res := e.doJSON(c, "POST", "/api/v1/projects/PAY/bootstrap/apply",
		`{"triggers": ["agent-pr-review", "changes-requested", "ci-failed-fix"]}`)
	if code != 200 {
		t.Fatalf("apply = %d: %v", code, res)
	}
	trs, err := e.st.Triggers().ForProject(context.Background(), e.projectID())
	if err != nil {
		t.Fatal(err)
	}
	if len(trs) != 3 {
		t.Fatalf("triggers = %d, want 3", len(trs))
	}

	byEvent := map[string]runAgentAction{}
	for _, tr := range trs {
		byEvent[tr.Event] = triggerActions(t, tr.Actions)
	}

	review, ok := byEvent["pull_request"]
	if !ok {
		t.Fatal("no pull_request rule was created")
	}
	if review.Params.AgentName != "Reviewer" {
		t.Errorf("agent_name = %q", review.Params.AgentName)
	}
	for _, want := range []string{"{{pr.number}}", "{{pr.branch}}", "submit_review"} {
		if !strings.Contains(review.Params.Prompt, want) {
			t.Errorf("the reviewer prompt does not mention %q:\n%s", want, review.Params.Prompt)
		}
	}

	address, ok := byEvent["agent_review"]
	if !ok {
		t.Fatal("no agent_review rule was created")
	}
	if address.Params.AgentName != "Dev" {
		t.Errorf("agent_name = %q", address.Params.AgentName)
	}
	for _, want := range []string{"{{pr.number}}", "{{pr.branch}}", "{{review.max_severity}}", "{{review.body}}"} {
		if !strings.Contains(address.Params.Prompt, want) {
			t.Errorf("the address-review prompt does not mention %q:\n%s", want, address.Params.Prompt)
		}
	}

	cifix, ok := byEvent["check_suite"]
	if !ok {
		t.Fatal("no check_suite rule was created")
	}
	if cifix.Params.AgentName != "Dev" {
		t.Errorf("agent_name = %q", cifix.Params.AgentName)
	}
	for _, want := range []string{"{{check.name}}", "{{pr.number}}"} {
		if !strings.Contains(cifix.Params.Prompt, want) {
			t.Errorf("the CI-fix prompt does not mention %q:\n%s", want, cifix.Params.Prompt)
		}
	}
}

var templatePath = regexp.MustCompile(`\{\{\s*([^}]+?)\s*\}\}`)

// TestSuggestedTriggerPromptsOnlyUseRealPayloadPaths is the drift guard. `{{...}}` is
// interpolation-only: an unknown path renders as the empty string with a warning on the
// firing row, so a typo like `{{pr.num}}` produces a prompt with a hole in it and nothing
// fails. Every path in the shipped defaults is checked against the field vocabulary the
// github.poll catalog declares for that event kind (contracts §4).
func TestSuggestedTriggerPromptsOnlyUseRealPayloadPaths(t *testing.T) {
	fields := map[string]map[string]bool{}
	for _, ev := range (&githubmod.Poller{}).Catalog().Events {
		set := map[string]bool{}
		for _, f := range ev.Fields {
			set[f.Path] = true
		}
		fields[ev.Kind] = set
	}

	for _, tc := range []struct{ kind, prompt string }{
		{"pull_request", bootstrap.ReviewerPrompt},
		{"agent_review", bootstrap.AddressReviewPrompt},
		{"check_suite", bootstrap.CIFixPrompt},
	} {
		known, ok := fields[tc.kind]
		if !ok {
			t.Fatalf("the github.poll catalog declares no %q event", tc.kind)
		}
		paths := templatePath.FindAllStringSubmatch(tc.prompt, -1)
		if len(paths) == 0 {
			t.Fatalf("%s: the prompt interpolates nothing", tc.kind)
		}
		for _, m := range paths {
			if !known[m[1]] {
				t.Errorf("%s: {{%s}} is not a field the catalog declares; it would render "+
					"as an empty string with a warning", tc.kind, m[1])
			}
		}
	}
}
