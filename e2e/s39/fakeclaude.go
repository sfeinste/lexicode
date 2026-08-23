package main

// fakeClaude is the scripted stand-in agent baked into the derived image at
// /usr/local/bin/claude. It honours the contracts §3.1 shape from the outside — consumes the
// prompt as the first stdin message, emits a stream-json session on stdout — and does REAL
// work through the real seams: real git in a real workspace, and the real Lexicode MCP server
// over HTTP using the run token from /workspace/.lexicode/mcp.json.
//
// It never pushes and never fetches. It cannot: `origin` is tokenless from the moment the
// clone finishes, and the fake GitHub's git endpoints demand the repository token. Branches
// it needs are already in the workspace as remote-tracking refs, fetched by the clone step
// while the credential was still live; branches it produces reach the remote through the
// orchestrator's teardown push.
//
// Which of the four roles it plays is read out of /workspace/.lexicode/prompt.md, because
// that is where a trigger's prompt override lands. The chain's triggers write the role, the
// pull request number and the pull request's branch into that override with {{...}}
// interpolation — the same mechanism a user types into the trigger editor.
//
//	E2E-ROLE: dev-implement   implement the ticket on the run's own branch, check off an
//	                          acceptance criterion, commit. The orchestrator pushes it and
//	                          opens the PR.
//	E2E-ROLE: reviewer        read the PR's diff and call submit_review with severity-tagged
//	                          findings. E2E-REVIEW says request_changes or comment. This role
//	                          gets NO E2E-PR and NO E2E-BRANCH: its trigger carries the
//	                          shipped bootstrap prompt (bootstrap.ReviewerPrompt) and nothing
//	                          else, so the pull request number has to come out of the prompt
//	                          the product assembled and the code under review has to already
//	                          be the workspace. Both are asserted, and a miss fails the run.
//	E2E-ROLE: dev-address     check out the PR's branch, commit a fix on it, then submit a
//	                          COMMENT review saying so — the "addressed, please re-review"
//	                          hop that carries the chain forward. The orchestrator pushes.
//	E2E-ROLE: dev-cifix       check out the PR's branch and commit a fix on it. No review:
//	                          the CI branch of the chain is not meant to bounce.
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
BASE_BRANCH=main

emit() { printf '%s\n' "$1"; }
# await_stdin_eof is what makes this fixture honest. Under --input-format stream-json the real
# CLI does not exit when it emits a result: it goes back to reading stdin for the next user
# message and exits only at EOF. The orchestrator closes stdin when the last turn ends with
# nothing queued, and that — not the script reaching its own end — is what ends this process.
# A fixture that exited by itself would let an adapter that never closes stdin pass, which is
# precisely how a completed run came to hang in the running state, with no push and no PR.
await_stdin_eof() { cat >/dev/null; }
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
  await_stdin_eof
  exit 0
}
# die ends the run as a FAILURE with the reason on it. It is how a fixture assertion — "the
# product did not give me what a real agent would need" — becomes a red acceptance with a
# readable cause, instead of a downstream count that is mysteriously off by one.
die() {
  say "FIXTURE ASSERTION FAILED: $1"
  emit "{\"type\":\"result\",\"subtype\":\"error_during_execution\",\"is_error\":true,\"num_turns\":1,\"result\":$(printf 'FIXTURE ASSERTION FAILED: %s' "$1" | jq -Rs .),\"total_cost_usd\":0.001,\"usage\":{\"input_tokens\":10,\"output_tokens\":5}}"
  await_stdin_eof
  exit 1
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
  finish "Added idempotency keys and committed them."
  ;;

reviewer)
  step "reading the pull request diff" 1 2
  # Everything this role knows about what it is reviewing comes out of the prompt the PRODUCT
  # assembled — the trigger's override is the shipped bootstrap default, and the event
  # context provider supplies the facts of the occurrence. The trigger writes no E2E-PR and
  # no E2E-BRANCH for this role, so the two assertions below fail loudly if either half
  # regresses instead of quietly reviewing the wrong thing.
  PR=$(grep -oE 'pull request #[0-9]+' "$PROMPT" | head -1 | grep -oE '[0-9]+')
  if [ -z "$PR" ]; then
    die "the prompt names no pull request: neither the event section nor the trigger's prompt override reached it. Prompt:
$(cat "$PROMPT")"
  fi
  # The workspace must BE the pull request's head. src/idempotency.ts exists only on that
  # branch; a workspace cut fresh from the default branch does not contain it, which is
  # exactly the bug that let a reviewer produce a competent review of the wrong code.
  if [ ! -f src/idempotency.ts ]; then
    die "the change under review is not in the workspace — the PR's head branch was not checked out. HEAD=$(git rev-parse --short HEAD 2>/dev/null), tracked files:
$(git ls-files | head -20)"
  fi
  DIFF=$(git diff --stat "origin/$BASE_BRANCH" HEAD 2>/dev/null | tail -5)
  say "Reviewing PR #$PR from the checked-out head. Diff against $BASE_BRANCH:
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
  # The tool call has to have SUCCEEDED, not merely been made. isError lives in the JSON-RPC
  # envelope, unescaped; the tool's own payload is escaped JSON inside a text block.
  case "$RESULT" in
    *'"isError":false'*) : ;;
    *) die "submit_review did not succeed: $RESULT" ;;
  esac
  finish "Submitted a $REVIEW review on PR #$PR."
  ;;

dev-address|dev-cifix)
  step "checking out $BRANCH" 1 3
  {
    git checkout -q -f -B "$BRANCH" "origin/$BRANCH"
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
  } >>/tmp/checkout.log 2>&1
  GIT_EXIT=$?
  emit "{\"type\":\"user\",\"message\":{\"role\":\"user\",\"content\":[{\"type\":\"tool_result\",\"tool_use_id\":\"t3\",\"content\":$(jq -Rs . </tmp/checkout.log),\"is_error\":$([ $GIT_EXIT -eq 0 ] && echo false || echo true)}]}}"
  step "committed on $BRANCH" 2 3
  if [ "$ROLE" = dev-address ]; then
    ARGS="{\"pr_number\":$PR,\"event\":\"comment\",\"summary\":\"Addressed the review: TTL is a named constant and the sweep is noted.\",\"findings\":[{\"severity\":\"nit\",\"title\":\"Persistence is still a TODO\",\"detail\":\"Tracked separately; the in-process map stays for now.\",\"file\":\"src/idempotency.ts\"}]}"
    emit "{\"type\":\"assistant\",\"message\":{\"id\":\"m11\",\"role\":\"assistant\",\"content\":[{\"type\":\"tool_use\",\"id\":\"t4\",\"name\":\"mcp__lexicode__submit_review\",\"input\":$ARGS}]}}"
    RESULT=$(mcp_call submit_review "$ARGS")
    emit "{\"type\":\"user\",\"message\":{\"role\":\"user\",\"content\":[{\"type\":\"tool_result\",\"tool_use_id\":\"t4\",\"content\":$(printf '%s' "$RESULT" | jq -Rs .)}]}}"
  fi
  step "done" 3 3
  finish "Committed $NOTE on $BRANCH."
  ;;

*)
  say "unknown role $ROLE"
  finish "nothing to do"
  ;;
esac
`
