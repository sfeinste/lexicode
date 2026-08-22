/*
 * The typed fetch wrapper. Every request in the SPA goes through api(); every error becomes
 * an ApiProblem carrying the server's stable problem+json `type` slug (architecture §14).
 *
 * Auth handling, per S07: a 401 of type `setup_required` sends the tab to /setup (the
 * database has no users yet); any other 401 sends it to /login — except on requests that opt
 * out (the auth forms themselves: a failed login must render its error, not loop through a
 * redirect).
 */
import type { components } from "./types.gen";

export type Problem = components["schemas"]["Problem"];
export type User = components["schemas"]["User"];
export type Invite = components["schemas"]["Invite"];
export type ModuleStatus = components["schemas"]["ModuleStatus"];
export type AuditListResponse = components["schemas"]["AuditListResponse"];
export type Project = components["schemas"]["Project"];
export type ProjectListItem = components["schemas"]["ProjectListItem"];
export type ProjectListResponse = components["schemas"]["ProjectListResponse"];
export type ProjectOverview = components["schemas"]["ProjectOverview"];
export type InheritedInt = components["schemas"]["InheritedInt"];
export type CreateProjectRequest = components["schemas"]["CreateProjectRequest"];
export type UpdateProjectRequest = components["schemas"]["UpdateProjectRequest"];
export type Column = components["schemas"]["Column"];
export type ColumnCategory = components["schemas"]["ColumnCategory"];
export type ColumnListResponse = components["schemas"]["ColumnListResponse"];
export type CreateColumnRequest = components["schemas"]["CreateColumnRequest"];
export type UpdateColumnRequest = components["schemas"]["UpdateColumnRequest"];
export type Ticket = components["schemas"]["Ticket"];
export type TicketPriority = components["schemas"]["TicketPriority"];
export type TicketDetail = components["schemas"]["TicketDetail"];
export type TicketListResponse = components["schemas"]["TicketListResponse"];
export type CreateTicketRequest = components["schemas"]["CreateTicketRequest"];
export type UpdateTicketRequest = components["schemas"]["UpdateTicketRequest"];
export type MoveTicketRequest = components["schemas"]["MoveTicketRequest"];
export type SubticketsRequest = components["schemas"]["SubticketsRequest"];
export type Criterion = components["schemas"]["Criterion"];
export type CreateCriterionRequest = components["schemas"]["CreateCriterionRequest"];
export type UpdateCriterionRequest = components["schemas"]["UpdateCriterionRequest"];
export type Label = components["schemas"]["Label"];
export type LabelListResponse = components["schemas"]["LabelListResponse"];
export type CreateLabelRequest = components["schemas"]["CreateLabelRequest"];
export type UpdateLabelRequest = components["schemas"]["UpdateLabelRequest"];
export type TicketStreamResponse = components["schemas"]["TicketStreamResponse"];
export type TicketStreamEntry = components["schemas"]["TicketStreamEntry"];
export type CreateCommentRequest = components["schemas"]["CreateCommentRequest"];
export type CommentResponse = components["schemas"]["CommentResponse"];
export type CommentRunRequest = components["schemas"]["CommentRunRequest"];
export type Member = components["schemas"]["Member"];
export type MemberListResponse = components["schemas"]["MemberListResponse"];
export type WorkspaceSettings = components["schemas"]["WorkspaceSettings"];
export type UpdateWorkspaceSettingsRequest =
  components["schemas"]["UpdateWorkspaceSettingsRequest"];
export type OverviewRepo = components["schemas"]["OverviewRepo"];
export type Repo = components["schemas"]["Repo"];
export type RepoStatus = components["schemas"]["RepoStatus"];
export type ConnectRepoRequest = components["schemas"]["ConnectRepoRequest"];
export type BootstrapPreview = components["schemas"]["BootstrapPreview"];
export type IssueCandidate = components["schemas"]["IssueCandidate"];
export type DocCandidate = components["schemas"]["DocCandidate"];
export type TriggerCandidate = components["schemas"]["TriggerCandidate"];
export type AgentCandidate = components["schemas"]["AgentCandidate"];
export type AgentScope = components["schemas"]["AgentScope"];
export type Agent = components["schemas"]["Agent"];
export type AgentListResponse = components["schemas"]["AgentListResponse"];
export type AgentPermissions = components["schemas"]["AgentPermissions"];
export type AgentAutonomy = components["schemas"]["AgentAutonomy"];
export type CreateAgentRequest = components["schemas"]["CreateAgentRequest"];
export type UpdateAgentRequest = components["schemas"]["UpdateAgentRequest"];
export type StarterRosterResult = components["schemas"]["StarterRosterResult"];
export type Directive = components["schemas"]["Directive"];
export type DirectiveListResponse = components["schemas"]["DirectiveListResponse"];
export type SaveDirectiveRequest = components["schemas"]["SaveDirectiveRequest"];
export type SaveDirectiveResponse = components["schemas"]["SaveDirectiveResponse"];
export type BootstrapApplyRequest = components["schemas"]["BootstrapApplyRequest"];
export type BootstrapApplyResult = components["schemas"]["BootstrapApplyResult"];
export type Secret = components["schemas"]["Secret"];
export type SecretListResponse = components["schemas"]["SecretListResponse"];
export type SetSecretRequest = components["schemas"]["SetSecretRequest"];
export type RenameSecretRequest = components["schemas"]["RenameSecretRequest"];

