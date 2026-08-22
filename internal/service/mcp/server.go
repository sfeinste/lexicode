package mcp

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/spruce/lexicode/internal/domain"
	"github.com/spruce/lexicode/internal/kernel/audit"
	"github.com/spruce/lexicode/internal/kernel/bus"
	"github.com/spruce/lexicode/internal/kernel/ports"
	"github.com/spruce/lexicode/internal/kernel/store"
)

// RunStateSetter transitions a run's state. Run state belongs to the S22 scheduler (D-14);
// this narrow seam is how the MCP server asks for the needs_input / awaiting_approval /
// running transitions without owning the state machine. Until S22 lands, production wiring
// passes nil (no runs can start before the scheduler exists) and tests inject a recorder;
// S22 replaces it with the scheduler's transition entry point.
type RunStateSetter func(ctx context.Context, runID string, state domain.RunState, reason string) error

// defaultWaitCeiling bounds a blocked elicitation when the agent row carries no wall-clock
// limit: generous enough to never cut a real overnight approval short, finite so a leaked
// waiter cannot live forever.
const defaultWaitCeiling = 24 * time.Hour

// ErrNotPending is returned when a response targets an elicitation that is not pending —
// already answered, denied, expired, or canceled.
var ErrNotPending = errors.New("mcp: elicitation is not pending")

// Options configures New.
type Options struct {
	Store  *store.Store
	Bus    *bus.Bus
	Audit  *audit.Writer
	Logger *slog.Logger
	// SetRunState is the run-state seam (see RunStateSetter). Nil logs and continues —
	// pre-S22 wiring, where no real runs exist.
	SetRunState RunStateSetter
	// WaitCeiling overrides the blocked-call ceiling; zero derives it from the run's
	// agent (MaxWallClockSeconds), falling back to defaultWaitCeiling.
	WaitCeiling time.Duration
	// Now overrides the clock for tests.
	Now func() time.Time
}

// Server is the Lexicode MCP server plus the elicitation-response service. Construct with
// New; mount MCP with Handler() (main mux and the egress-proxy listener) and the human-side
// routes with Routes().
type Server struct {
	st          *store.Store
	bus         *bus.Bus
	audit       *audit.Writer
	logger      *slog.Logger
	setState    RunStateSetter
	waitCeiling time.Duration
	now         func() time.Time

	mu      sync.Mutex
	byToken map[string]string // token → run ID
	byRun   map[string]string // run ID → token
	waiters map[string][]chan ports.Response
}

// New builds the server.
func New(opts Options) *Server {
	logger := opts.Logger
	if logger == nil {
		logger = slog.Default()
	}
	now := opts.Now
	if now == nil {
		now = time.Now
	}
	return &Server{
		st:          opts.Store,
		bus:         opts.Bus,
		audit:       opts.Audit,
		logger:      logger,
		setState:    opts.SetRunState,
		waitCeiling: opts.WaitCeiling,
		now:         now,
		byToken:     map[string]string{},
		byRun:       map[string]string{},
		waiters:     map[string][]chan ports.Response{},
	}
}

// MintToken makes a fresh run token valid for runID and returns it. Minting again for the
// same run revokes the previous token (one live token per run).
func (s *Server) MintToken(runID string) (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", fmt.Errorf("mcp: minting run token: %w", err)
	}
	token := hex.EncodeToString(b[:])
	s.RegisterToken(runID, token)
	return token, nil
}

// RegisterToken makes a known token valid for runID — the S22 reattach path, which reads the
// surviving container's /workspace/.lexicode/mcp.json back instead of minting a token the
// container would never learn (the file is written once, at Prepare).
func (s *Server) RegisterToken(runID, token string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if old, ok := s.byRun[runID]; ok {
		delete(s.byToken, old)
	}
	s.byRun[runID] = token
	s.byToken[token] = runID
}

// RevokeRun invalidates a run's token — teardown and every terminal state (S22 calls it).
// Unknown runs are a no-op.
func (s *Server) RevokeRun(runID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if token, ok := s.byRun[runID]; ok {
		delete(s.byToken, token)
		delete(s.byRun, runID)
	}
}

// runForToken resolves a presented token, constant-time enough for our threat model: the
// token space is 128 bits of crypto/rand and the map lookup leaks only presence.
func (s *Server) runForToken(token string) (string, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	runID, ok := s.byToken[token]
	return runID, ok
}

