// progress.go is the liveness half of a blocking tool call: MCP progress notifications,
// streamed over the same POST that carries the call.
//
// Why it exists. ask_human blocks for as long as a human takes, and the MCP *client* — not
// this server — decides how long it is willing to wait. Claude Code aborts an HTTP server's
// tool call after 60 seconds per request unless MCP_TOOL_TIMEOUT says otherwise (S19's
// container env now sets it), and independently after an idle window with "no response and
// no progress notification" (five minutes for a network server). Progress notifications are
// what the protocol provides for exactly this case: a long call signalling that it is alive
// rather than hung.
//
// The shape is the spec's, not ours (MCP 2025-06-18, Basic/Utilities/Progress):
//
//   - The client opts in by putting a `progressToken` in the request's `params._meta`.
//     Without one we send nothing at all — "progress notifications MUST only reference
//     tokens that were provided in an active request".
//   - A notification is `{"jsonrpc":"2.0","method":"notifications/progress","params":
//     {"progressToken":…,"progress":N,"message":"…"}}`, with `progress` strictly increasing
//     and `total` omitted because the wait has no known length.
//   - They stop when the call settles — the response is the last message on the stream.
//
// Delivering them needs the streamable-HTTP transport's other answer to a POST: instead of
// one `application/json` body, the server MAY reply `text/event-stream` and "send JSON-RPC
// requests and notifications before sending the JSON-RPC response". So a tools/call that
// carries a progress token, from a client whose Accept header offers text/event-stream, is
// answered as an SSE stream: zero or more notifications, then the response, then close.
// Everything else keeps the plain-JSON path it always had.
package mcp

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"
)

// defaultProgressInterval is how often a blocked call reports liveness. It is well inside
// the five-minute idle window Claude Code applies to network MCP servers, and matches the
// cadence of the CLI's own tool heartbeats, so a run's transcript ticks at one rhythm.
const defaultProgressInterval = 20 * time.Second

// progressEvery is the configured notification cadence (Options.ProgressInterval overrides
// it; tests compress it to milliseconds).
func (s *Server) progressEvery() time.Duration {
	if s.progressInterval > 0 {
		return s.progressInterval
	}
	return defaultProgressInterval
}

// acceptsSSE reports whether the client offered text/event-stream. The spec requires an MCP
// client to list both content types, but a hand-rolled caller (the e2e stand-in agent, curl)
// may not — and answering SSE to a caller that asked for JSON would break it.
func acceptsSSE(r *http.Request) bool {
	for _, v := range r.Header.Values("Accept") {
		for _, part := range strings.Split(v, ",") {
			media, _, _ := strings.Cut(strings.TrimSpace(part), ";")
			switch strings.TrimSpace(media) {
			case "text/event-stream", "*/*":
				return true
			}
		}
	}
	return false
}

// progressTokenOf returns the client's `params._meta.progressToken` verbatim. It is echoed
// as raw JSON rather than decoded: the spec allows a string or an integer, and a token that
// comes back byte-identical cannot be mangled by a round trip through a Go type.
func progressTokenOf(params json.RawMessage) (json.RawMessage, bool) {
	var p struct {
		Meta struct {
			ProgressToken json.RawMessage `json:"progressToken"`
		} `json:"_meta"`
	}
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, false
	}
	tok := json.RawMessage(strings.TrimSpace(string(p.Meta.ProgressToken)))
	if len(tok) == 0 || string(tok) == "null" {
		return nil, false
	}
	// Only a string or an integer is a legal token; anything else is ignored rather than
	// echoed back into a notification the client cannot match.
	var asString string
	var asNumber float64
	if json.Unmarshal(tok, &asString) != nil && json.Unmarshal(tok, &asNumber) != nil {
		return nil, false
	}
	return tok, true
}

// toolNameOf reads the tool name out of tools/call params, for the notification's message.
func toolNameOf(params json.RawMessage) string {
	var p struct {
		Name string `json:"name"`
	}
	_ = json.Unmarshal(params, &p)
	return p.Name
}

// callToolStreaming answers one tools/call as an SSE stream: a progress notification every
// interval for as long as the call is outstanding, then the JSON-RPC response, then close.
// The tool runs in its own goroutine and this loop is the stream's only writer.
func (s *Server) callToolStreaming(w http.ResponseWriter, r *http.Request, runID string, req rpcRequest, token json.RawMessage) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		// No flushing means no streaming; a buffered "stream" would deliver every
		// notification at once, after the wait, which is worse than not sending them.
		result, rpcErr := s.callTool(r, runID, req.Params)
		writeRPC(w, rpcResponse{JSONRPC: "2.0", ID: req.ID, Result: result, Error: rpcErr})
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache, no-transform")
	w.Header().Set("Connection", "keep-alive")
	// The egress relay and any intermediary must not sit on these bytes; the whole point is
	// that they arrive while the call is still open.
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	flusher.Flush()

	type outcome struct {
		result any
		rpcErr *rpcError
	}
	done := make(chan outcome, 1)
	go func() {
		result, rpcErr := s.callTool(r, runID, req.Params)
		done <- outcome{result: result, rpcErr: rpcErr}
	}()

	tool := toolNameOf(req.Params)
	start := s.now()
	ticker := time.NewTicker(s.progressEvery())
	defer ticker.Stop()

	var sent int64
	for {
		select {
		case <-r.Context().Done():
			// The client hung up. The tool call unwinds on the same context; the
			// elicitation row stays pending so an answer is not lost (see await).
			return
		case <-ticker.C:
			sent++
			if !writeSSE(w, flusher, map[string]any{
				"jsonrpc": "2.0",
				"method":  "notifications/progress",
				"params": map[string]any{
					"progressToken": token,
					"progress":      sent,
					"message":       progressMessage(tool, s.now().Sub(start)),
				},
			}) {
				return
			}
		case out := <-done:
			writeSSE(w, flusher, rpcResponse{
				JSONRPC: "2.0", ID: req.ID, Result: out.result, Error: out.rpcErr,
			})
			return // "Progress notifications MUST stop after completion."
		}
	}
}

// progressMessage is the notification's human-readable line. The two blocking tools say what
// is actually being waited on — a human — because "still running" reads like a hung server
// and this is the opposite of one.
func progressMessage(tool string, elapsed time.Duration) string {
	d := elapsed.Round(time.Second)
	switch tool {
	case "ask_human":
		return fmt.Sprintf("waiting for a human to answer (%s elapsed)", d)
	case "request_approval":
		return fmt.Sprintf("waiting for a human decision (%s elapsed)", d)
	case "":
		return fmt.Sprintf("still working (%s elapsed)", d)
	}
	return fmt.Sprintf("%s is still working (%s elapsed)", tool, d)
}

// writeSSE frames one JSON-RPC message as an SSE event and flushes it. It reports whether
// the write reached the client; a broken pipe ends the stream rather than looping on it.
func writeSSE(w http.ResponseWriter, flusher http.Flusher, msg any) bool {
	body, err := json.Marshal(msg)
	if err != nil {
		return false
	}
	// The default event type ("message") is what MCP clients listen for, and the payload is
	// compact JSON, so no embedded newline can split the data field.
	if _, err := fmt.Fprintf(w, "event: message\ndata: %s\n\n", body); err != nil {
		return false
	}
	flusher.Flush()
	return true
}
