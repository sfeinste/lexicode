package claudecode

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/spruce/lexicode/internal/domain"
	"github.com/spruce/lexicode/internal/kernel/ports"
)

// ErrRespondUnrouted is returned by Handle.Respond until the S21 MCP server registers a
// route. Elicitation answers travel back to the agent as the blocking MCP tool's *result*,
// not via stdin (contracts §3.4): the MCP server holds the open tool call, so it — not this
// adapter — is what delivers the answer. Options.Respond is the seam it plugs into.
var ErrRespondUnrouted = errors.New(
	"claudecode: no elicitation route registered; answers are delivered as the MCP tool result (S21)")

// ErrSessionEnded is returned by Steer once the agent process has exited.
var ErrSessionEnded = errors.New("claudecode: session has ended")

// RespondFunc routes an elicitation answer to whatever is holding the blocking MCP call.
type RespondFunc func(ctx context.Context, runID, elicitationID string, r ports.Response) error

// KillFunc delivers a signal ("TERM" or "KILL") to the agent process.
type KillFunc func(ctx context.Context, signal string) error

// defaultGrace is how long Stop waits between SIGTERM and SIGKILL.
const defaultGrace = 5 * time.Second

// AttachOptions configures Attach.
type AttachOptions struct {
	// Kill delivers a signal to the agent process; nil means Stop can only close stdin and
	// wait. Launch wires the in-container pidfile kill; the scripted runtime closes its
	// fixture stream.
	Kill KillFunc
	// Grace is the SIGTERM→SIGKILL grace period; zero means defaultGrace.
	Grace time.Duration
	// Respond routes elicitation answers (see ErrRespondUnrouted).
	Respond RespondFunc
	// Now overrides the clock; tests inject a stepping clock for deterministic timing
	// fields. Nil means time.Now.
	Now func() time.Time
}

// Attach runs the stream-json pump over already-attached streams and returns the session
// handle. Launch calls it after exec'ing the CLI; module/testkit's scripted runtime calls it
// directly over a fixture stream. The pump goroutines run until stdout and stderr reach EOF,
// then Streams.Wait is collected and Wait unblocks.
//
// Timing capture (the activity timing gutter): the stream carries no timestamps, so the
// adapter derives them from arrival times. model_ms on a thought/action is the gap between
// the previous stream message and the assistant message that produced it — model plus API
// time. tool_ms (and duration_ms) on an action is the gap between its tool_use and its
// tool_result. queued_ms is not derivable from the stream and stays null.
func Attach(spec ports.RunSpec, st ports.Streams, sink ports.RunSink, opts AttachOptions) ports.Handle {
	s := &session{
		spec:     spec,
		st:       st,
		sink:     sink,
		opts:     opts,
		inFlight: map[string]*action{},
		lastDone: map[string]finished{},
		done:     make(chan struct{}),
	}
	if s.opts.Grace <= 0 {
		s.opts.Grace = defaultGrace
	}
	if s.opts.Now == nil {
		s.opts.Now = time.Now
	}
	s.lastEventAt = s.opts.Now()

	var pumps sync.WaitGroup
	pumps.Add(2)
	go func() { defer pumps.Done(); s.pumpStdout() }()
	go func() { defer pumps.Done(); s.pumpStderr() }()
	go func() {
		pumps.Wait()
		s.finish()
	}()
	return s
}

// action is one in-flight tool call, kept until its tool_result merges onto it.
type action struct {
	seq      int64
	tool     string
	title    string
	payload  map[string]any
	inputKey string // tool + input JSON; the retry heuristic's identity
	attempt  int64
	started  time.Time
	modelMS  *int64
	tokIn    int64
	tokOut   int64
	tokCache int64
}

// finished remembers the last completed call per tool for the retry heuristic: a new call
// with the same tool and identical input immediately after a failure is attempt N+1.
type finished struct {
	inputKey string
	attempt  int64
	failed   bool
}

type session struct {
	spec ports.RunSpec
	st   ports.Streams
	sink ports.RunSink
	opts AttachOptions

	mu          sync.Mutex
	seq         int64
	inFlight    map[string]*action
	lastDone    map[string]finished
	queue       []string // steering messages awaiting a gap between tool calls
	stopped     bool
	stopReason  string
	ended       bool
	lastEventAt time.Time
	lastUsageID string            // message id whose usage was already counted
	usageTotal  domain.UsageDelta // everything emitted through sink.Usage so far
	resultLine  *streamLine       // the final "result" message, if one arrived

	done    chan struct{}
	result  ports.Result
	waitErr error
}

