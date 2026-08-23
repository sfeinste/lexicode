package claudecode

import (
	"context"
	"fmt"
	"io"
	"time"

	"github.com/spruce/lexicode/internal/kernel"
	"github.com/spruce/lexicode/internal/kernel/ports"
)

// runtimeID is both the kernel module name and the AgentRuntime port ID.
const runtimeID = "claude-code"

// pidFile is where the launch wrapper records the agent's PID inside the container, so Stop
// can signal a process this adapter never sees directly (it only holds exec streams). /tmp is
// writable in the agent container under every posture — it was a tmpfs while the rootfs was
// read-only, and an ordinary directory on the writable rootfs the POC ships (see the
// "Container posture" block in module/docker's sandbox.go).
const pidFile = "/tmp/lexicode-agent.pid"

// Workspace paths fixed by contracts §3.1; S19's workspace preparation materialises both files.
const (
	mcpConfigPath = "/workspace/.lexicode/mcp.json"
	settingsPath  = "/workspace/.claude/settings.json"
)

// Options configures New.
type Options struct {
	// Bin is the agent executable; empty means "claude". A test seam: the docker-tagged
	// smoke test points it at a fixture script so the full Launch path — exec, pidfile
	// wrapper, prompt on stdin, parser — runs without credentials.
	Bin string
	// Grace is the Stop SIGTERM→SIGKILL grace period; zero means the 5s default.
	Grace time.Duration
	// Respond routes elicitation answers to whatever holds the blocking MCP tool call. Nil
	// until the S21 MCP server registers itself; Handle.Respond then fails with
	// ErrRespondUnrouted (see that error for why the MCP server owns delivery).
	Respond RespondFunc
}

// Module implements the kernel module lifecycle and registers the one Claude Code runtime.
type Module struct {
	rt *Runtime
}

// New builds the module.
func New(opts Options) *Module { return &Module{rt: NewRuntime(opts)} }

// Name implements kernel.Module.
func (m *Module) Name() string { return runtimeID }

// Init implements kernel.Module: register the runtime port. No I/O.
func (m *Module) Init(k *kernel.Kernel) error { return k.RegisterRuntime(m.rt) }

// Start implements kernel.Module. The runtime has no background work of its own; sessions
// are owned by the scheduler that launches them.
func (m *Module) Start(context.Context) error { return nil }

// Stop implements kernel.Module.
func (m *Module) Stop(context.Context) error { return nil }

// Runtime returns the concrete adapter, for the wiring site (S21 hands it the Respond route).
func (m *Module) Runtime() *Runtime { return m.rt }

// Runtime is the Claude Code implementation of ports.AgentRuntime (story S20).
type Runtime struct {
	opts Options
}

// NewRuntime builds the runtime without the module wrapper; tests use it directly.
func NewRuntime(opts Options) *Runtime {
	if opts.Bin == "" {
		opts.Bin = "claude"
	}
	return &Runtime{opts: opts}
}

// ID implements ports.AgentRuntime.
func (r *Runtime) ID() string { return runtimeID }

// Caps implements ports.AgentRuntime. Claude Code supports all four: steering via stdin,
// elicitations and approvals via the Lexicode MCP server, and cost reporting in its result
// message.
func (r *Runtime) Caps() ports.Caps {
	return ports.Caps{Steering: true, Elicitation: true, Approvals: true, CostReporting: true}
}

// Launch implements ports.AgentRuntime: exec the CLI (contracts §3.1) inside inst, deliver
// the prompt as the first stdin message, and attach the stream pump.
func (r *Runtime) Launch(ctx context.Context, spec ports.RunSpec, inst ports.Instance, sink ports.RunSink) (ports.Handle, error) {
	st, err := inst.Exec(ctx, launchArgv(r.opts.Bin, spec.Model), ports.ExecOpts{})
	if err != nil {
		return nil, fmt.Errorf("claudecode: exec agent: %w", err)
	}

	// The prompt is the first stdin message, never argv — prompts carry wiki pages and
	// exceed argv limits (contracts §3.1).
	if _, err := st.Stdin.Write(userMessage(spec.Prompt)); err != nil {
		_ = st.Stdin.Close()
		drainAndWait(st)
		return nil, fmt.Errorf("claudecode: delivering prompt: %w", err)
	}

	return Attach(spec, st, sink, AttachOptions{
		Kill:    killFunc(inst),
		Grace:   r.opts.Grace,
		Respond: r.opts.Respond,
	}), nil
}

// launchArgv is the exact command line of contracts §3.1, wrapped in a shell that records the
// process's PID first: the port gives Stop no process handle, so the adapter signals the PID
// via a second Exec (see killFunc). exec "$@" keeps the agent as the recorded process — no
// intermediate shell survives to swallow the signal.
func launchArgv(bin, model string) []string {
	agent := []string{
		bin, "-p",
		"--output-format", "stream-json", "--input-format", "stream-json", "--verbose",
		"--model", model,
		"--permission-prompt-tool", "mcp__lexicode__request_approval",
		"--mcp-config", mcpConfigPath,
		"--settings", settingsPath,
	}
	return append([]string{
		"/bin/sh", "-c", `echo $$ >` + pidFile + ` && exec "$@"`, "sh",
	}, agent...)
}

// killFunc signals the launched agent via its pidfile: SIGTERM for the grace period, SIGKILL
// after. Errors are tolerated by Stop — a process that already exited is success.
func killFunc(inst ports.Instance) KillFunc {
	return func(ctx context.Context, signal string) error {
		st, err := inst.Exec(ctx, []string{
			"/bin/sh", "-c", "kill -" + signal + " $(cat " + pidFile + ") 2>/dev/null",
		}, ports.ExecOpts{})
		if err != nil {
			return err
		}
		_ = st.Stdin.Close()
		drainAndWait(st)
		return nil
	}
}

// drainAndWait reads a short-lived exec's streams to EOF and collects its exit, releasing
// the connection.
func drainAndWait(st ports.Streams) {
	if st.Stdout != nil {
		_, _ = io.Copy(io.Discard, st.Stdout)
	}
	if st.Stderr != nil {
		_, _ = io.Copy(io.Discard, st.Stderr)
	}
	if st.Wait != nil {
		_, _ = st.Wait()
	}
}
