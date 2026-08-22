/*
 * Can a person CLICK their way to every screen?
 *
 * This is the regression test whose absence let two bugs ship at once: `/settings` existed,
 * rendered, was documented ("Settings → Credentials" in docs/oauth-token.md, docs/install.md
 * and `lexicode doctor`) — and nothing in the app linked to it, so the one screen that holds
 * the Claude credentials every agent needs was reachable only by typing a URL. `/settings/audit`
 * inherited the same fate: linked from the settings page, which nobody could get to.
 *
 * Every story verified its screen by navigating straight to its URL, and the e2e scripts drive
 * the HTTP API. Neither layer can see a missing link. This one can.
 *
 * Mechanics: the real route tree over a memory history, the API stubbed at fetch with a seeded
 * fixture set (one project, ticket, wiki page, run, agent, connected repo) so lists render the
 * links they would render in production. Breadth-first from `/`, following only what a mouse
 * can follow — rendered anchors, plus the items inside menus opened from `[aria-haspopup="menu"]`
 * buttons. Every URL visited records the route ids it matched; at the end, every route in the
 * tree must appear in that set or be listed in URL_ONLY with a reason.
 *
 * A new orphan route fails the suite by name.
 */
import { act, cleanup, fireEvent, render, waitFor } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { createMemoryHistory, createRouter, RouterProvider } from "@tanstack/react-router";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import { routeTree } from "../../app/router";

/**
 * Routes a signed-in user is NOT expected to reach by clicking, each with the reason. Adding
 * an entry here is a deliberate, reviewable statement — that is the point. Keys are route ids
 * (the `/shell/...` form the router assigns), so a renamed or deleted route makes its own
 * exemption stale and the suite says so.
 */
const URL_ONLY: Record<string, string> = {
  "/setup":
    "first-run screen: it exists only while the database has no users, and the API redirects to it",
  "/login": "signed-out entry point; the shell sends a 401 here, a signed-in user never links to it",
  "/invite/$token": "an emailed invite link — the token in the URL is the whole address",
  "/shell/p/$key/t/$ticket":
    "opened by ACTIVATING a ticket, not by a link: a board card (role=button, Enter or double-click), " +
    "a triage row, or an inbox row. The affordance is the card itself, so there is no anchor to follow; " +
    "TicketPage's own sub-ticket links are the only <a> that points here.",
};

const T = "2026-08-22T12:00:00Z";

const user = {
  id: "u1",
  email: "ada@example.com",
  display_name: "Ada Lovelace",
  role: "owner",
  avatar_color: "#446688",
  created_at: T,
};

const inheritedInt = (value: number) => ({ value, inherited: true, workspace_value: value });

const project = {
  id: "p1",
  key: "PAY",
  name: "Payments",
  description: "Seeded fixture project",
  color: "#4653cf",
  owner_id: "u1",
  agent_guidance: "",
  archived_at: null,
  created_at: T,
  updated_at: T,
  settings: {
    daily_budget_cents: inheritedInt(2000),
    context_threshold_tokens: inheritedInt(4000),
    verification_days: inheritedInt(30),
    pr_size_warning_lines: inheritedInt(600),
  },
};

const projectListItem = {
  ...project,
  stats: {
    open_tickets: 1,
    running_agents: 0,
    needs_you: 0,
    spend_today_cents: 12,
    last_activity: T,
  },
};

const agent = {
  id: "a1",
  project_id: "p1",
  name: "Dev",
  role: "implementer",
  color: "#0f7f9c",
  runtime_id: "claude-code",
  model: "claude-sonnet-5",
  effort: "medium",
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
  created_at: T,
  updated_at: T,
  runs_week: 0,
  spend_week_cents: 0,
  success_rate: null,
};

const column = {
  id: "col1",
  project_id: "p1",
  name: "Backlog",
  category: "backlog",
  position: 1,
  wip_limit: null,
  auto_start_delegate: false,
  ticket_count: 1,
  created_at: T,
  updated_at: T,
};

const ticket = {
  id: "t1",
  project_id: "p1",
  seq: 1,
  key: "PAY-1",
  title: "Add idempotency keys",
  description: "",
  column_id: "col1",
  category: "backlog",
  position: 1,
  priority: "none",
  assignee_id: null,
  delegate_agent_id: "a1",
  parent_id: null,
  pr_number: null,
  pr_state: null,
  branch: null,
  origin: "human",
  created_by_user_id: "u1",
  created_by_agent_id: null,
  label_ids: [],
  criteria_total: 0,
  criteria_checked: 0,
  archived_at: null,
  created_at: T,
  updated_at: T,
};

