# Data Model

SQLite, WAL (D-2). IDs are ULIDs stored as `TEXT` unless noted. Timestamps are `TEXT` RFC3339 UTC.
`JSON` columns are `TEXT` with a `json_valid` CHECK and are read/written through typed Go structs.

Conventions: every table has `created_at`; mutable tables have `updated_at`. Deletes are `RESTRICT`
by default — cascading deletes across a graph this connected destroy audit history. Soft-delete
(`archived_at`) is used where the product needs disappearance without data loss.

---

## 1. Identity and workspace

```sql
CREATE TABLE users (
  id TEXT PRIMARY KEY,
  email TEXT NOT NULL UNIQUE,
  display_name TEXT NOT NULL,
  password_hash TEXT NOT NULL,              -- argon2id
  role TEXT NOT NULL CHECK (role IN ('owner','member')),
  avatar_color TEXT NOT NULL,
  prefs JSON NOT NULL DEFAULT '{}',         -- rail collapsed, density, theme, default verbosity
  archived_at TEXT,
  created_at TEXT NOT NULL
);

CREATE TABLE sessions (
  id TEXT PRIMARY KEY,                      -- opaque, hashed before storage
  user_id TEXT NOT NULL REFERENCES users(id),
  expires_at TEXT NOT NULL,
  user_agent TEXT,
  created_at TEXT NOT NULL
);

CREATE TABLE invites (
  id TEXT PRIMARY KEY, token_hash TEXT NOT NULL UNIQUE,
  role TEXT NOT NULL, created_by TEXT NOT NULL REFERENCES users(id),
  expires_at TEXT NOT NULL, redeemed_by TEXT REFERENCES users(id), created_at TEXT NOT NULL
);

CREATE TABLE workspace_settings (            -- single row, id=1. Source of "inherited from workspace"
  id INTEGER PRIMARY KEY CHECK (id = 1),
  default_branch TEXT NOT NULL DEFAULT 'main',
  default_branch_template TEXT NOT NULL DEFAULT '{agent}/{ticket-key}-{slug}',
  default_network_policy TEXT NOT NULL DEFAULT 'allowlist',
  default_daily_budget_cents INTEGER NOT NULL DEFAULT 2000,
  default_context_threshold_tokens INTEGER NOT NULL DEFAULT 4000,
  default_verification_days INTEGER NOT NULL DEFAULT 90,
  max_concurrent_containers INTEGER NOT NULL DEFAULT 6,
  poll_interval_seconds INTEGER NOT NULL DEFAULT 30,
  updated_at TEXT NOT NULL
);
```

**Settings inheritance.** Project-level settings columns are **nullable**, and null means "inherit".
The UI's *"Inherited from workspace: `main`. Override."* / *"Reset to workspace default"* pattern
(UI spec §5.11) is a direct read of null-ness. Never copy a default into a project row at creation
time — that silently freezes the value and breaks the pattern.

---

## 2. Projects and repositories

