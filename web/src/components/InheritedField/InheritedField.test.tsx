/*
 * S08: the three states of the inheritance control (UI spec §5.11) — inherited (spec wording
 * verbatim, Override affordance), overridden (Reset affordance), and inherited-following-a-
 * workspace-change (the line always shows the LIVE workspace value, never a frozen copy).
 */
import { fireEvent, render, screen } from "@testing-library/react";
import { describe, expect, it, vi } from "vitest";

import { InheritedField } from "./InheritedField";

function subject(overrides: Partial<Parameters<typeof InheritedField>[0]> = {}) {
  const onOverride = vi.fn();
  const onReset = vi.fn();
  const utils = render(
    <InheritedField
      label="Daily budget"
      inherited
      workspaceValue="$20.00"
      onOverride={onOverride}
      onReset={onReset}
      {...overrides}
    >
      <input aria-label="Daily budget input" />
    </InheritedField>,
  );
  return { onOverride, onReset, ...utils };
}

describe("InheritedField", () => {
  it("inherited: renders the spec wording with the workspace value and fires onOverride", () => {
    const { onOverride } = subject();
    // The line is split across elements; assert the pieces and the exact button label.
    expect(screen.getByText(/Inherited from workspace:/)).toBeTruthy();
    expect(screen.getByText("$20.00")).toBeTruthy();
    const override = screen.getByRole("button", { name: "Override." });
    fireEvent.click(override);
    expect(onOverride).toHaveBeenCalledTimes(1);
    expect(screen.queryByRole("button", { name: "Reset to workspace default." })).toBeNull();
  });

  it("overridden: renders Reset to workspace default. and fires onReset", () => {
    const { onReset } = subject({ inherited: false });
    expect(screen.queryByText(/Inherited from workspace:/)).toBeNull();
    const reset = screen.getByRole("button", { name: "Reset to workspace default." });
    fireEvent.click(reset);
    expect(onReset).toHaveBeenCalledTimes(1);
  });

  it("inherited: follows a live workspace value change (never a frozen copy)", () => {
    const { rerender, onOverride, onReset } = subject();
    expect(screen.getByText("$20.00")).toBeTruthy();
    rerender(
      <InheritedField
        label="Daily budget"
        inherited
        workspaceValue="$7.50"
        onOverride={onOverride}
        onReset={onReset}
      >
        <input aria-label="Daily budget input" />
      </InheritedField>,
    );
    expect(screen.queryByText("$20.00")).toBeNull();
    expect(screen.getByText("$7.50")).toBeTruthy();
  });
});
