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

export const auditApi = {
  list: (params: URLSearchParams, signal?: AbortSignal) =>
    api<AuditListResponse>("GET", `/audit?${params}`, { signal }),
};