```sql
CREATE TABLE projects (
  id TEXT PRIMARY KEY,
  key TEXT NOT NULL UNIQUE,                 -- 'PAY'; drives ticket keys; [A-Z][A-Z0-9]{1,9}
  name TEXT NOT NULL,
  description TEXT NOT NULL DEFAULT '',
  color TEXT NOT NULL,
  owner_id TEXT NOT NULL REFERENCES users(id),
  agent_guidance TEXT NOT NULL DEFAULT '',  -- project-wide prompt preamble
  daily_budget_cents INTEGER,               -- null = inherit
  context_threshold_tokens INTEGER,
  verification_days INTEGER,
  ticket_seq INTEGER NOT NULL DEFAULT 0,    -- allocator for PAY-14
  archived_at TEXT,
  created_at TEXT NOT NULL, updated_at TEXT NOT NULL
);

CREATE TABLE project_members (
  project_id TEXT NOT NULL REFERENCES projects(id),
  user_id TEXT NOT NULL REFERENCES users(id),
  PRIMARY KEY (project_id, user_id)
);

CREATE TABLE repos (
  project_id TEXT PRIMARY KEY REFERENCES projects(id),
  provider TEXT NOT NULL DEFAULT 'github',  -- ForgeProvider ID
  owner TEXT NOT NULL, name TEXT NOT NULL,  -- 'acme','payments'
  default_branch TEXT,                      -- null = inherit
  branch_template TEXT,
  setup_script TEXT NOT NULL DEFAULT '',
  image_ref TEXT,                           -- null = built-in image (D-7)
  network_policy TEXT,                      -- none|allowlist|open, null = inherit
  network_allowlist JSON NOT NULL DEFAULT '[]',
  token_secret_id TEXT REFERENCES secrets(id),
  connected_at TEXT, last_synced_at TEXT,
  head_sha TEXT, head_message TEXT,         -- for the Overview About card
  created_at TEXT NOT NULL, updated_at TEXT NOT NULL
);

CREATE TABLE secrets (                       -- D-16; values never readable through the API
  id TEXT PRIMARY KEY,
  scope TEXT NOT NULL CHECK (scope IN ('workspace','project')),
  project_id TEXT REFERENCES projects(id),
  name TEXT NOT NULL,                        -- env var name for project secrets
  ciphertext BLOB NOT NULL, nonce BLOB NOT NULL,
  created_by TEXT NOT NULL REFERENCES users(id),
  created_at TEXT NOT NULL, updated_at TEXT NOT NULL,
  UNIQUE (scope, project_id, name)
);
```

---

## 3. Agents

```sql
CREATE TABLE agents (
  id TEXT PRIMARY KEY,
  project_id TEXT NOT NULL REFERENCES projects(id),
  name TEXT NOT NULL, role TEXT NOT NULL DEFAULT '', color TEXT NOT NULL,
  runtime_id TEXT NOT NULL DEFAULT 'claude-code',   -- AgentRuntime port ID
  model TEXT NOT NULL, effort TEXT NOT NULL DEFAULT 'medium',
  autonomy TEXT NOT NULL CHECK (autonomy IN ('suggest','approve_each','auto_gates','auto')),
  permissions JSON NOT NULL,                 -- see §3.1
  git_author_name TEXT NOT NULL, git_author_email TEXT NOT NULL,
  forge_login TEXT,                          -- display only in V1 (D-9)
  forge_token_secret_id TEXT REFERENCES secrets(id),  -- nullable: per-agent bot account, later
  concurrency_cap INTEGER NOT NULL DEFAULT 1,
  daily_cap_cents INTEGER,
  max_wall_clock_seconds INTEGER NOT NULL DEFAULT 3600,
  max_steps INTEGER NOT NULL DEFAULT 200,
  enabled INTEGER NOT NULL DEFAULT 1,
  directive_version_id TEXT REFERENCES agent_directives(id),  -- current
  archived_at TEXT, created_at TEXT NOT NULL, updated_at TEXT NOT NULL,
  UNIQUE (project_id, name)
);

CREATE TABLE agent_directives (              -- append-only; diff view reads two rows
  id TEXT PRIMARY KEY,
  agent_id TEXT NOT NULL REFERENCES agents(id),
  version INTEGER NOT NULL,
  body TEXT NOT NULL, token_estimate INTEGER NOT NULL,
  author_id TEXT REFERENCES users(id), note TEXT NOT NULL DEFAULT '',
  created_at TEXT NOT NULL,
  UNIQUE (agent_id, version)
);

CREATE TABLE agent_permission_rules (        -- what "always allow" writes (interaction rule 8)
  id TEXT PRIMARY KEY,
  agent_id TEXT NOT NULL REFERENCES agents(id),
  tool TEXT NOT NULL,                        -- 'Bash'
  pattern TEXT NOT NULL,                     -- 'npm test:*'
  decision TEXT NOT NULL CHECK (decision IN ('allow','deny','ask')),
  created_from_run_id TEXT REFERENCES runs(id),
  created_by TEXT REFERENCES users(id),
  created_at TEXT NOT NULL
);
```

