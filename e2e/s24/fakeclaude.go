package main

// fakeClaude is the scripted stand-in agent baked into the derived image at
// /usr/local/bin/claude (the S20/S22 smoke pattern, image-ref edition). It honours the
// contracts §3.1 shape from the outside — consumes the prompt as the first stdin message,
// emits a stream-json session on stdout — and does REAL work through the real seams: it
// calls the Lexicode MCP server over HTTP (ask_human parks the run; request_approval parks
// it again in interactive mode) using the run token from /workspace/.lexicode/mcp.json, and
// it COMMITS its work locally.
//
// It does not push, and could not: `origin` is tokenless by the time it starts, and the fake
// GitHub's git endpoints demand the repository token. The branch reaching the remote is the
// orchestrator's teardown push, which the driver asserts.
//
// Modes, chosen by a marker the driver puts in the delegate prompt (read back from
// /workspace/.lexicode/prompt.md):
//
//	(none)                → ask a question, wait for the answer, commit, finish.
//	E2E-MODE: stall       → leave WIP in the tree and sleep; the driver stops the run
//	                        midway and asserts the §10.5 artifact push preserved the branch.
//	E2E-MODE: interactive → ask a question, then request an approval, then loop slowly on
//	                        set_step so a human can steer / stop / take over — the browser
//	                        walkthrough script.
const fakeClaude = `#!/bin/bash
set -u
IFS= read -r _prompt || true

MCP_URL=$(jq -r '.mcpServers.lexicode.url' /workspace/.lexicode/mcp.json)
MODE=normal
grep -q 'E2E-MODE: stall' /workspace/.lexicode/prompt.md 2>/dev/null && MODE=stall
grep -q 'E2E-MODE: interactive' /workspace/.lexicode/prompt.md 2>/dev/null && MODE=interactive

emit() { printf '%s\n' "$1"; }

# await_stdin_eof is what makes this fixture honest. Under --input-format stream-json the real
# CLI does not exit when it emits a result: it goes back to reading stdin for the next user
# message and exits only at EOF. The orchestrator closes stdin when the last turn ends with
# nothing queued, and that — not the script reaching its own end — is what ends this process.
# A fixture that exited by itself would let an adapter that never closes stdin pass, which is
# precisely how a completed run came to hang in the running state, with no push and no PR.
await_stdin_eof() { cat >/dev/null; }

mcp_call() { # $1 = tool name, $2 = JSON arguments; blocks until the tool returns
  curl -sS --max-time 86400 -X POST "$MCP_URL" \
    -H 'Content-Type: application/json' -H 'Accept: application/json' \
    -d "{\"jsonrpc\":\"2.0\",\"id\":1,\"method\":\"tools/call\",\"params\":{\"name\":\"$1\",\"arguments\":$2}}"
}

emit '{"type":"system","subtype":"init","cwd":"/workspace","session_id":"e2e","tools":["Bash","mcp__lexicode__ask_human"],"model":"fake-model"}'

if [ "$MODE" = stall ]; then
  emit '{"type":"assistant","message":{"id":"m1","role":"assistant","content":[{"type":"text","text":"picking the work back up; leaving WIP in the tree"}],"usage":{"input_tokens":12,"output_tokens":6}}}'
  echo "wip: half-finished follow-up" > WIP-NOTES.md
  mcp_call set_step '{"step":"working slowly on purpose","index":1,"total":9}' >/dev/null 2>&1
  # Short sleeps, not one long one: bash only handles the adapter's TERM between foreground
  # commands, and a long-lived sleep child would hold the exec's stdout open past the kill.
  trap 'exit 143' TERM
  i=0
  while [ $i -lt 600 ]; do sleep 1; i=$((i+1)); done
  emit '{"type":"result","subtype":"success","is_error":false,"num_turns":1,"result":"unreachable","total_cost_usd":0.001,"usage":{"input_tokens":12,"output_tokens":6}}'
  await_stdin_eof
  exit 0
fi

QUESTION_ARGS='{"questions":[{"question":"Which storage should the idempotency keys use?","header":"Storage","options":[{"label":"Postgres","description":"Same database as the charges table"},{"label":"Redis","description":"Fast, but another moving part to operate"}],"multiSelect":false}]}'

emit '{"type":"assistant","message":{"id":"m1","role":"assistant","content":[{"type":"text","text":"reading the ticket; the storage choice is ambiguous, asking"}],"usage":{"input_tokens":40,"output_tokens":12}}}'
emit "{\"type\":\"assistant\",\"message\":{\"id\":\"m2\",\"role\":\"assistant\",\"content\":[{\"type\":\"tool_use\",\"id\":\"t1\",\"name\":\"mcp__lexicode__ask_human\",\"input\":$QUESTION_ARGS}]}}"
ANSWER=$(mcp_call ask_human "$QUESTION_ARGS")
CONTENT=$(printf '%s' "$ANSWER" | jq -Rs .)
emit "{\"type\":\"user\",\"message\":{\"role\":\"user\",\"content\":[{\"type\":\"tool_result\",\"tool_use_id\":\"t1\",\"content\":$CONTENT}]}}"
emit '{"type":"assistant","message":{"id":"m3","role":"assistant","content":[{"type":"text","text":"answer received; implementing"}],"usage":{"input_tokens":30,"output_tokens":9}}}'

if [ "$MODE" = interactive ]; then
  APPROVAL_ARGS='{"tool_name":"Bash","input":{"command":"npm publish --access public"}}'
  emit "{\"type\":\"assistant\",\"message\":{\"id\":\"m4\",\"role\":\"assistant\",\"content\":[{\"type\":\"tool_use\",\"id\":\"t2\",\"name\":\"mcp__lexicode__request_approval\",\"input\":$APPROVAL_ARGS}]}}"
  DECISION=$(mcp_call request_approval "$APPROVAL_ARGS")
  DCONTENT=$(printf '%s' "$DECISION" | jq -Rs .)
  emit "{\"type\":\"user\",\"message\":{\"role\":\"user\",\"content\":[{\"type\":\"tool_result\",\"tool_use_id\":\"t2\",\"content\":$DCONTENT}]}}"
  trap 'exit 143' TERM
  for i in 1 2 3 4 5 6 7 8 9 10 11 12; do
    mcp_call set_step "{\"step\":\"polishing pass $i of 12\",\"index\":$i,\"total\":12}" >/dev/null 2>&1
    j=0
    while [ $j -lt 10 ]; do sleep 1; j=$((j+1)); done
  done
fi

mcp_call set_step '{"step":"editing IMPLEMENTATION.md","index":2,"total":3}' >/dev/null 2>&1
emit '{"type":"assistant","message":{"id":"m5","role":"assistant","content":[{"type":"tool_use","id":"t3","name":"Bash","input":{"command":"git add -A && git commit -m \"feat: add idempotency keys\""}}]}}'
{
  echo "# Idempotency keys" > IMPLEMENTATION.md
  echo "" >> IMPLEMENTATION.md
  echo "Chosen storage (from the delegating human): see the answer in the run log." >> IMPLEMENTATION.md
  git add -A
  git commit -q -m "feat: add idempotency keys"
} >/tmp/gitwork.log 2>&1
GIT_EXIT=$?
GITLOG=$(jq -Rs . </tmp/gitwork.log)
emit "{\"type\":\"user\",\"message\":{\"role\":\"user\",\"content\":[{\"type\":\"tool_result\",\"tool_use_id\":\"t3\",\"content\":$GITLOG,\"is_error\":$([ $GIT_EXIT -eq 0 ] && echo false || echo true)}]}}"
mcp_call set_step '{"step":"done","index":3,"total":3}' >/dev/null 2>&1
emit '{"type":"result","subtype":"success","is_error":false,"num_turns":4,"result":"Implemented idempotency keys and committed them.","total_cost_usd":0.0342,"usage":{"input_tokens":1200,"output_tokens":340}}'
await_stdin_eof
exit 0
`
