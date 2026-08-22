// Package mcp is the Lexicode MCP server (story S21, decision D-12): the one mechanism by
// which a running agent asks a human anything. It hosts the five tools of contracts §3.3 —
// ask_human, set_step, propose_wiki_page, check_criterion, request_approval — over a
// hand-rolled JSON-RPC 2.0 / streamable-HTTP endpoint at /mcp/{run_token}, plus the human
// side of the loop: POST /api/v1/elicitations/{id}/respond.
//
// # Protocol
//
// The transport is MCP's streamable HTTP, implemented by hand rather than through an SDK
// (nothing else in this codebase takes a framework dependency for a wire format it can write
// in a page, and the surface here is four methods): POST carries one JSON-RPC message and
// gets one JSON response; `initialize`, `notifications/*` (accepted with 202 and ignored),
// `ping`, `tools/list` and `tools/call` are implemented; GET (the optional server-initiated
// SSE stream) answers 405 — this server never speaks first; DELETE (session teardown) is a
// 200 no-op — the server is stateless per request, authenticated purely by the run token in
// the path. Claude Code's client is happy with exactly this subset.
//
// # Reachability (the decision S19 deferred)
//
// The endpoint is mounted twice:
//
//   - On the main API mux at /mcp/{token} — host-local callers and tests.
//   - On the S18 egress-proxy listener (0.0.0.0, docker.Proxy.SetMCPHandler): the proxy
//     dispatches to this handler any origin-form request whose path starts with /mcp/, and
//     any absolute-form proxied request targeting one of the orchestrator's own names
//     (lexicode-egress, host.docker.internal) with an /mcp/ path.
//
// That second mount is what makes the endpoint reachable from every container:
//
//   - none/allowlist runs live on lexicode-internal, which cannot reach the host at all
//     (measured; see module/docker/proxy.go). Their mcp.json therefore points at
//     http://lexicode-egress:3128/mcp/<token> — the relay container, resolvable by name on
//     the internal network, which forwards every byte to the proxy listener. A client that
//     honours HTTP_PROXY sends the same bytes absolute-form through the same relay; both
//     shapes land here.
//   - open runs ride the default bridge, where host.docker.internal reaches the host
//     (Docker Desktop natively; native Linux via the host-gateway ExtraHost the sandbox now
//     sets on agent containers). Their mcp.json points at
//     http://host.docker.internal:<proxy-port>/mcp/<token>, i.e. the same proxy listener.
//
// No proxy-policy admission of host.docker.internal was needed: MCP requests are recognised
// by path and served directly, never dialed upstream, and they authenticate by run token, not
// by Proxy-Authorization.
//
// # Run tokens
//
// Tokens are minted per run (32 hex chars of crypto/rand), kept in an in-memory registry
// (token → run), and revoked on terminal state. The runs schema has no token column
// (02-data-model.md) and this story adds no schema, so the registry does not survive a
// process restart. That is acceptable and recoverable: the token's only durable home is the
// container's own /workspace/.lexicode/mcp.json, so S22's crash reconciliation re-registers
// a surviving container's token by reading that file back (RegisterToken exists for exactly
// that), and a run whose container died is terminated by reconciliation anyway. An unknown
// or revoked token answers 404 before any JSON-RPC parsing.
//
// # Blocking and durability
//
// ask_human and a parked request_approval block until answered: elicitation row, level-0
// elicitation activity, run.elicitation SSE frame, run state → needs_input /
// awaiting_approval (through the injected RunStateSetter — the S22 scheduler owns run state;
// until it lands the setter is a seam, and tests inject a recorder), then a wait on the
// elicitation's channel bounded by the agent's wall-clock limit. Responding flips the run
// back to running and returns the answer as the MCP tool's *result* (contracts §3.4), which
// is what makes the agent resume exactly where it asked.
//
// If the orchestrator restarts while a call is blocked, the row survives but the HTTP
// response can never be written (the connection died with the process). Claude Code sees a
// failed tool call and retries or re-asks; a re-called tool with a byte-identical request
// while the elicitation is still pending REUSES the open row (ElicitationsRepo
// .PendingByRequest) — no duplicate rows, no double notification. The remaining half of
// durability — terminating runs whose containers died, expiring stranded elicitations — is
// S22 crash reconciliation, documented there.
package mcp