### 3.1 `agents.permissions`

```json
{
  "read_files": true, "edit_files": true, "run_commands": true,
  "push_branches": true, "open_prs": true, "comment_prs": true,
  "submit_reviews": false, "create_wiki_pages": true
}
```

These are **enforcement, not guidance** (brief D7). They compile to: Claude Code allow/deny rules in
the container's `.claude/settings.json` (read/edit/run), and hard refusals in the forge adapter
(push/PR/comment/review) and the wiki service. An agent with `submit_reviews:false` calling the
review endpoint gets a denial the agent can see, not a silent no-op.

`merge` is deliberately absent from this object. There is no merge permission (brief D6).

---

## 4. Board and tickets

```sql
CREATE TABLE columns (
  id TEXT PRIMARY KEY,
  project_id TEXT NOT NULL REFERENCES projects(id),
  name TEXT NOT NULL,                         -- user-renameable
  category TEXT NOT NULL CHECK (category IN
    ('backlog','ready','running','review','done','canceled')),   -- automation keys off THIS (D2)
  position INTEGER NOT NULL,
  wip_limit INTEGER,                          -- enforcing on category='running'
  auto_start_delegate INTEGER NOT NULL DEFAULT 0,
  created_at TEXT NOT NULL, updated_at TEXT NOT NULL
);

CREATE TABLE tickets (
  id TEXT PRIMARY KEY,
  project_id TEXT NOT NULL REFERENCES projects(id),
  seq INTEGER NOT NULL, key TEXT NOT NULL,    -- 'PAY-14', generated from projects.ticket_seq
  title TEXT NOT NULL, description TEXT NOT NULL DEFAULT '',
  column_id TEXT NOT NULL REFERENCES columns(id),
  position REAL NOT NULL,                     -- fractional ranking for drag ordering
  priority TEXT NOT NULL CHECK (priority IN ('none','low','medium','high','urgent')),
  assignee_id TEXT REFERENCES users(id),      -- human, accountable      (D1)
  delegate_agent_id TEXT REFERENCES agents(id), -- agent, doing the work (D1)
  parent_id TEXT REFERENCES tickets(id),      -- exactly one level; CHECK enforced in service
  pr_number INTEGER, pr_state TEXT, pr_checks TEXT, pr_additions INTEGER, pr_deletions INTEGER,
  branch TEXT,
  origin TEXT NOT NULL DEFAULT 'human'
    CHECK (origin IN ('human','agent','trigger','import')),
  created_by_user_id TEXT REFERENCES users(id),
  created_by_agent_id TEXT REFERENCES agents(id),
  archived_at TEXT,
  created_at TEXT NOT NULL, updated_at TEXT NOT NULL,
  UNIQUE (project_id, seq)
);

CREATE TABLE acceptance_criteria (
  id TEXT PRIMARY KEY,
  ticket_id TEXT NOT NULL REFERENCES tickets(id),
  position INTEGER NOT NULL, text TEXT NOT NULL,
  checked INTEGER NOT NULL DEFAULT 0,
  checked_by_run_id TEXT REFERENCES runs(id),
  checked_by_user_id TEXT REFERENCES users(id),
  note TEXT NOT NULL DEFAULT '',
  updated_at TEXT NOT NULL
);

CREATE TABLE labels (
  id TEXT PRIMARY KEY, project_id TEXT NOT NULL REFERENCES projects(id),
  name TEXT NOT NULL, color TEXT NOT NULL, UNIQUE (project_id, name)
);
CREATE TABLE ticket_labels (
  ticket_id TEXT NOT NULL REFERENCES tickets(id),
  label_id TEXT NOT NULL REFERENCES labels(id),
  PRIMARY KEY (ticket_id, label_id)
);

CREATE TABLE triage_items (
  id TEXT PRIMARY KEY,
  ticket_id TEXT NOT NULL UNIQUE REFERENCES tickets(id),
  provenance TEXT NOT NULL,                   -- "Created by trigger 'CI failed' from run #482"
  source_trigger_id TEXT REFERENCES triggers(id),
  source_run_id TEXT REFERENCES runs(id),
  state TEXT NOT NULL CHECK (state IN ('pending','accepted','duplicate','declined','snoozed')),
  duplicate_of TEXT REFERENCES tickets(id),
  reason TEXT NOT NULL DEFAULT '',
  snooze_until TEXT,                          -- null + state='snoozed' = "until new activity"
  resolved_by TEXT REFERENCES users(id), resolved_at TEXT,
  created_at TEXT NOT NULL
);
```

