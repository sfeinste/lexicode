package mcp_test

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/spruce/lexicode/internal/domain"
)

// The regression these tests stand on: ask_human died after about sixty seconds no matter
// what ceiling the server was prepared to wait. Two causes, both client-side. Claude Code
// applies a 60-second per-request timer to an HTTP MCP server unless MCP_TOOL_TIMEOUT raises
// it (S19's container env now does — see runs.mcpToolTimeout), and it aborts any call whose
// server has sent "no response and no progress notification" for the idle window. This file
// covers the second half: the server keeps saying it is alive, on the transport the spec
// provides for it, for as long as the question is open — and stops the moment it is answered.

// sseEvent is one decoded `data:` payload from an SSE stream, kept as raw JSON so a test can
// assert on the exact wire shape rather than on a Go type's opinion of it.
type sseEvent struct {
	raw    json.RawMessage
	Method string          `json:"method"`
	ID     json.RawMessage `json:"id"`
	Params struct {
		ProgressToken json.RawMessage `json:"progressToken"`
		Progress      float64         `json:"progress"`
		Total         *float64        `json:"total"`
		Message       string          `json:"message"`
	} `json:"params"`
	Result map[string]any `json:"result"`
}

// callToolSSE opens a tools/call as the streamable-HTTP transport's SSE variant and returns
// a channel of decoded events plus the response, closed when the stream ends.
func callToolSSE(t *testing.T, url, tool string, args any, progressToken any) <-chan sseEvent {
	t.Helper()
	params := map[string]any{"name": tool, "arguments": args}
	if progressToken != nil {
		params["_meta"] = map[string]any{"progressToken": progressToken}
	}
	body, err := json.Marshal(map[string]any{
		"jsonrpc": "2.0", "id": 7, "method": "tools/call", "params": params,
	})
	if err != nil {
		t.Fatal(err)
	}
	req, err := http.NewRequestWithContext(context.Background(), http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	// What the spec requires of an MCP client: "include an Accept header, listing both
	// application/json and text/event-stream".
	req.Header.Set("Accept", "application/json, text/event-stream")

	resp, err := http.DefaultClient.Do(req) //nolint:bodyclose // closed by the reader goroutine
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusOK {
		_ = resp.Body.Close()
		t.Fatalf("tools/call = HTTP %d", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "text/event-stream") {
		// Not a stream: hand the single JSON body back as one event so callers can assert
		// on the no-token path too.
		defer func() { _ = resp.Body.Close() }()
		raw, _ := io.ReadAll(resp.Body)
		ch := make(chan sseEvent, 1)
		ch <- decodeEvent(t, raw)
		close(ch)
		return ch
	}

	events := make(chan sseEvent, 64)
	go func() {
		defer close(events)
		defer func() { _ = resp.Body.Close() }()
		sc := bufio.NewScanner(resp.Body)
		for sc.Scan() {
			line := sc.Text()
			data, ok := strings.CutPrefix(line, "data: ")
			if !ok {
				continue
			}
			events <- decodeEvent(t, []byte(data))
		}
	}()
	return events
}

func decodeEvent(t *testing.T, raw []byte) sseEvent {
	t.Helper()
	var e sseEvent
	if err := json.Unmarshal(raw, &e); err != nil {
		t.Fatalf("event is not JSON: %v\n%s", err, raw)
	}
	e.raw = append(json.RawMessage(nil), raw...)
	return e
}

// TestProgressNotificationsWhilePendingAndStopWhenAnswered is the acceptance for the
// liveness half of the fix: while an elicitation is pending the server emits
// notifications/progress on the call's own stream, and the response is the last message on
// it. Timescale is compressed (Options.ProgressInterval), so what is exercised is the
// mechanism — the handshake, the shape, the cadence, the stop — not a real sixty seconds.
func TestProgressNotificationsWhilePendingAndStopWhenAnswered(t *testing.T) {
	e := newEnvWith(t, time.Minute, 25*time.Millisecond)
	f := e.fixtures(domain.AutonomyApproveEach, domain.AgentPermissions{ReadFiles: true})

	events := callToolSSE(t, e.srv.URL+"/mcp/"+f.token, "ask_human", askArgs(t), "tok-abc")

	// Let a few heartbeats accumulate before anyone answers — the whole point is that the
	// call is still alive long after a silent one would have been abandoned.
	var notes []sseEvent
	deadline := time.After(10 * time.Second)
	for len(notes) < 3 {
		select {
		case ev, ok := <-events:
			if !ok {
				t.Fatalf("stream closed after %d notifications, before any answer", len(notes))
			}
			if ev.Method != "notifications/progress" {
				t.Fatalf("expected a progress notification, got %s", ev.raw)
			}
			notes = append(notes, ev)
		case <-deadline:
			t.Fatalf("only %d progress notifications arrived", len(notes))
		}
	}

	// The elicitation is still pending — nothing timed the call out.
	el := e.waitPending(f.run.ID)

	for i, n := range notes {
		if string(n.Params.ProgressToken) != `"tok-abc"` {
			t.Errorf("notification %d token = %s, want \"tok-abc\"", i, n.Params.ProgressToken)
		}
		if n.Params.Total != nil {
			t.Errorf("notification %d carries a total (%v); the wait has no known length",
				i, *n.Params.Total)
		}
		if !strings.Contains(n.Params.Message, "waiting for a human") {
			t.Errorf("notification %d message = %q, want it to name the wait", i, n.Params.Message)
		}
		if len(n.ID) != 0 {
			t.Errorf("notification %d carries an id (%s); a JSON-RPC notification must not", i, n.ID)
		}
		// "The progress value MUST increase with each notification."
		if i > 0 && n.Params.Progress <= notes[i-1].Params.Progress {
			t.Errorf("progress did not increase: %v then %v",
				notes[i-1].Params.Progress, n.Params.Progress)
		}
	}

	status, _ := e.doJSON(e.owner, "POST", "/api/v1/elicitations/"+el.ID+"/respond",
		`{"action":"answer","answers":{"Which response format should the endpoint use?":["JSON"]}}`)
	if status != http.StatusOK {
		t.Fatalf("respond = %d", status)
	}

	// The response arrives on the same stream, and is the last thing on it.
	var response *sseEvent
	for ev := range events {
		if ev.Method == "notifications/progress" {
			if response != nil {
				t.Fatalf("a progress notification followed the response: %s", ev.raw)
			}
			continue
		}
		if response != nil {
			t.Fatalf("a second message followed the response: %s", ev.raw)
		}
		e := ev
		response = &e
	}
	if response == nil {
		t.Fatal("the stream closed without a JSON-RPC response")
	}
	if string(response.ID) != "7" {
		t.Errorf("response id = %s, want 7", response.ID)
	}
	content, _ := response.Result["content"].([]any)
	if len(content) == 0 {
		t.Fatalf("response carries no content: %s", response.raw)
	}
	text, _ := content[0].(map[string]any)["text"].(string)
	if !strings.Contains(text, "JSON") {
		t.Errorf("tool result did not carry the answer: %s", text)
	}
}

// TestNoProgressTokenNoNotifications: the spec makes progress opt-in — "progress
// notifications MUST only reference tokens that were provided in an active request" — so a
// client that did not ask gets the plain JSON body it always got.
func TestNoProgressTokenNoNotifications(t *testing.T) {
	e := newEnvWith(t, time.Minute, 10*time.Millisecond)
	f := e.fixtures(domain.AutonomyApproveEach, domain.AgentPermissions{ReadFiles: true})

	events := callToolSSE(t, e.srv.URL+"/mcp/"+f.token, "set_step",
		map[string]any{"step": "no token, no stream"}, nil)

	var got []sseEvent
	for ev := range events {
		got = append(got, ev)
	}
	if len(got) != 1 {
		t.Fatalf("messages = %d, want exactly the one JSON response", len(got))
	}
	if got[0].Method != "" {
		t.Errorf("got a notification without asking for one: %s", got[0].raw)
	}
}

// TestProgressStreamSurvivesTheClientsSixtySecondCutoff proves the server end of the fix at a
// compressed timescale: a call held open for many multiples of the progress interval keeps
// receiving liveness signals, so an idle-timeout client has no reason to abandon it. The
// scale factor is what the real world supplies — 20-second notifications inside a five-minute
// idle window — and the ratio here is the same.
func TestProgressStreamSurvivesTheClientsSixtySecondCutoff(t *testing.T) {
	const interval = 20 * time.Millisecond
	// The stand-in for the client's idle timeout: 15 intervals of silence would kill the
	// call, exactly as five minutes of silence kills a real one.
	const idleWindow = 15 * interval

	e := newEnvWith(t, time.Minute, interval)
	f := e.fixtures(domain.AutonomyApproveEach, domain.AgentPermissions{ReadFiles: true})
	events := callToolSSE(t, e.srv.URL+"/mcp/"+f.token, "ask_human", askArgs(t), 42)

	el := e.waitPending(f.run.ID)

	// Hold the question open for 100 intervals, asserting the gap between notifications
	// never reaches the idle window.
	last := time.Now()
	for i := 0; i < 100; i++ {
		select {
		case ev, ok := <-events:
			if !ok {
				t.Fatalf("the stream closed after %d notifications; the call was abandoned", i)
			}
			if ev.Method != "notifications/progress" {
				t.Fatalf("unexpected message while pending: %s", ev.raw)
			}
			if gap := time.Since(last); gap > idleWindow {
				t.Fatalf("notification %d arrived %s after the previous one; an idle "+
					"client would already have given up", i, gap)
			}
			last = time.Now()
		case <-time.After(idleWindow):
			t.Fatalf("no notification for %s at heartbeat %d", idleWindow, i)
		}
	}

	// Still pending, still answerable, long after silence would have killed it.
	after, err := e.st.Elicitations().ByID(context.Background(), el.ID)
	if err != nil {
		t.Fatal(err)
	}
	if after.State != domain.ElicitationPending {
		t.Fatalf("elicitation state = %s, want pending", after.State)
	}

	status, _ := e.doJSON(e.owner, "POST", "/api/v1/elicitations/"+el.ID+"/respond",
		`{"action":"answer","answers":{"Which response format should the endpoint use?":["JSON"]}}`)
	if status != http.StatusOK {
		t.Fatalf("respond = %d", status)
	}
	for ev := range events {
		if ev.Method == "" {
			return // the response landed; the agent resumes where it asked
		}
	}
	t.Fatal("the stream closed without delivering the answer")
}

// TestProgressTokenEchoedVerbatim: the spec allows a string or an integer, and the client
// matches notifications by the token it sent, so whatever it sent must come back unchanged.
func TestProgressTokenEchoedVerbatim(t *testing.T) {
	for _, tok := range []any{"a-string-token", 1234} {
		t.Run(fmt.Sprintf("%v", tok), func(t *testing.T) {
			e := newEnvWith(t, time.Minute, 10*time.Millisecond)
			f := e.fixtures(domain.AutonomyApproveEach, domain.AgentPermissions{ReadFiles: true})
			events := callToolSSE(t, e.srv.URL+"/mcp/"+f.token, "ask_human", askArgs(t), tok)

			want, err := json.Marshal(tok)
			if err != nil {
				t.Fatal(err)
			}
			select {
			case ev := <-events:
				if string(ev.Params.ProgressToken) != string(want) {
					t.Fatalf("token = %s, want %s", ev.Params.ProgressToken, want)
				}
			case <-time.After(5 * time.Second):
				t.Fatal("no progress notification")
			}

			// Release the blocked call so the server's waiter does not outlive the test.
			el := e.waitPending(f.run.ID)
			e.doJSON(e.owner, "POST", "/api/v1/elicitations/"+el.ID+"/respond",
				`{"action":"answer","answers":{"Which response format should the endpoint use?":["JSON"]}}`)
		})
	}
}