const run = {
  id: "r1",
  seq: 1,
  project_id: "p1",
  agent_id: "a1",
  ticket_id: "t1",
  trigger_id: null,
  // Failed, deliberately: the run list's default view is the saved "Needs attention"
  // filter (§5.7), so a completed run renders no row and therefore no link to a run detail.
  state: "failed",
  state_reason: "the container exited 1",
  hold_reason: "",
  autonomy: "supervised",
  directive_version_id: null,
  model: "claude-sonnet-5",
  effort: "medium",
  branch: "dev/pay-1",
  subject_key: "ticket:PAY-1",
  current_step: "",
  cost_cents: 12,
  tokens_in: 1000,
  tokens_out: 400,
  tokens_cache_read: 0,
  tokens_cache_write: 0,
  step_count: 3,
  error_message: "npm test exited 1",
  takeover_note: "",
  queued_at: T,
  started_at: T,
  ended_at: T,
  acknowledged_at: null,
};

const wikiPage = {
  id: "w1",
  project_id: "p1",
  slug: "conventions",
  title: "Conventions",
  parent_id: null,
  position: 1,
  owner_id: "u1",
  verified_until: null,
  agent_scope: "always",
  scope_paths: [],
  tags: [],
  body: "# Conventions",
  token_estimate: 4,
  state: "published",
  proposed_by_run_id: null,
  proposed_base_version: null,
  proposal_target_id: null,
  proposed_reason: null,
  imported_from: null,
  demoted_at: null,
  created_at: T,
  updated_at: T,
};

const trigger = {
  id: "tr1",
  project_id: "p1",
  name: "CI failed → file a ticket",
  enabled: true,
  source_id: "github",
  event: "check_failed",
  activity_types: [],
  filters: {},
  conditions: null,
  actions: [{ action_id: "create_ticket" }],
  loop_config: { depth_limit: 3 },
  cron: null,
  created_by: "u1",
  created_at: T,
  updated_at: T,
};

const workspaceSettings = {
  default_branch: "main",
  default_branch_template: "{agent}/{ticket-key}-{slug}",
  default_network_policy: "allowlist",
  default_daily_budget_cents: 2000,
  default_context_threshold_tokens: 4000,
  default_verification_days: 30,
  max_concurrent_containers: 6,
  poll_interval_seconds: 60,
  pr_size_warning_lines: 600,
  updated_at: T,
};

