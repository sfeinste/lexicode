/*
 * S36 acceptance: answering a question works FROM the home needs-you card, without a
 * navigation — the card's primary action expands the SAME S24 respond components the run
 * detail renders (InlineElicitation → ElicitationDetail → QuestionRow), and submitting
 * posts to the elicitation respond endpoint. The router is mocked to plain anchors, so any
 * navigation attempt would be visible here as a broken assumption, not a page change.
 */
import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { afterEach, describe, expect, it, vi } from "vitest";

import type { NeedsYouRun } from "../../lib/api/client";

vi.mock("@tanstack/react-router", () => ({
  // The card and the S24 renderers only need Link and useNavigate; plain anchors keep the
  // test free of a route tree.
  Link: ({ children }: { children?: React.ReactNode }) => (
    <a data-mock-link href="#">
      {children}
    </a>
  ),
  useNavigate: () => () => {},
}));

import { NeedsYouCard } from "./HomePage";

const RUN_ID = "run-1";
const EL_ID = "el-1";

const detail = {
  run: { id: RUN_ID, agent_id: "agent-1", state: "needs_input" },
  outputs: [],
  context: [],
  messages: [],
  elicitations: [
    {
      id: EL_ID,
      run_id: RUN_ID,
      activity_seq: 5,
      kind: "question",
      state: "pending",
      request: {},
      created_at: "2026-08-22T12:00:00Z",
    },
  ],
};

const activities = {
  activities: [
    {
      seq: 5,
      type: "elicitation",
      level: 0,
      tool_name: "mcp__lexicode__ask_human",
      group_key: "mcp__lexicode__ask_human",
      title: "Question: Which retry strategy?",
      payload: {
        questions: [
          {
            question: "Which retry strategy?",
            header: "Retries",
            multiSelect: false,
            options: [
              { label: "Exponential backoff", description: "Doubles the delay each try" },
              { label: "Fixed interval", description: "" },
            ],
          },
        ],
      },
      ok: null,
      attempt: 1,
      duration_ms: null,
      queued_ms: null,
      model_ms: null,
      tool_ms: null,
      cost_cents: 0,
      tokens_in: 0,
      tokens_out: 0,
      created_at: "2026-08-22T12:00:00Z",
    },
  ],
};

function jsonResponse(body: unknown): Response {
  return new Response(JSON.stringify(body), {
    status: 200,
    headers: { "Content-Type": "application/json" },
  });
}

const questionRow: NeedsYouRun = {
  kind: "run",
  id: RUN_ID,
  project_key: "PAY",
  ticket_id: "t1",
  ticket_key: "PAY-14",
  ticket_title: "Add idempotency keys",
  agent: "Dev",
  flavor: "question",
  status: "needs_input",
  started_at: "2026-08-22T12:00:00Z",
};

function renderCard(row: NeedsYouRun) {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return render(
    <QueryClientProvider client={qc}>
      <NeedsYouCard row={row} />
    </QueryClientProvider>,
  );
}

afterEach(() => {
  vi.unstubAllGlobals();
});

describe("NeedsYouCard inline answer (S36)", () => {
  it("expands the S24 question components on the card and answers without navigating", async () => {
    const calls: Array<{ url: string; method: string; body: unknown }> = [];
    vi.stubGlobal(
      "fetch",
      vi.fn(async (url: string, init?: RequestInit) => {
        calls.push({
          url: String(url),
          method: init?.method ?? "GET",
          body: init?.body !== undefined ? JSON.parse(String(init.body)) : undefined,
        });
        if (String(url).endsWith(`/runs/${RUN_ID}/activities`)) {
          return jsonResponse(activities);
        }
        if (String(url).endsWith(`/runs/${RUN_ID}`)) return jsonResponse(detail);
        if (String(url).endsWith(`/elicitations/${EL_ID}/respond`)) {
          return jsonResponse({ elicitation: { ...detail.elicitations[0], state: "answered" } });
        }
        throw new Error(`unexpected fetch: ${String(url)}`);
      }),
    );

    renderCard(questionRow);

    // The card, collapsed: flavor in words, one primary action.
    expect(screen.getByText(/Answer a question/)).toBeTruthy();
    fireEvent.click(screen.getByRole("button", { name: "Answer" }));

    // The S24 QuestionRow renders ON the card: question text and its option cards.
    await waitFor(() => {
      expect(screen.getByText("Which retry strategy?")).toBeTruthy();
    });
    expect(screen.getByText("Exponential backoff")).toBeTruthy();
    expect(screen.getByText("Fixed interval")).toBeTruthy();

    // Answering posts to the respond endpoint — no navigation happened (the router is
    // mocked; nothing here can change the URL).
    fireEvent.click(screen.getByText("Exponential backoff"));
    const submit = screen
      .getAllByRole("button", { name: "Answer" })
      .find((b) => b.className.includes("answerButton"));
    expect(submit).toBeTruthy();
    fireEvent.click(submit!);

    await waitFor(() => {
      const respond = calls.find((c) => c.url.endsWith(`/elicitations/${EL_ID}/respond`));
      expect(respond).toBeTruthy();
      expect(respond!.method).toBe("POST");
      expect(respond!.body).toEqual({
        action: "answer",
        answers: { "Which retry strategy?": ["Exponential backoff"] },
      });
    });
  });

  it("a pull-request row's action is a link to the PR, with a run link beside it", () => {
    vi.stubGlobal("fetch", vi.fn());
    renderCard({
      ...questionRow,
      kind: "pull_request",
      id: "out-1",
      run_id: RUN_ID,
      flavor: "review",
      status: "open",
      pr_number: 212,
      url: "https://github.com/acme/payments/pull/212",
    });
    const pr = screen.getByRole("link", { name: "Review PR" });
    expect(pr.getAttribute("href")).toBe("https://github.com/acme/payments/pull/212");
    expect(screen.getByText("Dev opened PR #212")).toBeTruthy();
  });
});
