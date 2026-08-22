package ports

import (
	"context"
	"encoding/json"

	"github.com/spruce/lexicode/internal/domain"
)

// AgentRuntime is an agent CLI or SDK that can be launched inside a sandbox instance and
// streams its work back as activities. Transcribed from contracts §2.4 (story S20). V1 ships
// one real implementation, the Claude Code adapter in internal/module/claudecode, plus the
// scripted replay runtime in internal/module/testkit.
type AgentRuntime interface {
	// ID is the stable identifier, e.g. "claude-code". It must be unique across registered
	// runtimes.
	ID() string
	// Caps declares what this runtime can do; the kernel and UI degrade gracefully around
	// missing capabilities instead of probing for errors.
	Caps() Caps
	// Launch starts the agent process inside inst and begins pumping its stream into sink.
	// It returns as soon as the process is started and the prompt is delivered; the stream
	// is consumed by background goroutines that run until the process exits or Stop is
	// called. ctx bounds the whole session: cancelling it abandons the pump and the exec.
	Launch(ctx context.Context, spec RunSpec, inst Instance, sink RunSink) (Handle, error)
}

// Caps is what a runtime supports: steering mid-run, elicitations (ask_human), approval
// prompts, and cost reporting in its stream.
type Caps struct {
	Steering      bool
	Elicitation   bool
	Approvals     bool
	CostReporting bool
}

// RunSpec is everything Launch needs. The scheduler assembles it; the adapter consumes it.
type RunSpec struct {
	RunID       string
	Prompt      string
	Model       string
	Effort      string
	Autonomy    domain.Autonomy
	Permissions domain.AgentPermissions
	MCPEndpoint string // host MCP URL with the run token (D-12)
	MaxSteps    int    // enforced by the scheduler (step cap → failed); informational here
	ResumeFrom  int64  // byte offset for reattach; 0 for a fresh launch
}

// Handle is one live agent session.
type Handle interface {
	// Steer queues a message for the agent. The adapter delivers it only between tool calls
	// (contracts §3.4): immediately when no tool is in flight, otherwise after the pending
	// tool_result is consumed.
	Steer(ctx context.Context, msg string) error
	// Respond routes a human's answer to an elicitation. For Claude Code the answer travels
	// back as the MCP tool's result, not stdin (contracts §3.4) — the S21 MCP server
	// registers the route; see the adapter for the seam.
	Respond(ctx context.Context, elicitationID string, r Response) error
	// Stop terminates the session: SIGTERM, a grace period, then SIGKILL. reason is carried
	// into the Result. Idempotent.
	Stop(ctx context.Context, reason string) error
	// Wait blocks until the session ends and returns its result. Safe to call from multiple
	// goroutines; ctx bounds only the wait, not the session.
	Wait(ctx context.Context) (Result, error)
}

// Response is a human's answer to an elicitation, routed back through Handle.Respond.
// Questions carry Answers (question → selected labels) or freeform Text; approvals carry
// Behavior ("allow" | "deny"), optionally UpdatedInput, and a deny Message.
type Response struct {
	Answers      map[string][]string `json:"answers,omitempty"`
	Text         string              `json:"text,omitempty"`
	Behavior     string              `json:"behavior,omitempty"`
	UpdatedInput json.RawMessage     `json:"updated_input,omitempty"`
	Message      string              `json:"message,omitempty"`
}

// Result is what a finished session amounts to: the process exit, the final message, and the
// rolled-up usage for the whole launch.
type Result struct {
	ExitCode   int
	ResultText string            // the agent's final message ("result" in stream-json)
	IsError    bool              // the runtime reported failure, or ended without a result
	Stopped    bool              // Stop was called
	StopReason string            // the reason passed to Stop
	NumTurns   int64             // agent turns, when the stream reports it
	Usage      domain.UsageDelta // final totals for this launch
}

// RunSink is the runtime→kernel direction. All methods are non-blocking and ordered: the
// adapter calls them from its pump goroutine in stream order.
type RunSink interface {
	// Activity appends one activity. Activity.Seq is the adapter's monotonically increasing
	// sequence for this launch (starting at 1); re-emitting a Seq updates that activity —
	// this is how a tool_result is merged onto its originating action. The kernel maps
	// adapter sequences onto the run's persisted activity numbering.
	Activity(domain.Activity)
	// CurrentStep updates the mutable one-liner (runs.current_step).
	CurrentStep(string)
	// Usage adds a delta to the run's token/cost rollup.
	Usage(domain.UsageDelta)
	// Elicit parks the run; returns when the elicitation row is persisted.
	Elicit(domain.Elicitation) error
	// Output records an artifact the run produced.
	Output(domain.RunOutput)
	// Offset reports how many bytes of the agent's stdout stream have been fully consumed
	// (persisted as runs.log_offset for reattach).
	Offset(int64)
}