// Resolve is the one way an elicitation gets its answer, shared by the HTTP respond endpoint
// and the ports.Handle.Respond seam: guard-update the row, log the level-0 activity, publish
// the run.elicitation frame, flip the run back to running, and wake the blocked tool call.
// respondedBy attributes the response to a user when one acted; nil for programmatic paths.
func (s *Server) Resolve(ctx context.Context, elicitationID string, resp ports.Response, respondedBy *string) (domain.Elicitation, error) {
	el, err := s.st.Elicitations().ByID(ctx, elicitationID)
	if err != nil {
		return domain.Elicitation{}, err
	}
	if el.State != domain.ElicitationPending {
		return el, ErrNotPending
	}

	state := domain.ElicitationAnswered
	if resp.Behavior == "deny" {
		state = domain.ElicitationDenied
	}
	raw, err := json.Marshal(resp)
	if err != nil {
		return el, fmt.Errorf("mcp: marshal response: %w", err)
	}
	nowStr := domain.FormatTime(s.now())
	if err := s.st.Elicitations().Respond(ctx, el.ID, state, raw, respondedBy, nowStr); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return el, ErrNotPending // lost a respond race; the row is already resolved
		}
		return el, err
	}
	el.State = state
	el.Response = raw
	el.RespondedBy = respondedBy
	el.RespondedAt = &nowStr

	s.appendActivity(ctx, domain.Activity{
		RunID:    el.RunID,
		Type:     domain.ActivityElicitation,
		Level:    0,
		ToolName: elicitationToolName(el.Kind),
		GroupKey: elicitationToolName(el.Kind),
		Title:    resolvedTitle(el.Kind, resp),
		Payload:  mustJSON(map[string]any{"elicitation_id": el.ID, "response": json.RawMessage(raw)}),
		OK:       boolPtr(resp.Behavior != "deny"),
	})
	s.emitElicitation(ctx, el)
	s.transition(ctx, el.RunID, domain.RunRunning, "")
	s.wake(el.ID, resp)
	return el, nil
}

// await blocks until the elicitation is resolved, the ceiling passes, or ctx ends. It
// re-reads the row after registering the waiter so a response that landed in the gap between
// row creation and registration is not missed. Several calls may wait on one elicitation —
// the idempotent re-ask path, where a retried tool call joined a row whose original caller
// is still blocked — so waiters are a list and wake is a broadcast.
func (s *Server) await(ctx context.Context, run domain.Run, elicitationID string) (ports.Response, error) {
	ch := make(chan ports.Response, 1)
	s.mu.Lock()
	s.waiters[elicitationID] = append(s.waiters[elicitationID], ch)
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		list := s.waiters[elicitationID]
		for i, c := range list {
			if c == ch {
				s.waiters[elicitationID] = append(list[:i:i], list[i+1:]...)
				break
			}
		}
		if len(s.waiters[elicitationID]) == 0 {
			delete(s.waiters, elicitationID)
		}
		s.mu.Unlock()
	}()

	// Close the registration gap: if the row is already resolved, use its stored response.
	if el, err := s.st.Elicitations().ByID(ctx, elicitationID); err == nil &&
		el.State != domain.ElicitationPending {
		var resp ports.Response
		if len(el.Response) > 0 {
			_ = json.Unmarshal(el.Response, &resp)
		}
		if el.State == domain.ElicitationExpired || el.State == domain.ElicitationCanceled {
			return resp, fmt.Errorf("mcp: elicitation %s is %s", elicitationID, el.State)
		}
		return resp, nil
	}

	timer := time.NewTimer(s.ceilingFor(ctx, run))
	defer timer.Stop()
	select {
	case resp := <-ch:
		return resp, nil
	case <-timer.C:
		// Expire the row so the UI stops asking; the run stays parked — timing the *run*
		// out is the scheduler's job (S22), this only ends the tool call honestly.
		nowStr := domain.FormatTime(s.now())
		if err := s.st.Elicitations().Respond(ctx, elicitationID,
			domain.ElicitationExpired, nil, nil, nowStr); err == nil {
			if el, err := s.st.Elicitations().ByID(ctx, elicitationID); err == nil {
				s.emitElicitation(ctx, el)
			}
		}
		return ports.Response{}, errors.New("mcp: no answer arrived within the run's wall-clock limit")
	case <-ctx.Done():
		// The caller hung up (container died, process shutdown, CLI retry). The row stays
		// pending: a human can still answer, and an identical re-ask reuses it.
		return ports.Response{}, ctx.Err()
	}
}

// wake delivers a response to every blocked call on this elicitation. A missing waiter is
// normal — restart durability: the row is resolved and a re-asked call picks the stored
// response up.
func (s *Server) wake(elicitationID string, resp ports.Response) {
	s.mu.Lock()
	list := s.waiters[elicitationID]
	delete(s.waiters, elicitationID)
	s.mu.Unlock()
	for _, ch := range list {
		ch <- resp // buffered per waiter; never blocks
	}
}

// ceilingFor is the blocked-call ceiling: the option override, else the run's agent
// wall-clock limit, else the default.
func (s *Server) ceilingFor(ctx context.Context, run domain.Run) time.Duration {
	if s.waitCeiling > 0 {
		return s.waitCeiling
	}
	if agent, err := s.st.Agents().ByID(ctx, run.AgentID); err == nil &&
		agent.MaxWallClockSeconds > 0 {
		return time.Duration(agent.MaxWallClockSeconds) * time.Second
	}
	return defaultWaitCeiling
}