export const API_BASE = "/api/v1";

export class ApiProblem extends Error {
  readonly type: string;
  readonly title: string;
  readonly status: number;
  readonly detail?: string;
  readonly errors?: Problem["errors"];

  constructor(p: Problem) {
    super(p.detail || p.title);
    this.name = "ApiProblem";
    this.type = p.type;
    this.title = p.title;
    this.status = p.status;
    this.detail = p.detail;
    this.errors = p.errors;
  }
}

/** True for a 4xx problem — the class TanStack Query must never retry. */
export function isClientProblem(err: unknown): boolean {
  return err instanceof ApiProblem && err.status >= 400 && err.status < 500;
}

/**
 * Where auth redirects go. The app installs the router's navigate here at boot;
 * the default hard-navigates, which also works outside the router (and in tests is a no-op
 * target to stub).
 */
let navigateTo = (path: string): void => {
  window.location.assign(path);
};

export function installAuthNavigator(fn: (path: string) => void): void {
  navigateTo = fn;
}

const AUTH_ROUTES = ["/setup", "/login", "/invite/"];

function onAuthRoute(): boolean {
  const p = window.location.pathname;
  return AUTH_ROUTES.some((r) => p === r || p.startsWith(r));
}

export interface ApiOptions {
  body?: unknown;
  /** The auth screens set this: their 401s are form errors, not session problems. */
  skipAuthRedirect?: boolean;
  signal?: AbortSignal;
}

export async function api<T>(method: string, path: string, opts: ApiOptions = {}): Promise<T> {
  const res = await fetch(API_BASE + path, {
    method,
    headers: opts.body !== undefined ? { "Content-Type": "application/json" } : undefined,
    body: opts.body !== undefined ? JSON.stringify(opts.body) : undefined,
    signal: opts.signal,
    credentials: "same-origin",
  });

  if (res.ok) {
    if (res.status === 204) return undefined as T;
    return (await res.json()) as T;
  }

  let problem: Problem;
  try {
    problem = (await res.json()) as Problem;
  } catch {
    problem = { type: "internal", title: res.statusText || "Request failed", status: res.status };
  }

  if (res.status === 401 && !opts.skipAuthRedirect && !onAuthRoute()) {
    navigateTo(problem.type === "setup_required" ? "/setup" : "/login");
  }
  throw new ApiProblem(problem);
}

// ---- Typed endpoint helpers (the routes that exist today; grow with the API) -------------

export const authApi = {
  me: (signal?: AbortSignal) => api<User>("GET", "/auth/me", { signal, skipAuthRedirect: true }),
  setup: (body: components["schemas"]["SetupRequest"]) =>
    api<User>("POST", "/auth/setup", { body, skipAuthRedirect: true }),
  login: (body: components["schemas"]["LoginRequest"]) =>
    api<User>("POST", "/auth/login", { body, skipAuthRedirect: true }),
  logout: () => api<void>("POST", "/auth/logout"),
  redeemInvite: (token: string, body: components["schemas"]["RedeemRequest"]) =>
    api<User>("POST", `/invites/${encodeURIComponent(token)}/redeem`, {
      body,
      skipAuthRedirect: true,
    }),
  createInvite: () => api<Invite>("POST", "/invites"),
};

export const systemApi = {
  modules: (signal?: AbortSignal) =>
    api<components["schemas"]["ModulesResponse"]>("GET", "/system/modules", { signal }),
};

export const projectsApi = {
  list: (opts?: { archived?: boolean }, signal?: AbortSignal) =>
    api<ProjectListResponse>(
      "GET",
      opts?.archived ? "/projects?archived=1" : "/projects",
      { signal },
    ),
  create: (body: CreateProjectRequest) => api<Project>("POST", "/projects", { body }),
  get: (key: string, signal?: AbortSignal) =>
    api<Project>("GET", `/projects/${encodeURIComponent(key)}`, { signal }),
  update: (key: string, body: UpdateProjectRequest) =>
    api<Project>("PATCH", `/projects/${encodeURIComponent(key)}`, { body }),
  overview: (key: string, signal?: AbortSignal) =>
    api<ProjectOverview>("GET", `/projects/${encodeURIComponent(key)}/overview`, { signal }),
};

