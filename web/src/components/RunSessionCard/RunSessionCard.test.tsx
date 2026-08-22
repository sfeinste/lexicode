/*
 * RunSessionCard with fixture data (S12): collapsed shows agent, status, elapsed, cost and
 * the current step; expanding shows the activities inline plus the "Open full run" link.
 * Nothing live renders through this component until S23 writes kind='run' stream rows —
 * the fixture here is the component's contract, not page data.
 */
import { fireEvent, render, screen } from "@testing-library/react";
import { describe, expect, it } from "vitest";

import { RunSessionCard, type RunSessionData } from "./RunSessionCard";

const FIXTURE: RunSessionData = {
  id: "run-1",
  agent: "dev",
  status: "running",
  elapsedMs: 318_000,
  costUsd: 1.42,
  currentStep: "editing src/api/charge.ts",
  runHref: "/p/PAY/runs/run-1",
  activities: [
    { id: "a1", type: "thought", text: "Reading the failing test first." },
    { id: "a2", type: "action", text: "npm test — 2 failing" },
    { id: "a3", type: "elicitation", text: "Should retries be idempotent per charge id?" },
  ],
};

describe("RunSessionCard", () => {
  it("collapsed: agent, status, elapsed, cost and current step; no activities", () => {
    render(<RunSessionCard run={FIXTURE} />);
    expect(screen.getByText("dev")).toBeTruthy();
    expect(screen.getByText("Running")).toBeTruthy(); // StatusDot's §4 vocabulary
    expect(screen.getByText("5m 18s")).toBeTruthy();
    expect(screen.getByText(/\$1\.42/)).toBeTruthy();
    expect(screen.getByText("editing src/api/charge.ts")).toBeTruthy();
    expect(screen.queryByText("npm test — 2 failing")).toBeNull();
    expect(screen.queryByText(/Open full run/)).toBeNull();
  });

  it("expanded: activities inline and the open-full-run link; collapses again", () => {
    render(<RunSessionCard run={FIXTURE} />);
    const head = screen.getByRole("button", { expanded: false });
    fireEvent.click(head);

    expect(screen.getByText("Reading the failing test first.")).toBeTruthy();
    expect(screen.getByText("npm test — 2 failing")).toBeTruthy();
    expect(screen.getByText("Should retries be idempotent per charge id?")).toBeTruthy();
    const link = screen.getByRole("link", { name: /Open full run/ });
    expect(link.getAttribute("href")).toBe("/p/PAY/runs/run-1");

    fireEvent.click(screen.getByRole("button", { expanded: true }));
    expect(screen.queryByText("npm test — 2 failing")).toBeNull();
  });
});
