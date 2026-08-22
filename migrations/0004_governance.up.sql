-- S37 governance surfaces.
--
-- pr_size_warning_lines: the diff-size warning threshold (brief §7, review-bottleneck row).
-- Same inheritance shape as the other governance settings: a workspace default every project
-- follows, and a nullable per-project override (null = inherit). The unit is total changed
-- lines (additions + deletions) on an agent PR; a PR above the effective threshold renders a
-- warning chip. 0 disables the warning.
ALTER TABLE workspace_settings ADD COLUMN pr_size_warning_lines INTEGER NOT NULL DEFAULT 800;
ALTER TABLE projects ADD COLUMN pr_size_warning_lines INTEGER;

-- poll_pr_state grows the PR's size counters. The poller's detail read (architecture §7)
-- already fetches additions/deletions for the trigger payload; storing them here lets run
-- outputs (kind = pull_request, ref = the PR number) join against live sizes instead of a
-- stale snapshot taken at output creation. NULL = unknown (detail read has not happened yet);
-- rows written before this migration stay NULL until the PR next changes.
ALTER TABLE poll_pr_state ADD COLUMN additions INTEGER;
ALTER TABLE poll_pr_state ADD COLUMN deletions INTEGER;

-- Per-step cache-read tokens (UI spec §7: the per-step cost hover's input/output/cache split).
-- The runtime reports cache reads per API message; S23 stored them only on the run rollup.
-- Reasoning tokens are deliberately absent: the Claude Code stream does not report them
-- separately (they are part of output_tokens), so no column pretends otherwise.
ALTER TABLE activities ADD COLUMN tokens_cache_read INTEGER NOT NULL DEFAULT 0;