// pumpStdout consumes the NDJSON stream line by line, reporting the consumed byte offset
// (spec.ResumeFrom plus everything fully processed) after each line so reattach can resume
// without re-emitting (runs.log_offset).
func (s *session) pumpStdout() {
	br := bufio.NewReaderSize(s.st.Stdout, 64*1024)
	var consumed int64
	for {
		line, err := br.ReadBytes('\n')
		if len(line) > 0 {
			s.handleLine(bytes.TrimRight(line, "\r\n"))
			consumed += int64(len(line))
			s.sink.Offset(s.spec.ResumeFrom + consumed)
		}
		if err != nil {
			return // io.EOF, or the stream broke; either way the pump is done
		}
	}
}

// pumpStderr turns every stderr line into a level-2 system activity (contracts §3.2).
func (s *session) pumpStderr() {
	if s.st.Stderr == nil {
		return
	}
	sc := bufio.NewScanner(s.st.Stderr)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for sc.Scan() {
		line := sc.Text()
		if line == "" {
			continue
		}
		capped, _ := capOutput(line)
		s.mu.Lock()
		s.emitLocked(domain.Activity{
			Type:    domain.ActivitySystem,
			Level:   2,
			Title:   truncateLine(line),
			Payload: mustJSON(map[string]any{"stream": "stderr", "line": capped}),
		}, s.opts.Now())
		s.mu.Unlock()
	}
}

