#!/usr/bin/env bash
# Apply .github/rulesets/require-tests-to-merge.json to the repository, so a pull request
# cannot be merged into the default branch until the `tests passed` gate of the `check`
# workflow is green. A committed workflow can run the tests; only a repository ruleset can
# block the merge button, and that lives in settings rather than in the tree — so this
# script is the enforcement, and it is idempotent: it updates the ruleset if it exists.
#
# Needs the `gh` CLI, authenticated with admin rights on the repository.
#
#   scripts/setup-branch-protection.sh [owner/repo]
set -euo pipefail
cd "$(dirname "$0")/.."

RULESET=.github/rulesets/require-tests-to-merge.json
NAME="require tests to merge"   # must match the "name" in $RULESET

repo="${1:-$(gh repo view --json nameWithOwner -q .nameWithOwner)}"

id=$(gh api "repos/$repo/rulesets" --jq ".[] | select(.name == \"$NAME\") | .id" | head -n 1)

if [ -n "$id" ]; then
  echo "updating ruleset '$NAME' ($id) on $repo"
  gh api --method PUT "repos/$repo/rulesets/$id" --input "$RULESET" >/dev/null
else
  echo "creating ruleset '$NAME' on $repo"
  gh api --method POST "repos/$repo/rulesets" --input "$RULESET" >/dev/null
fi

echo "done — '$NAME' is active on the default branch of $repo"
