/*
 * Seed data for the screenshot harness (LEXI-13). Not test fixtures and not shipped: this
 * is one realistic run, rich enough that a screenshot of the converted run detail shows
 * the things the screen exists for — grouped tool calls, a failed bash step with output, a
 * diff, a pending approval, loaded context, a pushed PR, and a live steering message.
 *
 * Deliberately hand-written rather than imported from the test fixtures: the tests seed the
 * minimum a route needs to render, which produces empty, uninteresting pictures.
 */

const T = (offsetSeconds: number): string =>
  new Date(Date.UTC(2026, 7, 25, 9, 12, 0) + offsetSeconds * 1000).toISOString();

export const RUN_ID = "r-4821";
export const PROJECT_KEY = "PAY";

const user = {
  id: "u1",
  email: "ada@example.com",
  display_name: "Ada Lovelace",
  role: "owner",
  avatar_color: "#446688",
  created_at: T(0),
};

const inheritedInt = (value: number) => ({ value, inherited: true, workspace_value: value });

const project = {
  id: "p1",
  key: PROJECT_KEY,
  name: "Payments Service",
  description: "Charges, refunds and the idempotency layer.",
  color: "#4653cf",
  owner_id: "u1",
  agent_guidance: "",
  archived_at: null,
  created_at: T(0),
  updated_at: T(0),
  settings: {
    daily_budget_cents: inheritedInt(2000),
    context_threshold_tokens: inheritedInt(4000),
    verification_days: inheritedInt(30),
    pr_size_warning_lines: inheritedInt(400),
  },
};

const agent = {
  id: "a1",
  project_id: "p1",
  name: "Dev",
  role: "implementer",
  color: "#0f7f9c",
  runtime_id: "claude-code",
  model: "claude-opus-5",
  effort: "high",
  autonomy: "supervised",
  permissions: { allow: [], deny: [], ask: [] },
  git_author_name: "Dev",
  git_author_email: "dev@agents.local",
  forge_login: null,
  concurrency_cap: 1,
  daily_cap_cents: null,
  max_wall_clock_seconds: 3600,
  max_steps: 100,
  enabled: true,
  directive_version_id: null,
  archived_at: null,
  created_at: T(0),
  updated_at: T(0),
  runs_week: 12,
  spend_week_cents: 810,
  success_rate: 0.83,
};

const run = {
  id: RUN_ID,
  seq: 482,
  project_id: "p1",
  agent_id: "a1",
  ticket_id: "t1",
  trigger_id: null,
  state: "awaiting_approval",
  state_reason: "",
  hold_reason: "",
  autonomy: "supervised",
  directive_version_id: null,
  model: "claude-opus-5",
  effort: "high",
  branch: "dev/pay-14-idempotency-keys",
  subject_key: "ticket:PAY-14",
  current_step: "waiting for approval to run the migration",
  cost_cents: 142,
  tokens_in: 71_400,
  tokens_out: 12_800,
  tokens_cache_read: 48_100,
  tokens_cache_write: 3_200,
  step_count: 7,
  error_message: "",
  takeover_note: "",
  queued_at: T(-90),
  started_at: T(0),
  ended_at: null,
  acknowledged_at: null,
};

/** A step, with the fields every activity carries filled in. */
function step(
  seq: number,
  type: string,
  toolName: string,
  title: string,
  payload: unknown,
  extra: Partial<{
    ok: boolean | null;
    level: number;
    groupKey: string;
    durationMs: number;
    queuedMs: number;
    modelMs: number;
    toolMs: number;
    costCents: number;
    attempt: number;
  }> = {},
) {
  const durationMs = extra.durationMs ?? 1200;
  return {
    seq,
    type,
    level: extra.level ?? 1,
    tool_name: toolName,
    group_key: extra.groupKey ?? toolName,
    title,
    payload,
    ok: extra.ok ?? null,
    attempt: extra.attempt ?? 1,
    duration_ms: durationMs,
    queued_ms: extra.queuedMs ?? Math.round(durationMs * 0.1),
    model_ms: extra.modelMs ?? Math.round(durationMs * 0.5),
    tool_ms: extra.toolMs ?? Math.round(durationMs * 0.4),
    cost_cents: extra.costCents ?? 0,
    tokens_in: 2_400,
    tokens_out: 380,
    tokens_cache_read: 1_900,
    created_at: T(seq * 6),
  };
}

const READ_PATHS = [
  "internal/service/charges/charge.go",
  "internal/service/charges/refund.go",
  "internal/service/charges/idempotency.go",
  "internal/kernel/store/charges.go",
  "internal/kernel/store/idempotency.go",
  "internal/api/v1/openapi.yaml",
  "internal/service/charges/charge_test.go",
  "migrations/0007_charges.up.sql",
  "docs/payments.md",
];

/** The activity stream: provisioning, a plan, a grouped read burst, an edit, a failing
 * test run, and the approval the run is currently blocked on. */