/**
 * Secrets are write-only (D-16): `set` carries a value in; nothing ever carries one out.
 * Project scope and workspace scope share shapes; workspace routes are owner-only.
 */
export const secretsApi = {
  list: (projectKey: string, signal?: AbortSignal) =>
    api<SecretListResponse>(
      "GET",
      `/projects/${encodeURIComponent(projectKey)}/secrets`,
      { signal },
    ),
  set: (projectKey: string, body: SetSecretRequest) =>
    api<Secret>("POST", `/projects/${encodeURIComponent(projectKey)}/secrets`, { body }),
  rename: (projectKey: string, id: string, body: RenameSecretRequest) =>
    api<Secret>(
      "PATCH",
      `/projects/${encodeURIComponent(projectKey)}/secrets/${encodeURIComponent(id)}`,
      { body },
    ),
  remove: (projectKey: string, id: string) =>
    api<void>(
      "DELETE",
      `/projects/${encodeURIComponent(projectKey)}/secrets/${encodeURIComponent(id)}`,
    ),
  workspaceList: (signal?: AbortSignal) =>
    api<SecretListResponse>("GET", "/workspace/secrets", { signal }),
  workspaceSet: (body: SetSecretRequest) => api<Secret>("POST", "/workspace/secrets", { body }),
  workspaceRename: (id: string, body: RenameSecretRequest) =>
    api<Secret>("PATCH", `/workspace/secrets/${encodeURIComponent(id)}`, { body }),
  workspaceRemove: (id: string) =>
    api<void>("DELETE", `/workspace/secrets/${encodeURIComponent(id)}`),
};

export const columnsApi = {
  list: (projectKey: string, signal?: AbortSignal) =>
    api<ColumnListResponse>(
      "GET",
      `/projects/${encodeURIComponent(projectKey)}/columns`,
      { signal },
    ),
  create: (projectKey: string, body: CreateColumnRequest) =>
    api<Column>("POST", `/projects/${encodeURIComponent(projectKey)}/columns`, { body }),
  update: (id: string, body: UpdateColumnRequest) =>
    api<Column>("PATCH", `/columns/${encodeURIComponent(id)}`, { body }),
  remove: (id: string, destinationColumnId?: string) =>
    api<void>(
      "DELETE",
      `/columns/${encodeURIComponent(id)}${
        destinationColumnId
          ? `?destination_column_id=${encodeURIComponent(destinationColumnId)}`
          : ""
      }`,
    ),
};

export const ticketsApi = {
  list: (projectKey: string, opts?: { archived?: boolean }, signal?: AbortSignal) =>
    api<TicketListResponse>(
      "GET",
      `/projects/${encodeURIComponent(projectKey)}/tickets${opts?.archived ? "?archived=1" : ""}`,
      { signal },
    ),
  create: (projectKey: string, body: CreateTicketRequest) =>
    api<Ticket>("POST", `/projects/${encodeURIComponent(projectKey)}/tickets`, { body }),
  get: (id: string, signal?: AbortSignal) =>
    api<TicketDetail>("GET", `/tickets/${encodeURIComponent(id)}`, { signal }),
  update: (id: string, body: UpdateTicketRequest) =>
    api<Ticket>("PATCH", `/tickets/${encodeURIComponent(id)}`, { body }),
  archive: (id: string, confirmActiveRuns = 0) =>
    api<void>(
      "DELETE",
      `/tickets/${encodeURIComponent(id)}?confirm_active_runs=${confirmActiveRuns}`,
    ),
  unarchive: (id: string) =>
    api<Ticket>("POST", `/tickets/${encodeURIComponent(id)}/unarchive`),
  move: (id: string, body: MoveTicketRequest) =>
    api<Ticket>("POST", `/tickets/${encodeURIComponent(id)}/move`, { body }),
  subtickets: (id: string, body: SubticketsRequest) =>
    api<TicketListResponse>("POST", `/tickets/${encodeURIComponent(id)}/subtickets`, { body }),
  stream: (id: string, signal?: AbortSignal) =>
    api<TicketStreamResponse>("GET", `/tickets/${encodeURIComponent(id)}/stream`, { signal }),
  comment: (id: string, body: CreateCommentRequest) =>
    api<CommentResponse>("POST", `/tickets/${encodeURIComponent(id)}/stream`, { body }),
  addCriterion: (id: string, body: CreateCriterionRequest) =>
    api<Criterion>("POST", `/tickets/${encodeURIComponent(id)}/criteria`, { body }),
  attachLabel: (id: string, labelId: string) =>
    api<void>(
      "PUT",
      `/tickets/${encodeURIComponent(id)}/labels/${encodeURIComponent(labelId)}`,
    ),
  detachLabel: (id: string, labelId: string) =>
    api<void>(
      "DELETE",
      `/tickets/${encodeURIComponent(id)}/labels/${encodeURIComponent(labelId)}`,
    ),
};