/** GET path → payload. First match wins; anything unmatched is a problem+json 404. */
const FIXTURES: Array<[RegExp, unknown]> = [
  [/\/auth\/me$/, user],
  [/\/projects(\?.*)?$/, { projects: [projectListItem] }],
  [/\/projects\/PAY$/, project],
  [
    /\/projects\/PAY\/overview$/,
    {
      project,
      owner: { id: "u1", display_name: "Ada Lovelace", avatar_color: "#446688" },
      repo: {
        provider: "github",
        owner: "acme",
        name: "payments",
        default_branch: "main",
        head_sha: "abc1234",
        head_message: "Seed the fixture",
      },
      agent_count: 1,
      open_tickets: 1,
      runs_today: 1,
      spend_today_cents: 12,
    },
  ],
  [
    /\/projects\/PAY\/budget$/,
    {
      spend_today_cents: 12,
      ceiling_cents: 2000,
      inherited: true,
      exhausted: false,
      day: "2026-08-22",
      resets_at: T,
    },
  ],
  [/\/projects\/PAY\/counts$/, { tickets: 1, runs: 1, wiki_pages: 1 }],
  [/\/projects\/PAY\/triage$/, { items: [], pending_count: 0 }],
  [/\/projects\/PAY\/runs\?view=needs_you$/, { runs: [] }],
  [/\/projects\/PAY\/runs(\?.*)?$/, { runs: [run] }],
  [/\/runs\/r1$/, { run, outputs: [], context: [], messages: [], elicitations: [] }],
  [/\/runs\/r1\/activities/, { activities: [] }],
  [/\/runs\/r1\/chain$/, { chain: [] }],
  [/\/projects\/PAY\/agents(\?.*)?$/, { agents: [agent] }],
  [/\/agents\/a1$/, agent],
  [/\/agents\/a1\/directives$/, { directives: [] }],
  [/\/agents\/a1\/permission-rules$/, { rules: [] }],
  [/\/agents\/a1\/context-preview$/, { items: [], total_tokens: 0 }],
  [/\/projects\/PAY\/tickets(\?.*)?$/, { tickets: [ticket] }],
  [/\/tickets\/t1$/, { ...ticket, criteria: [], labels: [], children: [] }],
  [/\/tickets\/t1\/stream$/, { entries: [] }],
  [/\/projects\/PAY\/columns$/, { columns: [column] }],
  [/\/projects\/PAY\/labels$/, { labels: [] }],
  [/\/projects\/PAY\/wiki$/, { pages: [wikiPage] }],
  [
    /\/projects\/PAY\/wiki\/context-budget$/,
    {
      threshold_tokens: 4000,
      always_tokens: 4,
      over: false,
      pages: [{ id: "w1", slug: "conventions", title: "Conventions", tokens: 4 }],
    },
  ],
  [/\/wiki\/w1$/, { page: wikiPage, version: 1, backlinks: [], unlinked_mentions: [] }],
  [/\/projects\/PAY\/triggers$/, { triggers: [trigger] }],
  [/\/triggers\/tr1$/, trigger],
  [/\/triggers\/tr1\/firings/, { firings: [] }],
  [/\/projects\/PAY\/trigger-catalog$/, { sources: [], actions: [], operators: [] }],
  [
    /\/projects\/PAY\/repo$/,
    {
      connected: true,
      repo: {
        provider: "github",
        owner: "acme",
        name: "payments",
        default_branch: "main",
        head_sha: "abc1234",
        head_message: "Seed the fixture",
        connected_at: T,
        last_synced_at: T,
      },
    },
  ],
  [/\/secrets$/, { secrets: [] }],
  [/\/workspace\/settings$/, workspaceSettings],
  [
    /\/workspace\/credentials$/,
    {
      oauth_token: { present: false, source: "none", detail: "" },
      env: { present: false, source: "none", detail: "" },
      import: { available: false, path: "" },
    },
  ],
  [/\/audit(\?.*)?$/, { entries: [], next_cursor: null }],
  [/\/users$/, { users: [user] }],
  [/\/projects\/PAY\/members$/, { members: [] }],
  [/\/inbox$/, { runs: [] }],
  [/\/notifications$/, { notifications: [], unread: 0 }],
  [/\/system\/modules$/, { modules: [] }],
];

/** Every unmatched GET, so a missing fixture is diagnosable instead of a mystery 404. */
const unmatched = new Set<string>();

function jsonResponse(body: unknown, status = 200): Response {
  return new Response(JSON.stringify(body), {
    status,
    headers: { "Content-Type": "application/json" },
  });
}

function installFetchStub(): void {
  vi.stubGlobal(
    "fetch",
    vi.fn(async (url: string, init?: RequestInit) => {
      const u = String(url);
      if ((init?.method ?? "GET") === "GET") {
        for (const [re, body] of FIXTURES) {
          if (re.test(u)) return jsonResponse(body);
        }
        unmatched.add(u.replace(/^https?:\/\/[^/]+/, ""));
      }
      return jsonResponse(
        { type: "not_found", title: "Not found", status: 404, detail: "stubbed" },
        404,
      );
    }),
  );
}

/** jsdom has no EventSource; the stream manager only needs the shape. */
class FakeEventSource {
  static readonly CONNECTING = 0;
  static readonly OPEN = 1;
  static readonly CLOSED = 2;
  readyState = 0;
  onopen: (() => void) | null = null;
  onerror: (() => void) | null = null;
  addEventListener(): void {}
  removeEventListener(): void {}
  close(): void {
    this.readyState = 2;
  }
}

/** Internal destinations only, deduped by pathname — `?view=backlog` is the same screen. */
function normalize(href: string): string | null {
  if (!href.startsWith("/") || href.startsWith("//")) return null;
  const path = href.split("#")[0]!.split("?")[0]!;
  if (path === "") return null;
  return path.length > 1 && path.endsWith("/") ? path.slice(0, -1) : path;
}

interface Visit {
  /** Route ids this URL matched — what "we got here" means to the router. */
  routeIds: string[];
  /** Every internal destination a mouse could follow from this screen. */
  links: string[];
}

/**
 * Render one URL the way a person would see it, then read off where they could go next:
 * anchors already on screen, plus anchors and router links revealed by opening every menu
 * button. Menus matter — the user menu in the top bar is exactly where workspace settings
 * belongs, and a crawler that never opens it would have "confirmed" the bug.
 */