**A ticket is on the board iff it has no pending triage item.** That single rule is what makes
automated ticket creation safe (brief §6.4) and it is enforced in the board query, not in six
callers.

### 4.1 The unified ticket stream

One table, so the ticket detail is one chronological query and there is no Comments/Activity tab
split to accidentally reintroduce (UI spec §5.4).

```sql
CREATE TABLE ticket_stream (
  id TEXT PRIMARY KEY,
  ticket_id TEXT NOT NULL REFERENCES tickets(id),
  kind TEXT NOT NULL CHECK (kind IN
    ('comment','status_change','field_change','run','pr_event','trigger_fired','proposal')),
  actor_kind TEXT NOT NULL CHECK (actor_kind IN ('human','agent','trigger','system')),
  actor_id TEXT,
  body TEXT NOT NULL DEFAULT '',              -- markdown for comments
  payload JSON NOT NULL DEFAULT '{}',
  run_id TEXT REFERENCES runs(id),            -- kind='run' → renders RunSessionCard
  edited_at TEXT, created_at TEXT NOT NULL
);
CREATE INDEX ix_stream_ticket ON ticket_stream(ticket_id, created_at);
```

---

## 5. Wiki

```sql
CREATE TABLE wiki_pages (
  id TEXT PRIMARY KEY,
  project_id TEXT NOT NULL REFERENCES projects(id),
  slug TEXT NOT NULL, title TEXT NOT NULL,
  parent_id TEXT REFERENCES wiki_pages(id),   -- max one level; service enforces parent.parent IS NULL
  position REAL NOT NULL,
  owner_id TEXT REFERENCES users(id),
  verified_until TEXT,
  agent_scope TEXT NOT NULL DEFAULT 'auto'
    CHECK (agent_scope IN ('always','auto','paths','manual','never')),
  scope_paths JSON NOT NULL DEFAULT '[]',     -- globs, used when agent_scope='paths'
  tags JSON NOT NULL DEFAULT '[]',
  body TEXT NOT NULL DEFAULT '',
  token_estimate INTEGER NOT NULL DEFAULT 0,
  state TEXT NOT NULL DEFAULT 'live' CHECK (state IN ('live','proposed')),
  proposed_by_run_id TEXT REFERENCES runs(id),
  proposed_base_version INTEGER,              -- set when the proposal edits an existing page
  proposal_target_id TEXT REFERENCES wiki_pages(id),
  proposed_reason TEXT,                       -- the agent's own words (S35 review banner; migration 0003)
  imported_from TEXT,                         -- 'AGENTS.md' etc (D-11)
  demoted_at TEXT, demoted_from TEXT,         -- verified_until enforcement leaves a trace
  archived_at TEXT, created_at TEXT NOT NULL, updated_at TEXT NOT NULL,
  UNIQUE (project_id, slug)
);

CREATE TABLE wiki_versions (
  id TEXT PRIMARY KEY, page_id TEXT NOT NULL REFERENCES wiki_pages(id),
  version INTEGER NOT NULL, title TEXT NOT NULL, body TEXT NOT NULL,
  front_matter JSON NOT NULL,
  author_user_id TEXT REFERENCES users(id), author_run_id TEXT REFERENCES runs(id),
  created_at TEXT NOT NULL, UNIQUE (page_id, version)
);

CREATE TABLE mentions (                        -- powers backlinks with containing paragraph
  id TEXT PRIMARY KEY,
  project_id TEXT NOT NULL REFERENCES projects(id),
  from_kind TEXT NOT NULL CHECK (from_kind IN ('wiki','ticket','comment')),
  from_id TEXT NOT NULL,
  to_kind TEXT NOT NULL CHECK (to_kind IN ('wiki','ticket','agent','user')),
  to_id TEXT NOT NULL,
  linked INTEGER NOT NULL DEFAULT 1,           -- 0 = unlinked mention, one-click linkable
  context_text TEXT NOT NULL                   -- the full containing paragraph
);

CREATE VIRTUAL TABLE wiki_fts USING fts5(title, body, tags, content='wiki_pages', content_rowid='rowid');
```