export const ACTIVITIES = [
  step(1, "provision", "provision", "Container started", { step: "image", state: "ok", detail: "lexicode/agent:local — cached" }, { ok: true, level: 0, durationMs: 4100 }),
  step(2, "provision", "provision", "Repository cloned", { step: "clone", state: "ok", detail: "acme/payments @ main (a1c9f02)" }, { ok: true, level: 0, durationMs: 8600 }),
  step(3, "thought", "", "Reading the idempotency layer before changing the charge path", { text: "PAY-14 asks for idempotency keys on the charge API. Before writing anything I want to see how refunds already do this — there is an idempotency table in the store, so the pattern probably exists and should be reused rather than reinvented." }, { level: 1, durationMs: 3200, costCents: 11 }),
  // A read burst: consecutive same-tool calls, which the timeline collapses into one
  // "Read 9 files ▸" row. Level 1 so it is visible at the default Normal verbosity —
  // collapsing a burst is the highest-value affordance on this screen (§5.7).
  ...READ_PATHS.map((p, i) =>
    step(4 + i, "action", "Read", `Read ${p}`, { path: p, lines: 120 + i * 37 }, {
      ok: true,
      level: 1,
      groupKey: "Read",
      durationMs: 220 + i * 40,
    }),
  ),
  step(13, "action", "TodoWrite", "Plan", {
    items: [
      { content: "Reuse the refund idempotency table for charges", status: "completed" },
      { content: "Add an Idempotency-Key header to POST /charges", status: "completed" },
      { content: "Write the migration that adds the charges index", status: "in_progress" },
      { content: "Backfill the existing rows", status: "pending" },
      { content: "Update the OpenAPI schema and regenerate types", status: "pending" },
    ],
  }, { ok: true, level: 0, durationMs: 900, costCents: 6 }),
  step(14, "action", "Edit", "Edit internal/service/charges/charge.go", {
    path: "internal/service/charges/charge.go",
    hunks: [
      {
        header: "@@ -42,9 +42,21 @@ func (s *Service) Charge(",
        lines: [
          " func (s *Service) Charge(ctx context.Context, req ChargeRequest) (*Charge, error) {",
          "-\tif err := req.Validate(); err != nil {",
          "-\t\treturn nil, err",
          "+\tif err := req.Validate(); err != nil {",
          "+\t\treturn nil, err",
          "+\t}",
          "+",
          "+\t// PAY-14: the same key must never charge twice. The refund path already",
          "+\t// keys on (tenant, key); charges reuse that table rather than a second one.",
          "+\tif req.IdempotencyKey != \"\" {",
          "+\t\texisting, err := s.store.ChargeByIdempotencyKey(ctx, req.TenantID, req.IdempotencyKey)",
          "+\t\tif err != nil && !errors.Is(err, store.ErrNotFound) {",
          "+\t\t\treturn nil, err",
          "+\t\t}",
          "+\t\tif existing != nil {",
          "+\t\t\treturn existing, nil",
          "+\t\t}",
          " \t}",
        ],
      },
    ],
  }, { ok: true, level: 0, durationMs: 5400, costCents: 24 }),
  step(15, "action", "Bash", "go test ./internal/service/charges/...", {
    argv: ["bash", "-lc", "go test ./internal/service/charges/..."],
    exit: 1,
    stdout: [
      "--- FAIL: TestChargeIsIdempotent (0.03s)",
      "    charge_test.go:88: second charge created a new row",
      "        want: same charge id as the first call",
      "        got:  ch_2f81a0 (first was ch_9d20e4)",
      "FAIL",
      "FAIL\tgithub.com/acme/payments/internal/service/charges\t0.412s",
      "ok  \tgithub.com/acme/payments/internal/kernel/store\t0.244s",
    ].join("\n"),
    stderr: "",
    truncated: false,
  }, { ok: false, level: 0, durationMs: 12_800, toolMs: 11_900, modelMs: 600, costCents: 8 }),
  step(16, "thought", "", "The index is missing, so the lookup never finds the first charge", { text: "The lookup is right but there is no unique index on (tenant_id, idempotency_key) for charges, so ChargeByIdempotencyKey returns ErrNotFound every time. That needs a migration — and a migration on the charges table is a destructive-enough change that my autonomy level gates it." }, { level: 1, durationMs: 2600, costCents: 9 }),
  step(17, "elicitation", "mcp__lexicode__request_approval", "Approval: run a migration on the charges table", {
    tool_name: "Bash",
  }, { level: 0, durationMs: 0 }),
];

