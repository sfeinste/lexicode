package testkit_test

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/spruce/lexicode/internal/domain"
	"github.com/spruce/lexicode/internal/kernel/ports"
	"github.com/spruce/lexicode/internal/module/testkit"
)

const fixture = `{"type":"system","subtype":"init","cwd":"/workspace","session_id":"s","tools":[],"model":"m"}
{"type":"assistant","message":{"id":"m1","role":"assistant","content":[{"type":"text","text":"thinking"}],"usage":{"input_tokens":3,"output_tokens":7}}}
{"type":"assistant","message":{"id":"m1","role":"assistant","content":[{"type":"tool_use","id":"t1","name":"Read","input":{"file_path":"/workspace/a.txt"}}]}}
{"type":"user","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"t1","content":"hello"}]}}
{"type":"result","subtype":"success","is_error":false,"num_turns":2,"result":"done","total_cost_usd":0.02,"usage":{"input_tokens":3,"output_tokens":7}}
`

type sink struct {
	mu         sync.Mutex
	activities []domain.Activity
	usage      []domain.UsageDelta
}

func (s *sink) Activity(a domain.Activity) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.activities = append(s.activities, a)
}
func (s *sink) CurrentStep(string) {}
func (s *sink) Usage(u domain.UsageDelta) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.usage = append(s.usage, u)
}
func (s *sink) Elicit(domain.Elicitation) error { return nil }
func (s *sink) Output(domain.RunOutput)         {}
func (s *sink) Offset(int64)                    {}

type nullProvision struct{}

func (nullProvision) Step(string, ports.StepState, string) {}
func (nullProvision) Log(string)                           {}

// TestScriptedReplaysWithoutAnInstance: the scripted runtime alone — no sandbox at all —
// replays a session through the real parser.
func TestScriptedReplaysWithoutAnInstance(t *testing.T) {
	rt := &testkit.Scripted{Fixture: []byte(fixture)}
	s := &sink{}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	h, err := rt.Launch(ctx, ports.RunSpec{RunID: "r1"}, nil, s)
	if err != nil {
		t.Fatalf("Launch: %v", err)
	}
	res, err := h.Wait(ctx)
	if err != nil {
		t.Fatalf("Wait: %v", err)
	}
	if res.IsError || res.ResultText != "done" || res.NumTurns != 2 {
		t.Errorf("result = %+v", res)
	}
	if res.Usage.CostCents != 2 {
		t.Errorf("cost = %d cents, want 2", res.Usage.CostCents)
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	var types []domain.ActivityType
	for _, a := range s.activities {
		types = append(types, a.Type)
	}
	// init system, thought, action (emitted), action (result merge), response.
	want := []domain.ActivityType{
		domain.ActivitySystem, domain.ActivityThought, domain.ActivityAction,
		domain.ActivityAction, domain.ActivityResponse,
	}
	if len(types) != len(want) {
		t.Fatalf("activity types = %v, want %v", types, want)
	}
	for i := range want {
		if types[i] != want[i] {
			t.Fatalf("activity types = %v, want %v", types, want)
		}
	}
}

// TestScriptedThroughFakeSandbox: the pairing S22 will lean on — fake sandbox instance plus
// scripted runtime, running the real claudecode Launch path end to end.
func TestScriptedThroughFakeSandbox(t *testing.T) {
	sb := testkit.NewSandbox(testkit.Script{}) // script comes from the runtime below
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	inst, err := sb.Prepare(ctx, ports.SandboxSpec{RunID: "r2"}, nullProvision{})
	if err != nil {
		t.Fatalf("Prepare: %v", err)
	}

	rt := &testkit.Scripted{Fixture: []byte(fixture)}
	s := &sink{}
	h, err := rt.Launch(ctx, ports.RunSpec{RunID: "r2", Prompt: "the prompt", Model: "m"}, inst, s)
	if err != nil {
		t.Fatalf("Launch: %v", err)
	}
	res, err := h.Wait(ctx)
	if err != nil {
		t.Fatalf("Wait: %v", err)
	}
	if res.IsError || res.ResultText != "done" {
		t.Errorf("result = %+v", res)
	}

	// The prompt travelled through the real stdin path.
	fake := inst.(*testkit.Instance)
	if got := fake.StdinWrites(); !strings.Contains(got, "the prompt") {
		t.Errorf("prompt not on stdin: %q", got)
	}
}
