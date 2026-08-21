-- 0001_init — the complete V1 schema, transcribed from plan/02-data-model.md.
--
-- Conventions (data model preamble): IDs are ULIDs stored as TEXT; timestamps are TEXT RFC3339
-- UTC; JSON columns are TEXT with a json_valid CHECK and are read/written through typed Go
-- structs; every table has created_at; mutable tables have updated_at; deletes are RESTRICT by
-- default; soft-delete via archived_at where the product needs disappearance without data loss.
--
-- Later stories add columns only when the model genuinely changes; they do not re-ship slices of
-- this base schema (implementation plan S03).

-- ------------------------------------------------------------------ §1 identity and workspace

CREATE TABLE users (
  id TEXT PRIMARY KEY,
  email TEXT NOT NULL UNIQUE,
  display_name TEXT NOT NULL,
  password_hash TEXT NOT NULL,              -- argon2id
  role TEXT NOT NULL CHECK (role IN ('owner','member')),
  avatar_color TEXT NOT NULL,
  prefs TEXT NOT NULL DEFAULT '{}' CHECK (json_valid(prefs)),
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
  id TEXT PRIMARY KEY,
  token_hash TEXT NOT NULL UNIQUE,
  role TEXT NOT NULL,
  created_by TEXT NOT NULL REFERENCES users(id),
  expires_at TEXT NOT NULL,
  redeemed_by TEXT REFERENCES users(id),
  created_at TEXT NOT NULL
);

CREATE TABLE workspace_settings (            -- single row, id=1
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

-- The single settings row exists from the first boot so that "inherited from workspace" always
-- has something to inherit from. Not in the data-model doc's SQL; noted in the S03 report.
INSERT INTO workspace_settings (id, updated_at)
VALUES (1, strftime('%Y-%m-%dT%H:%M:%SZ', 'now'));

-- ------------------------------------------------------------- §2 projects and repositories

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
  created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL
);

CREATE TABLE project_members (
  project_id TEXT NOT NULL REFERENCES projects(id),
  user_id TEXT NOT NULL REFERENCES users(id),
  PRIMARY KEY (project_id, user_id)
);

CREATE TABLE repos (
  project_id TEXT PRIMARY KEY REFERENCES projects(id),
  provider TEXT NOT NULL DEFAULT 'github',  -- ForgeProvider ID
  owner TEXT NOT NULL,
  name TEXT NOT NULL,                       -- 'acme','payments'
  default_branch TEXT,                      -- null = inherit
  branch_template TEXT,
  setup_script TEXT NOT NULL DEFAULT '',
  image_ref TEXT,                           -- null = built-in image (D-7)
  network_policy TEXT,                      -- none|allowlist|open, null = inherit
  network_allowlist TEXT NOT NULL DEFAULT '[]' CHECK (json_valid(network_allowlist)),
  token_secret_id TEXT REFERENCES secrets(id),
  connected_at TEXT,
  last_synced_at TEXT,
  head_sha TEXT,
  head_message TEXT,                        -- for the Overview About card
  created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL
);

CREATE TABLE secrets (                       -- D-16; values never readable through the API
  id TEXT PRIMARY KEY,
  scope TEXT NOT NULL CHECK (scope IN ('workspace','project')),
  project_id TEXT REFERENCES projects(id),
  name TEXT NOT NULL,                        -- env var name for project secrets
  ciphertext BLOB NOT NULL,
  nonce BLOB NOT NULL,
  created_by TEXT NOT NULL REFERENCES users(id),
  created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL,
  UNIQUE (scope, project_id, name)
);

-- ----------------------------------------------------------------------------- §3 agents

