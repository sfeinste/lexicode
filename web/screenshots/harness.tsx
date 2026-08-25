/*
 * The screenshot harness (LEXI-13). Dev-only: it is never imported by src/main.tsx, has its
 * own HTML entry, and is built by screenshots/capture.mjs, not by `npm run build`.
 *
 * It mounts the app's REAL route tree over a memory history with `fetch` stubbed from
 * screenshots/fixtures.ts, so what a screenshot shows is the shipped components rendering
 * shipped markup — not a mock-up. Query parameters drive it:
 *
 *   ?path=/p/PAY/runs/r-4821   which screen to render
 *   ?theme=dark|light          forces the theme (writes data-theme, like the user menu)
 *   ?state=failed              swaps in the terminal-failure variant of the run
 */
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { createMemoryHistory, createRouter, RouterProvider } from "@tanstack/react-router";
import { StrictMode } from "react";
import { createRoot } from "react-dom/client";

import { routeTree } from "../src/app/router";
import "../src/styles/reset.css";
import "../src/styles/tokens.css";
import { FAILED_RUN, FIXTURES, RUN_ID } from "./fixtures";

const params = new URLSearchParams(window.location.search);
const path = params.get("path") ?? "/";
const state = params.get("state");

// `?theme=` is applied by the inline script in index.html — it has to run before this
// module is even parsed, because the persisted UI store hydrates at import time.

function jsonResponse(body: unknown, status = 200): Response {
  return new Response(JSON.stringify(body), {
    status,
    headers: { "Content-Type": "application/json" },
  });
}

window.fetch = (async (url: string) => {
  const u = String(url);
  for (const [re, body] of FIXTURES) {
    if (re.test(u)) {
      if (state === "failed" && new RegExp(`/runs/${RUN_ID}$`).test(u)) {
        const detail = body as { run: unknown };
        return jsonResponse({ ...detail, run: FAILED_RUN });
      }
      return jsonResponse(body);
    }
  }
  return jsonResponse({ type: "not_found", title: "Not found", status: 404 }, 404);
}) as typeof fetch;

/** jsdom-free, but the harness still runs without a server, so SSE is stubbed too. */
class NoEventSource {
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
window.EventSource = NoEventSource as unknown as typeof EventSource;

const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false } } });
const router = createRouter({
  routeTree,
  history: createMemoryHistory({ initialEntries: [path] }),
});

createRoot(document.getElementById("root")!).render(
  <StrictMode>
    <QueryClientProvider client={queryClient}>
      <RouterProvider router={router} />
    </QueryClientProvider>
  </StrictMode>,
);