Search is FTS5 and is the primary wiki navigation (UI spec §5.6); the tree is secondary.

---

## 6. Triggers

```sql
CREATE TABLE triggers (
  id TEXT PRIMARY KEY,
  project_id TEXT NOT NULL REFERENCES projects(id),
  name TEXT NOT NULL, enabled INTEGER NOT NULL DEFAULT 0,   -- suggested triggers ship OFF (brief §6.3)
  source_id TEXT NOT NULL DEFAULT 'github.poll',            -- EventSource port ID
  event TEXT NOT NULL,                                      -- 'pull_request'
  activity_types JSON NOT NULL DEFAULT '[]',                -- ['opened','synchronize']
  filters JSON NOT NULL DEFAULT '{}',                       -- {branches:[], paths:[], labels:[]}
  conditions JSON NOT NULL DEFAULT '{"all":[]}',            -- condition tree
  actions JSON NOT NULL,                                    -- ordered [{action_id, params}]
  loop_config JSON NOT NULL,                                -- see §6.1
  cron TEXT,                                                -- when event='schedule'
  created_by TEXT REFERENCES users(id),
  created_at TEXT NOT NULL, updated_at TEXT NOT NULL
);

CREATE TABLE trigger_firings (
  id TEXT PRIMARY KEY,
  trigger_id TEXT NOT NULL REFERENCES triggers(id),
  event_id TEXT NOT NULL REFERENCES events(id),
  outcome TEXT NOT NULL CHECK (outcome IN
    ('succeeded','no_action','awaiting_approval','errored',
     'debounced','superseded','loop_stopped','budget_exceeded')),
  reason TEXT NOT NULL DEFAULT '',            -- 'actor suppressed' | 'conditions not met' | ...
  run_id TEXT REFERENCES runs(id),
  absorbed_by_run_id TEXT REFERENCES runs(id),
  warnings JSON NOT NULL DEFAULT '[]',
  created_at TEXT NOT NULL,
  UNIQUE (trigger_id, event_id)               -- idempotency for bus re-dispatch
);
CREATE INDEX ix_firings_health ON trigger_firings(trigger_id, created_at DESC);
```

The rule-health sparkline and the `14 ok · 3 no action · 1 loop` breakdown are one `GROUP BY outcome`
over the last N firings. Outcome is never collapsed to success/failure anywhere in the stack.

### 6.1 `triggers.loop_config`

```json
{
  "actor_suppression": true,
  "debounce_seconds": 90,
  "cancel_in_progress": true,
  "depth_limit": 3,
  "daily_budget_cents": null
}
```

`null` on the budget means inherit the project ceiling. Each field maps to exactly one row in the
always-visible loop-protection panel (UI spec §5.9).

---

## 7. Events

