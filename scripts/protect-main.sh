#!/usr/bin/env bash
# Apply .github/rulesets/main.json, so a pull request cannot merge into the default branch until
# the `all checks passed` job in .github/workflows/check.yml is green (ticket LEXI-2).
#
# Branch protection is repository *state*, not repository content — GitHub never reads a ruleset
# out of a branch, so committing the JSON is not enough on its own. This script is how the
# committed JSON gets applied, and re-applied when it changes.
#
# Requirements: the `gh` CLI, authenticated as someone with admin on the repository.
#
#   scripts/protect-main.sh              # the repo the working copy points at
#   scripts/protect-main.sh owner/name   # some other repo
set -euo pipefail
cd "$(dirname "$0")/.."

repo="${1:-$(gh repo view --json nameWithOwner -q .nameWithOwner)}"
ruleset=.github/rulesets/main.json
name="$(jq -r .name "$ruleset")"

id="$(gh api "repos/$repo/rulesets" --jq ".[] | select(.name == \"$name\") | .id" | head -n1)"

if [[ -n "$id" ]]; then
	echo "updating ruleset '$name' (id $id) on $repo"
	gh api --method PUT "repos/$repo/rulesets/$id" --input "$ruleset" >/dev/null
else
	echo "creating ruleset '$name' on $repo"
	gh api --method POST "repos/$repo/rulesets" --input "$ruleset" >/dev/null
fi

echo "done — merges into the default branch of $repo now require 'all checks passed'"
