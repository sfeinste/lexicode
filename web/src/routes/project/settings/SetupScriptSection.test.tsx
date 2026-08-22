/*
 * The setup script field: the stored script renders, an edit autosaves over
 * PATCH /projects/{key}/repo, and clearing the box saves the empty string — the state that
 * makes provisioning skip the step entirely rather than run an empty shell.
 *
 * The helper text is asserted too, because it is the only place a user can learn that the
 * script runs on every run, that its output is a checklist step, and that a non-zero exit
 * fails the run before the agent starts.
 */
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";

import type { Repo } from "../../../lib/api/client";

import { SetupScriptField, SetupScriptSection } from "./SetupScriptSection";

function repoWith(script: string): Repo {
  return {
    provider: "github",
    owner: "acme",
    name: "payments",
    default_branch: "main",
    head_sha: null,
    head_message: null,
    connected_at: null,
    last_synced_at: null,
    has_token: true,
    setup_script: script,
    branch_template: null,
    network_policy: "open",
    network_allowlist: [],
    workspace_network_policy: "open",
  };
}

afterEach(() => {
  vi.unstubAllGlobals();
  vi.useRealTimers();
});

describe("SetupScriptField", () => {
  it("renders the stored script and reports every edit", () => {
    const onChange = vi.fn();
    render(<SetupScriptField script="apt-get install -y python3" onChange={onChange} />);
    const box = screen.getByLabelText("Setup script") as HTMLTextAreaElement;
    expect(box.value).toBe("apt-get install -y python3");

    fireEvent.change(box, { target: { value: "apt-get install -y python3 chromium" } });
    expect(onChange).toHaveBeenCalledWith("apt-get install -y python3 chromium");
  });

  it("states the three things the screen is the only place to learn", () => {
    const { container } = render(<SetupScriptField script="" onChange={vi.fn()} />);
    const prose = container.textContent ?? "";
    // 1. Every run, before the agent starts.
    expect(prose).toContain("on every run, after the clone and before the agent starts");
    // 2. Its output is a step in the run's provisioning checklist.
    expect(prose).toContain(
      "setup script step in that run’s provisioning checklist, with its output in the run’s verbose activities",
    );
    // 3. A non-zero exit fails the run, with the script's output in the failure message.
    expect(prose).toContain(
      "A non-zero exit fails the run there, with the script’s output in the failure message",
    );
    // And the two things it must not oversell.
    expect(prose).toContain("An empty script is skipped entirely.");
    expect(prose).toContain("Nothing it installs is cached");
  });
});

describe("SetupScriptSection (wired)", () => {
  function renderWired(script: string) {
    const calls: Array<{ method: string; url: string; body: unknown }> = [];
    vi.stubGlobal(
      "fetch",
      vi.fn((url: string, init: RequestInit) => {
        calls.push({
          method: init.method ?? "GET",
          url,
          body: init.body ? JSON.parse(String(init.body)) : undefined,
        });
        return Promise.resolve(
          new Response(JSON.stringify(repoWith(script)), {
            status: 200,
            headers: { "Content-Type": "application/json" },
          }),
        );
      }),
    );
    const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    const utils = render(
      <QueryClientProvider client={qc}>
        <SetupScriptSection projectKey="PAY" repo={repoWith(script)} />
      </QueryClientProvider>,
    );
    return { calls, ...utils };
  }

  it("autosaves an edit to PATCH /projects/{key}/repo", async () => {
    vi.useFakeTimers();
    const { calls } = renderWired("");
    fireEvent.change(screen.getByLabelText("Setup script"), {
      target: { value: "apt-get update && apt-get install -y python3" },
    });
    // Debounced: nothing has gone out yet.
    expect(calls).toHaveLength(0);
    await vi.advanceTimersByTimeAsync(700);

    expect(calls).toHaveLength(1);
    expect(calls[0].method).toBe("PATCH");
    expect(calls[0].url).toBe("/api/v1/projects/PAY/repo");
    expect(calls[0].body).toEqual({
      setup_script: "apt-get update && apt-get install -y python3",
    });
  });

  it("clearing the box saves the empty string, not nothing", async () => {
    vi.useFakeTimers();
    const { calls } = renderWired("apt-get install -y python3");
    fireEvent.change(screen.getByLabelText("Setup script"), { target: { value: "" } });
    await vi.advanceTimersByTimeAsync(700);

    expect(calls).toHaveLength(1);
    expect(calls[0].body).toEqual({ setup_script: "" });
  });

  it("shows the save indicator once the write lands", async () => {
    renderWired("");
    fireEvent.change(screen.getByLabelText("Setup script"), { target: { value: "echo hi" } });
    await waitFor(() => expect(screen.getByText("Saved")).toBeTruthy(), { timeout: 3000 });
  });
});