CREATE TABLE agents (
  id TEXT PRIMARY KEY,
  project_id TEXT NOT NULL REFERENCES projects(id),
  name TEXT NOT NULL,
  role TEXT NOT NULL DEFAULT '',
  color TEXT NOT NULL,
  runtime_id TEXT NOT NULL DEFAULT 'claude-code',   -- AgentRuntime port ID
  model TEXT NOT NULL,
  effort TEXT NOT NULL DEFAULT 'medium',
  autonomy TEXT NOT NULL CHECK (autonomy IN ('suggest','approve_each','auto_gates','auto')),
  permissions TEXT NOT NULL CHECK (json_valid(permissions)),   -- data model §3.1
  git_author_name TEXT NOT NULL,
  git_author_email TEXT NOT NULL,
  forge_login TEXT,                          -- display only in V1 (D-9)
  forge_token_secret_id TEXT REFERENCES secrets(id),  -- nullable: per-agent bot account, later
  concurrency_cap INTEGER NOT NULL DEFAULT 1,
  daily_cap_cents INTEGER,
  max_wall_clock_seconds INTEGER NOT NULL DEFAULT 3600,
  max_steps INTEGER NOT NULL DEFAULT 200,
  enabled INTEGER NOT NULL DEFAULT 1,
  directive_version_id TEXT REFERENCES agent_directives(id),  -- current
  archived_at TEXT,
  created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL,
  UNIQUE (project_id, name)
);

