/*
 * S34 acceptance for the meter: amber exactly when the always-on total exceeds the
 * threshold, with the spec's advice rendered verbatim in that state — and ONE component:
 * the wiki tree, the Agents tab and the run detail all import this file, asserted
 * structurally so a copy-paste reimplementation on any surface fails the build.
 */
import { render, screen } from "@testing-library/react";
import { readFileSync } from "node:fs";
import { join } from "node:path";
import { describe, expect, it } from "vitest";

import { CONTEXT_BUDGET_ADVICE, isOverBudget } from "./budget";
import { ContextMeter } from "./ContextMeter";

describe("isOverBudget", () => {
  it("flags only past the threshold", () => {
    expect(isOverBudget(3999, 4000)).toBe(false);
    expect(isOverBudget(4000, 4000)).toBe(false); // at the limit is not over
    expect(isOverBudget(4001, 4000)).toBe(true);
  });

  it("never flags without a positive threshold", () => {
    expect(isOverBudget(99999, 0)).toBe(false);
  });
});

describe("ContextMeter", () => {
  it("renders quietly under budget — no advice, no amber", () => {
    const { container } = render(
      <ContextMeter alwaysTokens={1200} thresholdTokens={4000} pageCount={3} />,
    );
    const meter = container.querySelector('[aria-label="Context budget"]');
    expect(meter).not.toBeNull();
    expect(meter?.getAttribute("data-over")).toBeNull();
    expect(screen.queryByText(new RegExp(CONTEXT_BUDGET_ADVICE))).toBeNull();
    expect(screen.getByText(/3 pages/)).toBeTruthy();
  });

  it("goes amber past the threshold with the advice verbatim", () => {
    const { container } = render(
      <ContextMeter alwaysTokens={5200} thresholdTokens={4000} pageCount={4} />,
    );
    const meter = container.querySelector('[aria-label="Context budget"]');
    expect(meter?.getAttribute("data-over")).toBe("true");
    // The exact copy from the implementation plan, inline.
    expect(
      screen.getByText(
        /cut what the agent can read from the code; keep pitfalls, rationale, and conventions that differ from defaults/,
      ),
    ).toBeTruthy();
  });
});

describe("one component, three surfaces", () => {
  // The brief's #1 differentiator must not be reimplemented per surface (architecture
  // §11). Each surface must import THIS ContextMeter — asserted against the sources.
  const surfaces = [
    "routes/project/wiki/WikiScreen.tsx",
    "routes/project/agents/AgentsPage.tsx",
    "routes/project/runs/RunDetailPage.tsx",
  ];
  const src = (rel: string) =>
    readFileSync(join(__dirname, "..", "..", rel), "utf8");

  it.each(surfaces)("%s imports the shared ContextMeter", (rel) => {
    const text = src(rel);
    expect(text).toMatch(
      /import \{ ContextMeter \} from "(\.\.\/)+components\/ContextMeter\/ContextMeter"/,
    );
    expect(text).toContain("<ContextMeter");
  });
});