```sql
CREATE TABLE events (
  id TEXT PRIMARY KEY,
  project_id TEXT REFERENCES projects(id),
  source TEXT NOT NULL,                        -- EventSource ID or 'internal'
  kind TEXT NOT NULL, activity_type TEXT NOT NULL DEFAULT '',
  actor_kind TEXT NOT NULL, actor_id TEXT, actor_login TEXT, actor_email TEXT,
  subject_kind TEXT NOT NULL, subject_id TEXT, subject_number INTEGER, subject_branch TEXT,
  payload JSON NOT NULL,                       -- NORMALIZED shape (contracts §4)
  cause_run_id TEXT REFERENCES runs(id),       -- causality edge
  dedupe_key TEXT NOT NULL UNIQUE,
  dispatch_state TEXT NOT NULL DEFAULT 'pending'
    CHECK (dispatch_state IN ('pending','done','failed')),
  occurred_at TEXT NOT NULL, created_at TEXT NOT NULL
);
CREATE INDEX ix_events_backtest ON events(project_id, kind, occurred_at DESC);
CREATE INDEX ix_events_subject ON events(project_id, subject_kind, subject_number);

CREATE TABLE poll_cursors (
  project_id TEXT NOT NULL REFERENCES projects(id),
  resource TEXT NOT NULL,                      -- 'pulls','reviews','issue_comments',...
  cursor TEXT NOT NULL DEFAULT '', etag TEXT NOT NULL DEFAULT '',
  baseline_done INTEGER NOT NULL DEFAULT 0,
  last_polled_at TEXT,
  PRIMARY KEY (project_id, resource)
);

CREATE TABLE poll_pr_state (                   -- the diff basis for deriving activity types
  project_id TEXT NOT NULL REFERENCES projects(id),
  number INTEGER NOT NULL,
  head_sha TEXT NOT NULL, state TEXT NOT NULL, draft INTEGER NOT NULL,
  updated_at TEXT NOT NULL, review_cursor TEXT NOT NULL DEFAULT '',
  PRIMARY KEY (project_id, number)
);
```

---

## 8. Runs

