// Package claudecode is the Claude Code agent runtime module (story S20): the one real
// implementation of ports.AgentRuntime. It execs the CLI inside a sandbox instance with the
// exact command line of contracts §3.1, delivers the prompt as the first stdin message, and
// is the only code in the system that understands the stream-json output format — parsing it
// into typed activities per contracts §3.2, with per-tool title formatters, diff hunks for
// edits, captured-and-truncated output for Bash, tool_result correlation, usage and cost
// rollup, and level assignment at ingest.
//
// Steering is written to stdin only between tool calls (contracts §3.4); Stop signals
// SIGTERM via an in-container pidfile, waits a grace period, then SIGKILL. The exported
// Attach seam runs the same pump over any attached streams — module/testkit's scripted
// runtime replays fixture sessions through it, which is what makes every later story
// testable without an API call.
package claudecode