// handleLine parses one stdout line. A malformed line becomes a level-2 system activity and
// the stream continues — nothing on stdout may kill the run.
func (s *session) handleLine(raw []byte) {
	now := s.opts.Now()
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 {
		return
	}

	var line streamLine
	if err := json.Unmarshal(trimmed, &line); err != nil {
		s.malformed(trimmed, "unparsed runtime output", now)
		return
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	switch line.Type {
	case "system":
		s.handleSystemLocked(&line, trimmed, now)
	case "assistant":
		s.handleAssistantLocked(&line, now)
	case "user":
		s.handleUserLocked(&line, now)
	case "result":
		s.handleResultLocked(&line, now)
	default:
		s.emitLocked(domain.Activity{
			Type:    domain.ActivitySystem,
			Level:   2,
			Title:   truncateLine("unhandled message: " + line.Type),
			Payload: mustJSON(map[string]any{"line": cappedString(trimmed)}),
		}, now)
	}
	s.lastEventAt = now
}

func (s *session) malformed(raw []byte, title string, now time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.emitLocked(domain.Activity{
		Type:    domain.ActivitySystem,
		Level:   2,
		Title:   title,
		Payload: mustJSON(map[string]any{"line": cappedString(raw)}),
	}, now)
	s.lastEventAt = now
}

func (s *session) handleSystemLocked(line *streamLine, raw []byte, now time.Time) {
	if line.Subtype == "init" {
		s.emitLocked(domain.Activity{
			Type:  domain.ActivitySystem,
			Level: 2,
			Title: "session started",
			Payload: mustJSON(map[string]any{
				"session_id": line.SessionID,
				"tools":      line.Tools,
				"cwd":        line.CWD,
				"model":      line.Model,
			}),
		}, now)
		return
	}
	s.emitLocked(domain.Activity{
		Type:    domain.ActivitySystem,
		Level:   2,
		Title:   truncateLine("system: " + line.Subtype),
		Payload: mustJSON(map[string]any{"line": cappedString(raw)}),
	}, now)
}

func (s *session) handleAssistantLocked(line *streamLine, now time.Time) {
	if line.Message == nil {
		return
	}
	// Usage attribution: each assistant event repeats its API message's usage, and one API
	// message can arrive as several events (one per content block) — count each message id
	// once, and stamp the tokens on the first activity it produces.
	var tokIn, tokOut, tokCacheRead int64
	if line.Message.ID != s.lastUsageID && len(line.Message.Usage) > 0 {
		var u apiUsage
		if err := json.Unmarshal(line.Message.Usage, &u); err == nil {
			delta := domain.UsageDelta{
				TokensIn:         u.InputTokens,
				TokensOut:        u.OutputTokens,
				TokensCacheRead:  u.CacheReadTokens,
				TokensCacheWrite: u.CacheCreationTokens,
			}
			if delta != (domain.UsageDelta{}) {
				s.sink.Usage(delta)
				s.usageTotal = s.usageTotal.Add(delta)
				tokIn, tokOut, tokCacheRead = u.InputTokens, u.OutputTokens, u.CacheReadTokens
			}
		}
		s.lastUsageID = line.Message.ID
	}

	// model_ms: the gap since the previous stream message — the time the model (plus API)
	// spent producing this message. Attributed to the first activity of the message.
	modelMS := now.Sub(s.lastEventAt).Milliseconds()
	first := true
	stamp := func(a *domain.Activity) {
		if first {
			a.ModelMS = &modelMS
			a.TokensIn, a.TokensOut, a.TokensCacheRead = tokIn, tokOut, tokCacheRead
			first = false
		}
	}

	for _, block := range line.Message.Content {
		switch block.Type {
		case "text", "thinking":
			text := block.Text
			if block.Type == "thinking" {
				text = block.Thinking
			}
			if text == "" {
				continue
			}
			a := domain.Activity{
				Type:    domain.ActivityThought,
				Level:   1,
				Title:   truncateLine(text),
				Payload: mustJSON(map[string]any{"text": text}),
			}
			stamp(&a)
			s.emitLocked(a, now)
		case "tool_use":
			s.beginActionLocked(block, stamp, now)
		}
	}
}

func (s *session) beginActionLocked(block contentBlock, stamp func(*domain.Activity), now time.Time) {
	title, payload := formatAction(block.Name, block.Input)
	act := &action{
		tool:     block.Name,
		title:    title,
		payload:  payload,
		inputKey: block.Name + "\x00" + string(block.Input),
		attempt:  1,
		started:  now,
	}
	// Retry badge: an immediate re-issue of a failed call with identical input is attempt N+1.
	if prev, ok := s.lastDone[block.Name]; ok && prev.failed && prev.inputKey == act.inputKey {
		act.attempt = prev.attempt + 1
	}

	a := domain.Activity{
		Type:     domain.ActivityAction,
		Level:    1,
		ToolName: block.Name,
		GroupKey: block.Name, // consecutive equal keys collapse in the UI
		Title:    title,
		Payload:  mustJSON(payload),
		Attempt:  act.attempt,
	}
	stamp(&a)
	act.modelMS, act.tokIn, act.tokOut, act.tokCache = a.ModelMS, a.TokensIn, a.TokensOut, a.TokensCacheRead

	act.seq = s.emitLocked(a, now)
	if block.ID != "" {
		s.inFlight[block.ID] = act
	}
}

func (s *session) handleUserLocked(line *streamLine, now time.Time) {
	if line.Message == nil {
		return
	}
	for _, block := range line.Message.Content {
		if block.Type != "tool_result" {
			continue // our own steering echoes and other user content carry no new information
		}
		act, ok := s.inFlight[block.ToolUseID]
		if !ok {
			s.emitLocked(domain.Activity{
				Type:    domain.ActivitySystem,
				Level:   2,
				Title:   "orphan tool_result " + block.ToolUseID,
				Payload: mustJSON(map[string]any{"content": cappedString(block.Content)}),
			}, now)
			continue
		}
		delete(s.inFlight, block.ToolUseID)

		mergeResult(act.tool, act.payload, block)
		toolMS := now.Sub(act.started).Milliseconds()
		okVal := !block.IsError

		// Re-emit under the same Seq: the sink upserts, merging the result onto the
		// originating action (contracts §3.2).
		s.emitAtLocked(domain.Activity{
			Seq:             act.seq,
			Type:            domain.ActivityAction,
			Level:           1,
			ToolName:        act.tool,
			GroupKey:        act.tool,
			Title:           act.title,
			Payload:         mustJSON(act.payload),
			OK:              &okVal,
			Attempt:         act.attempt,
			DurationMS:      &toolMS,
			ToolMS:          &toolMS,
			ModelMS:         act.modelMS,
			TokensIn:        act.tokIn,
			TokensOut:       act.tokOut,
			TokensCacheRead: act.tokCache,
		}, now)
		s.lastDone[act.tool] = finished{inputKey: act.inputKey, attempt: act.attempt, failed: block.IsError}
	}
	// A gap between tool calls: deliver queued steering only when nothing is in flight
	// (contracts §3.4 — "applied after the current step" is literally true).
	if len(s.inFlight) == 0 {
		s.flushQueueLocked()
	}
}

func (s *session) handleResultLocked(line *streamLine, now time.Time) {
	s.resultLine = line

	// Final usage: the result carries session totals; emit only what was not already
	// attributed per step, plus the cost (which the stream reports nowhere else).
	var u apiUsage
	if len(line.Usage) > 0 {
		_ = json.Unmarshal(line.Usage, &u)
	}
	final := domain.UsageDelta{
		TokensIn:         u.InputTokens,
		TokensOut:        u.OutputTokens,
		TokensCacheRead:  u.CacheReadTokens,
		TokensCacheWrite: u.CacheCreationTokens,
		CostCents:        centsUSD(line.TotalCostUSD),
	}
	delta := domain.UsageDelta{
		TokensIn:         clampNonNeg(final.TokensIn - s.usageTotal.TokensIn),
		TokensOut:        clampNonNeg(final.TokensOut - s.usageTotal.TokensOut),
		TokensCacheRead:  clampNonNeg(final.TokensCacheRead - s.usageTotal.TokensCacheRead),
		TokensCacheWrite: clampNonNeg(final.TokensCacheWrite - s.usageTotal.TokensCacheWrite),
		CostCents:        clampNonNeg(final.CostCents - s.usageTotal.CostCents),
	}
	if delta != (domain.UsageDelta{}) {
		s.sink.Usage(delta)
		s.usageTotal = s.usageTotal.Add(delta)
	}

	failed := line.IsError || (line.Subtype != "" && line.Subtype != "success")
	if failed {
		okVal := false
		title := line.Result
		if title == "" {
			title = line.Subtype
		}
		s.emitLocked(domain.Activity{
			Type:  domain.ActivityError,
			Level: 0,
			Title: truncateLine(title),
			OK:    &okVal,
			Payload: mustJSON(map[string]any{
				"subtype": line.Subtype,
				"result":  line.Result,
			}),
			CostCents: delta.CostCents,
		}, now)
		return
	}
	s.emitLocked(domain.Activity{
		Type:      domain.ActivityResponse,
		Level:     0,
		Title:     truncateLine(line.Result),
		Payload:   mustJSON(map[string]any{"text": line.Result}),
		CostCents: delta.CostCents,
	}, now)
}

// emitLocked assigns the next adapter sequence number and sends the activity. mu held.
func (s *session) emitLocked(a domain.Activity, now time.Time) int64 {
	s.seq++
	a.Seq = s.seq
	return s.emitAtLocked(a, now)
}

// emitAtLocked sends an activity carrying an explicit Seq (a re-emission updates that
// activity). mu held.
func (s *session) emitAtLocked(a domain.Activity, now time.Time) int64 {
	a.RunID = s.spec.RunID
	if a.Attempt == 0 {
		a.Attempt = 1
	}
	a.CreatedAt = domain.FormatTime(now)
	s.sink.Activity(a)
	return a.Seq
}

// finish collects the process exit and publishes the result.
func (s *session) finish() {
	var exit int
	var err error
	if s.st.Wait != nil {
		exit, err = s.st.Wait()
	}

	s.mu.Lock()
	s.ended = true
	res := ports.Result{
		ExitCode:   exit,
		Stopped:    s.stopped,
		StopReason: s.stopReason,
		Usage:      s.usageTotal,
	}
	if rl := s.resultLine; rl != nil {
		res.ResultText = rl.Result
		res.NumTurns = rl.NumTurns
		res.IsError = rl.IsError || (rl.Subtype != "" && rl.Subtype != "success")
	} else if !s.stopped {
		// The stream ended without a result message and nobody asked it to stop: that is a
		// failure however the process exited.
		res.IsError = true
	}
	// Undelivered steering is reported, not silently dropped.
	dropped := len(s.queue)
	s.queue = nil
	s.result = res
	s.waitErr = err
	s.mu.Unlock()

	if dropped > 0 {
		s.mu.Lock()
		s.emitLocked(domain.Activity{
			Type:    domain.ActivitySystem,
			Level:   2,
			Title:   fmt.Sprintf("%d steering message(s) undelivered at exit", dropped),
			Payload: mustJSON(map[string]any{"count": dropped}),
		}, s.opts.Now())
		s.mu.Unlock()
	}
	close(s.done)
}

// --- ports.Handle ---

// Steer queues msg for the agent, delivering immediately when no tool call is in flight and
// otherwise after the pending tool_result is consumed (contracts §3.4).
func (s *session) Steer(_ context.Context, msg string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.ended {
		return ErrSessionEnded
	}
	if len(s.inFlight) > 0 {
		s.queue = append(s.queue, msg)
		return nil
	}
	return s.writeUserLocked(msg)
}

// Respond routes an elicitation answer; see ErrRespondUnrouted for why this is a seam.
func (s *session) Respond(ctx context.Context, elicitationID string, r ports.Response) error {
	if s.opts.Respond == nil {
		return ErrRespondUnrouted
	}
	return s.opts.Respond(ctx, s.spec.RunID, elicitationID, r)
}

// Stop terminates the session: close stdin, SIGTERM, wait the grace period, SIGKILL, then
// wait for the pump to drain. Idempotent; a second Stop just waits.
func (s *session) Stop(ctx context.Context, reason string) error {
	s.mu.Lock()
	already := s.stopped
	if !already {
		s.stopped = true
		s.stopReason = reason
	}
	stdin := s.st.Stdin
	s.mu.Unlock()

	if !already {
		if stdin != nil {
			_ = stdin.Close()
		}
		if s.opts.Kill != nil {
			_ = s.opts.Kill(ctx, "TERM")
		}
	}

	select {
	case <-s.done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	case <-time.After(s.opts.Grace):
	}
	if s.opts.Kill != nil {
		_ = s.opts.Kill(ctx, "KILL")
	}
	select {
	case <-s.done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// Wait blocks until the session ends. ctx bounds only the wait.
func (s *session) Wait(ctx context.Context) (ports.Result, error) {
	select {
	case <-s.done:
		s.mu.Lock()
		defer s.mu.Unlock()
		return s.result, s.waitErr
	case <-ctx.Done():
		return ports.Result{}, ctx.Err()
	}
}

// flushQueueLocked writes every queued steering message. mu held.
func (s *session) flushQueueLocked() {
	for len(s.queue) > 0 {
		if err := s.writeUserLocked(s.queue[0]); err != nil {
			return // stdin is gone; finish() reports the remainder as undelivered
		}
		s.queue = s.queue[1:]
	}
	s.queue = nil
}

// writeUserLocked writes one user message in the CLI's --input-format stream-json shape.
// mu held.
func (s *session) writeUserLocked(text string) error {
	if s.st.Stdin == nil {
		return errors.New("claudecode: no stdin attached")
	}
	_, err := s.st.Stdin.Write(userMessage(text))
	return err
}

// userMessage is the stdin wire shape for both the initial prompt and steering messages.
func userMessage(text string) []byte {
	msg := map[string]any{
		"type": "user",
		"message": map[string]any{
			"role": "user",
			"content": []map[string]any{
				{"type": "text", "text": text},
			},
		},
	}
	b, _ := json.Marshal(msg)
	return append(b, '\n')
}

// --- small helpers ---

func mustJSON(v any) json.RawMessage {
	b, err := json.Marshal(v)
	if err != nil {
		// Payloads are built from decoded JSON and plain values; this cannot fail without a
		// programming error, and an activity must still carry *a* payload.
		return json.RawMessage(fmt.Sprintf(`{"marshal_error":%q}`, err.Error()))
	}
	return b
}

func cappedString(b []byte) string {
	s, _ := capOutput(string(b))
	return s
}

// centsUSD converts the stream's total_cost_usd to integer cents, rounding half up.
func centsUSD(usd float64) int64 {
	return int64(usd*100 + 0.5)
}

func clampNonNeg(v int64) int64 {
	if v < 0 {
		return 0
	}
	return v
}
