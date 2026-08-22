/*
 * S38 acceptance: the ⌘J ask-an-agent palette. It registers mod+j in the keyboard registry
 * (so the cheatsheet and the ⌘K palette list it), and its flow — pick an agent, type a
 * prompt, submit — POSTs the free-floating run (D-15) to /projects/{key}/runs and jumps to
 * the created run.
 */
import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { afterEach, describe, expect, it, vi } from "vitest";

const navigateSpy = vi.fn();
vi.mock("@tanstack/react-router", () => ({
  useNavigate: () => navigateSpy,
}));

import { keyboard } from "../../lib/keyboard/registry";
import { useUIStore } from "../../stores/ui";
import { AskAgentPalette } from "./AskAgentPalette";

const agents = {
  agents: [
    {
      id: "agent-1",
      project_id: "p1",
      name: "Dev",
      role: "implementer",
      color: "#888888",
      runtime_id: "claude-code",
      model: "claude-sonnet-5",
      effort: "medium",
      autonomy: "auto",
      permissions: {},
      git_author_name: "Dev",
      git_author_email: "dev@example.com",
      forge_login: null,
      concurrency_cap: 1,
      daily_cap_cents: null,
      enabled: true,
      archived_at: null,
    },
    {
      id: "agent-2",
      project_id: "p1",
      name: "Reviewer",
      role: "review",
      color: "#333333",
      enabled: false, // ineligible — must not be listed
      archived_at: null,
    },
  ],
};

function jsonResponse(body: unknown, status = 200): Response {
  return new Response(JSON.stringify(body), {
    status,
    headers: { "Content-Type": "application/json" },
  });
}

function renderPalette(projectKey: string | undefined) {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return render(
    <QueryClientProvider client={qc}>
      <AskAgentPalette projectKey={projectKey} />
    </QueryClientProvider>,
  );
}

afterEach(() => {
  vi.unstubAllGlobals();
  navigateSpy.mockReset();
  useUIStore.setState({ askAgentOpen: false });
});

describe("AskAgentPalette (S38, UI spec §6)", () => {
  it("registers mod+j in the keyboard registry, gated on a project being open", () => {
    const { unmount } = renderPalette("PAY");
    const binding = keyboard.getBindings().find((b) => b.id === "shell.ask-agent");
    expect(binding).toBeDefined();
    expect(binding?.chord).toBe("mod+j");
    expect(binding?.title).toBe("Ask an agent");
    expect(binding?.palette).toBe(true);
    expect(binding?.enabled?.()).toBe(true);
    unmount();
    expect(keyboard.getBindings().some((b) => b.id === "shell.ask-agent")).toBe(false);
  });

  it("is disabled outside a project", () => {
    renderPalette(undefined);
    const binding = keyboard.getBindings().find((b) => b.id === "shell.ask-agent");
    expect(binding?.enabled?.()).toBe(false);
  });

  it("lists eligible agents; picking one and submitting a prompt creates a free run", async () => {
    const calls: Array<{ url: string; method: string; body: unknown }> = [];
    vi.stubGlobal(
      "fetch",
      vi.fn(async (url: string, init?: RequestInit) => {
        const u = String(url);
        calls.push({
          url: u,
          method: init?.method ?? "GET",
          body: init?.body !== undefined ? JSON.parse(String(init.body)) : undefined,
        });
        if (u.endsWith("/projects/PAY/agents") && (init?.method ?? "GET") === "GET") {
          return jsonResponse(agents);
        }
        if (u.endsWith("/projects/PAY/runs") && init?.method === "POST") {
          return jsonResponse({ run_id: "run-9" }, 201);
        }
        throw new Error(`unexpected fetch: ${u}`);
      }),
    );

    renderPalette("PAY");
    useUIStore.getState().setAskAgentOpen(true);

    // Eligible agents only: Dev appears, the disabled Reviewer does not.
    const dev = await screen.findByText("Dev");
    expect(screen.queryByText("Reviewer")).toBeNull();

    fireEvent.click(dev);
    const prompt = await screen.findByLabelText("Prompt");
    fireEvent.change(prompt, { target: { value: "Summarize open PRs" } });
    fireEvent.click(screen.getByText("Start run"));

    await waitFor(() => {
      const post = calls.find((c) => c.method === "POST");
      expect(post).toBeDefined();
      expect(post?.url.endsWith("/api/v1/projects/PAY/runs")).toBe(true);
      expect(post?.body).toEqual({ agent_id: "agent-1", prompt: "Summarize open PRs" });
    });
    // The palette closes and jumps to the created run.
    await waitFor(() => {
      expect(useUIStore.getState().askAgentOpen).toBe(false);
      expect(navigateSpy).toHaveBeenCalledWith({
        to: "/p/$key/runs/$id",
        params: { key: "PAY", id: "run-9" },
      });
    });
  });
});
