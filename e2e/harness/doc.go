// Package harness is the shared fixture stack the end-to-end acceptance drivers run on
// (extracted from the S24 harness in S39, so the two drivers cannot drift apart).
//
// What is real: the built binary, the Docker containers, git over HTTP, the MCP server, the
// forge adapter, the poller, the trigger engine, the loop guard. What is faked, and only at
// its network edge:
//
//   - GitHub — [GitHub] serves the REST endpoints the forge adapter and the poller call, plus
//     real git smart-HTTP through `git http-backend`, so the container clones from and pushes
//     to the same host the API calls hit. It also simulates CI: a push that changes a pull
//     request's head produces a check suite whose conclusion the driver controls.
//   - `claude` — a scripted bash stand-in baked into a derived image at
//     /usr/local/bin/claude ([BuildAgentImage]). It speaks real stream-json and calls the
//     real MCP server; the script itself belongs to each driver, because what the agent is
//     scripted to do IS the story under test.
//
// Nothing in here imports the product. It talks to the binary over HTTP, to Docker over the
// CLI, and to the fixture repository over git — the same three surfaces a user has.
package harness