export const ELICITATIONS = [
  {
    id: "el-1",
    run_id: RUN_ID,
    activity_seq: 17,
    kind: "approval",
    state: "pending",
    request: {
      tool_name: "Bash",
      action: "Add a unique index on charges(tenant_id, idempotency_key)",
      scope: "One migration file, applied to the run's own database copy.",
      impact:
        "Adds migrations/0011_charges_idempotency.up.sql and runs it. No existing row is rewritten; the index build locks the table for a few seconds on a real deployment.",
      reason:
        "Without the index the idempotency lookup cannot find the first charge, so TestChargeIsIdempotent fails.",
      alternatives:
        "A non-unique index would make the lookup fast but would not stop a concurrent double-charge.",
      recovery: "The down migration drops the index; nothing else changes.",
      input: {
        command: "cat > migrations/0011_charges_idempotency.up.sql <<'SQL'\nCREATE UNIQUE INDEX charges_idempotency\n  ON charges (tenant_id, idempotency_key)\n  WHERE idempotency_key IS NOT NULL;\nSQL",
      },
    },
    created_at: T(102),
  },
];

export const CONTEXT_ITEMS = [
  {
    provider: "wiki",
    source_ref: "conventions",
    position: 1,
    title: "Go conventions",
    reason: "scope: always — loaded on every run in this project",
    tokens: 1_240,
  },
  {
    provider: "wiki",
    source_ref: "payments-runbook",
    position: 2,
    title: "Payments runbook",
    reason: "scope: paths — the run touched internal/service/charges/",
    tokens: 2_910,
  },
  {
    provider: "repo",
    source_ref: "AGENTS.md",
    position: 3,
    title: "AGENTS.md",
    reason: "repo instruction file at the root",
    tokens: 640,
  },
];

export const OUTPUTS = [
  {
    id: "out-1",
    run_id: RUN_ID,
    kind: "branch",
    ref: "dev/pay-14-idempotency-keys",
    url: "",
    summary: "3 commits, pushed",
    created_at: T(96),
  },
  {
    id: "out-2",
    run_id: RUN_ID,
    kind: "pull_request",
    ref: "#219",
    url: "https://github.com/acme/payments/pull/219",
    summary: "Add idempotency keys to the charge API",
    additions: 512,
    deletions: 44,
    created_at: T(98),
  },
];

export const MESSAGES = [
  {
    id: "m-1",
    run_id: RUN_ID,
    body: "Reuse the refund idempotency table rather than adding a second one.",
    state: "delivered",
    created_at: T(60),
  },
];

const ticket = {
  id: "t1",
  project_id: "p1",
  seq: 14,
  key: "PAY-14",
  title: "Add idempotency keys to charge API",
  description: "",
  column_id: "col2",
  category: "running",
  position: 1,
  priority: "high",
  assignee_id: "u1",
  delegate_agent_id: "a1",
  parent_id: null,
  pr_number: 219,
  pr_state: "open",
  branch: "dev/pay-14-idempotency-keys",
  origin: "human",
  created_by_user_id: "u1",
  created_by_agent_id: null,
  label_ids: [],
  criteria_total: 5,
  criteria_checked: 3,
  archived_at: null,
  created_at: T(-3600),
  updated_at: T(0),
};

/** GET path → payload. First match wins; anything unmatched is a 404. */
export const FIXTURES: Array<[RegExp, unknown]> = [
  [/\/auth\/me$/, user],
  [/\/projects(\?.*)?$/, { projects: [{ ...project, stats: { open_tickets: 6, running_agents: 2, needs_you: 1, spend_today_cents: 142, last_activity: T(102) } }] }],
  [new RegExp(`/projects/${PROJECT_KEY}$`), project],
  [new RegExp(`/projects/${PROJECT_KEY}/budget$`), { spend_today_cents: 142, ceiling_cents: 2000, inherited: true, exhausted: false, day: "2026-08-25", resets_at: T(50_000) }],
  [new RegExp(`/projects/${PROJECT_KEY}/triage$`), { items: [], pending_count: 2 }],
  [new RegExp(`/projects/${PROJECT_KEY}/runs`), { runs: [run] }],
  [new RegExp(`/projects/${PROJECT_KEY}/agents`), { agents: [agent] }],
  [new RegExp(`/projects/${PROJECT_KEY}/tickets`), { tickets: [ticket] }],
  [
    new RegExp(`/projects/${PROJECT_KEY}/wiki/context-budget$`),
    {
      threshold_tokens: 4_000,
      always_tokens: 1_240,
      over: false,
      pages: [{ id: "w1", slug: "conventions", title: "Go conventions", tokens: 1_240 }],
    },
  ],
  [new RegExp(`/runs/${RUN_ID}/activities`), { activities: ACTIVITIES }],
  [new RegExp(`/runs/${RUN_ID}/chain$`), { chain: [] }],
  [
    new RegExp(`/runs/${RUN_ID}$`),
    {
      run,
      outputs: OUTPUTS,
      context: CONTEXT_ITEMS,
      messages: MESSAGES,
      elicitations: ELICITATIONS,
    },
  ],
  [/\/inbox$/, { runs: [] }],
  [/\/notifications$/, { notifications: [], unread: 1 }],
];

/** A variant of the run in a terminal failed state, for the second screenshot. */
export const FAILED_RUN = {
  ...run,
  state: "failed",
  current_step: "",
  error_message: "go test ./internal/service/charges/... exited 1",
  ended_at: T(150),
};
