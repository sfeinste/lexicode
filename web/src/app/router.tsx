/*
 * The route tree — every URL in UI spec §1, plus the three auth screens, a 404 and error
 * boundaries. Code-based TanStack Router (D-1: typed routes; selection state lives in the
 * URL, so search params are validated here and nowhere else).
 *
 * Conventions this file establishes (interaction rule 12):
 * - Filters and view switches are search params validated by the route that owns them
 *   (`?view=backlog` on the board, `?step=&line=&level=` on a run).
 * - Auth screens are root-level siblings of the shell: no chrome for signed-out users.
 * - Everything else nests under the pathless "shell" layout, which owns the auth gate,
 *   the chrome and the global key bindings.
 */
import {
  createRootRoute,
  createRoute,
  createRouter,
} from "@tanstack/react-router";

import { ErrorScreen } from "../routes/ErrorScreen";
import { NotFound } from "../routes/NotFound";
import { InvitePage } from "../routes/auth/InvitePage";
import { LoginPage } from "../routes/auth/LoginPage";
import { SetupPage } from "../routes/auth/SetupPage";
import { HomePage } from "../routes/home/HomePage";
import { InboxPage } from "../routes/inbox/InboxPage";
import { AgentDetailPage } from "../routes/project/agents/AgentDetailPage";
import { AgentsPage } from "../routes/project/agents/AgentsPage";
import { BoardPage } from "../routes/project/board/BoardPage";
import { BootstrapPage } from "../routes/project/bootstrap/BootstrapPage";
import { OverviewPage } from "../routes/project/overview/OverviewPage";
import { RunDetailPage } from "../routes/project/runs/RunDetailPage";
import { RunsPage } from "../routes/project/runs/RunsPage";
import { ProjectSettingsPage } from "../routes/project/settings/ProjectSettingsPage";
import { TicketPage } from "../routes/project/ticket/TicketPage";
import { TriagePage } from "../routes/project/triage/TriagePage";
import { TriggerEditorPage } from "../routes/project/triggers/TriggerEditorPage";
import { TriggersPage } from "../routes/project/triggers/TriggersPage";
import { WikiIndexPage } from "../routes/project/wiki/WikiIndexPage";
import { WikiPagePage } from "../routes/project/wiki/WikiPagePage";
import { WorkspaceSettingsPage } from "../routes/workspace-settings/WorkspaceSettingsPage";
import { AppShell } from "./shell/AppShell";
import { ProjectLayout } from "./shell/ProjectLayout";

const rootRoute = createRootRoute({
  notFoundComponent: NotFound,
  errorComponent: ErrorScreen,
});

// ---- auth (outside the chrome) ----------------------------------------------------------

const setupRoute = createRoute({
  getParentRoute: () => rootRoute,
  path: "/setup",
  component: SetupPage,
});

const loginRoute = createRoute({
  getParentRoute: () => rootRoute,
  path: "/login",
  component: LoginPage,
});

const inviteRoute = createRoute({
  getParentRoute: () => rootRoute,
  path: "/invite/$token",
  component: InvitePage,
});

// ---- the shell (auth gate + chrome) ------------------------------------------------------

const shellRoute = createRoute({
  getParentRoute: () => rootRoute,
  id: "shell",
  component: AppShell,
});

const homeRoute = createRoute({
  getParentRoute: () => shellRoute,
  path: "/",
  component: HomePage,
});

const inboxRoute = createRoute({
  getParentRoute: () => shellRoute,
  path: "/inbox",
  component: InboxPage,
});

const workspaceSettingsRoute = createRoute({
  getParentRoute: () => shellRoute,
  path: "/settings",
  component: WorkspaceSettingsPage,
});

// ---- the project: one URL prefix, tabs beneath (IA rule 3) -------------------------------

const projectRoute = createRoute({
  getParentRoute: () => shellRoute,
  path: "/p/$key",
  component: ProjectLayout,
});

const overviewRoute = createRoute({
  getParentRoute: () => projectRoute,
  path: "/",
  component: OverviewPage,
});

// The bootstrap checklist (S15): connect a repo on the Overview gate, review the scan here.
// Reached again later via "Re-scan repository" in project settings.
const bootstrapRoute = createRoute({
  getParentRoute: () => projectRoute,
  path: "bootstrap",
  component: BootstrapPage,
});

/**
 * Board selection state, all in the URL (interaction rule 12): `?view=backlog` is the
 * backlog — same view, filtered route (§1); `?layout=list` is the ⌘B list rendering (absent =
 * board); `?group_by=` the grouping (absent = status); the filter chips (`assignee`,
 * `delegate`, `label`, `priority` — "none" filters for the unset value), the search box
 * (`q`), the keyboard selection (`sel`, a ticket key) and the ⇧V display properties (`show`,
 * a comma list; absent = everything on).
 */
export interface BoardSearch {
  view?: "backlog";
  layout?: "list";
  group_by?: "status" | "assignee" | "delegate" | "priority" | "label";
  assignee?: string;
  delegate?: string;
  label?: string;
  priority?: "none" | "low" | "medium" | "high" | "urgent";
  q?: string;
  sel?: string;
  show?: string;
}

const optStr = (v: unknown): string | undefined =>
  typeof v === "string" && v !== "" ? v : undefined;

