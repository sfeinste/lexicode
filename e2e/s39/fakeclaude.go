package main

// fakeClaude is the scripted stand-in agent baked into the derived image at
// /usr/local/bin/claude. It honours the contracts §3.1 shape from the outside — consumes the
// prompt as the first stdin message, emits a stream-json session on stdout — and does REAL
// work through the real seams: git against the fake GitHub's smart-HTTP remote, and the real
// Lexicode MCP server over HTTP using the run token from /workspace/.lexicode/mcp.json.
//
// Which of the four roles it plays is read out of /workspace/.lexicode/prompt.md, because
// that is where a trigger's prompt override lands. The chain's triggers write the role, the
// pull request number and the pull request's branch into that override with {{...}}
// interpolation — the same mechanism a user types into the trigger editor.
//
//	E2E-ROLE: dev-implement   implement the ticket on the run's own branch, check off an
//	                          acceptance criterion, push. The orchestrator opens the PR.
//	E2E-ROLE: reviewer        read the PR's diff and call submit_review with severity-tagged
//	                          findings. E2E-REVIEW says request_changes or comment.
//	E2E-ROLE: dev-address     check out the PR's branch, push a fix to it, then submit a
//	                          COMMENT review saying so — the "addressed, please re-review"
//	                          hop that carries the chain forward.
//	E2E-ROLE: dev-cifix       check out the PR's branch and push a fix to it. No review: the
//	                          CI branch of the chain is not meant to bounce.
const fakeClaude = `#!/bin/bash
set -u
IFS= read -r _prompt || true

PROMPT=/workspace/.lexicode/prompt.md
MCP_URL=$(jq -r '.mcpServers.lexicode.url' /workspace/.lexicode/mcp.json)

field() { grep -o "$1: .*" "$PROMPT" 2>/dev/null | head -1 | sed "s/^$1: //" | tr -d '\r'; }
ROLE=$(field 'E2E-ROLE'); [ -n "$ROLE" ] || ROLE=dev-implement
PR=$(field 'E2E-PR')
BRANCH=$(field 'E2E-BRANCH')
REVIEW=$(field 'E2E-REVIEW'); [ -n "$REVIEW" ] || REVIEW=request_changes

emit() { printf '%s\n' "$1"; }
mcp_call() { # $1 = tool name, $2 = JSON arguments
  curl -sS --max-time 600 -X POST "$MCP_URL" \
    -H 'Content-Type: application/json' -H 'Accept: application/json' \
    -d "{\"jsonrpc\":\"2.0\",\"id\":1,\"method\":\"tools/call\",\"params\":{\"name\":\"$1\",\"arguments\":$2}}"
}
step() { mcp_call set_step "{\"step\":$(printf '%s' "$1" | jq -Rs .),\"index\":$2,\"total\":$3}" >/dev/null 2>&1; }
say() {
  emit "{\"type\":\"assistant\",\"message\":{\"id\":\"m$RANDOM\",\"role\":\"assistant\",\"content\":[{\"type\":\"text\",\"text\":$(printf '%s' "$1" | jq -Rs .)}],\"usage\":{\"input_tokens\":120,\"output_tokens\":40}}}"
}
finish() {
  emit "{\"type\":\"result\",\"subtype\":\"success\",\"is_error\":false,\"num_turns\":4,\"result\":$(printf '%s' "$1" | jq -Rs .),\"total_cost_usd\":0.0217,\"usage\":{\"input_tokens\":1400,\"output_tokens\":260}}"
  exit 0
}

emit '{"type":"system","subtype":"init","cwd":"/workspace","session_id":"e2e-s39","tools":["Bash","mcp__lexicode__submit_review"],"model":"fake-model"}'
say "role: $ROLE"

# Note: this agent does NOT exclude the orchestrator's scaffolding itself. The provisioner
# materializes .lexicode/ (which holds the run's live MCP token) and .claude/ into the
# workspace root, and a plain "git add -A" below would commit both — so the sandbox writes
# them into .git/info/exclude during Prepare. The assertion after the first push proves it:
# if the sandbox ever stops doing it, the token lands in the fake GitHub's repository and
# the run fails there, not silently.

case "$ROLE" in

dev-implement)
  step "writing the idempotency key store" 1 3
  say "The ticket wants idempotency keys on POST /charges. Implementing."
  {
    mkdir -p src
    cat > src/idempotency.ts <<'EOF'
// Idempotency keys for POST /charges. Keys live for 24h; a replay returns the first result.
const seen = new Map();
export function remember(key, result) { seen.set(key, { result, at: Date.now() }); }
export function replay(key) {
  const hit = seen.get(key);
  if (!hit) return null;
  if (Date.now() - hit.at > 86400000) { seen.delete(key); return null; }
  return hit.result;
}
EOF
    git add -A
    git commit -q -m "feat: idempotency keys for POST /charges"
    git push -q origin HEAD
  } >/tmp/work.log 2>&1
  GIT_EXIT=$?
  emit "{\"type\":\"user\",\"message\":{\"role\":\"user\",\"content\":[{\"type\":\"tool_result\",\"tool_use_id\":\"t1\",\"content\":$(jq -Rs . </tmp/work.log),\"is_error\":$([ $GIT_EXIT -eq 0 ] && echo false || echo true)}]}}"
  step "checking the acceptance criteria" 2 3
  # Check off the first criterion, so the run summary has a real checked box.
  CRIT=$(grep -o 'E2E-CRITERION: [^ ]*' "$PROMPT" | head -1 | sed 's/E2E-CRITERION: //')
  if [ -n "$CRIT" ]; then
    mcp_call check_criterion "{\"criterion_id\":\"$CRIT\",\"met\":true,\"note\":\"covered by src/idempotency.ts replay()\"}" >/dev/null 2>&1
  fi
  step "done" 3 3
  finish "Added idempotency keys and pushed the branch."
  ;;

reviewer)
  step "reading the pull request diff" 1 2
  git fetch -q origin "$BRANCH" >/tmp/fetch.log 2>&1
  DIFF=$(git diff --stat HEAD FETCH_HEAD 2>/dev/null | tail -5)
  say "Reviewing PR #$PR on $BRANCH. Diff:
$DIFF"
  if [ "$REVIEW" = comment ]; then
    ARGS='{"event":"comment","summary":"Second pass: the retry path reads better.","findings":[{"severity":"nit","title":"Prefer a named constant for the 24h TTL","file":"src/idempotency.ts","line":6}]}'
  else
    ARGS='{"event":"request_changes","summary":"Two problems worth fixing before this merges.","findings":[{"severity":"blocker","title":"Replays are not persisted across restarts","detail":"The Map is in-process; a restart loses every key and the next webhook double-charges.","file":"src/idempotency.ts","line":3},{"severity":"minor","title":"Expiry is checked lazily","detail":"Nothing sweeps expired keys, so the map grows without bound.","file":"src/idempotency.ts","line":6},{"severity":"nit","title":"Magic number 86400000","file":"src/idempotency.ts","line":6}]}'
  fi
  emit "{\"type\":\"assistant\",\"message\":{\"id\":\"m9\",\"role\":\"assistant\",\"content\":[{\"type\":\"tool_use\",\"id\":\"t2\",\"name\":\"mcp__lexicode__submit_review\",\"input\":$ARGS}]}}"
  # No pr_number: the tool resolves it from the event that spawned this run.
  RESULT=$(mcp_call submit_review "$ARGS")
  emit "{\"type\":\"user\",\"message\":{\"role\":\"user\",\"content\":[{\"type\":\"tool_result\",\"tool_use_id\":\"t2\",\"content\":$(printf '%s' "$RESULT" | jq -Rs .)}]}}"
  step "review submitted" 2 2
  finish "Submitted a $REVIEW review on PR #$PR."
  ;;

dev-address|dev-cifix)
  step "checking out $BRANCH" 1 3
  {
    git fetch origin "$BRANCH"
    git checkout -q -f -B "$BRANCH" FETCH_HEAD
  } >/tmp/checkout.log 2>&1
  if [ "$ROLE" = dev-cifix ]; then
    NOTE="fix: repair the failing check"
    printf '\n// CI fix: guard against an undefined key.\n' >> src/idempotency.ts
  else
    NOTE="fix: persist idempotency keys and sweep expired ones"
    printf '\nconst TTL_MS = 86400000;\n// TODO: back this with the charges table.\n' >> src/idempotency.ts
  fi
  {
    git add -A
    git commit -q -m "$NOTE"
    git push -q origin "HEAD:$BRANCH"
  } >>/tmp/checkout.log 2>&1
  GIT_EXIT=$?
  emit "{\"type\":\"user\",\"message\":{\"role\":\"user\",\"content\":[{\"type\":\"tool_result\",\"tool_use_id\":\"t3\",\"content\":$(jq -Rs . </tmp/checkout.log),\"is_error\":$([ $GIT_EXIT -eq 0 ] && echo false || echo true)}]}}"
  step "pushed to $BRANCH" 2 3
  if [ "$ROLE" = dev-address ]; then
    ARGS="{\"pr_number\":$PR,\"event\":\"comment\",\"summary\":\"Addressed the review: TTL is a named constant and the sweep is noted.\",\"findings\":[{\"severity\":\"nit\",\"title\":\"Persistence is still a TODO\",\"detail\":\"Tracked separately; the in-process map stays for now.\",\"file\":\"src/idempotency.ts\"}]}"
    emit "{\"type\":\"assistant\",\"message\":{\"id\":\"m11\",\"role\":\"assistant\",\"content\":[{\"type\":\"tool_use\",\"id\":\"t4\",\"name\":\"mcp__lexicode__submit_review\",\"input\":$ARGS}]}}"
    RESULT=$(mcp_call submit_review "$ARGS")
    emit "{\"type\":\"user\",\"message\":{\"role\":\"user\",\"content\":[{\"type\":\"tool_result\",\"tool_use_id\":\"t4\",\"content\":$(printf '%s' "$RESULT" | jq -Rs .)}]}}"
  fi
  step "done" 3 3
  finish "Pushed $NOTE to $BRANCH."
  ;;

*)
  say "unknown role $ROLE"
  finish "nothing to do"
  ;;
esac
`
