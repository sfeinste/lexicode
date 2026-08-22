package github

import (
	"bytes"
	"encoding/json"
	"flag"
	"os"
	"path/filepath"
	"testing"
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
	if len(c.Events) != 5 {
		t.Fatalf("catalog has %d event kinds, want 5", len(c.Events))
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