// transition asks the scheduler seam for a state change; pre-S22 wiring has no seam and the
// intent is logged rather than lost silently.
func (s *Server) transition(ctx context.Context, runID string, state domain.RunState, reason string) {
	if s.setState == nil {
		s.logger.Warn("mcp: no run-state seam wired (pre-S22); state transition dropped",
			slog.String("run", runID), slog.String("state", string(state)))
		return
	}
	if err := s.setState(ctx, runID, state, reason); err != nil {
		s.logger.Error("mcp: run state transition failed",
			slog.String("run", runID), slog.String("state", string(state)),
			slog.String("error", err.Error()))
	}
}

// appendActivity writes one activity row at the run's next free seq, detached from the
// request context so a hung-up client cannot lose the record.
func (s *Server) appendActivity(ctx context.Context, a domain.Activity) int64 {
	if a.CreatedAt == "" {
		a.CreatedAt = domain.FormatTime(s.now())
	}
	if a.Attempt == 0 {
		a.Attempt = 1
	}
	if err := s.st.Activities().AppendNext(context.WithoutCancel(ctx), &a); err != nil {
		s.logger.Error("mcp: activity append failed",
			slog.String("run", a.RunID), slog.String("title", a.Title),
			slog.String("error", err.Error()))
		return -1
	}
	return a.Seq
}

// emitElicitation publishes the run.elicitation SSE frame (contracts §5.1): Kind "run" +
// ActivityType "elicitation" + subject run maps onto topic run:<id> in the hub with no topic
// map change needed.
func (s *Server) emitElicitation(ctx context.Context, el domain.Elicitation) {
	run, err := s.st.Runs().ByID(ctx, el.RunID)
	if err != nil {
		s.logger.Warn("mcp: elicitation event skipped; run lookup failed",
			slog.String("run", el.RunID), slog.String("error", err.Error()))
		return
	}
	s.emitRunEvent(ctx, run, "elicitation", map[string]any{
		"elicitation": map[string]any{
			"id":           el.ID,
			"run_id":       el.RunID,
			"kind":         string(el.Kind),
			"state":        string(el.State),
			"request":      json.RawMessage(el.Request),
			"activity_seq": el.ActivitySeq,
			"created_at":   el.CreatedAt,
		},
	})
}

// emitRunEvent publishes one run.<activity> bus event, best-effort: by the time it runs the
// mutation is committed, so a failure is logged, never unwound.
func (s *Server) emitRunEvent(ctx context.Context, run domain.Run, activity string, body map[string]any) {
	if s.bus == nil {
		return
	}
	payload, err := json.Marshal(body)
	if err != nil {
		s.logger.Error("mcp: marshal event payload failed", slog.String("error", err.Error()))
		return
	}
	pid, rid := run.ProjectID, run.ID
	e := domain.Event{
		ProjectID: &pid, Kind: "run", ActivityType: activity,
		SubjectKind: "run", SubjectID: &rid,
		ActorKind: domain.ActorAgent, ActorID: &run.AgentID,
		Payload: payload, OccurredAt: domain.FormatTime(s.now()),
	}
	if err := s.bus.Emit(context.WithoutCancel(ctx), e); err != nil {
		s.logger.Error("mcp: emit failed",
			slog.String("kind", "run."+activity), slog.String("error", err.Error()))
	}
}

// elicitationToolName maps a kind back onto the MCP tool that produces it.
func elicitationToolName(kind domain.ElicitationKind) string {
	if kind == domain.ElicitationApproval {
		return "mcp__lexicode__request_approval"
	}
	return "mcp__lexicode__ask_human"
}

// resolvedTitle is the level-0 line the transcript shows once a human (or a rule) responded.
func resolvedTitle(kind domain.ElicitationKind, resp ports.Response) string {
	if kind == domain.ElicitationApproval {
		if resp.Behavior == "deny" {
			return truncTitle("Denied: " + resp.Message)
		}
		return "Approved"
	}
	var parts []string
	for _, labels := range resp.Answers {
		parts = append(parts, strings.Join(labels, ", "))
	}
	answer := strings.Join(parts, "; ")
	if answer == "" {
		answer = resp.Text
	}
	return truncTitle("Answered: " + answer)
}

// truncTitle caps a title to one readable line.
func truncTitle(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	s = strings.TrimSpace(s)
	r := []rune(s)
	if len(r) > 96 {
		return string(r[:95]) + "…"
	}
	return s
}

func boolPtr(b bool) *bool { return &b }

// mustJSON marshals a value the caller controls; a failure is a programming error rendered
// honestly rather than panicking mid-request.
func mustJSON(v any) json.RawMessage {
	b, err := json.Marshal(v)
	if err != nil {
		return json.RawMessage(fmt.Sprintf(`{"marshal_error":%q}`, err.Error()))
	}
	return b
}
