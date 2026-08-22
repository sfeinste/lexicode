/*
 * S29 acceptance: the loop panel cannot be fully disabled without an explicit per-layer
 * action — five layers, five individual toggles, no "disable all" — with the spec table's
 * plain-language descriptions verbatim; and a rule card with zero firings renders (empty
 * sparkline, "never fired").
 */
import { fireEvent, render, screen } from "@testing-library/react";
import { useState } from "react";
import { describe, expect, it } from "vitest";

import type { LoopConfig } from "../../../lib/api/client";
import { DEFAULT_LOOP_CONFIG } from "./loopDefaults";
import { LoopPanel } from "./LoopPanel";
import { TriggerCard } from "./TriggerCard";
import { GITHUB_CATALOG, makeTrigger } from "./testFixtures";

const LAYERS = [
  "Actor suppression",
  "Debounce",
  "Cancel in progress",
  "Depth limit",
  "Budget ceiling",
];

const DESCRIPTIONS = [
  "Ignore events caused by this agent's own identity",
  "Collapse a burst of pushes into one run",
  "A new push supersedes the running review",
  "Stop after 3 agent-caused re-triggers on the same PR",
  "Stop when this rule has spent $X today",
];

function Harness({ onValue }: { onValue?: (v: LoopConfig) => void }) {
  const [value, setValue] = useState<LoopConfig>({ ...DEFAULT_LOOP_CONFIG });
  return (
    <LoopPanel
      value={value}
      onChange={(v) => {
        setValue(v);
        onValue?.(v);
      }}
    />
  );
}

describe("loop protection panel", () => {
  it("renders one row per layer with the spec's plain-language description verbatim", () => {
    render(<Harness />);
    for (const name of LAYERS) {
      expect(screen.getByLabelText(name)).toBeTruthy();
    }
    for (const description of DESCRIPTIONS) {
      expect(screen.getByText(`“${description}”`)).toBeTruthy();
    }
  });

  it("defaults on: actor, debounce 90s, cancel, depth 3; budget inherits the project", () => {
    render(<Harness />);
    expect((screen.getByLabelText("Actor suppression") as HTMLInputElement).checked).toBe(true);
    expect((screen.getByLabelText("Debounce") as HTMLInputElement).checked).toBe(true);
    expect(
      (screen.getByLabelText("Debounce window in seconds") as HTMLInputElement).value,
    ).toBe("90");
    expect((screen.getByLabelText("Cancel in progress") as HTMLInputElement).checked).toBe(true);
    expect((screen.getByLabelText("Depth limit") as HTMLInputElement).checked).toBe(true);
    expect(screen.getByText("inherits the project ceiling")).toBeTruthy();
  });

  it("toggles each layer individually, mapping to the §6.1 loop_config shape", () => {
    let last: LoopConfig | undefined;
    render(<Harness onValue={(v) => (last = v)} />);

    fireEvent.click(screen.getByLabelText("Actor suppression"));
    expect(last?.actor_suppression).toBe(false);

    fireEvent.click(screen.getByLabelText("Debounce"));
    expect(last?.debounce_seconds).toBe(0);

    fireEvent.click(screen.getByLabelText("Cancel in progress"));
    expect(last?.cancel_in_progress).toBe(false);

    fireEvent.click(screen.getByLabelText("Depth limit"));
    expect(last?.depth_limit).toBe(0);

    fireEvent.click(screen.getByLabelText("Budget ceiling"));
    expect(last?.daily_budget_cents).toBe(500);

    // Every other layer's toggle was independent — turning one off touched only it.
    expect(last?.actor_suppression).toBe(false);
    expect(last?.debounce_seconds).toBe(0);
  });

  it("has NO disable-all control — five individual toggles and nothing else", () => {
    render(<Harness />);
    const checkboxes = screen.getAllByRole("checkbox");
    expect(checkboxes).toHaveLength(LAYERS.length);
    expect(screen.queryByText(/disable all|turn (all|everything) off|all off/i)).toBeNull();
  });
});

describe("rule card with zero firings", () => {
  it("renders the prose lines, an empty sparkline and the never-fired state", () => {
    const trigger = makeTrigger(); // health: {counts: {}, last_fired_at: null, recent: []}
    render(
      <TriggerCard
        trigger={trigger}
        catalog={GITHUB_CATALOG}
        agentNames={new Map([["a1", "Reviewer"]])}
        onToggle={() => {}}
      />,
    );
    expect(screen.getByText("WHEN")).toBeTruthy();
    expect(screen.getByText("IF")).toBeTruthy();
    expect(screen.getByText("THEN")).toBeTruthy();
    expect(screen.getByText("Never fired")).toBeTruthy();
    expect(screen.getAllByText("never fired").length).toBeGreaterThan(0);
    expect(screen.getByLabelText("No firings yet")).toBeTruthy();
    expect(screen.getByText("Ignores events caused by @Reviewer")).toBeTruthy();
  });

  it("mutes a disabled rule and reports its toggle state", () => {
    const trigger = makeTrigger({ enabled: false });
    const { container } = render(
      <TriggerCard
        trigger={trigger}
        catalog={GITHUB_CATALOG}
        agentNames={new Map()}
        onToggle={() => {}}
      />,
    );
    const card = container.querySelector("article");
    expect(card?.getAttribute("data-disabled")).toBe("true");
    expect((screen.getByLabelText("Review new PRs enabled") as HTMLInputElement).checked).toBe(
      false,
    );
  });
});