CREATE TABLE agent_directives (              -- append-only; diff view reads two rows
  id TEXT PRIMARY KEY,
  agent_id TEXT NOT NULL REFERENCES agents(id),
  version INTEGER NOT NULL,
  body TEXT NOT NULL,
  token_estimate INTEGER NOT NULL,
  author_id TEXT REFERENCES users(id),
  note TEXT NOT NULL DEFAULT '',
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

-- ------------------------------------------------------------------- §4 board and tickets

CREATE TABLE columns (
  id TEXT PRIMARY KEY,
  project_id TEXT NOT NULL REFERENCES projects(id),
  name TEXT NOT NULL,                         -- user-renameable
  category TEXT NOT NULL CHECK (category IN
    ('backlog','ready','running','review','done','canceled')),   -- automation keys off THIS (D2)
  position INTEGER NOT NULL,
  wip_limit INTEGER,                          -- enforcing on category='running'
  auto_start_delegate INTEGER NOT NULL DEFAULT 0,
  created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL
);

CREATE TABLE tickets (
  id TEXT PRIMARY KEY,
  project_id TEXT NOT NULL REFERENCES projects(id),
  seq INTEGER NOT NULL,
  key TEXT NOT NULL,                          -- 'PAY-14', generated from projects.ticket_seq
  title TEXT NOT NULL,
  description TEXT NOT NULL DEFAULT '',
  column_id TEXT NOT NULL REFERENCES columns(id),
  position REAL NOT NULL,                     -- fractional ranking for drag ordering
  priority TEXT NOT NULL CHECK (priority IN ('none','low','medium','high','urgent')),
  assignee_id TEXT REFERENCES users(id),      -- human, accountable      (D1)
  delegate_agent_id TEXT REFERENCES agents(id), -- agent, doing the work (D1)
  parent_id TEXT REFERENCES tickets(id),      -- exactly one level; CHECK enforced in service
  pr_number INTEGER,
  pr_state TEXT,
  pr_checks TEXT,
  pr_additions INTEGER,
  pr_deletions INTEGER,
  branch TEXT,
  origin TEXT NOT NULL DEFAULT 'human'
    CHECK (origin IN ('human','agent','trigger','import')),
  created_by_user_id TEXT REFERENCES users(id),
  created_by_agent_id TEXT REFERENCES agents(id),
  archived_at TEXT,
  created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL,
  UNIQUE (project_id, seq)
);

CREATE TABLE acceptance_criteria (
  id TEXT PRIMARY KEY,
  ticket_id TEXT NOT NULL REFERENCES tickets(id),
  position INTEGER NOT NULL,
  text TEXT NOT NULL,
  checked INTEGER NOT NULL DEFAULT 0,
  checked_by_run_id TEXT REFERENCES runs(id),
  checked_by_user_id TEXT REFERENCES users(id),
  note TEXT NOT NULL DEFAULT '',
  updated_at TEXT NOT NULL
);

CREATE TABLE labels (
  id TEXT PRIMARY KEY,
  project_id TEXT NOT NULL REFERENCES projects(id),
  name TEXT NOT NULL,
  color TEXT NOT NULL,
  UNIQUE (project_id, name)
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
  resolved_by TEXT REFERENCES users(id),
  resolved_at TEXT,
  created_at TEXT NOT NULL
);

-- §4.1 the unified ticket stream

CREATE TABLE ticket_stream (
  id TEXT PRIMARY KEY,
  ticket_id TEXT NOT NULL REFERENCES tickets(id),
  kind TEXT NOT NULL CHECK (kind IN
    ('comment','status_change','field_change','run','pr_event','trigger_fired','proposal')),
  actor_kind TEXT NOT NULL CHECK (actor_kind IN ('human','agent','trigger','system')),
  actor_id TEXT,
  body TEXT NOT NULL DEFAULT '',              -- markdown for comments
  payload TEXT NOT NULL DEFAULT '{}' CHECK (json_valid(payload)),
  run_id TEXT REFERENCES runs(id),            -- kind='run' → renders RunSessionCard
  edited_at TEXT,
  created_at TEXT NOT NULL
);
CREATE INDEX ix_stream_ticket ON ticket_stream(ticket_id, created_at);

-- -------------------------------------------------------------------------------- §5 wiki

CREATE TABLE wiki_pages (
  id TEXT PRIMARY KEY,
  project_id TEXT NOT NULL REFERENCES projects(id),
  slug TEXT NOT NULL,
  title TEXT NOT NULL,
  parent_id TEXT REFERENCES wiki_pages(id),   -- max one level; service enforces parent.parent IS NULL
  position REAL NOT NULL,
  owner_id TEXT REFERENCES users(id),
  verified_until TEXT,
  agent_scope TEXT NOT NULL DEFAULT 'auto'
    CHECK (agent_scope IN ('always','auto','paths','manual','never')),
  scope_paths TEXT NOT NULL DEFAULT '[]' CHECK (json_valid(scope_paths)),  -- globs, when agent_scope='paths'
  tags TEXT NOT NULL DEFAULT '[]' CHECK (json_valid(tags)),
  body TEXT NOT NULL DEFAULT '',
  token_estimate INTEGER NOT NULL DEFAULT 0,
  state TEXT NOT NULL DEFAULT 'live' CHECK (state IN ('live','proposed')),
  proposed_by_run_id TEXT REFERENCES runs(id),
  proposed_base_version INTEGER,              -- set when the proposal edits an existing page
  proposal_target_id TEXT REFERENCES wiki_pages(id),
  imported_from TEXT,                         -- 'AGENTS.md' etc (D-11)
  demoted_at TEXT,
  demoted_from TEXT,                          -- verified_until enforcement leaves a trace
  archived_at TEXT,
  created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL,
  UNIQUE (project_id, slug)
);

CREATE TABLE wiki_versions (
  id TEXT PRIMARY KEY,
  page_id TEXT NOT NULL REFERENCES wiki_pages(id),
  version INTEGER NOT NULL,
  title TEXT NOT NULL,
  body TEXT NOT NULL,
  front_matter TEXT NOT NULL CHECK (json_valid(front_matter)),
  author_user_id TEXT REFERENCES users(id),
  author_run_id TEXT REFERENCES runs(id),
  created_at TEXT NOT NULL,
  UNIQUE (page_id, version)
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

-- ---------------------------------------------------------------------------- §6 triggers

CREATE TABLE triggers (
  id TEXT PRIMARY KEY,
  project_id TEXT NOT NULL REFERENCES projects(id),
  name TEXT NOT NULL,
  enabled INTEGER NOT NULL DEFAULT 0,                       -- suggested triggers ship OFF (brief §6.3)
  source_id TEXT NOT NULL DEFAULT 'github.poll',            -- EventSource port ID
  event TEXT NOT NULL,                                      -- 'pull_request'
  activity_types TEXT NOT NULL DEFAULT '[]' CHECK (json_valid(activity_types)),
  filters TEXT NOT NULL DEFAULT '{}' CHECK (json_valid(filters)),
  conditions TEXT NOT NULL DEFAULT '{"all":[]}' CHECK (json_valid(conditions)),
  actions TEXT NOT NULL CHECK (json_valid(actions)),        -- ordered [{action_id, params}]
  loop_config TEXT NOT NULL CHECK (json_valid(loop_config)),  -- data model §6.1
  cron TEXT,                                                -- when event='schedule'
  created_by TEXT REFERENCES users(id),
  created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL
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
  warnings TEXT NOT NULL DEFAULT '[]' CHECK (json_valid(warnings)),
  created_at TEXT NOT NULL,
  UNIQUE (trigger_id, event_id)               -- idempotency for bus re-dispatch
);
CREATE INDEX ix_firings_health ON trigger_firings(trigger_id, created_at DESC);

-- ------------------------------------------------------------------------------ §7 events

CREATE TABLE events (
  id TEXT PRIMARY KEY,
  project_id TEXT REFERENCES projects(id),
  source TEXT NOT NULL,                        -- EventSource ID or 'internal'
  kind TEXT NOT NULL,
  activity_type TEXT NOT NULL DEFAULT '',
  actor_kind TEXT NOT NULL,
  actor_id TEXT,
  actor_login TEXT,
  actor_email TEXT,
  subject_kind TEXT NOT NULL,
  subject_id TEXT,
  subject_number INTEGER,
  subject_branch TEXT,
  payload TEXT NOT NULL CHECK (json_valid(payload)),  -- NORMALIZED shape (contracts §4)
  cause_run_id TEXT REFERENCES runs(id),       -- causality edge
  dedupe_key TEXT NOT NULL UNIQUE,
  dispatch_state TEXT NOT NULL DEFAULT 'pending'
    CHECK (dispatch_state IN ('pending','done','failed')),
  occurred_at TEXT NOT NULL,
  created_at TEXT NOT NULL
);
CREATE INDEX ix_events_backtest ON events(project_id, kind, occurred_at DESC);
CREATE INDEX ix_events_subject ON events(project_id, subject_kind, subject_number);

CREATE TABLE poll_cursors (
  project_id TEXT NOT NULL REFERENCES projects(id),
  resource TEXT NOT NULL,                      -- 'pulls','reviews','issue_comments',...
  cursor TEXT NOT NULL DEFAULT '',
  etag TEXT NOT NULL DEFAULT '',
  baseline_done INTEGER NOT NULL DEFAULT 0,
  last_polled_at TEXT,
  PRIMARY KEY (project_id, resource)
);

CREATE TABLE poll_pr_state (                   -- the diff basis for deriving activity types
  project_id TEXT NOT NULL REFERENCES projects(id),
  number INTEGER NOT NULL,
  head_sha TEXT NOT NULL,
  state TEXT NOT NULL,
  draft INTEGER NOT NULL,
  updated_at TEXT NOT NULL,
  review_cursor TEXT NOT NULL DEFAULT '',
  PRIMARY KEY (project_id, number)
);

-- -------------------------------------------------------------------------------- §8 runs

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
  model TEXT NOT NULL,
  effort TEXT NOT NULL,
  prompt TEXT NOT NULL,                        -- the fully rendered prompt, kept for reproducibility
  runtime_id TEXT NOT NULL,
  sandbox_id TEXT NOT NULL,
  container_id TEXT,
  instance_id TEXT,
  log_offset INTEGER NOT NULL DEFAULT 0,
  branch TEXT,
  base_sha TEXT,
  depth INTEGER NOT NULL DEFAULT 0,
  subject_key TEXT NOT NULL DEFAULT '',        -- guard key: 'pr:219' | 'ticket:PAY-14' | 'repo'
  current_step TEXT NOT NULL DEFAULT '',
  cost_cents INTEGER NOT NULL DEFAULT 0,
  tokens_in INTEGER NOT NULL DEFAULT 0,
  tokens_out INTEGER NOT NULL DEFAULT 0,
  tokens_cache_read INTEGER NOT NULL DEFAULT 0,
  tokens_cache_write INTEGER NOT NULL DEFAULT 0,
  step_count INTEGER NOT NULL DEFAULT 0,
  error_message TEXT NOT NULL DEFAULT '',
  takeover_note TEXT NOT NULL DEFAULT '',
  queued_at TEXT NOT NULL,
  started_at TEXT,
  ended_at TEXT,
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
  payload TEXT NOT NULL DEFAULT '{}' CHECK (json_valid(payload)),
  ok INTEGER,                                  -- null = n/a; 0 = failed step (auto-expands)
  attempt INTEGER NOT NULL DEFAULT 1,          -- retry badge
  duration_ms INTEGER,
  queued_ms INTEGER,
  model_ms INTEGER,
  tool_ms INTEGER,                             -- timing gutter
  cost_cents INTEGER NOT NULL DEFAULT 0,
  tokens_in INTEGER NOT NULL DEFAULT 0,
  tokens_out INTEGER NOT NULL DEFAULT 0,
  created_at TEXT NOT NULL,
  PRIMARY KEY (run_id, seq)
);

CREATE TABLE elicitations (
  id TEXT PRIMARY KEY,
  run_id TEXT NOT NULL REFERENCES runs(id),
  activity_seq INTEGER NOT NULL,
  kind TEXT NOT NULL CHECK (kind IN ('question','approval')),
  request TEXT NOT NULL CHECK (json_valid(request)),  -- question+options, or tool+input+scope+impact+reason
  state TEXT NOT NULL CHECK (state IN ('pending','answered','denied','expired','canceled')),
  response TEXT CHECK (response IS NULL OR json_valid(response)),  -- {choice|text} or {behavior, updated_input, remember}
  responded_by TEXT REFERENCES users(id),
  responded_at TEXT,
  created_at TEXT NOT NULL
);

CREATE TABLE run_outputs (
  id TEXT PRIMARY KEY,
  run_id TEXT NOT NULL REFERENCES runs(id),
  kind TEXT NOT NULL CHECK (kind IN
    ('branch','pull_request','comment','review','wiki_proposal','ticket','partial_work')),
  ref TEXT NOT NULL,
  url TEXT NOT NULL DEFAULT '',
  summary TEXT NOT NULL DEFAULT '',
  created_at TEXT NOT NULL
);

CREATE TABLE run_context_items (               -- the Context panel; also the agent preview
  id TEXT PRIMARY KEY,
  run_id TEXT NOT NULL REFERENCES runs(id),
  provider TEXT NOT NULL,                      -- ContextProvider ID
  source_kind TEXT NOT NULL,                   -- 'wiki'|'repo_file'|'project'|'ticket'
  source_ref TEXT NOT NULL,                    -- slug or path
  title TEXT NOT NULL,
  reason TEXT NOT NULL,                        -- 'always' | 'matched path infra/**' | 'repo file'
  tokens INTEGER NOT NULL,
  position INTEGER NOT NULL,
  injected INTEGER NOT NULL DEFAULT 1
);

CREATE TABLE run_messages (                    -- steering queue (queue, don't interrupt)
  id TEXT PRIMARY KEY,
  run_id TEXT NOT NULL REFERENCES runs(id),
  body TEXT NOT NULL,
  author_id TEXT REFERENCES users(id),
  state TEXT NOT NULL CHECK (state IN ('queued','delivered','dropped')),
  created_at TEXT NOT NULL,
  delivered_at TEXT
);

-- ------------------------------------------------------- §9 governance, attention, audit

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
  title TEXT NOT NULL,
  body TEXT NOT NULL DEFAULT '',
  state TEXT NOT NULL CHECK (state IN ('unread','read','dismissed')),
  pushed INTEGER NOT NULL DEFAULT 0,
  created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL,
  UNIQUE (user_id, run_id)                     -- schema-level "never stack" (interaction rule 3)
);

CREATE TABLE audit_log (
  id TEXT PRIMARY KEY,
  project_id TEXT REFERENCES projects(id),
  actor_kind TEXT NOT NULL CHECK (actor_kind IN ('human','agent','trigger','system')),
  actor_id TEXT,
  action TEXT NOT NULL,                        -- 'ticket.move' | 'agent.directive.update' | ...
  target_kind TEXT NOT NULL,
  target_id TEXT NOT NULL,
  before TEXT CHECK (before IS NULL OR json_valid(before)),
  after TEXT CHECK (after IS NULL OR json_valid(after)),
  note TEXT NOT NULL DEFAULT '',
  created_at TEXT NOT NULL
);

CREATE TABLE saved_views (
  id TEXT PRIMARY KEY,
  user_id TEXT REFERENCES users(id),
  project_id TEXT REFERENCES projects(id),
  surface TEXT NOT NULL,                       -- 'runs'|'board'|'inbox'
  name TEXT NOT NULL,
  filters TEXT NOT NULL CHECK (json_valid(filters)),
  is_default INTEGER NOT NULL DEFAULT 0
);
