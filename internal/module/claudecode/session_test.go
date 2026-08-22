package claudecode

import (
	"context"
	"encoding/json"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/spruce/lexicode/internal/domain"
	"github.com/spruce/lexicode/internal/kernel/ports"
)

const initLine = `{"type":"system","subtype":"init","cwd":"/workspace","session_id":"s","tools":[],"model":"m"}` + "\n"

func toolUseLine(id, name, input string) string {
	return `{"type":"assistant","message":{"id":"msg_` + id + `","role":"assistant","content":[{"type":"tool_use","id":"` + id + `","name":"` + name + `","input":` + input + `}]}}` + "\n"
}

func toolResultLine(id, content string, isErr bool) string {
	e := "false"
	if isErr {
		e = "true"
	}
	return `{"type":"user","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"` + id + `","is_error":` + e + `,"content":` + content + `}]}}` + "\n"
}

const resultLine = `{"type":"result","subtype":"success","is_error":false,"num_turns":1,"result":"done","total_cost_usd":0.01,"usage":{"input_tokens":1,"output_tokens":2}}` + "\n"

// waitUntil polls cond for up to two seconds.
func waitUntil(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

// TestSteeringBetweenToolCalls is S20 acceptance: steering during an in-flight tool call is
// buffered and delivered after its tool_result; steering while no tool is in flight goes to
// stdin immediately.
func TestSteeringBetweenToolCalls(t *testing.T) {
	outR, outW := io.Pipe()
	stdin := &lockedBuffer{}
	sink := &recordSink{}
	h := Attach(ports.RunSpec{RunID: "run-steer"}, ports.Streams{
		Stdin:  stdin,
		Stdout: outR,
		Wait:   func() (int, error) { return 0, nil },
	}, sink, AttachOptions{})
	ctx := context.Background()

	// No tool in flight: delivered immediately.
	if err := h.Steer(ctx, "steer-idle"); err != nil {
		t.Fatalf("Steer: %v", err)
	}
	if !strings.Contains(stdin.String(), "steer-idle") {
		t.Fatalf("idle steering was not written immediately; stdin = %q", stdin.String())
	}

	// Put a Bash call in flight.
	mustWrite(t, outW, initLine+toolUseLine("tu1", "Bash", `{"command":"sleep 5"}`))
	waitUntil(t, "the action activity", func() bool { return sink.emissionCount() >= 2 })

	// Steering now must buffer, not write.
	if err := h.Steer(ctx, "steer-inflight"); err != nil {
		t.Fatalf("Steer: %v", err)
	}
	time.Sleep(20 * time.Millisecond) // give a buggy immediate write the chance to show up
	if strings.Contains(stdin.String(), "steer-inflight") {
		t.Fatal("steering was written while a tool call was in flight")
	}

	// The tool_result opens the gap: the buffered message must be delivered.
	mustWrite(t, outW, toolResultLine("tu1", `"ok"`, false))
	waitUntil(t, "buffered steering delivery", func() bool {
		return strings.Contains(stdin.String(), "steer-inflight")
	})

	// And it was delivered in the CLI's user-message shape.
	last := stdin.String()
	if !strings.Contains(last, `"type":"user"`) || !strings.Contains(last, `"type":"text"`) {
		t.Errorf("steering not in stream-json user shape: %q", last)
	}

	mustWrite(t, outW, resultLine)
	_ = outW.Close()
	if _, err := h.Wait(ctx); err != nil {
		t.Fatalf("Wait: %v", err)
	}

	// After the session ends, steering is refused.
	if err := h.Steer(ctx, "too late"); err == nil {
		t.Fatal("Steer after end = nil error, want ErrSessionEnded")
	}
}

// TestBashFailureSetsOKZero is S20 acceptance: a failing Bash merges ok=false onto the action
// activity, and an immediate identical retry carries attempt 2.
func TestBashFailureSetsOKZero(t *testing.T) {
	fixture := initLine +
		toolUseLine("tu1", "Bash", `{"command":"npm test"}`) +
		toolResultLine("tu1", `[{"type":"text","text":"boom\nExit code 2"}]`, true) +
		toolUseLine("tu2", "Bash", `{"command":"npm test"}`) +
		toolResultLine("tu2", `"all green"`, false) +
		resultLine

	sink := runFixture(t, fixture)
	var bashes []domain.Activity
	for _, a := range sink.final() {
		if a.ToolName == "Bash" {
			bashes = append(bashes, a)
		}
	}
	if len(bashes) != 2 {
		t.Fatalf("bash activities = %d, want 2", len(bashes))
	}

	fail := bashes[0]
	if fail.OK == nil || *fail.OK {
		t.Errorf("failed bash ok = %v, want false", fail.OK)
	}
	var p struct {
		Exit   int    `json:"exit"`
		Stderr string `json:"stderr"`
	}
	if err := json.Unmarshal(fail.Payload, &p); err != nil {
		t.Fatal(err)
	}
	if p.Exit != 2 || !strings.Contains(p.Stderr, "boom") {
		t.Errorf("failed bash payload = %s", fail.Payload)
	}

	retry := bashes[1]
	if retry.Attempt != 2 {
		t.Errorf("retry attempt = %d, want 2", retry.Attempt)
	}
	if retry.OK == nil || !*retry.OK {
		t.Errorf("retry ok = %v, want true", retry.OK)
	}
}

// TestMalformedLineDoesNotKillTheRun is S20 acceptance: a malformed NDJSON line becomes a
// level-2 system activity and the stream continues to a clean completion.
func TestMalformedLineDoesNotKillTheRun(t *testing.T) {
	fixture := initLine +
		"this is not json at all\n" +
		toolUseLine("tu1", "Read", `{"file_path":"/workspace/a.txt"}`) +
		toolResultLine("tu1", `"hello"`, false) +
		resultLine

	sink := runFixture(t, fixture)
	acts := sink.final()

	var malformed *domain.Activity
	for i, a := range acts {
		if a.Type == domain.ActivitySystem && a.Title == "unparsed runtime output" {
			malformed = &acts[i]
		}
	}
	if malformed == nil {
		t.Fatalf("no malformed-line activity; titles: %v", titles(acts))
	}
	if malformed.Level != 2 {
		t.Errorf("malformed activity level = %d, want 2", malformed.Level)
	}

	// The stream continued: the Read after the bad line completed, and the run finished.
	read := acts[len(acts)-2]
	if read.ToolName != "Read" || read.OK == nil || !*read.OK {
		t.Errorf("post-malformed Read = %+v", read)
	}
	if last := acts[len(acts)-1]; last.Type != domain.ActivityResponse {
		t.Errorf("last activity = %s, want response", last.Type)
	}
}

// TestStderrBecomesSystemActivities: every stderr line is a level-2 system activity.
func TestStderrBecomesSystemActivities(t *testing.T) {
	sink := &recordSink{}
	h := Attach(ports.RunSpec{RunID: "run-stderr"}, ports.Streams{
		Stdin:  &lockedBuffer{},
		Stdout: strings.NewReader(initLine + resultLine),
		Stderr: strings.NewReader("warning: something\nanother line\n"),
		Wait:   func() (int, error) { return 0, nil },
	}, sink, AttachOptions{})
	if _, err := h.Wait(context.Background()); err != nil {
		t.Fatalf("Wait: %v", err)
	}
	count := 0
	for _, a := range sink.final() {
		if a.Type == domain.ActivitySystem && a.Level == 2 && strings.Contains(string(a.Payload), "stderr") {
			count++
		}
	}
	if count != 2 {
		t.Errorf("stderr system activities = %d, want 2", count)
	}
}

// TestResumeFromOffsets: with ResumeFrom set, reported offsets continue from it.
func TestResumeFromOffsets(t *testing.T) {
	fixture := initLine + resultLine
	sink := &recordSink{}
	h := Attach(ports.RunSpec{RunID: "run-resume", ResumeFrom: 500}, ports.Streams{
		Stdin:  &lockedBuffer{},
		Stdout: strings.NewReader(fixture),
		Wait:   func() (int, error) { return 0, nil },
	}, sink, AttachOptions{})
	if _, err := h.Wait(context.Background()); err != nil {
		t.Fatalf("Wait: %v", err)
	}
	if got, want := sink.lastOffset(), int64(500+len(fixture)); got != want {
		t.Errorf("last offset = %d, want %d", got, want)
	}
}

// TestRespondUnrouted: before S21 wires the MCP server, Respond fails with the named error.
func TestRespondUnrouted(t *testing.T) {
	sink := &recordSink{}
	h := Attach(ports.RunSpec{RunID: "run-respond"}, ports.Streams{
		Stdin:  &lockedBuffer{},
		Stdout: strings.NewReader(resultLine),
		Wait:   func() (int, error) { return 0, nil },
	}, sink, AttachOptions{})
	if err := h.Respond(context.Background(), "elic-1", ports.Response{Text: "answer"}); err != ErrRespondUnrouted {
		t.Errorf("Respond = %v, want ErrRespondUnrouted", err)
	}
	if _, err := h.Wait(context.Background()); err != nil {
		t.Fatalf("Wait: %v", err)
	}
}

func runFixture(t *testing.T, fixture string) *recordSink {
	t.Helper()
	sink := &recordSink{}
	h := Attach(ports.RunSpec{RunID: "run-test"}, ports.Streams{
		Stdin:  &lockedBuffer{},
		Stdout: strings.NewReader(fixture),
		Wait:   func() (int, error) { return 0, nil },
	}, sink, AttachOptions{})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := h.Wait(ctx); err != nil {
		t.Fatalf("Wait: %v", err)
	}
	return sink
}

func mustWrite(t *testing.T, w io.Writer, s string) {
	t.Helper()
	if _, err := io.WriteString(w, s); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
}
