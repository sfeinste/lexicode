// Package testkit is the test double module (architecture §3.1): a "fake" ports.Sandbox whose
// instances replay canned streams in memory, and a "scripted" ports.AgentRuntime that feeds a
// fixture stream-json session through the real claudecode parser. Together they are what lets
// the scheduler, trigger and steering tests exercise the whole engine — event → trigger →
// scheduler → runtime → activities — without Docker or an API call (story S20; consumed
// heavily from S22 on).
//
// The architecture doc places these behind a `testkit` build tag; they are shipped as a plain
// package instead so that other packages' tests can import them without tag gymnastics. The
// tradeoff is deliberate and safe: nothing registers this module — cmd/lexicode/serve.go must
// never wire it — so the fakes are dead code in the shipped binary, present only for tests
// that import them explicitly.
package testkit