async function visit(path: string): Promise<Visit> {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  const router = createRouter({
    routeTree,
    history: createMemoryHistory({ initialEntries: [path] }),
  });
  const { container, unmount } = render(
    <QueryClientProvider client={qc}>
      <RouterProvider router={router} />
    </QueryClientProvider>,
  );
  await waitFor(() => {
    expect(container.childElementCount).toBeGreaterThan(0);
  });
  // Let the queries this screen fires settle into their loaded render; a list that has not
  // arrived yet has no rows, and rows are where most links live.
  await act(async () => {
    await new Promise((r) => setTimeout(r, 250));
  });

  // Open every menu. A menu item is as clickable as a link and hides its destination until
  // the menu is open.
  for (const button of container.querySelectorAll('[aria-haspopup="menu"]')) {
    await act(async () => {
      fireEvent.click(button);
      await new Promise((r) => setTimeout(r, 0));
    });
  }

  const links = new Set<string>();
  for (const a of container.querySelectorAll("a[href]")) {
    const to = normalize(a.getAttribute("href") ?? "");
    if (to !== null) links.add(to);
  }
  const routeIds = router.state.matches.map((m) => m.routeId);

  unmount();
  cleanup();
  qc.clear();
  return { routeIds, links: [...links].sort() };
}

/** Every route the tree defines, with the paths that identify it in a failure message. */
function allRoutes(): Array<{ id: string; fullPath: string }> {
  const router = createRouter({
    routeTree,
    history: createMemoryHistory({ initialEntries: ["/"] }),
  });
  return Object.entries(router.routesById as Record<string, { fullPath?: string }>)
    .filter(([id]) => id !== "__root__")
    .map(([id, r]) => ({ id, fullPath: r.fullPath ?? id }));
}

beforeEach(() => {
  unmatched.clear();
  installFetchStub();
  vi.stubGlobal("EventSource", FakeEventSource);
});

afterEach(() => {
  vi.unstubAllGlobals();
});

describe("every screen is reachable by clicking, starting at /", () => {
  it("no route is an orphan", async () => {
    const reached = new Set<string>();
    const seen = new Set<string>(["/"]);
    const queue: string[] = ["/"];
    /** Which screen first offered each destination — the breadcrumb a failure needs. */
    const via = new Map<string, string>([["/", "the app's entry point"]]);

    while (queue.length > 0) {
      const path = queue.shift()!;
      const { routeIds, links } = await visit(path);
      for (const id of routeIds) reached.add(id);
      for (const link of links) {
        if (seen.has(link)) continue;
        seen.add(link);
        via.set(link, path);
        queue.push(link);
      }
    }

    const orphans = allRoutes().filter(
      (r) => !reached.has(r.id) && URL_ONLY[r.id] === undefined,
    );
    expect(
      orphans,
      orphans.length === 0
        ? ""
        : [
            "These routes exist but nothing in the app links to them — a signed-in user",
            "starting at / cannot click their way there:",
            ...orphans.map((r) => `  ${r.fullPath}  (route id ${r.id})`),
            "",
            "Add a link (the top bar's user menu, the left rail, a tab, a list row), or —",
            "if the route really is URL-only, like the auth screens — add it to URL_ONLY",
            "with the reason.",
            "",
            `Crawled ${seen.size} URLs: ${[...seen].sort().join(", ")}`,
            unmatched.size > 0
              ? `Unstubbed GETs (a missing fixture can hide a link): ${[...unmatched].sort().join(", ")}`
              : "",
          ].join("\n"),
    ).toEqual([]);
  }, 120_000);

  it("workspace settings and the audit log are in the crawl, not just in the router", async () => {
    // The two screens the missing link actually cost us, pinned by name so a future
    // refactor that drops the menu item fails here with an unmistakable message.
    const home = await visit("/");
    expect(
      home.links,
      `The top bar offers: ${[...home.links].join(", ")}. Workspace settings holds the Claude ` +
        "credentials every agent needs; docs and `lexicode doctor` tell people to go to " +
        "Settings → Credentials, so the app has to have a Settings.",
    ).toContain("/settings");

    const settings = await visit("/settings");
    expect(settings.links).toContain("/settings/audit");
  }, 60_000);

  it("every URL_ONLY exemption names a route that still exists, and says why", () => {
    const ids = new Set(allRoutes().map((r) => r.id));
    for (const [id, reason] of Object.entries(URL_ONLY)) {
      expect(ids, `URL_ONLY exempts "${id}", which is no longer a route`).toContain(id);
      expect(reason.length, `URL_ONLY["${id}"] needs a real reason`).toBeGreaterThan(20);
    }
  });
});