const boardRoute = createRoute({
  getParentRoute: () => projectRoute,
  path: "board",
  component: BoardPage,
  validateSearch: (search: Record<string, unknown>): BoardSearch => ({
    view: search.view === "backlog" ? "backlog" : undefined,
    layout: search.layout === "list" ? "list" : undefined,
    group_by:
      search.group_by === "status" ||
      search.group_by === "assignee" ||
      search.group_by === "delegate" ||
      search.group_by === "priority" ||
      search.group_by === "label"
        ? search.group_by
        : undefined,
    assignee: optStr(search.assignee),
    delegate: optStr(search.delegate),
    label: optStr(search.label),
    priority:
      search.priority === "none" ||
      search.priority === "low" ||
      search.priority === "medium" ||
      search.priority === "high" ||
      search.priority === "urgent"
        ? search.priority
        : undefined,
    q: optStr(search.q),
    sel: optStr(search.sel),
    show: optStr(search.show),
  }),
});

const triageRoute = createRoute({
  getParentRoute: () => projectRoute,
  path: "triage",
  component: TriagePage,
});

const ticketRoute = createRoute({
  getParentRoute: () => projectRoute,
  path: "t/$ticket",
  component: TicketPage,
});

const wikiRoute = createRoute({
  getParentRoute: () => projectRoute,
  path: "wiki",
});

/** Wiki index state in the URL (interaction rule 12): `?tag=` filters to one tag. */
export interface WikiSearch {
  tag?: string;
}

const wikiIndexRoute = createRoute({
  getParentRoute: () => wikiRoute,
  path: "/",
  component: WikiIndexPage,
  validateSearch: (search: Record<string, unknown>): WikiSearch => ({
    tag: optStr(search.tag),
  }),
});

const wikiPageRoute = createRoute({
  getParentRoute: () => wikiRoute,
  path: "$slug",
  component: WikiPagePage,
});

const runsRoute = createRoute({
  getParentRoute: () => projectRoute,
  path: "runs",
});

/**
 * Run-list filter state, in the URL (interaction rule 12): `?state=` a comma list of run
 * states, `?agent=` / `?ticket=` ids, `?view=` a saved-view name ("all" is the explicit
 * everything view). No search at all means the default **Needs attention** view (§5.7).
 */
export interface RunsSearch {
  state?: string;
  agent?: string;
  ticket?: string;
  view?: string;
}

const runsIndexRoute = createRoute({
  getParentRoute: () => runsRoute,
  path: "/",
  component: RunsPage,
  validateSearch: (search: Record<string, unknown>): RunsSearch => ({
    state: optStr(search.state),
    agent: optStr(search.agent),
    ticket: optStr(search.ticket),
    view: optStr(search.view),
  }),
});

/** Run-detail selection state: sharing a step or a log line is copying the URL (§5.7). */
export interface RunDetailSearch {
  step?: number;
  line?: number;
  level?: "summary" | "normal" | "verbose";
}

const runDetailRoute = createRoute({
  getParentRoute: () => runsRoute,
  path: "$id",
  component: RunDetailPage,
  validateSearch: (search: Record<string, unknown>): RunDetailSearch => ({
    step: typeof search.step === "number" ? search.step : undefined,
    line: typeof search.line === "number" ? search.line : undefined,
    level:
      search.level === "summary" || search.level === "normal" || search.level === "verbose"
        ? search.level
        : undefined,
  }),
});

const agentsRoute = createRoute({
  getParentRoute: () => projectRoute,
  path: "agents",
});

const agentsIndexRoute = createRoute({
  getParentRoute: () => agentsRoute,
  path: "/",
  component: AgentsPage,
});

const agentDetailRoute = createRoute({
  getParentRoute: () => agentsRoute,
  path: "$id",
  component: AgentDetailPage,
});

const triggersRoute = createRoute({
  getParentRoute: () => projectRoute,
  path: "triggers",
});

const triggersIndexRoute = createRoute({
  getParentRoute: () => triggersRoute,
  path: "/",
  component: TriggersPage,
});

const triggerEditorRoute = createRoute({
  getParentRoute: () => triggersRoute,
  path: "$id",
  component: TriggerEditorPage,
});

// `/p/:key/settings/*` — sections (general, board, wiki, repo, members, danger) are splat
// segments until their owning stories land real screens.
const projectSettingsRoute = createRoute({
  getParentRoute: () => projectRoute,
  path: "settings",
});

const projectSettingsIndexRoute = createRoute({
  getParentRoute: () => projectSettingsRoute,
  path: "/",
  component: ProjectSettingsPage,
});

const projectSettingsSectionRoute = createRoute({
  getParentRoute: () => projectSettingsRoute,
  path: "$",
  component: ProjectSettingsPage,
});

const routeTree = rootRoute.addChildren([
  setupRoute,
  loginRoute,
  inviteRoute,
  shellRoute.addChildren([
    homeRoute,
    inboxRoute,
    workspaceSettingsRoute,
    projectRoute.addChildren([
      overviewRoute,
      bootstrapRoute,
      boardRoute,
      triageRoute,
      ticketRoute,
      wikiRoute.addChildren([wikiIndexRoute, wikiPageRoute]),
      runsRoute.addChildren([runsIndexRoute, runDetailRoute]),
      agentsRoute.addChildren([agentsIndexRoute, agentDetailRoute]),
      triggersRoute.addChildren([triggersIndexRoute, triggerEditorRoute]),
      projectSettingsRoute.addChildren([
        projectSettingsIndexRoute,
        projectSettingsSectionRoute,
      ]),
    ]),
  ]),
]);

export const router = createRouter({
  routeTree,
  defaultNotFoundComponent: NotFound,
  defaultErrorComponent: ErrorScreen,
});

declare module "@tanstack/react-router" {
  interface Register {
    router: typeof router;
  }
}
