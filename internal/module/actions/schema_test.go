// schema_test.go: Describe rendered for each action over sample params (the rule-card and
// backtest sentences), and the Schema() JSON pinned as a snapshot — the THEN form is
// generated from these, so an accidental shape change should fail a test, not a UI.
package actions_test

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/spruce/lexicode/internal/kernel/ports"
	actionsmod "github.com/spruce/lexicode/internal/module/actions"
)

// bare returns the five actions with no dependencies wired: Schema and Describe must work
// standalone (Describe falls back to raw IDs when the roster is unreachable).
func bare() map[string]ports.TriggerAction {
	out := map[string]ports.TriggerAction{}
	for _, a := range actionsmod.All(actionsmod.Deps{}) {
		out[a.ID()] = a
	}
	return out
}

func TestDescribeRendersRuleCardSentences(t *testing.T) {
	acts := bare()
	cases := []struct {
		action  string
		params  string
		want    string // "" with wantErr set means Describe must error
		wantErr string
	}{
		{action: "run_agent", params: `{"agent_name":"Reviewer"}`, want: "run agent Reviewer"},
		{action: "run_agent", params: `{"agent_name":"Reviewer","prompt_override":"look at {{pr.number}}"}`,
			want: "run agent Reviewer with a prompt override"},
		{action: "run_agent", params: `{"agent_name":"Dev","prompt":""}`, want: "run agent Dev"}, // S15 shape
		{action: "run_agent", params: `{}`, wantErr: "an agent is required"},
		{action: "run_agent", params: `{"agent":"typo"}`, wantErr: "unknown field"},

		{action: "create_ticket", params: `{"title":"CI failed on PR {{pr.number}}"}`,
			want: `file a ticket into triage: "CI failed on PR {{pr.number}}"`},
		{action: "create_ticket", params: `{"title":"  "}`, wantErr: "a title is required"},

		{action: "move_ticket", params: `{"category":"review"}`, want: "move the ticket to a review column"},
		{action: "move_ticket", params: `{"category":"done","ticket":"{{ticket.key}}"}`,
			want: "move {{ticket.key}} to a done column"},
		{action: "move_ticket", params: `{}`, wantErr: "a destination category is required"},
		{action: "move_ticket", params: `{"category":"In Review"}`, wantErr: "not a column category"},

		{action: "post_comment", params: `{"agent_name":"Reviewer","body":"Thanks!"}`,
			want: "comment on the pull request as Reviewer"},
		{action: "post_comment", params: `{"body":"Thanks!"}`, wantErr: "an acting agent is required"},
		{action: "post_comment", params: `{"agent_name":"Reviewer"}`, wantErr: "a comment body is required"},

		{action: "notify", params: `{"message":"PR {{pr.number}} merged"}`,
			want: `notify the delegating human: "PR {{pr.number}} merged"`},
		{action: "notify", params: `{}`, wantErr: "a message is required"},
	}
	for _, tc := range cases {
		a := acts[tc.action]
		if a == nil {
			t.Fatalf("no action %q", tc.action)
		}
		got, err := a.Describe(json.RawMessage(tc.params))
		if tc.wantErr != "" {
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("%s %s: err = %v, want %q", tc.action, tc.params, err, tc.wantErr)
			}
			continue
		}
		if err != nil {
			t.Fatalf("%s %s: %v", tc.action, tc.params, err)
		}
		if got != tc.want {
			t.Fatalf("%s %s:\n got %q\nwant %q", tc.action, tc.params, got, tc.want)
		}
	}
}

// TestSchemaSnapshot pins each action's Schema() JSON. A deliberate schema change updates the
// snapshot here AND the S29 editor's expectations; an accidental one fails loudly.
func TestSchemaSnapshot(t *testing.T) {
	want := map[string]string{
		"run_agent": `{"fields":[` +
			`{"key":"agent_id","label":"Agent","type":"agent","required":true,"help":"The agent to run. Loop protection counts and suppresses on this agent."},` +
			`{"key":"prompt_override","label":"Prompt override","type":"template","required":false,"help":"Optional extra task instruction; {{...}} fields interpolate from the event."}]}`,
		"create_ticket": `{"fields":[` +
			`{"key":"title","label":"Title","type":"template","required":true,"help":"{{...}} fields interpolate from the event, e.g. \"CI failed on PR {{pr.number}}\"."},` +
			`{"key":"description","label":"Description","type":"template","required":false},` +
			`{"key":"labels","label":"Labels","type":"list","required":false,"help":"Existing label names to attach; unknown names are skipped."}]}`,
		"move_ticket": `{"fields":[` +
			`{"key":"category","label":"To category","type":"category","required":true,"enum":["backlog","ready","running","review","done","canceled"],"help":"Categories survive column renames; the ticket lands in the project's first column of the category."},` +
			`{"key":"ticket","label":"Ticket","type":"template","required":false,"help":"Which ticket to move. Empty means the event's own ticket ({{ticket.key}})."}]}`,
		"post_comment": `{"fields":[` +
			`{"key":"agent_id","label":"As agent","type":"agent","required":true,"help":"The comment carries this agent's marker, so it can never re-trigger the rule. Needs the comment_prs permission."},` +
			`{"key":"body","label":"Comment","type":"template","required":true,"help":"{{...}} fields interpolate from the event."}]}`,
		"notify": `{"fields":[` +
			`{"key":"message","label":"Message","type":"template","required":true,"help":"Delivered to the delegating human: the causing run's requester, else the ticket's assignee, else the project owner."}]}`,
	}
	acts := bare()
	if len(acts) != len(want) {
		t.Fatalf("registered actions = %d, want %d", len(acts), len(want))
	}
	for id, a := range acts {
		raw, err := json.Marshal(a.Schema())
		if err != nil {
			t.Fatal(err)
		}
		if string(raw) != want[id] {
			t.Fatalf("%s schema snapshot mismatch:\n got %s\nwant %s", id, raw, want[id])
		}
	}
}

// TestLabels: the THEN dropdown names.
func TestLabels(t *testing.T) {
	want := map[string]string{
		"run_agent":     "Run an agent",
		"create_ticket": "File a ticket",
		"move_ticket":   "Move the ticket",
		"post_comment":  "Post a comment",
		"notify":        "Notify me",
	}
	for id, a := range bare() {
		if a.Label() != want[id] {
			t.Fatalf("%s label = %q, want %q", id, a.Label(), want[id])
		}
	}
}