export const criteriaApi = {
  update: (id: string, body: UpdateCriterionRequest) =>
    api<Criterion>("PATCH", `/criteria/${encodeURIComponent(id)}`, { body }),
  remove: (id: string) => api<void>("DELETE", `/criteria/${encodeURIComponent(id)}`),
};

export const labelsApi = {
  list: (projectKey: string, signal?: AbortSignal) =>
    api<LabelListResponse>("GET", `/projects/${encodeURIComponent(projectKey)}/labels`, {
      signal,
    }),
  create: (projectKey: string, body: CreateLabelRequest) =>
    api<Label>("POST", `/projects/${encodeURIComponent(projectKey)}/labels`, { body }),
  update: (id: string, body: UpdateLabelRequest) =>
    api<Label>("PATCH", `/labels/${encodeURIComponent(id)}`, { body }),
  remove: (id: string) => api<void>("DELETE", `/labels/${encodeURIComponent(id)}`),
};

export const usersApi = {
  /** The member directory (S12): public display identity only, non-archived users. */
  list: (signal?: AbortSignal) => api<MemberListResponse>("GET", "/users", { signal }),
};

export const workspaceApi = {
  settings: (signal?: AbortSignal) =>
    api<WorkspaceSettings>("GET", "/workspace/settings", { signal }),
  update: (body: UpdateWorkspaceSettingsRequest) =>
    api<WorkspaceSettings>("PUT", "/workspace/settings", { body }),
};

export const auditApi = {
  list: (params: URLSearchParams, signal?: AbortSignal) =>
    api<AuditListResponse>("GET", `/audit?${params}`, { signal }),
};

/**
 * Repo connect and project bootstrap (S15). The PAT crosses in the connect request only —
 * responses carry `has_token`, never the token (D-16).
 */
export const repoApi = {
  status: (projectKey: string, signal?: AbortSignal) =>
    api<RepoStatus>("GET", `/projects/${encodeURIComponent(projectKey)}/repo`, { signal }),
  connect: (projectKey: string, body: ConnectRepoRequest) =>
    api<Repo>("POST", `/projects/${encodeURIComponent(projectKey)}/repo`, { body }),
  disconnect: (projectKey: string) =>
    api<void>("DELETE", `/projects/${encodeURIComponent(projectKey)}/repo`),
};

/**
 * Agents (S16). The list powers both the roster (all agents) and — with eligible=1 — the
 * delegate pickers and mention autocomplete (enabled, non-archived only). Directive saves are
 * append-only versioning with a server-side no-op guard for unchanged bodies.
 */
export const agentsApi = {
  list: (projectKey: string, opts?: { eligible?: boolean }, signal?: AbortSignal) =>
    api<AgentListResponse>(
      "GET",
      `/projects/${encodeURIComponent(projectKey)}/agents${opts?.eligible ? "?eligible=1" : ""}`,
      { signal },
    ),
  create: (projectKey: string, body: CreateAgentRequest) =>
    api<Agent>("POST", `/projects/${encodeURIComponent(projectKey)}/agents`, { body }),
  starter: (projectKey: string) =>
    api<StarterRosterResult>(
      "POST",
      `/projects/${encodeURIComponent(projectKey)}/agents/starter`,
    ),
  get: (id: string, signal?: AbortSignal) =>
    api<Agent>("GET", `/agents/${encodeURIComponent(id)}`, { signal }),
  update: (id: string, body: UpdateAgentRequest) =>
    api<Agent>("PATCH", `/agents/${encodeURIComponent(id)}`, { body }),
  archive: (id: string) => api<void>("DELETE", `/agents/${encodeURIComponent(id)}`),
  saveDirective: (id: string, body: SaveDirectiveRequest) =>
    api<SaveDirectiveResponse>("PUT", `/agents/${encodeURIComponent(id)}/directive`, { body }),
  directives: (id: string, signal?: AbortSignal) =>
    api<DirectiveListResponse>("GET", `/agents/${encodeURIComponent(id)}/directives`, {
      signal,
    }),
};

export const bootstrapApi = {
  preview: (projectKey: string) =>
    api<BootstrapPreview>(
      "POST",
      `/projects/${encodeURIComponent(projectKey)}/bootstrap/preview`,
    ),
  apply: (projectKey: string, body: BootstrapApplyRequest) =>
    api<BootstrapApplyResult>(
      "POST",
      `/projects/${encodeURIComponent(projectKey)}/bootstrap/apply`,
      { body },
    ),
};
