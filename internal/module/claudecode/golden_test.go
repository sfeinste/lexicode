package claudecode

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/spruce/lexicode/internal/domain"
	"github.com/spruce/lexicode/internal/kernel/ports"
)

// -update regenerates testdata/session.golden.json from the current parser output.
var update = flag.Bool("update", false, "rewrite golden files")

// TestGoldenSession is the S20 acceptance test: a recorded stream-json session parses into
// exactly the expected activity list — every field, including titles, payloads, levels,
// grouping, ok/duration merges, and (via the deterministic stepping clock) timestamps and the
// timing gutter. The fixture covers init, a thought, Read, Edit with diff hunks, a failing
// Bash, TodoWrite, an unknown tool, a Grep, an MCP tool, a malformed line mid-stream, and the
// final result.
func TestGoldenSession(t *testing.T) {
	fixture, err := os.ReadFile("testdata/session.ndjson")
	if err != nil {
		t.Fatal(err)
	}

	sink := &recordSink{}
	clock := newStepClock(time.Date(2026, 1, 2, 15, 4, 5, 0, time.UTC), 100*time.Millisecond)
	st := ports.Streams{
		Stdin:  &lockedBuffer{},
		Stdout: bytes.NewReader(fixture),
		Wait:   func() (int, error) { return 0, nil },
	}
	h := Attach(ports.RunSpec{RunID: "run-golden"}, st, sink, AttachOptions{Now: clock.Now})

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	res, err := h.Wait(ctx)
	if err != nil {
		t.Fatalf("Wait: %v", err)
	}

	got := sink.final()
	gotJSON, err := json.MarshalIndent(got, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	gotJSON = append(gotJSON, '\n')

	const goldenPath = "testdata/session.golden.json"
	if *update {
		if err := os.WriteFile(goldenPath, gotJSON, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	wantJSON, err := os.ReadFile(goldenPath)
	if err != nil {
		t.Fatalf("read golden (run with -update to generate): %v", err)
	}

	var want []domain.Activity
	if err := json.Unmarshal(wantJSON, &want); err != nil {
		t.Fatalf("golden file does not parse: %v", err)
	}

	// Field-by-field comparison, reported per activity so a mismatch names the exact step.
	if len(got) != len(want) {
		t.Fatalf("activity count = %d, golden has %d\ngot:\n%s", len(got), len(want), gotJSON)
	}
	for i := range want {
		g, _ := json.Marshal(got[i])
		w, _ := json.Marshal(want[i])
		if !bytes.Equal(g, w) {
			t.Errorf("activity[%d] (seq %d) mismatch:\n got: %s\nwant: %s", i, want[i].Seq, g, w)
		}
	}

	// The final result and rollups.
	if res.IsError || res.Stopped {
		t.Errorf("result = %+v, want a clean success", res)
	}
	if want := "Fixed the retry handling in src/api/charge.ts and updated both failing tests."; res.ResultText != want {
		t.Errorf("ResultText = %q, want %q", res.ResultText, want)
	}
	if res.NumTurns != 9 {
		t.Errorf("NumTurns = %d, want 9", res.NumTurns)
	}
	wantUsage := domain.UsageDelta{
		TokensIn: 60, TokensOut: 900, TokensCacheRead: 14000, TokensCacheWrite: 400,
		CostCents: 21, // 0.2137 USD
	}
	if res.Usage != wantUsage {
		t.Errorf("final usage = %+v, want %+v", res.Usage, wantUsage)
	}
	if sink.usageTotal() != wantUsage {
		t.Errorf("sink usage rollup = %+v, want %+v", sink.usageTotal(), wantUsage)
	}

	// The whole stream was consumed and its byte offset reported for reattach.
	if sink.lastOffset() != int64(len(fixture)) {
		t.Errorf("last offset = %d, want %d (the full fixture)", sink.lastOffset(), len(fixture))
	}

	// Spot-check the S20 acceptance essentials directly, so this test fails readably even if
	// the golden file were regenerated with a bug.
	byTitle := map[string]domain.Activity{}
	for _, a := range got {
		byTitle[a.Title] = a
	}
	bash, ok := byTitle["$ npm test"]
	if !ok {
		t.Fatalf("no Bash action activity; titles: %v", titles(got))
	}
	if bash.OK == nil || *bash.OK {
		t.Errorf("failing Bash: ok = %v, want false", bash.OK)
	}
	var bashPayload struct {
		Exit   int      `json:"exit"`
		Argv   []string `json:"argv"`
		Stderr string   `json:"stderr"`
	}
	if err := json.Unmarshal(bash.Payload, &bashPayload); err != nil {
		t.Fatal(err)
	}
	if bashPayload.Exit != 1 || len(bashPayload.Argv) != 3 || bashPayload.Stderr == "" {
		t.Errorf("bash payload = %s", bash.Payload)
	}

	edit, ok := byTitle["Edit src/api/charge.ts"]
	if !ok {
		t.Fatalf("no Edit action activity; titles: %v", titles(got))
	}
	var editPayload struct {
		Path  string     `json:"path"`
		Hunks []diffHunk `json:"hunks"`
	}
	if err := json.Unmarshal(edit.Payload, &editPayload); err != nil {
		t.Fatal(err)
	}
	if len(editPayload.Hunks) != 1 || len(editPayload.Hunks[0].Lines) != 4 {
		t.Errorf("edit hunks = %+v, want one hunk with 2 removed + 2 added lines", editPayload.Hunks)
	}

	malformedSeen := false
	for _, a := range got {
		if a.Type == domain.ActivitySystem && a.Level == 2 && a.Title == "unparsed runtime output" {
			malformedSeen = true
		}
	}
	if !malformedSeen {
		t.Error("the malformed fixture line did not produce a level-2 system activity")
	}
}

func titles(as []domain.Activity) []string {
	out := make([]string, len(as))
	for i, a := range as {
		out[i] = fmt.Sprintf("%d:%s", a.Seq, a.Title)
	}
	return out
}