```sql
CREATE TABLE runs (
  id TEXT PRIMARY KEY,
  seq INTEGER NOT NULL,                        -- per-project, renders as "run #482"
  project_id TEXT NOT NULL REFERENCES projects(id),
  agent_id TEXT NOT NULL REFERENCES agents(id),
  ticket_id TEXT REFERENCES tickets(id),       -- NULLABLE (D-15: free-floating runs exist)
  trigger_id TEXT REFERENCES triggers(id),
  cause_event_id TEXT REFERENCES events(id),
  parent_run_id TEXT REFERENCES runs(id),
  requested_by_user_id TEXT REFERENCES users(id),   -- the delegating human; notification target
  state TEXT NOT NULL CHECK (state IN
    ('queued','provisioning','running','needs_input','awaiting_approval',
     'completed','failed','timed_out','canceled','loop_stopped')),
  state_reason TEXT NOT NULL DEFAULT '',       -- 'superseded' | 'ticket archived' | 'takeover' | ...
  hold_reason TEXT NOT NULL DEFAULT '',        -- why a queued run is queued, in words
  autonomy TEXT NOT NULL,                      -- snapshot at launch; echoed on the run header
  directive_version_id TEXT REFERENCES agent_directives(id),  -- snapshot: what actually ran
  model TEXT NOT NULL, effort TEXT NOT NULL,
  prompt TEXT NOT NULL,                        -- the fully rendered prompt, kept for reproducibility
  runtime_id TEXT NOT NULL, sandbox_id TEXT NOT NULL,
  container_id TEXT, instance_id TEXT, log_offset INTEGER NOT NULL DEFAULT 0,
  branch TEXT, base_sha TEXT,
  depth INTEGER NOT NULL DEFAULT 0,
  subject_key TEXT NOT NULL DEFAULT '',        -- guard key: 'pr:219' | 'ticket:PAY-14' | 'repo'
  current_step TEXT NOT NULL DEFAULT '',
  cost_cents INTEGER NOT NULL DEFAULT 0,
  tokens_in INTEGER NOT NULL DEFAULT 0, tokens_out INTEGER NOT NULL DEFAULT 0,
  tokens_cache_read INTEGER NOT NULL DEFAULT 0, tokens_cache_write INTEGER NOT NULL DEFAULT 0,
  step_count INTEGER NOT NULL DEFAULT 0,
  error_message TEXT NOT NULL DEFAULT '',
  takeover_note TEXT NOT NULL DEFAULT '',
  queued_at TEXT NOT NULL, started_at TEXT, ended_at TEXT,
  acknowledged_at TEXT,                        -- dismisses it from needs-you surfaces
  UNIQUE (project_id, seq)
);
CREATE INDEX ix_runs_attention ON runs(project_id, state, queued_at DESC);
CREATE INDEX ix_runs_subject ON runs(project_id, subject_key, queued_at DESC);

CREATE TABLE activities (
  run_id TEXT NOT NULL REFERENCES runs(id),
  seq INTEGER NOT NULL,
  type TEXT NOT NULL CHECK (type IN
    ('thought','action','elicitation','response','error','system','provision')),
  level INTEGER NOT NULL DEFAULT 1,            -- 0 summary · 1 normal · 2 verbose
  tool_name TEXT NOT NULL DEFAULT '',          -- drives tool-aware rendering + grouping
  group_key TEXT NOT NULL DEFAULT '',          -- consecutive equal keys collapse in the UI
  title TEXT NOT NULL DEFAULT '',              -- one-line render, always present
  payload JSON NOT NULL DEFAULT '{}',          -- typed per tool: diff hunks, argv+exit+output, ...
  ok INTEGER,                                  -- null = n/a; 0 = failed step (auto-expands)
  attempt INTEGER NOT NULL DEFAULT 1,          -- retry badge
  duration_ms INTEGER, queued_ms INTEGER, model_ms INTEGER, tool_ms INTEGER,  -- timing gutter
  cost_cents INTEGER NOT NULL DEFAULT 0,
  tokens_in INTEGER NOT NULL DEFAULT 0, tokens_out INTEGER NOT NULL DEFAULT 0,
  created_at TEXT NOT NULL,
  PRIMARY KEY (run_id, seq)
);

CREATE TABLE elicitations (
  id TEXT PRIMARY KEY,
  run_id TEXT NOT NULL REFERENCES runs(id),
  activity_seq INTEGER NOT NULL,
  kind TEXT NOT NULL CHECK (kind IN ('question','approval')),
  request JSON NOT NULL,                       -- question+options, or tool+input+scope+impact+reason
  state TEXT NOT NULL CHECK (state IN ('pending','answered','denied','expired','canceled')),
  response JSON,                               -- {choice|text} or {behavior, updated_input, remember}
  responded_by TEXT REFERENCES users(id),
  responded_at TEXT, created_at TEXT NOT NULL
);

CREATE TABLE run_outputs (
  id TEXT PRIMARY KEY, run_id TEXT NOT NULL REFERENCES runs(id),
  kind TEXT NOT NULL CHECK (kind IN
    ('branch','pull_request','comment','review','wiki_proposal','ticket','partial_work')),
  ref TEXT NOT NULL, url TEXT NOT NULL DEFAULT '', summary TEXT NOT NULL DEFAULT '',
  created_at TEXT NOT NULL
);

CREATE TABLE run_context_items (               -- the Context panel; also the agent preview
  id TEXT PRIMARY KEY, run_id TEXT NOT NULL REFERENCES runs(id),
  provider TEXT NOT NULL,                      -- ContextProvider ID
  source_kind TEXT NOT NULL,                   -- 'wiki'|'repo_file'|'project'|'ticket'
  source_ref TEXT NOT NULL,                    -- slug or path
  title TEXT NOT NULL, reason TEXT NOT NULL,   -- 'always' | 'matched path infra/**' | 'repo file'
  tokens INTEGER NOT NULL, position INTEGER NOT NULL, injected INTEGER NOT NULL DEFAULT 1
);

CREATE TABLE run_messages (                    -- steering queue (queue, don't interrupt)
  id TEXT PRIMARY KEY, run_id TEXT NOT NULL REFERENCES runs(id),
  body TEXT NOT NULL, author_id TEXT REFERENCES users(id),
  state TEXT NOT NULL CHECK (state IN ('queued','delivered','dropped')),
  created_at TEXT NOT NULL, delivered_at TEXT
);
```

