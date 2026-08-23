package github

import (
	"bytes"
	"encoding/json"
	"flag"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/spruce/lexicode/internal/kernel/ports"
)

// -update rewrites the golden catalog snapshot. The snapshot is the compatibility surface the
// trigger editor is generated from (contracts §2.1); a diff here is a deliberate catalog
// change, reviewed as one.
var updateGolden = flag.Bool("update", false, "rewrite testdata golden files")

// TestCatalogSnapshot pins the full github.poll descriptor set: event kinds, activity types,
// filters, payload fields with their operator types, and the SubjectKey guard templates.
func TestCatalogSnapshot(t *testing.T) {
	got, err := json.MarshalIndent(catalog(), "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	got = append(got, '\n')

	golden := filepath.Join("testdata", "catalog.json")
	if *updateGolden {
		if err := os.WriteFile(golden, got, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(golden)
	if err != nil {
		t.Fatalf("read golden (run `go test -run TestCatalogSnapshot -update` after a deliberate catalog change): %v", err)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("catalog drifted from the golden snapshot.\n--- got ---\n%s\n--- want ---\n%s", got, want)
	}
}

// TestCatalogShape asserts the structural facts the trigger editor and the S26 matcher rely
// on, independent of the snapshot bytes.
func TestCatalogShape(t *testing.T) {
	c := catalog()
	if len(c.Events) != 6 {
		t.Fatalf("catalog has %d event kinds, want 6", len(c.Events))
	}
	byKind := map[string][]string{}
	for _, d := range c.Events {
		if d.SubjectKey != "pr:{{pr.number}}" {
			t.Errorf("%s subject key = %q", d.Kind, d.SubjectKey)
		}
		if len(d.Fields) == 0 || len(d.Filters) == 0 {
			t.Errorf("%s has no fields or filters", d.Kind)
		}
		for _, a := range d.ActivityTypes {
			byKind[d.Kind] = append(byKind[d.Kind], a.Value)
		}
	}
	want := map[string][]string{
		"pull_request":                {"opened", "synchronize", "ready_for_review", "closed"},
		"pull_request_review":         {"submitted"},
		"agent_review":                {"submitted"},
		"pull_request_review_comment": {"created"},
		"issue_comment":               {"created"},
		"check_suite":                 {"completed"},
	}
	for kind, acts := range want {
		got := byKind[kind]
		if len(got) != len(acts) {
			t.Errorf("%s activity types = %v, want %v", kind, got, acts)
			continue
		}
		for i := range acts {
			if got[i] != acts[i] {
				t.Errorf("%s activity types = %v, want %v", kind, got, acts)
				break
			}
		}
	}
}

// TestAgentReviewFieldsAreOfferedToTheEditor pins the IF-row menu for the agent_review kind.
// The trigger editor is generated from this catalog and nothing else: a field that is not
// here cannot be selected in a dropdown, whatever the event's payload actually carries. The
// severity fields exist so a "changes requested" rule can key on what the reviewer found
// rather than on the state GitHub stored, so their paths, their operator families and — for
// the enums — their values are the contract.
func TestAgentReviewFieldsAreOfferedToTheEditor(t *testing.T) {
	var desc *ports.EventDescriptor
	for i, d := range catalog().Events {
		if d.Kind == kindAgentReview {
			desc = &catalog().Events[i]
		}
	}
	if desc == nil {
		t.Fatal("the catalog offers no agent_review event kind")
	}

	byPath := map[string]ports.PayloadField{}
	for _, f := range desc.Fields {
		byPath[f.Path] = f
	}
	want := []ports.PayloadField{
		{Path: "review.id", Type: "text"},
		{Path: "review.author", Type: "text"},
		{Path: "review.body", Type: "text"},
		{Path: "review.findings_count", Type: "number"},
		{Path: "review.severity_counts.blocker", Type: "number"},
		{Path: "review.severity_counts.major", Type: "number"},
		{Path: "review.severity_counts.minor", Type: "number"},
		{Path: "review.severity_counts.nit", Type: "number"},
		{Path: "review.state", Type: "enum", Enum: []string{"changes_requested", "commented"}},
		{Path: "review.intended_state", Type: "enum", Enum: []string{"changes_requested", "commented"}},
		{Path: "review.max_severity", Type: "enum",
			Enum: []string{"blocker", "major", "minor", "nit", "none"}},
		// The pull request the review is on: a rule addresses it exactly as it would on a
		// poller event, and the run the rule starts checks pr.branch out.
		{Path: "pr.number", Type: "number"},
		{Path: "pr.branch", Type: "text"},
		{Path: "actor.agent", Type: "text"},
	}
	for _, w := range want {
		got, ok := byPath[w.Path]
		if !ok {
			t.Errorf("%s is not offered to the editor", w.Path)
			continue
		}
		if got.Type != w.Type {
			t.Errorf("%s type = %q, want %q", w.Path, got.Type, w.Type)
		}
		if len(w.Enum) > 0 && !slices.Equal(got.Enum, w.Enum) {
			t.Errorf("%s enum = %v, want %v", w.Path, got.Enum, w.Enum)
		}
	}

	// An agent cannot approve (brief D6), so "approved" must not be selectable here — unlike
	// on pull_request_review, where a human's approval is a real event.
	if slices.Contains(byPath["review.state"].Enum, "approved") {
		t.Error("review.state offers `approved` on an event only an agent can produce (brief D6)")
	}
}