---

## 9. Governance, attention, audit

```sql
CREATE TABLE budget_ledger (                   -- fast admission checks without scanning runs
  day TEXT NOT NULL,                           -- 'YYYY-MM-DD' UTC
  project_id TEXT NOT NULL REFERENCES projects(id),
  agent_id TEXT REFERENCES agents(id),
  trigger_id TEXT REFERENCES triggers(id),
  cents INTEGER NOT NULL DEFAULT 0,
  PRIMARY KEY (day, project_id, agent_id, trigger_id)
);

CREATE TABLE notifications (
  id TEXT PRIMARY KEY,
  user_id TEXT NOT NULL REFERENCES users(id),
  project_id TEXT NOT NULL REFERENCES projects(id),
  run_id TEXT REFERENCES runs(id),
  flavor TEXT NOT NULL CHECK (flavor IN ('question','approval','review','failure')),
  title TEXT NOT NULL, body TEXT NOT NULL DEFAULT '',
  state TEXT NOT NULL CHECK (state IN ('unread','read','dismissed')),
  pushed INTEGER NOT NULL DEFAULT 0,
  created_at TEXT NOT NULL, updated_at TEXT NOT NULL,
  UNIQUE (user_id, run_id)                     -- schema-level "never stack" (interaction rule 3)
);

CREATE TABLE audit_log (
  id TEXT PRIMARY KEY,
  project_id TEXT REFERENCES projects(id),
  actor_kind TEXT NOT NULL CHECK (actor_kind IN ('human','agent','trigger','system')),
  actor_id TEXT,
  action TEXT NOT NULL,                        -- 'ticket.move' | 'agent.directive.update' | ...
  target_kind TEXT NOT NULL, target_id TEXT NOT NULL,
  before JSON, after JSON, note TEXT NOT NULL DEFAULT '',
  created_at TEXT NOT NULL
);

CREATE TABLE saved_views (
  id TEXT PRIMARY KEY, user_id TEXT REFERENCES users(id),
  project_id TEXT REFERENCES projects(id),
  surface TEXT NOT NULL,                       -- 'runs'|'board'|'inbox'
  name TEXT NOT NULL, filters JSON NOT NULL, is_default INTEGER NOT NULL DEFAULT 0
);
```

---

## 10. Invariants the schema alone does not enforce

Each of these is a service-layer assertion with a unit test. They are listed here because they are
the rules most likely to be broken by a well-meaning change.

1. **Sub-tickets are one level.** `parent.parent_id IS NULL` on every insert.
2. **A ticket's column belongs to the ticket's project.**
3. **`categories` are not unique per project** (two `review` columns are legal), but at least one
   `backlog`, one `running` and one `done` column must exist; deleting the last of a category is
   refused with a named error.
4. **Only the scheduler writes `runs.state`.** Enforced by keeping the update method unexported
   outside `kernel/sched`.
5. **No agent may merge.** The forge adapter has no merge method at all — the capability is absent,
   not gated (brief D6).
6. **A run's directive and prompt are snapshots.** Editing an agent never mutates a past run's
   record of what it was told.
7. **Triage-pending tickets are invisible to the board** and to `move_ticket` actions.
8. **An `always` wiki page past `verified_until` is demoted before the next run resolves context.**
9. **Secret values leave the process only into a container env.** No API path returns `ciphertext`
   or plaintext; a test asserts no handler references the field.
